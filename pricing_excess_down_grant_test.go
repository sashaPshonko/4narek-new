package main

import "testing"

func TestAutomaticDownAllowed(t *testing.T) {
	if automaticDownAllowed(0, 5) {
		t.Fatal("held=0 ≤ hi=5 → no grant")
	}
	if automaticDownAllowed(5, 5) {
		t.Fatal("held=5 ≤ hi → no grant")
	}
	if !automaticDownAllowed(6, 5) {
		t.Fatal("held=6 > hi → grant")
	}
}

func TestGrantBlocksDOIOverAtNoExcess(t *testing.T) {
	// held=3 hi=5 DOI over — grant closed; C not involved
	if doiCoverIntensity(3, 1) != "over" {
		t.Fatal("precondition")
	}
	if automaticDownAllowed(3, 5) {
		t.Fatal("no grant")
	}
}
