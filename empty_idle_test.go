package main

import "testing"

func TestIsEmptyIdle(t *testing.T) {
	if !isEmptyIdle(0, 0, 0) {
		t.Fatal("0,0,0")
	}
	if isEmptyIdle(1, 0, 0) || isEmptyIdle(0, 1, 0) || isEmptyIdle(0, 0, 1) {
		t.Fatal("non-empty must not match")
	}
}

func TestEmptyIdleNoBookUp(t *testing.T) {
	// held=0,sales=0,buys=0, no AH, far above floor → still +1 recovery
	price, step, floor := 2_000_000, 100_000, 400_000
	ev := evalFloorEscape(price, step, floor, 0, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !ev.WouldFire || ev.WouldPrice != 2_100_000 || ev.Reason != "empty_idle_step" {
		t.Fatalf("empty no book: %+v", ev)
	}
	if ev.Action != floorEscapeActionEmptyIdle {
		t.Fatalf("action: %s", ev.Action)
	}
}

func TestEmptyIdleThinBookUp(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 2, SellersNearMin: 1, OK: true}
	tm := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, book, false)
	if tm.WouldFire {
		t.Fatalf("thin must not jump: %+v", tm)
	}
	ev := evalFloorEscape(400_000, 100_000, 400_000, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.WouldPrice != 500_000 {
		t.Fatalf("thin → +1: %+v", ev)
	}
}

// Thin AH dump BELOW our price must not cancel EMPTY_IDLE +1 via capPrice.
func TestEmptyIdleThinAHBelowOurPricePlusOne(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 159_000, UniqueSellers: 7, SellersNearMin: 3, OK: true}
	our, step, floor := 300_000, 50_000, 200_000
	tm := evalTrustedMinDiscovery(our, step, 0, 0, book, false)
	if tm.WouldFire {
		t.Fatalf("thin dump must not jump: %+v", tm)
	}
	ev := evalFloorEscape(our, step, floor, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.WouldPrice != our+step {
		t.Fatalf("thin AH below our → +1 not HOLD: %+v", ev)
	}
	if ev.SkipReason == "at_or_above_cap" {
		t.Fatal("untrusted AH must not at_or_above_cap EMPTY_IDLE +1")
	}
}

// min_unconfirmed (untrusted) AH below our price → still +1.
func TestEmptyIdleUntrustedAHBelowOurPricePlusOne(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 200_000, UniqueSellers: 20, SellersNearMin: 1, OK: true}
	our, step, floor := 300_000, 50_000, 200_000
	tm := evalTrustedMinDiscovery(our, step, 0, 0, book, false)
	if tm.WouldFire || tm.SkipReason != "min_unconfirmed" {
		t.Fatalf("want min_unconfirmed: %+v", tm)
	}
	ev := evalFloorEscape(our, step, floor, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.WouldPrice != our+step {
		t.Fatalf("untrusted AH below → +1: %+v", ev)
	}
}

// Trusted AH below our price: no jump DOWN through AH; EMPTY_IDLE still +1 (not AH-capped).
func TestEmptyIdleTrustedAHBelowOurPriceNoJumpViaAH(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 200_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	our, step, floor := 300_000, 50_000, 200_000
	tm := evalTrustedMinDiscovery(our, step, 0, 0, book, false)
	if tm.WouldFire {
		t.Fatalf("AH below our must not jump: %+v", tm)
	}
	if tm.SkipReason != "our_at_or_above_min" {
		t.Fatalf("skip: %s", tm.SkipReason)
	}
	// DOWN via this AH is not a floor-escape outcome; +1 must not be blocked by that AH.
	ev := evalFloorEscape(our, step, floor, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, tm.WouldFire, tm.WouldPrice)
	if !ev.WouldFire || ev.WouldPrice != our+step {
		t.Fatalf("no AH-jump / no AH-cap hold: %+v", ev)
	}
	if ev.Reason == "floor_escape_trusted_jump" || ev.WouldPrice == book.TrustedMin {
		t.Fatalf("must not UP via that AH: %+v", ev)
	}
}

func TestEmptyIdleCooldownSkipsExactlyOneCycle(t *testing.T) {
	price, step, floor := 300_000, 50_000, 200_000
	// Cycle A: UP
	a := evalFloorEscape(price, step, floor, 0, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !a.WouldFire || a.WouldPrice != price+step {
		t.Fatalf("cycle A UP: %+v", a)
	}
	if floorEscapeCooldownCycles != 1 {
		t.Fatalf("FloorEscapeCooldownCycles=%d want 1", floorEscapeCooldownCycles)
	}
	cd := floorEscapeCooldownCycles // set on fire (caller)
	// Cycle B: cooldown blocks
	b := evalFloorEscape(a.WouldPrice, step, floor, 0, 0, 0, 0, 0, cd, 0, false, false, false, 0)
	if b.WouldFire || b.SkipReason != "escape_cooldown" {
		t.Fatalf("cycle B must skip exactly via escape_cooldown: %+v", b)
	}
	// Non-escape HOLD ticks CD → 0 (mirrors pricing.go tick on non-escape)
	cd--
	if cd != 0 {
		t.Fatalf("after one non-escape cycle CD=%d want 0", cd)
	}
	// Cycle C: UP again
	c := evalFloorEscape(a.WouldPrice, step, floor, 0, 0, 0, 0, 0, cd, 0, false, false, false, 0)
	if !c.WouldFire || c.WouldPrice != a.WouldPrice+step {
		t.Fatalf("cycle C UP after one skip: %+v", c)
	}
}

func TestEmptyIdleTwoAvailableCyclesTwoUps(t *testing.T) {
	// No trusted cap, no other stops: two available cycles (CD=0 each) → two +1.
	price, step, floor := 300_000, 50_000, 200_000
	thinBelow := 159_000
	ev1 := evalFloorEscape(price, step, floor, thinBelow, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !ev1.WouldFire || ev1.WouldPrice != price+step {
		t.Fatalf("first UP: %+v", ev1)
	}
	ev2 := evalFloorEscape(ev1.WouldPrice, step, floor, thinBelow, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !ev2.WouldFire || ev2.WouldPrice != ev1.WouldPrice+step {
		t.Fatalf("second available cycle UP: %+v", ev2)
	}
}

func TestEmptyIdleTrustedJump(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 6, SellersNearMin: 2, OK: true}
	tm := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, book, false)
	if !tm.WouldFire {
		t.Fatalf("trusted should jump: %+v", tm)
	}
	ev := evalFloorEscape(400_000, 100_000, 400_000, book.TrustedMin, 0, 0, 0, 0, 0, 0, false, false, true, tm.WouldPrice)
	if !ev.WouldFire || ev.Reason != "floor_escape_trusted_jump" || ev.WouldPrice != 1_000_000 {
		t.Fatalf("jump: %+v", ev)
	}
}

func TestEmptyIdleAhBookBearishDownForbidden(t *testing.T) {
	// Bearish AH conditions would soft-↓ at held=0 without EMPTY_IDLE gate.
	if !shouldSoftDownFromAhBook(2_000_000, 500_000, 400_000, 300_000, 50, 100_000, false, false, 0) {
		t.Fatal("precondition: ah_book soft-↓ would fire")
	}
	if !isEmptyIdle(0, 0, 0) {
		t.Fatal("empty idle")
	}
	allow := shouldSoftDownFromAhBook(2_000_000, 500_000, 400_000, 300_000, 50, 100_000, false, false, 0) &&
		!isEmptyIdle(0, 0, 0)
	if allow {
		t.Fatal("EMPTY_IDLE must forbid ah_book DOWN")
	}
}

func TestEmptyIdleStaleDownForbidden(t *testing.T) {
	// ghost already blocks held=0; EMPTY_IDLE is absolute belt for any applyDown label.
	if ghostPriceDownOK(0, 0, 99) {
		t.Fatal("ghost must not OK at held=0")
	}
	if !isEmptyIdle(0, 0, 0) {
		t.Fatal("empty idle")
	}
	// stale path requires ghostPriceDownOK — both layers forbid
	if isEmptyIdle(0, 0, 0) && ghostPriceDownOK(0, 0, 99) {
		t.Fatal("stale must stay forbidden")
	}
}

func TestNonEmptySalesZeroDownStillAllowed(t *testing.T) {
	// held>0, sales=0: hard-down / ah_book soft-↓ gates still open
	if !allowHardDown(0, 0, 0.6) {
		t.Fatal("first idle hard down with stock should allow")
	}
	if isEmptyIdle(8, 0, 0) {
		t.Fatal("held>0 is not empty idle")
	}
	if !shouldSoftDownFromAhBook(2_000_000, 500_000, 400_000, 300_000, 50, 100_000, false, false, 8) {
		t.Fatal("with held>0 bearish ah_book soft-↓ still allowed")
	}
	allow := shouldSoftDownFromAhBook(2_000_000, 500_000, 400_000, 300_000, 50, 100_000, false, false, 8) &&
		!isEmptyIdle(8, 0, 0)
	if !allow {
		t.Fatal("non-empty must keep ah_book DOWN")
	}
}

func TestEmptyIdleOneUpPerCycle(t *testing.T) {
	ev := evalFloorEscape(400_000, 100_000, 400_000, 1_000_000, 0, 0, 0, 0, 0, 0, false, true, true, 1_000_000)
	if ev.WouldFire || ev.SkipReason != "already_moved" {
		t.Fatalf("already_moved blocks second UP: %+v", ev)
	}
}
