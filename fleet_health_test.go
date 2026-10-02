package main

import (
	"testing"
	"time"
)

func TestFleetOutageVsDegraded(t *testing.T) {
	fleetHealthMu.Lock()
	fleetTypeState = map[string]*fleetTypeHealth{}
	fleetHealthMu.Unlock()

	now := time.Now()
	fleetHealthMu.Lock()
	updateFleetTypeLocked("netherite_sword-1.21", 3, now)
	updateFleetTypeLocked("netherite_sword-1.21", 1, now.Add(time.Minute))
	st := fleetTypeState["netherite_sword-1.21"]
	mode1 := st.lastMode
	updateFleetTypeLocked("netherite_sword-1.21", 0, now.Add(2*time.Minute))
	mode0 := st.lastMode
	fleetHealthMu.Unlock()

	if mode1 != fleetDegraded {
		t.Fatalf("1 of 3 → degraded, got %d", mode1)
	}
	if mode0 != fleetOutage {
		t.Fatalf("0 of peak → outage, got %d", mode0)
	}
}
