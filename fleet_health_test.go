package main

import (
	"testing"
	"time"
)

func TestFleetStickyShareIgnoresBriefDrop(t *testing.T) {
	fleetHealthMu.Lock()
	fleetTypeState = map[string]*fleetTypeHealth{}
	fleetHealthMu.Unlock()

	now := time.Now()
	fleetHealthMu.Lock()
	updateFleetTypeLocked("netherite_sword-1.21", 3, now, false)
	updateFleetTypeLocked("netherite_sword-1.21", 1, now.Add(time.Minute), false) // разовый вылет
	st := fleetTypeState["netherite_sword-1.21"]
	sticky := st.stickyBots
	fleetHealthMu.Unlock()

	if sticky != 3 {
		t.Fatalf("sticky after brief drop: want 3 got %d", sticky)
	}
	if got := stickyBotsForGoType("netherite_sword-1.21", 1); got != 3 {
		t.Fatalf("share bots: want 3 got %d", got)
	}
}

func TestFleetStickyShrinksAfterHold(t *testing.T) {
	fleetHealthMu.Lock()
	fleetTypeState = map[string]*fleetTypeHealth{}
	fleetHealthMu.Unlock()

	now := time.Now()
	fleetHealthMu.Lock()
	updateFleetTypeLocked("netherite_sword-1.21", 3, now, false)
	updateFleetTypeLocked("netherite_sword-1.21", 1, now.Add(time.Minute), false)
	updateFleetTypeLocked("netherite_sword-1.21", 1, now.Add(fleetStickyHold+2*time.Minute), false)
	st := fleetTypeState["netherite_sword-1.21"]
	sticky := st.stickyBots
	fleetHealthMu.Unlock()

	if sticky != 1 {
		t.Fatalf("sticky after hold: want 1 got %d", sticky)
	}
}

func TestFleetOutageLogOnlyAfterGrace(t *testing.T) {
	fleetHealthMu.Lock()
	fleetTypeState = map[string]*fleetTypeHealth{}
	fleetHealthMu.Unlock()

	now := time.Now()
	fleetHealthMu.Lock()
	updateFleetTypeLocked("netherite_sword-1.21", 3, now, false)
	updateFleetTypeLocked("netherite_sword-1.21", 0, now.Add(10*time.Second), false)
	st := fleetTypeState["netherite_sword-1.21"]
	if st.outageLogged {
		t.Fatal("OUTAGE must not log within grace")
	}
	updateFleetTypeLocked("netherite_sword-1.21", 0, now.Add(10*time.Second+fleetOutageGrace+5*time.Second), false)
	if !st.outageLogged {
		t.Fatal("OUTAGE should log after grace")
	}
	fleetHealthMu.Unlock()
}

func TestFleetScheduleOffSnapsStickyImmediately(t *testing.T) {
	fleetHealthMu.Lock()
	fleetTypeState = map[string]*fleetTypeHealth{}
	fleetHealthMu.Unlock()

	now := time.Now()
	fleetHealthMu.Lock()
	updateFleetTypeLocked("netherite_sword-1.21", 3, now, false)
	updateFleetTypeLocked("netherite_sword-1.21", 1, now.Add(time.Minute), true)
	st := fleetTypeState["netherite_sword-1.21"]
	sticky := st.stickyBots
	fleetHealthMu.Unlock()

	if sticky != 1 {
		t.Fatalf("schedule off: sticky want 1 got %d", sticky)
	}
	if got := stickyBotsForGoType("netherite_sword-1.21", 1); got != 1 {
		t.Fatalf("schedule off share: want 1 got %d", got)
	}
}
