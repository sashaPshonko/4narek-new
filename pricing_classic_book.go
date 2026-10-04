package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// classic_book_2026_10 — Exact NormalSales + книжные рельсы + grant/fleet-совместимые guards.
// Без пола «не ниже самой дешёвой покупки». Rollback: capitalPolicy = capitalPolicyV9.

const capitalPolicyClassicBook = "classic_book_2026_10"

func isPricingPolicyClassicBook() bool {
	return capitalPolicy == capitalPolicyClassicBook
}

type classicBookInput struct {
	Sales, Buys, OnAH, Inv, NormalSales, Price, Step int
	ShareHi                                          int
	BookFloor                                        int
	BookFloorOK                                      bool
	BookMid                                          int
	BookMidOK                                        bool
	P10                                              int
	P10OK                                            bool
	IsLeader                                         bool
	BlockUp, BlockDown                               bool
}

type classicBookDecision struct {
	Action   string
	NewPrice int
	Reason   string
}

func classicBookDownFloor(in classicBookInput) int {
	if in.BookFloorOK && in.BookFloor > 0 {
		return in.BookFloor
	}
	return 0
}

// classicBookDecide — рельсы книги, затем classic NormalSales; ↓ только при stock > shareHi.
func classicBookDecide(in classicBookInput) classicBookDecision {
	price := in.Price
	step := in.Step
	if step <= 0 {
		step = 1
	}
	n := in.NormalSales
	if n <= 0 {
		n = 5
	}
	stock := in.OnAH + in.Inv
	floor := classicBookDownFloor(in)

	out := classicBookDecision{
		Action:   "classic_book_hold",
		NewPrice: price,
		Reason:   "no_signal",
	}

	// --- Рельсы книги (всегда первыми) ---
	if !in.BlockUp && in.BookFloorOK && in.BookFloor > 0 && price < in.BookFloor {
		out.Action = "classic_book_floor_jump"
		out.NewPrice = in.BookFloor
		out.Reason = "below_book_floor_jump"
		return out
	}
	if !in.BlockDown && in.BookMidOK && in.BookMid > 0 && price > in.BookMid {
		out.Action = "classic_book_mid_jump"
		out.NewPrice = in.BookMid
		out.Reason = "above_book_mid_jump"
		return out
	}

	// --- UP: sales < N ∧ stock < N*3 (пусто тоже ↑) ---
	if !in.BlockUp && in.Sales < n && stock < n*1.5{
		ratio, ratioOK := v9MarketRatio(price, in.P10, in.P10OK)
		if ratioOK && ratio >= v9DemandMaxRatio {
			out.Action = "classic_book_hold_above_market"
			out.Reason = "above_market_no_up"
			return out
		}
		newP := price + step
		if in.BookMidOK && in.BookMid > 0 && newP > in.BookMid {
			newP = in.BookMid
		}
		if newP > price {
			out.Action = "classic_book_price_up"
			out.NewPrice = newP
			out.Reason = "sales_below_normal"
			return out
		}
		out.Action = "classic_book_hold_at_mid"
		out.Reason = "at_book_mid"
		return out
	}

	// --- DOWN только если сток уже не тонкий (grant) ---
	if in.BlockDown || stock <= in.ShareHi || step <= 0 {
		return out
	}

	clampDown := func(p int) int {
		if floor > 0 && p < floor {
			p = floor
		}
		return p
	}

	// 2a. АХ раздут + слабые продажи
	if (in.OnAH > in.Sales && in.OnAH > n) && in.Sales < n {
		newP := clampDown(price - step)
		if newP < price {
			out.Action = "classic_book_price_down_ah"
			out.NewPrice = newP
			out.Reason = "ah_bloated_weak_sales"
			return out
		}
		out.Action = "classic_book_hold_at_floor"
		out.Reason = "at_book_floor"
		return out
	}

	// 2b. Закуп обогнал слив
	if float64(in.Buys) > float64(in.Sales)*2 && stock > n {
		newP := clampDown(price - step)
		if newP < price {
			out.Action = "classic_book_price_down_buys"
			out.NewPrice = newP
			out.Reason = "buy_excess"
			return out
		}
		out.Action = "classic_book_hold_at_floor"
		out.Reason = "at_book_floor"
		return out
	}

	return out
}

func adjustPriceClassicBook(
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
	ahCounts, invCounts map[string]int,
) AdjustReport {
	band := stockBandFor(item, cfg)
	_, shareHi, _, _, _ := stockTargets(share, band)
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	treasuryCashBlocksUp := blockUpTreasuryCashShortLocked(cfg, totalHeld)
	if treasuryCashBlocksUp {
		blockUp = true
	}
	leaderID := classicTypeLeaderLocked(cfg.Type, ahCounts, invCounts)

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	multiFloor, multiFloorQ, multiFloorOK := stockNormBookCatchupFloor(cfg, bookSince)
	bookMid, bookMidQ, bookMidOK := stockNormBookMid(cfg, now.Add(-ahBook2Window))
	mutex.Lock()

	dec := classicBookDecide(classicBookInput{
		Sales:       sales,
		Buys:        buys,
		OnAH:        onAH,
		Inv:         invCount,
		NormalSales: cfg.NormalSales,
		Price:       priceBefore,
		Step:        step,
		ShareHi:     shareHi,
		BookFloor:   multiFloor,
		BookFloorOK: multiFloorOK,
		BookMid:     bookMid,
		BookMidOK:   bookMidOK,
		P10:         p10,
		P10OK:       p10OK,
		IsLeader:    item == leaderID,
		BlockUp:     blockUp,
		BlockDown:   blockDown,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("classic_book reason=%s sales=%d N=%d onAH=%d inv=%d stock=%d shareHi=%d leader=%s p10=%d floor(p%.0f)=%d mid(p%.0f)=%d ratio=%s",
			dec.Reason, sales, cfg.NormalSales, onAH, invCount, totalHeld, shareHi, leaderID, p10,
			multiFloorQ*100, multiFloor, bookMidQ*100, bookMid,
			v9RatioStr(priceBefore, p10, p10OK)),
	}
	if treasuryCashBlocksUp {
		notes = append(notes, "treasury_empty + held>0 → ↑ gated")
	}
	_ = priceFloor // buy10m/nac floor намеренно не используем

	if blockDown && strings.Contains(action, "price_down") {
		newPrice = priceBefore
		action = "hold_manual_min"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual min/set → ↓ запрещён")
	}
	if blockUp && (strings.Contains(action, "price_up") || strings.Contains(action, "floor_jump")) {
		newPrice = priceBefore
		action = "hold_manual_max"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual max/set → ↑ запрещён")
	}

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
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
	if strings.Contains(action, "price_up") || strings.Contains(action, "floor_jump") {
		dir = "UP"
	} else if strings.Contains(action, "price_down") || strings.Contains(action, "mid_jump") {
		dir = "DOWN"
	}
	log.Printf("[CLASSIC_BOOK] %s: %s reason=%s | цена %d→%d | onAH=%d inv=%d sales=%d/%d buys=%d | %s",
		item, dir, dec.Reason, priceBefore, newPrice, onAH, invCount, sales, cfg.NormalSales, buys, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, nacenka,
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
		Threshold:      float64(cfg.NormalSales),
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
		PriceFloor:     multiFloor,
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

	shadowSnap := mlAdjustSnapshot{}
	if mlShadowEnabled() {
		shadowSnap = mlAdjustSnapshot{
			At: now, Item: item, CategoryType: cfg.Type, GoAction: action,
		}
	}
	needBroadcast := changed
	mutex.Unlock()

	logCapitalCycle(capitalRow)
	if mlShadowEnabled() {
		runMLShadowAsync(shadowSnap)
	}
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
		Held:          totalHeld,
		NormalSales:   cfg.NormalSales,
		Share:         share,
		Free:          free,
		Need:          need,
		PriceFloor:    multiFloor,
		Step:          step,
	}
}
