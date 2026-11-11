package main

import (
	"sort"
	"strings"
	"time"
)

// Нормы стока и пол/потолок рынка по книге АХ — общие помощники для pridumalnahuy и stock_target.

// stockNormBookFloor — пол рынка для рычага/проверок (дорогое → дно мульти, иначе seller-p10).
func stockNormBookFloor(item string, since time.Time) (floor int, ok bool) {
	if book2ExpensiveSKU(item, 0) {
		sell, _, _, ok2 := ahBookExpensiveBottomAnchorsSince(item, since)
		if ok2 && sell > 0 {
			return sell, true
		}
	}
	_, buyEdge, _, ok2 := ahBookMarketAnchorsSince(item, since)
	if ok2 && buyEdge > 0 {
		return buyEdge, true
	}
	p10, pn, ok3 := ahBookP10Since(item, since)
	if ok3 && pn >= ahBookMinLotsInWindow && p10 > 0 {
		return p10, true
	}
	return 0, false
}

func stockNormBookCeilQDefault(item string) float64 {
	id := strings.ToLower(strings.TrimSpace(item))
	switch {
	case id == "sword-sharp5-1.21":
		return 0.60
	case id == "sword-sharp6-1.21":
		return 0.50
	case id == "sword7-1.21":
		return 0.45
	case id == "megasword-1.21":
		// p25 книги ~4.0–4.5M, наши сливы ~3.3–3.5; p15 ≈ универсальный потолок
		return 0.15
	case strings.Contains(id, "яд") && strings.Contains(id, "megasword"):
		return 0.10
	case strings.Contains(id, "pochti"):
		return 0.25
	case strings.Contains(id, "sword"):
		return 0.25 // loot и прочие мечи
	default:
		return 0.50 // броня / кирки — потолок на перцентиле 50
	}
}

func stockNormBookFloorQDefault(item string) float64 {
	id := strings.ToLower(strings.TrimSpace(item))
	switch {
	case id == "sword-sharp5-1.21", id == "sword-sharp6-1.21":
		return 0.15
	case strings.Contains(id, "sharp5") || strings.Contains(id, "sharp6"):
		return 0.15
	case id == "megasword-1.21":
		return 0.01
	case strings.Contains(id, "яд") && strings.Contains(id, "megasword"):
		return 0.005
	case strings.Contains(id, "pochti"):
		return 0.01
	case id == "sword7-1.21":
		return 0.10
	case strings.Contains(id, "sword"):
		return 0.10
	default:
		return 0.10
	}
}

// stockNormBookCeilQForConfig — потолок ↑: JSON book_ceil_q или дефолт по id.
func stockNormBookCeilQForConfig(cfg ItemConfig) float64 {
	if cfg.BookCeilQ > 0 && cfg.BookCeilQ < 1 {
		return cfg.BookCeilQ
	}
	return stockNormBookCeilQDefault(cfg.ID)
}

// stockNormBookFloorQForConfig — порог empty catchup / низ ↓ (прыжок к полу).
func stockNormBookFloorQForConfig(cfg ItemConfig) float64 {
	if cfg.BookFloorQ > 0 && cfg.BookFloorQ < 1 {
		return cfg.BookFloorQ
	}
	return stockNormBookFloorQDefault(cfg.ID)
}

// ahBookMinMultiSellers — меньше стольких продавцов с ≥N лотами выборка слишком мала (перцентиль по 4–6 точкам
// скачет ступеньками): тогда берём всех продавцов.
const ahBookMinMultiSellers = 5

// bookMinLotsForConfig — book_min_lots: сколько лотов предмета должно быть у продавца, чтобы попасть в
// перцентиль (минимум по продавцу). 0 = 2 (ходовые: отсекаем разовых «сливальщиков»), 1 = все продавцы
// (редкие предметы: продавцов мало, фильтр оставляет 4–6 точек).
func bookMinLotsForConfig(cfg ItemConfig) int {
	if cfg.BookMinLots <= 0 {
		return 2
	}
	return cfg.BookMinLots
}

func stockNormBookPercentileSince(cfg ItemConfig, since time.Time, q float64) (px int, ok bool) {
	return stockNormBookPercentileMode(cfg, since, q, bookMinLotsForConfig(cfg))
}

// bookFloorMinLotsForConfig — book_floor_min_lots: режим продавцов для ПОЛА. 0 = 1 (все продавцы, минимум по
// каждому): пол должен ловить «мы ниже рынка», и разовые продавцы тут не мешают.
func bookFloorMinLotsForConfig(cfg ItemConfig) int {
	if cfg.BookFloorMinLots <= 0 {
		return 1
	}
	return cfg.BookFloorMinLots
}

// stockNormBookPercentileMode — перцентиль минимальных цен продавцов; minLots ≥2: только продавцы с ≥minLots
// лотов (при выборке меньше ahBookMinMultiSellers — вся книга), minLots=1: все продавцы.
func stockNormBookPercentileMode(cfg ItemConfig, since time.Time, q float64, minLots int) (px int, ok bool) {
	if q <= 0 || q >= 1 {
		return 0, false
	}
	if minLots >= 2 {
		if mps, mn := ahBookMultiSellerMinPricesSince(cfg.ID, since, minLots); mn >= ahBookMinMultiSellers {
			if px = ahBookPercentileSorted(mps, q); px > 0 {
				return px, true
			}
		}
	}
	ps, n := ahBookSellerMinPricesSince(cfg.ID, since)
	if n < ahBookMarketMinSellers {
		return 0, false
	}
	px = ahBookPercentileSorted(ps, q)
	if px <= 0 {
		return 0, false
	}
	return px, true
}

// applyRiskCut — минус к цене рельс (risk_cut из items_config). 0 = без сдвига.
func applyRiskCut(px int, cfg ItemConfig) int {
	if px <= 0 || cfg.RiskCut <= 0 {
		return px
	}
	out := px - cfg.RiskCut
	if out < 1 {
		return 1
	}
	return out
}

// Потолок и пол считаются по окну 30 минут (ahBookCeilWindow / ahBookFloorWindow) без сглаживания:
// перцентиль читается как есть, длинные окна не используем (в 3-часовом полно давно проданных дампов).
const (
	ahBookCeilWindow  = 30 * time.Minute
	ahBookFloorWindow = 30 * time.Minute
)

// stockNormBookCatchupFloorNow — пол по окну ahBookFloorWindow, считая от now.
func stockNormBookCatchupFloorNow(cfg ItemConfig, now time.Time) (floor int, q float64, ok bool) {
	return stockNormBookCatchupFloor(cfg, now.Add(-ahBookFloorWindow))
}

// stockNormBookCeilNow — потолок по окну ahBookCeilWindow, считая от now.
func stockNormBookCeilNow(cfg ItemConfig, now time.Time) (ceil int, q float64, ok bool) {
	if cfg.BookCeilQNight > 0 && isNightNow(now) {
		cfg.BookCeilQ = cfg.BookCeilQNight // ночной режим: q как в «хорошую ночь»
	}
	return stockNormBookCeil(cfg, now.Add(-ahBookCeilWindow))
}

// stockNormBookCeil — потолок ↑ по книге: минимум по каждому продавцу (с ≥book_min_lots лотов).
// Только блокирует ↑; ↓/overstock не трогает. risk_cut сдвигает потолок вниз.
func stockNormBookCeil(cfg ItemConfig, since time.Time) (ceil int, q float64, ok bool) {
	q = stockNormBookCeilQForConfig(cfg)
	ceil, ok = stockNormBookPercentileSince(cfg, since, q)
	if ok {
		ceil = applyRiskCut(ceil, cfg)
	}
	return ceil, q, ok
}

func stockNormBookCatchupFloor(cfg ItemConfig, since time.Time) (floor int, q float64, ok bool) {
	q = stockNormBookFloorQForConfig(cfg)
	floor, ok = stockNormBookPercentileMode(cfg, since, q, bookFloorMinLotsForConfig(cfg))
	if ok {
		floor = applyRiskCut(floor, cfg)
	}
	return floor, q, ok
}

// serverMaxBookAnomalous — set_max далеко от книги: шум FunTime / стена.
// true = игнорировать сообщение.
func serverMaxBookAnomalous(ours, proposed int, item string, now time.Time) bool {
	if proposed <= 0 {
		return true
	}
	if ours > 0 && proposed*100 < ours*90 {
		return true
	}
	floor, ok := stockNormBookFloor(item, now.Add(-ahBook2Window))
	if !ok || floor <= 0 {
		return false
	}
	if proposed*100 < floor*85 {
		return true
	}
	if proposed > floor*2 && (ours <= 0 || proposed > ours*150/100) {
		return true
	}
	return false
}

// Прыжок к полу (только предметы с floor_jump=true): цена ниже пола 2 цикла подряд — предмет «мёртвый»
// (по данным продаж в 12 раз меньше, покупок почти нет, цена сама возвращается за 3 цикла лишь в 28% случаев).
const (
	floorJumpOwnSalesWindow = 24 * time.Hour
	floorJumpOwnSalesMin    = 8
	floorJumpMaxUpPct       = 10 // за один прыжок не больше +10% к цене
)

// ownSalesQuantile — перцентиль цен наших продаж за окно (trade_events). ok=false, если продаж меньше minN.
func ownSalesQuantile(item string, now time.Time, window time.Duration, q float64, minN int) (px int, ok bool) {
	if mlDB == nil || strings.TrimSpace(item) == "" {
		return 0, false
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT price FROM trade_events
		 WHERE item_id = ? AND event_type = 'sell' AND ts >= ? AND price > 0 AND nacenka < 100000000`,
		item, now.Add(-window).UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		return 0, false
	}
	var ps []int
	for rows.Next() {
		var p int
		if rows.Scan(&p) == nil && p > 0 {
			ps = append(ps, p)
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	if len(ps) < minN {
		return 0, false
	}
	sort.Ints(ps)
	return ahBookPercentileSorted(ps, q), true
}

// floorJumpTarget — куда прыгать вверх: меньшее из (пол сейчас, пол прошлого цикла, p25 наших продаж за 24 ч),
// не больше +floorJumpMaxUpPct% к цене и не выше потолка. 0 — прыгать некуда.
func floorJumpTarget(price, floorNow, floorPrev, ownP25 int, ownOK bool, ceil int, ceilOK bool) int {
	if floorNow <= 0 || price <= 0 {
		return 0
	}
	t := floorNow
	if floorPrev > 0 && floorPrev < t {
		t = floorPrev
	}
	if ownOK && ownP25 > 0 && ownP25 < t {
		t = ownP25
	}
	if up := price + price*floorJumpMaxUpPct/100; t > up {
		t = up
	}
	if ceilOK && ceil > 0 && t > ceil {
		t = ceil
	}
	if t <= price {
		return 0
	}
	return t
}
