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
	t.Cleanup(func() {
		clientBannedBots = oldBots
		clientClanOwners = oldOwners
		persistedBannedBots = oldPersist
		persistedClanOwnerBans = oldOwnerPersist
	})

	clientBannedBots = make(map[*websocket.Conn][]bannedBotView)
	clientClanOwners = make(map[*websocket.Conn][]clanOwnerView)
	persistedBannedBots = map[string]bannedBotView{
		"oldbot": {Username: "oldbot", Anarchy: 502, BannedAt: "2026-08-01T10:00:00Z"},
	}
	persistedClanOwnerBans = map[string]clanOwnerView{
		"owner504": {
			Username:  "owner504",
			Anarchy:   504,
			Status:    "banned",
			Banned:    true,
			BannedAt:  "2026-08-01T11:00:00Z",
			Reason:    "вы забанены",
			CheckedAt: "2026-08-01T11:00:00Z",
		},
	}

	ws := &websocket.Conn{}
	setClientBannedBots(ws, []bannedBotView{
		{Username: "livebot", Anarchy: 503, GoType: "boots", BannedAt: "2026-08-06T10:00:00Z"},
	})
	setClientClanOwners(ws, []clanOwnerView{
		{Username: "owner504", Anarchy: 504, Status: "error", CheckedAt: "2026-08-06T12:00:00Z"},
		{Username: "owner507", Anarchy: 507, Status: "ok", CheckedAt: "2026-08-06T12:00:00Z"},
	})

	out := buildFleetOverview()
	if out.Total != 2 {
		t.Fatalf("total=%d want 2 (persisted oldbot + live livebot)", out.Total)
	}
	var oldbot, livebot bannedBotView
	for _, b := range out.Banned {
		switch b.Username {
		case "oldbot":
			oldbot = b
		case "livebot":
			livebot = b
		}
	}
	if oldbot.Source != "persisted" {
		t.Fatalf("oldbot source=%q want persisted", oldbot.Source)
	}
	if livebot.Username != "livebot" {
		t.Fatalf("livebot=%+v", livebot)
	}

	var owner504 clanOwnerView
	for _, o := range out.ClanOwners {
		if o.Username == "owner504" {
			owner504 = o
		}
	}
	if owner504.Status != "banned" || !owner504.Banned {
		t.Fatalf("owner504 should stay banned on error, got %+v", owner504)
	}
	if len(persistedBannedBots) != 2 {
		t.Fatalf("persisted bots=%d want 2", len(persistedBannedBots))
	}
}
