package main

import (
	"strings"
	"testing"
)

func isV9Down(action string) bool {
	return strings.Contains(action, "corridor_price_down")
}

func isV9Up(action string) bool {
	return strings.Contains(action, "corridor_price_up")
}

// share=100 → lo=18 hi=25 soft=28 over=35 dump=50 (без corridorMinBandSpan expand).
func v9Base(held, sales, buys, price, step, share, p10 int, p10OK bool) v9Input {
	return v9Input{
		Held: held, Sales: sales, Buys: buys,
		Price: price, Step: step, Share: share,
		P10: p10, P10OK: p10OK,
		PriceFloor: 100_000,
		Band: stockBandFracs{
			lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
			over: stockOverFrac, dump: stockDumpFrac,
		},
	}
}

func TestV9DownLowStockVeto(t *testing.T) {
	in := v9Base(25, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true) // held==hi
	d := v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("held<=hi must not DOWN: %+v", d)
	}
	in.Held = 10
	d = v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("held<hi must not DOWN: %+v", d)
	}
}

func TestV9DownOverDumpBlockedWhenHeldAtHi(t *testing.T) {
	in := v9Base(25, 0, 0, 2_000_000, 100_000, 100, 1_500_000, true)
	d := v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("held<=hi blocks DOWN: %+v", d)
	}
}

func TestV9DownUnderpriceVeto(t *testing.T) {
	// held=50 dump; ratio 1.8/2.1 ≈ 0.857 < 0.95
	in := v9Base(50, 0, 0, 1_800_000, 100_000, 100, 2_100_000, true)
	d := v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("ratio<0.95 must block DOWN: %+v", d)
	}
	if d.Reason != "underprice_down_veto" {
		t.Fatalf("reason=%s want underprice_down_veto", d.Reason)
	}
}

func TestV9DownRatioBoundary095(t *testing.T) {
	// exactly 0.95 → allow; 0.90 → still block (stricter than old 0.90 gate)
	in := v9Base(50, 0, 0, 1_900_000, 100_000, 100, 2_000_000, true) // 0.95
	d := v9Decide(in)
	if !isV9Down(d.Action) {
		t.Fatalf("ratio==0.95 must allow DOWN: %+v", d)
	}
	in = v9Base(50, 0, 0, 1_800_000, 100_000, 100, 2_000_000, true) // 0.90
	d = v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("ratio==0.90 must block DOWN under 0.95 veto: %+v", d)
	}
}

func TestV9DownOverAllowed(t *testing.T) {
	in := v9Base(35, 1, 0, 2_000_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if d.Action != "corridor_price_down_v9_over" {
		t.Fatalf("want over DOWN got %+v", d)
	}
	if d.NewPrice != 1_800_000 {
		t.Fatalf("over should -2step: %d", d.NewPrice)
	}
}

func TestV9DownDumpAllowed(t *testing.T) {
	in := v9Base(50, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if d.Action != "corridor_price_down_v9_dump" {
		t.Fatalf("want dump got %+v", d)
	}
	if d.NewPrice != 1_800_000 {
		t.Fatalf("dump -2step: %d", d.NewPrice)
	}
}

func TestV9DownSoftAboveHi(t *testing.T) {
	in := v9Base(26, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true) // >hi, <over
	d := v9Decide(in)
	if d.Action != "corridor_price_down_v9_soft" {
		t.Fatalf("want soft got %+v", d)
	}
	if d.NewPrice != 1_900_000 {
		t.Fatalf("soft -1step: %d", d.NewPrice)
	}
}

func TestV9DemandUp(t *testing.T) {
	in := v9Base(10, 3, 1, 1_900_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_demand" {
		t.Fatalf("want demand UP got %+v", d)
	}
	if d.NewPrice != 2_000_000 {
		t.Fatalf("UP +1step: %d", d.NewPrice)
	}
	if d.UpCooldown != v9UpCooldownCycles {
		t.Fatalf("up_cd=%d", d.UpCooldown)
	}
}

func TestV9DemandSales2Hold(t *testing.T) {
	in := v9Base(10, 2, 0, 1_900_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("sales=2 day must HOLD: %+v", d)
	}
}

func TestV9DemandBuysGteSalesHold(t *testing.T) {
	in := v9Base(10, 3, 3, 1_900_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("buys>=sales must HOLD: %+v", d)
	}
}

func TestV9DemandAboveMarketHold(t *testing.T) {
	in := v9Base(10, 3, 1, 2_200_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("ratio>=1.05 must not UP: %+v", d)
	}
	if d.Reason != "demand_above_market" {
		t.Fatalf("reason=%s", d.Reason)
	}
}

func TestV9NightDemandNeeds4(t *testing.T) {
	in := v9Base(10, 3, 1, 1_900_000, 100_000, 100, 2_000_000, true)
	in.Night = true
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("night sales=3 must HOLD: %+v", d)
	}
	in.Sales = 4
	d = v9Decide(in)
	if d.Action != "corridor_price_up_v9_demand" {
		t.Fatalf("night sales=4 must UP: %+v", d)
	}
}

func TestV9EmptyCatchup(t *testing.T) {
	in := v9Base(0, 0, 0, 1_500_000, 100_000, 100, 2_000_000, true)
	in.EmptyStreak = 1
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_empty_catchup" {
		t.Fatalf("want catchup got %+v", d)
	}
	if d.NewPrice != 1_600_000 || d.NewPrice > in.P10 {
		t.Fatalf("catchup price=%d", d.NewPrice)
	}
}

func TestV9EmptyCatchupStreak1Hold(t *testing.T) {
	in := v9Base(0, 0, 0, 1_500_000, 100_000, 100, 2_000_000, true)
	in.EmptyStreak = 0
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("streak 1 must HOLD: %+v", d)
	}
}

func TestV9EmptyCatchupBelowP10OK(t *testing.T) {
	// ratio 0.85 — раньше стоп на 0.80; теперь safety только p10
	in := v9Base(0, 0, 0, 1_700_000, 100_000, 100, 2_000_000, true)
	in.EmptyStreak = 1
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_empty_catchup" {
		t.Fatalf("ratio 0.85 must catchup toward p10: %+v", d)
	}
}

func TestV9EmptyCatchupAtP10Hold(t *testing.T) {
	in := v9Base(0, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true) // ratio 1.0
	in.EmptyStreak = 2
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("at p10 safety must HOLD: %+v", d)
	}
}

func TestV9EmptyCatchupStopsWhenBuys(t *testing.T) {
	// buys>0 → empty streak resets inside decide → no catchup
	in := v9Base(0, 0, 1, 1_500_000, 100_000, 100, 2_000_000, true)
	in.EmptyStreak = 5
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("buys>0 must not catchup: %+v", d)
	}
}

func TestV9EmptyCatchupCap(t *testing.T) {
	in := v9Base(0, 0, 0, 900_000, 400_000, 100, 1_200_000, true) // ratio 0.75; +step > p10
	in.EmptyStreak = 2
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("price+step>market must HOLD: %+v", d)
	}
	if d.Reason != "catchup_above_market" {
		t.Fatalf("reason=%s", d.Reason)
	}
}

func TestV9HoldNoPriceChange(t *testing.T) {
	in := v9Base(20, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true)
	d := v9Decide(in)
	if d.NewPrice != in.Price {
		t.Fatalf("HOLD changed price %d→%d", in.Price, d.NewPrice)
	}
}

func TestV9CooldownBlocksSecondUp(t *testing.T) {
	in := v9Base(10, 3, 1, 1_900_000, 100_000, 100, 2_000_000, true)
	in.UpCooldown = 2
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("up_cd must block: %+v", d)
	}
}

func TestV9TrajectoryEmptyCatchupThenCd(t *testing.T) {
	price, p10, step := 1_500_000, 2_000_000, 100_000
	streak, cd, ups := 0, 0, 0
	for i := 0; i < 8; i++ {
		in := v9Base(0, 0, 0, price, step, 100, p10, true)
		in.EmptyStreak = streak
		in.UpCooldown = cd
		d := v9Decide(in)
		streak, cd = d.EmptyStreak, d.UpCooldown
		if isV9Up(d.Action) {
			ups++
			price = d.NewPrice
		}
	}
	if ups < 1 {
		t.Fatal("expected catchup UP")
	}
	if price > p10 {
		t.Fatalf("price %d > p10 %d", price, p10)
	}
}

func TestV9NeverDownWhenLowStockInvariant(t *testing.T) {
	for held := 0; held <= 25; held++ {
		in := v9Base(held, 0, 5, 2_000_000, 100_000, 100, 2_000_000, true)
		d := v9Decide(in)
		if isV9Down(d.Action) {
			t.Fatalf("held=%d DOWN: %+v", held, d)
		}
	}
}

func TestV9NeverDownWhenUnderpriceInvariant(t *testing.T) {
	for held := 26; held <= 60; held++ {
		in := v9Base(held, 0, 0, 1_700_000, 100_000, 100, 2_000_000, true)
		d := v9Decide(in)
		if isV9Down(d.Action) {
			t.Fatalf("underprice held=%d DOWN: %+v", held, d)
		}
	}
}

func TestCapitalPolicyV9(t *testing.T) {
	if capitalPolicy != capitalPolicyV9 {
		t.Fatalf("active=%s want %s (rollback: capitalPolicyV4 / classic)", capitalPolicy, capitalPolicyV9)
	}
}
