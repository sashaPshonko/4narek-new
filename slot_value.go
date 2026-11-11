package main

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// Ценность слота: прибыль на слот в час по реальным сделкам.
//
// Слот бота (5 лотов АХ + запас, всего ~8 предметов) — главный ресурс. Каждый предмет занимает слот от покупки
// до продажи, поэтому эффективность товара = прибыль / время занятия слота, а не маржа сама по себе:
// товар с маржой 50%, который лежит 8 часов, хуже товара с маржой 15%, который уходит за 10 минут.
//
// Считаем по FIFO: продажа закрывает самую раннюю непроданную покупку этого товара. Прибыль = цена продажи − цена
// покупки, время слота = от покупки до продажи. Ещё не проданные лоты тоже занимают слот (время растёт, прибыли нет),
// поэтому медленные товары честно теряют в оценке. Шаг 1: только считаем, пишем в лог и каталог; на решения не влияет.
const (
	slotValueWindow       = 72 * time.Hour
	slotValueHalfLife     = 12 * time.Hour // вес сделки падает вдвое за 12 ч: оценка сама подстраивается под рынок
	slotValueFreshFor     = 60 * time.Second
	slotValueMinSlotHours = 2.0 // меньше (взвешенных) слот-часов — оценка шумная, считаем неизвестной
	slotValueMinSells     = 3   // и меньше закрытых лотов — тоже
	slotValueLogEvery     = 10 * time.Minute
	slotValueMinHold      = time.Minute // нижняя граница времени слота на лот (мгновенные перепродажи)

	// Режимы товара (шаг 1: только считаем и показываем).
	//   stable        — ценность положительна и не ниже slotModeStableFrac от средней по категории;
	//   opportunistic — ценность отрицательна или заметно ниже средней: брать только разовые выгодные лоты;
	//   unknown       — данных мало: ограничений нет.
	slotModeStable        = "stable"
	slotModeOpportunistic = "opportunistic"
	slotModeUnknown       = "unknown"
	slotModeStableFrac    = 0.5

	slotValueEpochPath = "ml_data/slot_value_epoch.json"
)

type slotValueInfo struct {
	Value     float64 // ₽ за слот-час
	Profit    float64
	SlotHours float64
	Sells     int // закрытые лоты в окне
	Open      int // ещё не проданные лоты
	Known     bool
}

// fifoSlotValue — чистая функция по сделкам одного товара. heldNow — сколько штук товара на руках сейчас
// (АХ + инвентарь), -1 = неизвестно. Не закрытые продажей покупки сверх heldNow — потерянные лоты (выкинуты,
// проданы мимо учёта): их не считаем ни прибылью, ни временем слота, иначе время слота раздувается.
// skipSells — сколько первых продаж пропустить: они закрывают лоты, купленные до точки отсчёта статистики.
// halfLife > 0 — сделка весит 0.5^(возраст/halfLife), свежие важнее старых; 0 — все веса равны.
func fifoSlotValue(trades []TradeLog, since, now time.Time, heldNow, skipSells int, halfLife time.Duration) slotValueInfo {
	sorted := append([]TradeLog(nil), trades...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	type lot struct {
		at    time.Time
		price int
	}
	var queue []lot
	var inf slotValueInfo
	for _, t := range sorted {
		if t.Price <= 0 {
			continue
		}
		switch t.Type {
		case "buy":
			queue = append(queue, lot{at: t.Time, price: t.Price})
		case "sell":
			if skipSells > 0 { // закрывает лот, купленный до точки отсчёта: наши покупки не трогаем
				skipSells--
				continue
			}
			if len(queue) == 0 {
				continue // купили до начала истории — закрыть нечем
			}
			b := queue[0]
			queue = queue[1:]
			if !t.Time.After(since) {
				continue
			}
			hold := t.Time.Sub(b.at)
			if hold < slotValueMinHold {
				hold = slotValueMinHold
			}
			w := 1.0
			if halfLife > 0 {
				w = math.Pow(0.5, float64(now.Sub(t.Time))/float64(halfLife))
			}
			inf.Profit += w * float64(t.Price-b.price)
			inf.SlotHours += w * hold.Hours()
			inf.Sells++
		}
	}
	if heldNow >= 0 && len(queue) > heldNow {
		queue = queue[len(queue)-heldNow:] // открытыми считаем самые свежие покупки
	}
	for _, b := range queue {
		from := b.at
		if from.Before(since) {
			from = since
		}
		if h := now.Sub(from).Hours(); h > 0 {
			inf.SlotHours += h
		}
		inf.Open++
	}
	if inf.SlotHours >= slotValueMinSlotHours && inf.Sells >= slotValueMinSells {
		inf.Value = inf.Profit / inf.SlotHours
		inf.Known = true
	}
	return inf
}

// slotMode — режим товара по его ценности и средней по категории.
func slotMode(inf slotValueInfo, catRef float64) string {
	if !inf.Known {
		return slotModeUnknown
	}
	if inf.Value <= 0 || (catRef > 0 && inf.Value < slotModeStableFrac*catRef) {
		return slotModeOpportunistic
	}
	return slotModeStable
}

// --- точка отсчёта статистики ---
// Старые сделки грязные (баны, перезапуски), поэтому копим с момента первого запуска. Файл хранит время отсчёта
// и сколько каждого товара было на руках в тот момент: первые столько продаж закрывают старые лоты и не считаются.
// Сбросить статистику: удалить файл (при следующем запуске отсчёт начнётся заново).

type slotValueEpochFile struct {
	Since time.Time      `json:"since"`
	Held  map[string]int `json:"held"`
}

var (
	slotEpochOnce sync.Once
	slotEpoch     slotValueEpochFile
)

// loadSlotEpochLocked — mutex данных взят (при создании читает остатки на руках).
func loadSlotEpochLocked(now time.Time) slotValueEpochFile {
	slotEpochOnce.Do(func() {
		if raw, err := os.ReadFile(slotValueEpochPath); err == nil {
			var f slotValueEpochFile
			if json.Unmarshal(raw, &f) == nil && !f.Since.IsZero() {
				slotEpoch = f
				return
			}
		}
		f := slotValueEpochFile{Since: now, Held: map[string]int{}}
		for item := range itemsConfig {
			if h := getItemCount(item) + getInventoryCount(item); h > 0 {
				f.Held[item] = h
			}
		}
		if raw, err := json.MarshalIndent(f, "", "  "); err == nil {
			if err := os.WriteFile(slotValueEpochPath, raw, 0o644); err != nil {
				log.Printf("[SLOT-VALUE] не смог записать %s: %v", slotValueEpochPath, err)
			}
		}
		slotEpoch = f
		log.Printf("[SLOT-VALUE] статистика ценности слота копится с %s (на руках на старте: %d товаров)", f.Since.Format(time.RFC3339), len(f.Held))
	})
	return slotEpoch
}

// tradesSince — сделки не раньше точки отсчёта.
func tradesSince(trades []TradeLog, epoch time.Time) []TradeLog {
	out := make([]TradeLog, 0, len(trades))
	for _, t := range trades {
		if !t.Time.Before(epoch) {
			out = append(out, t)
		}
	}
	return out
}

var (
	slotValueMu       sync.Mutex
	slotValueCache    map[string]slotValueInfo
	slotValueCatRef   map[string]float64 // go-тип → средняя ценность слота по категории
	slotValueAt       time.Time
	slotValueLoggedAt time.Time
)

// refreshSlotValuesLocked — mutex данных взят (читает TradeHistory и itemsConfig).
func refreshSlotValuesLocked(now time.Time) {
	slotValueMu.Lock()
	defer slotValueMu.Unlock()
	if slotValueCache != nil && now.Sub(slotValueAt) < slotValueFreshFor {
		return
	}
	slotValueAt = now
	epoch := loadSlotEpochLocked(now)
	since := now.Add(-slotValueWindow)
	if epoch.Since.After(since) {
		since = epoch.Since
	}
	slotValueCache = make(map[string]slotValueInfo, len(itemsConfig))
	slotValueCatRef = map[string]float64{}

	type agg struct{ profit, hours float64 }
	byType := map[string]*agg{}
	for item, cfg := range itemsConfig {
		inf := fifoSlotValue(tradesSince(data.TradeHistory[item], epoch.Since), since, now, getItemCount(item)+getInventoryCount(item), epoch.Held[item], slotValueHalfLife)
		slotValueCache[item] = inf
		a := byType[cfg.Type]
		if a == nil {
			a = &agg{}
			byType[cfg.Type] = a
		}
		a.profit += inf.Profit
		a.hours += inf.SlotHours
	}
	for typ, a := range byType {
		if a.hours >= slotValueMinSlotHours {
			slotValueCatRef[typ] = a.profit / a.hours
		}
	}
	logSlotValuesLocked(now)
}

func logSlotValuesLocked(now time.Time) {
	if now.Sub(slotValueLoggedAt) < slotValueLogEvery {
		return
	}
	slotValueLoggedAt = now
	type row struct {
		item string
		inf  slotValueInfo
	}
	var rows []row
	for item, inf := range slotValueCache {
		if inf.Known {
			rows = append(rows, row{item, inf})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].inf.Value > rows[j].inf.Value })
	for typ, ref := range slotValueCatRef {
		log.Printf("[SLOT-VALUE] категория %s: средняя %.0f ₽/слот·ч", typ, ref)
	}
	modes := map[string]int{}
	for item, inf := range slotValueCache {
		cr := 0.0
		if cfg, ok := itemsConfig[item]; ok {
			cr = slotValueCatRef[cfg.Type]
		}
		modes[slotMode(inf, cr)]++
	}
	log.Printf("[SLOT-VALUE] режимы: стабильных %d, разовых %d, мало данных %d (отсчёт с %s)",
		modes[slotModeStable], modes[slotModeOpportunistic], modes[slotModeUnknown], slotEpoch.Since.Format("02.01 15:04"))
	n := len(rows)
	for i := 0; i < n && i < 3; i++ {
		r := rows[i]
		log.Printf("[SLOT-VALUE] топ: %s %.0f ₽/слот·ч (продаж %d, открыто %d, слот-часов %.1f)", r.item, r.inf.Value, r.inf.Sells, r.inf.Open, r.inf.SlotHours)
	}
	for i := n - 1; i >= 0 && i >= n-3 && i >= 3; i-- {
		r := rows[i]
		log.Printf("[SLOT-VALUE] хвост: %s %.0f ₽/слот·ч (продаж %d, открыто %d, слот-часов %.1f)", r.item, r.inf.Value, r.inf.Sells, r.inf.Open, r.inf.SlotHours)
	}
}

// slotValueLocked — ценность слота товара и средняя по его категории; ok=false — данных мало.
func slotValueLocked(item string, now time.Time) (inf slotValueInfo, catRef float64, ok bool) {
	refreshSlotValuesLocked(now)
	slotValueMu.Lock()
	defer slotValueMu.Unlock()
	inf = slotValueCache[item]
	if cfg, exists := itemsConfig[item]; exists {
		catRef = slotValueCatRef[cfg.Type]
	}
	return inf, catRef, inf.Known
}
