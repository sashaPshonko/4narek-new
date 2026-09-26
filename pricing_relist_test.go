package main

import "testing"

func TestRelistWeakNormIs5(t *testing.T) {
	if relistSalesNorm != 5 {
		t.Fatalf("relistSalesNorm=%d want 5", relistSalesNorm)
	}
}

func TestRelistDecideStuckDown(t *testing.T) {
	// Mirror core branching without full adjustPriceRelist mutex/book.
	onAH, sales := 2, 3 // weak
	weak := sales < relistSalesNorm
	if !weak || onAH < 1 {
		t.Fatal("setup")
	}
	// with ratio OK at market → would DOWN
	ratio := 1.0
	if ratio < relistDownUnderpriceMax {
		t.Fatal("should allow down")
	}
}

func TestRelistDecideEmptyUpGate(t *testing.T) {
	onAH, sales := 0, 2
	weak := sales < relistSalesNorm
	ratio := 0.85
	if !(weak && onAH == 0 && ratio < relistEmptyUpMaxRatio) {
		t.Fatal("should qualify for empty up")
	}
	ratio = 0.95
	if weak && onAH == 0 && ratio < relistEmptyUpMaxRatio {
		t.Fatal("should NOT up when not underpriced")
	}
}
