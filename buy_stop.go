package main

import (
	"log"
	"time"
)

// Стоп-закуп дорогих предметов (мега, почти-мега, «починки», крушители, кирки).
//
// Цена ходовых и «дорогих» (режим hot) не имеет потолков: она растёт, пока запас ниже нормы. Это бывает
// опасно: если наша цена ушла выше рынка, предмет не продаётся, а бот по растущему лимиту закупки всё
// равно докупает запас. Защита: считаем потолок отдельно для этой цели — перцентиль buy_stop_q от минимальных
// цен продавцов в книге за buyStopWindow — и, пока наша цена выше потолка, закупку выключаем (наценка
// «бесконечность», как у no_buy). Саму цену это не трогает: логика цены работает как раньше, продажа
// остатков идёт. Вернулась ниже потолка (с гистерезисом) — закупка включается.
//
// Включается полем buy_stop_q в items_config.json (0 / нет = защиты нет). Мало продавцов в книге
// (меньше buy_stop_min_sellers, по умолчанию 10: перцентиль от 5–9 цен — почти максимум и шумит) — потолок недостоверен, защита отключается: так «совсем редкие»
// предметы автоматически остаются без защиты.
const (
	buyStopWindow            = 3 * time.Hour
	buyStopDefaultMinSellers = 10
	// Закупку возвращаем, когда цена опустилась хотя бы на 3% ниже потолка (чтобы не мигала на границе).
	buyStopResumePct = 97
)

// buyStopActive — предметы, у которых закупка выключена из-за цены выше потолка книги. Под mutex.
var buyStopActive = map[string]bool{}

// buyStopStock — предметы, у которых закупка выключена из-за запаса (buy_stop_held). Под mutex.
var buyStopStock = map[string]bool{}

// buyStopCeilNow — потолок для стоп-закупа и число продавцов. Не держать mutex (идёт в БД).
func buyStopCeilNow(cfg ItemConfig, now time.Time) (ceil, nSellers int, ok bool) {
	if cfg.BuyStopQ <= 0 {
		return 0, 0, false
	}
	ps, n := ahBookSellerMinPricesSince(cfg.ID, now.Add(-buyStopWindow))
	min := cfg.BuyStopMinSellers
	if min <= 0 {
		min = buyStopDefaultMinSellers
	}
	if n < min || len(ps) == 0 {
		return 0, n, false
	}
	ceil = ahBookPercentileSorted(ps, cfg.BuyStopQ)
	return ceil, n, ceil > 0
}

// buyStopDecide — чистая функция: нужно ли держать закупку выключенной.
// Нет достоверного потолка → закупка включена (защита не мешает).
func buyStopDecide(active bool, price, ceil int, ok bool) bool {
	if !ok || ceil <= 0 || price <= 0 {
		return false
	}
	if active {
		// держим стоп, пока цена не опустилась на 3% ниже потолка
		return price*100 > ceil*buyStopResumePct
	}
	return price > ceil
}

// setBuyStopLocked — записать состояние; true, если оно изменилось (тогда каталог надо переслать ботам).
func setBuyStopLocked(item string, stop bool) bool {
	if buyStopActive[item] == stop {
		return false
	}
	if stop {
		buyStopActive[item] = true
	} else {
		delete(buyStopActive, item)
	}
	return true
}

func logBuyStopChange(item string, stop bool, price, ceil, nSellers int, q float64) {
	if stop {
		log.Printf("[BUY-STOP] %s: цена %d выше потолка p%.0f=%d (продавцов %d) — закупка выключена", item, price, q*100, ceil, nSellers)
		return
	}
	log.Printf("[BUY-STOP] %s: цена %d снова ниже потолка p%.0f=%d (продавцов %d) — закупка включена", item, price, q*100, ceil, nSellers)
}

// --- стоп по запасу ---------------------------------------------------------------------------------------
//
// Это и есть прямая защита «не закупать слишком много»: цена и потолок не ограничивают КОЛИЧЕСТВО закупки
// (на боте есть только общий лимит 8 предметов на всё), поэтому у дорогих предметов закупка выключается, как только
// на АХ и в инвентарях всего флота лежит buy_stop_held штук этого предмета. Пока запас выше нормы, цена и продажи идут
// как обычно; упал ниже — закупка снова включается. Проверяется на каждое presence (раз в несколько секунд), а не раз в
// цикл цены (10 минут): закупка успевает набрать лишнее быстрее.

// stockStopDecide — чистая функция: держать ли закупку выключенной при таком запасе.
func stockStopDecide(held, cap int) bool {
	return cap > 0 && held >= cap
}

// evalStockBuyStopsLocked — пересчитать стоп по запасу у всех предметов с buy_stop_held. Вызывать под mutex.
// true, если что-то поменялось и каталог (наценку) надо переслать ботам.
func evalStockBuyStopsLocked() bool {
	changed := false
	for id, cfg := range itemsConfig {
		if cfg.BuyStopHeld <= 0 || cfg.NoBuy {
			if buyStopStock[id] {
				delete(buyStopStock, id)
				changed = true
			}
			continue
		}
		held := getItemCount(id) + getInventoryCount(id)
		stop := stockStopDecide(held, cfg.BuyStopHeld)
		if buyStopStock[id] == stop {
			continue
		}
		if stop {
			buyStopStock[id] = true
			log.Printf("[BUY-STOP] %s: запас %d ≥ %d — закупка выключена", id, held, cfg.BuyStopHeld)
		} else {
			delete(buyStopStock, id)
			log.Printf("[BUY-STOP] %s: запас %d < %d — закупка включена", id, held, cfg.BuyStopHeld)
		}
		changed = true
	}
	return changed
}

// --- рыночная «полоса» для ходовых ---------------------------------------------------------------------------

// marketPercentilesNow — перцентили qs минимальных цен продавцов в книге за buyStopWindow. Не держать mutex (идёт в БД).
// Продавцов меньше минимума (buy_stop_min_sellers, по умолчанию 10) — ok=false: рынку верить нельзя, защита не работает.
func marketPercentilesNow(cfg ItemConfig, now time.Time, qs ...float64) (vals []int, nSellers int, ok bool) {
	ps, n := ahBookSellerMinPricesSince(cfg.ID, now.Add(-buyStopWindow))
	min := cfg.BuyStopMinSellers
	if min <= 0 {
		min = buyStopDefaultMinSellers
	}
	if n < min || len(ps) == 0 {
		return nil, n, false
	}
	vals = make([]int, len(qs))
	for i, q := range qs {
		vals[i] = ahBookPercentileSorted(ps, q)
	}
	return vals, n, true
}

// hotMarketBandNow — потолок повышения и «поводок» для ходового предмета (0 = нет данных или выключено).
func hotMarketBandNow(cfg ItemConfig, now time.Time) (capPrice, tetherPrice, nSellers int) {
	if !cfg.Hot || (cfg.HotPriceCapQ <= 0 && cfg.HotTetherQ <= 0) {
		return 0, 0, 0
	}
	qs := []float64{cfg.HotPriceCapQ, cfg.HotTetherQ}
	vals, n, ok := marketPercentilesNow(cfg, now, qs...)
	if !ok {
		return 0, 0, n
	}
	if cfg.HotPriceCapQ > 0 {
		capPrice = vals[0]
	}
	if cfg.HotTetherQ > 0 {
		tetherPrice = vals[1]
	}
	return capPrice, tetherPrice, n
}
