package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestFleetBanPersistAndMerge(t *testing.T) {
	oldBots := clientBannedBots
	oldOwners := clientClanOwners
	oldPersist := persistedBannedBots
	oldOwnerPersist := persistedClanOwnerBans
	oldClients := clients
	oldOrchAn := clientOrchestratorAnarchy
	oldRoster := fleetNickRoster
	t.Cleanup(func() {
		clientBannedBots = oldBots
		clientClanOwners = oldOwners
		persistedBannedBots = oldPersist
		persistedClanOwnerBans = oldOwnerPersist
		clients = oldClients
		clientOrchestratorAnarchy = oldOrchAn
		fleetNickRoster = oldRoster
	})

	clientBannedBots = make(map[*websocket.Conn][]bannedBotView)
	clientClanOwners = make(map[*websocket.Conn][]clanOwnerView)
	clients = make(map[*websocket.Conn]bool)
	clientOrchestratorAnarchy = make(map[*websocket.Conn]int)
	skipFleetRosterReload = true
	t.Cleanup(func() { skipFleetRosterReload = false })
	fleetNickRoster = funauthRoster{
		502: {"tablydait13": {}},
		503: {"krupkaobod15": {}},
		504: {"eblivi1_r0t7": {}},
	}

	persistedBannedBots = map[string]bannedBotView{
		"tablydait13": {Username: "tablydait13", Anarchy: 502, BannedAt: "2026-08-01T10:00:00Z"},
		"oldghost":    {Username: "oldghost", Anarchy: 502, BannedAt: "2026-08-01T09:00:00Z"},
	}
	persistedClanOwnerBans = map[string]clanOwnerView{
		"eblivi1_r0t7": {
			Username:  "eblivi1_r0t7",
			Anarchy:   504,
			Status:    "banned",
			Banned:    true,
			BannedAt:  "2026-08-01T11:00:00Z",
			Reason:    "вы забанены",
			CheckedAt: "2026-08-01T11:00:00Z",
		},
	}

	ws := &websocket.Conn{}
	clients[ws] = true
	setClientOrchestratorAnarchy(ws, 503)
	setClientBannedBots(ws, []bannedBotView{
		{Username: "krupkaobod15", Anarchy: 503, GoType: "armor", BannedAt: "2026-08-06T10:00:00Z"},
	})
	setClientClanOwners(ws, []clanOwnerView{
		{Username: "eblivi1_r0t7", Anarchy: 504, Status: "error", CheckedAt: "2026-08-06T12:00:00Z"},
		{Username: "vorishkaok14", Anarchy: 507, Status: "ok", CheckedAt: "2026-08-06T12:00:00Z"},
	})

	out := buildFleetOverview()
	if out.Total != 1 {
		t.Fatalf("total=%d want 1 (only an503 running: krupkaobod15)", out.Total)
	}
	var live bannedBotView
	for _, b := range out.Banned {
		if b.Username == "krupkaobod15" {
			live = b
		}
	}
	if live.Username != "krupkaobod15" {
		t.Fatalf("live=%+v", live)
	}

	if len(out.ClanOwners) != 0 {
		t.Fatalf("owners filtered (504 not running): %+v", out.ClanOwners)
	}
	if _, ok := persistedBannedBots["oldghost"]; ok {
		t.Fatal("oldghost should be pruned after leaving roster")
	}
	if len(persistedBannedBots) != 2 {
		t.Fatalf("persisted bots=%d want 2 (tablydait13 + live krupkaobod15)", len(persistedBannedBots))
	}
}

func TestPrunePersistedBansNotInRoster(t *testing.T) {
	oldPersist := persistedBannedBots
	oldOwners := persistedClanOwnerBans
	t.Cleanup(func() {
		persistedBannedBots = oldPersist
		persistedClanOwnerBans = oldOwners
	})
	persistedBannedBots = map[string]bannedBotView{
		"keepme":   {Username: "keepme", Anarchy: 503},
		"oldghost": {Username: "oldghost", Anarchy: 503},
	}
	persistedClanOwnerBans = map[string]clanOwnerView{
		"goneowner":    {Username: "goneowner", Anarchy: 504, Status: "banned"},
		"eblivi1_r0t7": {Username: "eblivi1_r0t7", Anarchy: 504, Status: "banned"},
	}
	prunePersistedBansNotInRoster(funauthRoster{
		503: {"keepme": {}},
		504: {"eblivi1_r0t7": {}},
	})
	if _, ok := persistedBannedBots["oldghost"]; ok {
		t.Fatal("expected oldghost pruned")
	}
	if _, ok := persistedBannedBots["keepme"]; !ok {
		t.Fatal("expected keepme")
	}
	if _, ok := persistedClanOwnerBans["goneowner"]; ok {
		t.Fatal("expected goneowner pruned")
	}
}
