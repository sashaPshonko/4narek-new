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
	if cfg, ok := itemsConfig[item]; ok {
		return cfg.Nacenka
	}
	return 0
}

// ensureNacenkasInitialized — всегда выравнивает рантайм-наценки под items_config
// (classic не крутит nacenka; старые capital-значения из daily/runtime сбрасываем).
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
	// minBuy — N-я дешёвая покупка из истории (см. getMinPriceFromHistory), не абсолютный минимум
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
	case "classic_price_up":
		return "classic: мало sales и запас < 3×нормы → +цена"
	case "classic_price_down_weak_sales":
		return "classic: АХ > sales и АХ > нормы при слабых sales → −цена"
	case "classic_price_down_buy_excess":
		return "classic: buys > 2×sales и запас > нормы → −цена"
	case "classic_price_down_leader":
		return "classic: лидер категории, запас > 3.5×sales → −цена"
	case "price_down_buy_surge":
		return "surge: выкуп ≥2×нормы при слабых sales → −цена"
	// legacy capital (старые логи/БД)
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
	// CLASSIC 2026-02-22 15:46 (commit b96739c5) — лучшее ценообразование.
	// Только цена; наценка не трогаем. Floor = min buy + nacenka (и base).
	// Min/max оркестратора — skip_manual выше. Логи / capital_cycles / ML — ниже.
	// ═══════════════════════════════════════════════════════════════════

	normal := cfg.NormalSales
	if normal < 1 {
		normal = 1
	}
	totalStock := totalHeld

	leaderID := ""
	maxTotal := -1
	for name, conf := range itemsConfig {
		if conf.Type != cfg.Type {
			continue
		}
		total := ahCounts[name] + invCounts[name]
		if total > maxTotal || (total == maxTotal && (leaderID == "" || name < leaderID)) {
			maxTotal = total
			leaderID = name
		}
	}

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
	stockLoad := float64(totalHeld) / float64(normal)

	applyDown := func(label, note string) {
		cand := priceBefore - step
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
		}
	}

	// 1. Повышение — мало продаж и запас < 3×нормы
	if sales < normal && totalStock < normal*3 {
		newPrice = priceBefore + step
		action = "classic_price_up"
		changed = true
		notes = append(notes, "sales < normal && stock < 3*normal")
	} else if (onAH > sales && onAH > normal) && sales < normal {
		// 2. Снижение при плохих продажах (смотрим АХ)
		applyDown("classic_price_down_weak_sales", "onAH > sales && onAH > normal && sales < normal")
	} else if float64(buys) > float64(sales)*2 && totalStock > normal {
		// 3. Снижение при избытке покупок
		applyDown("classic_price_down_buy_excess", "buys > 2*sales && stock > normal")
	} else if item == leaderID {
		// 4. Перенасыщение 3.5× — только лидер категории (AH+INV)
		salesLeader := normal
		if sales > normal {
			salesLeader = sales
		}
		if float64(totalStock) > float64(salesLeader)*3.5 {
			applyDown("classic_price_down_leader", fmt.Sprintf("leader stock>%.1f×%d", 3.5, salesLeader))
		}
	}

	if action == "" {
		action = "hold"
	}

	if state.StockVsSalesCooldown > 0 {
		state.StockVsSalesCooldown--
	}
	if state.FillPriceCooldown > 0 {
		state.FillPriceCooldown--
	}

	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	// classic не меняет наценку
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
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d→%d | Σнаценок %d (было %d) | продажи %d | на АХ %d | в инв %d | всего %d | лидер %s | %s",
			item, action, priceBefore, newPrice, nacenkaBefore, nacenka, nacenkaSumNow, nacenkaSumPrev, sales, onAH, invCount, totalHeld, leaderID, reason)
	} else {
		log.Printf("[HOLD] %s: %s | цена %d | наценка %d | продажи %d buys=%d try=%d | АХ %d инв %d | лидер %s | %s",
			item, actionTaken, newPrice, nacenka, sales, buys, trySells, onAH, invCount, leaderID, reason)
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
		Winner:          actionTaken,
		Dump:            0,
		Fill:            0,
		Skim:            0,
		Threshold:       0,
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
		PriceFloor:      priceFloor,
		Step:            step,
		Cooldown:        state.StockVsSalesCooldown,
		PlayersOnline:   onlineForCap,
		Notes:           strings.Join(notes, " · "),
		ProfitNow:       profitNow,
		CheapFrac:       0,
		CheapN:          0,
		MinBuyHistory:   minPrice,
		BotsCategory:    aggregateBotsPerTypeLocked()[cfg.Type],
		CycleMinutes:    cfg.AnalysisTime.Minutes(),
		GoodStreak:      state.GoodStreak,
		DumpBlockedCD:   false,
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
