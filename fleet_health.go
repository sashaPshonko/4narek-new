package main

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Флот для ценообразования:
//   OK        — норма
//   Degraded  — часть ботов мертва/в хабе, но ≥1 pricing → цены крутим на оставшихся
//   Outage    — 0 pricing при недавнем пике ≥2 → пауза adjust (не dump «в пустоту»)
//
// Хаб / вылет режет orch (on_anarchy / success). Здесь — режим типа + лог.

type fleetMode int

const (
	fleetOK fleetMode = iota
	fleetDegraded
	fleetOutage
)

type fleetTypeHealth struct {
	peakPricing   int
	peakAt        time.Time
	lastPricing   int
	lastMode      fleetMode
	outageSince   time.Time
	degradedSince time.Time
}

var (
	fleetHealthMu   sync.Mutex
	fleetTypeState  = map[string]*fleetTypeHealth{}
	fleetPeakWindow = 45 * time.Minute
)

func noteFleetPresenceLocked(botsPerType map[string]int, fleetStats map[string]int) {
	now := time.Now()
	fleetHealthMu.Lock()
	defer fleetHealthMu.Unlock()

	seen := make(map[string]struct{})
	for t, n := range botsPerType {
		seen[t] = struct{}{}
		updateFleetTypeLocked(t, n, now)
	}
	// Типы, которые пропали из bots_per_type целиком.
	for t, st := range fleetTypeState {
		if _, ok := seen[t]; ok {
			continue
		}
		updateFleetTypeLocked(t, 0, now)
		_ = st
	}

	if len(fleetStats) > 0 {
		live := fleetStats["live"]
		pricing := fleetStats["pricing"]
		hub := fleetStats["hub"]
		unstable := fleetStats["unstable"]
		if live > 0 || pricing > 0 || hub > 0 {
			log.Printf("[FLEET] presence live=%d pricing=%d hub=%d unstable=%d", live, pricing, hub, unstable)
		}
	}
}

func updateFleetTypeLocked(goType string, pricing int, now time.Time) {
	st := fleetTypeState[goType]
	if st == nil {
		st = &fleetTypeHealth{}
		fleetTypeState[goType] = st
	}
	// Пик: максимум за окно; если окно протухло — сбрасываем к текущему.
	if st.peakAt.IsZero() || now.Sub(st.peakAt) > fleetPeakWindow {
		st.peakPricing = pricing
		st.peakAt = now
	} else if pricing > st.peakPricing {
		st.peakPricing = pricing
		st.peakAt = now
	}

	mode := fleetOK
	peakFresh := now.Sub(st.peakAt) <= fleetPeakWindow && st.peakPricing >= 2
	switch {
	case pricing == 0:
		mode = fleetOutage
	case peakFresh && st.peakPricing >= 3 && pricing == 1:
		mode = fleetDegraded
	case peakFresh && pricing*2 < st.peakPricing:
		mode = fleetDegraded
	}

	if mode != st.lastMode || pricing != st.lastPricing {
		switch mode {
		case fleetOutage:
			if st.outageSince.IsZero() {
				st.outageSince = now
			}
			if peakFresh {
				log.Printf("[FLEET] OUTAGE type=%s pricing=%d peak=%d (за %s) — adjust на паузе",
					goType, pricing, st.peakPricing, fleetPeakWindow)
			}
		case fleetDegraded:
			if st.degradedSince.IsZero() {
				st.degradedSince = now
			}
			st.outageSince = time.Time{}
			log.Printf("[FLEET] DEGRADED type=%s pricing=%d peak=%d — частичный вылет, цены на оставшихся",
				goType, pricing, st.peakPricing)
		default:
			if st.lastMode != fleetOK && st.lastMode != 0 {
				log.Printf("[FLEET] OK type=%s pricing=%d", goType, pricing)
			}
			st.outageSince = time.Time{}
			st.degradedSince = time.Time{}
		}
		st.lastMode = mode
		st.lastPricing = pricing
	}
}

// fleetModeForGoTypeLocked — для логов / будущего soft-hold. Outage ≡ нет active (уже SKIP).
func fleetModeForGoTypeLocked(goType string) fleetMode {
	fleetHealthMu.Lock()
	defer fleetHealthMu.Unlock()
	st := fleetTypeState[goType]
	if st == nil {
		return fleetOK
	}
	return st.lastMode
}

// applyPresenceFleetStats — вызывать под mutex после setClientBotsPerType.
func applyPresenceFleetStats(ws *websocket.Conn, botsPerType map[string]int, fleetStats map[string]int) {
	_ = ws
	noteFleetPresenceLocked(botsPerType, fleetStats)
}
