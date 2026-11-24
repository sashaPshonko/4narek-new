package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"
)

func TestPickForAnarchyBindOneToOne(t *testing.T) {
	p := newFunauthPool()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}, ready: true, api: &tg.Client{}},
		"tg2": {meta: funauthAccountMeta{ID: "tg2", Phone: "+2"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{
		"nick_a": "tg1",
	}

	acc, diag := p.pickForAnarchyBindDiag("nick_b", 503, nil)
	if acc == nil || acc.meta.ID != "tg2" {
		t.Fatalf("expected free tg2, got %v", acc)
	}
	if diag.Busy != 1 {
		t.Fatalf("busy=%d want 1", diag.Busy)
	}

	acc, _ = p.pickForAnarchyBindDiag("nick_a", 503, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected tg1 for mapped nick, got %v", acc)
	}

	p.nicks["nick_b"] = "tg2"
	acc, diag = p.pickForAnarchyBindDiag("nick_c", 503, nil)
	if acc != nil {
		t.Fatalf("expected nil when all busy, got %s", acc.meta.ID)
	}
	if diag.Busy != 2 {
		t.Fatalf("busy=%d want 2", diag.Busy)
	}
}

func TestSyncAccountRosterFullOneToOne(t *testing.T) {
	p := newFunauthPool()
	p.dir = t.TempDir()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1"}},
	}
	p.nicks = map[string]string{"a": "tg1"}

	if p.syncAccountRosterFull("tg1") {
		t.Fatal("owner-only bind should not mark full")
	}
	if p.accounts["tg1"].meta.Full {
		t.Fatal("meta.Full set for owner")
	}
}

func TestPickReusesOwnerTGForNextOwner(t *testing.T) {
	root := t.TempDir()
	owners := filepath.Join(root, "clan-owners.json")
	if err := os.WriteFile(owners, []byte(`{"502":{"username":"newOwner","anarchy":502}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_OWNERS_FILE", owners)
	t.Setenv("FLEET_BOTS_DIR", filepath.Join(root, "bots"))
	_ = os.Mkdir(filepath.Join(root, "bots"), 0o755)

	p := newFunauthPool()
	p.accounts = map[string]*funauthAccount{
		"tg1": {
			meta:  funauthAccountMeta{ID: "tg1", Phone: "+1", Anarchy: 502},
			ready: true,
			api:   &tg.Client{},
		},
	}
	p.nicks = map[string]string{"oldOwner": "tg1"}

	acc, diag := p.pickForAnarchyBindDiag("newOwner", 502, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected reuse tg1, got %v busy=%d full=%d", acc, diag.Busy, diag.Full)
	}
}

// Один TG — одна бинд-задача: пока tg1 занят ником a, другой ник его не получает (раньше три бинда шли
// параллельно на одном TG и воровали ответы FunAuthBot друг у друга).
func TestPickClaimsAccountUntilReleased(t *testing.T) {
	p := newFunauthPool()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{}

	a, _ := p.pickForAnarchyBindDiag("nick_a", 221, nil)
	if a == nil || a.meta.ID != "tg1" {
		t.Fatalf("nick_a должен получить tg1, got %v", a)
	}
	b, diag := p.pickForAnarchyBindDiag("nick_b", 221, nil)
	if b != nil {
		t.Fatalf("tg1 занят nick_a, а выдан nick_b")
	}
	if diag.Busy != 1 {
		t.Fatalf("busy=%d want 1 (ждать, а не no_accounts)", diag.Busy)
	}
	p.releaseClaim(a)
	b, _ = p.pickForAnarchyBindDiag("nick_b", 221, nil)
	if b == nil || b.meta.ID != "tg1" {
		t.Fatalf("после release tg1 свободен, got %v", b)
	}
}

func TestFunauthUnconfirmed(t *testing.T) {
	for s, want := range map[string]bool{
		"bind_unconfirmed":                   true,
		"twofa_unconfirmed":                  true,
		"twofa_unconfirmed: account_offline": true,
		"reply_timeout":                      false,
		"":                                   false,
	} {
		if got := funauthUnconfirmed(s); got != want {
			t.Fatalf("funauthUnconfirmed(%q)=%v want %v", s, got, want)
		}
	}
}
