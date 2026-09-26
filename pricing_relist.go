package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// Relist AH-only (buy + перевыстав, 5 слотов на бота). Без inventory-коридора.
//
//   fair = max(1, share)  где share = (5×боты)/nItems категории
//   weak := sales < relistSalesNorm (5)
//   onAH ≥ fair && weak → ↓ 1 step (если ratio ≥ 0.95 или нет p10)
//   onAH <  fair && weak && ratio < 0.90 → ↑ 1 step, не выше p10
//   АХ категории полон / некуда выставить этот id → ↑ запрещён
//
// Норма 5 — стартовая; собираем capital_cycles и крутим.

const (
	relistSalesNorm         = 5
	relistDownUnderpriceMax = 0.95
	relistEmptyUpMaxRatio   = 0.90
)

func relistFairStock(share int) int {
	if share < 1 {
		return 1
	}
	return share
}

func adjustPriceRelist(
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

	ahFull := false
	noRoom := false
	cap := categoryAhCapacityLocked(cfg.Type)
	sumAH := 0
	for name, c := range ahCounts {
		if c <= 0 {
			continue
		}
		other, ok := itemsConfig[name]
		if !ok || other.Type != cfg.Type {
			continue
		}
		sumAH += c
	}
	if cap > 0 && sumAH >= cap {
		ahFull = true
		blockUp = true
	}
	if maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts) <= onAH {
		noRoom = true
		blockUp = true
	}

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	mutex.Lock()

	ratio, ratioOK := v9MarketRatio(price, p10, p10OK)
	weak := sales < relistSalesNorm
	fair := relistFairStock(share)
	stockHigh := onAH >= fair
	stockLow := onAH < fair

	action := "relist_hold"
	decReason := "no_signal"
	newPrice := price
	notes := []string{
		fmt.Sprintf(
			"relist sales=%d norm=%d weak=%v onAH=%d fair=%d (share=%d) low=%v high=%v inv=%d buys=%d p10=%d p10N=%d ratio=%s ahFull=%v noRoom=%v sumAH=%d/%d",
			sales, relistSalesNorm, weak, onAH, fair, share, stockLow, stockHigh, invCount, buys, p10, p10N,
			v9RatioStr(price, p10, p10OK), ahFull, noRoom, sumAH, cap,
		),
	}

	// DOWN: на АХ не меньше fair-доли, продаж меньше нормы
	if !blockDown && weak && stockHigh && step > 0 {
		if ratioOK && ratio < relistDownUnderpriceMax {
			action = "relist_hold_underprice_down_veto"
			decReason = "underprice_down_veto"
			notes = append(notes, fmt.Sprintf("↓ skip ratio=%.3f < %.2f", ratio, relistDownUnderpriceMax))
		} else {
			cand := price - step
			if cand < priceFloor {
				cand = priceFloor
			}
			if cand < price {
				action = "relist_price_down_stuck"
				decReason = "stuck_weak_sales"
				newPrice = cand
				notes = append(notes, fmt.Sprintf("↓ onAH=%d≥fair=%d sales=%d<%d", onAH, fair, sales, relistSalesNorm))
			} else {
				action = "relist_hold_floor"
				decReason = "floor"
			}
		}
	}

	// UP: меньше fair-доли, слабо, недооценены (только если ещё hold)
	if action == "relist_hold" || strings.HasPrefix(action, "relist_hold_") {
		if weak && stockLow && step > 0 {
			if blockUp {
				action = "relist_hold_empty_ah_full"
				decReason = "ah_full_or_no_room"
				notes = append(notes, "↑ skip ah full / no room")
			} else if !p10OK || p10 <= 0 {
				action = "relist_hold_empty_no_p10"
				decReason = "empty_no_p10"
				notes = append(notes, "↑ skip no thick p10")
			} else if !ratioOK || ratio >= relistEmptyUpMaxRatio {
				action = "relist_hold_empty_not_under"
				decReason = "empty_not_underpriced"
				notes = append(notes, fmt.Sprintf("↑ skip ratio=%s ≥ %.2f", v9RatioStr(price, p10, p10OK), relistEmptyUpMaxRatio))
			} else {
				cand := price + step
				if cand > p10 {
					cand = p10
				}
				if cand > price {
					action = "relist_price_up_empty"
					decReason = "low_stock_weak_underprice"
					newPrice = cand
					notes = append(notes, fmt.Sprintf("↑ onAH=%d<fair=%d sales=%d<%d → p10=%d", onAH, fair, sales, relistSalesNorm, p10))
				} else {
					action = "relist_hold_empty_cap"
					decReason = "empty_already_at_p10"
				}
			}
		}
	}

	if blockDown && strings.Contains(action, "price_down") {
		newPrice = priceBefore
		action = "hold_manual_min"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↓ запрещён")
	}
	if blockUp && strings.Contains(action, "price_up") {
		newPrice = priceBefore
		action = "hold_manual_max"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↑ запрещён")
	}

	if newPrice < priceFloor {
		newPrice = priceFloor
		if newPrice > priceBefore {
			action = "corridor_price_up_floor"
			notes = append(notes, fmt.Sprintf("пол %d", priceFloor))
		}
	}

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	if onAH > 0 {
		state.EmptyMarketGapStreak = 0
		state.EmptyInventoryClimbSteps = 0
		state.EmptyInventoryAnchorPrice = 0
	} else if sales == 0 && buys == 0 {
		state.EmptyMarketGapStreak++
	} else {
		state.EmptyMarketGapStreak = 0
	}
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	changed := newPrice != priceBefore
	if changed {
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
	}
	log.Printf("[RELIST] %s: %s reason=%s | цена %d→%d | onAH=%d fair=%d sales=%d/%d | %s",
		item, dir, decReason, priceBefore, newPrice, onAH, fair, sales, relistSalesNorm, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
		onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy + "+relist5",
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
		Share:          fair, // в лог — fair-доля АХ, не 32-share
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
		NacenkaAfter:   nacenka,
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

	needBroadcast := changed
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
		NacenkaAfter:  nacenka,
		Sales:         sales,
		Buys:          buys,
		TrySells:      trySells,
		OnAH:          onAH,
		Inv:           invCount,
		Held:          onAH,
		NormalSales:   relistSalesNorm,
		Share:         share,
		Free:          free,
		Need:          need,
		PriceFloor:    priceFloor,
		Step:          step,
	}
}
