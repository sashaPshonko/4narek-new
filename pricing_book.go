package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// book2 — sell + nacenka только от актуальной книги (~10 мин окно).
//
// Никакой истории покупок/недель: вайп, x2 за час, старт после простоя —
// первый толстый скан книги сразу ставит sell/nac под рынок.
//
//	sell   ≈ 1.00 × p10          (якорь витрины)
//	buyMax ≈ book p5, clamp [0.75, 0.88]×p10   (дешёвый хвост asks)
//	fallback buyMax = 0.85×p10 если p5 битый
//	nacenka = sell − buyMax
//
// Толстая книга (≥ ahBookMinLotsInWindow); иначе HOLD (неугадываем рынок).

const (
	bookSellMult = 1.00

	// Profit-scan мечи: buy-gate пик ~0.85–0.90×p10; clamp вокруг live p5.
	bookBuyFallbackMult = 0.85
	bookBuyFloorMult    = 0.75
	bookBuyCeilMult     = 0.88
)

func bookSnapWithMarker(target, step, priceBefore int) int {
	if target <= 0 {
		return priceBefore
	}
	if step > 0 {
		target = (target / step) * step
	}
	marker := 0
	if priceBefore > 0 {
		marker = priceBefore % 100
	}
	if marker > 0 {
		target = (target/100)*100 + marker
	}
	if target <= 0 {
		return priceBefore
	}
	return target
}

// bookBuyMaxFromLiveBook — потолок закупа из p5 живой книги vs p10.
func bookBuyMaxFromLiveBook(p10, p5 int) (buyMax int, src string) {
	if p10 <= 0 {
		return 0, "no_p10"
	}
	lo := int(float64(p10)*bookBuyFloorMult + 0.5)
	hi := int(float64(p10)*bookBuyCeilMult + 0.5)
	fb := int(float64(p10)*bookBuyFallbackMult + 0.5)

	if p5 > 0 {
		buyMax = p5
		src = "book_p5"
	} else {
		buyMax = fb
		src = "fallback"
	}
	if buyMax < lo {
		buyMax = lo
		src += "+floor"
	}
	if buyMax > hi {
		buyMax = hi
		src += "+ceil"
	}
	return buyMax, src
}

func bookTargetsFromLiveBook(p10, p5, step, priceBefore, priceFloor, nacenkaMin int) (sell, nac int, buySrc string) {
	if p10 <= 0 {
		return priceBefore, nacenkaMin, "no_p10"
	}
	rawSell := int(float64(p10)*bookSellMult + 0.5)
	sell = bookSnapWithMarker(rawSell, step, priceBefore)
	if priceFloor > 0 && sell < priceFloor {
		sell = bookSnapWithMarker(priceFloor, step, priceBefore)
		if sell < priceFloor {
			sell = priceFloor
		}
	}
	rawBuy, buySrc := bookBuyMaxFromLiveBook(p10, p5)
	buyMax := bookSnapWithMarker(rawBuy, step, priceBefore)
	if buyMax <= 0 {
		buyMax = rawBuy
	}
	nac = sell - buyMax
	if nac < nacenkaMin {
		nac = nacenkaMin
	}
	if nac < 0 {
		nac = 0
	}
	if sell-nac <= 0 && step > 0 {
		nac = sell - step
		if nac < nacenkaMin {
			nac = nacenkaMin
		}
		if nac < 0 {
			nac = 0
		}
	}
	return sell, nac, buySrc
}

func setRuntimeNacenkaLocked(item string, nac int) {
	if nac < 0 {
		nac = 0
	}
	if cfg, ok := itemsConfig[item]; ok {
		cfg.Nacenka = nac
		itemsConfig[item] = cfg
	}
	if data.Nacenkas == nil {
		data.Nacenkas = make(map[string]int)
	}
	if dailyData.Nacenkas == nil {
		dailyData.Nacenkas = make(map[string]int)
	}
	data.Nacenkas[item] = nac
	dailyData.Nacenkas[item] = nac
}

func adjustPriceBook(
	item string,
	cfg ItemConfig,
	now time.Time,
	_ time.Time,
	sales, buys, trySells, profitNow int,
	state ItemAdjustState,
	priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor int,
	onAH, invCount, _held, share, free, need, stockNorm int,
	underbuyOK bool,
	tryRatio, stockLoad float64,
	onlineForCap, onlineMaxForML int,
	ahCounts map[string]int,
) AdjustReport {
	if step <= 0 {
		step = 1
	}
	price := priceBefore
	if price < priceFloor && priceFloor > 0 {
		price = priceFloor
	}

	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	nacMin := resolveNacenkaMin(cfg)

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	p5, p10, p10N, p10OK := ahBookP5P10Since(item, bookSince)
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	buySrc := ""
	notes := []string{
		fmt.Sprintf(
			"book2 live-only sell×%.2f buy=p5 clamp[%.2f,%.2f] fb×%.2f p5=%d p10=%d n=%d ok=%v onAH=%d sales=%d buys=%d price=%d nac=%d floor=%d",
			bookSellMult, bookBuyFloorMult, bookBuyCeilMult, bookBuyFallbackMult,
			p5, p10, p10N, p10OK, onAH, sales, buys, price, nacenka, priceFloor,
		),
	}

	if p10OK {
		// Старые/недельные цены игнорируем: жёсткий snap к текущей книге.
		sellT, nacT, src := bookTargetsFromLiveBook(p10, p5, step, priceBefore, priceFloor, nacMin)
		buySrc = src
		newNac = nacT
		newPrice = sellT
		decReason = "book_snap"
		if newPrice > priceBefore {
			action = "book_price_up"
		} else if newPrice < priceBefore {
			action = "book_price_down"
		} else if newNac != nacenkaBefore {
			action = "book_nacenka_set"
		} else {
			action = "book_hold_at_target"
			decReason = "at_target"
		}
		notes = append(notes, fmt.Sprintf(
			"target sell=%d nac=%d buyMax=%d (p5=%d p10=%d src=%s)",
			sellT, nacT, sellT-nacT, p5, p10, buySrc,
		))
	} else {
		notes = append(notes, "thin/absent book → hold (ждём актуальный скан)")
	}

	if blockDown && newPrice < priceBefore {
		newPrice = priceBefore
		action = "hold_manual_min"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↓ запрещён")
	}
	if blockUp && newPrice > priceBefore {
		newPrice = priceBefore
		action = "hold_manual_max"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↑ запрещён")
	}

	if newPrice < priceFloor && priceFloor > 0 {
		newPrice = priceFloor
		if newPrice > priceBefore {
			action = "corridor_price_up_floor"
			notes = append(notes, fmt.Sprintf("пол %d", priceFloor))
		}
	}

	setRuntimeNacenkaLocked(item, newNac)

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	if onAH > 0 {
		state.EmptyMarketGapStreak = 0
	} else if sales == 0 && buys == 0 {
		state.EmptyMarketGapStreak++
	} else {
		state.EmptyMarketGapStreak = 0
	}
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	priceChanged := newPrice != priceBefore
	nacChanged := newNac != nacenkaBefore
	if priceChanged {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
	}

	reason := actionReasonRU(action)
	if len(notes) > 0 {
		reason = reason + " | " + strings.Join(notes, " · ")
	}

	dir := "HOLD"
	if strings.Contains(action, "price_up") {
		dir = "UP"
	} else if strings.Contains(action, "price_down") {
		dir = "DOWN"
	} else if nacChanged {
		dir = "NAC"
	}
	log.Printf("[BOOK2] %s: %s reason=%s | цена %d→%d nac %d→%d | p5=%d p10=%d n=%d buySrc=%s | %s",
		item, dir, decReason, priceBefore, newPrice, nacenkaBefore, newNac, p5, p10, p10N, buySrc, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, newNac,
		now,
		onlineForCap, onlineMaxForML,
	)

	fair := relistFairStock(share)
	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy + "+book2",
		Item:           item,
		Category:       cfg.Type,
		Action:         action,
		Winner:         action,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(fair),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           onAH,
		Share:          fair,
		Free:           free,
		Need:           need,
		NormalSales:    relistSalesNorm,
		NormalCount:    stockNorm,
		TryRatio:       tryRatio,
		StockLoad:      stockLoad,
		Underbuy:       underbuyOK,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   newNac,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		PriceFloor:     priceFloor,
		Step:           step,
		Cooldown:       0,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   botsForGoTypeLocked(cfg.Type),
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     0,
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}

	needBroadcast := priceChanged || nacChanged
	mutex.Unlock()

	logCapitalCycle(capitalRow)
	if needBroadcast {
		publishPriceUpdate()
	}
	saveDailyDataNoMessageUpdate()

	return AdjustReport{
		Item:          item,
		Action:        action,
		Reason:        reason,
		PriceBefore:   priceBefore,
		PriceAfter:    newPrice,
		NacenkaBefore: nacenkaBefore,
		NacenkaAfter:  newNac,
		Sales:         sales,
		Buys:          buys,
		TrySells:      trySells,
		NormalSales:   relistSalesNorm,
		Step:          step,
	}
}
