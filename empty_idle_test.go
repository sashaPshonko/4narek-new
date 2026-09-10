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
