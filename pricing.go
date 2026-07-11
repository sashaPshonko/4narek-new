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

// AdjustReport — итог цикла adjustPrice для TG/логов.
type AdjustReport struct {
	Item              string
	Action            string
	Reason            string
	Skipped           bool
	PriceBefore       int
	PriceAfter        int
	NacenkaBefore     int
	NacenkaAfter      int
	Sales             int
	Buys              int
	TrySells          int
	OnAH              int
	Inv               int
	Held              int
	NormalSales       int
	Share             int
	Free              int
	Need              int
	PriceFloor        int
	Step              int
	Cooldown          int
	NacenkaSumNow     int
	NacenkaSumPrev    int
	GoodStreak        int
	BlockNacenkaUp    bool
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

// ItemAdjustState — состояние CAPITAL-контроллера между циклами.
type ItemAdjustState struct {
	GoodStreak           int  `json:"good_streak"`            // тихие циклы с непадающей Σнаценок (для skim)
	ExperimentCheck      bool `json:"experiment_check"`       // проверить прошлый skim в этом цикле
	LastCycleProfit      int  `json:"last_cycle_profit"`
	LastCycleNacenkaSum  int  `json:"last_cycle_nacenka_sum"`
	StockVsSalesCooldown int  `json:"stock_vs_sales_cooldown"` // refractory после dump −P
	FillPriceCooldown    int  `json:"fill_price_cooldown"`    // refractory после +P (v2: не дёргать цену каждый цикл)
}

func resolveNacenkaMin(cfg ItemConfig) int {
	if cfg.NacenkaMin > 0 {
		return cfg.NacenkaMin
	}
	return cfg.Nacenka
}

func getNacenka(item string) int {
	if n, ok := data.Nacenkas[item]; ok && n > 0 {
		return n
	}
	if cfg, ok := itemsConfig[item]; ok {
		return cfg.Nacenka
	}
	return 0
}

func ensureNacenkasInitialized() {
	if data.Nacenkas == nil {
		data.Nacenkas = make(map[string]int)
	}
	if data.AdjustState == nil {
		data.AdjustState = make(map[string]ItemAdjustState)
	}
	for item, cfg := range itemsConfig {
		if _, ok := data.Nacenkas[item]; !ok {
			data.Nacenkas[item] = cfg.Nacenka
			dailyData.Nacenkas[item] = cfg.Nacenka
		}
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

			if !isMinecraftTypeActiveLocked(cfg.Type) {
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

func sellPriceFloor(cfg ItemConfig, minBuy, nacenka int) int {
	floor := minBuy + nacenka
	if cfg.BasePrice > floor {
		floor = cfg.BasePrice
	}
	return floor
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
	case "price_down_buy_surge":
		return "surge: выкуп ≥2×нормы при слабых sales → −цена"
	// legacy aliases (старые логи/БД)
	case "hold":
		return "hold"
	case "skip_inactive":
		return "пропуск: нет активных ботов этого типа"
	case "skip_manual":
		return "пропуск: min/max от оркестратора — цикл на паузе"
	case "experiment_ok", "experiment_rollback", "experiment_start":
		return action
	default:
		if action == "" {
			return "нет решения"
		}
		return action
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

// maybeBuySurgePriceDownLocked — на каждый buy: отдельный счётчик +=1;
// если счётчик ≥ 2×normal и sales < normal → −step, счётчик = 0.
// Сделки/статы основного цикла не трогаем. Только под mutex.Lock.
func maybeBuySurgePriceDownLocked(item string) BuySurgeEvent {
	ev := BuySurgeEvent{Item: item}
	cfg, ok := itemsConfig[item]
	if !ok || cfg.NormalSales <= 0 || cfg.PriceStep <= 0 {
		return ev
	}
	if !isMinecraftTypeActiveLocked(cfg.Type) {
		return ev
	}
	if time.Since(data.LastManualUpdate[item]) < cfg.AnalysisTime {
		return ev
	}

	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	data.BuySurgeCount[item]++
	surgeCount := data.BuySurgeCount[item]
	threshold := cfg.NormalSales * 2

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

	if surgeCount < threshold || sales >= cfg.NormalSales {
		return ev
	}

	priceBefore := data.Prices[item]
	if priceBefore <= 0 {
		return ev
	}
	nacenka := getNacenka(item)
	minBuy := getMinPriceFromHistory(item)
	floor := sellPriceFloor(cfg, minBuy, nacenka)
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
	// сброс только surge-счётчика — цикл/TradeHistory не трогаем
	data.BuySurgeCount[item] = 0

	ev.Dropped = true
	ev.PriceBefore = priceBefore
	ev.PriceAfter = newPrice
	log.Printf("[SURGE] %s: surge=%d thr=%d sales=%d/%d | цена %d→%d | surge→0",
		item, surgeCount, threshold, sales, cfg.NormalSales, priceBefore, newPrice)
	return ev
}

func adjustPrice(item string) AdjustReport {
	cfg, ok := itemsConfig[item]
	if !ok {
		return AdjustReport{Item: item, Action: "skip", Reason: "нет в items_config", Skipped: true}
	}

	mutex.Lock()
	now := time.Now()
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

	if !isMinecraftTypeActiveLocked(cfg.Type) {
		log.Printf("[SKIP] %s: тип %s — нет активных ботов", item, cfg.Type)
		mutex.Unlock()
		rep.Action = "skip_inactive"
		rep.Reason = actionReasonRU(rep.Action)
		rep.Skipped = true
		return rep
	}

	if time.Since(data.LastManualUpdate[item]) < cfg.AnalysisTime {
		ago := time.Since(data.LastManualUpdate[item])
		log.Printf("[SKIP] %s: ручное изменение %v назад, пропускаем анализ", item, ago)
		price := data.Prices[item]
		nac := getNacenka(item)
		mutex.Unlock()
		rep.Action = "skip_manual"
		rep.Reason = actionReasonRU(rep.Action) + fmt.Sprintf(" (%v назад)", ago.Round(time.Second))
		rep.Skipped = true
		rep.PriceBefore, rep.PriceAfter = price, price
		rep.NacenkaBefore, rep.NacenkaAfter = nac, nac
		return rep
	}

	ensureNacenkasInitialized()

	sales := countRecentSales(item, lastUpdate)
	buys := countRecentBuys(item, lastUpdate)
	trySells := countRecentTrySells(item, lastUpdate)
	profitNow := profitInWindow(item, lastUpdate)
	state := data.AdjustState[item]

	tryAdvanceCategoryMLOutcomesLocked(cfg.Type, now)
	tryAdvanceCapitalForwardsLocked(now)

	newPrice := data.Prices[item]
	priceBefore := newPrice
	nacenka := getNacenka(item)
	nacenkaBefore := nacenka
	minNacenka := resolveNacenkaMin(cfg)
	step := cfg.PriceStep
	minPrice := getMinPriceFromHistory(item)
	nacenkaSumNow := nacenkaSumInWindow(item, lastUpdate)
	nacenkaSumPrev := state.LastCycleNacenkaSum
	priceFloor := sellPriceFloor(cfg, minPrice, nacenka)

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
	// CAPITAL v3 — SOLO vs MULTI (БД 2026-07-12).
	//
	// Ботинки: sell avg 1.5M→1.1M. Dump задавил, потом hold на 1.1M при
	// sales=13..19 — цены ЗАНИЖЕНЫ, деньги на столе.
	//
	// SOLO (шлем/ботинки/штаны/нагрудник):
	//   • underbuy по target=normal, не share=96
	//   • dump только если продажи МЁРТВЫЕ (≤ normal/2)
	//   • demand → +P когда sales ≥ нормы (поднять цену при спросе)
	//   • +P от underbuy запрещён
	// MULTI: hog → dump; fill_price только не-hog.
	// ═══════════════════════════════════════════════════════════════════

	normal := cfg.NormalSales
	if normal < 1 {
		normal = 1
	}
	target := normal
	nCatItems := countItemsInCategoryLocked(cfg.Type)
	solo := nCatItems <= 1
	stepN := step
	stepP := step
	canMoveP := state.StockVsSalesCooldown <= 0

	underbuyOK := false
	if buys < sales {
		need = sales - buys
		if solo {
			free = target - totalHeld
			if free < 0 {
				free = 0
			}
			underbuyOK = free > 0 && totalHeld < target
		} else {
			var freeDef, needDef int
			underbuyOK, freeDef, needDef = hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
			if underbuyOK {
				free, need = freeDef, needDef
			} else {
				free = share - totalHeld
				if free < 0 {
					free = 0
				}
			}
		}
	}

	catHeldSum := 0
	siblingMaxHeld := 0
	for name, conf := range itemsConfig {
		if conf.Type != cfg.Type {
			continue
		}
		h := ahCounts[name] + invCounts[name]
		catHeldSum += h
		if name != item && h > siblingMaxHeld {
			siblingMaxHeld = h
		}
	}
	hog := !solo && totalHeld >= target*2 && totalHeld > siblingMaxHeld+2

	tryRatio := 0.0
	if sales > 0 {
		tryRatio = float64(trySells) / float64(sales)
	} else if trySells > 0 {
		tryRatio = float64(trySells)
	}
	salesGap := float64(normal-sales) / float64(normal)
	if salesGap < 0 {
		salesGap = 0
	}
	stockLoad := float64(totalHeld) / float64(target)
	emptyFrac := 0.0
	if totalHeld < target {
		emptyFrac = float64(target-totalHeld) / float64(target)
	}
	buyGap := 0.0
	if sales > buys {
		buyGap = float64(sales-buys) / float64(sales)
	}

	const actThreshold = 1.15
	const fillPriceMinScore = 2.0
	const dumpCDBypassScore = 2.5
	const dumpCDBypassLoad = 3.0

	// DUMP
	dumpScore := 0.0
	if solo {
		// Только мёртвые продажи — иначе давим цену при живом спросе.
		deadSales := sales <= (normal / 2)
		if deadSales && totalHeld > 1 {
			if sales == 0 {
				dumpScore += 1.6
			} else {
				dumpScore += 1.0
			}
			if stockLoad > 2.0 {
				dumpScore += 0.6 * (stockLoad - 2.0)
			}
			if tryRatio >= 2.0 {
				dumpScore += 0.5
			}
		}
	} else if sales < normal {
		if tryRatio >= 2.0 {
			dumpScore += 1.2
		} else if trySells > normal {
			dumpScore += 0.8
		}
		if onAH > normal {
			dumpScore += 0.7
		}
		dumpScore += 0.6 * salesGap
		if stockLoad > 1.5 {
			dumpScore += 0.5 * (stockLoad - 1.5)
		}
	}
	if hog {
		dumpScore += 0.9
		notes = append(notes, fmt.Sprintf("hog held=%d sibMax=%d cat=%d", totalHeld, siblingMaxHeld, catHeldSum))
	}
	if solo && totalHeld <= 1 {
		dumpScore = 0
	}
	// Тонкий сток (load<1): dump = кринж. try/s высокий при 2 шт на АХ ≠ «затоварились».
	if !hog && stockLoad < 1.0 {
		if dumpScore > 0 {
			notes = append(notes, "thin stock → dump↓")
		}
		dumpScore = 0
	}

	// FILL (−N / restock). Пустая полка SOLO (held=0, sales=0) — не hold:
	// нечего продавать → сначала наполняем, иначе вечный простой.
	fillScore := 0.0
	if underbuyOK {
		fillScore += 1.4 + 0.8*buyGap
	}
	if emptyFrac > 0.4 && sales >= buys && sales > 0 {
		fillScore += 0.9 * emptyFrac
	}
	if solo && totalHeld < target {
		fillScore += 1.3 + 1.0*emptyFrac
		if totalHeld == 0 {
			fillScore += 0.5
			notes = append(notes, "solo empty shelf → restock")
		}
	}
	// мёртвый рынок только если сток уже есть, а сделок ноль
	if sales == 0 && trySells == 0 && buys == 0 && totalHeld >= target {
		fillScore = 0
	}
	if hog {
		fillScore *= 0.4
	}

	// DEMAND (+P): живой спрос / дефицит → поднимаем цену, не dump/hold.
	demandScore := 0.0
	if canMoveP && state.FillPriceCooldown <= 0 {
		if solo && sales >= normal {
			demandScore = 1.2
			if sales >= normal+normal/2 {
				demandScore += 0.5
			}
			if sales >= normal*2 {
				demandScore += 0.5
			}
			if sales > buys {
				demandScore += 0.4
			}
			if stockLoad >= 2.0 {
				demandScore += 0.3
			}
		}
		// scarcity: мало на витрине, но рынок шевелится → +P (multi кирка held=2 load=0.5)
		if stockLoad < 1.0 && totalHeld > 0 && (sales > 0 || buys > 0 || trySells > 0) {
			scarce := 1.25
			if tryRatio >= 1.5 {
				scarce += 0.3 // смотрят/пытаются — товар редкий, не дешевеем
			}
			if scarce > demandScore {
				demandScore = scarce
				notes = append(notes, "scarcity → +P")
			}
		}
	} else if state.FillPriceCooldown > 0 && solo {
		notes = append(notes, fmt.Sprintf("demand cd=%d", state.FillPriceCooldown))
	}

	// SKIM (+N)
	skimScore := 0.0
	cheapFrac, cheapN := 0.0, 0
	maxLoadForSkim := 1.8
	if solo {
		maxLoadForSkim = 3.0
	}
	flowOK := sales >= normal && buys >= (normal+1)/2 && buys >= sales-2 && stockLoad <= maxLoadForSkim && stockLoad >= 0.4
	if flowOK && !underbuyOK {
		cheapFrac, cheapN = cheapBuyFraction(item, newPrice, nacenka, step, lastUpdate)
		if cheapN >= 3 && cheapFrac >= cheapBuyFractionThreshold {
			skimScore = 1.0 + cheapFrac
			notes = append(notes, fmt.Sprintf("cheap=%.0f%%/%d", cheapFrac*100, cheapN))
		} else if nacenkaSumNow > nacenkaSumPrev && nacenkaSumPrev > 0 && state.GoodStreak >= 2 {
			skimScore = 0.85
			if solo {
				skimScore = 1.05
			}
		}
	}

	winner := "hold"
	dumpBlockedCD := false

	shapeTag := "multi"
	if solo {
		shapeTag = "solo"
	}
	notes = append(notes, fmt.Sprintf("%s dump=%.2f fill=%.2f demand=%.2f skim=%.2f try/s=%.2f load=%.2f",
		shapeTag, dumpScore, fillScore, demandScore, skimScore, tryRatio, stockLoad))

	if state.ExperimentCheck {
		state.ExperimentCheck = false
		winner = "rollback"
		if nacenkaSumNow < nacenkaSumPrev {
			if nacenka > minNacenka {
				nacenka -= stepN
				if nacenka < minNacenka {
					nacenka = minNacenka
				}
			}
			action = "capital_rollback"
			changed = true
			state.GoodStreak = 0
			notes = append(notes, "skim не окупился → −N")
		} else {
			notes = append(notes, "skim ок, оставляем")
			action = "capital_hold"
		}
	} else {
		best := "hold"
		bestScore := actThreshold
		if dumpScore >= bestScore {
			best, bestScore = "dump", dumpScore
		}
		if fillScore > bestScore {
			best, bestScore = "fill", fillScore
		}
		if demandScore > bestScore {
			best, bestScore = "demand", demandScore
		}
		if skimScore > bestScore {
			best, bestScore = "skim", skimScore
		}
		winner = best

		switch best {
		case "dump":
			dumpBypass := dumpScore >= dumpCDBypassScore || stockLoad >= dumpCDBypassLoad || hog
			if !canMoveP && !dumpBypass {
				dumpBlockedCD = true
				notes = append(notes, fmt.Sprintf("dump на cd=%d → next lever", state.StockVsSalesCooldown))
				// не hold: выбираем следующий рычаг без dump
				best = "hold"
				bestScore = actThreshold
				if fillScore > bestScore {
					best, bestScore = "fill", fillScore
				}
				if demandScore > bestScore {
					best, bestScore = "demand", demandScore
				}
				if skimScore > bestScore {
					best, bestScore = "skim", skimScore
				}
				winner = best
				switch best {
				case "fill":
					goto doFill
				case "demand":
					goto doDemand
				case "skim":
					goto doSkim
				default:
					action = "capital_hold"
				}
			} else {
				if !canMoveP && dumpBypass {
					notes = append(notes, fmt.Sprintf("dump cd bypass (score=%.2f load=%.2f hog=%v)", dumpScore, stockLoad, hog))
				}
				priceFloor = sellPriceFloor(cfg, minPrice, nacenka)
				if newPrice-stepP >= priceFloor {
					newPrice -= stepP
					action = "capital_dump"
					changed = true
					state.GoodStreak = 0
					state.StockVsSalesCooldown = 2
				} else {
					notes = append(notes, fmt.Sprintf("dump упёрся в пол %d", priceFloor))
					action = "capital_hold"
				}
			}
		case "fill":
			goto doFill
		case "demand":
			goto doDemand
		case "skim":
			goto doSkim
		default:
			action = "capital_hold"
			if nacenkaSumNow >= nacenkaSumPrev {
				state.GoodStreak++
			} else {
				state.GoodStreak = 0
			}
		}
	}
	goto afterAction

doFill:
	if nacenka > minNacenka {
		nacenka -= stepN
		if nacenka < minNacenka {
			nacenka = minNacenka
		}
		action = "capital_fill"
		changed = true
		state.GoodStreak = 0
	} else if solo && totalHeld < target {
		newPrice += stepP
		action = "capital_fill_price"
		changed = true
		state.GoodStreak = 0
		state.FillPriceCooldown = 1
		notes = append(notes, "solo restock: N мин → +P (buy ceiling)")
	} else if underbuyOK && !solo {
		switch {
		case hog:
			notes = append(notes, "multi hog: fill_price запрещён → hold")
			action = "capital_hold"
		case fillScore < fillPriceMinScore:
			notes = append(notes, fmt.Sprintf("fill_price слабо (%.2f<%.2f) → hold", fillScore, fillPriceMinScore))
			action = "capital_hold"
		case state.FillPriceCooldown > 0:
			notes = append(notes, fmt.Sprintf("fill_price на cd=%d → hold", state.FillPriceCooldown))
			action = "capital_hold"
		default:
			newPrice += stepP
			action = "capital_fill_price"
			changed = true
			state.GoodStreak = 0
			state.FillPriceCooldown = 3
		}
	} else if !solo && totalHeld < target && stockLoad < 1.0 {
		// multi thin: N на полу → всё равно +P (scarcity/restock)
		newPrice += stepP
		action = "capital_fill_price"
		changed = true
		state.GoodStreak = 0
		state.FillPriceCooldown = 1
		notes = append(notes, "multi thin: N мин → +P")
	} else {
		notes = append(notes, "fill: N на мин → hold")
		action = "capital_hold"
	}
	goto afterAction

doDemand:
	newPrice += stepP
	action = "capital_fill_price"
	changed = true
	state.GoodStreak = 0
	state.FillPriceCooldown = 1
	notes = append(notes, "demand → +P")
	goto afterAction

doSkim:
	nacenka += stepN
	action = "capital_skim"
	changed = true
	state.GoodStreak = 0
	state.ExperimentCheck = true
	goto afterAction

afterAction:

	if action != "capital_dump" && state.StockVsSalesCooldown > 0 {
		state.StockVsSalesCooldown--
	}
	if action != "capital_fill_price" && state.FillPriceCooldown > 0 {
		state.FillPriceCooldown--
	}

	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	if nacenka != nacenkaBefore {
		data.Nacenkas[item] = nacenka
		dailyData.Nacenkas[item] = nacenka
	}
	if newPrice != priceBefore {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
	}

	actionTaken := action
	if actionTaken == "" {
		actionTaken = "hold"
	}
	reason := actionReasonRU(actionTaken)
	if len(notes) > 0 {
		reason = reason + " | " + strings.Join(notes, " · ")
	}

	if changed {
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d→%d | Σнаценок %d (было %d) | продажи %d | на АХ %d | в инв %d | всего %d | %s",
			item, action, priceBefore, newPrice, nacenkaBefore, nacenka, nacenkaSumNow, nacenkaSumPrev, sales, onAH, invCount, totalHeld, reason)
	} else {
		log.Printf("[HOLD] %s: %s | цена %d | наценка %d | продажи %d buys=%d try=%d | АХ %d инв %d | %s",
			item, actionTaken, newPrice, nacenka, sales, buys, trySells, onAH, invCount, reason)
	}

	queueMLDecisionLocked(
		item, cfg, actionTaken,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
	)

	onlineForCap, _ := fetchOnlineSnapshot()
	logCapitalCycleLocked(CapitalCycleRow{
		Policy:          capitalPolicy,
		Item:            item,
		Category:        cfg.Type,
		Action:          actionTaken,
		Winner:          winner,
		Dump:            dumpScore,
		Fill:            fillScore,
		Skim:            skimScore,
		Threshold:       actThreshold,
		Sales:           sales,
		Buys:            buys,
		TrySells:        trySells,
		OnAH:            onAH,
		Inv:             invCount,
		Held:            totalHeld,
		Share:           share,
		Free:            free,
		Need:            need,
		NormalSales:     cfg.NormalSales,
		NormalCount:     stockNorm,
		TryRatio:        tryRatio,
		StockLoad:       stockLoad,
		Underbuy:        underbuyOK,
		PriceBefore:     priceBefore,
		PriceAfter:      newPrice,
		NacenkaBefore:   nacenkaBefore,
		NacenkaAfter:    nacenka,
		NacenkaSumNow:   nacenkaSumNow,
		NacenkaSumPrev:  nacenkaSumPrev,
		PriceFloor:      sellPriceFloor(cfg, minPrice, nacenka),
		Step:            step,
		Cooldown:        state.StockVsSalesCooldown,
		PlayersOnline:   onlineForCap,
		Notes:           strings.Join(notes, " · "),
		ProfitNow:       profitNow,
		CheapFrac:       cheapFrac,
		CheapN:          cheapN,
		MinBuyHistory:   minPrice,
		BotsCategory:    aggregateBotsPerTypeLocked()[cfg.Type],
		CycleMinutes:    cfg.AnalysisTime.Minutes(),
		GoodStreak:      state.GoodStreak,
		DumpBlockedCD:   dumpBlockedCD,
		DecisionAt:      now,
		CycleDuration:   cfg.AnalysisTime,
	})

	shadowSnap := mlAdjustSnapshot{}
	if mlShadowEnabled() {
		online, _ := fetchOnlineSnapshot()
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
			CanRaisePrice:  totalHeld < cfg.NormalSales,
			BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
			PlayersOnline:  online,
		}
	}

	rep = AdjustReport{
		Item:            item,
		Action:          actionTaken,
		Reason:          reason,
		Skipped:         false,
		PriceBefore:     priceBefore,
		PriceAfter:      newPrice,
		NacenkaBefore:   nacenkaBefore,
		NacenkaAfter:    nacenka,
		Sales:           sales,
		Buys:            buys,
		TrySells:        trySells,
		OnAH:            onAH,
		Inv:             invCount,
		Held:            totalHeld,
		NormalSales:     cfg.NormalSales,
		Share:           share,
		Free:            free,
		Need:            need,
		PriceFloor:      sellPriceFloor(cfg, minPrice, nacenka),
		Step:            step,
		Cooldown:        state.StockVsSalesCooldown,
		NacenkaSumNow:   nacenkaSumNow,
		NacenkaSumPrev:  nacenkaSumPrev,
		GoodStreak:     state.GoodStreak,
		BlockNacenkaUp: underbuyOK,
	}

	needBroadcast := changed
	mutex.Unlock()

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
