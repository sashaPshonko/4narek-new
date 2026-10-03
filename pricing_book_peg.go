package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// book_peg — статика с книги: sell = percentile(book_peg_q) − risk_cut.
// Для рисковых SKU (починка / крушитель / яд3). Коридор v10 не трогает.

const (
	bookPegModeName        = "book_peg"
	bookPegDefaultQ        = 0.10
	bookPegDefaultMinSell  = 3
	bookPegMultiMinLots    = 2
)

func isBookPegConfig(cfg ItemConfig) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.PriceMode), bookPegModeName)
}

func bookPegQ(cfg ItemConfig) float64 {
	if cfg.BookPegQ > 0 && cfg.BookPegQ < 1 {
		return cfg.BookPegQ
	}
	return bookPegDefaultQ
}

func bookPegMinSellers(cfg ItemConfig) int {
	if cfg.BookPegMinSellers > 0 {
		return cfg.BookPegMinSellers
	}
	return bookPegDefaultMinSell
}

// bookPegRawPercentile — перцентиль мульти-селлеров без risk_cut; n = число продавцов.
func bookPegRawPercentile(item string, since time.Time, q float64) (px int, n int, ok bool) {
	if q <= 0 || q >= 1 {
		return 0, 0, false
	}
	mps, mn := ahBookMultiSellerMinPricesSince(item, since, bookPegMultiMinLots)
	if mn >= 1 {
		px = ahBookPercentileSorted(mps, q)
		if px > 0 {
			return px, mn, true
		}
	}
	ps, nAll := ahBookSellerMinPricesSince(item, since)
	if nAll < 1 {
		return 0, 0, false
	}
	px = ahBookPercentileSorted(ps, q)
	if px <= 0 {
		return 0, nAll, false
	}
	return px, nAll, true
}

// bookPegTarget — цель sell: raw(q) − risk_cut. ok=false если мало продавцов / нет книги.
func bookPegTarget(cfg ItemConfig, since time.Time) (target int, raw int, q float64, n int, ok bool) {
	q = bookPegQ(cfg)
	minN := bookPegMinSellers(cfg)
	raw, n, ok = bookPegRawPercentile(cfg.ID, since, q)
	if !ok || n < minN || raw <= 0 {
		return 0, raw, q, n, false
	}
	target = applyRiskCut(raw, cfg)
	if target < 1 {
		target = 1
	}
	return target, raw, q, n, true
}

type bookPegDecision struct {
	Action   string
	NewPrice int
	Reason   string
}

func bookPegDecide(priceBefore, target int, bookOK bool) bookPegDecision {
	if !bookOK {
		return bookPegDecision{
			Action:   "book_peg_hold_thin",
			NewPrice: priceBefore,
			Reason:   "thin_book",
		}
	}
	if target == priceBefore {
		return bookPegDecision{
			Action:   "book_peg_hold",
			NewPrice: priceBefore,
			Reason:   "on_peg",
		}
	}
	if target > priceBefore {
		return bookPegDecision{
			Action:   "book_peg_price_up",
			NewPrice: target,
			Reason:   "peg_up",
		}
	}
	return bookPegDecision{
		Action:   "book_peg_price_down",
		NewPrice: target,
		Reason:   "peg_down",
	}
}

func adjustPriceBookPeg(
	item string,
	cfg ItemConfig,
	now time.Time,
	_ time.Time,
	sales, buys, trySells, profitNow int,
	state ItemAdjustState,
	priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor int,
	onAH, invCount, totalHeld, share, free, need, stockNorm int,
	underbuyOK bool,
	tryRatio, stockLoad float64,
	onlineForCap, onlineMaxForML int,
	_ map[string]int,
) AdjustReport {
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)

	bookSince := now.Add(-ahBook2Window)
	mutex.Unlock()
	target, raw, q, nSell, bookOK := bookPegTarget(cfg, bookSince)
	mutex.Lock()

	dec := bookPegDecide(priceBefore, target, bookOK)
	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("book_peg q=%.0f%% raw=%d cut=%d target=%d sellers=%d min=%d sales=%d buys=%d held=%d",
			q*100, raw, cfg.RiskCut, target, nSell, bookPegMinSellers(cfg), sales, buys, totalHeld),
	}

	if blockDown && strings.Contains(action, "price_down") {
		newPrice = priceBefore
		action = "hold_manual_min"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual min/set → ↓ запрещён")
	}
	if blockUp && strings.Contains(action, "price_up") {
		newPrice = priceBefore
		action = "hold_manual_max"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual max/set → ↑ запрещён")
	}
	if newPrice < priceFloor && priceFloor > 0 {
		newPrice = priceFloor
	}

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	if totalHeld > 0 {
		state.EmptyInventoryClimbSteps = 0
		state.EmptyInventoryAnchorPrice = 0
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
	log.Printf("[BOOK_PEG] %s: %s reason=%s | цена %d→%d | raw(p%.0f)=%d cut=%d | %s",
		item, dir, dec.Reason, priceBefore, newPrice, q*100, raw, cfg.RiskCut, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
		onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy + "+book_peg",
		Item:           item,
		Category:       cfg.Type,
		Action:         action,
		Winner:         action,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(bookPegMinSellers(cfg)),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		Share:          share,
		Free:           free,
		Need:           need,
		NormalSales:    cfg.NormalSales,
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

	_ = underbuyOK
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
		Held:          totalHeld,
		NormalSales:   cfg.NormalSales,
		Share:         share,
		Free:          free,
		Need:          need,
		PriceFloor:    priceFloor,
		Step:          step,
	}
}
