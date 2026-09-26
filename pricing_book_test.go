package main

import "testing"

func TestBookTargetsFromP10(t *testing.T) {
	step := 100_000
	priceBefore := 2_499_999
	p10 := 2_000_000
	sell, nac := bookTargetsFromP10(p10, step, priceBefore, 0, 100_000)
	if sell < 1_900_000 || sell > 2_100_000 {
		t.Fatalf("sell=%d want near p10", sell)
	}
	buyMax := sell - nac
	ratioBuy := float64(buyMax) / float64(p10)
	if ratioBuy < 0.75 || ratioBuy > 0.85 {
		t.Fatalf("buyMax/p10=%.3f want ~0.80 (sell=%d nac=%d buyMax=%d)", ratioBuy, sell, nac, buyMax)
	}
	if nac < 100_000 {
		t.Fatalf("nac=%d", nac)
	}
}

func TestBookSnapWithMarker(t *testing.T) {
	got := bookSnapWithMarker(2_050_000, 100_000, 1_234_567)
	// step→2_000_000, marker 67 → 2_000_067
	if got != 2_000_067 {
		t.Fatalf("got=%d want 2000067", got)
	}
}

func TestBookThinBookConstants(t *testing.T) {
	if bookSellMult != 1.0 || bookBuyMult != 0.80 {
		t.Fatalf("mult sell=%.2f buy=%.2f", bookSellMult, bookBuyMult)
	}
}
