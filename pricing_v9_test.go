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
// p10 — demand-guard. MultiFloor по умолчанию низкий (не мешает ↓); catchup-тесты ставят пол явно.
func v9Base(held, sales, buys, price, step, share, p10 int, p10OK bool) v9Input {
	return v9Input{
		Held: held, Sales: sales, Buys: buys,
		Price: price, Step: step, Share: share,
		P10: p10, P10OK: p10OK,
		MultiFloor: 100_000, MultiFloorOK: true,
		PriceFloor: 100_000,
		Band: stockBandFracs{
			lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
			over: stockOverFrac, dump: stockDumpFrac,
		},
	}
}

func v9WithBookFloor(in v9Input, floor int) v9Input {
	in.MultiFloor = floor
	in.MultiFloorOK = floor > 0
	return in
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

func TestV9DownUnderpriceNoLongerVeto(t *testing.T) {
	// held=50 dump; ratio 1.8/2.1 ≈ 0.857 — раньше veto 0.95, теперь ↓ до пола книги
	in := v9Base(50, 0, 0, 1_800_000, 100_000, 100, 2_100_000, true)
	in.MultiFloor = 1_500_000
	in.MultiFloorOK = true
	d := v9Decide(in)
	if !isV9Down(d.Action) {
		t.Fatalf("underprice must allow DOWN to book floor: %+v", d)
	}
	if d.NewPrice != 1_600_000 { // dump -2step, выше пола 1.5M
		t.Fatalf("newPrice=%d want 1600000", d.NewPrice)
	}
}

func TestV9DownClampsToBookFloor(t *testing.T) {
	in := v9Base(50, 0, 0, 1_600_000, 100_000, 100, 2_000_000, true)
	in.MultiFloor = 1_550_000
	in.MultiFloorOK = true
	d := v9Decide(in)
	if !isV9Down(d.Action) {
		t.Fatalf("want DOWN clamped to floor: %+v", d)
	}
	if d.NewPrice != 1_550_000 {
		t.Fatalf("newPrice=%d want book floor 1550000", d.NewPrice)
	}
}

func TestV9DownAtBookFloorHold(t *testing.T) {
	in := v9Base(50, 0, 0, 1_500_000, 100_000, 100, 2_000_000, true)
	in.MultiFloor = 1_500_000
	in.MultiFloorOK = true
	d := v9Decide(in)
	if isV9Down(d.Action) {
		t.Fatalf("at floor must not DOWN: %+v", d)
	}
	if d.Reason != "at_book_floor" {
		t.Fatalf("reason=%s want at_book_floor", d.Reason)
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
	in := v9Base(10, 3, 1, 2_100_000, 100_000, 100, 2_000_000, true) // ratio==1.05
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("ratio>=1.05 must not UP: %+v", d)
	}
	if d.Reason != "demand_above_market" {
		t.Fatalf("reason=%s", d.Reason)
	}
	in.Price = 2_000_000 // ratio 1.00 < 1.05 → allow
	d = v9Decide(in)
	if d.Action != "corridor_price_up_v9_demand" {
		t.Fatalf("ratio 1.00 must UP: %+v", d)
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
	in := v9WithBookFloor(v9Base(0, 0, 0, 1_500_000, 100_000, 100, 2_000_000, true), 2_000_000)
	in.EmptyStreak = 1
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_empty_catchup" {
		t.Fatalf("want catchup got %+v", d)
	}
	if d.NewPrice != 2_000_000 {
		t.Fatalf("catchup jump to floor=%d got %d", in.MultiFloor, d.NewPrice)
	}
	if d.Reason != "empty_catchup_jump" {
		t.Fatalf("reason=%s", d.Reason)
	}
}

func TestV9EmptyCatchupStreak1Hold(t *testing.T) {
	in := v9WithBookFloor(v9Base(0, 0, 0, 1_500_000, 100_000, 100, 2_000_000, true), 2_000_000)
	in.EmptyStreak = 0
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("streak 1 must HOLD: %+v", d)
	}
}

func TestV9EmptyCatchupBelowP10OK(t *testing.T) {
	in := v9WithBookFloor(v9Base(0, 0, 0, 1_700_000, 100_000, 100, 2_000_000, true), 2_000_000)
	in.EmptyStreak = 1
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_empty_catchup" {
		t.Fatalf("below floor must catchup: %+v", d)
	}
	if d.NewPrice != in.MultiFloor {
		t.Fatalf("jump to %d got %d", in.MultiFloor, d.NewPrice)
	}
}

func TestV9EmptyCatchupAtP10Hold(t *testing.T) {
	in := v9WithBookFloor(v9Base(0, 0, 0, 2_000_000, 100_000, 100, 2_000_000, true), 2_000_000)
	in.EmptyStreak = 2
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("at floor must HOLD: %+v", d)
	}
}

func TestV9EmptyCatchupStopsWhenBuys(t *testing.T) {
	in := v9WithBookFloor(v9Base(0, 0, 1, 1_500_000, 100_000, 100, 2_000_000, true), 2_000_000)
	in.EmptyStreak = 5
	d := v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("buys>0 must not catchup: %+v", d)
	}
}

func TestV9EmptyCatchupJumpNearFloor(t *testing.T) {
	in := v9WithBookFloor(v9Base(0, 0, 0, 900_000, 400_000, 100, 1_200_000, true), 1_200_000)
	in.EmptyStreak = 2
	d := v9Decide(in)
	if d.Action != "corridor_price_up_v9_empty_catchup" {
		t.Fatalf("want jump got %+v", d)
	}
	if d.NewPrice != 1_200_000 {
		t.Fatalf("jump price=%d", d.NewPrice)
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
		in := v9WithBookFloor(v9Base(0, 0, 0, price, step, 100, p10, true), p10)
		in.EmptyStreak = streak
		in.UpCooldown = cd
		d := v9Decide(in)
		streak, cd = d.EmptyStreak, d.UpCooldown
		if isV9Up(d.Action) {
			ups++
			price = d.NewPrice
		}
	}
	if ups != 1 {
		t.Fatalf("jump catchup once, got ups=%d", ups)
	}
	if price != p10 {
		t.Fatalf("price %d want floor %d", price, p10)
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

func TestV9DownStopsAtBookFloorInvariant(t *testing.T) {
	floor := 1_800_000
	for held := 26; held <= 60; held++ {
		in := v9Base(held, 0, 0, 2_000_000, 100_000, 100, 2_500_000, true)
		in.MultiFloor = floor
		in.MultiFloorOK = true
		d := v9Decide(in)
		if isV9Down(d.Action) && d.NewPrice < floor {
			t.Fatalf("held=%d went below floor: %+v", held, d)
		}
	}
}

func TestCapitalPolicyV9(t *testing.T) {
	if capitalPolicy != capitalPolicyV9 {
		t.Fatalf("active=%s want %s", capitalPolicy, capitalPolicyV9)
	}
}

func TestV9BlockUpStopsDemand(t *testing.T) {
	in := v9Base(5, 5, 0, 1_900_000, 100_000, 100, 2_000_000, true) // under lo=18, under p10
	in.Held = 5
	d := v9Decide(in)
	if !isV9Up(d.Action) {
		t.Fatalf("expected demand UP without BlockUp: %+v", d)
	}
	in.BlockUp = true
	d = v9Decide(in)
	if isV9Up(d.Action) {
		t.Fatalf("BlockUp must stop UP: %+v", d)
	}
}
