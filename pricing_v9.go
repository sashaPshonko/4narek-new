package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// stock_corridor_v9 — inventory corridor + market guards (Sep 2026 research).
// Не патч v8af: отдельная политика. Rollback: capitalPolicy = capitalPolicyV8af.

const (
	capitalPolicyV8af = "stock_corridor_v8af"
	capitalPolicyV9   = "stock_corridor_v9"

	v9DownBlockRatio   = 0.90 // ratio < this → DOWN запрещён
	v9CatchupGapRatio  = 0.80 // empty catchup только при our/mkt < this
	v9DemandMaxRatio   = 1.05 // demand UP только при our/mkt < this
	v9CatchupArmCycles = 2
	v9UpCooldownCycles  = 2
	v9MaxUpStreak      = 1
	v9DayMinSalesUp    = 3
	v9NightMinSalesUp  = 4
)

func isPricingPolicyV9() bool {
	return capitalPolicy == capitalPolicyV9
}

// v9Input — снимок цикла для чистого решения (unit-testable).
type v9Input struct {
	Held, Sales, Buys   int
	Price, Step, Share  int
	P10                 int
	P10OK               bool // thick raw p10: p10N≥40
	EmptyStreak         int  // до обновления этим циклом
	UpCooldown, UpStreak int
	Night               bool
	BlockUp, BlockDown  bool
	PriceFloor          int
	Band                stockBandFracs
}

// v9Decision — результат одного цикла.
type v9Decision struct {
	Action      string
	NewPrice    int
	Reason      string
	EmptyStreak int
	UpCooldown  int
	UpStreak    int
}

func v9MarketRatio(price, p10 int, p10OK bool) (ratio float64, ok bool) {
	if !p10OK || p10 <= 0 || price <= 0 {
		return 0, false
	}
	return float64(price) / float64(p10), true
}

func v9MinSalesForUp(night bool) int {
	if night {
		return v9NightMinSalesUp
	}
	return v9DayMinSalesUp
}

// v9Decide — ядро v9. Порядок: DOWN veto → DOWN → UP demand → UP catchup → HOLD.
func v9Decide(in v9Input) v9Decision {
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

	emptyStreak := in.EmptyStreak
	if in.Held == 0 && in.Sales == 0 && in.Buys == 0 {
		emptyStreak++
	} else {
		emptyStreak = 0
	}

	upCD := in.UpCooldown
	if upCD > 0 {
		upCD--
	}
	upStreak := in.UpStreak

	ratio, ratioOK := v9MarketRatio(price, in.P10, in.P10OK)

	out := v9Decision{
		Action:      "corridor_hold_v9_no_signal",
		NewPrice:    price,
		Reason:      "no_signal",
		EmptyStreak: emptyStreak,
		UpCooldown:  upCD,
		UpStreak:    0,
	}

	// --- DOWN (только excess; жёсткие veto нельзя обойти) ---
	if !in.BlockDown && step > 0 {
		downBlocked := false
		downReason := ""
		if in.Held <= hi {
			downBlocked = true
			downReason = "low_stock_down_veto"
		} else if ratioOK && ratio < v9DownBlockRatio {
			downBlocked = true
			downReason = "underprice_down_veto"
		}

		if !downBlocked && in.Held > hi {
			mult := 1
			action := "corridor_price_down_v9_soft"
			reason := "overstock"
			if in.Held >= dump {
				mult = corridorHardDownStepMult
				action = "corridor_price_down_v9_dump"
				reason = "dump"
			} else if in.Held >= over {
				mult = corridorHardDownStepMult
				action = "corridor_price_down_v9_over"
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
				out.UpStreak = 0
				out.UpCooldown = upCD
				return out
			}
		}
		if downBlocked && in.Held > hi {
			// Явно логируем veto при excess, который иначе выглядел бы как DOWN.
			out.Action = "corridor_hold_v9_" + downReason
			out.Reason = downReason
			out.UpCooldown = upCD
			out.UpStreak = 0
			_ = lo // understock used below
			return out
		}
	}

	// --- UP ---
	canUp := !in.BlockUp && step > 0 && upCD == 0 && upStreak < v9MaxUpStreak

	// 1) demand
	minSales := v9MinSalesForUp(in.Night)
	if canUp && in.Held > 0 && in.Held < lo && in.Sales >= minSales && in.Sales > in.Buys {
		if ratioOK && ratio >= v9DemandMaxRatio {
			out.Action = "corridor_hold_v9_demand_above_market"
			out.Reason = "demand_above_market"
			out.UpCooldown = upCD
			return out
		}
		newP := price + step
		out.Action = "corridor_price_up_v9_demand"
		out.NewPrice = newP
		out.Reason = "demand"
		out.UpCooldown = v9UpCooldownCycles
		out.UpStreak = upStreak + 1
		return out
	}

	// 2) empty catchup (raw p10 thick only)
	if canUp && in.Held == 0 && emptyStreak >= v9CatchupArmCycles && in.P10OK && in.P10 > 0 {
		if !ratioOK || ratio >= v9CatchupGapRatio {
			out.Action = "corridor_hold_v9_catchup_no_gap"
			out.Reason = "catchup_no_gap"
			out.UpCooldown = upCD
			return out
		}
		if price+step > in.P10 {
			out.Action = "corridor_hold_v9_catchup_cap"
			out.Reason = "catchup_above_market"
			out.UpCooldown = upCD
			return out
		}
		out.Action = "corridor_price_up_v9_empty_catchup"
		out.NewPrice = price + step
		out.Reason = "empty_catchup"
		out.UpCooldown = v9UpCooldownCycles
		out.UpStreak = upStreak + 1
		return out
	}

	out.UpCooldown = upCD
	out.UpStreak = 0
	if in.Held <= hi && in.Held >= lo {
		out.Action = "corridor_hold_v9_band"
		out.Reason = "band"
	}
	return out
}

// adjustPriceV9 — полный цикл под mutex (вызывающий уже держит Lock с начала adjustPrice).
// Книгу читает вне Lock; состояние/цены пишет обратно под Lock.
func adjustPriceV9(
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

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	// Thick = same raw sample as p10 (no ban-filter bookOK).
	if p10N < ahBookMinLotsInWindow || p10 <= 0 {
		p10OK = false
	}
	mutex.Lock()

	dec := v9Decide(v9Input{
		Held:        totalHeld,
		Sales:       sales,
		Buys:        buys,
		Price:       priceBefore,
		Step:        step,
		Share:       share,
		P10:         p10,
		P10OK:       p10OK,
		EmptyStreak: state.EmptyMarketGapStreak,
		UpCooldown:  state.CorridorUpCooldown,
		UpStreak:    state.CorridorUpStreak,
		Night:       night,
		BlockUp:     blockUp,
		BlockDown:   blockDown,
		PriceFloor:  priceFloor,
		Band:        band,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("v9 reason=%s held=%d lo=%d hi=%d sales=%d buys=%d p10=%d p10N=%d ratio=%s empty_streak=%d up_cd=%d",
			dec.Reason, totalHeld, targetLo, targetHi, sales, buys, p10, p10N,
			v9RatioStr(priceBefore, p10, p10OK), dec.EmptyStreak, dec.UpCooldown),
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

	state.EmptyMarketGapStreak = dec.EmptyStreak
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
	log.Printf("[V9] %s: %s reason=%s | цена %d→%d | held %d/share %d | sales=%d buys=%d | %s",
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

func v9RatioStr(price, p10 int, ok bool) string {
	if !ok || p10 <= 0 {
		return "na"
	}
	return fmt.Sprintf("%.3f", float64(price)/float64(p10))
}
