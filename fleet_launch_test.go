package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFleetLaunchSerializesGrants(t *testing.T) {
	// Isolated state for test
	old := fleetLaunch
	fleetLaunch = &fleetLaunchState{inQueue: make(map[string]bool)}
	t.Cleanup(func() { fleetLaunch = old })

	// Force ready
	fleetLaunch.nextAt = time.Time{}

	r1 := fleetLaunch.tryGrant(502, "alpha", fleetLaunchBot)
	if !r1.Granted {
		t.Fatalf("first should grant: %+v", r1)
	}
	r2 := fleetLaunch.tryGrant(503, "beta", fleetLaunchBot)
	if r2.Granted {
		t.Fatalf("second must wait cooldown: %+v", r2)
	}
	if r2.Position != 1 {
		t.Fatalf("beta should be sole queued head pos=1, got %d", r2.Position)
	}

	// Advance clock
	fleetLaunch.mu.Lock()
	fleetLaunch.nextAt = time.Now().Add(-time.Second)
	fleetLaunch.mu.Unlock()

	r3 := fleetLaunch.tryGrant(503, "beta", fleetLaunchBot)
	if !r3.Granted {
		t.Fatalf("beta after cooldown: %+v", r3)
	}
}

func TestFleetLaunchQueueOrder(t *testing.T) {
	old := fleetLaunch
	fleetLaunch = &fleetLaunchState{inQueue: make(map[string]bool)}
	t.Cleanup(func() { fleetLaunch = old })
	fleetLaunch.nextAt = time.Now().Add(time.Hour) // block grants

	fleetLaunch.tryGrant(502, "a", fleetLaunchBot)
	fleetLaunch.tryGrant(503, "b", fleetLaunchBot)
	fleetLaunch.tryGrant(504, "c", fleetLaunchOwner)
	// re-poll: owner вперёд ботов
	a := fleetLaunch.tryGrant(502, "a", fleetLaunchBot)
	b := fleetLaunch.tryGrant(503, "b", fleetLaunchBot)
	c := fleetLaunch.tryGrant(504, "c", fleetLaunchOwner)
	if c.Position != 1 || a.Position != 2 || b.Position != 3 {
		t.Fatalf("positions owner-first c=%d a=%d b=%d", c.Position, a.Position, b.Position)
	}

	fleetLaunch.cancel(503, "b")
	c2 := fleetLaunch.tryGrant(504, "c", fleetLaunchOwner)
	if c2.Position != 1 {
		t.Fatalf("after cancel b, owner c should stay 1 got %d", c2.Position)
	}
}

func TestFleetLaunchDropsStaleHead(t *testing.T) {
	old := fleetLaunch
	fleetLaunch = &fleetLaunchState{inQueue: make(map[string]bool)}
	t.Cleanup(func() { fleetLaunch = old })
	fleetLaunch.nextAt = time.Time{}

	fleetLaunch.mu.Lock()
	fleetLaunch.queue = []fleetLaunchReq{{
		Anarchy: 502, Username: "ghost", Kind: fleetLaunchBot,
		QueuedAt: time.Now().Add(-5 * time.Minute),
		LastSeen: time.Now().Add(-5 * time.Minute),
	}}
	fleetLaunch.inQueue["ghost@502"] = true
	fleetLaunch.mu.Unlock()

	r := fleetLaunch.tryGrant(503, "alive", fleetLaunchBot)
	if !r.Granted {
		t.Fatalf("alive should grant after stale drop: %+v", r)
	}
}

func TestFleetLaunchHTTP(t *testing.T) {
	old := fleetLaunch
	fleetLaunch = &fleetLaunchState{inQueue: make(map[string]bool)}
	t.Cleanup(func() { fleetLaunch = old })
	fleetLaunch.nextAt = time.Time{}

	mux := http.NewServeMux()
	registerFleetHTTP(mux)
	body, _ := json.Marshal(map[string]any{"anarchy": 502, "username": "zola_u_mosta", "kind": "bot"})
	req := httptest.NewRequest(http.MethodPost, "/api/fleet/launch", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var resp fleetLaunchResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Granted {
		t.Fatalf("want grant: %+v", resp)
	}
}
