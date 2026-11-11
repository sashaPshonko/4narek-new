package main

import (
	"log"
	"os"
	"strings"
	"time"
)

const (
	stockBandLoFrac       = 0.18
	stockBandHiFrac       = 0.25
	stockSoftDownFrac     = 0.28
	stockOverFrac         = 0.35
	stockDumpFrac         = 0.50
	pozorBandLoFrac       = 0.10
	pozorBandHiFrac       = 0.18
	pozorSoftDownFrac     = 0.22
	pozorOverFrac         = 0.25
	pozorDumpFrac         = 0.40
	corridorMinBandSpan   = 4
	ahBookRaiseWindow     = 30 * time.Minute // как ahBook2Window: пол и потолок книги по одному окну
	ahBookMinLotsInWindow = 40
	serverBoundLookCycles = 3
	serverBoundMinBuys    = 2
)

// AdjustReport — итог цикла adjustPrice для TG/логов.
type AdjustReport struct {
	Item           string
	Action         string
	Reason         string
	Skipped        bool
	PriceBefore    int
	PriceAfter     int
	NacenkaBefore  int
	NacenkaAfter   int
	Sales          int
	Buys           int
	TrySells       int
	OnAH           int
	Inv            int
	Held           int
	NormalSales    int
	Share          int
	Free           int
	Need           int
	PriceFloor     int
	Step           int
	Cooldown       int
	NacenkaSumNow  int
	NacenkaSumPrev int
	GoodStreak     int
	BlockNacenkaUp bool
}

// typeRelistDisabled — go-типы в режиме «без перевыставления».
var typeRelistDisabled map[string]struct{}

func initFleetRelistFlags() {
	typeRelistDisabled = make(map[string]struct{})
	raw := strings.TrimSpace(os.Getenv("FLEET_ABSORB_TYPES"))
	if raw == "" {
		return
	}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			typeRelistDisabled[t] = struct{}{}
		}
	}
	if len(typeRelistDisabled) > 0 {
		types := make([]string, 0, len(typeRelistDisabled))
		for t := range typeRelistDisabled {
			types = append(types, t)
		}
		log.Printf("[FLEET] без перевыставления (absorb): %v", types)
	}
}

func isTypeRelistEnabled(minecraftType string) bool {
	if typeRelistDisabled == nil {
		return true
	}
	_, off := typeRelistDisabled[minecraftType]
	return !off
}

const ()

// ItemAdjustState — состояние контроллера цены между циклами.
type ItemAdjustState struct {
	GoodStreak                int    `json:"good_streak"`
	LastCycleProfit           int    `json:"last_cycle_profit"`
	LastCycleNacenkaSum       int    `json:"last_cycle_nacenka_sum"`
	EmptyInventoryClimbSteps  int    `json:"empty_inventory_climb_steps"`
	EmptyInventoryAnchorPrice int    `json:"empty_inventory_anchor_price"`
	LastCycleSales            int    `json:"last_cycle_sales"`
	PriceExplored             bool   `json:"price_explored"`
	PriceExplorationCycles    int    `json:"price_exploration_cycles"`
	PriceOriginKind           string `json:"price_origin_kind,omitempty"`
	PriceOriginPrice          int    `json:"price_origin_price,omitempty"`
	// pridumalnahuy: фриз наценки (1_000_000) на цикл.
	NacenkaFrozen   bool `json:"nacenka_frozen"`
	NacenkaOriginal int  `json:"nacenka_original"`
	// Подряд циклов, когда цена выше потолка больше чем на наценку (подтверждение прыжка к потолку).
	AboveCeilCycles int `json:"above_ceil_cycles,omitempty"`
	// Подряд циклов, когда цена ниже пола (подтверждение прыжка к полу) и пол прошлого цикла.
	BelowFloorCycles int `json:"below_floor_cycles,omitempty"`
	PrevFloor        int `json:"prev_floor,omitempty"`
}

func resolveNacenkaMin(cfg ItemConfig) int {
	if cfg.NacenkaMin > 0 {
		return cfg.NacenkaMin
	}
	return cfg.Nacenka
}

func getNacenka(item string) int {
	if cfg, ok := itemsConfig[item]; ok {
		return cfg.Nacenka
	}
	return 0
}

func ensureNacenkasInitialized() {
	if data.Nacenkas == nil {
		data.Nacenkas = make(map[string]int)
	}
	if dailyData.Nacenkas == nil {
		dailyData.Nacenkas = make(map[string]int)
	}
	if data.AdjustState == nil {
		data.AdjustState = make(map[string]ItemAdjustState)
	}
	for item, cfg := range itemsConfig {
		data.Nacenkas[item] = cfg.Nacenka
		dailyData.Nacenkas[item] = cfg.Nacenka
		if _, ok := data.AdjustState[item]; !ok {
			data.AdjustState[item] = ItemAdjustState{}
		}
	}
}

func priceUpdatePayloadLocked() PriceUpdate {
	p := filterPrices()
	p.Catalog = buildCatalogOut()
	return p
}

func publishPriceUpdate() {
	mutex.RLock()
	payload := priceUpdatePayloadLocked()
	mutex.RUnlock()
	enrichCatalogBookBounds(&payload)
	select {
	case broadcast <- payload:
	default:
	}
}

func profitInWindow(item string, since time.Time) int {
	profit := 0
	for _, trade := range data.TradeHistory[item] {
		if !trade.Time.After(since) {
			continue
		}
		if trade.Price <= 0 {
			continue
		}
		switch trade.Type {
		case "buy":
			profit -= trade.Price
		case "sell":
			profit += trade.Price
		}
	}
	return profit
}

func nacenkaSumInWindow(item string, since time.Time) int {
	sum := 0
	fallback := getNacenka(item)
	for _, trade := range data.TradeHistory[item] {
		if !trade.Time.After(since) {
			continue
		}
		if trade.Type != "sell" {
			continue
		}
		n := trade.Nacenka
		if n <= 0 {
			n = fallback
		}
		sum += n
	}
	return sum
}

func stockNormFromConfig(cfg ItemConfig) int {
	if cfg.NormalCount > 0 {
		return cfg.NormalCount
	}
	return cfg.NormalSales
}

type stockBandFracs struct {
	lo, hi, soft, over, dump float64
}

func isPozorCategory(item string, cfg ItemConfig) bool {
	if strings.Contains(item, "позор") {
		return true
	}
	return strings.Contains(cfg.Type, "позор")
}

func isCrusherCategory(item string, cfg ItemConfig) bool {
	if strings.Contains(item, "крушител") {
		return true
	}
	return strings.Contains(cfg.Type, "крушител")
}

func stockBandFor(item string, cfg ItemConfig) stockBandFracs {
	if isPozorCategory(item, cfg) || isCrusherCategory(item, cfg) {
		return stockBandFracs{
			lo: pozorBandLoFrac, hi: pozorBandHiFrac, soft: pozorSoftDownFrac,
			over: pozorOverFrac, dump: pozorDumpFrac,
		}
	}
	return stockBandFracs{
		lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
		over: stockOverFrac, dump: stockDumpFrac,
	}
}

func stockTargets(share int, band stockBandFracs) (lo, hi, soft, over, dump int) {
	if share <= 0 {
		return 1, 2, 3, 4, 5
	}
	lo = int(float64(share)*band.lo + 0.5)
	hi = int(float64(share)*band.hi + 0.5)
	soft = int(float64(share)*band.soft + 0.5)
	over = int(float64(share)*band.over + 0.5)
	dump = int(float64(share)*band.dump + 0.5)
	if lo < 1 {
		lo = 1
	}
	if hi <= lo {
		hi = lo + 1
	}
	if band.lo >= stockBandLoFrac-1e-9 && hi-lo < corridorMinBandSpan {
		need := corridorMinBandSpan - (hi - lo)
		down := (need + 1) / 2
		up := need - down
		lo -= down
		hi += up
		if lo < 1 {
			hi += 1 - lo
			lo = 1
		}
	}
	if soft <= hi {
		soft = hi + 1
	}
	if over <= soft {
		over = soft + 1
	}
	if dump <= over {
		dump = over + 1
	}
	return lo, hi, soft, over, dump
}

func automaticDownAllowed(held, targetHi int) bool {
	return held > targetHi
}

func excessDepth(held, targetHi int) int {
	return held - targetHi
}

func postGrantCBlockDown(held, targetHi, sales, buys, zeroStreak int) bool {
	if !automaticDownAllowed(held, targetHi) {
		return false
	}
	d := excessDepth(held, targetHi)
	if sales >= 2 && d == 1 {
		return true
	}
	if sales == 0 && zeroStreak == 1 && d <= 1 && buys == 0 {
		return true
	}
	return false
}

func serverFunTimeRaiseAnomalous(ours, proposed int, item string, cycle time.Duration, now time.Time) bool {
	if proposed <= ours {
		return false
	}
	if serverMinBuyBlocksUp(item, cycle, now) {
		return true
	}
	mutex.RLock()
	cfg, ok := itemsConfig[item]
	mutex.RUnlock()
	if ok {
		if mid, _, mok := stockNormBookCeilNow(cfg, now); mok && mid > 0 {
			if proposed > mid+cfg.PriceStep {
				return true
			}
			return false
		}
	}
	since := now.Add(-ahBookRaiseWindow)
	p10, n, ok := ahBookP10Since(item, since)
	if !ok || n < ahBookMinLotsInWindow || p10 <= 0 {
		return false
	}
	return proposed > p10+getNacenka(item)
}

func serverMinBuyBlocksUp(item string, cycle time.Duration, now time.Time) bool {
	if item == "" {
		return false
	}
	if cycle <= 0 {
		cycle = 10 * time.Minute
	}
	since := now.Add(-time.Duration(serverBoundLookCycles) * cycle)
	buys := countRecentBuys(item, since)
	if buys < serverBoundMinBuys {
		return false
	}
	return buys >= countRecentSales(item, since)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sellPriceFloor(minBuy, nacenka int) int {
	_ = minBuy
	_ = nacenka
	return 0
}

func countItemsInCategoryLocked(minecraftType string) int {
	n := 0
	for _, conf := range itemsConfig {
		if conf.Type == minecraftType {
			n++
		}
	}
	return n
}

func itemSlotShareLocked(minecraftType string) int {
	bots := botsForGoTypeLocked(minecraftType)
	nItems := countItemsInCategoryLocked(minecraftType)
	if bots <= 0 || nItems <= 0 {
		return 0
	}
	return (botBuyStopHeld * bots) / nItems
}

func actionReasonRU(action string) string {
	switch action {
	case "corridor_price_up_pridumalnahuy":
		return "pridumalnahuy: запас ниже нормы, попыток мало → +шаг (не выше потолка)"
	case "corridor_price_down_pridumalnahuy":
		return "pridumalnahuy: попыток много, продаж мало (или цена выше потолка) → −шаг"
	case "corridor_hold_pridumalnahuy_sellthrough":
		return "pridumalnahuy: sales > normal → hold"
	case "corridor_hold_pridumalnahuy_freeze":
		return "pridumalnahuy: FREEZE — nacenka 1_000_000 на цикл"
	case "corridor_hold_pridumalnahuy_no_signal":
		return "pridumalnahuy: нет сигнала"
	case "corridor_hold_pridumalnahuy_no_up":
		return "pridumalnahuy: холд (цену не меняем, причина в reason)"
	case "corridor_price_up_pridumalnahuy_book_floor":
		return "pridumalnahuy: цена < пола книги → прыжок к полу"
	case "corridor_price_down_pridumalnahuy_book_ceiling":
		return "pridumalnahuy: цена > потолка книги → прыжок к потолку"
	case "skip_inactive":
		return "пропуск: нет активных ботов этого типа"
	case "hold":
		return "hold"
	case "":
		return "нет решения"
	default:
		return action
	}
}

func manualDirectionClampLocked(item string, window time.Duration) (blockUp, blockDown bool) {
	t, ok := data.LastManualUpdate[item]
	if !ok || t.IsZero() || time.Since(t) >= window {
		return false, false
	}
	switch data.LastManualKind[item] {
	case "min":
		return false, true
	case "max":
		return true, false
	case "set":
		return true, true
	default:
		return true, true
	}
}

type BuySurgeEvent struct {
	Dropped     bool
	Item        string
	PriceBefore int
	PriceAfter  int
	SurgeCount  int
	Sales       int
	Threshold   int
	NormalSales int
	Step        int
}

func maybeBuySurgePriceDownLocked(item string) BuySurgeEvent {
	ev := BuySurgeEvent{Item: item}
	cfg, ok := itemsConfig[item]
	if !ok || cfg.PriceStep <= 0 {
		return ev
	}
	if !itemConfigActiveLocked(cfg) {
		return ev
	}
	if isTypeRelistEnabled(cfg.Type) {
		return ev
	}
	if _, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime); blockDown {
		return ev
	}

	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	data.BuySurgeCount[item]++
	surgeCount := data.BuySurgeCount[item]

	share := itemSlotShareLocked(cfg.Type)
	_, targetHi, soft, _, _ := stockTargets(share, stockBandFor(item, cfg))
	threshold := maxInt(4, soft)
	held := getItemCount(item) + getInventoryCount(item)

	now := time.Now()
	since := now.Add(-cfg.AnalysisTime)
	if t, ok := data.LastCycleAt[item]; ok && !t.IsZero() {
		elapsed := now.Sub(t)
		if elapsed > 0 && elapsed <= cfg.AnalysisTime+time.Minute {
			since = t
		}
	}
	sales := countRecentSales(item, since)

	ev.SurgeCount, ev.Sales, ev.Threshold = surgeCount, sales, threshold
	ev.NormalSales, ev.Step = cfg.NormalSales, cfg.PriceStep

	if surgeCount < threshold || held < soft {
		return ev
	}
	if !automaticDownAllowed(held, targetHi) || postGrantCBlockDown(held, targetHi, sales, 0, 0) {
		return ev
	}

	priceBefore := data.Prices[item]
	if priceBefore <= 0 {
		return ev
	}
	nacenka := getNacenka(item)
	minBuy := getMinPriceFromHistory(item)
	floor := sellPriceFloor(minBuy, nacenka)
	newPrice := priceBefore - cfg.PriceStep
	if newPrice < floor {
		newPrice = floor
	}
	if newPrice >= priceBefore {
		return ev
	}

	data.Prices[item] = newPrice
	dailyData.Prices[item] = newPrice
	lastPriceUpdate[item] = now
	data.BuySurgeCount[item] = 0

	ev.Dropped = true
	ev.PriceBefore = priceBefore
	ev.PriceAfter = newPrice
	log.Printf("[SURGE] %s: surge=%d thr=%d held=%d soft=%d sales=%d | цена %d→%d | surge→0",
		item, surgeCount, threshold, held, soft, sales, priceBefore, newPrice)
	return ev
}

func adjustPrice(item string) AdjustReport {
	cfg, ok := itemsConfig[item]
	if !ok {
		return AdjustReport{Item: item, Action: "skip", Reason: "нет в items_config", Skipped: true}
	}
	// no_buy: предмет не покупаем, запаса нет — «дефицитная» логика повышала бы цену бесконечно
	// (кирка-бульд-маг 3.6 → 25 млн за 5 суток). Цена заморожена, её меняют вручную.
	if cfg.NoBuy {
		return AdjustReport{Item: item, Action: "skip", Reason: "no_buy: цена заморожена", Skipped: true}
	}

	now := time.Now()
	tryAdvanceCategoryMLOutcomes(cfg.Type, now)
	tryAdvanceCapitalForwards(now)
	onlineForCap, onlineMaxForML := fetchOnlineSnapshot()
	recordOnlineSample(now, onlineForCap)
	startHourProfileRefresher()

	mutex.Lock()
	swordTimes[item] = now
	if data.LastCycleAt == nil {
		data.LastCycleAt = make(map[string]time.Time)
	}
	prevCycleAt := data.LastCycleAt[item]
	data.LastCycleAt[item] = now
	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	delete(data.BuySurgeCount, item)
	lastUpdate := now.Add(-cfg.AnalysisTime)
	if !prevCycleAt.IsZero() {
		elapsed := now.Sub(prevCycleAt)
		if elapsed > 0 && elapsed <= cfg.AnalysisTime+time.Minute {
			lastUpdate = prevCycleAt
		}
	}

	rep := AdjustReport{Item: item, NormalSales: cfg.NormalSales, Step: cfg.PriceStep}

	if !itemConfigActiveLocked(cfg) {
		mode := fleetModeForGoTypeLocked(cfg.Type)
		if mode == fleetOutage {
			log.Printf("[SKIP] %s: тип %s — FLEET OUTAGE (нет pricing-ботов)", item, cfg.Type)
			rep.Reason = "fleet_outage"
		} else {
			log.Printf("[SKIP] %s: тип %s — нет активных ботов", item, cfg.Type)
			rep.Reason = actionReasonRU("skip_inactive")
		}
		mutex.Unlock()
		rep.Action = "skip_inactive"
		rep.Skipped = true
		return rep
	}

	ensureNacenkasInitialized()

	sales := countRecentSales(item, lastUpdate)
	buys := countRecentBuys(item, lastUpdate)
	trySells := countRecentTrySells(item, lastUpdate)
	profitNow := profitInWindow(item, lastUpdate)
	state := data.AdjustState[item]

	newPrice := data.Prices[item]
	priceBefore := newPrice
	nacenka := getNacenka(item)
	nacenkaBefore := nacenka
	step := cfg.PriceStep
	minPrice := getMinPriceFromHistory(item)
	nacenkaSumNow := nacenkaSumInWindow(item, lastUpdate)
	nacenkaSumPrev := state.LastCycleNacenkaSum
	priceFloor := sellPriceFloor(minPrice, nacenka)

	ahCounts := make(map[string]int)
	invCounts := make(map[string]int)

	for _, items := range clientItems {
		for name, count := range items {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				ahCounts[name] += count
			}
		}
	}
	for _, inv := range clientInventory {
		for name, count := range inv {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				invCounts[name] += count
			}
		}
	}

	stockNorm := stockNormFromConfig(cfg)

	onAH := ahCounts[item]
	invCount := invCounts[item]
	totalHeld := onAH + invCount
	heldForCorridor := totalHeld

	share := itemSlotShareLocked(cfg.Type)
	free, need := 0, 0
	if buys < sales && share > 0 {
		free = share - heldForCorridor
		if free < 0 {
			free = 0
		}
		need = sales - buys
	}

	stockLoad := 0.0
	if share > 0 {
		stockLoad = float64(heldForCorridor) / float64(share)
	}
	underbuyOK := buys < sales && share > 0 && free >= need
	tryRatio := 0.0
	if sales > 0 {
		tryRatio = float64(trySells) / float64(sales)
	}

	if isBookPegConfig(cfg) {
		return adjustPriceBookPeg(
			item, cfg, now, lastUpdate,
			sales, buys, trySells, profitNow,
			state,
			priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor,
			onAH, invCount, heldForCorridor, share, free, need, stockNorm,
			underbuyOK, tryRatio, stockLoad,
			onlineForCap, onlineMaxForML,
			ahCounts,
		)
	}

	return adjustPricePridumalnahuy(
		item, cfg, now, lastUpdate,
		sales, buys, trySells, profitNow,
		state,
		priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor,
		onAH, invCount, heldForCorridor, share, free, need, stockNorm,
		underbuyOK, tryRatio, stockLoad,
		onlineForCap, onlineMaxForML,
		ahCounts, invCounts,
	)
}

// Cold-start price discovery (v8ae): temporary +1 UP while sell-price is unknown.
// Not "sales==0 → UP". Explored SKUs never enter this path via empty silence alone.

const (
	// UP only if our/p10 < this
	// abort exploration when our/p10 >= this (thick book)
	// budget of cold-start cycles (~4h at 10m)

	// Backfill: lifetime evidence that SKU is already past discovery.
	coldStartBackfillMinCycles = 12
)

// resetPriceExplorationLocked — новая разведка после manual_set / явного re-seed.
func resetPriceExplorationLocked(state *ItemAdjustState, originKind string, originPrice int) {
	if state == nil {
		return
	}
	state.PriceExplored = false
	state.PriceExplorationCycles = 0
	state.PriceOriginKind = originKind
	state.PriceOriginPrice = originPrice
	log.Printf("[COLD_START] reset exploration origin=%s price=%d", originKind, originPrice)
}

// backfillPriceExplorationFromDB — one-shot after mlDB ready: старые SKU с sells/циклами → Explored.
func backfillPriceExplorationFromDB() {
	if mlDB == nil {
		return
	}
	type hit struct {
		item   string
		sells  int
		cycles int
	}
	var hits []hit
	mlDBMu.Lock()
	for item := range itemsConfig {
		var sells, cycles int
		_ = mlDB.QueryRow(
			`SELECT COUNT(*) FROM trade_events WHERE item_id=? AND event_type='sell'`, item,
		).Scan(&sells)
		_ = mlDB.QueryRow(
			`SELECT COUNT(*) FROM capital_cycles WHERE item_id=?`, item,
		).Scan(&cycles)
		if sells > 0 || cycles >= coldStartBackfillMinCycles {
			hits = append(hits, hit{item: item, sells: sells, cycles: cycles})
		}
	}
	mlDBMu.Unlock()

	mutex.Lock()
	defer mutex.Unlock()
	if data.AdjustState == nil {
		data.AdjustState = make(map[string]ItemAdjustState)
	}
	marked := 0
	for _, h := range hits {
		st := data.AdjustState[h.item]
		if st.PriceExplored {
			continue
		}
		st.PriceExplored = true
		if st.PriceOriginKind == "" {
			st.PriceOriginKind = "backfill"
		}
		data.AdjustState[h.item] = st
		if dailyData.AdjustState != nil {
			dailyData.AdjustState[h.item] = st
		}
		marked++
	}
	log.Printf("[COLD_START] backfill: marked Explored on %d SKUs", marked)
}
