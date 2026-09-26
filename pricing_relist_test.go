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

func TestRelistFairUnreachableBlocksUp(t *testing.T) {
	fair := relistFairStock(3)
	maxReach := 1 // другие id съели слоты
	if !(maxReach < fair) {
		t.Fatal("expected unreachable")
	}
}

