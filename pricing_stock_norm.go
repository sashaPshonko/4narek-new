package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// stock_norm_july11 — ядро 37e01ade (май–июль, норма стока / слоты АХ), без крутилки наценки.
//
// normal_count = порог переизбытка (не «держи ровно N»):
//   held > norm ∧ sales < NormalSales → ↓ sell
//   иначе sales < NormalSales → ↑ sell (переизбыток исключён)
// Книга не якорь цены; рычаг: пусто ∧ сильно ниже пола книги → ↑.
// Потолок ↑ (только ↑) — перцентиль минимальных цен мульти-селлеров (≥2 лота):
//   sharp5 → 60%, sharp6 → 50%, sword7 → 40%,
//   pochti → 25%, mega/яд3 → 10%, прочие мечи → 25%, остальное → 50%.
// ↓ не трогаем.
// set_min/set_max проверяются по книге в main.go.
//
// Rollback: capitalPolicy = capitalPolicyV9 (+book2).

const capitalPolicyStockNorm = "stock_norm_july11"

// Пусто + цена ниже bookFloor×ratio → можно ↑ к рынку.
const stockNormBookCatchupRatio = 0.85

const stockNormBookMidMultiMinLots = 2

func isPricingPolicyStockNorm() bool {
	return capitalPolicy == capitalPolicyStockNorm
}

type stockNormInput struct {
	Held, OnAH, Sales, Buys, TrySells int
	StockNorm, NormalSales            int
	Price, Step, PriceFloor           int
	BookFloor                         int
	BookOK                            bool
	BookMid                           int // потолок ↑ из книги; 0 = нет
	BookMidOK                         bool
	BookMidQ                          float64 // какой % использовали (лог)
	BlockUp, BlockDown                bool
}

type stockNormDecision struct {
	Action   string
	NewPrice int
	Reason   string
}

func stockNormDecide(in stockNormInput) stockNormDecision {
	price := in.Price
	if in.PriceFloor > 0 && price < in.PriceFloor {
		price = in.PriceFloor
	}
	step := in.Step
	if step <= 0 {
		step = 1
	}
	norm := in.StockNorm
	if norm <= 0 {
		norm = 4
	}
	nSales := in.NormalSales
	if nSales <= 0 {
		nSales = 5
	}

	out := stockNormDecision{Action: "stock_norm_hold", NewPrice: price, Reason: "no_signal"}

	applyDown := func(action, reason string) {
		if in.BlockDown {
			out.Action = "hold_manual_min"
			out.Reason = "manual → ↓ запрещён"
			return
		}
		cand := price - step
		if in.PriceFloor > 0 && cand < in.PriceFloor {
			cand = in.PriceFloor
		}
		if cand >= price {
			out.Action = "stock_norm_hold_floor"
			out.Reason = reason + " · floor"
			return
		}
		out.Action = action
		out.NewPrice = cand
		out.Reason = reason
	}

	applyUp := func(action, reason string) {
		if in.BlockUp {
			out.Action = "hold_manual_max"
			out.Reason = "manual → ↑ запрещён"
			return
		}
		cand := price + step
		if in.BookMidOK && in.BookMid > 0 {
			qTag := fmt.Sprintf("p%.0f", in.BookMidQ*100)
			if in.BookMidQ <= 0 {
				qTag = "pmid"
			}
			if price >= in.BookMid {
				out.Action = "stock_norm_hold_book_mid"
				out.Reason = fmt.Sprintf("%s · уже ≥ bookMid(%s)=%d", reason, qTag, in.BookMid)
				return
			}
			if cand > in.BookMid {
				cand = in.BookMid
				reason = reason + fmt.Sprintf(" · cap bookMid(%s)=%d", qTag, in.BookMid)
			}
		}
		out.Action = action
		out.NewPrice = cand
		out.Reason = reason
	}

	// 1) Переизбыток: сток выше порога + слабые продажи → ↓
	if in.Held > norm && in.Sales < nSales {
		applyDown("stock_norm_price_down_overstock",
			fmt.Sprintf("held=%d > norm=%d ∧ sales=%d < %d", in.Held, norm, in.Sales, nSales))
		return out
	}

	// 2) Книга-рычаг: пусто и сильно ниже пола → ↑
	if in.Held <= 0 && in.BookOK && in.BookFloor > 0 {
		cap := int(float64(in.BookFloor)*stockNormBookCatchupRatio + 0.5)
		if price < cap {
			applyUp("stock_norm_price_up_book_empty",
				fmt.Sprintf("empty ∧ price=%d < %.0f%% bookFloor=%d", price, stockNormBookCatchupRatio*100, in.BookFloor))
			return out
		}
	}

	// 3) Недобор продаж при отсутствии переизбытка → ↑
	if in.Sales < nSales {
		applyUp("stock_norm_price_up_deficit",
			fmt.Sprintf("sales=%d < %d ∧ held=%d ≤ norm=%d", in.Sales, nSales, in.Held, norm))
		return out
	}

	return out
}

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

// stockNormBookMidQForItem — какой перцентиль мульти-селлеров режет ↑ для SKU.
func stockNormBookMidQForItem(item string) float64 {
	id := strings.ToLower(strings.TrimSpace(item))
	switch {
	case id == "sword-sharp5-1.21":
		return 0.60
	case id == "sword-sharp6-1.21":
		return 0.50
	case id == "sword7-1.21":
		return 0.40
	case id == "megasword-1.21":
		return 0.10
	case strings.Contains(id, "яд") && strings.Contains(id, "megasword"):
		return 0.10
	case strings.Contains(id, "pochti"):
		return 0.25
	case strings.Contains(id, "sword"):
		return 0.25 // loot/bare и прочие мечи
	default:
		return 0.50 // броня / кирки — середина
	}
}

// stockNormBookMid — потолок ↑ из мульти-селлеров (fallback: все селлеры, тот же %).
// Только блокирует ↑; ↓/overstock не трогает.
func stockNormBookMid(item string, since time.Time) (mid int, q float64, ok bool) {
	q = stockNormBookMidQForItem(item)
	if mps, mn := ahBookMultiSellerMinPricesSince(item, since, stockNormBookMidMultiMinLots); mn >= 1 {
		mid = ahBookPercentileSorted(mps, q)
		if mid > 0 {
			return mid, q, true
		}
	}
	ps, n := ahBookSellerMinPricesSince(item, since)
	if n < ahBookMarketMinSellers {
		return 0, q, false
	}
	mid = ahBookPercentileSorted(ps, q)
	if mid <= 0 {
		return 0, q, false
	}
	return mid, q, true
}

func adjustPriceStockNorm(
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
	ahCounts map[string]int,
) AdjustReport {
	if step <= 0 {
		step = 1
	}
	if stockNorm <= 0 {
		stockNorm = stockNormFromConfig(cfg)
	}
	if stockNorm <= 0 {
		stockNorm = 4
	}
	normalSales := cfg.NormalSales
	if normalSales <= 0 {
		normalSales = 5
	}

	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)

	mutex.Unlock()
	bookSince := now.Add(-ahBook2Window)
	bookFloor, bookOK := stockNormBookFloor(item, bookSince)
	bookMid, bookMidQ, bookMidOK := stockNormBookMid(item, bookSince)
	mutex.Lock()

	nacT := nacenka
	if nacT <= 0 {
		nacT = nacenkaBaseLocked(item, cfg)
	}

	dec := stockNormDecide(stockNormInput{
		Held:        totalHeld,
		OnAH:        onAH,
		Sales:       sales,
		Buys:        buys,
		TrySells:    trySells,
		StockNorm:   stockNorm,
		NormalSales: normalSales,
		Price:       priceBefore,
		Step:        step,
		PriceFloor:  priceFloor,
		BookFloor:   bookFloor,
		BookOK:      bookOK,
		BookMid:     bookMid,
		BookMidOK:   bookMidOK,
		BookMidQ:    bookMidQ,
		BlockUp:     blockUp,
		BlockDown:   blockDown,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	maxReach := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
	notes := []string{
		fmt.Sprintf(
			"stock_norm held=%d onAH=%d inv=%d norm=%d maxReach=%d sales=%d/%d bookFloor=%d ok=%v bookMid=%d q=%.2f midOK=%v | %s",
			totalHeld, onAH, invCount, stockNorm, maxReach, sales, normalSales, bookFloor, bookOK, bookMid, bookMidQ, bookMidOK, dec.Reason,
		),
	}

	// Слоты АХ забиты другими id → норма недостижима → не ↑ (как 37e01ade).
	if strings.Contains(action, "price_up") &&
		!allowSellPriceIncreaseLocked(item, cfg, onAH, ahCounts, stockNorm) {
		newPrice = priceBefore
		action = "stock_norm_hold_slots"
		notes = append(notes, fmt.Sprintf("↑ blocked: maxReachable=%d < norm=%d (слоты заняты другими)", maxReach, stockNorm))
	}

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
	log.Printf("[STOCK_NORM] %s: %s %s | цена %d→%d nac=%d | held=%d norm=%d sales=%d book=%d mid=%d | %s",
		item, dir, action, priceBefore, newPrice, nacT, totalHeld, stockNorm, sales, bookFloor, bookMid, dec.Reason)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, nacT,
		now,
		onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy,
		Item:           item,
		Category:       cfg.Type,
		Action:         action,
		Winner:         action,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(stockNorm),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		Share:          share,
		Free:           free,
		Need:           need,
		NormalSales:    normalSales,
		NormalCount:    stockNorm,
		TryRatio:       tryRatio,
		StockLoad:      stockLoad,
		Underbuy:       underbuyOK,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   nacT,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		PriceFloor:     priceFloor,
		Step:           step,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   botsForGoTypeLocked(cfg.Type),
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}
	_ = ahCounts

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
		NacenkaAfter:  nacT,
		Sales:         sales,
		Buys:          buys,
		TrySells:      trySells,
		OnAH:          onAH,
		Inv:           invCount,
		Held:          totalHeld,
		NormalSales:   normalSales,
		Share:         share,
		Free:          free,
		Need:          need,
		PriceFloor:    priceFloor,
		Step:          step,
	}
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
