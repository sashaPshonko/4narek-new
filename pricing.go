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

// ItemAdjustState — streak / эксперимент / метрики прошлого цикла.
type ItemAdjustState struct {
	GoodStreak           int  `json:"good_streak"`
	ExperimentCheck      bool `json:"experiment_check"`
	LastCycleProfit      int  `json:"last_cycle_profit"`
	LastCycleNacenkaSum  int  `json:"last_cycle_nacenka_sum"` // сумма наценок продаж за прошлый цикл
	StockVsSalesCooldown int  `json:"stock_vs_sales_cooldown"`
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

// skipOverstockPriceDown — сток выше нормы, покупок меньше продаж, есть место докупить:
// не роняем sell (проблема в недокупке, не в переизбытке).
func skipOverstockPriceDown(share, totalHeld, sales, buys, normalSales int) bool {
	if normalSales <= 0 || totalHeld <= normalSales {
		return false
	}
	ok, _, _ := hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
	return ok
}

func adjustPrice(item string) {
	cfg, ok := itemsConfig[item]
	if !ok {
		return
	}

	mutex.Lock()
	now := time.Now()
	swordTimes[item] = now
	lastUpdate := now.Add(-cfg.AnalysisTime)

	if !isMinecraftTypeActiveLocked(cfg.Type) {
		log.Printf("[SKIP] %s: тип %s — нет активных ботов", item, cfg.Type)
		mutex.Unlock()
		return
	}

	if time.Since(data.LastManualUpdate[item]) < cfg.AnalysisTime {
		log.Printf("[SKIP] %s: ручное изменение %v назад, пропускаем анализ",
			item, time.Since(data.LastManualUpdate[item]))
		mutex.Unlock()
		return
	}

	ensureNacenkasInitialized()

	sales := countRecentSales(item, lastUpdate)
	buys := countRecentBuys(item, lastUpdate)
	trySells := countRecentTrySells(item, lastUpdate)
	profitNow := profitInWindow(item, lastUpdate)
	state := data.AdjustState[item]

	tryAdvanceCategoryMLOutcomesLocked(cfg.Type, now)

	newPrice := data.Prices[item]
	priceBefore := newPrice
	nacenka := getNacenka(item)
	nacenkaBefore := nacenka
	minNacenka := resolveNacenkaMin(cfg)
	step := cfg.PriceStep
	minPrice := getMinPriceFromHistory(item)
	nacenkaSumNow := nacenkaSumInWindow(item, lastUpdate)
	nacenkaSumPrev := state.LastCycleNacenkaSum

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

	changed := false
	action := ""
	var experimentTG *experimentTelegramEvent

	// 0. Сначала закрываем эксперимент прошлого цикла (метрика — сумма наценок продаж).
	if state.ExperimentCheck {
		state.ExperimentCheck = false
		if nacenkaSumNow < nacenkaSumPrev {
			if nacenka > minNacenka {
				nacenka -= step
				if nacenka < minNacenka {
					nacenka = minNacenka
				}
			}
			priceFloor := sellPriceFloor(cfg, minPrice, nacenka)
			if newPrice-step > priceFloor {
				newPrice -= step
			} else if newPrice > priceFloor {
				newPrice = priceFloor
			}
			action = "experiment_rollback"
			changed = true
			state.GoodStreak = 0
		} else {
			action = "experiment_ok"
		}
		experimentTG = &experimentTelegramEvent{
			Item:           item,
			Action:         action,
			PriceBefore:    priceBefore,
			PriceAfter:     newPrice,
			NacenkaBefore:  nacenkaBefore,
			NacenkaAfter:   nacenka,
			NacenkaSumNow:  nacenkaSumNow,
			NacenkaSumPrev: nacenkaSumPrev,
			Sales:          sales,
		}
	} else if totalHeld < cfg.NormalSales {
		newPrice += step
		action = "price_up_low_stock"
		changed = true
		state.GoodStreak = 0
	} else {
		share := itemSlotShareLocked(cfg.Type)
		noOverstockDown := skipOverstockPriceDown(share, totalHeld, sales, buys, cfg.NormalSales)

		if !noOverstockDown && onAH > sales && (onAH > cfg.NormalSales || trySells > cfg.NormalSales) && sales < cfg.NormalSales {
			priceFloor := sellPriceFloor(cfg, minPrice, nacenka)
			if newPrice-step >= priceFloor {
				newPrice -= step
				action = "price_down_ah_overstock"
				changed = true
				state.GoodStreak = 0
			}
		} else if state.StockVsSalesCooldown <= 0 && totalHeld > 0 && totalHeld >= sales*3 {
			// наличие ≥ 3× продаж: продажи в норме → наценка вверх; иначе sell вниз
			// (вниз только если onAH или trySells выше нормы — иначе при sales=0 любое наличие бьёт цену)
			if sales >= cfg.NormalSales {
				nacenka += step
				action = "nacenka_up_stock_vs_sales"
				changed = true
				state.GoodStreak = 0
				state.StockVsSalesCooldown = 3
			} else if !noOverstockDown && (onAH > cfg.NormalSales || trySells > cfg.NormalSales) {
				priceFloor := sellPriceFloor(cfg, minPrice, nacenka)
				if newPrice-step >= priceFloor {
					newPrice -= step
					action = "price_down_stock_vs_sales"
					changed = true
					state.GoodStreak = 0
					state.StockVsSalesCooldown = 3
				}
			}
		}

		if !changed && buys < sales && totalHeld < cfg.NormalSales*2 {
			okSpace, free, need := hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
			if okSpace {
				newPrice += step
				action = "price_up_buy_deficit_with_space"
				changed = true
				state.GoodStreak = 0
				log.Printf("[ADJUST] %s: buy_deficit space ok | buys=%d sales=%d need=%d free=%d share=%d held=%d bots=%d items=%d",
					item, buys, sales, need, free, share, totalHeld,
					aggregateBotsPerTypeLocked()[cfg.Type], countItemsInCategoryLocked(cfg.Type))
			}
		}
	}

	// Динамика наценки + старт эксперимента (если цена ещё не менялась).
	if !changed {
		if sales < cfg.NormalSales && nacenka > minNacenka {
			nacenka -= step
			action = "nacenka_down_deficit"
			changed = true
			state.GoodStreak = 0
		} else {
			if nacenkaSumNow >= nacenkaSumPrev {
				state.GoodStreak++
				if state.GoodStreak >= 3 {
					newPrice += step
					nacenka += step
					state.GoodStreak = 0
					state.ExperimentCheck = true
					action = "experiment_start"
					changed = true
					experimentTG = &experimentTelegramEvent{
						Item:           item,
						Action:         action,
						PriceBefore:    priceBefore,
						PriceAfter:     newPrice,
						NacenkaBefore:  nacenkaBefore,
						NacenkaAfter:   nacenka,
						NacenkaSumNow:  nacenkaSumNow,
						NacenkaSumPrev: nacenkaSumPrev,
						Sales:          sales,
					}
				}
			} else {
				state.GoodStreak = 0
			}

			if !changed {
				frac, n := cheapBuyFraction(item, newPrice, nacenka, step, lastUpdate)
				if n > 0 && frac >= cheapBuyFractionThreshold {
					nacenka += step
					action = "nacenka_up_cheap_buys"
					changed = true
				}
			}
		}
		if action == "" {
			action = "hold"
		}
	}

	if action != "nacenka_up_stock_vs_sales" && action != "price_down_stock_vs_sales" && state.StockVsSalesCooldown > 0 {
		state.StockVsSalesCooldown--
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
	if changed {
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d→%d | Σнаценок %d (было %d) | продажи %d | на АХ %d | в инв %d | всего %d",
			item, action, priceBefore, newPrice, nacenkaBefore, nacenka, nacenkaSumNow, nacenkaSumPrev, sales, onAH, invCount, totalHeld)
	}

	queueMLDecisionLocked(
		item, cfg, actionTaken,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
	)

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
}
