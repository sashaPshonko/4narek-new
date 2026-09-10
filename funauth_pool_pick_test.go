package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"
)

func TestPickForAnarchyBindMultiUntilFull(t *testing.T) {
	p := newFunauthPool()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1", Anarchy: 503}, ready: true, api: &tg.Client{}},
		"tg2": {meta: funauthAccountMeta{ID: "tg2", Phone: "+2"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{
		"nick_a": "tg1",
	}

	acc, diag := p.pickForAnarchyBindDiag("nick_b", 503, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected reuse tg1 for second nick, got %v busy=%d full=%d", acc, diag.Busy, diag.Full)
	}

	acc, _ = p.pickForAnarchyBindDiag("nick_a", 503, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected tg1 for mapped nick, got %v", acc)
	}

	p.accounts["tg1"].meta.Full = true
	acc, diag = p.pickForAnarchyBindDiag("nick_c", 503, nil)
	if acc == nil || acc.meta.ID != "tg2" {
		t.Fatalf("expected free tg2 when tg1 full, got %v", acc)
	}
	if diag.Full != 1 {
		t.Fatalf("full=%d want 1", diag.Full)
	}
}

func TestPickReusesOtherAnarchyUntilFull(t *testing.T) {
	p := newFunauthPool()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1", Anarchy: 502}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{"nick_a": "tg1"}

	acc, diag := p.pickForAnarchyBindDiag("nick_b", 507, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected reuse tg1 across anarchy, got %v full=%d", acc, diag.Full)
	}
}

func TestSyncAccountRosterFullDoesNotAutoFull(t *testing.T) {
	p := newFunauthPool()
	p.dir = t.TempDir()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1"}},
	}
	p.nicks = map[string]string{"a": "tg1"}

	if p.syncAccountRosterFull("tg1") {
		t.Fatal("farm bind must not mark full")
	}
	if p.accounts["tg1"].meta.Full {
		t.Fatal("meta.Full set without FunTime limit")
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
