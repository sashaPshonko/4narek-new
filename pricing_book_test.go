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
	if ratio < 0.85 || ratio > 0.95 {
		t.Fatalf("buyMax/p10=%.3f want ~0.90 (buyMax=%d nac=%d src=%s)", ratio, buyMax, nac, src)
	}
	if !strings.Contains(src, "0.90") {
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
	if s.Buy != 0.90 || s.Sell != 1.00 {
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
	if buyMax < 850_000 || buyMax > 950_000 {
		t.Fatalf("buyMax=%d nac=%d want ~0.90×p10", buyMax, nac)
	}
}

func TestBook2ClampStep(t *testing.T) {
	step := 100_000
	// обычный: ±2 step
	if got := book2ClampStep(1_000_000, 1_500_000, step); got != 1_200_000 {
		t.Fatalf("cap up got=%d", got)
	}
	if got := book2ClampStep(1_200_000, 1_000_000, step); got != 1_000_000 {
		t.Fatalf("cap down got=%d want 1.0M (2 steps)", got)
	}
	// deep ≥25%: без cap (cold 2.7→0.9)
	if got := book2ClampStep(2_700_000, 900_000, step); got != 900_000 {
		t.Fatalf("deep down got=%d", got)
	}
}
