package main

import (
	"strings"
	"testing"
)

func TestBookTargetsFromP10Fallback(t *testing.T) {
	step := 100_000
	priceBefore := 2_499_999
	p10 := 2_000_000
	sell, nac, src := bookTargetsFromP10(p10, step, priceBefore, 0, 100_000, nil)
	if sell < 1_900_000 || sell > 2_100_000 {
		t.Fatalf("sell=%d want near p10", sell)
	}
	buyMax := sell - nac
	ratioBuy := float64(buyMax) / float64(p10)
	// fallback 0.85 ± snap
	if ratioBuy < 0.80 || ratioBuy > 0.90 {
		t.Fatalf("buyMax/p10=%.3f want ~0.85 (sell=%d nac=%d buyMax=%d src=%s)", ratioBuy, sell, nac, buyMax, src)
	}
	if !strings.Contains(src, "fallback") {
		t.Fatalf("src=%s want fallback", src)
	}
}

func TestBookBuyMaxFromHistP90(t *testing.T) {
	p10 := 2_000_000
	// 10 buys clustered ~1.4M → p90 below floor 0.75 → clamp floor
	hist := make([]int, 0, 10)
	for i := 0; i < 10; i++ {
		hist = append(hist, 1_400_000+i*10_000)
	}
	buyMax, src := bookBuyMaxFromP10(p10, hist)
	lo := int(float64(p10)*bookBuyFloorMult + 0.5)
	if buyMax != lo {
		t.Fatalf("buyMax=%d want floor %d src=%s", buyMax, lo, src)
	}
	if !strings.Contains(src, "floor") {
		t.Fatalf("src=%s want +floor", src)
	}

	// expensive hist → ceil 0.88
	hist2 := make([]int, 0, 10)
	for i := 0; i < 10; i++ {
		hist2 = append(hist2, 1_900_000+i*10_000)
	}
	buyMax2, src2 := bookBuyMaxFromP10(p10, hist2)
	hi := int(float64(p10)*bookBuyCeilMult + 0.5)
	if buyMax2 != hi {
		t.Fatalf("buyMax=%d want ceil %d src=%s", buyMax2, hi, src2)
	}
}

func TestBookPercentileInt(t *testing.T) {
	ps := []int{100, 200, 300, 400, 500}
	if got := bookPercentileInt(ps, 0.5); got != 300 {
		t.Fatalf("p50=%d", got)
	}
	if got := bookPercentileInt(ps, 0.9); got < 400 || got > 500 {
		t.Fatalf("p90=%d", got)
	}
}

func TestBookSnapWithMarker(t *testing.T) {
	got := bookSnapWithMarker(2_050_000, 100_000, 1_234_567)
	if got != 2_000_067 {
		t.Fatalf("got=%d want 2000067", got)
	}
}

func TestBook2Constants(t *testing.T) {
	if bookSellMult != 1.0 {
		t.Fatalf("sell mult %.2f", bookSellMult)
	}
	if bookBuyFallbackMult != 0.85 || bookBuyFloorMult != 0.75 || bookBuyCeilMult != 0.88 {
		t.Fatalf("buy fb=%.2f floor=%.2f ceil=%.2f", bookBuyFallbackMult, bookBuyFloorMult, bookBuyCeilMult)
	}
	if bookBuyHistQ != 0.90 {
		t.Fatalf("hist q %.2f", bookBuyHistQ)
	}
}
