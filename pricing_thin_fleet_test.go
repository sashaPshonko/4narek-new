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

func TestRecoverMaxStepsFor(t *testing.T) {
	if recoverMaxStepsFor(2) != corridorRecoverMaxStepsThin {
		t.Fatalf("thin=%d", recoverMaxStepsFor(2))
	}
	if recoverMaxStepsFor(3) != corridorRecoverMaxSteps {
		t.Fatalf("normal=%d", recoverMaxStepsFor(3))
	}
}
