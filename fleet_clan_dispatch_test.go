package main

import (
	"testing"
	"time"
)

func TestClanSetupBatchesBeforeDispatch(t *testing.T) {
	old := clanSetupCtl
	clanSetupCtl = &clanSetupController{byAn: make(map[int]*clanAnarchyState)}
	t.Cleanup(func() { clanSetupCtl = old })

	noteClanNeeded(502, "alpha", "not_in_clan")
	clanSetupCtl.mu.Lock()
	st := clanSetupCtl.stateLocked(502)
	if st.running {
		t.Fatal("should not run immediately on first need")
	}
	// pretend earliest was 4 min ago
	st.needs["alpha"] = time.Now().Add(-4 * time.Minute)
	clanSetupCtl.mu.Unlock()

	// no orch connected → dispatch fails and clears running
	maybeDispatchClanSetups()
	clanSetupCtl.mu.Lock()
	defer clanSetupCtl.mu.Unlock()
	st = clanSetupCtl.stateLocked(502)
	if st.running {
		t.Fatal("running should clear when no orch")
	}
	if len(st.needs) != 1 {
		t.Fatalf("needs kept: %d", len(st.needs))
	}
}

func TestClanSetupResultCooldown(t *testing.T) {
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
	if !time.Now().Before(st.cooldownUntil) {
		t.Fatal("want success cooldown")
	}
	clanSetupCtl.mu.Unlock()

	noteClanNeeded(503, "c", "x")
	clanSetupCtl.mu.Lock()
	st = clanSetupCtl.stateLocked(503)
	st.needs["c"] = time.Now().Add(-clanSetupMaxWait)
	clanSetupCtl.mu.Unlock()
	maybeDispatchClanSetups()
	clanSetupCtl.mu.Lock()
	defer clanSetupCtl.mu.Unlock()
	if clanSetupCtl.stateLocked(503).running {
		t.Fatal("must not dispatch during cooldown")
	}
}
