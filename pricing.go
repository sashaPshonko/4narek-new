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

// stock_corridor_v3 — стратегический контроллер fill=held/share.
//
// v2→v3 (19.07): lo 15%→18%; ↑ veto buys≥sales; soft↓ с hi каждый цикл; Fill в БД.
// Макс. эффективность по fwd: ↑ почти всегда −profit след. цикла; HOLD в under лучше.
// Поэтому ↑ редкий: sales>buys, streak≤1, try-veto, только ниже lo.
const (
	stockBandLoFrac       = 0.18
	stockBandHiFrac       = 0.25
	stockSoftDownFrac     = 0.28
	stockOverFrac         = 0.35
	stockDumpFrac         = 0.50
	corridorMaxUpStreak   = 1 // v3: максимум один ↑ подряд (было 3)
	corridorSoftDownEvery = 1
	tryUpVetoMinTries     = 5
	tryUpVetoPerSale      = 2
)

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

// ItemAdjustState — состояние контроллера цены между циклами.
type ItemAdjustState struct {
	GoodStreak           int  `json:"good_streak"`
	ExperimentCheck      bool `json:"experiment_check"`
	LastCycleProfit      int  `json:"last_cycle_profit"`
	LastCycleNacenkaSum  int  `json:"last_cycle_nacenka_sum"`
	StockVsSalesCooldown int  `json:"stock_vs_sales_cooldown"`
	FillPriceCooldown    int  `json:"fill_price_cooldown"`
	CorridorUpStreak     int  `json:"corridor_up_streak"`     // подряд ↑ в stock_corridor
	CorridorDeadStreak   int  `json:"corridor_dead_streak"`   // подряд sales=0 && buys=0
	CorridorDownCooldown int  `json:"corridor_down_cooldown"` // циклы до следующего soft↓
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
// (stock_corridor_v2 не крутит nacenka; старые capital-значения из daily/runtime сбрасываем).
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

// stockTargets — пороги fill=held/share.
// lo/hi — мёртвая зона (18–25%); soft — ориентир в логах; over — жёсткий ↓; dump — залипший перезапас.
// v3: soft↓ стартует сразу с hi (не HOLD до soft).
func stockTargets(share int) (lo, hi, soft, over, dump int) {
	if share <= 0 {
		return 1, 2, 3, 4, 5
	}
	lo = int(float64(share)*stockBandLoFrac + 0.5)
	hi = int(float64(share)*stockBandHiFrac + 0.5)
	soft = int(float64(share)*stockSoftDownFrac + 0.5)
	over = int(float64(share)*stockOverFrac + 0.5)
	dump = int(float64(share)*stockDumpFrac + 0.5)
	if lo < 1 {
		lo = 1
	}
	if hi <= lo {
		hi = lo + 1
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

// trySellsBlockUp — рынок уже отказывается от цены: ↑ запрещён даже при недоборе стока.
func trySellsBlockUp(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	return trySells >= tryUpVetoPerSale*maxInt(sales, 1)
}

// downWouldUndershoot — ↓ ускорит продажи; если уже один цикл sales утащит ниже lo — не режем.
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
	// minBuy — самая дешёвая покупка из истории (см. getMinPriceFromHistory)
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
	case "corridor_price_down_dump":
		return "corridor_v3: held ≥ 50% share → −цена (dump залипшего перезапаса)"
	case "corridor_price_down_over":
		return "corridor_v3: held ≥ 35% share → −цена (жёсткий перезапас)"
	case "corridor_price_down_soft":
		return "corridor_v3: held > hi → −цена (слив хвоста выше полосы)"
	case "corridor_price_down_hi":
		return "corridor_v3(legacy): held > hi → −цена"
	case "corridor_price_up_demand":
		return "corridor_v3: held < 18% share, sales>buys → +цена (редкий разбор витрины)"
	case "corridor_hold_band":
		return "corridor_v3: held в 18–25% share → hold (мёртвая зона)"
	case "corridor_hold_hysteresis":
		return "corridor_v3(legacy): held 25–28% → hold"
	case "corridor_hold_filling":
		return "corridor_v3: held < lo, идут buys, sales=0 → hold (набираем сток)"
	case "corridor_hold_dead":
		return "corridor_v3: held < lo, sales=0 buys=0 → hold (не разгонять пустоту)"
	case "corridor_hold_up_cap":
		return "corridor_v3: недобор, но лимит ↑ подряд → hold"
	case "corridor_hold_try_veto":
		return "corridor_v3: недобор, но try_sells≫sales → ↑ запрещён"
	case "corridor_hold_buy_veto":
		return "corridor_v3: недобор, но buys≥sales → ↑ запрещён"
	case "corridor_hold_overshoot":
		return "corridor_v3: ↓ пропустил бы ниже lo → hold"
	case "corridor_hold_down_cd":
		return "corridor_v3: soft↓ на cooldown → hold"
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
	if !isMinecraftTypeActiveLocked(cfg.Type) {
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
	_, _, soft, _, _ := stockTargets(share)
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
	// stock_corridor_v3 — стратегический fill-контроллер.
	// Мёртвая зона [lo,hi]≈18–25%: только hold.
	// Сверху hi: soft↓ каждый цикл (не HOLD 25–28%); hard ≥35%; dump ≥50%.
	// ↑ ниже lo: sales>0, sales≥buys, без try-veto, streak<3.
	// ═══════════════════════════════════════════════════════════════════

	targetLo, targetHi, targetSoft, targetOver, targetDump := stockTargets(share)
	stockLoad := 0.0
	if share > 0 {
		stockLoad = float64(totalHeld) / float64(share)
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
		applyDown(label, note)
		if changed && strings.Contains(action, "price_down") {
			// corridorSoftDownEvery=1 → cooldown 0 (↓ снова на след. цикле).
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
		// Залипший перезапас: ↓ каждый цикл без soft-cooldown.
		applyDown("corridor_price_down_dump",
			fmt.Sprintf("held=%d ≥ dump=%d (%.0f%% share=%d)", totalHeld, targetDump, stockLoad*100, share))
		state.CorridorDownCooldown = 0

	case totalHeld >= targetOver:
		if downWouldUndershoot(totalHeld, sales, targetLo) {
			action = "corridor_hold_overshoot"
			notes = append(notes, fmt.Sprintf("held=%d ≥ over=%d но ↓ утащит < lo=%d", totalHeld, targetOver, targetLo))
		} else {
			applyDown("corridor_price_down_over",
				fmt.Sprintf("held=%d ≥ over=%d (%.0f%% share=%d)", totalHeld, targetOver, stockLoad*100, share))
		}

	case totalHeld > targetHi:
		// v3: сразу soft↓ выше hi (раньше 25–28% был чистый HOLD — хвост не сливался).
		trySoftDown("corridor_price_down_soft",
			fmt.Sprintf("held=%d > hi=%d (полоса [%d,%d] soft=%d share=%d)",
				totalHeld, targetHi, targetLo, targetHi, targetSoft, share))

	case totalHeld < targetLo:
		switch {
		case trySellsBlockUp(sales, trySells):
			action = "corridor_hold_try_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d try=%d — рынок не берёт, ↑ запрещён",
				totalHeld, targetLo, sales, trySells))
		case buys >= sales:
			action = "corridor_hold_buy_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d ≥ sales=%d — нет чистого разбора витрины, ↑ запрещён",
				totalHeld, targetLo, buys, sales))
		case sales > 0 && state.CorridorUpStreak < corridorMaxUpStreak:
			applyUp("corridor_price_up_demand",
				fmt.Sprintf("held=%d < lo=%d sales=%d > buys=%d (разбирают витрину)", totalHeld, targetLo, sales, buys))
		case sales > 0 && state.CorridorUpStreak >= corridorMaxUpStreak:
			action = "corridor_hold_up_cap"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d но up_streak=%d≥%d",
				totalHeld, targetLo, sales, state.CorridorUpStreak, corridorMaxUpStreak))
		case buys > 0:
			action = "corridor_hold_filling"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d sales=0 — набираем сток", totalHeld, targetLo, buys))
		default:
			action = "corridor_hold_dead"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=0 buys=0 — не разгонять пустоту", totalHeld, targetLo))
		}

	default:
		// Мёртвая зона [lo, hi]: только hold. Soft↓ внутри полосы убран (выбивал вниз).
		action = "corridor_hold_band"
		notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] share=%d", totalHeld, targetLo, targetHi, share))
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

	// Стрелки коридора + soft↓ cooldown
	if strings.Contains(action, "price_up") {
		state.CorridorUpStreak++
		state.CorridorDeadStreak = 0
		state.CorridorDownCooldown = 0
	} else {
		state.CorridorUpStreak = 0
		if sales == 0 && buys == 0 {
			state.CorridorDeadStreak++
		} else {
			state.CorridorDeadStreak = 0
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
	)

	onlineForCap, _ := fetchOnlineSnapshot()
	logCapitalCycleLocked(CapitalCycleRow{
		Policy:          capitalPolicy,
		Item:            item,
		Category:        cfg.Type,
		Action:          actionTaken,
		Winner:          actionTaken,
		Dump:            0,
		Fill:            stockLoad,
		Skim:            0,
		Threshold:       float64(targetHi),
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
		GoodStreak:      state.CorridorUpStreak,
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
			CanRaisePrice:  totalHeld < targetLo && sales > buys && state.CorridorUpStreak < corridorMaxUpStreak && !trySellsBlockUp(sales, trySells),
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
		GoodStreak:      state.CorridorUpStreak,
		BlockNacenkaUp:  underbuyOK,
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
