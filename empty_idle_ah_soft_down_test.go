package main

import (
	"strings"
	"testing"
)

// simEmptyIdleAhCycle mirrors pricing.go AH soft → floor_escape order with A/anti-yoyo flags.
func simEmptyIdleAhCycle(
	price, p10, minAsk, nacenka, n, step, floor int,
	marketDownCD int,
) (newPrice int, action string, feWouldFire bool, nextCD int, notes []string) {
	emptyIdle := isEmptyIdle(0, 0, 0)
	bookOK := n >= ahBookMinLotsInWindow && minAsk > 0
	p10OK := n >= ahBookMinLotsInWindow && p10 > 0
	viaEmpty := emptyIdleAhSoftDownOK(emptyIdle, bookOK, p10OK, price, p10, minAsk, nacenka, n, step)
	newPrice = price
	action = "corridor_hold_recover_stale"
	firedA := false
	if viaEmpty {
		tgt := ahBookSoftDownTarget(p10, minAsk, nacenka, step)
		bookFloor := minAsk + nacenka
		if tgt < bookFloor {
			tgt = bookFloor
		}
		applied := ahBookSoftDownApply(newPrice, tgt, floor, step, 0)
		if applied < newPrice {
			newPrice = applied
			action = "corridor_price_down_ah_book"
			firedA = true
			notes = append(notes, "empty_idle_ah_soft_down")
		}
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

func TestEmptyIdleAhSoftDownThickBookDown(t *testing.T) {
	// 1) emptyIdle + thick + ≫p10 → A DOWN, no emptyIdle UP
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	got, action, feFire, _, notes := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if action != "corridor_price_down_ah_book" {
		t.Fatalf("action=%s want corridor_price_down_ah_book notes=%v", action, notes)
	}
	want := price - ahBookMaxRaiseSteps*step
	if got != want {
		t.Fatalf("price %d→%d want %d", price, got, want)
	}
	if feFire {
		t.Fatal("floor_escape Must not WouldFire after already_moved from A DOWN")
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "empty_idle_ah_soft_down") {
		t.Fatalf("missing empty_idle_ah_soft_down note: %v", notes)
	}
}

func TestEmptyIdleAhSoftDownThinBookNoA(t *testing.T) {
	// 2) emptyIdle + thin → A не срабатывает
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 12, 100_000, 400_000
	if emptyIdleAhSoftDownOK(true, false, false, price, p10, minAsk, nac, n, step) {
		t.Fatal("thin must not A")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if action != floorEscapeActionEmptyIdle || got != price+step {
		t.Fatalf("thin → empty_idle UP, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownNoBookNoA(t *testing.T) {
	// 3) emptyIdle + no book → A не срабатывает
	price, step, floor := 7_200_004, 100_000, 400_000
	if emptyIdleAhSoftDownOK(true, false, false, price, 0, 0, 400_000, 0, step) {
		t.Fatal("no book must not A")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(price, 0, 0, 400_000, 0, step, floor, 0)
	if action != floorEscapeActionEmptyIdle || got != price+step {
		t.Fatalf("no book → empty_idle UP, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownTreasuryEmptyStillDown(t *testing.T) {
	// 4) treasury_empty suppress UP, but A DOWN still allowed (treasury does not gate A)
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	if !emptyIdleAhSoftDownOK(true, true, true, price, p10, minAsk, nac, n, step) {
		t.Fatal("A must open under treasury-like empty_idle")
	}
	got, action, feFire, _, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if action != "corridor_price_down_ah_book" || got >= price || feFire {
		t.Fatalf("treasury path: want A DOWN, got %s %d fe=%v", action, got, feFire)
	}
	// suppressEmptyIdle only blocks recovery UP — A is independent.
	feAlone := evalFloorEscape(price, step, floor, 0, 0, 0, 0, 0, 0, 0, false, false, false, 0)
	if !feAlone.WouldFire {
		t.Fatal("precondition: without A, empty_idle would UP")
	}
}

func TestEmptyIdleAhSoftDownCooldownBlocksRecoveryUp(t *testing.T) {
	// 5) После A → 3 следующих цикла emptyIdle/trusted/MR UP заблокированы
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	_, _, _, cd, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if cd != emptyIdleMarketDownCooldownCycles {
		t.Fatalf("after A cd=%d want %d", cd, emptyIdleMarketDownCooldownCycles)
	}
	// Thin book next cycles: without CD would UP; with CD must HOLD
	thinPrice := price - ahBookMaxRaiseSteps*step
	for i := 0; i < emptyIdleMarketDownCooldownCycles; i++ {
		got, action, _, next, notes := simEmptyIdleAhCycle(thinPrice, 0, 0, nac, 0, step, floor, cd)
		if strings.Contains(action, "price_up") {
			t.Fatalf("cycle after A #%d must not UP: %s notes=%v", i+1, action, notes)
		}
		if got != thinPrice {
			t.Fatalf("cycle #%d price changed %d→%d under CD", i+1, thinPrice, got)
		}
		cd = next
	}
	if cd != 0 {
		t.Fatalf("after 3 ticks cd=%d want 0", cd)
	}
	// CD expired + thin → empty_idle UP resumes
	got, action, _, _, _ := simEmptyIdleAhCycle(thinPrice, 0, 0, nac, 0, step, floor, 0)
	if action != floorEscapeActionEmptyIdle || got != thinPrice+step {
		t.Fatalf("after CD: want empty_idle UP, got %s %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownAboveMarketBlocksUp(t *testing.T) {
	// 6) После A при fresh bearish book → UP не происходит (above-market guard)
	price, p10, minAsk, nac, n, step, floor := 7_000_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	if !emptyIdleAboveMarketBlocksUp(true, true, true, price, p10, nac) {
		t.Fatal("sell > p10+nac must block idle UP")
	}
	// Even with CD=0, above-market blocks FE UP if A doesn't fire (price not ≫ enough for soft slack?)
	// Use price that is > p10+nac but maybe inside soft slack so A might not fire.
	// 4.37M+0.4M+0.2M = 4.97M; price 5.0M is > p10+nac but may not clear 2*step slack from p10+nac... 
	// 4.37M+0.4M+2*0.1M = 4.97M; 5.0M > 4.97M → A would also fire.
	// Use CD=0 and price after A still ≫: A fires again OR above-market blocks.
	got, action, _, _, notes := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	if strings.Contains(action, "price_up") {
		t.Fatalf("fresh bearish must not UP: %s %v", action, notes)
	}
	if got > price {
		t.Fatalf("price rose %d→%d", price, got)
	}
}

func TestEmptyIdleAhSoftDownDoesNotBlockOrdinaryDownGates(t *testing.T) {
	// 7) После A обычный DOWN pipeline (DOI/fill/ghost gates) не ломается cooldown'ом
	if doiCoverIntensity(5, 1) != "soft" || doiCoverIntensity(3, 1) != "over" {
		t.Fatal("DOI mapping changed")
	}
	if !allowHardDown(0, 0, 0.6) {
		t.Fatal("idle hard-down must stay allowed")
	}
	// ghost still needs held>0 — CD не открывает ghost на held=0
	if ghostPriceDownOK(0, 0, 99) {
		t.Fatal("ghost must stay blocked at held=0")
	}
	// applyDown empty_idle belt: ordinary path still gated
	if !isEmptyIdle(0, 0, 0) {
		t.Fatal("empty")
	}
}

func TestOrdinaryHeldAhSoftDownUnchanged(t *testing.T) {
	// 8) Ordinary held>0 fresh AH soft-down не изменился
	held, price, p10, minAsk, nac, n, step := 1, 10_300_002, 2_799_999, 800_000, 300_000, 50, 100_000
	if isEmptyIdle(held, 0, 0) {
		t.Fatal("held>0 not empty_idle")
	}
	if emptyIdleAhSoftDownOK(false, true, true, price, p10, minAsk, nac, n, step) {
		t.Fatal("A branch must stay off when not empty_idle")
	}
	if !shouldSoftDownFromAhBook(price, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("ordinary soft-↓ must fire")
	}
	softDownFromBook := !isEmptyIdle(held, 0, 0) &&
		shouldSoftDownFromAhBook(price, p10, minAsk, nac, n, step, false, false, held)
	if !softDownFromBook {
		t.Fatal("softDownFromBook gate")
	}
	tgt := ahBookSoftDownApply(price, ahBookSoftDownTarget(p10, minAsk, nac, step), 300_000, step, held)
	if tgt >= price {
		t.Fatalf("want DOWN, got %d", tgt)
	}
}

func TestEmptyIdleNoBookNoGhostDown(t *testing.T) {
	// 9) held=0 + no book → никакого ghost DOWN
	if ghostPriceDownOK(0, 0, 99) {
		t.Fatal("ghost must not OK at held=0")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(7_200_004, 0, 0, 400_000, 0, 100_000, 400_000, 0)
	if strings.Contains(action, "price_down") {
		t.Fatalf("no AH must not DOWN, got %s → %d", action, got)
	}
}

func TestEmptyIdleAhSoftDownMegaswordReplay(t *testing.T) {
	// megasword @12:55: old UP 7.2→7.3; new DOWN 7.2→7.0
	price, p10, minAsk, nac, n, step, floor := 7_200_004, 4_374_999, 3_237_499, 400_000, 48, 100_000, 400_000
	got, action, feFire, cd, _ := simEmptyIdleAhCycle(price, p10, minAsk, nac, n, step, floor, 0)
	want := 7_000_004
	if action != "corridor_price_down_ah_book" || got != want || feFire {
		t.Fatalf("megasword: action=%s %d→%d fe=%v want DOWN to %d", action, price, got, feFire, want)
	}
	// Next thin cycle must not immediately UP
	got2, action2, _, _, _ := simEmptyIdleAhCycle(got, 0, 0, nac, 0, step, floor, cd)
	if strings.Contains(action2, "price_up") || got2 != got {
		t.Fatalf("next thin under CD: want HOLD %d, got %s %d", got, action2, got2)
	}
}

func TestEmptyIdleAboveMarketGuardThinUnaffected(t *testing.T) {
	// thin/no book: above-market guard off → empty_idle UP preserved
	if emptyIdleAboveMarketBlocksUp(true, false, false, 7_000_000, 4_000_000, 400_000) {
		t.Fatal("thin must not trigger above-market guard")
	}
	got, action, _, _, _ := simEmptyIdleAhCycle(2_000_000, 0, 0, 400_000, 0, 100_000, 400_000, 0)
	if action != floorEscapeActionEmptyIdle || got != 2_100_000 {
		t.Fatalf("thin/no book empty_idle UP preserved: %s %d", action, got)
	}
}
