package main

import (
	"strings"
	"testing"
)

func TestBookTargetsFromLiveBookFallback(t *testing.T) {
	step := 100_000
	priceBefore := 2_499_999
	p10 := 2_000_000
	sell, nac, src := bookTargetsFromLiveBook(p10, 0, step, priceBefore, 0, 100_000)
	if sell < 1_900_000 || sell > 2_100_000 {
		t.Fatalf("sell=%d want near p10", sell)
	}
	buyMax := sell - nac
	ratioBuy := float64(buyMax) / float64(p10)
	if ratioBuy < 0.80 || ratioBuy > 0.90 {
		t.Fatalf("buyMax/p10=%.3f want ~0.85 (sell=%d nac=%d buyMax=%d src=%s)", ratioBuy, sell, nac, buyMax, src)
	}
	if !strings.Contains(src, "fallback") {
		t.Fatalf("src=%s want fallback", src)
	}
}

func TestBookBuyMaxFromLiveP5(t *testing.T) {
	p10 := 2_000_000
	// p5 глубоко → floor 0.75
	buyMax, src := bookBuyMaxFromLiveBook(p10, 1_200_000)
	lo := int(float64(p10)*bookBuyFloorMult + 0.5)
	if buyMax != lo {
		t.Fatalf("buyMax=%d want floor %d src=%s", buyMax, lo, src)
	}
	if !strings.Contains(src, "floor") {
		t.Fatalf("src=%s want +floor", src)
	}

	// p5 почти у p10 → ceil 0.88
	buyMax2, src2 := bookBuyMaxFromLiveBook(p10, 1_950_000)
	hi := int(float64(p10)*bookBuyCeilMult + 0.5)
	if buyMax2 != hi {
		t.Fatalf("buyMax=%d want ceil %d src=%s", buyMax2, hi, src2)
	}

	// p5 в зоне → как есть
	buyMax3, src3 := bookBuyMaxFromLiveBook(p10, 1_700_000)
	if buyMax3 != 1_700_000 {
		t.Fatalf("buyMax=%d want 1700000 src=%s", buyMax3, src3)
	}
}

func TestBookSnapWithMarker(t *testing.T) {
	got := bookSnapWithMarker(2_050_000, 100_000, 1_234_567)
	if got != 2_000_067 {
		t.Fatalf("got=%d want 2000067", got)
	}
}

func TestBook2LiveConstants(t *testing.T) {
	if bookSellMult != 1.0 {
		t.Fatalf("sell mult %.2f", bookSellMult)
	}
	if bookBuyFallbackMult != 0.85 || bookBuyFloorMult != 0.75 || bookBuyCeilMult != 0.88 {
		t.Fatalf("buy fb=%.2f floor=%.2f ceil=%.2f", bookBuyFallbackMult, bookBuyFloorMult, bookBuyCeilMult)
	}
}

func TestAhBookPercentileSorted(t *testing.T) {
	ps := []int{100, 200, 300, 400, 500}
	if got := ahBookPercentileSorted(ps, 0.5); got != 300 {
		t.Fatalf("p50=%d", got)
	}
	if got := ahBookPercentileSorted(ps, 0.05); got < 100 || got > 200 {
		t.Fatalf("p5=%d", got)
	}
}

func TestColdStartSnapsAwayFromStale(t *testing.T) {
	// Неделя простоя: цена 5M, книга p10=2M → сразу к книге.
	stale := 5_000_099
	p10 := 2_000_000
	p5 := 1_700_000
	sell, nac, _ := bookTargetsFromLiveBook(p10, p5, 100_000, stale, 0, 100_000)
	if sell > 2_200_000 || sell < 1_800_000 {
		t.Fatalf("sell=%d want ~p10 after cold start", sell)
	}
	buyMax := sell - nac
	if buyMax != 1_700_000 && buyMax < int(0.75*float64(p10)) {
		t.Fatalf("buyMax=%d unexpected", buyMax)
	}
}
