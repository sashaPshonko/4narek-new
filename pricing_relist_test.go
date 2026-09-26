package main

import "testing"

func TestRelistWeakNormIs5(t *testing.T) {
	if relistSalesNorm != 5 {
		t.Fatalf("relistSalesNorm=%d want 5", relistSalesNorm)
	}
}

func TestRelistFairStock(t *testing.T) {
	if relistFairStock(0) != 1 || relistFairStock(3) != 3 {
		t.Fatalf("fair 0→1, 3→3")
	}
}

func TestRelistDynamicStockGates(t *testing.T) {
	fair := relistFairStock(3)
	sales := 4
	weak := sales < relistSalesNorm
	if !weak {
		t.Fatal("sales 4 should be weak vs norm 5")
	}
	if !(3 >= fair) {
		t.Fatal("onAH=3 should be high vs fair=3")
	}
	if !(1 < fair) {
		t.Fatal("onAH=1 should be low vs fair=3")
	}
}
