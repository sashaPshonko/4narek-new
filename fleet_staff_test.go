package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaffCheckAndDeskChat(t *testing.T) {
	staffDeskPersistPath = t.TempDir() + "/staff_desk.json"
	staffDeskMu.Lock()
	staffChecksMem = nil
	deskChatsMem = nil
	staffDeskMu.Unlock()
	t.Cleanup(func() {
		staffDeskMu.Lock()
		staffChecksMem = nil
		deskChatsMem = nil
		staffDeskMu.Unlock()
	})

	ingestStaffCheck("nickOne", 504, "вызваны на проверку читов")
	ingestDeskChat("nickOne", "suckfuntime", "открой инвентарь", 504)
	ingestDeskChat("nickOne", "suckfuntime", "кинь /anydesk", 504)
	endStaffCheck("nickOne")
	ingestStaffCheck("nickOne", 504, "ещё раз")

	out := listStaffChecksForAPI()
	if len(out) < 2 {
		t.Fatalf("checks=%d want >=2", len(out))
	}
	live := 0
	var latest staffCheckView
	for _, c := range out {
		if c.ID == "loose" {
			continue
		}
		if c.Status == "live" {
			live++
			latest = c
		}
	}
	if live != 1 {
		t.Fatalf("live=%d want 1", live)
	}
	if latest.Username != "nickOne" {
		t.Fatalf("latest=%+v", latest)
	}
	// чат привязан к первой (тогда live) сессии
	first := out[len(out)-1]
	if first.Status == "live" {
		first = out[1]
	}
	var withChat staffCheckView
	for _, c := range out {
		if len(c.Chat) >= 2 {
			withChat = c
			break
		}
	}
	if len(withChat.Chat) < 2 {
		t.Fatalf("chat not attached: %+v", out)
	}
	if withChat.Chat[0].Text != "открой инвентарь" {
		t.Fatalf("chat0=%q", withChat.Chat[0].Text)
	}
}

func TestStaffDeskHTTP(t *testing.T) {
	staffDeskPersistPath = t.TempDir() + "/staff_desk.json"
	staffDeskMu.Lock()
	staffChecksMem = nil
	deskChatsMem = nil
	staffDeskMu.Unlock()

	mux := http.NewServeMux()
	registerFleetHTTP(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/fleet/api/staff-check", "application/json", strings.NewReader(
		`{"username":"httpNick","anarchy":502,"reason":"проверка"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res, err = http.Post(srv.URL+"/fleet/api/desk-chat", "application/json", strings.NewReader(
		`{"username":"httpNick","from":"tlauncher","text":"hi","anarchy":502}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	ov, err := http.Get(srv.URL + "/fleet/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer ov.Body.Close()
	var body fleetOverview
	if err := json.NewDecoder(ov.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.StaffCheckLive < 1 {
		t.Fatalf("live=%d overview=%+v", body.StaffCheckLive, body.StaffChecks)
	}
	found := false
	for _, c := range body.StaffChecks {
		if strings.EqualFold(c.Username, "httpNick") && len(c.Chat) == 1 && c.Chat[0].Text == "hi" {
			found = true
		}
	}
	if !found {
		t.Fatalf("http nick missing in %+v", body.StaffChecks)
	}
}
