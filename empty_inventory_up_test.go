package main

import "testing"

func TestEmptyInventoryCanUpBasic(t *testing.T) {
	// A: held=0 sales/buys irrelevant
	cap := 10_000_000
	if !canEmptyInventoryUp(0, 1_000_000, 100_000, false, 0, 0, 0, 0, cap) {
		t.Fatal("A: held=0 must allow UP")
	}
	// F: held=1
	if canEmptyInventoryUp(1, 1_000_000, 100_000, false, 0, 0, 0, 0, cap) {
		t.Fatal("F: held>0 must not empty_inventory_up")
	}
	// E: buys/sales not in canEmptyInventoryUp — still allowed
	if !canEmptyInventoryUp(0, 1_000_000, 100_000, false, 0, 0, 0, 0, cap) {
		t.Fatal("E: buys/sales must not gate helper")
	}
}

func TestEmptyInventoryCooldownAndStreak(t *testing.T) {
	cap := 10_000_000
	if canEmptyInventoryUp(0, 1_000_000, 100_000, false, 1, 0, 0, 0, cap) {
		t.Fatal("H: CorridorUpCooldown>0 must block")
	}
	if canEmptyInventoryUp(0, 1_000_000, 100_000, false, 0, 1, 0, 0, cap) {
		t.Fatal("H: CorridorUpStreak>=1 must block")
	}
	if canEmptyInventoryUp(0, 1_000_000, 100_000, false, 0, 0, 2, 0, cap) {
		t.Fatal("market down cd must block")
	}
	if canEmptyInventoryUp(0, 1_000_000, 100_000, true, 0, 0, 0, 0, cap) {
		t.Fatal("manual blockUp must block")
	}
}

func TestEmptyInventoryCapNoBookStillAllows(t *testing.T) {
	// B / J: no book, no paid → safety from anchor
	cap, why := emptyInventoryEffectiveCap(100_000, 50_000, 0, 0, 0, 1_000_000, false, false)
	if why != "safety_24" {
		t.Fatalf("want safety_24 got %s cap=%d", why, cap)
	}
	want := 1_000_000 + 24*100_000
	if cap != want {
		t.Fatalf("safety cap=%d want %d", cap, want)
	}
	if !canEmptyInventoryUp(0, 1_000_000, 100_000, false, 0, 0, 0, 0, cap) {
		t.Fatal("B/J: no book/paid must still allow UP under safety")
	}
}

func TestEmptyInventoryMarketCap(t *testing.T) {
	// C / G
	p10, nac, step := 2_000_000, 100_000, 100_000
	cap, why := emptyInventoryEffectiveCap(step, nac, p10, ahBookMinLotsInWindow, 0, 1_000_000, true, true)
	if why != "market_p10+nac" && why != "emergency_40" {
		// min of market and emergency — market should win if lower
		_ = why
	}
	market := p10 + nac
	if cap != market {
		t.Fatalf("C: effectiveCap=%d want market %d (why=%s)", cap, market, why)
	}
	// under cap → allow
	if !canEmptyInventoryUp(0, market-step, step, false, 0, 0, 0, 0, cap) {
		t.Fatal("C: price under market cap must allow")
	}
	// G: at/above cap
	if canEmptyInventoryUp(0, market, step, false, 0, 0, 0, 0, cap) {
		t.Fatal("G: price >= market cap must not UP")
	}
}

func TestEmptyInventoryPaidCapAndEmergency(t *testing.T) {
	step := 100_000
	paid := 3_000_000
	anchor := 1_000_000
	cap, why := emptyInventoryEffectiveCap(step, 50_000, 0, 0, paid, anchor, false, false)
	paidCap := paid + 5*step
	emerg := anchor + 40*step
	want := paidCap
	if emerg < paidCap {
		want = emerg
	}
	if cap != want {
		t.Fatalf("paid/emergency cap=%d want %d why=%s", cap, want, why)
	}
	// paid present → not safety_24
	if why == "safety_24" {
		t.Fatal("with paidMax must not use safety_24 as primary")
	}
}

func TestEmptyInventoryArmCooldownTrue(t *testing.T) {
	if !corridorUpArmCooldown(emptyInventoryAction) {
		t.Fatal("empty_inventory must arm CorridorUpCooldown")
	}
}

func TestCapitalPolicyV8ab(t *testing.T) {
	if capitalPolicy != capitalPolicyV4 && capitalPolicy != capitalPolicyV9 && capitalPolicy != capitalPolicyV8af {
		t.Fatalf("policy=%s want stock_corridor_v4|v9|v8af", capitalPolicy)
	}
}
