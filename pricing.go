package main

import (
	"log"
	"os"
	"strings"
	"time"
)

const cheapBuyFractionThreshold = 0.50

// Слотов «Хранилище» на АХ у одного бота (4NAREK.mjs STORAGE_AH_SLOTS).
const ahStorageSlotsPerBot = 5

// Всего слотов у бота под лоты категории: инвентарь + АХ.
const botTotalSlots = 32

const (
	stockBandLoFrac                    = 0.18
	stockBandHiFrac                    = 0.25
	stockSoftDownFrac                  = 0.28
	stockOverFrac                      = 0.35
	stockDumpFrac                      = 0.50
	pozorBandLoFrac                    = 0.10
	pozorBandHiFrac                    = 0.18
	pozorSoftDownFrac                  = 0.22
	pozorOverFrac                      = 0.25
	pozorDumpFrac                      = 0.40
	corridorMaxUpStreak                = 1
	corridorUpCooldownCycles           = 2
	corridorMinSalesForUp              = 3
	corridorNightMinSalesForUp         = 4
	corridorSoftDownEvery              = 1
	corridorHardDownStepMult           = 2
	tryUpVetoMinTries                  = 5
	tryUpVetoPerSale                   = 2
	corridorSkimMinLead                = 3
	corridorSkimEnabled                = false
	corridorDemandUpEnabled            = true
	corridorRecoverUpEnabled           = true
	corridorPaidClimbEnabled           = true
	corridorMaxNoBuyUps                = 2
	corridorRecoverProbeSteps          = 1
	corridorThinFleetMaxBots           = 2
	corridorMaxIdleHardDowns           = 1
	corridorIdleHardDownsDump          = 3
	corridorPaidBandGapSteps           = 3
	corridorMinBandSpan                = 4
	ahBookRaiseWindow                  = 10 * time.Minute
	ahBookMinLotsInWindow              = 40
	ahBookMinLotsWhenEmpty             = 12
	ahBookSoftDownSlackSteps           = 2
	ahBookMaxRaiseSteps                = 2
	ahBookPriceUpEnabled               = false
	floorEscapePriceUpEnabled          = false
	emptyIdleMarketDownCooldownCycles  = 3
	serverBoundLookCycles              = 3
	serverBoundMinBuys                 = 2
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

const (
	marketFloorWindow    = 10 * time.Minute
	marketFloorWindowMin = 9*time.Minute + 30*time.Second
	marketFloorMaxStale  = 2 * time.Minute
)

// ItemAdjustState — состояние контроллера цены между циклами.
type ItemAdjustState struct {
	GoodStreak            int  `json:"good_streak"`
	ExperimentCheck       bool `json:"experiment_check"`
	LastCycleProfit       int  `json:"last_cycle_profit"`
	LastCycleNacenkaSum   int  `json:"last_cycle_nacenka_sum"`
	StockVsSalesCooldown  int  `json:"stock_vs_sales_cooldown"`
	FillPriceCooldown     int  `json:"fill_price_cooldown"`
	CorridorUpStreak      int  `json:"corridor_up_streak"`
	CorridorDeadStreak    int  `json:"corridor_dead_streak"`
	CorridorDownCooldown  int  `json:"corridor_down_cooldown"`
	CorridorUpCooldown    int  `json:"corridor_up_cooldown"`
	CorridorNoBuyUpStreak int  `json:"corridor_no_buy_up_streak"`
	IdleHardDownStreak    int  `json:"idle_hard_down_streak"`
	CorridorDownStreak    int  `json:"corridor_down_streak"`
	EmptyMarketGapStreak  int  `json:"empty_market_gap_streak"`
	FloorEscapeCooldown   int  `json:"floor_escape_cooldown"`
	EmptyIdleMarketDownCooldown int `json:"empty_idle_market_down_cooldown"`
	EmptyInventoryClimbSteps  int `json:"empty_inventory_climb_steps"`
	EmptyInventoryAnchorPrice int `json:"empty_inventory_anchor_price"`
	LastCycleSales            int `json:"last_cycle_sales"`
	ZeroSalesExcessStreak int `json:"zero_sales_excess_streak"`
	PriceExplored            bool   `json:"price_explored"`
	PriceExplorationCycles   int    `json:"price_exploration_cycles"`
	PriceOriginKind          string `json:"price_origin_kind,omitempty"`
	PriceOriginPrice         int    `json:"price_origin_price,omitempty"`
	// pridumalnahuy: фриз наценки (1_000_000) на цикл.
	NacenkaFrozen   bool `json:"nacenka_frozen"`
	NacenkaOriginal int  `json:"nacenka_original"`
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

func priceUpdatePayload() PriceUpdate {
	mutex.RLock()
	p := priceUpdatePayloadLocked()
	mutex.RUnlock()
	enrichCatalogBookBounds(&p)
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

func deepUnderstock(totalHeld, targetLo int) bool {
	return totalHeld == 0 && targetLo > 0
}

func lastPaidSellMax(trades []TradeLog, now time.Time, since time.Time) (maxPrice int, at time.Time) {
	for _, trade := range trades {
		if trade.Type != "sell" || trade.Price <= 0 {
			continue
		}
		if trade.Time.Before(since) || trade.Time.After(now) {
			continue
		}
		if trade.Price > maxPrice {
			maxPrice = trade.Price
			at = trade.Time
		}
	}
	return
}

func recoverPaidCap(lastPaid, step int) int {
	if lastPaid <= 0 || step <= 0 {
		return 0
	}
	return lastPaid + corridorRecoverProbeSteps*step
}

func recoverBlockedByPaidCap(price, lastPaid, step int) bool {
	cap := recoverPaidCap(lastPaid, step)
	if cap <= 0 {
		return true
	}
	return price >= cap
}

func automaticDownAllowed(held, targetHi int) bool {
	return held > targetHi
}

func excessDepth(held, targetHi int) int {
	return held - targetHi
}

func zeroSalesExcessStreakNow(sales, held, targetHi, prevStreak int) int {
	if sales != 0 || !automaticDownAllowed(held, targetHi) {
		return 0
	}
	if prevStreak < 0 {
		prevStreak = 0
	}
	return prevStreak + 1
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

func postGrantCDownCandidate(held, targetHi, sales, buys, zeroStreak int) bool {
	if sales != 0 || !automaticDownAllowed(held, targetHi) {
		return false
	}
	d := excessDepth(held, targetHi)
	if zeroStreak >= 2 {
		return true
	}
	if buys > 0 && (d >= 2 || zeroStreak >= 1) {
		return true
	}
	if d >= 3 {
		return true
	}
	return false
}

func doiCover(held, sales int) float64 {
	if sales <= 0 {
		return 0
	}
	return float64(held) / float64(sales)
}

func doiCoverIntensity(held, sales int) string {
	if sales <= 0 {
		return ""
	}
	doi := doiCover(held, sales)
	switch {
	case doi < 3:
		return "hold"
	case doi < 5:
		return "over"
	case doi < 10:
		return "soft"
	default:
		return "hold"
	}
}

func allowHardDown(sales, idleStreak int, stockLoad float64) bool {
	if sales > 0 {
		return true
	}
	maxIdle := corridorMaxIdleHardDowns
	if stockLoad >= 0.50 {
		maxIdle = corridorIdleHardDownsDump
	}
	return idleStreak < maxIdle
}

func priceFarBelowPaid(price, paidMax, step int) bool {
	if paidMax <= 0 || step <= 0 || price <= 0 {
		return false
	}
	return price <= paidMax-corridorPaidBandGapSteps*step
}

func ghostPriceDownOK(held, sales, trySells int) bool {
	if held <= 0 {
		return false
	}
	return trySellsBlockUp(sales, trySells) || trySells >= tryUpVetoMinTries
}

func isGhostCatalogDown(label string) bool {
	switch label {
	case "corridor_price_down_stale", "corridor_price_down_overcap", "corridor_price_down_empty_fair":
		return true
	default:
		return false
	}
}

func corridorUpArmCooldown(action string) bool {
	if !strings.Contains(action, "price_up") {
		return false
	}
	switch {
	case strings.Contains(action, "empty_idle"),
		strings.Contains(action, "empty_book"),
		strings.Contains(action, "floor_escape"),
		action == "corridor_price_up_floor",
		strings.Contains(action, "market_recovery"),
		strings.Contains(action, "trusted_ah_min"):
		return false
	default:
		return true
	}
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
		if mid, _, mok := stockNormBookMid(cfg, now.Add(-ahBook2Window)); mok && mid > 0 {
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

func trySellsBlockUp(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	if sales >= 2 {
		return trySells >= 3*sales
	}
	return trySells >= tryUpVetoPerSale*maxInt(sales, 1)
}

func trySellsBlockSkim(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	return trySells >= maxInt(sales, tryUpVetoMinTries)
}

func skimSalesLeadOK(sales, buys int) bool {
	return sales >= buys+corridorSkimMinLead
}

func skimShouldRevert(upCooldown, sales, buys, trySells int) bool {
	if upCooldown <= 0 {
		return false
	}
	if trySellsBlockUp(sales, trySells) {
		return true
	}
	return sales <= buys && trySells >= tryUpVetoMinTries
}

func isNightMSK(t time.Time) bool {
	msk := t.In(time.FixedZone("MSK", 3*60*60))
	h := msk.Hour()
	return h >= 3 && h < 9
}

func demandMinSalesForUp(botsInCategory int, night bool) int {
	if night {
		if botsInCategory > 0 && botsInCategory <= corridorThinFleetMaxBots {
			return 3
		}
		return corridorNightMinSalesForUp
	}
	if botsInCategory > 0 && botsInCategory <= corridorThinFleetMaxBots {
		return 2
	}
	return corridorMinSalesForUp
}

func demandStrongEnoughForUp(sales, lastCycleSales, minSales int, night bool) bool {
	if sales >= minSales {
		return true
	}
	if night {
		return false
	}
	if minSales > 2 {
		return false
	}
	return sales >= 2 && lastCycleSales >= 2
}

func downWouldUndershoot(totalHeld, sales, targetLo int) bool {
	drain := maxInt(sales, 1)
	return totalHeld-drain < targetLo
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func categoryAhCapacityLocked(minecraftType string) int {
	bots := botsForGoTypeLocked(minecraftType)
	if bots <= 0 {
		return 0
	}
	return bots * ahStorageSlotsPerBot
}

func maxReachableStockOnAHLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int) int {
	capacity := categoryAhCapacityLocked(cfg.Type)
	if capacity <= 0 {
		return onAH
	}

	occupiedOthers := 0
	for name, count := range ahCounts {
		if name == item || count <= 0 {
			continue
		}
		other, ok := itemsConfig[name]
		if !ok || other.Type != cfg.Type {
			continue
		}
		occupiedOthers += count
	}
	if occupiedOthers == 0 {
		return capacity
	}

	totalOccupied := onAH + occupiedOthers
	freeSlots := capacity - totalOccupied
	if freeSlots < 0 {
		freeSlots = 0
	}
	return onAH + freeSlots
}

func stockNormReachableOnAHLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int, stockNorm int) bool {
	if !isTypeRelistEnabled(cfg.Type) {
		return true
	}
	if stockNorm <= 0 {
		return true
	}
	maxReachable := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
	return maxReachable >= stockNorm
}

func effectiveStockForOverstock(onAH, totalStock int, relistEnabled bool) int {
	if relistEnabled {
		return onAH
	}
	return totalStock
}

func allowSellPriceIncreaseLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int, stockNorm int) bool {
	if !isTypeRelistEnabled(cfg.Type) {
		return true
	}
	if stockNorm <= 0 {
		return true
	}
	maxReachable := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
	return maxReachable >= stockNorm
}

func cheapBuyFraction(item string, sellPrice, nacenka, step int, since time.Time) (float64, int) {
	hist := priceHistory[item]
	if hist == nil || len(hist.Records) == 0 {
		return 0, 0
	}
	buyMax := sellPrice - nacenka
	cheap, total := 0, 0
	for _, r := range hist.Records {
		if !r.Time.After(since) {
			continue
		}
		total++
		if buyMax-r.Price >= step {
			cheap++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(cheap) / float64(total), total
}

func applyMarketFloors(floors map[string]int, windowStartMs, windowEndMs, windowMs int64) {
	_ = floors
	_ = windowStartMs
	_ = windowEndMs
	_ = windowMs
	return
}

func sellPriceFloor(minBuy, nacenka int) int {
	_ = minBuy
	_ = nacenka
	return 0
}

func ahBookRaiseTarget(minAsk, nacenka, step int) int {
	if minAsk <= 0 {
		return 0
	}
	if step < 0 {
		step = 0
	}
	return minAsk + nacenka + step
}

func ahBookRaiseTargetCapped(sell, minAsk, nacenka, step int) int {
	full := ahBookRaiseTarget(minAsk, nacenka, step)
	if full <= 0 {
		return 0
	}
	if step <= 0 || ahBookMaxRaiseSteps <= 0 {
		return full
	}
	cap := sell + ahBookMaxRaiseSteps*step
	if full > cap {
		return cap
	}
	return full
}

func ahBookSoftDownTarget(p10, minAsk, nacenka, step int) int {
	if p10 <= 0 || minAsk <= 0 {
		return 0
	}
	if step < 0 {
		step = 0
	}
	tgt := p10 + nacenka + step
	floor := minAsk + nacenka
	if tgt < floor {
		tgt = floor
	}
	return tgt
}

func shouldRaiseFromAhBook(sell, minAsk, nacenka, n, sales, buys, held int, dumpZone, alreadyDown, hadBuys bool) bool {
	if n < ahBookMinLotsInWindow || minAsk <= 0 || dumpZone || alreadyDown || hadBuys {
		return false
	}
	if held <= 0 {
		return false
	}
	if sales <= buys || sales < corridorMinSalesForUp {
		return false
	}
	return sell < minAsk+nacenka
}

func shouldRaiseEmptyFromAhBook(sell, p10, minAsk, nacenka, n, step, held, buys int, alreadyDown, dumpZone bool) bool {
	if held > 0 || buys > 0 || alreadyDown || dumpZone {
		return false
	}
	if n < ahBookMinLotsInWindow || p10 <= 0 || minAsk <= 0 || step <= 0 {
		return false
	}
	marketFloor := maxInt(p10+nacenka, minAsk+nacenka)
	return marketFloor >= sell+2*step
}

func emptyBookRaiseTarget(sell, p10, minAsk, nacenka, step int) int {
	if step <= 0 {
		return sell
	}
	marketFloor := maxInt(p10+nacenka, minAsk+nacenka)
	tgt := sell + step
	if tgt > marketFloor {
		tgt = marketFloor
	}
	return tgt
}

func shouldSoftDownFromAhBook(sell, p10, minAsk, nacenka, n, step int, alreadyUp, hadBuys bool, held int) bool {
	if n < ahBookMinLotsInWindow || p10 <= 0 || minAsk <= 0 || alreadyUp {
		return false
	}
	if hadBuys && held > 0 {
		return false
	}
	bookFloor := minAsk + nacenka
	if sell <= bookFloor {
		return false
	}
	if step <= 0 {
		return sell > p10+nacenka
	}
	return sell > p10+nacenka+ahBookSoftDownSlackSteps*step
}

func emptyIdleAhSoftDownOK(emptyIdle, bookOK, p10OK bool, sell, p10, minAsk, nacenka, n, step int) bool {
	if !emptyIdle || !bookOK || !p10OK {
		return false
	}
	return shouldSoftDownFromAhBook(sell, p10, minAsk, nacenka, n, step, false, false, 0)
}

func emptyIdleAboveMarketBlocksUp(emptyIdle, bookOK, p10OK bool, sell, p10, nacenka int) bool {
	if !emptyIdle || !bookOK || !p10OK || p10 <= 0 {
		return false
	}
	return sell > p10+nacenka
}

func ahBookSoftDownApply(sell, softDownTgt, priceFloor, step, held int) int {
	if softDownTgt <= 0 || softDownTgt >= sell {
		return sell
	}
	tgt := softDownTgt
	if tgt < priceFloor {
		tgt = priceFloor
	}
	if held <= 0 && step > 0 && ahBookMaxRaiseSteps > 0 {
		cap := sell - ahBookMaxRaiseSteps*step
		if cap < priceFloor {
			cap = priceFloor
		}
		if tgt < cap {
			tgt = cap
		}
	}
	if tgt >= sell {
		return sell
	}
	return tgt
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
	return (botTotalSlots * bots) / nItems
}

func hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys int) (ok bool, free, need int) {
	if buys >= sales {
		return false, 0, 0
	}
	need = sales - buys
	if share <= 0 {
		return false, 0, need
	}
	free = share - totalHeld
	if free < 0 {
		free = 0
	}
	return free >= need, free, need
}

func blockNacenkaRaise(share, totalHeld, sales, buys int) bool {
	ok, _, _ := hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
	return ok
}

func actionReasonRU(action string) string {
	switch action {
	case "corridor_price_up_pridumalnahuy":
		return "pridumalnahuy: tight slots / inv>N no freeze → +step"
	case "corridor_price_down_pridumalnahuy":
		return "pridumalnahuy: try_sells ≫ sales*7 → −step"
	case "corridor_hold_pridumalnahuy_sellthrough":
		return "pridumalnahuy: sales > normal → hold"
	case "corridor_hold_pridumalnahuy_freeze":
		return "pridumalnahuy: FREEZE — nacenka 1_000_000 на цикл"
	case "corridor_hold_pridumalnahuy_no_signal":
		return "pridumalnahuy: нет сигнала"
	case "corridor_hold_pridumalnahuy_no_up":
		return "pridumalnahuy: UP veto (floor/ceil)"
	case "corridor_price_up_pridumalnahuy_book_floor":
		return "pridumalnahuy: цена < пола книги → прыжок к полу"
	case "corridor_price_down_pridumalnahuy_book_mid":
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

	now := time.Now()
	tryAdvanceCategoryMLOutcomes(cfg.Type, now)
	tryAdvanceCapitalForwards(now)
	onlineForCap, onlineMaxForML := fetchOnlineSnapshot()

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

	if isPricingPolicyStockNorm() {
		return adjustPriceStockNorm(
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

	if isPricingPolicyClassicBook() {
		return adjustPriceClassicBook(
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

	if isPricingPolicyClassic() {
		return adjustPriceClassic(
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

	if isPricingPolicyV4() {
		return adjustPriceV4(
			item, cfg, now, lastUpdate,
			sales, buys, trySells, profitNow,
			state,
			priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor,
			onAH, invCount, heldForCorridor, share, free, need, stockNorm,
			underbuyOK, tryRatio, stockLoad,
			onlineForCap, onlineMaxForML,
		)
	}

	if isPricingPolicyPridumalnahuy() {
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

	if isPricingPolicyV10() {
		return adjustPriceV10(
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

	if isPricingPolicyV9() {
		return adjustPriceV9(
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

	// Ни одна политика не подошла. Раньше тут был v8u-fallback — убран.
	// Если попадаем сюда — значит capitalPolicy задан в неизвестное значение.
	log.Printf("[ADJUST] %s: неизвестная capitalPolicy=%q → skip", item, capitalPolicy)
	mutex.Unlock()
	rep.Action = "skip_inactive"
	rep.Skipped = true
	rep.Reason = "unknown capitalPolicy: " + capitalPolicy
	return rep
}