package main

import "testing"

func TestColdStartActiveGates(t *testing.T) {
	if coldStartActive(true, 0, 0, 0, 0) {
		t.Fatal("explored must block")
	}
	if coldStartActive(false, 0, 1, 0, 0) {
		t.Fatal("held>0 must block")
	}
	if coldStartActive(false, 0, 0, 1, 0) {
		t.Fatal("sales>0 must block")
	}
	if coldStartActive(false, 0, 0, 0, 1) {
		t.Fatal("buys>0 must block")
	}
	if coldStartActive(false, coldStartMaxCycles, 0, 0, 0) {
		t.Fatal("max cycles must block")
	}
	if !coldStartActive(false, 0, 0, 0, 0) {
		t.Fatal("fresh !explored s0b0 must allow")
	}
}

func TestColdStartGapAndNear(t *testing.T) {
	p10 := 2_000_000
	if !coldStartGapOK(900_000, p10) {
		t.Fatal("our/p10 < 0.50 must gap OK")
	}
	if coldStartGapOK(1_000_000, p10) {
		t.Fatal("our/p10 == 0.50 must not gap OK")
	}
	if coldStartGapOK(1_200_000, p10) {
		t.Fatal("our/p10 > 0.50 must not gap OK")
	}
	if !coldStartNearMarket(1_800_000, p10, true) {
		t.Fatal("our/p10 >= 0.90 thick must near")
	}
	if coldStartNearMarket(1_800_000, p10, false) {
		t.Fatal("thin book must not near-abort")
	}
	if coldStartNearMarket(1_700_000, p10, true) {
		t.Fatal("our/p10 < 0.90 must not near")
	}
}

func TestCanColdStartUpHappyAndGuards(t *testing.T) {
	step, p10, bookN := 100_000, 2_000_000, ahBookMinLotsInWindow
	our := 900_000 // ratio 0.45
	if !canColdStartUp(false, 0, 0, 0, 0, our, step, p10, bookN, true, true, false, 0, 0, 0) {
		t.Fatal("happy path must allow +1")
	}
	// Explored SKU (legacy empty_inventory replacement): no blind UP.
	if canColdStartUp(true, 0, 0, 0, 0, our, step, p10, bookN, true, true, false, 0, 0, 0) {
		t.Fatal("explored must not cold-start")
	}
	// Gap not deep enough.
	if canColdStartUp(false, 0, 0, 0, 0, 1_100_000, step, p10, bookN, true, true, false, 0, 0, 0) {
		t.Fatal("shallow gap must block")
	}
	// Thin book.
	if canColdStartUp(false, 0, 0, 0, 0, our, step, p10, 10, true, true, false, 0, 0, 0) {
		t.Fatal("thin book must block")
	}
	// Cap: next step above p10.
	if canColdStartUp(false, 0, 0, 0, 0, p10, step, p10, bookN, true, true, false, 0, 0, 0) {
		t.Fatal("price+step > p10 must block")
	}
	// Cooldown / streak / marketDown.
	if canColdStartUp(false, 0, 0, 0, 0, our, step, p10, bookN, true, true, false, 1, 0, 0) {
		t.Fatal("up cooldown must block")
	}
	if canColdStartUp(false, 0, 0, 0, 0, our, step, p10, bookN, true, true, false, 0, corridorMaxUpStreak, 0) {
		t.Fatal("up streak must block")
	}
	if canColdStartUp(false, 0, 0, 0, 0, our, step, p10, bookN, true, true, false, 0, 0, 1) {
		t.Fatal("market down cd must block")
	}
}

func TestColdStartArmsCooldown(t *testing.T) {
	if !corridorUpArmCooldown(coldStartAction) {
		t.Fatal("cold_start must arm CorridorUpCooldown")
	}
}

func TestMarkAndResetExploration(t *testing.T) {
	st := ItemAdjustState{}
	markPriceExploredLocked(&st, "sell")
	if !st.PriceExplored {
		t.Fatal("mark must set explored")
	}
	markPriceExploredLocked(&st, "sell") // sticky no-op
	resetPriceExplorationLocked(&st, "manual_set", 123)
	if st.PriceExplored || st.PriceExplorationCycles != 0 {
		t.Fatal("reset must clear exploration")
	}
	if st.PriceOriginKind != "manual_set" || st.PriceOriginPrice != 123 {
		t.Fatalf("origin=%s/%d", st.PriceOriginKind, st.PriceOriginPrice)
	}
}

func TestCapitalPolicyV8ae(t *testing.T) {
	// superseded by v8af; keep name so old test files still compile if imported — redirect.
	if capitalPolicy != capitalPolicyV4 && capitalPolicy != capitalPolicyV9 && capitalPolicy != capitalPolicyV8af {
		t.Fatalf("policy=%s want stock_corridor_v4|v9|v8af", capitalPolicy)
	}
}
