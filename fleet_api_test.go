package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestFleetOverviewGroupsByAnarchy(t *testing.T) {
	old := clientBannedBots
	clientBannedBots = make(map[*websocket.Conn][]bannedBotView)
	t.Cleanup(func() { clientBannedBots = old })

	wsA := &websocket.Conn{}
	wsB := &websocket.Conn{}
	setClientBannedBots(wsA, []bannedBotView{
		{Username: "botA", Anarchy: 503, GoType: "boots", BannedAt: "2026-08-06T10:00:00Z"},
		{Username: "botB", Anarchy: 503, Role: "seller"},
	})
	setClientBannedBots(wsB, []bannedBotView{
		{Username: "botC", Anarchy: 510},
		{Username: "botA", Anarchy: 503, BannedAt: "2026-08-06T12:00:00Z", Reason: "newer"},
	})

	out := buildFleetOverview()
	if !out.OK {
		t.Fatal("ok=false")
	}
	if out.Total != 3 {
		t.Fatalf("total=%d want 3", out.Total)
	}
	if len(out.Anarchies) != 2 {
		t.Fatalf("anarchies=%d want 2", len(out.Anarchies))
	}
	if out.Anarchies[0].Anarchy != 503 || out.Anarchies[0].Count != 2 {
		t.Fatalf("first group = %+v", out.Anarchies[0])
	}
	var botA bannedBotView
	for _, b := range out.Banned {
		if b.Username == "botA" {
			botA = b
		}
	}
	if botA.Reason != "newer" || botA.BannedAt != "2026-08-06T12:00:00Z" {
		t.Fatalf("dedupe prefer newer: %+v", botA)
	}
}

func TestFleetHTTPOverview(t *testing.T) {
	mux := http.NewServeMux()
	registerFleetHTTP(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/fleet/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != true {
		t.Fatalf("payload=%v", payload)
	}

	res2, err := http.Get(srv.URL + "/fleet/")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatalf("page status %d", res2.StatusCode)
	}
}
