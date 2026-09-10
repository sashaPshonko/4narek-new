package main

import "testing"

func TestCorridorUpArmCooldown(t *testing.T) {
	skip := []string{
		"corridor_price_up_empty_idle",
		"corridor_price_up_empty_book",
		"corridor_price_up_floor_escape_empty",
		"corridor_price_up_floor_escape_deep_ah",
		"corridor_price_up_floor_escape_down_streak",
		"corridor_price_up_floor_escape_trusted_jump",
		"corridor_price_up_floor",
		"corridor_price_up_market_recovery",
		"corridor_price_up_trusted_ah_min",
	}
	for _, a := range skip {
		if corridorUpArmCooldown(a) {
			t.Fatalf("%s should skip up_cd", a)
		}
	}
	arm := []string{
		"corridor_price_up_deep",
		"corridor_price_up_skim",
		"corridor_price_up_ah_book",
		"corridor_price_up_recover",
		"corridor_price_up_paid",
	}
	for _, a := range arm {
		if !corridorUpArmCooldown(a) {
			t.Fatalf("%s should arm up_cd", a)
		}
	}
	if corridorUpArmCooldown("corridor_hold_band") {
		t.Fatal("hold should not arm")
	}
}
