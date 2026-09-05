package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLookupMCNickSOCKS(t *testing.T) {
	root := t.TempDir()
	bots := filepath.Join(root, "bots")
	if err := os.Mkdir(bots, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bots, "502b.json"), []byte(`[
  {"username":"uglevik_q3","ip":"502-1","anarchy":502}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ip.json"), []byte(`{
  "502-1": "socks5://u:p@10.1.2.3:50101"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clan-owners.json"), []byte(`{
  "502": {"username":"tormozKisl","ip":"502","anarchy":502}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owner-ip.json"), []byte(`{
  "502": "socks5://u:p@10.9.9.9:50101"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_BOTS_DIR", bots)

	got := lookupMCNickSOCKS("uglevik_q3")
	if got != "socks5://u:p@10.1.2.3:50101" {
		t.Fatalf("bot socks: %q", got)
	}
	got = lookupMCNickSOCKS("tormozKisl")
	if got != "socks5://u:p@10.9.9.9:50101" {
		t.Fatalf("owner socks: %q", got)
	}
	if lookupMCNickSOCKS("nobody") != "" {
		t.Fatal("unknown nick")
	}
}

func TestPickBindSOCKSUsesCache(t *testing.T) {
	prev := funauthBindProxies
	t.Cleanup(func() { funauthBindProxies = prev })

	bad := "socks5://u:p@10.0.0.1:50101"
	good := "socks5://u:p@10.0.0.2:50101"
	funauthBindProxies = &funauthBindProxyPool{
		path:  filepath.Join(t.TempDir(), "bind_proxies.json"),
		cache: make(map[string]bindProxyCacheEntry),
		items: []funauthBindProxy{
			{ID: "a", URL: bad},
			{ID: "b", URL: good},
		},
	}
	funauthBindProxies.remember(bad, false, "NotAllowed")
	funauthBindProxies.remember(good, true, "")
	// свежий TTL
	funauthBindProxies.cache[bad] = bindProxyCacheEntry{ok: false, err: "NotAllowed", checkedAt: time.Now()}
	funauthBindProxies.cache[good] = bindProxyCacheEntry{ok: true, checkedAt: time.Now()}

	got := pickFarmLoginSOCKS()
	if got != good {
		t.Fatalf("got %q want %q", got, good)
	}
}

func TestNormalizeBindProxyURL(t *testing.T) {
	got, err := normalizeBindProxyURL("privetnitron:pass@1.2.3.4:50101")
	if err != nil {
		t.Fatal(err)
	}
	if got != "socks5://privetnitron:pass@1.2.3.4:50101" {
		t.Fatalf("got %q", got)
	}
}

func TestSocks5DialerParsesAuth(t *testing.T) {
	d, err := socks5ContextDialer("socks5://privetnitron:secret@50.114.27.199:50101")
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("nil dialer")
	}
}
