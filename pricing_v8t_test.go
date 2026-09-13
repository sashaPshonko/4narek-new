package main

import "testing"

func TestV8aaInventoryUpFlags(t *testing.T) {
	// v8aa: inventory ↑ (мало стока + мало покупок) — ON; книга/empty_idle — OFF.
	if !corridorDemandUpEnabled {
		t.Fatal("v8aa: up_demand must be enabled")
	}
	if !corridorRecoverUpEnabled {
		t.Fatal("v8aa: up_recover must be enabled")
	}
	if !corridorPaidClimbEnabled {
		t.Fatal("v8aa: up_paid must be enabled")
	}
	if corridorSkimEnabled {
		t.Fatal("v8aa: up_skim stays disabled")
	}
	if ahBookPriceUpEnabled {
		t.Fatal("ah_book/empty_book ↑ must stay disabled")
	}
	if floorEscapePriceUpEnabled {
		t.Fatal("floor_escape/empty_idle ↑ must stay disabled")
	}
	if marketRecoveryLiveEnabled {
		t.Fatal("market_recovery ↑ must stay disabled")
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

func TestCapitalPolicyCurrent(t *testing.T) {
	if capitalPolicy != "stock_corridor_v8ab" {
		t.Fatalf("policy=%s want stock_corridor_v8ab", capitalPolicy)
	}
}
