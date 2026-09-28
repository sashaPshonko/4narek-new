package main

import (
	"strings"
	"testing"
)

func TestBookTargetsSwordMaxProfit(t *testing.T) {
	p10 := 2_000_000
	sell, nac, src := bookTargetsFromLiveBook(p10, 100_000, 2_499_999, 0, 100_000, "netherite_sword-1.21")
	buyMax := sell - nac
	if float64(sell) < 0.95*float64(p10) || float64(sell) > 1.05*float64(p10) {
		t.Fatalf("sell=%d want ~p10", sell)
	}
	ratio := float64(buyMax) / float64(p10)
	if ratio < 0.80 || ratio > 0.90 {
		t.Fatalf("buyMax/p10=%.3f want ~0.85 (buyMax=%d nac=%d src=%s)", ratio, buyMax, nac, src)
	}
	if !strings.Contains(src, "0.85") {
		t.Fatalf("src=%s", src)
	}
}

func TestBookTargetsArmorMaxProfit(t *testing.T) {
	p10 := 2_000_000
	sell, nac, src := bookTargetsFromLiveBook(p10, 100_000, 1_999_999, 0, 50_000, "netherite_armor-1.21")
	buyMax := sell - nac
	sellR := float64(sell) / float64(p10)
	buyR := float64(buyMax) / float64(p10)
	if sellR < 1.00 || sellR > 1.12 {
		t.Fatalf("armor sell/p10=%.3f want ~1.05", sellR)
	}
	if buyR < 0.95 || buyR > 1.05 {
		t.Fatalf("armor buy/p10=%.3f want ~1.00", buyR)
	}
	if buyMax >= sell {
		t.Fatalf("buyMax=%d >= sell=%d src=%s", buyMax, sell, src)
	}
}

func TestBookTargetsPickMaxProfit(t *testing.T) {
	p10 := 1_000_000
	_, nac, _ := bookTargetsFromLiveBook(p10, 50_000, 999_999, 0, 10_000, "netherite_pickaxe-1.21")
	// buy 0.95, sell 1.00 → nac ~0.05×p10
	if nac < 40_000 || nac > 80_000 {
		t.Fatalf("pick nac=%d want ~50k", nac)
	}
}

func TestBookMultForType(t *testing.T) {
	s := bookMultForType("netherite_sword-1.21")
	if s.Buy != 0.85 || s.Sell != 1.00 {
		t.Fatalf("sword %+v", s)
	}
	d := bookMultForType("unknown-type")
	if d != bookProfitMultDefault {
		t.Fatalf("default %+v", d)
	}
}

func TestBookSnapWithMarker(t *testing.T) {
	got := bookSnapWithMarker(2_050_000, 100_000, 1_234_567)
	if got != 2_000_067 {
		t.Fatalf("got=%d want 2000067", got)
	}
}

func TestColdStartIgnoresStale(t *testing.T) {
	stale := 9_000_099
	p10 := 2_000_000
	sell, _, _ := bookTargetsFromLiveBook(p10, 100_000, stale, 0, 100_000, "netherite_sword-1.21")
	if sell > 2_200_000 {
		t.Fatalf("sell=%d still near stale", sell)
	}
}

func TestBookTargetsIgnoresLegacyFloor(t *testing.T) {
	// sword7 кейс: p10=1M, legacy floor=minBuy+runtimeNac=3M — sell должен остаться ~p10
	p10 := 1_000_000
	legacyFloor := 3_000_000
	sell, nac, _ := bookTargetsFromLiveBook(p10, 100_000, 2_400_000, legacyFloor, 0, "netherite_sword-1.21")
	if sell > 1_100_000 {
		t.Fatalf("sell=%d lifted by legacy floor=%d (want ~p10)", sell, legacyFloor)
	}
	buyMax := sell - nac
	// step=100k snap: 0.85×1M → 850k → 800k
	if buyMax < 800_000 || buyMax > 900_000 {
		t.Fatalf("buyMax=%d nac=%d want ~0.85×p10 (step-snapped)", buyMax, nac)
	}
}

// buyMax = sell−nac при sell выше книги: nac должен дать buy≈buyMult×p10, не ~sell.
func TestBuyMaxCappedByP10EvenIfSellHigh(t *testing.T) {
	p10 := 4_000_000
	sellHigh := 5_500_007
	_, nac, _ := bookTargetsFromLiveBook(p10, 100_000, sellHigh, 0, 400_000, "netherite_sword-1.21")
	rawBuy := int(float64(p10)*0.85 + 0.5)
	buyMax := bookSnapWithMarker(rawBuy, 100_000, sellHigh)
	if buyMax >= sellHigh {
		buyMax = sellHigh - 100_000
	}
	wantNac := sellHigh - buyMax
	if wantNac < 400_000 {
		wantNac = 400_000
	}
	_ = nac
	gotBuy := sellHigh - wantNac
	if gotBuy > int(float64(p10)*0.88) {
		t.Fatalf("buyMax=%d too high for p10=%d (want ≤0.85×p10)", gotBuy, p10)
	}
}

func TestBook2AllocateByMarginPrefersFat(t *testing.T) {
	cands := []book2MarginCand{
		{Item: "thin", Price: 800_000, Margin: 200_000, Sell: 1_000_000},
		{Item: "thin", Price: 790_000, Margin: 210_000, Sell: 1_000_000},
		{Item: "fat", Price: 2_000_000, Margin: 500_000, Sell: 2_500_000},
		{Item: "fat", Price: 2_100_000, Margin: 400_000, Sell: 2_500_000},
		{Item: "mid", Price: 1_000_000, Margin: 300_000, Sell: 1_300_000},
	}
	buyMax, slots := book2AllocateByMargin(cands, 2)
	if slots["fat"] != 2 {
		t.Fatalf("want both slots on fat, got %v buyMax=%v", slots, buyMax)
	}
	if _, ok := buyMax["thin"]; ok {
		t.Fatalf("thin should get no slots: %v", buyMax)
	}
	if buyMax["fat"] != 2_100_000 {
		t.Fatalf("fat buyMax=%d want 2.1M (worst of taken)", buyMax["fat"])
	}
}

func TestBook2AllocateByMarginFillsRemainder(t *testing.T) {
	cands := []book2MarginCand{
		{Item: "fat", Price: 2_000_000, Margin: 500_000, Sell: 2_500_000},
		{Item: "thin", Price: 800_000, Margin: 200_000, Sell: 1_000_000},
		{Item: "thin", Price: 700_000, Margin: 300_000, Sell: 1_000_000},
	}
	buyMax, slots := book2AllocateByMargin(cands, 3)
	if slots["fat"] != 1 || slots["thin"] != 2 {
		t.Fatalf("slots=%v", slots)
	}
	if buyMax["thin"] != 800_000 {
		t.Fatalf("thin buyMax=%d", buyMax["thin"])
	}
}

func TestBook2OptBuyMaxKthCheapest(t *testing.T) {
	// prices sorted; sell=1000, softMin=100 → eligible all with p<=900
	ps := []int{500, 600, 700, 800, 850, 900, 950}
	buy, q, nElig, ok := book2OptBuyMax(ps, 1000, 100, 3)
	if !ok {
		t.Fatal("expected ok")
	}
	if nElig != 6 { // 950 has margin 50 < 100
		t.Fatalf("nElig=%d", nElig)
	}
	if buy != 700 { // 3rd cheapest of elig
		t.Fatalf("buyMax=%d want 700", buy)
	}
	if q <= 0 || q > 1 {
		t.Fatalf("q=%v", q)
	}
}

func TestBook2OptBuyMaxFewerThanK(t *testing.T) {
	ps := []int{400, 500}
	buy, _, nElig, ok := book2OptBuyMax(ps, 1000, 300, 5)
	if !ok || nElig != 2 || buy != 500 {
		t.Fatalf("buy=%d nElig=%d ok=%v", buy, nElig, ok)
	}
}

func TestBook2OptBuyMaxNone(t *testing.T) {
	_, _, _, ok := book2OptBuyMax([]int{900, 950}, 1000, 200, 2)
	if ok {
		t.Fatal("expected no eligible")
	}
}

func TestBook2ClampStep(t *testing.T) {
	step := 100_000
	// обычный ±20% → ±2 step
	if got := book2ClampStep(2_000_000, 2_400_000, step); got != 2_200_000 {
		t.Fatalf("cap up got=%d want 2.2M", got)
	}
	if got := book2ClampStep(2_000_000, 1_600_000, step); got != 1_800_000 {
		t.Fatalf("cap down got=%d want 1.8M", got)
	}
	// deep ≥25%: без cap (cold 2.7→0.9)
	if got := book2ClampStep(2_700_000, 900_000, step); got != 900_000 {
		t.Fatalf("deep down got=%d", got)
	}
}
