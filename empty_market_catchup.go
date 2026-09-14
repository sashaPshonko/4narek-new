package main

// empty_market_catchup (v8af): Explored SKU, пустой сток, но рынок объективно выше.
// Не «held=0 → UP». Нужны thick book + our/p10 < gap + streak ≥ N.
// +1 step only; stop near p10; up_cd как у sales-driven ↑.

const (
	emptyMarketCatchupAction = "corridor_price_up_empty_market_catchup"

	// our/p10 ниже этого — есть рыночный gap (не пустота сама по себе).
	emptyMarketCatchupGapRatio = 0.85
	// Догнали рынок → стоп cursor.
	emptyMarketCatchupStopRatio = 0.90
	// Сколько подряд циклов с thick+gap+empty, прежде чем первый ↑.
	emptyMarketCatchupArmCycles = 2
	// Бюджет шагов на один empty-run (потом ждём held/sales или near-market).
	emptyMarketCatchupMaxSteps = 16
)

// emptyMarketCatchupThickBook — та же выборка, что p10 (без ban-filter min-book).
func emptyMarketCatchupThickBook(p10OK bool, p10N, p10 int) bool {
	return p10OK && p10N >= ahBookMinLotsInWindow && p10 > 0
}

// emptyMarketCatchupEvidence — B: пусто ИЗ-ЗА заниженной цены vs толстая книга.
// A (просто пусто / тонкая книга / уже у рынка) → false.
func emptyMarketCatchupEvidence(
	explored bool,
	held, sales, buys int,
	our, p10, p10N int,
	p10OK bool,
) bool {
	if !explored {
		return false
	}
	if held != 0 || sales != 0 || buys != 0 {
		return false
	}
	if !emptyMarketCatchupThickBook(p10OK, p10N, p10) {
		return false
	}
	if our <= 0 || p10 <= 0 {
		return false
	}
	return float64(our)/float64(p10) < emptyMarketCatchupGapRatio
}

func emptyMarketCatchupNearMarket(our, p10 int) bool {
	if our <= 0 || p10 <= 0 {
		return false
	}
	return float64(our)/float64(p10) >= emptyMarketCatchupStopRatio
}

func canEmptyMarketCatchupUp(
	explored bool,
	held, sales, buys int,
	gapStreak, climbSteps int,
	priceBefore, step, p10, p10N int,
	p10OK bool,
	blockUp bool,
	upCooldown, upStreak, marketDownCD int,
) bool {
	if !emptyMarketCatchupEvidence(explored, held, sales, buys, priceBefore, p10, p10N, p10OK) {
		return false
	}
	if gapStreak < emptyMarketCatchupArmCycles {
		return false
	}
	if climbSteps >= emptyMarketCatchupMaxSteps {
		return false
	}
	if emptyMarketCatchupNearMarket(priceBefore, p10) {
		return false
	}
	if blockUp || step <= 0 {
		return false
	}
	if upCooldown != 0 || upStreak >= corridorMaxUpStreak {
		return false
	}
	if marketDownCD > 0 {
		return false
	}
	// Cap: не выше raw p10 (без jump).
	if priceBefore+step > p10 {
		return false
	}
	return true
}
