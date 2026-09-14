package main

import (
	"testing"
	"time"
)

func TestClanSetupDispatchesOnFirstNeed(t *testing.T) {
	old := clanSetupCtl
	clanSetupCtl = &clanSetupController{byAn: make(map[int]*clanAnarchyState)}
	t.Cleanup(func() { clanSetupCtl = old })

	noteClanNeeded(502, "alpha", "not_in_clan")
	// no orch → dispatch attempted and running cleared
	clanSetupCtl.mu.Lock()
	defer clanSetupCtl.mu.Unlock()
	st := clanSetupCtl.stateLocked(502)
	if st.running {
		t.Fatal("running should clear when no orch")
	}
	if len(st.needs) != 1 {
		t.Fatalf("needs kept: %d", len(st.needs))
	}
}

func TestClanSetupOkAllowsImmediateRetrigger(t *testing.T) {
	old := clanSetupCtl
	clanSetupCtl = &clanSetupController{byAn: make(map[int]*clanAnarchyState)}
	t.Cleanup(func() { clanSetupCtl = old })

	noteClanNeeded(503, "a", "x")
	noteClanNeeded(503, "b", "x")
	noteClanSetupResult(503, true, false, "ok")
	clanSetupCtl.mu.Lock()
	st := clanSetupCtl.stateLocked(503)
	if len(st.needs) != 0 {
		t.Fatalf("needs cleared, got %d", len(st.needs))
	}
	if !st.cooldownUntil.IsZero() && time.Now().Before(st.cooldownUntil) {
		t.Fatal("OK must not set success lock")
	}
	clanSetupCtl.mu.Unlock()

	noteClanNeeded(503, "c", "not_in_clan")
	clanSetupCtl.mu.Lock()
	defer clanSetupCtl.mu.Unlock()
	st = clanSetupCtl.stateLocked(503)
	if len(st.needs) != 1 {
		t.Fatalf("re-need after OK: %d", len(st.needs))
	}
	// dispatch tried (no orch) → not stuck running
	if st.running {
		t.Fatal("running should clear when no orch")
	}
}

func TestClanSetupFailCooldownBlocksBriefly(t *testing.T) {
	old := clanSetupCtl
	clanSetupCtl = &clanSetupController{byAn: make(map[int]*clanAnarchyState)}
	t.Cleanup(func() { clanSetupCtl = old })

	noteClanNeeded(504, "x", "not_in_clan")
	noteClanSetupResult(504, false, false, "vpn")
	clanSetupCtl.mu.Lock()
	st := clanSetupCtl.stateLocked(504)
	st.needs["x"] = time.Now().Add(-time.Minute)
	clanSetupCtl.mu.Unlock()

	maybeDispatchClanSetups()
	clanSetupCtl.mu.Lock()
	defer clanSetupCtl.mu.Unlock()
	if clanSetupCtl.stateLocked(504).running {
		t.Fatal("must not dispatch during fail cooldown")
	}
}
