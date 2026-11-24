package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Регулятор наценки для ходовых (hot) товаров.
//
// Наценка = насколько дешевле цены продажи бот обязан купить (лимит покупки = цена − наценка).
// Регулируем не «число», а ПОТОК: сколько мы покупаем относительно того, сколько продаём.
//
//	сток раздут  → хотим покупать меньше, чем продаём (запас рассасывается), и брать только выгодное
//	сток в норме → покупаем столько же, сколько продаём (наценка из конфига)
//	стока мало   → покупаем всё, что можно (наценка из конфига, никогда ниже)
//
// Цель по доле покупок к продажам r* за последний час:
//
//	на АХ > 2×HotDownStockMin  → r* = 0.5
//	на АХ > HotDownStockMin     → r* = 0.8
//	иначе                       → r* = 1.0, и наценка возвращается к значению из конфига
//
// Если фактическая доля больше r*×1.25 → наценка +шаг (покупаем слишком много).
// Если меньше r*×0.6 и мы выше конфига → наценка −шаг (задушили закупку сильнее, чем надо).
// Между ними не трогаем. Не чаще одного изменения за nacRegCooldown (спад за ночь не гонит наценку вверх:
// решает доля, а не абсолютные продажи, и нужно минимум nacRegMinSales продаж за час).
//
// Потолок: наценка из конфига × 1.5, и не больше «цена − p25 наших покупок за 3 ч».
// Ниже конфига не опускаем никогда (инвариант pridumalnahuy).
//
// Выключить: NACENKA_REG=0.

const (
	nacRegCapNum, nacRegCapDen = 3, 2 // потолок = конфиг × 3/2
	nacRegBuyWindow            = 3 * time.Hour
	nacRegBuyQ                 = 0.25
	nacRegBuyMinN              = 10
	nacRegFlowWindow           = time.Hour
	nacRegMinSales             = 8                // меньше продаж за час — доли не доверяем
	nacRegCooldown             = 30 * time.Minute // между изменениями
	nacRegUpFactor             = 1.25
	nacRegDownFactor           = 0.6
	nacRegFreeSlotsMin         = 4 // свободных слотов у покупателей категории: АХ пустеет — закупку не душим
)

func nacRegEnabled() bool {
	return strings.TrimSpace(os.Getenv("NACENKA_REG")) != "0"
}

type nacRegInput struct {
	Base, Cur, Step int
	Price           int // цена продажи после решения цикла
	OnAH            int
	Buys, Sales     int // поток за nacRegFlowWindow
	DownStockMin    int
	UpStockMax      int
	BuyP25          int
	BuyP25OK        bool
	FreeSlots       int           // свободные слоты ботов-покупателей категории (0 = неизвестно/нет)
	SinceChange     time.Duration // сколько прошло с последнего изменения (большое = можно)
}

type nacRegDecision struct {
	New    int
	Reason string
}

func nacRegCap(in nacRegInput) int {
	limit := in.Base * nacRegCapNum / nacRegCapDen
	if in.BuyP25OK && in.BuyP25 > 0 && in.Price > 0 {
		if h := in.Price - in.BuyP25; h < limit {
			limit = h
		}
	}
	if limit < in.Base {
		limit = in.Base
	}
	return limit
}

func nacRegTarget(onAH, downStockMin int) float64 {
	switch {
	case onAH > downStockMin*2:
		return 0.5
	case onAH > downStockMin:
		return 0.8
	default:
		return 1.0
	}
}

// nacRegDecide — чистая функция: новая наценка и причина.
func nacRegDecide(in nacRegInput) nacRegDecision {
	if in.Base <= 0 || in.Step <= 0 || in.DownStockMin <= 0 {
		return nacRegDecision{New: in.Cur, Reason: "off"}
	}
	if in.Cur < in.Base {
		return nacRegDecision{New: in.Base, Reason: "below_base"}
	}
	limit := nacRegCap(in)
	// Потолок можно опустить (изменился p25 / конфиг): сразу прижимаемся, без ожидания.
	if in.Cur > limit {
		return nacRegDecision{New: limit, Reason: "over_cap"}
	}
	// Стока мало → сразу к конфигу (бот должен набирать запас).
	if in.UpStockMax > 0 && in.OnAH < in.UpStockMax {
		if in.Cur > in.Base {
			return nacRegDecision{New: in.Base, Reason: "stock_low_reset"}
		}
		return nacRegDecision{New: in.Cur, Reason: "base"}
	}
	if in.SinceChange < nacRegCooldown {
		return nacRegDecision{New: in.Cur, Reason: "cooldown"}
	}
	// Сток в норме → плавно к конфигу.
	if in.OnAH <= in.DownStockMin {
		if in.Cur > in.Base {
			n := in.Cur - in.Step
			if n < in.Base {
				n = in.Base
			}
			return nacRegDecision{New: n, Reason: "stock_ok_down"}
		}
		return nacRegDecision{New: in.Cur, Reason: "base"}
	}
	// Свободные слоты у покупателей: пустой АХ хуже, чем чуть дороже закупка — наценку не поднимаем, снижаем к конфигу.
	if in.FreeSlots >= nacRegFreeSlotsMin {
		if in.Cur > in.Base {
			n := in.Cur - in.Step
			if n < in.Base {
				n = in.Base
			}
			return nacRegDecision{New: n, Reason: "free_slots_down"}
		}
		return nacRegDecision{New: in.Cur, Reason: "free_slots_hold"}
	}
	// Сток выше нормы: смотрим на поток.
	if in.Sales < nacRegMinSales {
		return nacRegDecision{New: in.Cur, Reason: "hold_few_sales"}
	}
	ratio := float64(in.Buys) / float64(in.Sales)
	target := nacRegTarget(in.OnAH, in.DownStockMin)
	switch {
	case ratio > target*nacRegUpFactor:
		if !in.BuyP25OK {
			return nacRegDecision{New: in.Cur, Reason: "hold_no_buy_history"}
		}
		if in.Cur >= limit {
			return nacRegDecision{New: in.Cur, Reason: "at_cap"}
		}
		n := in.Cur + in.Step
		if n > limit {
			n = limit
		}
		return nacRegDecision{New: n, Reason: "flow_up"}
	case ratio < target*nacRegDownFactor && in.Cur > in.Base:
		n := in.Cur - in.Step
		if n < in.Base {
			n = in.Base
		}
		return nacRegDecision{New: n, Reason: "flow_down"}
	}
	return nacRegDecision{New: in.Cur, Reason: "hold"}
}

// nacRegLastChange — когда регулятор последний раз менял наценку товара (читается/пишется под общим mutex).
var nacRegLastChange = map[string]time.Time{}

// ownFlowCounts — наши покупки и продажи товара за окно.
func ownFlowCounts(item string, now time.Time, window time.Duration) (buys, sells int, ok bool) {
	if mlDB == nil || strings.TrimSpace(item) == "" {
		return 0, 0, false
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	rows, err := mlDB.Query(
		`SELECT event_type, COUNT(*) FROM trade_events
		 WHERE item_id = ? AND event_type IN ('buy','sell') AND ts >= ? GROUP BY event_type`,
		item, now.Add(-window).UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, 0, false
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		var n int
		if rows.Scan(&e, &n) == nil {
			if e == "buy" {
				buys = n
			} else {
				sells = n
			}
		}
	}
	return buys, sells, true
}

// ownBuysQuantile — перцентиль цен наших покупок товара за окно.
func ownBuysQuantile(item string, now time.Time, window time.Duration, q float64, minN int) (int, bool) {
	if mlDB == nil || strings.TrimSpace(item) == "" {
		return 0, false
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT price FROM trade_events WHERE item_id = ? AND event_type = 'buy' AND ts >= ? AND price > 0`,
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

// applyNacenkaRegLocked — применить решение регулятора (mutex уже взят).
func applyNacenkaRegLocked(item string, now time.Time, in nacRegInput) (int, string, bool) {
	if t, ok := nacRegLastChange[item]; ok {
		in.SinceChange = now.Sub(t)
	} else {
		in.SinceChange = time.Hour * 24
	}
	dec := nacRegDecide(in)
	note := fmt.Sprintf("наценка-рег: %s %d→%d (конфиг %d, потолок %d, на АХ %d, за час купили %d / продали %d)",
		dec.Reason, in.Cur, dec.New, in.Base, nacRegCap(in), in.OnAH, in.Buys, in.Sales)
	if dec.New == in.Cur {
		return in.Cur, note, false
	}
	c := itemsConfig[item]
	c.Nacenka = dec.New
	itemsConfig[item] = c
	ensureNacenkasInitialized()
	data.Nacenkas[item] = dec.New
	dailyData.Nacenkas[item] = dec.New
	nacRegLastChange[item] = now
	return dec.New, note, true
}
