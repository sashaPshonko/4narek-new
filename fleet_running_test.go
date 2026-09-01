package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFleetRunningNicksFromBotsJSON(t *testing.T) {
	dir := t.TempDir()
	bots := filepath.Join(dir, "bots")
	if err := os.Mkdir(bots, 0o755); err != nil {
		t.Fatal(err)
	}
	botJSON := `[
	  {"username": "tablydait13", "anarchy": 502},
	  {"username": "gorbtikphon12", "anarchy": 502}
	]`
	if err := os.WriteFile(filepath.Join(bots, "502b.json"), []byte(botJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	owners := `{
	  "502": {"username": "bubkagub9", "anarchy": 502},
	  "503": {"username": "kokos_555117", "anarchy": 503}
	}`
	if err := os.WriteFile(filepath.Join(dir, "clan-owners.json"), []byte(owners), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FLEET_BOTS_DIR", bots)
	t.Setenv("FLEET_OWNERS_FILE", filepath.Join(dir, "clan-owners.json"))

	r := loadFleetRunningNicks()
	if !r.nickOnAnarchy(502, "tablydait13") || !r.nickOnAnarchy(502, "gorbtikphon12") {
		t.Fatalf("bots missing: %+v", r)
	}
	if !r.nickOnAnarchy(502, "bubkagub9") {
		t.Fatalf("owner missing: %+v", r)
	}
	if r.nickOnAnarchy(502, "rosteronly_old") {
		t.Fatal("should not include nicks that are only in funauth roster")
	}
	if !r.nickOnAnarchy(503, "kokos_555117") {
		t.Fatal("503 owner")
	}
}

func TestLoadClanOwnersJSONIgnoresMyNickString(t *testing.T) {
	dir := t.TempDir()
	owners := `{
	  "502": {"username": "syrnikbomb16", "anarchy": 502},
	  "506": {"username": "klanvshlem13", "anarchy": 506},
	  "myNick": "nebotovodt4n8"
	}`
	path := filepath.Join(dir, "clan-owners.json")
	if err := os.WriteFile(path, []byte(owners), 0o644); err != nil {
		t.Fatal(err)
	}
	out := make(funauthRoster)
	loadFleetClanOwnersJSON(path, out)
	if !out.nickOnAnarchy(502, "syrnikbomb16") || !out.nickOnAnarchy(506, "klanvshlem13") {
		t.Fatalf("owners not loaded (myNick broke parse?): %+v", out)
	}
}

func TestFilterClanOwnersShowsBanWithoutOrchestrator(t *testing.T) {
	roster := funauthRoster{504: {"eblivi1_r0t7": {}}}
	running := map[int]struct{}{503: {}}
	out := filterClanOwnersForFleet([]clanOwnerView{
		{Username: "eblivi1_r0t7", Anarchy: 504, Status: "banned", Banned: true},
		{Username: "kokos_555117", Anarchy: 503, Status: "ok"},
		{Username: "old_owner", Anarchy: 504, Status: "banned", Banned: true},
	}, running, roster)
	if len(out) != 1 || out[0].Username != "eblivi1_r0t7" {
		t.Fatalf("got %+v", out)
	}
}

func TestFilterBannedIgnoresRosterOnlyNicks(t *testing.T) {
	runningNicks := funauthRoster{
		502: {"tablydait13": {}, "gorbtikphon12": {}, "kventikasha12": {}, "bubkagub9": {}},
	}
	running := map[int]struct{}{502: {}}
	out := filterBannedForFleet([]bannedBotView{
		{Username: "tablydait13", Anarchy: 502},
		{Username: "bubkagub9", Anarchy: 502},
		{Username: "old_roster_nick", Anarchy: 502},
	}, running, runningNicks)
	if len(out) != 2 {
		t.Fatalf("got %+v", out)
	}
}
