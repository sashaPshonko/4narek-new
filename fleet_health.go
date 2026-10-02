package main

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Флот:
//   - разовый вылет/хаб/рестарт → НЕ сужаем share и НЕ сбрасываем «онлайн с …»
//   - хронический флап → orch помечает unstable (вне bots_per_type)
//   - полный 0 дольше grace → OUTAGE (пауза цен); короткий 0 (кормёжка мавры) — тихо SKIP

type fleetMode int

const (
	fleetOK fleetMode = iota
	fleetDegraded
	fleetOutage
)

type fleetTypeHealth struct {
	peakPricing   int
	peakAt        time.Time
	stickyBots    int // для share: не падает от коротких дыр
	stickyBelowAt time.Time
	lastPricing   int
	lastMode      fleetMode
	zeroSince     time.Time
	outageLogged  bool
	degradedSince time.Time
}

var (
	fleetHealthMu sync.Mutex
	fleetTypeState = map[string]*fleetTypeHealth{}

	fleetPeakWindow   = 45 * time.Minute
	fleetStickyHold   = 12 * time.Minute // сколько держим старый N ботов для share
	fleetOutageGrace  = 90 * time.Second // полный 0 короче — не орём OUTAGE
	fleetPresenceGrace = 3 * time.Minute // typeActiveSince не сбрасываем сразу
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
	for t := range fleetTypeState {
		if _, ok := seen[t]; ok {
			continue
		}
		updateFleetTypeLocked(t, 0, now)
	}

	if len(fleetStats) > 0 {
		live := fleetStats["live"]
		pricing := fleetStats["pricing"]
		hub := fleetStats["hub"]
		unstable := fleetStats["unstable"]
		if hub > 0 || unstable > 0 || (live > 0 && pricing == 0) {
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

	if st.peakAt.IsZero() || now.Sub(st.peakAt) > fleetPeakWindow {
		if pricing > 0 {
			st.peakPricing = pricing
			st.peakAt = now
		}
	} else if pricing > st.peakPricing {
		st.peakPricing = pricing
		st.peakAt = now
	}

	// Sticky для share: растёт сразу, падает только после fleetStickyHold ниже пика.
	if pricing > st.stickyBots {
		st.stickyBots = pricing
		st.stickyBelowAt = time.Time{}
	} else if pricing > 0 && pricing < st.stickyBots {
		if st.stickyBelowAt.IsZero() {
			st.stickyBelowAt = now
		} else if now.Sub(st.stickyBelowAt) >= fleetStickyHold {
			st.stickyBots = pricing
			st.stickyBelowAt = time.Time{}
		}
	}
	// pricing==0: sticky не трогаем (вернутся — share тот же)

	if pricing == 0 {
		if st.zeroSince.IsZero() {
			st.zeroSince = now
		}
	} else {
		st.zeroSince = time.Time{}
		st.outageLogged = false
	}

	mode := fleetOK
	peakFresh := !st.peakAt.IsZero() && now.Sub(st.peakAt) <= fleetPeakWindow && st.peakPricing >= 2
	zeroLong := pricing == 0 && !st.zeroSince.IsZero() && now.Sub(st.zeroSince) >= fleetOutageGrace
	switch {
	case pricing == 0 && zeroLong:
		mode = fleetOutage
	case pricing == 0:
		mode = fleetOutage // для skip; лог только после grace
	case peakFresh && st.peakPricing >= 3 && pricing == 1:
		mode = fleetDegraded
	case peakFresh && pricing*2 < st.peakPricing:
		mode = fleetDegraded
	}

	if mode != st.lastMode || pricing != st.lastPricing {
		switch mode {
		case fleetOutage:
			// лог ниже по zeroLong
		case fleetDegraded:
			if st.degradedSince.IsZero() {
				st.degradedSince = now
			}
			if now.Sub(st.degradedSince) >= 2*time.Minute && (st.lastMode != fleetDegraded || pricing != st.lastPricing) {
				log.Printf("[FLEET] DEGRADED type=%s pricing=%d sticky=%d peak=%d — частичный простой",
					goType, pricing, st.stickyBots, st.peakPricing)
			}
		default:
			if st.lastMode == fleetOutage || st.lastMode == fleetDegraded {
				log.Printf("[FLEET] OK type=%s pricing=%d sticky=%d", goType, pricing, st.stickyBots)
			}
			st.degradedSince = time.Time{}
			st.outageLogged = false
		}
		st.lastMode = mode
		st.lastPricing = pricing
	}
	if mode == fleetOutage && zeroLong && peakFresh && !st.outageLogged {
		st.outageLogged = true
		log.Printf("[FLEET] OUTAGE type=%s pricing=0 peak=%d >%s — adjust на паузе",
			goType, st.peakPricing, fleetOutageGrace)
	}
}

func fleetModeForGoTypeLocked(goType string) fleetMode {
	fleetHealthMu.Lock()
	defer fleetHealthMu.Unlock()
	st := fleetTypeState[goType]
	if st == nil {
		return fleetOK
	}
	return st.lastMode
}

// stickyBotsForGoType — N для share/ёмкости: не схлопывается от короткого вылета одного бота.
// При current==0 → 0 (тип неактивен, adjust и так SKIP).
func stickyBotsForGoType(goType string, current int) int {
	if current <= 0 {
		return 0
	}
	fleetHealthMu.Lock()
	defer fleetHealthMu.Unlock()
	st := fleetTypeState[goType]
	if st == nil || st.stickyBots <= current {
		return current
	}
	return st.stickyBots
}

func applyPresenceFleetStats(ws *websocket.Conn, botsPerType map[string]int, fleetStats map[string]int) {
	_ = ws
	noteFleetPresenceLocked(botsPerType, fleetStats)
}
