package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// book2 — sell + nacenka от книги и перцентиля реальных покупок (max profit, Sep 2026).
//
// Эмпирика (FIFO × hourly p10, мечи):
//   - выигрышные buy/p10: p50≈0.67, p90≈0.83, p95≈0.90
//   - выигрышные sell/p10: p50≈0.91, p75≈1.00
//   - суммарный profit по buy-gate пик ~0.85–0.90×p10; выше 0.95 — bad_loss растёт
//   - статичная nacenka резала вход: смотрим покупки с price < ref−nac
//
// Политика:
//
//	sell   ≈ 1.00 × p10
//	buyMax ≈ p90(недавних покупок ниже gate), clamp [0.75, 0.88]×p10
//	fallback buyMax = 0.85×p10 если истории мало
//	nacenka = sell − buyMax
//
// Толстая книга обязательна; иначе HOLD.

const (
	bookSellMult = 1.00

	// Fallback / clamps от profit-scan мечей (не «магические 20%»).
	bookBuyFallbackMult = 0.85
	bookBuyFloorMult    = 0.75
	bookBuyCeilMult     = 0.88

	bookBuyHistQ        = 0.90
	bookBuyHistMinN     = 8
	bookBuyHistLookback = 7 * 24 * time.Hour
)

// bookSnapWithMarker — step-grid + хвост маркера (xx99), как у ручных цен.
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

func bookPercentileInt(ps []int, q float64) int {
	if len(ps) == 0 {
		return 0
	}
	cp := append([]int(nil), ps...)
	sort.Ints(cp)
	if q <= 0 {
		return cp[0]
	}
	if q >= 1 {
		return cp[len(cp)-1]
	}
	k := q * float64(len(cp)-1)
	f := int(k)
	c := f + 1
	if c >= len(cp) {
		return cp[len(cp)-1]
	}
	w := k - float64(f)
	return int(float64(cp[f])*(1-w) + float64(cp[c])*w + 0.5)
}

// tradeBuyPricesBelowGateSince — цены покупок item после since, где бот взял
// лот дешевле sell−nacenka (ref_price − nacenka). Без ref — берём все buy>0.
// Вызывать без mutex (sqlite).
func tradeBuyPricesBelowGateSince(itemID string, since time.Time) []int {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return nil
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	rows, err := mlDB.Query(`
SELECT price, COALESCE(nacenka, 0), COALESCE(ref_price, 0)
FROM trade_events
WHERE item_id = ? AND event_type = 'buy' AND ts >= ? AND price > 0
ORDER BY ts DESC
LIMIT 200`, itemID, since.UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("[BOOK2] buy hist %s: %v", itemID, err)
		return nil
	}
	defer rows.Close()
	out := make([]int, 0, 64)
	for rows.Next() {
		var price, nac, ref int
		if err := rows.Scan(&price, &nac, &ref); err != nil {
			continue
		}
		if price <= 0 {
			continue
		}
		if ref > 0 {
			gate := ref - nac
			if gate > 0 && price >= gate {
				continue // не прошёл buy-gate — шум/ошибка
			}
		}
		out = append(out, price)
	}
	return out
}

// bookBuyMaxFromP10 — потолок закупа: hist p90 или fallback, clamp к книге.
func bookBuyMaxFromP10(p10 int, histBuys []int) (buyMax int, src string) {
	if p10 <= 0 {
		return 0, "no_p10"
	}
	lo := int(float64(p10)*bookBuyFloorMult + 0.5)
	hi := int(float64(p10)*bookBuyCeilMult + 0.5)
	fb := int(float64(p10)*bookBuyFallbackMult + 0.5)
	if len(histBuys) >= bookBuyHistMinN {
		h := bookPercentileInt(histBuys, bookBuyHistQ)
		if h > 0 {
			buyMax = h
			src = fmt.Sprintf("hist_p%.0f_n%d", bookBuyHistQ*100, len(histBuys))
		} else {
			buyMax = fb
			src = "fallback"
		}
	} else {
		buyMax = fb
		src = fmt.Sprintf("fallback_n%d", len(histBuys))
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

func bookTargetsFromP10(p10, step, priceBefore, priceFloor, nacenkaMin int, histBuys []int) (sell, nac int, buySrc string) {
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
	rawBuy, buySrc := bookBuyMaxFromP10(p10, histBuys)
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
	histSince := now.Add(-bookBuyHistLookback)
	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	histBuys := tradeBuyPricesBelowGateSince(item, histSince)
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	buySrc := ""
	notes := []string{
		fmt.Sprintf(
			"book2 sell×%.2f buyHist_p%.0f clamp[%.2f,%.2f] fb×%.2f p10=%d p10N=%d ok=%v histN=%d onAH=%d sales=%d buys=%d price=%d nac=%d floor=%d",
			bookSellMult, bookBuyHistQ*100, bookBuyFloorMult, bookBuyCeilMult, bookBuyFallbackMult,
			p10, p10N, p10OK, len(histBuys), onAH, sales, buys, price, nacenka, priceFloor,
		),
	}

	if p10OK {
		sellT, nacT, src := bookTargetsFromP10(p10, step, priceBefore, priceFloor, nacMin, histBuys)
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
			"target sell=%d nac=%d buyMax=%d (p10=%d src=%s)",
			sellT, nacT, sellT-nacT, p10, buySrc,
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
	log.Printf("[BOOK2] %s: %s reason=%s | цена %d→%d nac %d→%d | p10=%d n=%d buySrc=%s | %s",
		item, dir, decReason, priceBefore, newPrice, nacenkaBefore, newNac, p10, p10N, buySrc, action)

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
