package main

import (
	"testing"
	"time"
)

func TestTwoFAOnlyFromOwnTG(t *testing.T) {
	p := newFunauthPool()
	p.dir = t.TempDir()
	p.accounts = map[string]*funauthAccount{
		"tg1": {meta: funauthAccountMeta{ID: "tg1", Phone: "+1"}},
	}
	p.nicks = map[string]string{"alive": "tg1", "lost": "gone"}

	if acc, mapped, lost := p.twoFATarget("Alive"); acc == nil || !mapped || lost {
		t.Fatalf("alive: acc=%v mapped=%v lost=%v", acc, mapped, lost)
	}
	if acc, mapped, lost := p.twoFATarget("lost"); acc != nil || !mapped || !lost {
		t.Fatalf("lost: acc=%v mapped=%v lost=%v", acc, mapped, lost)
	}
	if acc, mapped, lost := p.twoFATarget("nobody"); acc != nil || mapped || lost {
		t.Fatalf("unmapped: acc=%v mapped=%v lost=%v", acc, mapped, lost)
	}

	// tg_lost сразу, без похода в Telegram и без перебора чужих TG
	b := newFunauthBinder(p)
	res := b.processTwoFA(&funauthBindJob{nick: "lost", mode: "twofa"})
	if res.OK || res.Error != funauthErrTGLost {
		t.Fatalf("lost nick: %+v", res)
	}
	// TG ника переподключается — не шлём /2fa с других
	res = b.processTwoFA(&funauthBindJob{nick: "alive", mode: "twofa"})
	if res.OK || res.Error != "account_offline" {
		t.Fatalf("offline tg: %+v", res)
	}
}

func TestTwoFAPendingBlocksDelete(t *testing.T) {
	p := newFunauthPool()
	p.dir = t.TempDir()
	p.nicks = map[string]string{"a": "tg1", "b": "tg1", "c": "tg2"}
	p.markTwoFAPending("B")
	p.mu.Lock()
	got := p.accountPendingNicksLocked("tg1")
	other := p.accountPendingNicksLocked("tg2")
	p.mu.Unlock()
	if len(got) != 1 || got[0] != "b" || len(other) != 0 {
		t.Fatalf("pending tg1=%v tg2=%v", got, other)
	}

	// переживает перезапуск
	q := newFunauthPool()
	q.dir = p.dir
	q.loadTwoFAPending()
	if !q.twofaPending["b"] {
		t.Fatal("pending not persisted")
	}
	q.clearTwoFAPending("b")
	r := newFunauthPool()
	r.dir = p.dir
	r.loadTwoFAPending()
	if len(r.twofaPending) != 0 {
		t.Fatalf("clear not persisted: %v", r.twofaPending)
	}
}

func TestTwoFABackoff(t *testing.T) {
	if d := funauthTwoFADelay(funauthErrTGLost, 1); d != 6*time.Hour {
		t.Fatalf("tg_lost delay %v", d)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if d := funauthTwoFADelay("twofa_not_accepted", i+1); d != w {
			t.Fatalf("fails=%d delay %v want %v", i+1, d, w)
		}
	}

	nick := "backoff_test_nick"
	funauthTwoFANote(nick, false, "twofa_not_accepted")
	if wait, lastErr, _ := funauthTwoFAWait(nick); wait <= 0 || lastErr != "twofa_not_accepted" {
		t.Fatalf("after fail: wait=%v err=%q", wait, lastErr)
	}
	funauthTwoFANote(nick, true, "")
	if wait, _, _ := funauthTwoFAWait(nick); wait != 0 {
		t.Fatalf("after ok: wait=%v", wait)
	}
	// «занято» паузу не включает
	funauthTwoFANote(nick, false, "busy")
	if wait, _, _ := funauthTwoFAWait(nick); wait != 0 {
		t.Fatalf("busy must not pause: %v", wait)
	}
}
