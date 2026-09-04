package main

import (
	"fmt"
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

// stock_corridor_v8k — v8j + замок set_min (FunTime не поднимает каталог втупую).
// stock_corridor_v8j — v8i, но пол книги = min за 3 мин; ↓ к ≥2 селлерам ≥min+наценка.
//
// stock_corridor_v8h — v8g + полоса на малом share (броня 1 тип / 6 id, share≈21):
// 18–25% давало lo/hi = 4–5, медиана held=2 → вечный недобор и deep-↑ (lo/2=2).
// Мин. ширина полосы вниз; deep только held=0. Over idle / paid-climb как в v8g.
// Demand / skim / noBuy без потолка. Цена < пола → поднимаем. Пустое не роняем.
//
// v7→v8 (29.07): в [lo,hi] всегда был hold → цена залипала в «удобном» равновесии
// (шлемы 1.2M вместо прибыльных ~2.5M). Skim: если сток в полосе и рынок реально
// разбирает (sales>buys, strong demand, без try/buy-veto) → ↑ пробуем дороже.
// Не ночью. Те же veto/cd/streak, что у demand-↑. Soft↓ при перестоке откатывает.
//
// v6→v7 (28.07): recover-↑ с пола; buy-veto только buys>sales.
// Онлайн в решение не входит.
//
// История: v1 15–25%; v2 try-veto; v3 lo=18% buy-veto; v4 weak_demand; v5 empty/night; v6–v7 …
const (
	stockBandLoFrac   = 0.18
	stockBandHiFrac   = 0.25
	stockSoftDownFrac = 0.28
	stockOverFrac     = 0.35
	stockDumpFrac     = 0.50
	// позор: цель тоньше, hard↓ раньше — sell-коридор иначе не сливает 60–99% fill.
	pozorBandLoFrac            = 0.10
	pozorBandHiFrac            = 0.18
	pozorSoftDownFrac          = 0.22
	pozorOverFrac              = 0.25
	pozorDumpFrac              = 0.40
	corridorMaxUpStreak        = 1 // не два ↑ подряд
	corridorUpCooldownCycles   = 1 // после ↑ ещё N циклов без ↑ (deep-↑ может обойти)
	corridorMinSalesForUp      = 3 // дневной пол спроса на ↑
	corridorNightMinSalesForUp = 4 // ночь 03–09 MSK
	corridorSoftDownEvery      = 1
	corridorHardDownStepMult   = 2 // over/dump: −step×2
	tryUpVetoMinTries          = 5
	tryUpVetoPerSale           = 2
	// recover-↑ без BasePrice: можно ↑ при недоборе без sales, но не бесконечно в пустоту.
	corridorMaxNoBuyUps       = 8 // recover-↑ подряд без buys → пауза (антиvacuum), без resume
	corridorRecoverProbeSteps = 2 // recover ≤ max(sell в TTL) + K×step
	corridorThinFleetMaxBots  = 2 // ≤ столько ботов в категории → мягче пороги спроса / recover
	corridorMaxIdleHardDowns = 1 // over/dump при sales=0: один щуп, не цепочка в пол
	corridorPaidBandGapSteps = 3 // в полосе цена ≤ paid−N×step → ↑ к якорю (и ночью)
	corridorMinBandSpan      = 4 // hi−lo; share=21 иначе полоса 4–5 шт. (позор не трогаем)
	ahBookRaiseWindow     = 3 * time.Minute // низ АХ «сейчас»; 10 мин слишком длинно
	ahBookMinLotsInWindow = 10 // меньше — окно пустое, min не считаем
	ahBookSellerClipMin   = 2  // честных селлеров ниже нас → клип к max из них
	serverBoundLookCycles = 3 // окно закупок для set_min
	serverBoundMinBuys    = 2 // одна покупка — шум
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

// typeRelistDisabled — go-типы в режиме «без перевыставления» (FLEET_ABSORB_TYPES, через запятую).
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
	CorridorUpStreak      int  `json:"corridor_up_streak"`        // подряд ↑ в stock_corridor
	CorridorDeadStreak    int  `json:"corridor_dead_streak"`      // подряд sales=0 && buys=0
	CorridorDownCooldown  int  `json:"corridor_down_cooldown"`    // циклы до следующего soft↓
	CorridorUpCooldown    int  `json:"corridor_up_cooldown"`      // циклы до следующего ↑
	CorridorNoBuyUpStreak int  `json:"corridor_no_buy_up_streak"` // ↑ подряд без buys (антиvacuum)
	IdleHardDownStreak    int  `json:"idle_hard_down_streak"`     // подряд over/dump при sales=0
	LastCycleSales        int  `json:"last_cycle_sales"`          // sales прошлого цикла
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

// ensureNacenkasInitialized — всегда выравнивает рантайм-наценки под items_config
// (stock_corridor не крутит nacenka; старые capital-значения из daily/runtime сбрасываем).
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
	defer mutex.RUnlock()
	return priceUpdatePayloadLocked()
}

// publishPriceUpdate — только без mutex.Lock в этой горутине (иначе дедлок RWMutex).
func publishPriceUpdate() {
	mutex.RLock()
	payload := priceUpdatePayloadLocked()
	mutex.RUnlock()
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

// nacenkaSumInWindow — сумма наценок по продажам в окне (успех эксперимента).
// Если у старых записей Nacenka=0 — берём текущую getNacenka как fallback.
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

// stockBandFracs — доли fill для stockTargets.
type stockBandFracs struct {
	lo, hi, soft, over, dump float64
}

func isPozorCategory(item string, cfg ItemConfig) bool {
	if strings.Contains(item, "позор") {
		return true
	}
	return strings.Contains(cfg.Type, "позор")
}

func stockBandFor(item string, cfg ItemConfig) stockBandFracs {
	if isPozorCategory(item, cfg) {
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

// stockTargets — пороги fill=held/share по полосе (default или позор).
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
	// Обычный коридор: на малом share (броня после merge) не оставлять полосу в 1–2 слота.
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

// deepUnderstock — витрина пустая. lo/2 при share≈21 = held=2, это норма брони, не «глубокий недобор».
func deepUnderstock(totalHeld, targetLo int) bool {
	return totalHeld == 0 && targetLo > 0
}

// lastPaidSellMax — самая дорогая фактическая продажа в окне (не BasePrice, не пол закупа).
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

// recoverPaidCap — recover может щупать чуть выше последней доказанной продажи.
func recoverPaidCap(lastPaid, step int) int {
	if lastPaid <= 0 || step <= 0 {
		return 0
	}
	return lastPaid + corridorRecoverProbeSteps*step
}

// recoverBlockedByPaidCap — нет продаж в окне или цена уже на/выше якоря+щуп.
func recoverBlockedByPaidCap(price, lastPaid, step int) bool {
	cap := recoverPaidCap(lastPaid, step)
	if cap <= 0 {
		return true
	}
	return price >= cap
}

// allowHardDown — over/dump. С продажами — всегда (fwd лучше hold). Без продаж — один щуп,
// не 4×−200к до пола (sword7).
func allowHardDown(sales, idleStreak int) bool {
	if sales > 0 {
		return true
	}
	return idleStreak < corridorMaxIdleHardDowns
}

// priceFarBelowPaid — в полосе сидим на полу, рынок только что платил дороже.
func priceFarBelowPaid(price, paidMax, step int) bool {
	if paidMax <= 0 || step <= 0 || price <= 0 {
		return false
	}
	return price <= paidMax-corridorPaidBandGapSteps*step
}

// serverFunTimeRaiseAnomalous — set_min: не поднимать каталог.
// Закупаем не меньше, чем продаём (≥2 buy за 3 цикла) — не ↑.
// Иначе не выше p10 книги + наценка (не сырой min: дампы).
func serverFunTimeRaiseAnomalous(ours, proposed int, item string, cycle time.Duration, now time.Time) bool {
	if proposed <= ours {
		return false
	}
	if serverMinBuyBlocksUp(item, cycle, now) {
		return true
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

// trySellsBlockUp — рынок уже отказывается от цены: ↑ запрещён даже при недоборе стока.
func trySellsBlockUp(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	return trySells >= tryUpVetoPerSale*maxInt(sales, 1)
}

// isNightMSK — окно 03:00–08:59 Europe/Moscow (false-scarcity / реконнекты).
func isNightMSK(t time.Time) bool {
	msk := t.In(time.FixedZone("MSK", 3*60*60))
	h := msk.Hour()
	return h >= 3 && h < 9
}

// demandMinSalesForUp — порог sales на ↑ с учётом размера флота в категории.
// 2 бота (504 кирки): sales/цикл естественно ниже → днём достаточно 2, ночью 3.
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

// demandStrongEnoughForUp — не поднимать на крошках.
// День: sales≥minSales или sales=2 два цикла подряд (если minSales≤2).
// Ночь 03–09 MSK: только sales≥minSales (sustained×2 ночью отключён).
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

// downWouldUndershoot — ↓ ускорит продажи; если уже один цикл sales утащит ниже lo — не режем.
// Только для soft↓; hard over/dump в v4 игнорируют этот guard.
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

// categoryAhCapacityLocked — ёмкость хранилища АХ по типу (боты × 5 слотов). Только под mutex.Lock.
func categoryAhCapacityLocked(minecraftType string) int {
	bots := aggregateBotsPerTypeLocked()[minecraftType]
	if bots <= 0 {
		return 0
	}
	return bots * ahStorageSlotsPerBot
}

// maxReachableStockOnAHLocked — сколько лотов этого id может быть на АХ с учётом занятых слотов другими id.
// Только под mutex.Lock.
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

// stockNormReachableOnAHLocked — сток-норма из конфига достижима на АХ (слоты не забиты другими id).
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

// effectiveStockForOverstock — при перевыставлении переизбыток только по лотам на АХ (инвентарь — буфер).
func effectiveStockForOverstock(onAH, totalStock int, relistEnabled bool) int {
	if relistEnabled {
		return onAH
	}
	return totalStock
}

// allowSellPriceIncreaseLocked — не поднимать цену, если другие id забили слоты
// и предмет физически не может дойти до сток-нормы из конфига.
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

// cheapBuyFraction — доля покупок ≥ на price_step ниже buy-потолка (sell − nacenka).
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

// applyMarketFloors — подтянуть sell-цену к мин. лоту на АХ (+ наценка), если ниже.
// Только при 0 продаж, 0 наличия и подтверждённом окне сбора ≥10 мин.
// ОТКЛЮЧЕНО: не повышаем цену, если она ниже мин. лота на АХ.
func applyMarketFloors(floors map[string]int, windowStartMs, windowEndMs, windowMs int64) {
	_ = floors
	_ = windowStartMs
	_ = windowEndMs
	_ = windowMs
	return

	/*
		if len(floors) == 0 {
			return
		}
		if windowStartMs <= 0 || windowEndMs <= 0 || windowEndMs <= windowStartMs {
			log.Printf("[MARKET_FLOOR] skip: нет метаданных окна")
			return
		}
		windowStart := time.UnixMilli(windowStartMs)
		windowEnd := time.UnixMilli(windowEndMs)
		span := windowEnd.Sub(windowStart)
		if span < marketFloorWindowMin {
			log.Printf("[MARKET_FLOOR] skip: окно %v < %v", span.Round(time.Second), marketFloorWindowMin)
			return
		}
		if windowMs > 0 && windowMs < int64(marketFloorWindow/time.Millisecond) {
			log.Printf("[MARKET_FLOOR] skip: window_ms=%d", windowMs)
			return
		}
		if time.Since(windowEnd) > marketFloorMaxStale {
			log.Printf("[MARKET_FLOOR] skip: устарело (конец окна %v назад)", time.Since(windowEnd).Round(time.Second))
			return
		}

		mutex.Lock()
		ensureNacenkasInitialized()
		changed := false
		now := time.Now()

		for item, floor := range floors {
			cfg, ok := itemsConfig[item]
			if !ok || floor <= 0 {
				continue
			}

			if !itemConfigActiveLocked(cfg) {
				continue
			}

			sales := countRecentSales(item, now.Add(-cfg.AnalysisTime))
			totalStock := getItemCount(item) + getInventoryCount(item)
			if sales != 0 || totalStock != 0 {
				continue
			}

			current := data.Prices[item]
			if current <= 0 {
				continue
			}

			nacenka := getNacenka(item)
			target := floor + nacenka
			marker := current % 100
			target = (target/100)*100 + marker

			if current >= target {
				continue
			}

			log.Printf("[MARKET_FLOOR] %s: %d → %d (лот %d + наценка %d, окно %v, sales=0 stock=0)",
				item, current, target, floor, nacenka, span.Round(time.Second))
			data.Prices[item] = target
			dailyData.Prices[item] = target
			lastPriceUpdate[item] = time.Now()
			changed = true
		}

		mutex.Unlock()

		if !changed {
			return
		}

		publishPriceUpdate()
		saveDailyDataNoMessageUpdate()
	*/
}

func sellPriceFloor(minBuy, nacenka int) int {
	// base_price не пол: только старт, если цены ещё нет.
	// Пол = дешёвая покупка из истории + наценка (без истории — только наценка).
	return minBuy + nacenka
}

// ahBookRaiseTarget — селл = самый дешёвый ask из выборки + наша наценка + шаг.
func ahBookRaiseTarget(minAsk, nacenka, step int) int {
	if minAsk <= 0 {
		return 0
	}
	if step < 0 {
		step = 0
	}
	return minAsk + nacenka + step
}

// shouldRaiseFromAhBook — селл ниже min(окно)+наценка; dump / ↓ цикла / были buys — не трогаем.
func shouldRaiseFromAhBook(sell, minAsk, nacenka, n int, dumpZone, alreadyDown, hadBuys bool) bool {
	if n < ahBookMinLotsInWindow || minAsk <= 0 || dumpZone || alreadyDown || hadBuys {
		return false
	}
	return sell < minAsk+nacenka
}

// countItemsInCategoryLocked — сколько id в items_config с данным go-типом. Только под mutex.Lock.
func countItemsInCategoryLocked(minecraftType string) int {
	n := 0
	for _, conf := range itemsConfig {
		if conf.Type == minecraftType {
			n++
		}
	}
	return n
}

// itemSlotShareLocked — доля слотов категории на один предмет:
// (32 × боты_в_категории) / число_предметов_в_категории.
// Только под mutex.Lock.
func itemSlotShareLocked(minecraftType string) int {
	bots := aggregateBotsPerTypeLocked()[minecraftType]
	nItems := countItemsInCategoryLocked(minecraftType)
	if bots <= 0 || nItems <= 0 {
		return 0
	}
	return (botTotalSlots * bots) / nItems
}

// hasSpaceToCoverBuyDeficit — хватает ли свободной доли предмета, чтобы докупить (sales−buys).
// free = share − totalHeld; need = sales − buys (только при buys < sales).
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

// blockNacenkaRaise — покупок меньше продаж и место есть → наценку не поднимаем никогда.
func blockNacenkaRaise(share, totalHeld, sales, buys int) bool {
	ok, _, _ := hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
	return ok
}

func actionReasonRU(action string) string {
	switch action {
	case "corridor_price_down_dump":
		return "corridor_v8h: held ≥ dump% → −цена×2 (без продаж — макс. 1 щуп подряд)"
	case "corridor_price_down_over":
		return "corridor_v8h: held ≥ over% → −цена×2 (без продаж — макс. 1 щуп подряд)"
	case "corridor_hold_over_idle":
		return "corridor_v8h: перезапас, sales=0 после idle-щупа — ждём продажу"
	case "corridor_price_up_paid":
		return "corridor_v8h: в полосе цена << lastPaid → ↑ к якорю (и ночью)"
	case "corridor_price_down_soft":
		return "corridor_v6: held > hi → −цена (слив хвоста выше полосы)"
	case "corridor_price_down_hi":
		return "corridor_v3(legacy): held > hi → −цена"
	case "corridor_price_up_demand":
		return "corridor_v8: held < lo, сильный спрос (день≥3 / ночь≥4) → +цена"
	case "corridor_price_up_deep":
		return "corridor_v8h: held=0 днём + сильный спрос → +цена (обход up_cd)"
	case "corridor_price_up_recover", "corridor_price_up_recover_deep":
		return "corridor_v8e: недобор, recover ≤ max(sell)+K×step (без BasePrice)"
	case "corridor_price_up_skim":
		return "corridor_v8: held в полосе + сильный разбор → +цена (probe прибыли)"
	case "corridor_price_up_floor":
		return "corridor_v8c: цена ниже пола (minBuy+наценка) → поднимаем"
	case "corridor_price_up_ah_book":
		return "corridor_v8j: селл < min(3 мин ah_book)+наценка → к min+наценка+шаг"
	case "corridor_price_down_ah_sellers":
		return "corridor_v8j: ≥2 селлера ниже нас, но ≥min(3 мин)+наценка → к max из них"
	case "corridor_hold_recover_ceiling":
		return "corridor_v8e: recover упёрся в max(sell за окно)+K×step"
	case "corridor_hold_recover_stale":
		return "corridor_v8e: нет продаж в окне — recover ↑ запрещён (якорь протух)"
	case "corridor_hold_recover_pause":
		return "corridor_v8e: слишком много recover-↑ без buys → пауза (без resume)"
	case "corridor_hold_band":
		return "corridor_v8: held в полосе, нет сигнала для skim → hold"
	case "corridor_hold_skim_veto":
		return "corridor_v8: в полосе, но try/buy/weak → skim ↑ запрещён"
	case "corridor_hold_hysteresis":
		return "corridor_v3(legacy): held 25–28% → hold"
	case "corridor_hold_filling":
		return "corridor_v8: held < lo, идут buys, sales=0 → hold (набираем сток)"
	case "corridor_hold_dead":
		return "corridor_v8: held < lo, sales=0 buys=0, не у пола → hold"
	case "corridor_hold_empty":
		return "corridor_v8: held=0 высоко над полом → ↑ запрещён (vacuum)"
	case "corridor_hold_weak_demand":
		return "corridor_v8: held < lo, спрос слабый → ↑ запрещён"
	case "corridor_hold_up_cap":
		return "corridor_v8: недобор, но лимит ↑ подряд → hold"
	case "corridor_hold_up_cd":
		return "corridor_v8: недобор, но ↑ на cooldown → hold"
	case "corridor_hold_try_veto":
		return "corridor_v8: недобор, но try_sells≫sales → ↑ запрещён"
	case "corridor_hold_buy_veto":
		return "corridor_v8: недобор, buys>sales → ↑ запрещён"
	case "corridor_hold_overshoot":
		return "corridor_v6: soft↓ пропустил бы ниже lo → hold"
	case "corridor_hold_down_cd":
		return "corridor_v6: soft↓ на cooldown → hold"
	case "price_down_buy_surge":
		return "surge: всплеск buys при стоке ≥ цели → −цена"
	// legacy (старые логи/БД)
	case "classic_price_up":
		return "classic(legacy): sales < normal && stock ≤ normal && onAH < normal → +цена"
	case "classic_price_down_weak_sales":
		return "classic(legacy): АХ > sales и АХ > нормы при слабых sales → −цена"
	case "classic_price_down_buy_excess":
		return "classic(legacy): buys > 2×sales и запас > нормы → −цена"
	case "classic_price_down_leader":
		return "classic(legacy): лидер категории, запас > 3×sales → −цена"
	case "oldoldold_price_up":
		return "oldoldold: АХ+инв < normal_sales → +цена"
	case "oldoldold_price_down":
		return "oldoldold: АХ > sales и АХ > нормы при слабых sales → −цена"
	case "capital_hold":
		return "капитал: hold — нет давления"
	case "capital_dump":
		return "капитал: dump — мёртвые sales / hog → −цена"
	case "capital_fill":
		return "капитал: fill — сток ниже цели → −наценка"
	case "capital_fill_price":
		return "капитал: +P — SOLO demand (спрос) или MULTI underbuy"
	case "capital_skim":
		return "капитал: skim — поток ок → +наценка"
	case "capital_rollback":
		return "капитал: откат неудачного skim"
	case "hold":
		return "hold"
	case "skip_inactive":
		return "пропуск: нет активных ботов этого типа"
	case "skip_manual":
		return "пропуск: min/max от оркестратора — цикл на паузе"
	case "hold_manual_min":
		return "hold: после min можно только ↑ — ↓ заблокирован"
	case "hold_manual_max":
		return "hold: после max можно только ↓ — ↑ заблокирован"
	case "experiment_ok", "experiment_rollback", "experiment_start":
		return action
	default:
		if action == "" {
			return "нет решения"
		}
		return action
	}
}

// manualDirectionClampLocked — в окне AnalysisTime после set_min/set_max:
// min → блок ↓; max → блок ↑. Неизвестный kind → блок обоих (старое поведение).
// Только под mutex.Lock.
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
	default:
		return true, true
	}
}

// BuySurgeEvent — мгновенное снижение цены из‑за всплеска покупок.
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

// maybeBuySurgePriceDownLocked — на каждый buy: счётчик +=1;
// если сток уже ≥ верхней цели коридора и счётчик ≥ hi → −step, счётчик = 0.
// Не зависит от NormalSales. Только под mutex.Lock.
func maybeBuySurgePriceDownLocked(item string) BuySurgeEvent {
	ev := BuySurgeEvent{Item: item}
	cfg, ok := itemsConfig[item]
	if !ok || cfg.PriceStep <= 0 {
		return ev
	}
	if !itemConfigActiveLocked(cfg) {
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
	_, _, soft, _, _ := stockTargets(share, stockBandFor(item, cfg))
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

	// Surge↓ только если уже выше гистерезиса soft (не с края полосы).
	if surgeCount < threshold || held < soft {
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
	// Сеть + sqlite до mutex.Lock — иначе клинит весь WS/HTTP на ping FunTime / БД.
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
	// новый цикл — только surge-счётчик, статы цикла не трогаем
	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	delete(data.BuySurgeCount, item)
	lastUpdate := now.Add(-cfg.AnalysisTime)
	// продолжение прерванного цикла: окно от прошлого якоря (не если просрочили >1м)
	if !prevCycleAt.IsZero() {
		elapsed := now.Sub(prevCycleAt)
		if elapsed > 0 && elapsed <= cfg.AnalysisTime+time.Minute {
			lastUpdate = prevCycleAt
		}
	}

	rep := AdjustReport{Item: item, NormalSales: cfg.NormalSales, Step: cfg.PriceStep}

	if !itemConfigActiveLocked(cfg) {
		log.Printf("[SKIP] %s: тип %s — нет активных ботов", item, cfg.Type)
		mutex.Unlock()
		rep.Action = "skip_inactive"
		rep.Reason = actionReasonRU(rep.Action)
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
	paidSince := now.Add(-maxAnalysisRetain())
	paidMax, _ := lastPaidSellMax(data.TradeHistory[item], now, paidSince)
	overCap := recoverBlockedByPaidCap(priceBefore, paidMax, step)

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

	share := itemSlotShareLocked(cfg.Type)
	free, need := 0, 0
	if buys < sales && share > 0 {
		free = share - totalHeld
		if free < 0 {
			free = 0
		}
		need = sales - buys
	}

	changed := false
	action := ""
	var notes []string
	var experimentTG *experimentTelegramEvent

	// ═══════════════════════════════════════════════════════════════════
	// stock_corridor_v8 — fill + recover + profit-skim в полосе.
	// Default полоса [lo,hi]≈18–25%; позор ≈10–18% + earlier over/dump.
	// ↑ ниже lo: спрос / deep / recover; try-veto; buy-veto buys>sales.
	// ↑ в полосе (skim): сильный разбор днём → probe выше (прибыль, не только fill).
	// hard↓ over/dump: step×2.
	// ═══════════════════════════════════════════════════════════════════

	band := stockBandFor(item, cfg)
	targetLo, targetHi, targetSoft, targetOver, targetDump := stockTargets(share, band)
	stockLoad := 0.0
	if share > 0 {
		stockLoad = float64(totalHeld) / float64(share)
	}
	prevCycleSales := state.LastCycleSales // до обновления в конце цикла
	nightMSK := isNightMSK(now)
	botsInCat := aggregateBotsPerTypeLocked()[cfg.Type]
	minSalesForUp := demandMinSalesForUp(botsInCat, nightMSK)

	underbuyOK := false
	if buys < sales {
		need = sales - buys
		free = share - totalHeld
		if free < 0 {
			free = 0
		}
		underbuyOK = share > 0 && free >= need
	}

	tryRatio := 0.0
	if sales > 0 {
		tryRatio = float64(trySells) / float64(sales)
	} else if trySells > 0 {
		tryRatio = float64(trySells)
	}

	applyDown := func(label, note string, stepMult int) {
		if stepMult < 1 {
			stepMult = 1
		}
		cand := priceBefore - step*stepMult
		if cand < priceFloor {
			cand = priceFloor
		}
		if cand < priceBefore {
			newPrice = cand
			action = label
			changed = true
			notes = append(notes, note)
		} else {
			notes = append(notes, note+" · floor")
			if action == "" {
				action = "hold"
			}
		}
	}
	applyUp := func(label, note string) {
		newPrice = priceBefore + step
		action = label
		changed = true
		notes = append(notes, note)
	}

	trySoftDown := func(label, note string) {
		if downWouldUndershoot(totalHeld, sales, targetLo) {
			action = "corridor_hold_overshoot"
			notes = append(notes, note+" · overshoot-guard")
			return
		}
		if state.CorridorDownCooldown > 0 {
			action = "corridor_hold_down_cd"
			notes = append(notes, fmt.Sprintf("%s · down_cd=%d", note, state.CorridorDownCooldown))
			return
		}
		applyDown(label, note, 1)
		if changed && strings.Contains(action, "price_down") {
			state.CorridorDownCooldown = corridorSoftDownEvery - 1
			if state.CorridorDownCooldown < 0 {
				state.CorridorDownCooldown = 0
			}
		}
	}

	switch {
	case share <= 0:
		action = "hold"
		notes = append(notes, "share=0 — нет базы для коридора")

	case totalHeld >= targetDump:
		if !allowHardDown(sales, state.IdleHardDownStreak) {
			action = "corridor_hold_over_idle"
			notes = append(notes, fmt.Sprintf("held=%d ≥ dump=%d sales=0 idleHard=%d — ждём продажу, не цепочку в пол", totalHeld, targetDump, state.IdleHardDownStreak))
		} else {
			applyDown("corridor_price_down_dump",
				fmt.Sprintf("held=%d ≥ dump=%d (%.0f%% share=%d ×%d)",
					totalHeld, targetDump, stockLoad*100, share, corridorHardDownStepMult),
				corridorHardDownStepMult)
			state.CorridorDownCooldown = 0
		}

	case totalHeld >= targetOver:
		if !allowHardDown(sales, state.IdleHardDownStreak) {
			action = "corridor_hold_over_idle"
			notes = append(notes, fmt.Sprintf("held=%d ≥ over=%d sales=0 idleHard=%d — ждём продажу, не цепочку в пол", totalHeld, targetOver, state.IdleHardDownStreak))
		} else {
			applyDown("corridor_price_down_over",
				fmt.Sprintf("held=%d ≥ over=%d (%.0f%% share=%d ×%d)",
					totalHeld, targetOver, stockLoad*100, share, corridorHardDownStepMult),
				corridorHardDownStepMult)
		}

	case totalHeld > targetHi:
		trySoftDown("corridor_price_down_soft",
			fmt.Sprintf("held=%d > hi=%d (полоса [%d,%d] soft=%d share=%d)",
				totalHeld, targetHi, targetLo, targetHi, targetSoft, share))

	case totalHeld < targetLo:
		deep := deepUnderstock(totalHeld, targetLo)
		bypassUpCD := deep && !nightMSK
		// buy-veto только когда реально набиваем сток сильнее продаж.
		// buys==sales (в т.ч. 1=1 при held≈0) — рынок забирает всё → ↑ можно.
		liveBuyVeto := buys > sales
		noBuyBlocked := state.CorridorNoBuyUpStreak >= corridorMaxNoBuyUps
		// recover: недобор, нет try/buy-veto, не ночь. Потолок — max(sell в retain)+K×step.
		recoverCDOK := state.CorridorUpCooldown == 0 || bypassUpCD
		recoverStreakOK := state.CorridorUpStreak < corridorMaxUpStreak || bypassUpCD
		recoverOK := !nightMSK &&
			recoverCDOK &&
			recoverStreakOK &&
			!trySellsBlockUp(sales, trySells) &&
			!liveBuyVeto &&
			!noBuyBlocked &&
			!overCap
		switch {
		case trySellsBlockUp(sales, trySells):
			action = "corridor_hold_try_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d try=%d — рынок не берёт, ↑ запрещён",
				totalHeld, targetLo, sales, trySells))
		case liveBuyVeto:
			action = "corridor_hold_buy_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d > sales=%d — набиваем сток, ↑ запрещён",
				totalHeld, targetLo, buys, sales))
		case sales > buys && state.CorridorUpCooldown > 0 && !bypassUpCD:
			action = "corridor_hold_up_cd"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d но up_cd=%d — пауза после ↑",
				totalHeld, targetLo, sales, state.CorridorUpCooldown))
		case sales > buys && !demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK):
			action = "corridor_hold_weak_demand"
			if nightMSK {
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d last=%d night — нужен sales≥%d, ↑ запрещён",
					totalHeld, targetLo, sales, prevCycleSales, minSalesForUp))
			} else {
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d last=%d — слабый спрос, ↑ запрещён",
					totalHeld, targetLo, sales, prevCycleSales))
			}
		case sales > buys && (state.CorridorUpStreak < corridorMaxUpStreak || bypassUpCD):
			upLabel := "corridor_price_up_demand"
			upNote := fmt.Sprintf("held=%d < lo=%d sales=%d > buys=%d last=%d night=%v (сильный разбор витрины)",
				totalHeld, targetLo, sales, buys, prevCycleSales, nightMSK)
			if bypassUpCD && (state.CorridorUpCooldown > 0 || state.CorridorUpStreak >= corridorMaxUpStreak) {
				upLabel = "corridor_price_up_deep"
				upNote = fmt.Sprintf("held=0 < lo=%d sales=%d > buys=%d — deep-↑ обход cd/streak",
					targetLo, sales, buys)
			}
			applyUp(upLabel, upNote)
		case sales > buys && state.CorridorUpStreak >= corridorMaxUpStreak:
			action = "corridor_hold_up_cap"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d но up_streak=%d≥%d",
				totalHeld, targetLo, sales, state.CorridorUpStreak, corridorMaxUpStreak))
		case recoverOK:
			label := "corridor_price_up_recover"
			note := fmt.Sprintf("held=%d < lo=%d sales=%d buys=%d paid=%d cap=%d noBuyUps=%d/%d — recover-↑",
				totalHeld, targetLo, sales, buys, paidMax, recoverPaidCap(paidMax, step),
				state.CorridorNoBuyUpStreak, corridorMaxNoBuyUps)
			if bypassUpCD && (state.CorridorUpCooldown > 0 || state.CorridorUpStreak >= corridorMaxUpStreak) {
				label = "corridor_price_up_recover_deep"
				note = fmt.Sprintf("held=%d < lo=%d deep recover-↑ noBuyUps=%d/%d",
					totalHeld, targetLo, state.CorridorNoBuyUpStreak, corridorMaxNoBuyUps)
			}
			applyUp(label, note)
		case overCap && paidMax <= 0:
			action = "corridor_hold_recover_stale"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d — нет sell за %s, recover ↑ запрещён",
				totalHeld, targetLo, maxAnalysisRetain().Round(time.Minute)))
		case overCap:
			cap := recoverPaidCap(paidMax, step)
			action = "corridor_hold_recover_ceiling"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d price=%d ≥ paid %d + %d×step → cap %d",
				totalHeld, targetLo, priceBefore, paidMax, corridorRecoverProbeSteps, cap))
		case noBuyBlocked:
			action = "corridor_hold_recover_pause"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d — пауза recover: %d ↑ без buys (антиvacuum)",
				totalHeld, targetLo, state.CorridorNoBuyUpStreak))
		case totalHeld <= 0:
			action = "corridor_hold_empty"
			notes = append(notes, fmt.Sprintf("held=0 < lo=%d — пусто, recover сейчас недоступен", targetLo))
		case buys > 0:
			action = "corridor_hold_filling"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d sales=0 — набираем сток", totalHeld, targetLo, buys))
		default:
			action = "corridor_hold_dead"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=0 buys=0 — hold", totalHeld, targetLo))
		}

	default:
		// В полосе: не мёртвая зона. Если витрина стабильно разбирается —
		// пробуем ↑ (skim), иначе залипаем в дешёвом локальном оптимуме.
		skimOK := !nightMSK &&
			state.CorridorUpCooldown == 0 &&
			state.CorridorUpStreak < corridorMaxUpStreak &&
			sales > buys &&
			demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) &&
			!trySellsBlockUp(sales, trySells)
		paidClimb := priceFarBelowPaid(priceBefore, paidMax, step) &&
			buys <= sales &&
			!trySellsBlockUp(sales, trySells) &&
			state.CorridorNoBuyUpStreak < corridorMaxNoBuyUps &&
			state.CorridorUpCooldown == 0
		switch {
		case trySellsBlockUp(sales, trySells):
			action = "corridor_hold_skim_veto"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] sales=%d try=%d — рынок не берёт, skim ↑ запрещён",
				totalHeld, targetLo, targetHi, sales, trySells))
		case buys > sales:
			action = "corridor_hold_skim_veto"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] buys=%d > sales=%d — набиваем, skim ↑ запрещён",
				totalHeld, targetLo, targetHi, buys, sales))
		case paidClimb:
			applyUp("corridor_price_up_paid",
				fmt.Sprintf("held=%d в [%d,%d] price=%d << paid=%d night=%v — ↑ к якорю",
					totalHeld, targetLo, targetHi, priceBefore, paidMax, nightMSK))
		case nightMSK:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] night — skim выкл", totalHeld, targetLo, targetHi))
		case state.CorridorUpCooldown > 0:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] up_cd=%d — пауза skim",
				totalHeld, targetLo, targetHi, state.CorridorUpCooldown))
		case !demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) || sales <= buys:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] sales=%d buys=%d last=%d — нет сигнала skim",
				totalHeld, targetLo, targetHi, sales, buys, prevCycleSales))
		case skimOK:
			applyUp("corridor_price_up_skim",
				fmt.Sprintf("held=%d в [%d,%d] sales=%d > buys=%d last=%d — skim-↑ (probe прибыли)",
					totalHeld, targetLo, targetHi, sales, buys, prevCycleSales))
		default:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] share=%d", totalHeld, targetLo, targetHi, share))
		}
	}

	dumpZone := totalHeld >= targetDump
	alreadyDown := strings.Contains(action, "price_down")
	bookSince := now.Add(-ahBookRaiseWindow)
	// sqlite книги — вне mutex.Lock (иначе WS/sales клинят на ah_book + mlDBMu).
	mutex.Unlock()
	minAsk, bookN, bookOK := ahBookMinSince(item, bookSince)
	raiseFromBook := bookOK && shouldRaiseFromAhBook(priceBefore, minAsk, nacenka, bookN, dumpZone, alreadyDown, buys > 0)
	var raiseTgt int
	if raiseFromBook {
		raiseTgt = ahBookRaiseTarget(minAsk, nacenka, step)
	}
	var clipTgt, clipN int
	var clipOK bool
	if bookOK {
		probe := newPrice
		if raiseTgt > probe {
			probe = raiseTgt
		}
		clipTgt, clipN, clipOK = ahBookSellerClipTarget(item, probe, minAsk+nacenka, bookSince)
	}
	mutex.Lock()
	if raiseFromBook && raiseTgt > newPrice {
		newPrice = raiseTgt
		action = "corridor_price_up_ah_book"
		changed = true
		notes = append(notes, fmt.Sprintf("ah_book min3=%d n=%d → селл %d < min+наценка %d → %d",
			minAsk, bookN, priceBefore, minAsk+nacenka, raiseTgt))
	}
	if clipOK && clipTgt < newPrice {
		tgt := clipTgt
		if tgt < priceFloor {
			tgt = priceFloor
		}
		if tgt < newPrice {
			newPrice = tgt
			action = "corridor_price_down_ah_sellers"
			changed = true
			notes = append(notes, fmt.Sprintf("ah_sellers n=%d floor=%d → клип к max=%d", clipN, minAsk+nacenka, tgt))
		}
	}

	// После set_min: только ↑. После set_max: только ↓. Окно = AnalysisTime.
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	if blockDown && newPrice < priceBefore {
		notes = append(notes, "manual min → ↓ запрещён")
		newPrice = priceBefore
		changed = false
		action = "hold_manual_min"
	} else if blockUp && newPrice > priceBefore {
		notes = append(notes, "manual max → ↑ запрещён")
		newPrice = priceBefore
		changed = false
		action = "hold_manual_max"
	}

	if action == "" {
		action = "hold"
	}

	if newPrice < priceFloor {
		newPrice = priceFloor
		if newPrice > priceBefore {
			changed = true
			action = "corridor_price_up_floor"
			notes = append(notes, fmt.Sprintf("цена %d < пола %d → поднимаем", priceBefore, priceFloor))
		} else if newPrice == priceBefore {
			notes = append(notes, "уже на полу")
		}
	}

	// Стрелки коридора + soft↓ cooldown + up-cooldown / last sales
	if strings.Contains(action, "price_up") {
		state.CorridorUpStreak++
		state.CorridorDeadStreak = 0
		state.CorridorDownCooldown = 0
		state.CorridorUpCooldown = corridorUpCooldownCycles
		state.IdleHardDownStreak = 0
		isRecover := strings.Contains(action, "recover") || strings.Contains(action, "price_up_paid")
		if buys > 0 || !isRecover {
			state.CorridorNoBuyUpStreak = 0
		} else {
			state.CorridorNoBuyUpStreak++
		}
	} else {
		state.CorridorUpStreak = 0
		if sales == 0 && buys == 0 {
			state.CorridorDeadStreak++
		} else {
			state.CorridorDeadStreak = 0
			if buys > 0 {
				state.CorridorNoBuyUpStreak = 0
			}
		}
		if sales > 0 {
			state.IdleHardDownStreak = 0
		} else if strings.Contains(action, "price_down_over") || strings.Contains(action, "price_down_dump") {
			state.IdleHardDownStreak++
		}
		if state.CorridorUpCooldown > 0 {
			state.CorridorUpCooldown--
		}
		// Soft-cooldown тикает каждый цикл, кроме только что выставленного после soft↓.
		if strings.Contains(action, "price_down_soft") {
			// уже выставили CorridorDownCooldown в trySoftDown
		} else if state.CorridorDownCooldown > 0 {
			state.CorridorDownCooldown--
		}
		if strings.Contains(action, "price_down_over") || strings.Contains(action, "price_down_dump") {
			state.CorridorDownCooldown = 0
		}
	}

	if state.StockVsSalesCooldown > 0 {
		state.StockVsSalesCooldown--
	}
	if state.FillPriceCooldown > 0 {
		state.FillPriceCooldown--
	}

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	// Наценку не меняем — фиксирована из items_config.
	nacenka = nacenkaBefore
	if newPrice != priceBefore {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
		changed = true
	}

	actionTaken := action
	reason := actionReasonRU(actionTaken)
	if len(notes) > 0 {
		reason = reason + " | " + strings.Join(notes, " · ")
	}

	if changed {
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d | held %d/share %d зона[%d,%d] soft=%d over=%d | продажи %d buys=%d try=%d | АХ %d инв %d | %s",
			item, action, priceBefore, newPrice, nacenka, totalHeld, share, targetLo, targetHi, targetSoft, targetOver, sales, buys, trySells, onAH, invCount, reason)
	} else {
		log.Printf("[HOLD] %s: %s | цена %d | наценка %d | held %d/share %d зона[%d,%d] soft=%d | продажи %d buys=%d try=%d | АХ %d инв %d | %s",
			item, actionTaken, newPrice, nacenka, totalHeld, share, targetLo, targetHi, targetSoft, sales, buys, trySells, onAH, invCount, reason)
	}

	queueMLDecisionLocked(
		item, cfg, actionTaken,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
		onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy,
		Item:           item,
		Category:       cfg.Type,
		Action:         actionTaken,
		Winner:         actionTaken,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(targetHi),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		Share:          share,
		Free:           free,
		Need:           need,
		NormalSales:    cfg.NormalSales,
		NormalCount:    stockNorm,
		TryRatio:       tryRatio,
		StockLoad:      stockLoad,
		Underbuy:       underbuyOK,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   nacenka,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		PriceFloor:     priceFloor,
		Step:           step,
		Cooldown:       state.StockVsSalesCooldown,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		CheapFrac:      0,
		CheapN:         0,
		MinBuyHistory:  minPrice,
		BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     state.CorridorUpStreak,
		DumpBlockedCD:  false,
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}

	shadowSnap := mlAdjustSnapshot{}
	if mlShadowEnabled() {
		shadowSnap = mlAdjustSnapshot{
			At:             now,
			Item:           item,
			CategoryType:   cfg.Type,
			GoAction:       actionTaken,
			PriceBefore:    priceBefore,
			NacenkaBefore:  nacenkaBefore,
			GoPriceAfter:   newPrice,
			GoNacenkaAfter: nacenka,
			Sales:          sales,
			Buys:           buys,
			TrySells:       trySells,
			ProfitNow:      profitNow,
			OnAH:           onAH,
			TotalStock:     totalHeld,
			NormalSales:    cfg.NormalSales,
			NormalCount:    stockNorm,
			MinBuyHistory:  minPrice,
			CanRaisePrice:  totalHeld < targetLo && !trySellsBlockUp(sales, trySells) && state.CorridorNoBuyUpStreak < corridorMaxNoBuyUps && buys <= sales && (sales > buys || !overCap) && ((sales > buys && demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) && (state.CorridorUpCooldown == 0 || deepUnderstock(totalHeld, targetLo)) && (state.CorridorUpStreak < corridorMaxUpStreak || deepUnderstock(totalHeld, targetLo))) || (!nightMSK && (state.CorridorUpCooldown == 0 || deepUnderstock(totalHeld, targetLo)) && (state.CorridorUpStreak < corridorMaxUpStreak || deepUnderstock(totalHeld, targetLo)))),
			BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
			PlayersOnline:  onlineForCap,
		}
	}

	rep = AdjustReport{
		Item:           item,
		Action:         actionTaken,
		Reason:         reason,
		Skipped:        false,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   nacenka,
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		NormalSales:    cfg.NormalSales,
		Share:          share,
		Free:           free,
		Need:           need,
		PriceFloor:     sellPriceFloor(minPrice, nacenka),
		Step:           step,
		Cooldown:       state.StockVsSalesCooldown,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		GoodStreak:     state.CorridorUpStreak,
		BlockNacenkaUp: underbuyOK,
	}

	needBroadcast := changed
	mutex.Unlock()

	logCapitalCycle(capitalRow)

	if experimentTG != nil {
		enqueueExperimentTelegram(*experimentTG)
	}

	if mlShadowEnabled() {
		runMLShadowAsync(shadowSnap)
	}

	if needBroadcast {
		publishPriceUpdate()
	}
	saveDailyDataNoMessageUpdate()
	return rep
}
