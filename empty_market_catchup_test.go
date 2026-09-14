package main

import "testing"

func TestEmptyMarketCatchupEvidence(t *testing.T) {
	p10, n := 3_000_000, ahBookMinLotsInWindow
	our := 2_000_000 // ratio ~0.67
	// A: simply empty without book
	if emptyMarketCatchupEvidence(true, 0, 0, 0, our, p10, 5, true) {
		t.Fatal("thin p10N must not evidence")
	}
	// A: empty but near market
	if emptyMarketCatchupEvidence(true, 0, 0, 0, 2_700_000, p10, n, true) {
		t.Fatal("near market must not evidence")
	}
	// held>0 / sales / buys / !explored
	if emptyMarketCatchupEvidence(true, 1, 0, 0, our, p10, n, true) {
		t.Fatal("held>0")
	}
	if emptyMarketCatchupEvidence(false, 0, 0, 0, our, p10, n, true) {
		t.Fatal("!explored is cold-start territory")
	}
	if emptyMarketCatchupEvidence(true, 0, 1, 0, our, p10, n, true) {
		t.Fatal("sales>0")
	}
	if emptyMarketCatchupEvidence(true, 0, 0, 1, our, p10, n, true) {
		t.Fatal("buys>0")
	}
	// B: underpriced empty thick on p10 sample (not ban-filter min-book)
	if !emptyMarketCatchupEvidence(true, 0, 0, 0, our, p10, n, true) {
		t.Fatal("p10OK+p10N thick + gap must evidence")
	}
	if emptyMarketCatchupEvidence(true, 0, 0, 0, our, p10, n, false) {
		t.Fatal("!p10OK must not evidence even if p10N large")
	}
	if !emptyMarketCatchupThickBook(true, n, p10) {
		t.Fatal("p10OK+p10N>=40")
	}
	if emptyMarketCatchupThickBook(false, n, p10) {
		t.Fatal("!p10OK")
	}
}

func TestCanEmptyMarketCatchupUp(t *testing.T) {
	step, p10, p10N := 100_000, 3_000_000, ahBookMinLotsInWindow
	our := 2_000_000
	if canEmptyMarketCatchupUp(true, 0, 0, 0, 1, 0, our, step, p10, p10N, true, false, 0, 0, 0) {
		t.Fatal("streak<arm must block")
	}
	if !canEmptyMarketCatchupUp(true, 0, 0, 0, 2, 0, our, step, p10, p10N, true, false, 0, 0, 0) {
		t.Fatal("happy catchup must allow")
	}
	if canEmptyMarketCatchupUp(true, 0, 0, 0, 2, emptyMarketCatchupMaxSteps, our, step, p10, p10N, true, false, 0, 0, 0) {
		t.Fatal("max climb must block")
	}
	if canEmptyMarketCatchupUp(true, 0, 0, 0, 2, 0, 2_700_000, step, p10, p10N, true, false, 0, 0, 0) {
		t.Fatal("near market must block")
	}
	if canEmptyMarketCatchupUp(true, 0, 0, 0, 2, 0, our, step, p10, p10N, true, false, 1, 0, 0) {
		t.Fatal("up cd must block")
	}
	// Cap p10
	if canEmptyMarketCatchupUp(true, 0, 0, 0, 2, 0, p10, step, p10, p10N, true, false, 0, 0, 0) {
		t.Fatal("price+step>p10 must block")
	}
}

func TestEmptyMarketCatchupArmsCooldown(t *testing.T) {
	if !corridorUpArmCooldown(emptyMarketCatchupAction) {
		t.Fatal("catchup must arm CorridorUpCooldown")
	}
}

func TestCapitalPolicyV8af(t *testing.T) {
	if capitalPolicy != capitalPolicyV9 && capitalPolicy != capitalPolicyV8af {
		t.Fatalf("policy=%s want stock_corridor_v8af", capitalPolicy)
	}
}
