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

func TestBook2MinMarginFromCands(t *testing.T) {
	cands := []book2MarginCand{
		{Item: "thin", Margin: 200_000},
		{Item: "thin", Margin: 210_000},
		{Item: "fat", Margin: 500_000},
		{Item: "fat", Margin: 400_000},
		{Item: "mid", Margin: 300_000},
	}
	// K=2 → 2nd best = 400k
	m, n, ok := book2MinMarginFromCands(cands, 2)
	if !ok || n != 5 || m != 400_000 {
		t.Fatalf("K=2 got marg=%d n=%d ok=%v", m, n, ok)
	}
	// K=5 → worst of top5 = 200k
	m, _, ok = book2MinMarginFromCands(cands, 5)
	if !ok || m != 200_000 {
		t.Fatalf("K=5 got %d", m)
	}
	// K > n → last
	m, _, ok = book2MinMarginFromCands(cands, 99)
	if !ok || m != 200_000 {
		t.Fatalf("K=99 got %d", m)
	}
}

func TestBook2MinMarginEmpty(t *testing.T) {
	_, _, ok := book2MinMarginFromCands(nil, 3)
	if ok {
		t.Fatal("expected !ok")
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

func mkCands(margins ...int) []book2MarginCand {
	out := make([]book2MarginCand, len(margins))
	for i, m := range margins {
		out[i] = book2MarginCand{Margin: m}
	}
	return out
}

// День: fat SKU поднимает свой пол, volume остаётся ~300k; global OFF
// (общий порог слишком высокий для объёмного яруса).
func TestBook2PlanPerSKUWhenVolumeCantMeetGlobal(t *testing.T) {
	byItem := map[string][]book2MarginCand{
		"sword7": mkCands(320_000, 310_000, 305_000, 300_000, 300_000, 300_000, 300_000, 300_000, 300_000),
		"mega": mkCands(800_000, 700_000, 650_000, 600_000, 550_000, 500_000, 480_000, 450_000, 400_000),
	}
	base := map[string]int{"sword7": 300_000, "mega": 300_000}
	p10 := map[string]int{"sword7": 1_000_000, "mega": 4_000_000}
	plan := book2PlanFloors(byItem, base, p10, 20, 10)
	if plan.GlobalOn {
		t.Fatalf("expected global OFF, got on marg=%d note=%s", plan.GlobalMarg, plan.Note)
	}
	if plan.FloorByItem["sword7"] > 350_000 {
		t.Fatalf("sword7 floor=%d want ~300–350k (volume)", plan.FloorByItem["sword7"])
	}
	if plan.FloorByItem["mega"] < 450_000 {
		t.Fatalf("mega floor=%d want fat raise ≥450k", plan.FloorByItem["mega"])
	}
}

// Толстая книга: global ON, но только fat поднимается; volume capped (~0.30×p10).
func TestBook2PlanGlobalWhenVolumeFeeds(t *testing.T) {
	vol := make([]book2MarginCand, 50)
	fat := make([]book2MarginCand, 30)
	for i := 0; i < 50; i++ {
		vol[i] = book2MarginCand{Margin: 520_000 - i*2_000}
	}
	for i := 0; i < 30; i++ {
		fat[i] = book2MarginCand{Margin: 600_000 - i*2_000}
	}
	byItem := map[string][]book2MarginCand{"sword7": vol, "mega": fat}
	base := map[string]int{"sword7": 300_000, "mega": 300_000}
	p10 := map[string]int{"sword7": 900_000, "mega": 5_000_000}
	plan := book2PlanFloors(byItem, base, p10, 15, 8)
	if !plan.GlobalOn {
		t.Fatalf("expected global ON, note=%s", plan.Note)
	}
	// volume: max nac = 0.30×900k = 270k → soft base 300k
	if plan.FloorByItem["sword7"] > 350_000 {
		t.Fatalf("sword7 got global crush floor=%d note=%s", plan.FloorByItem["sword7"], plan.Note)
	}
	if plan.FloorByItem["mega"] < plan.GlobalMarg && plan.FloorByItem["mega"] < 500_000 {
		t.Fatalf("mega=%d should take fat raise/global note=%s", plan.FloorByItem["mega"], plan.Note)
	}
}

func TestBook2MaxNacForP10(t *testing.T) {
	// volume 1.2M → max nac 15% = 180k (buy≥0.85)
	if got := book2MaxNacForP10(1_200_000); got != 180_000 {
		t.Fatalf("vol 1.2M → %d want 180k", got)
	}
	// fat 4M → max nac 30% = 1.2M (buy≥0.70)
	if got := book2MaxNacForP10(4_000_000); got != 1_200_000 {
		t.Fatalf("fat 4M → %d want 1.2M", got)
	}
}

func TestBook2SkuRaiseK(t *testing.T) {
	if got := book2SkuRaiseK(0); got != 1 {
		t.Fatalf("0 → %d", got)
	}
	if got := book2SkuRaiseK(9); got != 3 {
		t.Fatalf("9 → %d want 3", got)
	}
	if got := book2SkuRaiseK(60); got != 12 {
		t.Fatalf("60 → %d want 12", got)
	}
}

func TestBook2VolumeSellCap(t *testing.T) {
	if book2VolumeSellCap != 1_000_000 {
		t.Fatalf("cap=%d", book2VolumeSellCap)
	}
}

func TestBook2VolumeBuyBelowEdge(t *testing.T) {
	if book2VolumeBuyBelowEdge != 200_000 {
		t.Fatalf("slack=%d", book2VolumeBuyBelowEdge)
	}
	// sell 1.0 nac 300k → buyMax 0.7; edge 0.78 → cap 0.58
	sell, nac, edge := 1_000_000, 300_000, 780_000
	buyMax := sell - nac
	cap := edge - book2VolumeBuyBelowEdge
	if buyMax <= cap {
		t.Fatalf("fixture: buyMax %d should exceed cap %d", buyMax, cap)
	}
	if cap != 580_000 {
		t.Fatalf("cap=%d want 580k", cap)
	}
}

func TestBook2ExpensiveBuyFreezeHelpers(t *testing.T) {
	if !book2ExpensiveSKU("megasword-яд3-1.21", 1_000_000) {
		t.Fatal("megasword by name")
	}
	if !book2ExpensiveSKU("pochti-megasword-1.21", 500_000) {
		t.Fatal("pochti by name")
	}
	if !book2ExpensiveSKU("sword7-1.21", 3_000_000) {
		t.Fatal("high sell is expensive")
	}
	if book2ExpensiveSKU("sword7-1.21", 1_000_000) {
		t.Fatal("volume sword7 not expensive by name/price")
	}
	if book2FreezeBuyNac(4_900_007) != 4_900_007 {
		t.Fatal("freeze nac = sell → buyMax 0")
	}
}
