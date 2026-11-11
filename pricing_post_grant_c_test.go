package main

import "testing"

func TestPostGrantCBlockDownMildLive(t *testing.T) {
	// sales≥2 ∧ excess==+1 → block
	if !postGrantCBlockDown(6, 5, 2, 0, 0) {
		t.Fatal("mild+live must block")
	}
	if !postGrantCBlockDown(6, 5, 5, 3, 0) {
		t.Fatal("mild+live sales=5 must block")
	}
	// excess≥2 → do not block (gray / current logic)
	if postGrantCBlockDown(7, 5, 3, 0, 0) {
		t.Fatal("sales≥2 excess+2 must PASS")
	}
	// sales==1 → do not block
	if postGrantCBlockDown(6, 5, 1, 0, 0) {
		t.Fatal("sales=1 must PASS")
	}
}

func TestPostGrantCBlockDownZeroOneMild(t *testing.T) {
	if !postGrantCBlockDown(6, 5, 0, 0, 1) {
		t.Fatal("zero1 mild must block")
	}
	// buys>0 → not this HOLD filter
	if postGrantCBlockDown(6, 5, 0, 1, 1) {
		t.Fatal("zero1 with buys must not use mild HOLD filter")
	}
	// streak≥2 → not block (candidate path)
	if postGrantCBlockDown(6, 5, 0, 0, 2) {
		t.Fatal("streak≥2 must not block via C HOLD")
	}
	// depth≥2 → not this mild filter
	if postGrantCBlockDown(7, 5, 0, 0, 1) {
		t.Fatal("zero1 depth+2 must PASS C HOLD filter")
	}
}
