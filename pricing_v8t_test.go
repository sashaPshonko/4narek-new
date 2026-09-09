package main

import "testing"

func TestV8tInventoryUpFlags(t *testing.T) {
	if corridorDemandUpEnabled {
		t.Fatal("v8t: up_demand must be disabled")
	}
	if corridorRecoverUpEnabled {
		t.Fatal("v8t: up_recover must be disabled")
	}
	if corridorPaidClimbEnabled {
		t.Fatal("v8t: up_paid must be disabled")
	}
	if corridorSkimEnabled {
		t.Fatal("v8t: up_skim stays disabled")
	}
	if !trustedMinDiscoveryLiveEnabled {
		t.Fatal("v8t: trusted_AH_min live discovery must be on")
	}
}

func TestTrySellsBlockUpNotBlanketAtHighSales(t *testing.T) {
	// Audit: try≥5 & sales≥5 — UP лучше HOLD; не банить try≥5 универсально.
	if trySellsBlockUp(5, 5) {
		t.Fatal("try=5 sales=5 must NOT block UP")
	}
	if trySellsBlockUp(5, 10) {
		t.Fatal("try=10 sales=5 (=2×) must NOT block under v8t (need 3×)")
	}
	if !trySellsBlockUp(5, 15) {
		t.Fatal("try=15 sales=5 (≥3×) must block")
	}
	// Empty / no sales: still conversion gate.
	if !trySellsBlockUp(0, 5) {
		t.Fatal("try=5 sales=0 must block")
	}
	if !trySellsBlockUp(1, 5) {
		t.Fatal("try=5 sales=1 must block (5≥2×1)")
	}
	if trySellsBlockUp(1, 4) {
		t.Fatal("try=4 < minTries must not block")
	}
}

func TestCapitalPolicyV8t(t *testing.T) {
	if capitalPolicy != "stock_corridor_v8t" {
		t.Fatalf("policy=%s want stock_corridor_v8t", capitalPolicy)
	}
}
