package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// book1 — sell + nacenka от книги (эмпирика Sep 2026):
//
//	sell ≈ 1.00 × p10
//	buyMax ≈ 0.80 × p10  →  nacenka = sell − buyMax ≈ 0.20 × p10
//
// Бот покупает если ahPrice < sell−nacenka. Без толстой книги — HOLD (не трогаем).
// Для relist-типов вместо stock/sales формулы.

const (
	bookSellMult = 1.00
	bookBuyMult  = 0.80
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

func bookTargetsFromP10(p10, step, priceBefore, priceFloor, nacenkaMin int) (sell, nac int) {
	if p10 <= 0 {
		return priceBefore, nacenkaMin
	}
	rawSell := int(float64(p10)*bookSellMult + 0.5)
	sell = bookSnapWithMarker(rawSell, step, priceBefore)
	if priceFloor > 0 && sell < priceFloor {
		sell = bookSnapWithMarker(priceFloor, step, priceBefore)
		if sell < priceFloor {
			sell = priceFloor
		}
	}
	rawBuy := int(float64(p10)*bookBuyMult + 0.5)
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
	// buyMax не должен съесть весь sell
	if sell-nac <= 0 && step > 0 {
		nac = sell - step
		if nac < nacenkaMin {
			nac = nacenkaMin
		}
		if nac < 0 {
			nac = 0
		}
	}
	return sell, nac
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
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	notes := []string{
		fmt.Sprintf(
			"book1 sell×%.2f buy×%.2f p10=%d p10N=%d ok=%v onAH=%d sales=%d buys=%d price=%d nac=%d floor=%d",
			bookSellMult, bookBuyMult, p10, p10N, p10OK, onAH, sales, buys, price, nacenka, priceFloor,
		),
	}

	if p10OK {
		sellT, nacT := bookTargetsFromP10(p10, step, priceBefore, priceFloor, nacMin)
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
			"target sell=%d nac=%d buyMax=%d (p10=%d)",
			sellT, nacT, sellT-nacT, p10,
		))
	} else {
		notes = append(notes, "thin/absent book → hold price+nacenka")
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
	log.Printf("[BOOK1] %s: %s reason=%s | цена %d→%d nac %d→%d | p10=%d n=%d | %s",
		item, dir, decReason, priceBefore, newPrice, nacenkaBefore, newNac, p10, p10N, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, newNac,
		now,
		onlineForCap, onlineMaxForML,
	)

	fair := relistFairStock(share)
	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy + "+book1",
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
