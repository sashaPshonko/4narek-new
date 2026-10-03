package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// stock_corridor_v10 — rails + цель по загрузке стока (см. v9_research/policy_v10_rails.py).
// Отличие от v9: при пустом/тонком стоке тянем цену к цели (↑), не требуя sales≥3.
// Rollback: capitalPolicy = capitalPolicyV9.

const capitalPolicyV10 = "stock_corridor_v10"

const (
	v10DeadbandSteps = 0.75
	v10MaxSteps      = 2
	v10TurnLow       = 0.08
	v10TurnHigh      = 0.25
)

func isPricingPolicyV10() bool {
	return capitalPolicy == capitalPolicyV10
}

type v10Input struct {
	Held, Sales, Buys, TrySells int
	Price, Step, Share          int
	Nacenka                     int // для режима забивки: потолок sell ≈ bookMid+nac
	MultiFloor                  int
	MultiFloorOK                bool
	BookMid                     int
	BookMidOK                   bool
	BlockUp, BlockDown          bool
	PriceFloor                  int
	Band                        stockBandFracs
}

type v10Decision struct {
	Action   string
	NewPrice int
	Reason   string
}

func v10PosTarget(load float64) float64 {
	switch {
	case load <= 0.05:
		return 0.90
	case load < 0.18:
		return 0.75
	case load <= 0.25:
		return 0.50
	case load < 0.35:
		return 0.28
	case load < 0.50:
		return 0.12
	default:
		return 0.05
	}
}

func v10RailSpan(price, step, floor int, floorOK bool, mid int, midOK bool) (int, int) {
	f := floor
	if !floorOK || f <= 0 {
		f = step
		if f < 1 {
			f = 1
		}
		if q := price / 4; q > f {
			f = q
		}
	}
	m := mid
	if !midOK || m <= 0 {
		m = price + 10*step
	}
	if m <= f {
		m = f + step
	}
	return f, m
}

func v10BandPos(price, floor, mid int) float64 {
	if mid <= floor {
		return 0.5
	}
	p := float64(price-floor) / float64(mid-floor)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

func v10Turn(sales, held int) float64 {
	h := held
	if h < 1 {
		h = 1
	}
	return float64(sales) / float64(h)
}

// v10Decide — ядро: рельсы → цель по load → ↓ при затаре+слабом сливе / ↑ при недоборе/пустоте.
// Режим забивки (held < lo): потолок не прибиваем к bookMid — держим до bookMid+nacenka,
// чтобы закуп (=sell−nac) доставал до рынка. Ночь отдельно не трогаем: день/ночь душат одинаково.
func v10Decide(in v10Input) v10Decision {
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

	out := v10Decision{
		Action:   "corridor_hold_v10_no_signal",
		NewPrice: price,
		Reason:   "no_signal",
	}

	fillMode := in.Held < lo
	capMid := in.BookMid
	if fillMode && in.BookMidOK && in.BookMid > 0 && in.Nacenka > 0 {
		capMid = in.BookMid + in.Nacenka
	}

	if !in.BlockUp && in.MultiFloorOK && in.MultiFloor > 0 && price < in.MultiFloor {
		out.Action = "corridor_price_up_v10_book_floor"
		out.NewPrice = in.MultiFloor
		out.Reason = "below_floor"
		return out
	}
	if !in.BlockDown && in.BookMidOK && capMid > 0 && price > capMid {
		out.Action = "corridor_price_down_v10_book_mid"
		out.NewPrice = capMid
		if fillMode {
			out.Reason = "above_fill_cap"
		} else {
			out.Reason = "above_mid"
		}
		return out
	}

	railMid := in.BookMid
	railMidOK := in.BookMidOK
	if fillMode && in.BookMidOK && in.BookMid > 0 && in.Nacenka > 0 {
		railMid = capMid
	}
	floor, mid := v10RailSpan(price, step, in.MultiFloor, in.MultiFloorOK, railMid, railMidOK)
	load := 0.0
	if in.Share > 0 {
		load = float64(in.Held) / float64(in.Share)
	}
	turn := v10Turn(in.Sales, in.Held)
	pos := v10BandPos(price, floor, mid)
	target := int(float64(floor) + v10PosTarget(load)*float64(mid-floor) + 0.5)
	if target < floor {
		target = floor
	}
	if target > mid {
		target = mid
	}
	dead := int(v10DeadbandSteps * float64(step))
	if dead < 1 {
		dead = 1
	}

	if absInt(price-target) <= dead {
		out.Action = "corridor_hold_v10_deadband"
		out.Reason = "near_target"
		return out
	}

	// DOWN: excess + weak sell-through
	if !in.BlockDown && target < price-dead && in.Held > hi {
		wantDown := false
		reason := "hold_flow_ok"
		if turn < v10TurnLow {
			wantDown = true
			reason = "low_turn"
		} else if in.Held >= dump && turn < v10TurnHigh {
			wantDown = true
			reason = "dump_zone"
		} else if in.Held >= over && turn < (v10TurnLow+v10TurnHigh)/2 {
			wantDown = true
			reason = "over_mid_turn"
		}
		if !wantDown {
			out.Action = "corridor_hold_v10_sellthrough"
			out.Reason = reason
			return out
		}
		delta := v10MaxSteps * step
		if gap := price - target; gap < delta {
			delta = gap
		}
		if in.Held >= over {
			d2 := 2 * step
			if gap := price - target; d2 > gap {
				d2 = gap
			}
			if d2 > delta {
				delta = d2
			}
		}
		newP := price - delta
		if newP < target {
			newP = target
		}
		if newP < floor {
			newP = floor
		}
		if newP < in.PriceFloor {
			newP = in.PriceFloor
		}
		if newP < price {
			out.Action = "corridor_price_down_v10"
			out.NewPrice = newP
			out.Reason = reason
			return out
		}
		out.Action = "corridor_hold_v10_book_floor"
		out.Reason = "at_floor"
		return out
	}
	if target < price-dead && in.Held <= hi {
		out.Action = "corridor_hold_v10_no_excess"
		out.Reason = "no_excess"
		return out
	}

	// UP: understock / empty — без требования сильных продаж
	if !in.BlockUp && target > price+dead {
		under := in.Held > 0 && in.Held < lo
		empty := in.Held == 0
		if pos >= 0.85 && in.TrySells >= 3 && in.Sales == 0 {
			out.Action = "corridor_hold_v10_up_veto"
			out.Reason = "dead_near_ceiling"
			return out
		}
		if under || empty {
			strong := in.Sales >= 3 && in.Sales > in.Buys
			hotTurn := turn >= v10TurnHigh
			anyFlow := in.Sales >= 1 && under
			tag := "under_weak"
			if empty {
				tag = "empty"
			} else if strong {
				tag = "under_strong"
			} else if hotTurn || anyFlow {
				tag = "under_hot_turn"
			}
			delta := v10MaxSteps * step
			if gap := target - price; gap < delta {
				delta = gap
			}
			if empty && in.Sales == 0 && in.Buys == 0 {
				if step < delta {
					delta = step
				}
			} else if tag == "under_weak" {
				if step < delta {
					delta = step
				}
			}
			newP := price + delta
			if newP > target {
				newP = target
			}
			if newP > mid {
				newP = mid
			}
			if newP > price {
				out.Action = "corridor_price_up_v10"
				out.NewPrice = newP
				out.Reason = tag
				return out
			}
			out.Action = "corridor_hold_v10_book_mid"
			out.Reason = "at_mid"
			return out
		}
		out.Action = "corridor_hold_v10_no_up"
		out.Reason = "no_understock"
		return out
	}

	out.Action = "corridor_hold_v10"
	out.Reason = "balanced"
	_ = lo
	return out
}

func adjustPriceV10(
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
	band := stockBandFor(item, cfg)
	targetLo, targetHi, _, _, _ := stockTargets(share, band)
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	treasuryCashBlocksUp := blockUpTreasuryCashShortLocked(cfg, totalHeld)
	if treasuryCashBlocksUp {
		blockUp = true
	}

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	multiFloor, multiFloorQ, multiFloorOK := stockNormBookCatchupFloor(cfg, bookSince)
	bookMid, bookMidQ, bookMidOK := stockNormBookMid(cfg, now.Add(-ahBook2Window))
	mutex.Lock()

	dec := v10Decide(v10Input{
		Held:         totalHeld,
		Sales:        sales,
		Buys:         buys,
		TrySells:     trySells,
		Price:        priceBefore,
		Step:         step,
		Share:        share,
		Nacenka:      nacenka,
		MultiFloor:   multiFloor,
		MultiFloorOK: multiFloorOK,
		BookMid:      bookMid,
		BookMidOK:    bookMidOK,
		BlockUp:      blockUp,
		BlockDown:    blockDown,
		PriceFloor:   priceFloor,
		Band:         band,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf("v10 reason=%s held=%d(onAH=%d inv=%d) lo=%d hi=%d sales=%d buys=%d try=%d floor(p%.0f)=%d mid(p%.0f)=%d load=%.2f",
			dec.Reason, totalHeld, onAH, invCount, targetLo, targetHi, sales, buys, trySells,
			multiFloorQ*100, multiFloor, bookMidQ*100, bookMid, stockLoad),
	}
	if treasuryCashBlocksUp {
		notes = append(notes, "treasury_empty + held>0 → ↑ gated")
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
	log.Printf("[V10] %s: %s reason=%s | цена %d→%d | held %d/share %d | sales=%d buys=%d | %s",
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
		BotsCategory:   botsForGoTypeLocked(cfg.Type),
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     state.CorridorUpStreak,
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
		Cooldown:      state.CorridorUpCooldown,
		GoodStreak:    state.CorridorUpStreak,
	}
}
