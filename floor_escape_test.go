package main

import "testing"

func TestFloorEscapeIdleNearFloorPlusOne(t *testing.T) {
	// held=0,sales=0,buys=0,near_floor → +1
	price, step, floor := 400_000, 100_000, 400_000
	ev := evalFloorEscape(price, step, floor, 0, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !ev.WouldFire || ev.WouldPrice != 500_000 || ev.Reason != "floor_escape_empty" {
		t.Fatalf("near_floor idle: %+v", ev)
	}
}

func TestFloorEscapeIdleDeepAHPlusOne(t *testing.T) {
	// held=0,sales=0,buys=0,deep_AH, no trusted jump → +1
	price, step, floor, ah := 400_000, 100_000, 200_000, 1_000_000 // deep vs AH, not near floor
	ev := evalFloorEscape(price, step, floor, ah, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !ev.WouldFire || ev.WouldPrice != 500_000 || ev.Reason != "floor_escape_deep_ah" {
		t.Fatalf("deep_AH idle: %+v", ev)
	}
	if !ev.DeepAH || ev.NearFloor {
		t.Fatalf("flags: near=%v deep=%v", ev.NearFloor, ev.DeepAH)
	}
}

func TestFloorEscapeBuysGtSalesHold(t *testing.T) {
	ev := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 0, 2, 0, 0, 0, false, false, false, 0)
	if ev.WouldFire || ev.SkipReason != "buys_gt_sales" {
		t.Fatalf("buys>sales: %+v", ev)
	}
}

func TestFloorEscapeSalesOnFloorNoSpecial(t *testing.T) {
	// sales>0 on floor, no down_streak → no special UP
	ev := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 2, 0, 0, 0, 0, false, false, false, 0)
	if ev.WouldFire || ev.SkipReason != "sales_on_floor_no_special" {
		t.Fatalf("sales on floor: %+v", ev)
	}
}

func TestFloorEscapeDownStreakPlusOne(t *testing.T) {
	// near_floor + down_streak≥3 → +1 (held may be >0)
	ev := evalFloorEscape(400_000, 100_000, 400_000, 0, 3, 0, 0, 3, 0, 0, false, false, false, 0)
	if !ev.WouldFire || ev.WouldPrice != 500_000 || ev.Reason != "floor_escape_down_streak" {
		t.Fatalf("down_streak: %+v", ev)
	}
}

func TestFloorEscapeManualLock(t *testing.T) {
	ev := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 0, 0, 0, 0, 0, true, false, false, 0)
	if ev.WouldFire || ev.SkipReason != "manual_lock" {
		t.Fatalf("manual: %+v", ev)
	}
}

func TestFloorEscapeMaxPriceCap(t *testing.T) {
	ev := evalFloorEscape(400_000, 100_000, 400_000, 0, 0, 0, 0, 0, 0, 450_000, false, false, false, 0)
	if !ev.WouldFire || ev.WouldPrice != 450_000 {
		t.Fatalf("maxPrice cap: %+v", ev)
	}
	// already at max → no raise
	ev2 := evalFloorEscape(450_000, 100_000, 400_000, 0, 0, 0, 0, 0, 0, 450_000, false, false, false, 0)
	if ev2.WouldFire {
		t.Fatalf("at max must not raise: %+v", ev2)
	}
}

func TestFloorEscapeAlreadyMovedOnlyOneUp(t *testing.T) {
	// floor escape + other UP signal → only one raise (caller alreadyMoved)
	ev := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 0, 0, 0, 0, 0, false, true, true, 1_000_000)
	if ev.WouldFire || ev.SkipReason != "already_moved" {
		t.Fatalf("already_moved: %+v", ev)
	}
	// Without already_moved, trusted jump wins once
	ev2 := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 0, 0, 0, 0, 0, false, false, true, 1_000_000)
	if !ev2.WouldFire || ev2.Reason != "floor_escape_trusted_jump" || ev2.WouldPrice != 1_000_000 {
		t.Fatalf("trusted jump path: %+v", ev2)
	}
}

func TestFloorEscapeDeepAHTrustedJump(t *testing.T) {
	// deep gap + trusted book → jump
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 6, SellersNearMin: 2, OK: true}
	tm := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, book, false)
	if !tm.WouldFire {
		t.Fatalf("soft deep trust should fire: %+v", tm)
	}
	ev := evalFloorEscape(400_000, 100_000, 400_000, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.Reason != "floor_escape_trusted_jump" || ev.WouldPrice != 1_000_000 {
		t.Fatalf("trusted jump: %+v", ev)
	}
}

func TestFloorEscapeDeepAHInsufficientTrustPlusOne(t *testing.T) {
	// deep gap, thin book → ordinary +1, not jump
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 3, SellersNearMin: 1, OK: true}
	tm := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, book, false)
	if tm.WouldFire {
		t.Fatalf("insufficient trust must not jump: %+v", tm)
	}
	ev := evalFloorEscape(400_000, 100_000, 400_000, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.Reason != "floor_escape_deep_ah" || ev.WouldPrice != 500_000 {
		t.Fatalf("want +1 not jump: %+v", ev)
	}
}

func TestPriceNearFloorAndDeepAHHelpers(t *testing.T) {
	if !priceNearFloor(400_000, 400_000, 100_000) {
		t.Fatal("at floor")
	}
	if priceNearFloor(700_000, 400_000, 100_000) {
		t.Fatal("far above floor")
	}
	if !priceDeepVsAH(400_000, 1_000_000, 100_000) {
		t.Fatal("ratio 0.4 deep")
	}
	if priceDeepVsAH(900_000, 1_000_000, 100_000) {
		t.Fatal("shallow not deep")
	}
}

func TestEvalTrustedMinDiscoveryDeepPitSoftGate(t *testing.T) {
	// our/min=0.40 ≤ 0.50: sellers≥5 near≥2 enough
	soft := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 5, SellersNearMin: 2, OK: true}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, soft, false); !ev.WouldFire {
		t.Fatalf("deep soft gate: %+v", ev)
	}
	// same sellers but shallow gap (ratio 0.70): hard gate sellers=15 → thin
	shallow := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 5, SellersNearMin: 2, OK: true}
	if ev := evalTrustedMinDiscovery(700_000, 100_000, 0, 0, shallow, false); ev.WouldFire || ev.SkipReason != "thin_sellers" {
		t.Fatalf("shallow keeps hard gate: %+v", ev)
	}
}
