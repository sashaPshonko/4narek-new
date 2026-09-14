package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// stock_corridor_v4 — пик live p/h (Jul 2026 ~199M/h) + v5/v6 guards.
// Inventory only: DOWN excess, UP understock+разбор. Без book/catchup/DOI/recover/empty↑.
// Rollback: capitalPolicy = capitalPolicyV9 | capitalPolicyV8af.

const capitalPolicyV4 = "stock_corridor_v4"

const (
	v4UpCooldownCycles = 2
	v4MaxUpStreak      = 1
	v4DayMinSalesUp    = 3
	v4NightMinSalesUp  = 4
)

func isPricingPolicyV4() bool {
	return capitalPolicy == capitalPolicyV4
}

type v4Input struct {
	Held, Sales, Buys   int
	Price, Step, Share  int
	UpCooldown, UpStreak int
	Night               bool
	BlockUp, BlockDown  bool
	PriceFloor          int
	Band                stockBandFracs
}

type v4Decision struct {
	Action     string
	NewPrice   int
	Reason     string
	UpCooldown int
	UpStreak   int
}

func v4MinSalesForUp(night bool) int {
	if night {
		return v4NightMinSalesUp
	}
	return v4DayMinSalesUp
}

// v4Decide — ядро эпохи v4–v6: inventory only (как pol_corridor_v5 в research).
func v4Decide(in v4Input) v4Decision {
	band := in.Band
	if band.hi == 0 {
		band = stockBandFracs{
			lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
			over: stockOverFrac, dump: stockDumpFrac,
		}
	}
	lo, hi, _, over, dump := stockTargets(in.Share, band)
	price := in.Price
	if price < in.PriceFloor && in.PriceFloor > 0 {
		price = in.PriceFloor
	}
	step := in.Step
	if step <= 0 {
		step = 1
	}

	upCD := in.UpCooldown
	if upCD > 0 {
		upCD--
	}
	upStreak := in.UpStreak

	out := v4Decision{
		Action:     "corridor_hold_v4_no_signal",
		NewPrice:   price,
		Reason:     "no_signal",
		UpCooldown: upCD,
		UpStreak:   0,
	}

	// DOWN — только excess (grant: held ≤ hi → никогда). Без sales==0 gate (как sim v4–v6).
	if !in.BlockDown && step > 0 && in.Held > hi {
		mult := 1
		action := "corridor_price_down_v4_soft"
		reason := "overstock"
		if in.Held >= dump {
			mult = corridorHardDownStepMult
			action = "corridor_price_down_v4_dump"
			reason = "dump"
		} else if in.Held >= over {
			mult = corridorHardDownStepMult
			action = "corridor_price_down_v4_over"
			reason = "over"
		}
		newP := price - mult*step
		if newP < in.PriceFloor {
			newP = in.PriceFloor
		}
		if newP < price {
			out.Action = action
			out.NewPrice = newP
			out.Reason = reason
			out.UpCooldown = upCD
			out.UpStreak = 0
			return out
		}
	}

	// UP — understock + weak_demand; held=0 запрещён (v5)
	canUp := !in.BlockUp && step > 0 && upCD == 0 && upStreak < v4MaxUpStreak
	minSales := v4MinSalesForUp(in.Night)
	if canUp && in.Held > 0 && in.Held < lo && in.Sales >= minSales && in.Sales > in.Buys {
		out.Action = "corridor_price_up_v4_demand"
		out.NewPrice = price + step
		out.Reason = "demand"
		out.UpCooldown = v4UpCooldownCycles
		out.UpStreak = upStreak + 1
		return out
	}

	out.UpCooldown = upCD
	if in.Held >= lo && in.Held <= hi {
		out.Action = "corridor_hold_v4_band"
		out.Reason = "band"
	} else if in.Held <= 0 {
		out.Action = "corridor_hold_v4_empty"
		out.Reason = "empty_no_up"
	}
	return out
}

// adjustPriceV4 — полный цикл под mutex (как adjustPriceV9, без AH book).
func adjustPriceV4(
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
) AdjustReport {
	band := stockBandFor(item, cfg)
	targetLo, targetHi, targetSoft, targetOver, _ := stockTargets(share, band)
	night := isNightMSK(now)
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)

	dec := v4Decide(v4Input{
		Held:       totalHeld,
		Sales:      sales,
		Buys:       buys,
		Price:      priceBefore,
		Step:       step,
		Share:      share,
		UpCooldown: state.CorridorUpCooldown,
		UpStreak:   state.CorridorUpStreak,
		Night:      night,
		BlockUp:    blockUp,
		BlockDown:  blockDown,
		PriceFloor: priceFloor,
		Band:       band,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("v4 reason=%s held=%d lo=%d hi=%d sales=%d buys=%d up_cd=%d",
			dec.Reason, totalHeld, targetLo, targetHi, sales, buys, dec.UpCooldown),
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

	state.CorridorUpCooldown = dec.UpCooldown
	state.CorridorUpStreak = dec.UpStreak
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
	log.Printf("[V4] %s: %s reason=%s | цена %d→%d | held %d/share %d | sales=%d buys=%d | %s",
		item, dir, dec.Reason, priceBefore, newPrice, totalHeld, share, sales, buys, action)

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
		Threshold:      float64(targetHi),
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
		Cooldown:       state.CorridorUpCooldown,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     state.CorridorUpStreak,
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}
	_ = targetSoft
	_ = targetOver

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
		Cooldown:      state.CorridorUpCooldown,
		GoodStreak:    state.CorridorUpStreak,
	}
}
