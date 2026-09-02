package main

import "testing"

func TestDemandMinSalesForUpThinFleet(t *testing.T) {
	if got := demandMinSalesForUp(2, false); got != 2 {
		t.Fatalf("day thin=%d want 2", got)
	}
	if got := demandMinSalesForUp(2, true); got != 3 {
		t.Fatalf("night thin=%d want 3", got)
	}
	if got := demandMinSalesForUp(3, false); got != corridorMinSalesForUp {
		t.Fatalf("day normal=%d want %d", got, corridorMinSalesForUp)
	}
}

func TestDemandStrongEnoughForUpWithMin(t *testing.T) {
	if !demandStrongEnoughForUp(2, 2, 2, false) {
		t.Fatal("sustained 2x2 day")
	}
	if demandStrongEnoughForUp(2, 1, 3, false) {
		t.Fatal("sales=2 < min 3")
	}
	if demandStrongEnoughForUp(2, 2, 3, true) {
		t.Fatal("night sales=2 < min 3")
	}
}

func TestRecoverPaidCap(t *testing.T) {
	step := 100_000
	paid := 2_500_000
	if recoverPaidCap(paid, step) != paid+2*step {
		t.Fatalf("cap=%d", recoverPaidCap(paid, step))
	}
	if recoverBlockedByPaidCap(paid, paid, step) {
		t.Fatal("at last paid should still allow +K probe")
	}
	if !recoverBlockedByPaidCap(paid+2*step, paid, step) {
		t.Fatal("at cap must block")
	}
	if !recoverBlockedByPaidCap(1_200_000, 0, step) {
		t.Fatal("no sells → block recover")
	}
	// dump: current 1.2M, last paid 2.5M — recover must climb
	if recoverBlockedByPaidCap(1_200_000, paid, step) {
		t.Fatal("after dump below paid should recover")
	}
}
