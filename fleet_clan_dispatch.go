package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Go решает, когда орх запускает clan-setup (owner login).
 // Орк только репортит clan_needed / clan_setup_result.

const (
	clanSetupBatchWait     = 3 * time.Minute  // копить дырки перед первым заходом owner
	clanSetupMaxWait       = 12 * time.Minute // даже одна дырка — не дольше
	clanSetupSuccessCD     = 2 * time.Hour
	clanSetupBanCD         = 24 * time.Hour
	clanSetupRunningTimeout = 25 * time.Minute
	clanSetupTick          = 20 * time.Second
)

type clanAnarchyState struct {
	needs       map[string]time.Time // nick → first seen
	running     bool
	runningSince time.Time
	cooldownUntil time.Time
	lastReason  string
}

type clanSetupController struct {
	mu    sync.Mutex
	byAn  map[int]*clanAnarchyState
}

var clanSetupCtl = &clanSetupController{
	byAn: make(map[int]*clanAnarchyState),
}

func (c *clanSetupController) stateLocked(an int) *clanAnarchyState {
	st := c.byAn[an]
	if st == nil {
		st = &clanAnarchyState{needs: make(map[string]time.Time)}
		c.byAn[an] = st
	}
	return st
}

func noteClanNeeded(anarchy int, username, reason string) {
	username = strings.TrimSpace(username)
	if anarchy <= 0 || username == "" {
		return
	}
	clanSetupCtl.mu.Lock()
	st := clanSetupCtl.stateLocked(anarchy)
	if _, ok := st.needs[username]; !ok {
		st.needs[username] = time.Now()
		log.Printf("[CLAN] need an%d %s (%s) total=%d", anarchy, username, reason, len(st.needs))
	}
	if reason != "" {
		st.lastReason = reason
	}
	clanSetupCtl.mu.Unlock()
	maybeDispatchClanSetups()
}

func noteClanSetupResult(anarchy int, ok, banned bool, detail string) {
	if anarchy <= 0 {
		return
	}
	clanSetupCtl.mu.Lock()
	st := clanSetupCtl.stateLocked(anarchy)
	st.running = false
	st.runningSince = time.Time{}
	now := time.Now()
	if banned {
		st.cooldownUntil = now.Add(clanSetupBanCD)
		log.Printf("[CLAN] result an%d BANNED — cooldown %s (%s)", anarchy, clanSetupBanCD, detail)
	} else if ok {
		st.needs = make(map[string]time.Time)
		st.cooldownUntil = now.Add(clanSetupSuccessCD)
		log.Printf("[CLAN] result an%d OK — cleared needs, cooldown %s", anarchy, clanSetupSuccessCD)
	} else {
		// неудача без бана — короткий отдых, дырки оставляем
		st.cooldownUntil = now.Add(15 * time.Minute)
		log.Printf("[CLAN] result an%d fail — retry after 15m (%s) needs=%d", anarchy, detail, len(st.needs))
	}
	clanSetupCtl.mu.Unlock()
}

func maybeDispatchClanSetups() {
	type job struct {
		an     int
		nicks  []string
		reason string
	}
	var jobs []job
	now := time.Now()

	clanSetupCtl.mu.Lock()
	for an, st := range clanSetupCtl.byAn {
		if st.running {
			if !st.runningSince.IsZero() && now.Sub(st.runningSince) > clanSetupRunningTimeout {
				log.Printf("[CLAN] an%d running timeout — reset", an)
				st.running = false
				st.runningSince = time.Time{}
			} else {
				continue
			}
		}
		if now.Before(st.cooldownUntil) {
			continue
		}
		if len(st.needs) == 0 {
			continue
		}
		earliest := now
		nicks := make([]string, 0, len(st.needs))
		for nick, t0 := range st.needs {
			nicks = append(nicks, nick)
			if t0.Before(earliest) {
				earliest = t0
			}
		}
		waited := now.Sub(earliest)
		ready := waited >= clanSetupBatchWait || len(nicks) >= 2 || waited >= clanSetupMaxWait
		if !ready {
			continue
		}
		st.running = true
		st.runningSince = now
		reason := st.lastReason
		if reason == "" {
			reason = "clan_needed"
		}
		jobs = append(jobs, job{an: an, nicks: nicks, reason: reason})
	}
	clanSetupCtl.mu.Unlock()

	for _, j := range jobs {
		if !dispatchRunClanSetup(j.an, j.nicks, j.reason) {
			clanSetupCtl.mu.Lock()
			st := clanSetupCtl.stateLocked(j.an)
			st.running = false
			st.runningSince = time.Time{}
			clanSetupCtl.mu.Unlock()
			log.Printf("[CLAN] dispatch an%d failed (no orch ws) — will retry", j.an)
		}
	}
}

func dispatchRunClanSetup(anarchy int, nicks []string, reason string) bool {
	ws := orchConnByAnarchy(anarchy)
	if ws == nil {
		return false
	}
	payload := map[string]any{
		"action":  "run_clan_setup",
		"anarchy": anarchy,
		"nicks":   nicks,
		"reason":  reason,
	}
	if err := wsWriteJSON(ws, payload); err != nil {
		log.Printf("[CLAN] ws send an%d: %v", anarchy, err)
		return false
	}
	log.Printf("[CLAN] dispatch run_clan_setup an%d nicks=%v reason=%s", anarchy, nicks, reason)
	return true
}

func orchConnByAnarchy(anarchy int) *websocket.Conn {
	mutex.RLock()
	defer mutex.RUnlock()
	for ws, an := range clientOrchestratorAnarchy {
		if an == anarchy {
			if _, ok := clients[ws]; ok {
				return ws
			}
		}
	}
	return nil
}

func startClanSetupDispatcher() {
	ticker := time.NewTicker(clanSetupTick)
	defer ticker.Stop()
	for range ticker.C {
		maybeDispatchClanSetups()
	}
}

func handleClanNeededHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Anarchy  any    `json:"anarchy"`
		Username string `json:"username"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	noteClanNeeded(anarchyInt(body.Anarchy), body.Username, body.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleClanSetupResultHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Anarchy int    `json:"anarchy"`
		OK      bool   `json:"ok"`
		Banned  bool   `json:"banned"`
		Detail  string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	noteClanSetupResult(body.Anarchy, body.OK, body.Banned, body.Detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleClanNeededFromWS(raw []byte) {
	var body struct {
		Anarchy  any    `json:"anarchy"`
		Username string `json:"username"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return
	}
	noteClanNeeded(anarchyInt(body.Anarchy), body.Username, body.Reason)
}

func handleClanSetupResultFromWS(raw []byte) {
	var body struct {
		Anarchy int    `json:"anarchy"`
		OK      bool   `json:"ok"`
		Banned  bool   `json:"banned"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return
	}
	noteClanSetupResult(body.Anarchy, body.OK, body.Banned, body.Detail)
}
