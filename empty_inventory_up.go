package main

// empty_inventory_up (v8ab): held=0 → exploration +1 step.
// sales/buys/paid/book/night не обязательны. AH book только как sell-cap (p10+nac), не диктует цену.

const (
	emptyInventoryAction = "corridor_price_up_empty_inventory"

	// Historical: чуть выше доказанного max sell.
	emptyInventoryPaidExploreSteps = 5
	// Нет book и нет paid — единственный soft safety.
	emptyInventorySafetyClimbSteps = 24
	// Book или paid есть — cumulative только аварийный.
	emptyInventoryEmergencyClimbSteps = 40
	// Счётчик ↑ за empty-run (не больше emergency).
	emptyInventoryMaxClimbSteps = 40
)

// emptyInventoryBookTrusted — та же «толстая» книга, что soft-↓ (≥40 uuid + p10).
func emptyInventoryBookTrusted(bookOK, p10OK bool, bookN, p10 int) bool {
	return bookOK && p10OK && bookN >= ahBookMinLotsInWindow && p10 > 0
}

// emptyInventoryEffectiveCap — min среди заданных caps; отсутствие book/paid не запрещает UP.
//
//	market:  p10+nacenka (sell-ориентир проекта)
//	paid:    paidMax+5×step
//	safety:  anchor+24×step только если нет book и нет paid
//	emergency: anchor+40×step если book или paid есть
func emptyInventoryEffectiveCap(
	step, nacenka, p10, bookN, paidMax, anchorPrice int,
	bookOK, p10OK bool,
) (cap int, reason string) {
	hasBook := emptyInventoryBookTrusted(bookOK, p10OK, bookN, p10)
	hasPaid := paidMax > 0
	caps := make([]int, 0, 3)
	reasons := make([]string, 0, 3)

	if hasBook {
		caps = append(caps, p10+nacenka)
		reasons = append(reasons, "market_p10+nac")
	}
	if hasPaid && step > 0 {
		caps = append(caps, paidMax+emptyInventoryPaidExploreSteps*step)
		reasons = append(reasons, "paid+5step")
	}
	if anchorPrice > 0 && step > 0 {
		if !hasBook && !hasPaid {
			caps = append(caps, anchorPrice+emptyInventorySafetyClimbSteps*step)
			reasons = append(reasons, "safety_24")
		} else {
			caps = append(caps, anchorPrice+emptyInventoryEmergencyClimbSteps*step)
			reasons = append(reasons, "emergency_40")
		}
	}
	if len(caps) == 0 {
		// Нет якоря ещё — не режем (caller выставит anchor при первом ↑).
		return int(^uint(0) >> 1), "uncapped"
	}
	cap = caps[0]
	reason = reasons[0]
	for i := 1; i < len(caps); i++ {
		if caps[i] < cap {
			cap = caps[i]
			reason = reasons[i]
		}
	}
	return cap, reason
}

// canEmptyInventoryUp — чистые условия ТЗ (без sales/buys/night/book обязательности).
func canEmptyInventoryUp(
	held int,
	priceBefore, step int,
	blockUp bool,
	upCooldown, upStreak, marketDownCD, climbSteps int,
	effectiveCap int,
) bool {
	if held != 0 || blockUp {
		return false
	}
	if upCooldown != 0 || upStreak >= corridorMaxUpStreak {
		return false
	}
	if marketDownCD > 0 {
		return false
	}
	if climbSteps >= emptyInventoryMaxClimbSteps {
		return false
	}
	if step <= 0 {
		return false
	}
	if priceBefore+step > effectiveCap {
		return false
	}
	return true
}
