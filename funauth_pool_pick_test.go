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

func TestPickSharesTGUpToLimit(t *testing.T) {
	p := newFunauthPool()
	p.perAccount = 3
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}, ready: true, api: &tg.Client{}},
		"tg2": {meta: funauthAccountMeta{ID: "tg2", Phone: "+2"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{"nick_a": "tg1"}

	// есть место на tg1 (1 из 3) — добираем его, tg2 остаётся свободным
	acc, _ := p.pickForAnarchyBindDiag("nick_b", 503, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected shared tg1, got %v", acc)
	}
	acc.claimedBy = ""
	p.nicks["nick_b"] = "tg1"
	acc, _ = p.pickForAnarchyBindDiag("nick_c", 503, nil)
	if acc == nil || acc.meta.ID != "tg1" {
		t.Fatalf("expected tg1 again (2 of 3), got %v", acc)
	}
	acc.claimedBy = ""
	p.nicks["nick_c"] = "tg1"

	// tg1 забит (3 из 3) — четвёртый бот уходит на свободный tg2
	acc, _ = p.pickForAnarchyBindDiag("nick_d", 503, nil)
	if acc == nil || acc.meta.ID != "tg2" {
		t.Fatalf("expected free tg2 when tg1 is at the limit, got %v", acc)
	}
}

func TestPickLimitOneKeepsOldBehaviour(t *testing.T) {
	p := newFunauthPool() // лимит по умолчанию 1
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{"nick_a": "tg1"}
	acc, diag := p.pickForAnarchyBindDiag("nick_b", 503, nil)
	if acc != nil || diag.Busy != 1 {
		t.Fatalf("limit 1: shared TG must stay busy, got acc=%v busy=%d", acc, diag.Busy)
	}
}

func TestPickFarmBotDoesNotShareWithOwnerTG(t *testing.T) {
	root := t.TempDir()
	owners := filepath.Join(root, "clan-owners.json")
	if err := os.WriteFile(owners, []byte(`{"503":{"username":"owner_a","anarchy":503}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_OWNERS_FILE", owners)
	t.Setenv("FLEET_BOTS_DIR", filepath.Join(root, "bots"))
	_ = os.Mkdir(filepath.Join(root, "bots"), 0o755)

	p := newFunauthPool()
	p.perAccount = 5
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}, ready: true, api: &tg.Client{}},
	}
	p.nicks = map[string]string{"owner_a": "tg1"}
	acc, _ := p.pickForAnarchyBindDiag("farm_b", 503, nil)
	if acc != nil {
		t.Fatalf("farm bot must not share TG with a clan owner, got %s", acc.meta.ID)
	}
}

func TestClampPerAccount(t *testing.T) {
	if clampPerAccount(0) != 1 || clampPerAccount(7) != 7 || clampPerAccount(99) != funauthMaxPerAccount {
		t.Fatal("clamp")
	}
}

func TestSyncFullFollowsPerAccountLimit(t *testing.T) {
	root := t.TempDir()
	bots := filepath.Join(root, "bots")
	_ = os.Mkdir(bots, 0o755)
	if err := os.WriteFile(filepath.Join(bots, "503b.json"), []byte(`[{"username":"a","anarchy":503},{"username":"b","anarchy":503}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_BOTS_DIR", bots)
	t.Setenv("FLEET_OWNERS_FILE", filepath.Join(root, "none.json"))

	p := newFunauthPool()
	p.dir = t.TempDir()
	p.accounts = map[string]*funauthAccount{"tg1": {meta: funauthAccountMeta{ID: "tg1"}}}
	p.nicks = map[string]string{"a": "tg1"}

	p.perAccount = 2
	if p.syncAccountRosterFull("tg1") {
		t.Fatal("1 бот из 2 — TG ещё не закрыт")
	}
	p.nicks["b"] = "tg1"
	if !p.syncAccountRosterFull("tg1") {
		t.Fatal("2 бота из 2 — TG закрыт")
	}
	// лимит подняли — закрытие по лимиту снимается
	p.perAccount = 5
	if p.syncAccountRosterFull("tg1") {
		t.Fatal("после повышения лимита TG снова открыт")
	}
	// а отказ самого FunAuthBot остаётся
	p.accounts["tg1"].meta.FullBot = true
	if !p.syncAccountRosterFull("tg1") {
		t.Fatal("FullBot должен держать TG закрытым")
	}
}
