package main

import (
	"log"
	"time"
)

const cheapBuyFractionThreshold = 0.65

const (
	marketFloorWindow    = 10 * time.Minute
	marketFloorWindowMin = 9*time.Minute + 30*time.Second
	marketFloorMaxStale  = 2 * time.Minute
)

// ItemAdjustState — streak / эксперимент / прибыль прошлого цикла.
type ItemAdjustState struct {
	GoodStreak      int  `json:"good_streak"`
	ExperimentCheck bool `json:"experiment_check"`
	LastCycleProfit int  `json:"last_cycle_profit"`
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

func cheapBuyFraction(item string, sellPrice, nacenka, step int, since time.Time) (float64, int) {
	hist := priceHistory[item]
	if hist == nil || len(hist.Records) == 0 {
		return 0, 0
	}
	threshold := sellPrice - nacenka - step
	cheap, total := 0, 0
	for _, r := range hist.Records {
		if !r.Time.After(since) {
			continue
		}
		total++
		if r.Price < threshold {
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
func applyMarketFloors(floors map[string]int, windowStartMs, windowEndMs, windowMs int64) {
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
	profitNow := profitInWindow(item, lastUpdate)
	state := data.AdjustState[item]
	profitPrev := state.LastCycleProfit

	newPrice := data.Prices[item]
	priceBefore := newPrice
	nacenka := getNacenka(item)
	nacenkaBefore := nacenka
	minNacenka := resolveNacenkaMin(cfg)
	step := cfg.PriceStep
	minPrice := getMinPriceFromHistory(item)

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

	stockNorm := cfg.NormalSales
	if cfg.NormalCount > 0 {
		stockNorm = cfg.NormalCount
	}

	onAH := ahCounts[item]
	totalStock := onAH + invCounts[item]

	changed := false
	action := ""

	// 1. Переизбыток (как было): много на АХ, мало продаж → цена вниз
	if totalStock > stockNorm && sales < cfg.NormalSales {
		priceFloor := minPrice + nacenka
		if newPrice-step > priceFloor {
			newPrice -= step
			action = "price_down_overstock"
			changed = true
			state.GoodStreak = 0
		}
	} else if sales < cfg.NormalSales {
		// 2. Мало продаж, сток не переизбыток → наценка вниз, иначе цена вверх
		if nacenka > minNacenka {
			nacenka -= step
			action = "nacenka_down_deficit"
			changed = true
			state.GoodStreak = 0
		} else {
			newPrice += step
			action = "price_up_deficit"
			changed = true
			state.GoodStreak = 0
		}
	} else if state.ExperimentCheck {
		// 3. Проверка эксперимента (после +цена и +наценка)
		state.ExperimentCheck = false
		if profitNow < profitPrev {
			if nacenka > minNacenka {
				nacenka -= step
				if nacenka < minNacenka {
					nacenka = minNacenka
				}
			}
			priceFloor := minPrice + nacenka
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
	} else {
		// 4. Streak по прибыли → эксперимент роста
		if profitNow >= profitPrev {
			state.GoodStreak++
			if state.GoodStreak >= 3 {
				newPrice += step
				nacenka += step
				state.GoodStreak = 0
				state.ExperimentCheck = true
				action = "experiment_start"
				changed = true
			}
		} else {
			state.GoodStreak = 0
		}

		// 5. Дешёвые покупки → наценка вверх
		if !changed {
			frac, n := cheapBuyFraction(item, newPrice, nacenka, step, lastUpdate)
			if n > 0 && frac >= cheapBuyFractionThreshold {
				nacenka += step
				action = "nacenka_up_cheap_buys"
				changed = true
			}
		}
	}

	state.LastCycleProfit = profitNow
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

	if changed {
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d→%d | прибыль %d (было %d) | продажи %d | сток %d",
			item, action, priceBefore, newPrice, nacenkaBefore, nacenka, profitNow, profitPrev, sales, totalStock)
	}

	needBroadcast := changed
	mutex.Unlock()

	if needBroadcast {
		publishPriceUpdate()
	}
	saveDailyDataNoMessageUpdate()
}
