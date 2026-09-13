package main

import (
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// Market recovery для B_price_trap.
// Live (marketRecoveryLiveEnabled): winner = corridor_price_up_market_recovery, +1 step/cycle.
// Не использует p10+nacenka. Книга — 60m (ahBookMarketRecoveryStats), не 10m ah_book.
// Остальные UP/DOWN / nacenka / v8s skim — не трогаем.

const (
	marketRecoveryLiveEnabled  = false // Sep 2026: книга наебывает — B_price_trap ↑ выкл
	marketRecoveryActionLive   = "corridor_price_up_market_recovery"
	marketRecoveryActionShadow = "corridor_price_up_market_recovery_shadow"
	marketRecoveryBookWindow   = 60 * time.Minute
	marketRecoveryMinSellers   = 15
	marketRecoveryMinUUID      = 25
	marketRecoveryRatioMax     = 0.80 // our/p10 ≤ 0.80
	marketRecoveryGapMinSteps  = 4    // (p10-our)/step ≥ 4
	marketRecoveryBuyableFrac  = 0.85 // stop when our ≥ 0.85*p10
	marketRecoveryP10StablePct = 0.10 // p10 в пределах ±10% на 2–3 obs
	marketRecoveryRecentDownN  = 3    // нет price_down в последних N циклах
	marketRecoveryP10HistN     = 3
	marketRecoveryPriorHistN   = 6 // для исключения C_sold_out / D_thru / dead-idle
)

type marketRecoveryShadowState struct {
	Active           bool
	PredictedPrice   int
	RecoveryI        int // сколько UP-шагов уже «сделали» в сессии
	SessionStartedAt time.Time
	LastStepAt       time.Time
	PendingRowIDs    []int64 // строки прошлого цикла → next_cycle_* / first buy
	P10Hist          []int
	RecentActions    []string // последние analysis actions (winner)
	PriorHeld        []int
	PriorBuys        []int
	PriorSales       []int
	// После stop из‑за buyable/gap — не перезапускать сразу на том же real our.
	ExhaustedUntilRealChange bool
	ExhaustedAtOur           int
}

var (
	mrShadowMu    sync.Mutex
	mrShadowState = map[string]*marketRecoveryShadowState{}
)

func initMarketRecoveryShadowTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS market_recovery_shadow (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	action TEXT NOT NULL,
	our_price INTEGER NOT NULL,
	p10 INTEGER NOT NULL,
	min_ask INTEGER NOT NULL,
	unique_sellers_noban INTEGER NOT NULL,
	uuid_noban INTEGER NOT NULL,
	gap_steps REAL NOT NULL,
	our_over_p10 REAL NOT NULL,
	step INTEGER NOT NULL,
	recovery_i INTEGER NOT NULL,
	held INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	sales INTEGER NOT NULL,
	predicted_price INTEGER NOT NULL,
	stop_reason TEXT,
	next_cycle_buys INTEGER,
	next_cycle_held INTEGER,
	next_cycle_sales INTEGER,
	cycles_to_first_buy INTEGER,
	ups_to_first_buy INTEGER,
	winner_action TEXT,
	session_active INTEGER NOT NULL
)`)
	if err != nil {
		log.Printf("[market_recovery_shadow] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_mrs_ts ON market_recovery_shadow(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_mrs_item_ts ON market_recovery_shadow(item_id, ts)`)
}

func mrShadowGet(item string) *marketRecoveryShadowState {
	st, ok := mrShadowState[item]
	if !ok {
		st = &marketRecoveryShadowState{}
		mrShadowState[item] = st
	}
	return st
}

// marketRecoveryGapOK — глубокая яма: our/p10 ≤ 0.80 ИЛИ (p10-our)/step ≥ 4.
// p10 — сырой sell-side reference, без nacenka.
func marketRecoveryGapOK(our, p10, step int) bool {
	if our <= 0 || p10 <= 0 || step <= 0 {
		return false
	}
	if float64(our)/float64(p10) <= marketRecoveryRatioMax {
		return true
	}
	gapSteps := float64(p10-our) / float64(step)
	return gapSteps >= float64(marketRecoveryGapMinSteps)
}

func marketRecoveryTrustOK(nSell, nUUID int) bool {
	return nSell >= marketRecoveryMinSellers && nUUID >= marketRecoveryMinUUID
}

func marketRecoveryBuyableReached(our, p10 int) bool {
	if our <= 0 || p10 <= 0 {
		return false
	}
	return float64(our) >= marketRecoveryBuyableFrac*float64(p10)
}

// marketRecoveryP10Stable — последние 2–3 наблюдения p10 в пределах ±10% от медианы/среднего окна.
func marketRecoveryP10Stable(hist []int) bool {
	if len(hist) < 2 {
		// недостаточно истории — не блокируем старт (холодный старт SKU)
		return true
	}
	n := len(hist)
	if n > marketRecoveryP10HistN {
		hist = hist[n-marketRecoveryP10HistN:]
	}
	sum := 0
	for _, v := range hist {
		if v <= 0 {
			return false
		}
		sum += v
	}
	avg := float64(sum) / float64(len(hist))
	if avg <= 0 {
		return false
	}
	for _, v := range hist {
		if math.Abs(float64(v)-avg)/avg > marketRecoveryP10StablePct {
			return false
		}
	}
	return true
}

func marketRecoveryHadPriceDown(actions []string, n int) bool {
	if n <= 0 || len(actions) == 0 {
		return false
	}
	start := 0
	if len(actions) > n {
		start = len(actions) - n
	}
	for _, a := range actions[start:] {
		if strings.Contains(a, "price_down") {
			return true
		}
	}
	return false
}

// marketRecoveryLooksSoldOutOrThru — C_sold_out / D_thru: недавно был held/sales/buys при пустом сейчас.
func marketRecoveryLooksSoldOutOrThru(held, buys, sales int, priorHeld, priorBuys, priorSales []int) bool {
	if held != 0 || buys != 0 || sales != 0 {
		return false
	}
	for i := 0; i < len(priorHeld); i++ {
		h, b, s := 0, 0, 0
		if i < len(priorHeld) {
			h = priorHeld[i]
		}
		if i < len(priorBuys) {
			b = priorBuys[i]
		}
		if i < len(priorSales) {
			s = priorSales[i]
		}
		if h > 0 || b > 0 || s > 0 {
			return true
		}
	}
	return false
}

// marketRecoveryLooksDeadIdle — нет доверия к книге / нет рынка: не B.
// (trusted book уже отфильтрован отдельно; здесь — явный dead без книги.)
func marketRecoveryLooksDeadIdle(bookOK bool, nSell, nUUID int) bool {
	if !bookOK {
		return true
	}
	return !marketRecoveryTrustOK(nSell, nUUID)
}

type marketRecoveryEvalIn struct {
	Item          string
	Now           time.Time
	OurPrice      int
	Step          int
	Held          int
	Buys          int
	Sales         int
	WinnerAction  string
	ManualLock    bool // manual min/max lock (любой clamp в окне)
	Book          ahBookMarketRecoverySnap
}

type marketRecoveryEvalOut struct {
	DidStep         bool
	PredictedPrice  int
	RecoveryI       int
	StopReason      string
	SessionActive   bool
	GapSteps        float64
	OurOverP10      float64
	Logged          bool
}

func appendBoundedInt(hist []int, v, max int) []int {
	hist = append(hist, v)
	if len(hist) > max {
		hist = hist[len(hist)-max:]
	}
	return hist
}

func appendBoundedStr(hist []string, v string, max int) []string {
	hist = append(hist, v)
	if len(hist) > max {
		hist = hist[len(hist)-max:]
	}
	return hist
}

func marketRecoveryStopReason(in marketRecoveryEvalIn, predicted int, active bool) string {
	if in.Buys > 0 {
		return "buys"
	}
	if in.Held > 0 {
		return "held"
	}
	if in.ManualLock {
		return "manual_lock"
	}
	if marketRecoveryHadPriceDown([]string{in.WinnerAction}, 1) {
		return "price_down"
	}
	if !in.Book.OK || !marketRecoveryTrustOK(in.Book.NSell, in.Book.NUUID) {
		return "trust_lost"
	}
	if in.Book.P10 <= 0 {
		return "trust_lost"
	}
	checkPrice := in.OurPrice
	if active && predicted > 0 {
		checkPrice = predicted
	}
	if marketRecoveryBuyableReached(checkPrice, in.Book.P10) {
		return "buyable_zone"
	}
	if !marketRecoveryGapOK(checkPrice, in.Book.P10, in.Step) {
		return "gap_closed"
	}
	return ""
}

// isBPriceTrap — чистая классификация B (без session state).
func isBPriceTrap(in marketRecoveryEvalIn, p10Hist []int, recentActions []string, priorHeld, priorBuys, priorSales []int) bool {
	if in.Held != 0 || in.Buys != 0 || in.Sales != 0 {
		return false
	}
	if in.ManualLock {
		return false
	}
	if in.Step <= 0 || in.OurPrice <= 0 {
		return false
	}
	if !in.Book.OK || !marketRecoveryTrustOK(in.Book.NSell, in.Book.NUUID) {
		return false
	}
	if marketRecoveryLooksDeadIdle(in.Book.OK, in.Book.NSell, in.Book.NUUID) {
		return false
	}
	if marketRecoveryLooksSoldOutOrThru(in.Held, in.Buys, in.Sales, priorHeld, priorBuys, priorSales) {
		return false
	}
	if !marketRecoveryGapOK(in.OurPrice, in.Book.P10, in.Step) {
		return false
	}
	if marketRecoveryHadPriceDown(recentActions, marketRecoveryRecentDownN) {
		return false
	}
	if !marketRecoveryP10Stable(p10Hist) {
		return false
	}
	if marketRecoveryBuyableReached(in.OurPrice, in.Book.P10) {
		return false
	}
	return true
}

// evaluateMarketRecoveryShadow — один analysis cycle. Не трогает реальную цену.
func evaluateMarketRecoveryShadow(in marketRecoveryEvalIn) marketRecoveryEvalOut {
	var out marketRecoveryEvalOut
	if strings.TrimSpace(in.Item) == "" || in.Step <= 0 {
		return out
	}

	mrShadowMu.Lock()
	defer mrShadowMu.Unlock()
	st := mrShadowGet(in.Item)

	// Backfill строк прошлого цикла: next_cycle_* и (при buys) cycles/ups to first buy.
	if len(st.PendingRowIDs) > 0 {
		upsAtBuy := st.RecoveryI
		for _, id := range st.PendingRowIDs {
			updateMarketRecoveryShadowNextCycle(id, in.Buys, in.Held, in.Sales, upsAtBuy)
		}
		st.PendingRowIDs = nil
	}

	st.RecentActions = appendBoundedStr(st.RecentActions, in.WinnerAction, marketRecoveryRecentDownN+2)
	st.PriorHeld = appendBoundedInt(st.PriorHeld, in.Held, marketRecoveryPriorHistN)
	st.PriorBuys = appendBoundedInt(st.PriorBuys, in.Buys, marketRecoveryPriorHistN)
	st.PriorSales = appendBoundedInt(st.PriorSales, in.Sales, marketRecoveryPriorHistN)
	if in.Book.OK && in.Book.P10 > 0 {
		st.P10Hist = appendBoundedInt(st.P10Hist, in.Book.P10, marketRecoveryP10HistN)
	}

	p10 := in.Book.P10
	if p10 > 0 && in.OurPrice > 0 {
		out.OurOverP10 = float64(in.OurPrice) / float64(p10)
		out.GapSteps = float64(p10-in.OurPrice) / float64(in.Step)
	}

	stop := marketRecoveryStopReason(in, st.PredictedPrice, st.Active)

	// Активная сессия: stop или +1 step.
	if st.Active {
		if stop != "" {
			out.StopReason = stop
			out.PredictedPrice = st.PredictedPrice
			out.RecoveryI = st.RecoveryI
			out.SessionActive = false
			logMarketRecoveryShadowRow(in, st, out, false)
			out.Logged = true
			if stop == "buyable_zone" || stop == "gap_closed" {
				st.ExhaustedUntilRealChange = true
				st.ExhaustedAtOur = in.OurPrice
			}
			st.Active = false
			st.PredictedPrice = 0
			st.RecoveryI = 0
			return out
		}
		// +1 step на цикл (cooldown = 1 cycle между шагами = сам цикл)
		st.PredictedPrice += in.Step
		st.RecoveryI++
		st.LastStepAt = in.Now
		out.DidStep = true
		out.PredictedPrice = st.PredictedPrice
		out.RecoveryI = st.RecoveryI
		out.SessionActive = true
		// после шага — если уже buyable/gap closed на predicted, пометим stop в логе но оставим active до след. цикла?
		// Спека: stop если our>=0.85*p10 — проверяем после шага.
		if post := marketRecoveryStopReason(in, st.PredictedPrice, true); post == "buyable_zone" || post == "gap_closed" {
			out.StopReason = post
			logMarketRecoveryShadowRow(in, st, out, true)
			out.Logged = true
			st.ExhaustedUntilRealChange = true
			st.ExhaustedAtOur = in.OurPrice
			st.Active = false
			st.PredictedPrice = 0
			st.RecoveryI = 0
			out.SessionActive = false
			return out
		}
		logMarketRecoveryShadowRow(in, st, out, true)
		out.Logged = true
		return out
	}

	// Неактивны: возможно старт новой сессии.
	if st.ExhaustedUntilRealChange && in.OurPrice == st.ExhaustedAtOur {
		return out
	}
	if in.OurPrice != st.ExhaustedAtOur {
		st.ExhaustedUntilRealChange = false
		st.ExhaustedAtOur = 0
	}

	// Не стартуем, если уже stop по текущим real метрикам (buys/held/down/lock/trust).
	if stop != "" && stop != "buyable_zone" && stop != "gap_closed" {
		return out
	}

	if !isBPriceTrap(in, st.P10Hist, st.RecentActions, trimPriorExcludeCurrent(st.PriorHeld), trimPriorExcludeCurrent(st.PriorBuys), trimPriorExcludeCurrent(st.PriorSales)) {
		return out
	}

	st.Active = true
	st.PredictedPrice = in.OurPrice + in.Step
	st.RecoveryI = 1
	st.SessionStartedAt = in.Now
	st.LastStepAt = in.Now
	st.ExhaustedUntilRealChange = false
	out.DidStep = true
	out.PredictedPrice = st.PredictedPrice
	out.RecoveryI = st.RecoveryI
	out.SessionActive = true
	if post := marketRecoveryStopReason(in, st.PredictedPrice, true); post == "buyable_zone" || post == "gap_closed" {
		out.StopReason = post
		logMarketRecoveryShadowRow(in, st, out, true)
		out.Logged = true
		st.ExhaustedUntilRealChange = true
		st.ExhaustedAtOur = in.OurPrice
		st.Active = false
		st.PredictedPrice = 0
		st.RecoveryI = 0
		out.SessionActive = false
		return out
	}
	logMarketRecoveryShadowRow(in, st, out, true)
	out.Logged = true
	return out
}

// trimPriorExcludeCurrent — prior* уже включает текущий цикл; для C/D смотрим прошлые.
func trimPriorExcludeCurrent(hist []int) []int {
	if len(hist) <= 1 {
		return nil
	}
	return hist[:len(hist)-1]
}

func logMarketRecoveryShadowRow(in marketRecoveryEvalIn, st *marketRecoveryShadowState, out marketRecoveryEvalOut, stepped bool) {
	if mlDB == nil {
		return
	}
	p10 := in.Book.P10
	minAsk := in.Book.MinAsk
	gap := out.GapSteps
	ratio := out.OurOverP10
	if p10 > 0 && in.OurPrice > 0 && in.Step > 0 {
		gap = float64(p10-in.OurPrice) / float64(in.Step)
		ratio = float64(in.OurPrice) / float64(p10)
	}
	active := 0
	if out.SessionActive {
		active = 1
	}
	_ = stepped
	actionName := marketRecoveryActionShadow
	if marketRecoveryLiveEnabled {
		actionName = marketRecoveryActionLive
	}

	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	res, err := mlDB.Exec(`
INSERT INTO market_recovery_shadow (
	ts, item_id, action, our_price, p10, min_ask,
	unique_sellers_noban, uuid_noban, gap_steps, our_over_p10, step,
	recovery_i, held, buys, sales, predicted_price, stop_reason,
	winner_action, session_active
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Now.UTC().Format(time.RFC3339),
		in.Item,
		actionName,
		in.OurPrice,
		p10,
		minAsk,
		in.Book.NSell,
		in.Book.NUUID,
		gap,
		ratio,
		in.Step,
		out.RecoveryI,
		in.Held,
		in.Buys,
		in.Sales,
		out.PredictedPrice,
		out.StopReason,
		in.WinnerAction,
		active,
	)
	if err != nil {
		log.Printf("[market_recovery_shadow] insert %s: %v", in.Item, err)
		return
	}
	id, err := res.LastInsertId()
	if err == nil {
		st.PendingRowIDs = append(st.PendingRowIDs, id)
	}
	log.Printf("[market_recovery_shadow] %s our=%d p10=%d pred=%d i=%d sellers=%d uuid=%d ratio=%.3f gap=%.1f stop=%q winner=%s",
		in.Item, in.OurPrice, p10, out.PredictedPrice, out.RecoveryI, in.Book.NSell, in.Book.NUUID, ratio, gap, out.StopReason, in.WinnerAction)
}

func updateMarketRecoveryShadowNextCycle(rowID int64, buys, held, sales, recoveryI int) {
	if mlDB == nil || rowID <= 0 {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
UPDATE market_recovery_shadow
SET next_cycle_buys = ?, next_cycle_held = ?, next_cycle_sales = ?,
    cycles_to_first_buy = CASE WHEN ? > 0 AND cycles_to_first_buy IS NULL THEN recovery_i ELSE cycles_to_first_buy END,
    ups_to_first_buy = CASE WHEN ? > 0 AND ups_to_first_buy IS NULL THEN ? ELSE ups_to_first_buy END
WHERE id = ?`,
		buys, held, sales, buys, buys, recoveryI, rowID,
	)
	if err != nil {
		log.Printf("[market_recovery_shadow] next_cycle update: %v", err)
	}
}

// marketRecoveryLiveShouldRaise — peek: полный B_price_trap по hist прошлых циклов.
// Не меняет state и не трогает цену. Вызывать до apply winner.
func marketRecoveryLiveShouldRaise(item string, our, step, held, buys, sales int, book ahBookMarketRecoverySnap, manualLock bool) bool {
	if !marketRecoveryLiveEnabled {
		return false
	}
	if strings.TrimSpace(item) == "" || our <= 0 || step <= 0 {
		return false
	}
	mrShadowMu.Lock()
	defer mrShadowMu.Unlock()
	st := mrShadowGet(item)
	if st.ExhaustedUntilRealChange && our == st.ExhaustedAtOur {
		return false
	}
	in := marketRecoveryEvalIn{
		Item:       item,
		OurPrice:   our,
		Step:       step,
		Held:       held,
		Buys:       buys,
		Sales:      sales,
		ManualLock: manualLock,
		Book:       book,
	}
	return isBPriceTrap(in, st.P10Hist, st.RecentActions, st.PriorHeld, st.PriorBuys, st.PriorSales)
}

// marketRecoveryCommitAfterLive — обновить hist + лог после real decision (live mode).
// Не меняет цену. Virtual shadow-session не ведёт — winner уже применил step.
func marketRecoveryCommitAfterLive(in marketRecoveryEvalIn) {
	mrShadowMu.Lock()
	defer mrShadowMu.Unlock()
	st := mrShadowGet(in.Item)

	if len(st.PendingRowIDs) > 0 {
		upsAtBuy := st.RecoveryI
		for _, id := range st.PendingRowIDs {
			updateMarketRecoveryShadowNextCycle(id, in.Buys, in.Held, in.Sales, upsAtBuy)
		}
		st.PendingRowIDs = nil
	}

	st.RecentActions = appendBoundedStr(st.RecentActions, in.WinnerAction, marketRecoveryRecentDownN+2)
	st.PriorHeld = appendBoundedInt(st.PriorHeld, in.Held, marketRecoveryPriorHistN)
	st.PriorBuys = appendBoundedInt(st.PriorBuys, in.Buys, marketRecoveryPriorHistN)
	st.PriorSales = appendBoundedInt(st.PriorSales, in.Sales, marketRecoveryPriorHistN)
	if in.Book.OK && in.Book.P10 > 0 {
		st.P10Hist = appendBoundedInt(st.P10Hist, in.Book.P10, marketRecoveryP10HistN)
	}

	// Сброс virtual shadow, если остался от pre-live периода.
	st.Active = false
	st.PredictedPrice = 0

	raised := in.WinnerAction == marketRecoveryActionLive
	if raised {
		st.RecoveryI++
		st.LastStepAt = in.Now
		if st.SessionStartedAt.IsZero() {
			st.SessionStartedAt = in.Now
		}
		out := marketRecoveryEvalOut{
			DidStep:        true,
			PredictedPrice: in.OurPrice, // уже после +1 step
			RecoveryI:      st.RecoveryI,
			SessionActive:  true,
		}
		if in.Book.P10 > 0 && in.Step > 0 {
			// our в логе — цена до шага удобнее для gap; здесь our уже post.
			// gap/ratio от post-price.
			out.OurOverP10 = float64(in.OurPrice) / float64(in.Book.P10)
			out.GapSteps = float64(in.Book.P10-in.OurPrice) / float64(in.Step)
		}
		// После шага — если уже buyable/gap closed, exhausted до смены real our.
		if post := marketRecoveryStopReason(in, in.OurPrice, false); post == "buyable_zone" || post == "gap_closed" {
			out.StopReason = post
			out.SessionActive = false
			st.ExhaustedUntilRealChange = true
			st.ExhaustedAtOur = in.OurPrice
			st.RecoveryI = 0
			st.SessionStartedAt = time.Time{}
		}
		logMarketRecoveryShadowRow(in, st, out, true)
		return
	}

	// Не raise: если были в серии recovery и теперь STOP — exhausted / сброс счётчика.
	if st.RecoveryI > 0 {
		stop := marketRecoveryStopReason(in, in.OurPrice, false)
		if stop != "" {
			out := marketRecoveryEvalOut{
				StopReason:     stop,
				RecoveryI:      st.RecoveryI,
				PredictedPrice: in.OurPrice,
				SessionActive:  false,
			}
			if in.Book.P10 > 0 && in.OurPrice > 0 && in.Step > 0 {
				out.OurOverP10 = float64(in.OurPrice) / float64(in.Book.P10)
				out.GapSteps = float64(in.Book.P10-in.OurPrice) / float64(in.Step)
			}
			logMarketRecoveryShadowRow(in, st, out, false)
			if stop == "buyable_zone" || stop == "gap_closed" {
				st.ExhaustedUntilRealChange = true
				st.ExhaustedAtOur = in.OurPrice
			}
			st.RecoveryI = 0
			st.SessionStartedAt = time.Time{}
		}
	}
}

// runMarketRecoveryShadow — хук после real decision; никогда не меняет цену.
// Live mode: только hist + лог. Shadow mode: virtual +1 step session.
func runMarketRecoveryShadow(item string, now time.Time, ourPrice, step, held, buys, sales int, winnerAction string, manualLock bool) {
	book := ahBookMarketRecoveryStats(item, now.Add(-marketRecoveryBookWindow))
	in := marketRecoveryEvalIn{
		Item:         item,
		Now:          now,
		OurPrice:     ourPrice,
		Step:         step,
		Held:         held,
		Buys:         buys,
		Sales:        sales,
		WinnerAction: winnerAction,
		ManualLock:   manualLock,
		Book:         book,
	}
	if marketRecoveryLiveEnabled {
		marketRecoveryCommitAfterLive(in)
		return
	}
	evaluateMarketRecoveryShadow(in)
}

// resetMarketRecoveryShadowStateForTest — только тесты.
func resetMarketRecoveryShadowStateForTest() {
	mrShadowMu.Lock()
	defer mrShadowMu.Unlock()
	mrShadowState = map[string]*marketRecoveryShadowState{}
}
