package main

import "testing"

func TestFilterBannedForFleet(t *testing.T) {
	roster := funauthRoster{
		503: {"krupkaobod15": {}, "kokos_555117": {}},
		504: {"nozhichok14": {}, "eblivi1_r0t7": {}},
	}
	running := map[int]struct{}{503: {}, 504: {}}

	all := []bannedBotView{
		{Username: "krupkaobod15", Anarchy: 503},
		{Username: "oldnick", Anarchy: 503},
		{Username: "depression13", Anarchy: 504},
		{Username: "botC", Anarchy: 510},
	}
	out := filterBannedForFleet(all, running, roster)
	if len(out) != 1 {
		t.Fatalf("visible=%d want 1 (only krupkaobod15): %+v", len(out), out)
	}
	if out[0].Username != "krupkaobod15" {
		t.Fatalf("got %+v", out[0])
	}
}

func TestFilterBannedHidesStoppedAnarchy(t *testing.T) {
	roster := funauthRoster{
		503: {"krupkaobod15": {}},
		510: {"pokavse16": {}},
	}
	running := map[int]struct{}{503: {}}

	out := filterBannedForFleet([]bannedBotView{
		{Username: "pokavse16", Anarchy: 510},
		{Username: "krupkaobod15", Anarchy: 503},
	}, running, roster)
	if len(out) != 1 || out[0].Username != "krupkaobod15" {
		t.Fatalf("got %+v", out)
	}
}
