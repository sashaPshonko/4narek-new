package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Динамическая цель запаса: сколько штук товара держать, чтобы сливать лот за
// stockHoldTargetMinutes, с учётом онлайна и ограниченных слотов бота.
// Цель заменяет фиксированную норму N там, где решается «хватит ли запаса» (фриз закупки).
//
//	спрос    = ceil(продаж_в_час × поправка_онлайна × stockHoldTargetMinutes/60), минимум 1
//	ценность = маржа × min(60/stockHoldTargetMinutes, продаж_в_час)  // прибыль в час на слот
//
// Каждому товару гарантирована 1 штука (чтобы цена и спрос продолжали замеряться и
// ни один товар не отключался молча). Если сумма спроса не влезает в бюджет слотов
// (боты × stockSlotsPerBot), сверх этой одной штуки слоты раздаются по убыванию
// ценности: слабый товар не мешает сильному, но и не выключается.
const (
	// 60 мин ≈ средний реальный слив (по логам: среднее удержание ~1 ч, медиана ~25 мин).
	// 90 мин дало бы запас втрое больше, чем есть сейчас. Подстраивать по данным.
	stockHoldTargetMinutes = 60.0
	stockRateWindow        = 3 * time.Hour
	stockMinObserved       = 20 * time.Minute
	stockOnlineElasticity  = 0.58 // спрос ∝ онлайн^0.58 (по данным за ~1,3 суток; пересмотреть)
	stockOnlineFactorMin   = 0.5
	stockOnlineFactorMax   = 1.5
	// stockSlotsPerBot — слотов на бота под запас: 5 листингов АХ + 1 запас в инвентаре. Цель — забить АХ
	// выгодным товаром, инвентарь забивать не нужно.
	stockSlotsPerBot = 6
	// stockSlotBudgetOn — цели урезаются по бюджету слотов (боты × stockSlotsPerBot): слоты отдаются по
	// убыванию ценности (маржа × скорость слива), каждому товару минимум 1 шт. По вчерашним данным
	// суммарный спрос ≈106 шт/ч против бюджета 36–72 слотов: полный спрос получают 20–26 из 34 товаров,
	// остальные держатся на 1 шт.
	stockSlotBudgetOn = true
	stockMaxPerItem   = 60
	// Давление на слоты: доля занятых листингов АХ (5 на бота). Когда оно выше порога,
	// слабые товары (ценность ниже средней по категории) копят только норму N, без надбавки
	// цели T, чтобы не занимать слоты, которые нужны дорогим.
	stockAHSlotsPerBot   = 5
	stockSqueezePressure = 0.85
	stockTargetsFreshFor = 45 * time.Second
)

type stockTargetInfo struct {
	Target       int
	Demand       int
	RatePerHour  float64
	OnlineFactor float64
	Margin       float64
	Value        float64
	Budget       int
	AvgBuy       float64 // средняя цена покупки за окно (0 — покупок не было)
	Squeezed     bool    // слабый товар при давлении на слоты: цель T не применяется, остаётся норма N
}

type stockItemStat struct {
	Item          string
	Type          string
	Sales         int
	SellSum       int
	SellCount     int
	BuySum        int
	BuyCount      int
	Nacenka       int
	ObservedHours float64
}

var (
	stockTargetsMu    sync.Mutex
	stockTargetsCache map[string]stockTargetInfo
	stockTargetsAt    time.Time
)

// --- онлайн ---

type onlineSample struct {
	At     time.Time
	Online int
}

var (
	onlineSamplesMu sync.Mutex
	onlineSamples   []onlineSample
)

// recordOnlineSample — вызывается там, где уже опрашивается онлайн сервера.
func recordOnlineSample(at time.Time, online int) {
	if online <= 0 {
		return
	}
	onlineSamplesMu.Lock()
	defer onlineSamplesMu.Unlock()
	if n := len(onlineSamples); n > 0 && at.Sub(onlineSamples[n-1].At) < time.Minute {
		onlineSamples[n-1] = onlineSample{At: at, Online: online}
	} else {
		onlineSamples = append(onlineSamples, onlineSample{At: at, Online: online})
	}
	cut := at.Add(-stockRateWindow - time.Hour)
	i := 0
	for i < len(onlineSamples) && onlineSamples[i].At.Before(cut) {
		i++
	}
	if i > 0 {
		onlineSamples = append([]onlineSample(nil), onlineSamples[i:]...)
	}
}

// stockOnlineFactor — во сколько раз спрос сейчас отличается от среднего за окно.
func stockOnlineFactor(latest, avg float64) float64 {
	if latest <= 0 || avg <= 0 {
		return 1
	}
	f := math.Pow(latest/avg, stockOnlineElasticity)
	return math.Max(stockOnlineFactorMin, math.Min(stockOnlineFactorMax, f))
}

func currentOnlineFactor(now time.Time) float64 {
	onlineSamplesMu.Lock()
	defer onlineSamplesMu.Unlock()
	from := now.Add(-stockRateWindow)
	var sum float64
	var n int
	var first, last time.Time
	var latest int
	for _, s := range onlineSamples {
		if s.At.Before(from) {
			continue
		}
		if n == 0 {
			first = s.At
		}
		last, latest = s.At, s.Online
		sum += float64(s.Online)
		n++
	}
	if n < 5 || last.Sub(first) < 30*time.Minute || now.Sub(last) > 15*time.Minute {
		return 1
	}
	return stockOnlineFactor(float64(latest), sum/float64(n))
}

// --- чистая математика ---

func stockDemand(ratePerHour, onlineFactor float64) int {
	d := int(math.Ceil(ratePerHour * onlineFactor * stockHoldTargetMinutes / 60))
	if d < 1 {
		d = 1
	}
	if d > stockMaxPerItem {
		d = stockMaxPerItem
	}
	return d
}

type stockCand struct {
	Item   string
	Demand int
	Value  float64
}

// allocateStockTargets — по одной штуке каждому, остаток бюджета по убыванию ценности.
func allocateStockTargets(cands []stockCand, budget int) map[string]int {
	out := make(map[string]int, len(cands))
	sorted := append([]stockCand(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Value != sorted[j].Value {
			return sorted[i].Value > sorted[j].Value
		}
		return sorted[i].Item < sorted[j].Item
	})
	floors := 0
	for _, c := range sorted {
		if c.Demand >= 1 {
			out[c.Item] = 1
			floors++
		} else {
			out[c.Item] = 0
		}
	}
	left := budget - floors
	for _, c := range sorted {
		if left <= 0 {
			break
		}
		extra := c.Demand - 1
		if extra > left {
			extra = left
		}
		if extra > 0 {
			out[c.Item] += extra
			left -= extra
		}
	}
	return out
}

// lendUnusedSlots — слоты, закреплённые за товаром, который не набирается (редкие лоты, рынок пустой),
// не должны простаивать, пока ходовой товар упёрся в свою цель. Половина неиспользованной части
// (цель минус наличие у товаров, не дотянувших до цели) отдаётся товарам, которые цели уже достигли и
// ещё не вышли на свой спрос, по убыванию ценности. Цель недобравшего не трогаем: если он набралось,
// он снова упрётся в свою цель, а общее число слотов регулирует фриз по ёмкости.
func lendUnusedSlots(targets map[string]int, demand map[string]int, value map[string]float64, held map[string]int) map[string]int {
	out := make(map[string]int, len(targets))
	slack := 0
	for item, t := range targets {
		out[item] = t
		if h := held[item]; h < t {
			slack += t - h
		}
	}
	lend := slack / 2
	if lend <= 0 {
		return out
	}
	var takers []string
	for item, t := range targets {
		if held[item] >= t && demand[item] > t {
			takers = append(takers, item)
		}
	}
	sort.SliceStable(takers, func(i, j int) bool {
		if value[takers[i]] != value[takers[j]] {
			return value[takers[i]] > value[takers[j]]
		}
		return takers[i] < takers[j]
	})
	for _, item := range takers {
		if lend <= 0 {
			break
		}
		add := demand[item] - out[item]
		if add > lend {
			add = lend
		}
		out[item] += add
		lend -= add
	}
	return out
}

// buildStockTargets — цели для одной категории (одного бюджета слотов).
func buildStockTargets(items []stockItemStat, budget int, onlineFactor float64) map[string]stockTargetInfo {
	cands := make([]stockCand, 0, len(items))
	info := make(map[string]stockTargetInfo, len(items))
	for _, it := range items {
		hours := it.ObservedHours
		if hours <= 0 {
			continue
		}
		rate := float64(it.Sales) / hours
		eff := rate * onlineFactor
		demand := stockDemand(rate, onlineFactor)
		margin := float64(it.Nacenka)
		if it.SellCount > 0 && it.BuyCount > 0 {
			margin = float64(it.SellSum)/float64(it.SellCount) - float64(it.BuySum)/float64(it.BuyCount)
		}
		if margin < 0 {
			margin = 0
		}
		value := margin * math.Min(60/stockHoldTargetMinutes, eff)
		cands = append(cands, stockCand{Item: it.Item, Demand: demand, Value: value})
		avgBuy := 0.0
		if it.BuyCount > 0 {
			avgBuy = float64(it.BuySum) / float64(it.BuyCount)
		}
		info[it.Item] = stockTargetInfo{Demand: demand, RatePerHour: rate, OnlineFactor: onlineFactor,
			Margin: margin, Value: value, Budget: budget, AvgBuy: avgBuy}
	}
	for item, t := range allocateStockTargets(cands, budget) {
		x := info[item]
		x.Target = t
		info[item] = x
	}
	return info
}

// squeezeWeakTargets — слабым товарам (ценность ниже средней по категории) при давлении на слоты
// отключает цель сверх нормы. Товары без продаж (ценность 0) в среднем не участвуют и не режутся.
func squeezeWeakTargets(infos map[string]stockTargetInfo, pressure float64) {
	if pressure < stockSqueezePressure {
		return
	}
	var sum float64
	var n int
	for _, inf := range infos {
		if inf.Value > 0 {
			sum += inf.Value
			n++
		}
	}
	if n == 0 {
		return
	}
	mean := sum / float64(n)
	for item, inf := range infos {
		if inf.Value > 0 && inf.Value < mean {
			inf.Squeezed = true
			infos[item] = inf
		}
	}
}

// --- сбор данных (mutex взят) ---

func refreshStockTargetsLocked(now time.Time) {
	stockTargetsMu.Lock()
	defer stockTargetsMu.Unlock()
	if stockTargetsCache != nil && now.Sub(stockTargetsAt) < stockTargetsFreshFor {
		return
	}
	stockTargetsAt = now
	stockTargetsCache = map[string]stockTargetInfo{}

	earliest := now
	for _, logs := range data.TradeHistory {
		for _, t := range logs {
			if t.Time.Before(earliest) {
				earliest = t.Time
			}
		}
	}
	observed := now.Sub(earliest)
	if observed < stockMinObserved {
		return // мало истории — везде работает обычная норма N
	}
	if observed > stockRateWindow {
		observed = stockRateWindow
	}
	since := now.Add(-observed)
	factor := currentOnlineFactor(now)

	byType := map[string][]stockItemStat{}
	for item, cfg := range itemsConfig {
		if !itemConfigActiveLocked(cfg) {
			continue
		}
		nacenka := cfg.Nacenka
		// Заморозка закупки подменяет cfg.Nacenka на 1e6..1e9 — для маржи берём исходную.
		if as := data.AdjustState[item]; as.NacenkaFrozen && as.NacenkaOriginal > 0 {
			nacenka = as.NacenkaOriginal
		}
		st := stockItemStat{Item: item, Type: cfg.Type, Nacenka: nacenka, ObservedHours: observed.Hours()}
		for _, t := range data.TradeHistory[item] {
			if !t.Time.After(since) || t.Price <= 0 {
				continue
			}
			switch t.Type {
			case "sell":
				st.Sales++
				st.SellSum += t.Price
				st.SellCount++
			case "buy":
				st.BuySum += t.Price
				st.BuyCount++
			}
		}
		byType[cfg.Type] = append(byType[cfg.Type], st)
	}
	for typ, items := range byType {
		// слоты АХ категории (5 на бота, 10 у ботов с арендой) плюс по одному в инвентаре на бота
		budget := slotsForGoTypeLocked(typ) + botsForGoTypeLocked(typ)*(stockSlotsPerBot-stockAHSlotsPerBot)
		if budget <= 0 {
			continue
		}
		if !stockSlotBudgetOn {
			budget = math.MaxInt32 // цели = спрос по скорости слива, без урезания
		}
		infos := buildStockTargets(items, budget, factor)
		onAH := 0
		for _, it := range items {
			onAH += getItemCount(it.Item)
		}
		if stockSlotBudgetOn {
			targets, demand := map[string]int{}, map[string]int{}
			value, held := map[string]float64{}, map[string]int{}
			for item, inf := range infos {
				targets[item], demand[item], value[item] = inf.Target, inf.Demand, inf.Value
				held[item] = getItemCount(item) + getInventoryCount(item)
			}
			for item, t := range lendUnusedSlots(targets, demand, value, held) {
				inf := infos[item]
				inf.Target = t
				infos[item] = inf
			}
		}
		pressure := float64(onAH) / float64(slotsForGoTypeLocked(typ))
		squeezeWeakTargets(infos, pressure)
		for item, inf := range infos {
			stockTargetsCache[item] = inf
		}
	}
}

// stockTargetLocked — цель запаса по товару; ok=false → использовать норму N из конфига.
func stockTargetLocked(item string, now time.Time) (stockTargetInfo, bool) {
	refreshStockTargetsLocked(now)
	stockTargetsMu.Lock()
	defer stockTargetsMu.Unlock()
	inf, ok := stockTargetsCache[item]
	if ok && inf.Squeezed {
		return inf, false // слабый товар при тесных слотах: работает обычная норма N
	}
	return inf, ok
}

// Ночной режим: онлайн сервера ниже порога (днём ~12,6 тыс., ночью ~3,4 тыс.). Нет свежих замеров онлайна —
// по часам (22:00–09:00 UTC).
const nightOnlineThreshold = 9000

func latestOnline(now time.Time) int {
	onlineSamplesMu.Lock()
	defer onlineSamplesMu.Unlock()
	n := len(onlineSamples)
	if n == 0 || now.Sub(onlineSamples[n-1].At) > 15*time.Minute {
		return 0
	}
	return onlineSamples[n-1].Online
}

// nightByOnline — чистая функция: ночь, если онлайн известен и ниже порога, иначе по часу UTC.
func nightByOnline(online int, hourUTC int) bool {
	if online > 0 {
		return online < nightOnlineThreshold
	}
	return hourUTC >= 22 || hourUTC < 9
}

func isNightNow(now time.Time) bool {
	return nightByOnline(latestOnline(now), now.UTC().Hour())
}
