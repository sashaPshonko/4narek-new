package main

import "testing"

func TestPostGrantCBlockDownMildLive(t *testing.T) {
	// sales≥2 ∧ excess==+1 → block
	if !postGrantCBlockDown(6, 5, 2, 0, 0) {
		t.Fatal("mild+live must block")
	}
	if !postGrantCBlockDown(6, 5, 5, 3, 0) {
		t.Fatal("mild+live sales=5 must block")
	}
	// excess≥2 → do not block (gray / current logic)
	if postGrantCBlockDown(7, 5, 3, 0, 0) {
		t.Fatal("sales≥2 excess+2 must PASS")
	}
	// sales==1 → do not block
	if postGrantCBlockDown(6, 5, 1, 0, 0) {
		t.Fatal("sales=1 must PASS")
	}
}

func TestPostGrantCBlockDownZeroOneMild(t *testing.T) {
	if !postGrantCBlockDown(6, 5, 0, 0, 1) {
		t.Fatal("zero1 mild must block")
	}
	// buys>0 → not this HOLD filter
	if postGrantCBlockDown(6, 5, 0, 1, 1) {
		t.Fatal("zero1 with buys must not use mild HOLD filter")
	}
	// streak≥2 → not block (candidate path)
	if postGrantCBlockDown(6, 5, 0, 0, 2) {
		t.Fatal("streak≥2 must not block via C HOLD")
	}
	// depth≥2 → not this mild filter
	if postGrantCBlockDown(7, 5, 0, 0, 1) {
		t.Fatal("zero1 depth+2 must PASS C HOLD filter")
	}
}

func TestPostGrantCDownCandidate(t *testing.T) {
	if !postGrantCDownCandidate(8, 5, 0, 0, 2) {
		t.Fatal("streak≥2")
	}
	if !postGrantCDownCandidate(7, 5, 0, 2, 1) {
		t.Fatal("buys>0 and depth≥2")
	}
	if !postGrantCDownCandidate(6, 5, 0, 1, 1) {
		t.Fatal("buys>0 and streak≥1")
	}
	if !postGrantCDownCandidate(9, 5, 0, 0, 1) {
		t.Fatal("excess≥3")
	}
	if postGrantCDownCandidate(6, 5, 0, 0, 1) {
		t.Fatal("zero1 mild is HOLD not candidate")
	}
	if postGrantCDownCandidate(6, 5, 2, 0, 0) {
		t.Fatal("sales>0 not candidate")
	}
}

func TestZeroSalesExcessStreakNow(t *testing.T) {
	if zeroSalesExcessStreakNow(0, 6, 5, 0) != 1 {
		t.Fatal("first zero")
	}
	if zeroSalesExcessStreakNow(0, 6, 5, 2) != 3 {
		t.Fatal("continue streak")
	}
	if zeroSalesExcessStreakNow(1, 6, 5, 2) != 0 {
		t.Fatal("sales>0 resets")
	}
	if zeroSalesExcessStreakNow(0, 5, 5, 2) != 0 {
		t.Fatal("no grant → 0")
	}
}

func TestDoiCoverHoldIntensityUnchanged(t *testing.T) {
	// doi_cover still maps DOI<3 to hold (not removed by C)
	if doiCoverIntensity(3, 1) != "over" {
		t.Fatal("DOI=3 over")
	}
	if doiCoverIntensity(2, 1) != "hold" {
		t.Fatal("DOI<3 hold → doi_cover path")
	}
	if doiCoverIntensity(10, 1) != "hold" {
		t.Fatal("DOI≥10 hold")
	}
}

func TestCapitalPolicyV8ad(t *testing.T) {
	if capitalPolicy != "stock_corridor_v8af" {
		t.Fatalf("policy=%s want stock_corridor_v8af", capitalPolicy)
	}
}
