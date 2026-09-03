package main

import (
	"testing"
)

func TestIsFleetSellerLocked(t *testing.T) {
	old := fleetNickRoster
	fleetNickRoster = funauthRoster{
		503: {"rebr0_tv3": {}},
	}
	t.Cleanup(func() { fleetNickRoster = old })
	if !isFleetSellerLocked("rebr0_tv3") {
		t.Fatal("own nick")
	}
	if !isFleetSellerLocked("Rebr0_Tv3") {
		t.Fatal("case")
	}
	if isFleetSellerLocked("Beyermy") {
		t.Fatal("competitor")
	}
	if isFleetSellerLocked("") {
		t.Fatal("empty is unknown, not fleet")
	}
}
