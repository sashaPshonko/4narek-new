package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// classic_2026_02_22 — Exact adjustPrice from b96739c5 (22.02.2026 15:46),
// до товаров *-1.21 и до enoughItems в ботах.
// Rollback: capitalPolicyV4 / V9 / V8af.

const capitalPolicyClassic = "classic_2026_02_22"

func isPricingPolicyClassic() bool {
	return capitalPolicy == capitalPolicyClassic
}

type classicInput struct {
	Sales, Buys, OnAH, Inv, NormalSales, Price, Step, PriceFloor int
	IsLeader                                                     bool
	BlockUp, BlockDown                                           bool
}

type classicDecision struct {
	Action   string
	NewPrice int
	Reason   string
}

// classicDecide — ядро Feb 22 NormalSales (без MaxPrice; пол = priceFloor).
func classicDecide(in classicInput) classicDecision {
	price := in.Price
	if price < in.PriceFloor && in.PriceFloor > 0 {
		price = in.PriceFloor
	}
	step := in.Step
	if step <= 0 {
		step = 1
	}
	n := in.NormalSales
	if n <= 0 {
		n = 5
	}
	totalStock := in.OnAH + in.Inv

	out := classicDecision{
		Action:   "classic_hold",
		NewPrice: price,
		Reason:   "no_signal",
	}

	// 1. UP — мало продаж и сток не раздут
	if !in.BlockUp && in.Sales < n && totalStock < n*3 {
		out.Action = "classic_price_up"
		out.NewPrice = price + step
		out.Reason = "sales_below_normal"
		return out
	}

	// 2. DOWN — АХ раздут + слабые продажи
	if !in.BlockDown && (in.OnAH > in.Sales && in.OnAH > n) && in.Sales < n {
		newP := price - step
		if newP < in.PriceFloor {
			newP = in.PriceFloor
		}
		if newP < price {
			out.Action = "classic_price_down_ah"
			out.NewPrice = newP
			out.Reason = "ah_bloated_weak_sales"
			return out
		}
	}

	// 3. DOWN — избыток покупок
	if !in.BlockDown && float64(in.Buys) > float64(in.Sales)*2 && totalStock > n {
		newP := price - step
		if newP < in.PriceFloor {
			newP = in.PriceFloor
		}
		if newP < price {
			out.Action = "classic_price_down_buys"
			out.NewPrice = newP
			out.Reason = "buy_excess"
			return out
		}
	}

	// 4. DOWN — лидер типа, перенасыщение 3.5×
	if !in.BlockDown && in.IsLeader {
		salesLeader := n
		if in.Sales > n {
			salesLeader = in.Sales
		}
		if float64(totalStock) > float64(salesLeader)*3.5 {
			newP := price - step
			if newP < in.PriceFloor {
				newP = in.PriceFloor
			}
			if newP < price {
				out.Action = "classic_price_down_leader"
				out.NewPrice = newP
				out.Reason = "leader_oversupply"
				return out
			}
		}
	}

	return out
}

func classicTypeLeaderLocked(itemType string, ahCounts, invCounts map[string]int) string {
	leaderID := ""
	maxTotal := -1
	for name, conf := range itemsConfig {
		if conf.Type != itemType {
			continue
		}
		total := ahCounts[name] + invCounts[name]
		if total > maxTotal || (total == maxTotal && (leaderID == "" || name < leaderID)) {
			maxTotal = total
			leaderID = name
		}
	}
	return leaderID
}

func adjustPriceClassic(
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
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	leaderID := classicTypeLeaderLocked(cfg.Type, ahCounts, invCounts)

	dec := classicDecide(classicInput{
		Sales:       sales,
		Buys:        buys,
		OnAH:        onAH,
		Inv:         invCount,
		NormalSales: cfg.NormalSales,
		Price:       priceBefore,
		Step:        step,
		PriceFloor:  priceFloor,
		IsLeader:    item == leaderID,
		BlockUp:     blockUp,
		BlockDown:   blockDown,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("classic reason=%s sales=%d N=%d onAH=%d inv=%d stock=%d leader=%s",
			dec.Reason, sales, cfg.NormalSales, onAH, invCount, totalHeld, leaderID),
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

	if newPrice < priceFloor {
		newPrice = priceFloor
		if newPrice > priceBefore {
			action = "corridor_price_up_floor"
			notes = append(notes, fmt.Sprintf("цена %d < пола %d → поднимаем", priceBefore, priceFloor))
		}
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
	if strings.Contains(action, "price_up") {
		dir = "UP"
	} else if strings.Contains(action, "price_down") {
		dir = "DOWN"
	}
	log.Printf("[CLASSIC] %s: %s reason=%s | цена %d→%d | onAH=%d inv=%d sales=%d/%d buys=%d | %s",
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
		PriceFloor:     priceFloor,
		Step:           step,
		Cooldown:       0,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
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
		PriceFloor:    priceFloor,
		Step:          step,
	}
}
