package main

import (
	"testing"
	"time"
)

func TestCappedDiscoveryAdvance(t *testing.T) {
	// +1 toward 850k from 400k step 100k
	if g := cappedDiscoveryAdvance(400_000, 100_000, 1, 850_000); g != 500_000 {
		t.Fatalf("cap1: %d", g)
	}
	if g := cappedDiscoveryAdvance(400_000, 100_000, 3, 850_000); g != 700_000 {
		t.Fatalf("cap3: %d", g)
	}
	if g := cappedDiscoveryAdvance(400_000, 100_000, 5, 850_000); g != 850_000 {
		t.Fatalf("cap5 should clamp to target: %d", g)
	}
	if g := cappedDiscoveryAdvance(900_000, 100_000, 5, 850_000); g != 900_000 {
		t.Fatalf("already above target: %d", g)
	}
}

func TestCappedDiscoveryBandFloor(t *testing.T) {
	if cappedDiscoveryBandFloor(1_000_000) != 850_000 {
		t.Fatal("0.85*p10")
	}
}

func TestCappedDiscoveryShadowThreeArmsSameEpisode(t *testing.T) {
	resetCappedDiscoveryStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()

	now := time.Now()
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	// inject book via evaluating with ahBook — runCappedDiscoveryShadow calls ahBookMarketRecoveryStats
	// so with mlDB=nil book is empty and gate fails. Call internals via state machine manually.

	cdMu.Lock()
	st := cdGet("CAP_SKU")
	st.EpisodeID = "ep-test"
	st.StartedAt = now
	st.InitialOur = 400_000
	st.InitialP10 = 1_000_000
	st.InitialRatio = 0.4
	st.TargetRef = 850_000
	st.Arm1 = cappedDiscoveryArm{Virtual: 400_000, Active: true}
	st.Arm3 = cappedDiscoveryArm{Virtual: 400_000, Active: true}
	st.Arm5 = cappedDiscoveryArm{Virtual: 400_000, Active: true}
	cdMu.Unlock()

	// one synthetic advance cycle
	cdMu.Lock()
	st = cdGet("CAP_SKU")
	p10 := 1_000_000
	target := cappedDiscoveryBandFloor(p10)
	st.Arm1.Virtual = cappedDiscoveryAdvance(st.Arm1.Virtual, 100_000, 1, target)
	st.Arm1.Cycles++
	st.Arm3.Virtual = cappedDiscoveryAdvance(st.Arm3.Virtual, 100_000, 3, target)
	st.Arm3.Cycles++
	st.Arm5.Virtual = cappedDiscoveryAdvance(st.Arm5.Virtual, 100_000, 5, target)
	st.Arm5.Cycles++
	v1, v3, v5 := st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual
	cdMu.Unlock()

	if v1 != 500_000 || v3 != 700_000 || v5 != 850_000 {
		t.Fatalf("same start, different caps: v1=%d v3=%d v5=%d", v1, v3, v5)
	}
	if v5 != target {
		t.Fatal("cap5 should hit band in 1 cycle from 400k")
	}
	_ = book
}

func TestCappedDiscoveryArmStopEnteredBand(t *testing.T) {
	stop := cappedDiscoveryArmStop(850_000, 1_000_000, 0, 0, true, false, "hold", true)
	if stop != "entered_band" {
		t.Fatalf("got %q", stop)
	}
	stop = cappedDiscoveryArmStop(500_000, 1_000_000, 0, 1, true, false, "hold", true)
	if stop != "real_buys" {
		t.Fatalf("got %q", stop)
	}
}

func TestCappedDiscoveryDoesNotMutateInputPrice(t *testing.T) {
	resetCappedDiscoveryStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()
	price := 400_000
	runCappedDiscoveryShadow("NOBOOK", time.Now(), price, 100_000, 0, 0, 0, 0, "hold", false)
	if price != 400_000 {
		t.Fatal("mutated")
	}
}
