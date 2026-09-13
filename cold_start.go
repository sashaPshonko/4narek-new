package main

import (
	"log"
)

// Cold-start price discovery (v8ae): temporary +1 UP while sell-price is unknown.
// Not "sales==0 → UP". Explored SKUs never enter this path via empty silence alone.

const (
	coldStartAction = "corridor_price_up_cold_start"

	// Config defaults (tunable without new heuristics elsewhere).
	coldStartGapRatio  = 0.50 // UP only if our/p10 < this
	coldStartStopRatio = 0.90 // abort exploration when our/p10 >= this (thick book)
	coldStartMaxCycles = 24   // budget of cold-start cycles (~4h at 10m)

	// Backfill: lifetime evidence that SKU is already past discovery.
	coldStartBackfillMinCycles = 12
)

// coldStartActive — разведка ещё не закрыта и сейчас пустой цикл без оборота.
func coldStartActive(explored bool, explorationCycles, held, sales, buys int) bool {
	if explored {
		return false
	}
	if held != 0 || sales != 0 || buys != 0 {
		return false
	}
	if explorationCycles >= coldStartMaxCycles {
		return false
	}
	return true
}

// coldStartThickBook — тот же порог, что ahBookMinLotsInWindow (uuid≥40 + p10).
func coldStartThickBook(bookOK, p10OK bool, bookN, p10 int) bool {
	return emptyInventoryBookTrusted(bookOK, p10OK, bookN, p10)
}

// coldStartGapOK — our существенно ниже raw p10.
func coldStartGapOK(our, p10 int) bool {
	if our <= 0 || p10 <= 0 {
		return false
	}
	return float64(our)/float64(p10) < coldStartGapRatio
}

// coldStartNearMarket — догнали рынок → abort без sell-доказательства.
func coldStartNearMarket(our, p10 int, thick bool) bool {
	if !thick || our <= 0 || p10 <= 0 {
		return false
	}
	return float64(our)/float64(p10) >= coldStartStopRatio
}

// canColdStartUp — +1 step only; thick book + deep gap + existing up guards.
func canColdStartUp(
	explored bool,
	explorationCycles, held, sales, buys int,
	priceBefore, step, p10, bookN int,
	bookOK, p10OK bool,
	blockUp bool,
	upCooldown, upStreak, marketDownCD int,
) bool {
	if !coldStartActive(explored, explorationCycles, held, sales, buys) {
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
	if !coldStartThickBook(bookOK, p10OK, bookN, p10) {
		return false
	}
	if !coldStartGapOK(priceBefore, p10) {
		return false
	}
	// Cap: не выше raw p10 (без jump).
	if priceBefore+step > p10 {
		return false
	}
	return true
}

// markPriceExploredLocked — sticky; caller holds mutex.
func markPriceExploredLocked(state *ItemAdjustState, why string) {
	if state == nil || state.PriceExplored {
		return
	}
	state.PriceExplored = true
	log.Printf("[COLD_START] explored=true reason=%s cycles=%d origin=%s",
		why, state.PriceExplorationCycles, state.PriceOriginKind)
}

// resetPriceExplorationLocked — новая разведка после manual_set / явного re-seed.
func resetPriceExplorationLocked(state *ItemAdjustState, originKind string, originPrice int) {
	if state == nil {
		return
	}
	state.PriceExplored = false
	state.PriceExplorationCycles = 0
	state.PriceOriginKind = originKind
	state.PriceOriginPrice = originPrice
	log.Printf("[COLD_START] reset exploration origin=%s price=%d", originKind, originPrice)
}

// backfillPriceExplorationFromDB — one-shot after mlDB ready: старые SKU с sells/циклами → Explored.
func backfillPriceExplorationFromDB() {
	if mlDB == nil {
		return
	}
	type hit struct {
		item   string
		sells  int
		cycles int
	}
	var hits []hit
	mlDBMu.Lock()
	for item := range itemsConfig {
		var sells, cycles int
		_ = mlDB.QueryRow(
			`SELECT COUNT(*) FROM trade_events WHERE item_id=? AND event_type='sell'`, item,
		).Scan(&sells)
		_ = mlDB.QueryRow(
			`SELECT COUNT(*) FROM capital_cycles WHERE item_id=?`, item,
		).Scan(&cycles)
		if sells > 0 || cycles >= coldStartBackfillMinCycles {
			hits = append(hits, hit{item: item, sells: sells, cycles: cycles})
		}
	}
	mlDBMu.Unlock()

	mutex.Lock()
	defer mutex.Unlock()
	if data.AdjustState == nil {
		data.AdjustState = make(map[string]ItemAdjustState)
	}
	marked := 0
	for _, h := range hits {
		st := data.AdjustState[h.item]
		if st.PriceExplored {
			continue
		}
		st.PriceExplored = true
		if st.PriceOriginKind == "" {
			st.PriceOriginKind = "backfill"
		}
		data.AdjustState[h.item] = st
		if dailyData.AdjustState != nil {
			dailyData.AdjustState[h.item] = st
		}
		marked++
	}
	log.Printf("[COLD_START] backfill: marked Explored on %d SKUs", marked)
}
