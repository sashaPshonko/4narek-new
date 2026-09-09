package main

import "testing"

func TestEvalTrustedMinDiscoveryJump(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, NUUID: 40, OK: true}
	ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, book, false)
	if !ev.WouldFire || ev.WouldPrice != 1_000_000 {
		t.Fatalf("should jump to min: %+v", ev)
	}
	if ev.GapSteps < 5 {
		t.Fatalf("gap_steps: %+v", ev)
	}
}

func TestEvalTrustedMinDiscoveryRequiresEmptyNoBuys(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 1, 0, book, false); ev.WouldFire || ev.SkipReason != "held_not_empty" {
		t.Fatalf("held: %+v", ev)
	}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 2, book, false); ev.WouldFire || ev.SkipReason != "had_buys" {
		t.Fatalf("buys: %+v", ev)
	}
}

func TestEvalTrustedMinDiscoveryNoLower(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	ev := evalTrustedMinDiscovery(1_200_000, 100_000, 0, 0, book, false)
	if ev.WouldFire || ev.SkipReason != "our_at_or_above_min" {
		t.Fatalf("must not lower: %+v", ev)
	}
}

func TestEvalTrustedMinDiscoveryShallowGapNoJump(t *testing.T) {
	// 119→120 style: trust OK but not deep (ratio>0.80 and <4 steps)
	book := ahBookTrustedSellerMinSnap{TrustedMin: 120_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	ev := evalTrustedMinDiscovery(119_000, 1_000, 0, 0, book, false)
	if ev.WouldFire || ev.SkipReason != "gap_too_small" {
		// 120 > 119+1000? No → gap_too_small
		t.Fatalf("shallow 119→120: %+v", ev)
	}
	book2 := ahBookTrustedSellerMinSnap{TrustedMin: 350_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	ev2 := evalTrustedMinDiscovery(300_000, 100_000, 0, 0, book2, false)
	// min > our+step (350>400? no) wait 350 > 300+100 = 400? No
	if ev2.WouldFire {
		t.Fatalf("should not fire when min <= our+step: %+v", ev2)
	}
	// Deep enough in steps but check 3-step gap blocked by deep filter when ratio > 0.80
	// our=700k min=1M step=100k: gap=3 steps, ratio=0.70 → fires via ratio
	book3 := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	if ev := evalTrustedMinDiscovery(700_000, 100_000, 0, 0, book3, false); !ev.WouldFire {
		t.Fatalf("ratio 0.70 should fire: %+v", ev)
	}
	// our=850k min=1M step=100k: gap=1.5 steps, ratio=0.85 → not deep, but also min > our+step? 1M > 950k yes
	book4 := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}
	if ev := evalTrustedMinDiscovery(850_000, 100_000, 0, 0, book4, false); ev.WouldFire || ev.SkipReason != "gap_not_deep" {
		t.Fatalf("shallow ratio+steps: %+v", ev)
	}
}

func TestEvalTrustedMinDiscoveryTrustGates(t *testing.T) {
	thin := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 5, SellersNearMin: 5, OK: true}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, thin, false); ev.WouldFire || ev.SkipReason != "thin_sellers" {
		t.Fatalf("thin: %+v", ev)
	}
	unconfirmed := ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 1, OK: true}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, unconfirmed, false); ev.WouldFire || ev.SkipReason != "min_unconfirmed" {
		t.Fatalf("unconfirmed: %+v", ev)
	}
	if ev := evalTrustedMinDiscovery(400_000, 100_000, 0, 0, ahBookTrustedSellerMinSnap{TrustedMin: 1_000_000, UniqueSellers: 20, SellersNearMin: 5, OK: true}, true); ev.WouldFire || ev.SkipReason != "manual_lock" {
		t.Fatalf("manual: %+v", ev)
	}
}

func TestEvalTrustedMinDiscoveryNoNacenkaInTarget(t *testing.T) {
	book := ahBookTrustedSellerMinSnap{TrustedMin: 900_000, UniqueSellers: 18, SellersNearMin: 4, OK: true}
	ev := evalTrustedMinDiscovery(500_000, 100_000, 0, 0, book, false)
	if !ev.WouldFire || ev.WouldPrice != 900_000 {
		t.Fatalf("target leaked nacenka? got %+v", ev)
	}
}

func TestTrustedMinDiscoveryGapOK(t *testing.T) {
	if !trustedMinDiscoveryGapOK(400_000, 1_000_000, 100_000) {
		t.Fatal("ratio deep")
	}
	if !trustedMinDiscoveryGapOK(500_000, 1_000_000, 100_000) {
		t.Fatal("4 steps")
	}
	if trustedMinDiscoveryGapOK(900_000, 1_000_000, 100_000) {
		t.Fatal("shallow")
	}
}
