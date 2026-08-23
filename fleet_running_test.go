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
