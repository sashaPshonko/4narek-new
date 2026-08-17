package main

import (
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

	if !p.syncAccountRosterFull("tg1") {
		t.Fatal("expected full after 1 bind")
	}
	if !p.accounts["tg1"].meta.Full {
		t.Fatal("meta.Full not set")
	}
}
