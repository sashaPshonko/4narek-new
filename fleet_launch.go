package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Глобальный gate: один логин на весь флот (бот/owner) с паузой между грантами.
// Орхи опрашивают POST /api/fleet/launch — без grant воркер не стартует.

const (
	fleetLaunchMinGap = 90 * time.Second
	fleetLaunchJitter = 120 * time.Second // итого 90–210 с между грантами
)

type fleetLaunchKind string

const (
	fleetLaunchBot   fleetLaunchKind = "bot"
	fleetLaunchOwner fleetLaunchKind = "owner"
)

type fleetLaunchReq struct {
	Anarchy  int             `json:"anarchy"`
	Username string          `json:"username"`
	Kind     fleetLaunchKind `json:"kind"`
	QueuedAt time.Time       `json:"-"`
}

type fleetLaunchResp struct {
	OK       bool   `json:"ok"`
	Granted  bool   `json:"granted"`
	WaitMs   int    `json:"wait_ms"`
	Position int    `json:"position"` // 1 = head, 0 если granted
	Reason   string `json:"reason,omitempty"`
	NextGapS int    `json:"next_gap_s,omitempty"`
}

type fleetLaunchState struct {
	mu       sync.Mutex
	queue    []fleetLaunchReq
	inQueue  map[string]bool
	nextAt   time.Time
	lastKey  string
	lastAt   time.Time
	lastKind fleetLaunchKind
}

var fleetLaunch = &fleetLaunchState{
	inQueue: make(map[string]bool),
}

func fleetLaunchKey(anarchy int, username string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "@" + strconv.Itoa(anarchy)
}

func (s *fleetLaunchState) enqueueLocked(req fleetLaunchReq) (pos int) {
	key := fleetLaunchKey(req.Anarchy, req.Username)
	if s.inQueue[key] {
		for i, q := range s.queue {
			if fleetLaunchKey(q.Anarchy, q.Username) == key {
				return i + 1
			}
		}
		return len(s.queue)
	}
	req.QueuedAt = time.Now()
	s.queue = append(s.queue, req)
	s.inQueue[key] = true
	return len(s.queue)
}

func (s *fleetLaunchState) positionLocked(anarchy int, username string) int {
	key := fleetLaunchKey(anarchy, username)
	for i, q := range s.queue {
		if fleetLaunchKey(q.Anarchy, q.Username) == key {
			return i + 1
		}
	}
	return 0
}

func (s *fleetLaunchState) tryGrant(anarchy int, username string, kind fleetLaunchKind) fleetLaunchResp {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	if username == "" || anarchy <= 0 {
		return fleetLaunchResp{OK: false, Reason: "bad anarchy/username"}
	}
	if kind != fleetLaunchOwner {
		kind = fleetLaunchBot
	}

	req := fleetLaunchReq{Anarchy: anarchy, Username: username, Kind: kind}
	pos := s.enqueueLocked(req)
	now := time.Now()

	if now.Before(s.nextAt) {
		wait := int(s.nextAt.Sub(now).Milliseconds())
		if wait < 500 {
			wait = 500
		}
		return fleetLaunchResp{
			OK:       true,
			Granted:  false,
			WaitMs:   wait,
			Position: pos,
			Reason:   "cooldown",
		}
	}

	// Слот свободен — только голова очереди.
	if len(s.queue) == 0 {
		return fleetLaunchResp{OK: true, Granted: false, WaitMs: 1000, Reason: "empty"}
	}
	head := s.queue[0]
	headKey := fleetLaunchKey(head.Anarchy, head.Username)
	myKey := fleetLaunchKey(anarchy, username)
	if headKey != myKey {
		// Не голова: короткий poll, очередь двигается только через grant головы.
		wait := 3000
		if now.Before(s.nextAt) {
			w := int(s.nextAt.Sub(now).Milliseconds())
			if w > 15_000 {
				w = 15_000
			}
			if w > wait {
				wait = w
			}
		}
		if wait < 1500 {
			wait = 1500
		}
		return fleetLaunchResp{
			OK:       true,
			Granted:  false,
			WaitMs:   wait,
			Position: pos,
			Reason:   "queued",
		}
	}

	// Grant head
	s.queue = s.queue[1:]
	delete(s.inQueue, headKey)
	gap := fleetLaunchMinGap
	if fleetLaunchJitter > 0 {
		gap += time.Duration(rand.Int63n(int64(fleetLaunchJitter)))
	}
	s.nextAt = now.Add(gap)
	s.lastKey = headKey
	s.lastAt = now
	s.lastKind = head.Kind
	log.Printf("[LAUNCH] grant %s an%d %s next_gap=%s queue_left=%d",
		head.Kind, head.Anarchy, head.Username, gap.Round(time.Second), len(s.queue))
	return fleetLaunchResp{
		OK:       true,
		Granted:  true,
		WaitMs:   0,
		Position: 0,
		Reason:   "granted",
		NextGapS: int(gap.Seconds()),
	}
}

// CancelLaunch — убрать из очереди (бан / стоп), слот не сжигает.
func (s *fleetLaunchState) cancel(anarchy int, username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fleetLaunchKey(anarchy, username)
	if !s.inQueue[key] {
		return false
	}
	out := s.queue[:0]
	for _, q := range s.queue {
		if fleetLaunchKey(q.Anarchy, q.Username) == key {
			continue
		}
		out = append(out, q)
	}
	s.queue = out
	delete(s.inQueue, key)
	log.Printf("[LAUNCH] cancel an%d %s", anarchy, username)
	return true
}

func handleFleetLaunchHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Anarchy  int    `json:"anarchy"`
		Username string `json:"username"`
		Kind     string `json:"kind"`
		Cancel   bool   `json:"cancel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Cancel {
		ok := fleetLaunch.cancel(body.Anarchy, body.Username)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": ok})
		return
	}
	kind := fleetLaunchKind(strings.ToLower(strings.TrimSpace(body.Kind)))
	resp := fleetLaunch.tryGrant(body.Anarchy, body.Username, kind)
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
