package main

import (
	"path/filepath"
	"testing"
	"time"
)

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
