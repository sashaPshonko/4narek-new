package main

import (
	"log"
	"sort"
	"sync"
	"time"
)

// Прибыльные и слабые часы суток для ходовых предметов.
//
// Продажи сильно зависят от времени суток: ночью (≈02–09 МСК) их в 2–4 раза меньше, чем днём и вечером. Правило
// «продаж мало + запас низкий → поднять цену» ночью срабатывало не из-за дефицита, а из-за нехватки покупателей:
// цена ползла вверх, продажи падали в 3–4 раза (штаны 07.11 00:00–03:20: 37 → 11 продаж/ч), потом поводок возвращал
// цену обратно. Правило: в слабый час суток ходовым цену не поднимаем (снижение и поводок работают как всегда).
//
// Профиль часов Go учит сам: среднее число продаж по всем предметам в каждый час суток (МСК) за последние 24 часа
// из capital_cycles, делится на P90 из 24 часовых значений, затем сглаживается соседними часами. Обновляется раз в 30 минут в фоне (в самом решении БД
// не читаем: оно идёт под общим mutex). Нет данных — правило выключено.
const (
	hourProfileHours   = 24 // только последние сутки: до 06.11 цены дрейфовали, неделя даёт устаревший профиль
	hotUpMinHourFrac   = 0.5
	// В горячие часы (доля часа ≥ hotUpPeakHourFrac) порог «продаж мало» для повышения умножается на
	// hotUpPeakSalesMul: днём в пик за цикл продаётся больше HotUpSalesMax, а запас пустой — это дефицит,
	// а не «мало продаж». Без этого цена в пик никогда не росла (sales ≥ порога → hold).
	hotUpPeakHourFrac = 0.8
	hotUpPeakSalesMul = 1.75
	hourProfileRefresh = 30 * time.Minute
)

var (
	hourProfileMu   sync.Mutex
	hourProfile     [24]float64 // доля от пика по часам МСК; 0 = нет данных
	hourProfileOnce sync.Once
)

// startHourProfileRefresher — запускает фоновое обновление профиля (один раз за процесс).
func startHourProfileRefresher() {
	hourProfileOnce.Do(func() {
		go func() {
			for {
				refreshHourProfile(time.Now())
				time.Sleep(hourProfileRefresh)
			}
		}()
	})
}

func refreshHourProfile(now time.Time) {
	if mlDB == nil {
		return
	}
	since := now.Add(-hourProfileHours * time.Hour).UTC().Format(time.RFC3339)
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT CAST(strftime('%H', ts) AS INTEGER), SUM(sales) FROM capital_cycles WHERE ts >= ? GROUP BY 1`, since,
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[hours] профиль часов: %v", err)
		return
	}
	var sales [24]float64
	for rows.Next() {
		var h int
		var s float64
		if rows.Scan(&h, &s) == nil && h >= 0 && h < 24 {
			sales[(h+3)%24] = s // ts в UTC → час по МСК
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	p := hourProfileFromSales(sales)
	hourProfileMu.Lock()
	hourProfile = p
	hourProfileMu.Unlock()
	log.Printf("[hours] профиль часов МСК обновлён (доля от пика): %.2f", p)
}

// hourProfileFromSales — доля от P90 по часам; если данных мало (меньше 16 часов с продажами) — нули.
func hourProfileFromSales(sales [24]float64) [24]float64 {
	var out [24]float64
	vals := make([]float64, 0, 24)
	for _, s := range sales {
		if s > 0 {
			vals = append(vals, s)
		}
	}
	if len(vals) < 16 {
		return out
	}
	sort.Float64s(vals)
	peak := vals[(len(vals)*9)/10]
	if peak <= 0 {
		return out
	}
	var raw [24]float64
	for h, s := range sales {
		raw[h] = s / peak
	}
	// сглаживание соседними часами (¼-½-¼): по одним суткам отдельный час шумит, а блокировать повышение в разгар
	// дня из-за одного провального часа нельзя
	for h := range raw {
		out[h] = 0.25*raw[(h+23)%24] + 0.5*raw[h] + 0.25*raw[(h+1)%24]
	}
	return out
}

// hotHourFrac — доходность текущего часа МСК относительно пика (0 = нет данных, фильтр не применять).
func hotHourFrac(now time.Time) float64 {
	h := now.In(time.FixedZone("MSK", 3*3600)).Hour()
	hourProfileMu.Lock()
	defer hourProfileMu.Unlock()
	return hourProfile[h]
}

// hotDownSalesLimit — порог «продаж мало» для снижения в текущий час: пиковое значение из конфига × доля часа
// (не выше 1). Нет профиля часов (frac ≤ 0) → фиксированный порог, как раньше.
func hotDownSalesLimit(peakMax int, frac float64) float64 {
	if frac <= 0 {
		return float64(peakMax)
	}
	if frac > 1 {
		frac = 1
	}
	return float64(peakMax) * frac
}

// hotUpSalesLimit — порог «продаж мало» для повышения в текущий час. В обычный час — HotUpSalesMax из конфига.
// В горячий час (frac ≥ hotUpPeakHourFrac) — выше в hotUpPeakSalesMul раз: пустой запас при хороших
// продажах считается дефицитом. Нет профиля часов (frac ≤ 0) — как раньше.
func hotUpSalesLimit(upMax int, frac float64) float64 {
	if frac >= hotUpPeakHourFrac {
		return float64(upMax) * hotUpPeakSalesMul
	}
	return float64(upMax)
}
