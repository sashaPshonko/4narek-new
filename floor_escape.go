package main

import "fmt"

// Floor escape (v8v): after DOWN→floor, idle-empty (or post-dump streak) must climb.
// Does NOT revive recover/demand/paid. One +1 (or trusted jump) per cycle max.

const (
	floorEscapeNearFloorStepMult = 1.5 // price <= floor + 1.5×step
	floorEscapeNearFloorPct      = 20  // or floor + floor/20 (5%)
	floorEscapeDeepAHRatio       = 0.80
	floorEscapeDeepAHGapSteps    = 4
	floorEscapeDownStreakMin     = 3
	floorEscapeCooldownCycles    = 1

	// Soft trust for very deep pits (our/trusted_min ≤ 0.50).
	floorEscapeDeepPitRatio          = 0.50
	floorEscapeDeepPitMinSellers     = 5
	floorEscapeDeepPitMinNear        = 2
)

const (
	floorEscapeActionEmpty       = "corridor_price_up_floor_escape_empty"
	floorEscapeActionDeepAH      = "corridor_price_up_floor_escape_deep_ah"
	floorEscapeActionDownStreak  = "corridor_price_up_floor_escape_down_streak"
	floorEscapeActionTrustedJump = "corridor_price_up_floor_escape_trusted_jump"
	floorEscapeActionEmptyIdle   = "corridor_price_up_empty_idle" // no book / not near-floor: still recover
)

// isEmptyIdle — нет товара и нет оборота. sales=0 здесь ≠ bearish.
func isEmptyIdle(held, sales, buys int) bool {
	return held == 0 && sales == 0 && buys == 0
}

// priceNearFloor — цена у экономического пола (не «дешево vs AH»).
func priceNearFloor(price, floor, step int) bool {
	if price <= 0 || floor <= 0 {
		return false
	}
	if step <= 0 {
		step = 1
	}
	lim := floor + int(floorEscapeNearFloorStepMult*float64(step))
	if pct := floor + floor/floorEscapeNearFloorPct; pct > lim {
		lim = pct
	}
	return price <= lim
}

// priceDeepVsAH — наша цена сильно ниже AH min (ratio или gap в steps).
func priceDeepVsAH(price, ahMin, step int) bool {
	if price <= 0 || ahMin <= 0 || step <= 0 {
		return false
	}
	if float64(price)/float64(ahMin) <= floorEscapeDeepAHRatio {
		return true
	}
	return float64(ahMin-price)/float64(step) >= float64(floorEscapeDeepAHGapSteps)
}

type floorEscapeEval struct {
	WouldFire   bool
	Action      string
	WouldPrice  int
	Reason      string // floor_escape_empty | floor_escape_deep_ah | ...
	SkipReason  string
	NearFloor   bool
	DeepAH      bool
	PriceFloor  int
	AHMin       int
	DownStreak  int
	FloorRatio  float64 // price/floor
	AHRatio     float64 // price/ah_min (0 if no AH)
}

// evalFloorEscape — pure decision for +1 climb or defer-to-jump hint.
// Jump itself is decided by evalTrustedMinDiscovery (with deep-pit soft gate);
// when jumpWouldFire, caller should apply jump with floor_escape_trusted_jump action.
func evalFloorEscape(
	price, step, floor, ahMin, held, sales, buys, downStreak, escapeCD, maxPrice int,
	manualLock, alreadyMoved, jumpWouldFire bool, jumpPrice int,
) floorEscapeEval {
	out := floorEscapeEval{
		PriceFloor: floor,
		AHMin:      ahMin,
		DownStreak: downStreak,
	}
	if price > 0 && floor > 0 {
		out.FloorRatio = float64(price) / float64(floor)
	}
	if price > 0 && ahMin > 0 {
		out.AHRatio = float64(price) / float64(ahMin)
	}
	out.NearFloor = priceNearFloor(price, floor, step)
	out.DeepAH = priceDeepVsAH(price, ahMin, step)

	if alreadyMoved {
		out.SkipReason = "already_moved"
		return out
	}
	if manualLock {
		out.SkipReason = "manual_lock"
		return out
	}
	if step <= 0 || price <= 0 {
		out.SkipReason = "bad_price_or_step"
		return out
	}
	if escapeCD > 0 {
		out.SkipReason = "escape_cooldown"
		return out
	}
	if buys > sales {
		out.SkipReason = "buys_gt_sales"
		return out
	}

	// Cap to AH only when that AH is trusted for discovery jump (jumpWouldFire).
	// Raw/thin TrustedMin must NOT block EMPTY_IDLE +1 via at_or_above_cap —
	// untrusted AH is still fine for DeepAH classification (display only).
	capPrice := func(tgt int) int {
		if jumpWouldFire && ahMin > 0 && tgt > ahMin {
			tgt = ahMin
		}
		if maxPrice > 0 && tgt > maxPrice {
			tgt = maxPrice
		}
		return tgt
	}

	// EMPTY_IDLE recovery (priority over hold_recover_stale / any empty HOLD):
	// trusted jump → jump; else +1 / cycle (AH absent or untrusted never blocks +1).
	if isEmptyIdle(held, sales, buys) {
		if jumpWouldFire && jumpPrice > price {
			tgt := jumpPrice
			if maxPrice > 0 && tgt > maxPrice {
				tgt = maxPrice
			}
			if tgt <= price {
				out.SkipReason = "at_or_above_max"
				return out
			}
			out.WouldFire = true
			out.WouldPrice = tgt
			out.Action = floorEscapeActionTrustedJump
			out.Reason = "floor_escape_trusted_jump"
			return out
		}
		tgt := capPrice(price + step)
		if tgt <= price {
			out.SkipReason = "at_or_above_cap"
			return out
		}
		out.WouldFire = true
		out.WouldPrice = tgt
		switch {
		case out.DeepAH:
			out.Action = floorEscapeActionDeepAH
			out.Reason = "floor_escape_deep_ah"
		case out.NearFloor:
			out.Action = floorEscapeActionEmpty
			out.Reason = "floor_escape_empty"
		default:
			// no book / thin unconfirmed / not near floor — still climb
			out.Action = floorEscapeActionEmptyIdle
			out.Reason = "empty_idle_step"
		}
		return out
	}

	// Post-dump bounce: near floor after ≥3 DOWNs (separate from idle-empty).
	if out.NearFloor && downStreak >= floorEscapeDownStreakMin {
		tgt := capPrice(price + step)
		if tgt <= price {
			out.SkipReason = "at_or_above_cap"
			return out
		}
		out.WouldFire = true
		out.WouldPrice = tgt
		out.Action = floorEscapeActionDownStreak
		out.Reason = "floor_escape_down_streak"
		return out
	}

	if sales >= 1 {
		out.SkipReason = "sales_on_floor_no_special"
		return out
	}
	out.SkipReason = "no_escape_state"
	return out
}

func floorEscapeNote(ev floorEscapeEval, held, sales, buys int, fill float64, sellers int) string {
	return fmt.Sprintf(
		"%s: held=%d sales=%d buys=%d fill=%.1f%% down_streak=%d floor=%d ah_min=%d sellers=%d price/floor=%.3f price/ah=%.3f → %d",
		ev.Reason, held, sales, buys, fill*100, ev.DownStreak, ev.PriceFloor, ev.AHMin, sellers,
		ev.FloorRatio, ev.AHRatio, ev.WouldPrice,
	)
}
