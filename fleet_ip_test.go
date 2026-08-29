package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeProxyHost(t *testing.T) {
	cases := map[string]string{
		"socks5://privetnitron:jacb9eSh7e@85.120.128.204:50101": "85.120.128.204",
		"85.120.128.204:50101": "85.120.128.204",
		"  50.114.27.152  ":    "50.114.27.152",
		"50.114.27.152":        "50.114.27.152",
	}
	for in, want := range cases {
		if got := normalizeProxyHost(in); got != want {
			t.Fatalf("%q → %q want %q", in, got, want)
		}
	}
}

func TestBannedIPLookupAndMark(t *testing.T) {
	old := persistedBannedIPs
	persistedBannedIPs = make(map[string]bannedIPView)
	t.Cleanup(func() { persistedBannedIPs = old })

	setBannedIP("socks5://u:p@85.120.131.5:50101", true, "owner 506", "kokos", "manual")
	got := lookupBannedIP("85.120.131.5")
	if !got.Banned || !got.Known || got.IP != "85.120.131.5" {
		t.Fatalf("lookup=%+v", got)
	}
	unknown := lookupBannedIP("1.2.3.4")
	if unknown.Banned || unknown.Known {
		t.Fatalf("unknown=%+v", unknown)
	}
}

func TestBannedIPHTTP(t *testing.T) {
	old := persistedBannedIPs
	persistedBannedIPs = make(map[string]bannedIPView)
	t.Cleanup(func() { persistedBannedIPs = old })

	mux := http.NewServeMux()
	registerFleetHTTP(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{"ip":"50.114.27.152","banned":true,"note":"502-3"}`
	res, err := http.Post(srv.URL+"/api/banned-ip", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	get, err := http.Get(srv.URL + "/fleet/api/ip?ip=50.114.27.152")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var payload struct {
		OK     bool         `json:"ok"`
		Result bannedIPView `json:"result"`
	}
	if err := json.NewDecoder(get.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Result.Banned {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestMarkAssignedProxiesBanned(t *testing.T) {
	old := persistedBannedIPs
	persistedBannedIPs = make(map[string]bannedIPView)
	t.Cleanup(func() { persistedBannedIPs = old })

	dir := t.TempDir()
	bots := filepath.Join(dir, "bots")
	if err := os.MkdirAll(bots, 0o755); err != nil {
		t.Fatal(err)
	}
	ipJSON := `{
		"502-1": "socks5://u:p@212.236.230.140:50101",
		"506-1": "socks5://u:p@85.120.128.204:50101"
	}`
	if err := os.WriteFile(filepath.Join(dir, "ip.json"), []byte(ipJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_BOTS_DIR", bots)
	n := markAssignedProxiesBanned()
	if n < 2 {
		t.Fatalf("marked=%d", n)
	}
	if !lookupBannedIP("212.236.230.140").Banned {
		t.Fatal("expected 212.236.230.140 banned")
	}
}
