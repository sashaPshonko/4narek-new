package main

import (
	"strings"
	"testing"
)

// simEmptyIdleAhCycle mirrors pricing.go AH soft → floor_escape with v8ac grant.
// held is always 0 here (empty idle); targetHi=1 → automaticDownAllowed=false → no A ↓.
func simEmptyIdleAhCycle(
	price, p10, minAsk, nacenka, n, step, floor int,
	marketDownCD int,
) (newPrice int, action string, feWouldFire bool, nextCD int, notes []string) {
	const held, targetHi = 0, 1
	emptyIdle := isEmptyIdle(held, 0, 0)
	bookOK := n >= ahBookMinLotsInWindow && minAsk > 0
	p10OK := n >= ahBookMinLotsInWindow && p10 > 0
	viaEmpty := emptyIdleAhSoftDownOK(emptyIdle, bookOK, p10OK, price, p10, minAsk, nacenka, n, step)
	newPrice = price
	action = "corridor_hold_recover_stale"
	firedA := false
	if viaEmpty && automaticDownAllowed(held, targetHi) {
		tgt := ahBookSoftDownTarget(p10, minAsk, nacenka, step)
		bookFloor := minAsk + nacenka
		if tgt < bookFloor {
			tgt = bookFloor
		}
		applied := ahBookSoftDownApply(newPrice, tgt, floor, step, held)
		if applied < newPrice {
			newPrice = applied
			action = "corridor_price_down_ah_book"
			firedA = true
			notes = append(notes, "empty_idle_ah_soft_down")
		}
	} else if viaEmpty && !automaticDownAllowed(held, targetHi) {
		notes = append(notes, "ah_book soft-↓ blocked no-excess")
	}
	alreadyDown := strings.Contains(action, "price_down")
	recoveryUpBlocked := marketDownCD > 0
	aboveMarket := emptyIdleAboveMarketBlocksUp(emptyIdle, bookOK, p10OK, price, p10, nacenka)
	fe := evalFloorEscape(price, step, floor, 0, 0, 0, 0, 0, 0, 0, false, alreadyDown, false, 0)
	feWouldFire = fe.WouldFire
	if !recoveryUpBlocked && !alreadyDown && fe.WouldFire && fe.WouldPrice > newPrice {
		if aboveMarket {
			notes = append(notes, "floor_escape skipped: above market")
		} else {
			newPrice = fe.WouldPrice
			action = fe.Action
		}
	} else if recoveryUpBlocked && !alreadyDown && fe.WouldFire {
		notes = append(notes, "recovery UP blocked by market_down_cd")
	}
	if firedA {
		nextCD = emptyIdleMarketDownCooldownCycles
	} else if marketDownCD > 0 {
		nextCD = marketDownCD - 1
	}
	return newPrice, action, feWouldFire, nextCD, notes
}

func TestEmptyIdleAhSoftDownThickBookNoGrant(t *testing.T) {
	// v8ac: thick book signal exists, but held=0 → no DOWN grant.
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	if !emptyIdleAhSoftDownOK(true, true, true, price, p10, minAsk, nac, n, step) {
		t.Fatal("signal should still detect thick book")
	}
	got, action, _, _, notes := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if strings.Contains(action, "price_down") || got < price {
		t.Fatalf("held=0 must not DOWN: %s %d→%d notes=%v", action, price, got, notes)
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "no-excess") {
		t.Fatalf("want no-excess note, got %v", notes)
	}
}

func TestEmptyIdleAhSoftDownThinBookNoA(t *testing.T) {
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 12, 100_000, 400_000
	if emptyIdleAhSoftDownOK(true, false, false, price, p10, minAsk, nac, n, step) {
		t.Fatal("thin must not A signal")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if action != floorEscapeActionEmptyIdle || got != price+step {
		t.Fatalf("thin → empty_idle UP, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownNoBookNoA(t *testing.T) {
	price, step, floor := 7_200_004, 100_000, 400_000
	if emptyIdleAhSoftDownOK(true, false, false, price, 0, 0, 400_000, 0, step) {
		t.Fatal("no book must not A")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(price, 0, 0, 400_000, 0, step, floor, 0)
	if action != floorEscapeActionEmptyIdle || got != price+step {
		t.Fatalf("no book → empty_idle UP, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownTreasuryEmptyNoDown(t *testing.T) {
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	if !emptyIdleAhSoftDownOK(true, true, true, price, p10, minAsk, nac, n, step) {
		t.Fatal("A signal under empty_idle")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if strings.Contains(action, "price_down") || got < price {
		t.Fatalf("v8ac: no A DOWN at held=0, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownCooldownTicksWithoutA(t *testing.T) {
	// Without A fire, CD only ticks down if already set.
	price, step, floor := 7_200_004, 100_000, 400_000
	cd := emptyIdleMarketDownCooldownCycles
	thinPrice := price
	for i := 0; i < emptyIdleMarketDownCooldownCycles; i++ {
		got, action, _, next, notes := simEmptyIdleAhCycle(thinPrice, 0, 0, 400_000, 0, step, floor, cd)
		if strings.Contains(action, "price_up") {
			t.Fatalf("under CD must not UP: %s notes=%v", action, notes)
		}
		if got != thinPrice {
			t.Fatalf("price changed under CD")
		}
		cd = next
	}
	if cd != 0 {
		t.Fatalf("after ticks cd=%d", cd)
	}
}

func TestEmptyIdleAhSoftDownAboveMarketBlocksUp(t *testing.T) {
	price, p10, minAsk, nac, n, step, floor := 7_000_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	if !emptyIdleAboveMarketBlocksUp(true, true, true, price, p10, nac) {
		t.Fatal("sell > p10+nac must block idle UP")
	}
	got, action, _, _, notes := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if strings.Contains(action, "price_up") {
		t.Fatalf("fresh bearish must not UP: %s %v", action, notes)
	}
	if got > price {
		t.Fatalf("price rose %d→%d", price, got)
	}
}

func TestEmptyIdleAhSoftDownDoesNotBlockOrdinaryDownGates(t *testing.T) {
	if doiCoverIntensity(5, 1) != "soft" || doiCoverIntensity(3, 1) != "over" {
		t.Fatal("DOI mapping changed")
	}
	if !allowHardDown(0, 0, 0.6) {
		t.Fatal("idle hard-down must stay allowed")
	}
	if ghostPriceDownOK(0, 0, 99) {
		t.Fatal("ghost must stay blocked at held=0")
	}
	if !isEmptyIdle(0, 0, 0) {
		t.Fatal("empty")
	}
	// v8ac: DOI over at held=3 hi=5 has no grant
	if automaticDownAllowed(3, 5) {
		t.Fatal("held=3 hi=5 must not grant")
	}
}

func TestOrdinaryHeldAhSoftDownNeedsGrant(t *testing.T) {
	held, hi := 8, 5
	price, p10, minAsk, nac, n, step := 10_300_002, 2_799_999, 800_000, 300_000, 50, 100_000
	if isEmptyIdle(held, 0, 0) {
		t.Fatal("held>0 not empty_idle")
	}
	if !automaticDownAllowed(held, hi) {
		t.Fatal("grant open")
	}
	if !shouldSoftDownFromAhBook(price, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("ordinary soft-↓ signal")
	}
	ahMay := automaticDownAllowed(held, hi) && shouldSoftDownFromAhBook(price, p10, minAsk, nac, n, step, false, false, held)
	if !ahMay {
		t.Fatal("AH may apply with grant")
	}
	tgt := ahBookSoftDownApply(price, ahBookSoftDownTarget(p10, minAsk, nac, step), 300_000, step, held)
	if tgt >= price {
		t.Fatalf("want lower tgt, got %d", tgt)
	}
	// held=1 ≤ hi=5 → no grant even if book wants ↓
	if automaticDownAllowed(1, hi) {
		t.Fatal("held=1 ≤ hi → no grant")
	}
}

func TestEmptyIdleNoBookNoGhostDown(t *testing.T) {
	if ghostPriceDownOK(0, 0, 99) {
		t.Fatal("ghost must not OK at held=0")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(7_200_004, 0, 0, 400_000, 0, 100_000, 400_000, 0)
	if strings.Contains(action, "price_down") {
		t.Fatalf("no AH must not DOWN, got %s → %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownMegaswordNoDown(t *testing.T) {
	// v8ac: megasword empty + thick book → HOLD (no grant), not ↓ to 7.0M
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	got, action, _, _, notes := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if strings.Contains(action, "price_down") || got < price {
		t.Fatalf("megasword empty: want no DOWN, got %s %d notes=%v", action, got, notes)
	}
}

func TestEmptyIdleAboveMarketGuardThinUnaffected(t *testing.T) {
	if emptyIdleAboveMarketBlocksUp(true, false, false, 7_000_000, 4_000_000, 400_000) {
		t.Fatal("thin must not trigger above-market guard")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(2_000_000, 0, 0, 400_000, 0, 100_000, 400_000, 0)
	if action != floorEscapeActionEmptyIdle || got != 2_100_000 {
		t.Fatalf("thin/no book empty_idle UP preserved: %s %d", action, got)
	}
}
