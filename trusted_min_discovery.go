package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

// Trusted AH-min discovery (v8t Level 1):
// held=0 && buys=0 && our ≪ trusted_AH_min → price = trusted_AH_min.
// Jump only up. No p10, no nacenka, no κ. Uses ah_book_lots + seller bans.
// Live winner: corridor_price_up_trusted_ah_min. market_recovery skips same cycle via alreadyUp.

const (
	trustedMinDiscoveryLiveEnabled  = false // Sep 2026: книга наебывает — jump ↑ выкл
	trustedMinDiscoveryActionLive   = "corridor_price_up_trusted_ah_min"
	trustedMinDiscoveryActionShadow = "corridor_price_up_trusted_ah_min_shadow"

	trustedMinDiscoveryMinSellers = 15 // independent sellers in window
	trustedMinDiscoveryMinNear    = 3  // sellers confirming the min band
	trustedMinDiscoveryNearSteps  = 2  // near = min + 2×step

	// Deep pit vs trusted min (not p10).
	trustedMinDiscoveryRatioMax    = 0.80 // our/min ≤ 0.80
	trustedMinDiscoveryGapMinSteps = 4    // (min−our)/step ≥ 4

	trustedMinDiscoveryOutcomeHorizon = time.Hour
)

func initTrustedMinDiscoveryShadowTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS trusted_min_discovery_shadow (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	action TEXT NOT NULL,
	our_price INTEGER NOT NULL,
	trusted_ah_min INTEGER NOT NULL,
	unique_sellers INTEGER NOT NULL,
	sellers_near_min INTEGER NOT NULL,
	uuid_noban INTEGER NOT NULL,
	step INTEGER NOT NULL,
	gap_abs INTEGER NOT NULL,
	gap_steps REAL NOT NULL,
	gap_pct REAL NOT NULL,
	would_fire INTEGER NOT NULL,
	would_price INTEGER NOT NULL,
	trust_ok INTEGER NOT NULL,
	skip_reason TEXT,
	held INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	sales INTEGER NOT NULL,
	winner_action TEXT,
	notes TEXT
)`)
	if err != nil {
		log.Printf("[trusted_min_discovery_shadow] schema: %v", err)
	} else {
		_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_ts ON trusted_min_discovery_shadow(ts)`)
		_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_item_ts ON trusted_min_discovery_shadow(item_id, ts)`)
		_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_fire ON trusted_min_discovery_shadow(would_fire, ts)`)
	}

	_, err = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS trusted_min_discovery_jumps (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	old_price INTEGER NOT NULL,
	trusted_ah_min INTEGER NOT NULL,
	unique_sellers INTEGER NOT NULL,
	sellers_near_min INTEGER NOT NULL,
	uuid_noban INTEGER NOT NULL,
	step INTEGER NOT NULL,
	gap_steps REAL NOT NULL,
	gap_ratio REAL NOT NULL,
	new_price INTEGER NOT NULL,
	held INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	sales INTEGER NOT NULL,
	stop_reason TEXT,
	first_buy_after_jump INTEGER,
	time_to_first_buy_sec REAL,
	sales_1h INTEGER,
	buys_1h INTEGER,
	profit_1h INTEGER,
	held_1h INTEGER,
	any_price_down INTEGER,
	price_down_reason TEXT,
	outcome_done INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		log.Printf("[trusted_min_discovery_jumps] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmdj_ts ON trusted_min_discovery_jumps(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmdj_item ON trusted_min_discovery_jumps(item_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmdj_pending ON trusted_min_discovery_jumps(outcome_done, ts)`)
}

type trustedMinDiscoveryEval struct {
	WouldFire      bool
	WouldPrice     int
	TrustedMin     int
	UniqueSellers  int
	SellersNearMin int
	NUUID          int
	GapAbs         int
	GapSteps       float64
	GapPct         float64
	GapRatio       float64 // our/trusted_min
	TrustOK        bool
	SkipReason     string
}

// trustedMinDiscoveryGapOK — глубокая яма относительно trusted AH min (не p10).
func trustedMinDiscoveryGapOK(our, trustedMin, step int) bool {
	if our <= 0 || trustedMin <= 0 || step <= 0 {
		return false
	}
	if float64(our)/float64(trustedMin) <= trustedMinDiscoveryRatioMax {
		return true
	}
	gapSteps := float64(trustedMin-our) / float64(step)
	return gapSteps >= float64(trustedMinDiscoveryGapMinSteps)
}

// evalTrustedMinDiscovery — pure decision. Target = trusted seller min (no nacenka). Only raises.
func evalTrustedMinDiscovery(our, step, held, buys int, book ahBookTrustedSellerMinSnap, manualLock bool) trustedMinDiscoveryEval {
	out := trustedMinDiscoveryEval{
		TrustedMin:     book.TrustedMin,
		UniqueSellers:  book.UniqueSellers,
		SellersNearMin: book.SellersNearMin,
		NUUID:          book.NUUID,
	}
	if manualLock {
		out.SkipReason = "manual_lock"
		return out
	}
	if held != 0 {
		out.SkipReason = "held_not_empty"
		return out
	}
	if buys != 0 {
		out.SkipReason = "had_buys"
		return out
	}
	if our <= 0 || step <= 0 {
		out.SkipReason = "bad_our_or_step"
		return out
	}
	if !book.OK || book.TrustedMin <= 0 {
		out.SkipReason = "no_book"
		return out
	}

	out.GapAbs = book.TrustedMin - our
	out.GapSteps = float64(out.GapAbs) / float64(step)
	out.GapPct = float64(out.GapAbs) / float64(our) * 100
	out.GapRatio = float64(our) / float64(book.TrustedMin)

	// Soft trust for very deep pits (our/min ≤ 0.50): sellers≥5, near≥2.
	// Shallower gaps keep the hard gate (15 / 3).
	minSellers := trustedMinDiscoveryMinSellers
	minNear := trustedMinDiscoveryMinNear
	if out.GapRatio > 0 && out.GapRatio <= floorEscapeDeepPitRatio {
		minSellers = floorEscapeDeepPitMinSellers
		minNear = floorEscapeDeepPitMinNear
	}
	if book.UniqueSellers < minSellers {
		out.SkipReason = "thin_sellers"
		return out
	}
	if book.SellersNearMin < minNear {
		out.SkipReason = "min_unconfirmed"
		return out
	}
	out.TrustOK = true

	if !(book.TrustedMin > our+step) {
		if our >= book.TrustedMin {
			out.SkipReason = "our_at_or_above_min"
		} else {
			out.SkipReason = "gap_too_small"
		}
		return out
	}
	if !trustedMinDiscoveryGapOK(our, book.TrustedMin, step) {
		out.SkipReason = "gap_not_deep"
		return out
	}
	out.WouldFire = true
	out.WouldPrice = book.TrustedMin
	return out
}

type trustedMinJumpSession struct {
	RowID          int64
	JumpedAt       time.Time
	OldPrice       int
	NewPrice       int
	TrustedMin     int
	AccumSales     int
	AccumBuys      int
	AccumProfit    int
	FirstBuyAt     time.Time
	FirstBuyPrice  int
	AnyPriceDown   bool
	PriceDownReason string
	StopReason     string
	Closed         bool
}

var (
	tmJumpMu    sync.Mutex
	tmJumpState = map[string]*trustedMinJumpSession{}
)

func tmJumpGet(item string) *trustedMinJumpSession {
	st, ok := tmJumpState[item]
	if !ok {
		st = &trustedMinJumpSession{}
		tmJumpState[item] = st
	}
	return st
}

// trustedMinDiscoveryStopReason — почему после jump discovery «стоп» (цена дальше не двигаем этим правилом).
func trustedMinDiscoveryStopReason(held, buys int, book ahBookTrustedSellerMinSnap, our, step int, winnerAction string, manualLock bool) string {
	if buys > 0 {
		return "buy"
	}
	if held > 0 {
		return "held"
	}
	if manualLock {
		return "manual_lock"
	}
	if strings.Contains(winnerAction, "price_down") {
		return "price_down"
	}
	if !book.OK || book.UniqueSellers < trustedMinDiscoveryMinSellers || book.SellersNearMin < trustedMinDiscoveryMinNear {
		return "trust_lost"
	}
	if our > 0 && book.TrustedMin > 0 {
		if our >= book.TrustedMin || !(book.TrustedMin > our+step) || !trustedMinDiscoveryGapOK(our, book.TrustedMin, step) {
			return "gap_closed"
		}
	}
	return ""
}

// logTrustedMinDiscoveryJump — запись production jump + старт outcome-сессии.
func logTrustedMinDiscoveryJump(item string, now time.Time, oldPrice, newPrice, step, held, buys, sales int, ev trustedMinDiscoveryEval) {
	if mlDB == nil || strings.TrimSpace(item) == "" {
		return
	}
	ratio := ev.GapRatio
	if ratio <= 0 && ev.TrustedMin > 0 && oldPrice > 0 {
		ratio = float64(oldPrice) / float64(ev.TrustedMin)
	}
	mlDBMu.Lock()
	res, err := mlDB.Exec(`
INSERT INTO trusted_min_discovery_jumps (
	ts, item_id, old_price, trusted_ah_min, unique_sellers, sellers_near_min, uuid_noban,
	step, gap_steps, gap_ratio, new_price, held, buys, sales, outcome_done
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`,
		now.UTC().Format(time.RFC3339),
		item,
		oldPrice,
		ev.TrustedMin,
		ev.UniqueSellers,
		ev.SellersNearMin,
		ev.NUUID,
		step,
		ev.GapSteps,
		ratio,
		newPrice,
		held, buys, sales,
	)
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[trusted_min_discovery_jumps] insert %s: %v", item, err)
		return
	}
	id, _ := res.LastInsertId()
	tmJumpMu.Lock()
	tmJumpState[item] = &trustedMinJumpSession{
		RowID:      id,
		JumpedAt:   now,
		OldPrice:   oldPrice,
		NewPrice:   newPrice,
		TrustedMin: ev.TrustedMin,
	}
	tmJumpMu.Unlock()
	log.Printf("[trusted_min_discovery] JUMP %s %d → %d (min=%d sellers=%d near=%d gap_steps=%.1f ratio=%.3f)",
		item, oldPrice, newPrice, ev.TrustedMin, ev.UniqueSellers, ev.SellersNearMin, ev.GapSteps, ratio)
}

// trackTrustedMinDiscoveryOutcome — после jump: копим 1h / first buy / downs / stop_reason.
func trackTrustedMinDiscoveryOutcome(item string, now time.Time, held, buys, sales, profitNow int, winnerAction string, book ahBookTrustedSellerMinSnap, ourPrice, step int, manualLock bool) {
	tmJumpMu.Lock()
	st := tmJumpState[item]
	if st == nil || st.RowID == 0 || st.Closed {
		tmJumpMu.Unlock()
		return
	}
	st.AccumSales += sales
	st.AccumBuys += buys
	st.AccumProfit += profitNow
	if buys > 0 && st.FirstBuyAt.IsZero() {
		st.FirstBuyAt = now
		st.FirstBuyPrice = ourPrice
	}
	if strings.Contains(winnerAction, "price_down") {
		st.AnyPriceDown = true
		if st.PriceDownReason == "" {
			st.PriceDownReason = winnerAction
		}
	}
	stop := trustedMinDiscoveryStopReason(held, buys, book, ourPrice, step, winnerAction, manualLock)
	if stop != "" && st.StopReason == "" {
		st.StopReason = stop
	}
	elapsed := now.Sub(st.JumpedAt)
	done := elapsed >= trustedMinDiscoveryOutcomeHorizon || (stop != "" && (stop == "buy" || stop == "price_down" || stop == "manual_lock"))
	if !done && elapsed < trustedMinDiscoveryOutcomeHorizon {
		// keep accumulating; flush stop_reason early if set but wait for 1h metrics when possible
		if stop != "" && st.StopReason == "" {
			st.StopReason = stop
		}
		snap := *st
		tmJumpMu.Unlock()
		if snap.StopReason != "" {
			updateTrustedMinJumpPartial(snap)
		}
		return
	}
	st.Closed = true
	snap := *st
	delete(tmJumpState, item)
	tmJumpMu.Unlock()
	finalizeTrustedMinJumpOutcome(snap, now, held)
}

func updateTrustedMinJumpPartial(st trustedMinJumpSession) {
	if mlDB == nil || st.RowID == 0 || st.StopReason == "" {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`UPDATE trusted_min_discovery_jumps SET stop_reason=? WHERE id=? AND (stop_reason IS NULL OR stop_reason='')`,
		st.StopReason, st.RowID)
	if err != nil {
		log.Printf("[trusted_min_discovery_jumps] partial stop: %v", err)
	}
}

func finalizeTrustedMinJumpOutcome(st trustedMinJumpSession, now time.Time, held1h int) {
	if mlDB == nil || st.RowID == 0 {
		return
	}
	var ttb interface{}
	var firstBuy interface{}
	if !st.FirstBuyAt.IsZero() {
		ttb = st.FirstBuyAt.Sub(st.JumpedAt).Seconds()
		firstBuy = st.FirstBuyPrice
	}
	down := 0
	if st.AnyPriceDown {
		down = 1
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
UPDATE trusted_min_discovery_jumps SET
	stop_reason=COALESCE(NULLIF(?,''), stop_reason),
	first_buy_after_jump=?,
	time_to_first_buy_sec=?,
	sales_1h=?, buys_1h=?, profit_1h=?, held_1h=?,
	any_price_down=?, price_down_reason=?,
	outcome_done=1
WHERE id=?`,
		st.StopReason,
		firstBuy,
		ttb,
		st.AccumSales,
		st.AccumBuys,
		st.AccumProfit,
		held1h,
		down,
		nullIfEmpty(st.PriceDownReason),
		st.RowID,
	)
	if err != nil {
		log.Printf("[trusted_min_discovery_jumps] finalize %d: %v", st.RowID, err)
		return
	}
	log.Printf("[trusted_min_discovery] OUTCOME id=%d stop=%q ttb=%v sales1h=%d buys1h=%d profit1h=%d down=%v",
		st.RowID, st.StopReason, ttb, st.AccumSales, st.AccumBuys, st.AccumProfit, st.AnyPriceDown)
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// runTrustedMinDiscoveryShadow — diagnostic log; never mutates price.
func runTrustedMinDiscoveryShadow(item string, now time.Time, ourPrice, step, held, buys, sales int, winnerAction string, manualLock bool) {
	if strings.TrimSpace(item) == "" || ourPrice <= 0 || step <= 0 {
		return
	}
	book := ahBookTrustedSellerMin(item, now.Add(-ahBookRaiseWindow), trustedMinDiscoveryNearSteps, step)
	ev := evalTrustedMinDiscovery(ourPrice, step, held, buys, book, manualLock)

	if mlDB == nil {
		return
	}
	fire := 0
	trust := 0
	if ev.WouldFire {
		fire = 1
	}
	if ev.TrustOK {
		trust = 1
	}
	notes := "diag; live=corridor_price_up_trusted_ah_min; target=trusted_seller_min no_nacenka"

	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
INSERT INTO trusted_min_discovery_shadow (
	ts, item_id, action, our_price, trusted_ah_min, unique_sellers, sellers_near_min, uuid_noban,
	step, gap_abs, gap_steps, gap_pct, would_fire, would_price, trust_ok, skip_reason,
	held, buys, sales, winner_action, notes
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		now.UTC().Format(time.RFC3339),
		item,
		trustedMinDiscoveryActionShadow,
		ourPrice,
		ev.TrustedMin,
		ev.UniqueSellers,
		ev.SellersNearMin,
		ev.NUUID,
		step,
		ev.GapAbs,
		ev.GapSteps,
		ev.GapPct,
		fire,
		ev.WouldPrice,
		trust,
		ev.SkipReason,
		held, buys, sales,
		winnerAction,
		notes,
	)
	if err != nil {
		log.Printf("[trusted_min_discovery_shadow] insert %s: %v", item, err)
		return
	}
	if ev.WouldFire && winnerAction != trustedMinDiscoveryActionLive {
		log.Printf("[trusted_min_discovery_shadow] WOULD %s our=%d → min=%d sellers=%d near=%d gap_steps=%.1f gap_pct=%.1f winner=%s",
			item, ourPrice, ev.TrustedMin, ev.UniqueSellers, ev.SellersNearMin, ev.GapSteps, ev.GapPct, winnerAction)
	}
}
