package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	staffCheckMax       = 200
	deskChatMax         = 3000
	deskChatPerCheckAPI = 200
)

var staffDeskPersistPath = "ml_data/staff_desk.json"

type staffCheckView struct {
	ID       string         `json:"id"`
	Username string         `json:"username"`
	Anarchy  any            `json:"anarchy"`
	Reason   string         `json:"reason,omitempty"`
	At       string         `json:"at"`
	EndedAt  string         `json:"ended_at,omitempty"`
	Status   string         `json:"status"` // live | done
	Chat     []deskChatView `json:"chat,omitempty"`
}

type deskChatView struct {
	ID       string `json:"id"`
	CheckID  string `json:"check_id,omitempty"`
	Username string `json:"username"`
	Anarchy  any    `json:"anarchy,omitempty"`
	From     string `json:"from,omitempty"`
	Text     string `json:"text"`
	At       string `json:"at"`
}

type staffDeskFile struct {
	Checks    []staffCheckView `json:"checks"`
	Chats     []deskChatView   `json:"chats"`
	UpdatedAt time.Time        `json:"updated_at"`
}

var (
	staffDeskMu     sync.RWMutex
	staffChecksMem  []staffCheckView
	deskChatsMem    []deskChatView
	staffDeskSaveCh = make(chan struct{}, 1)
)

func loadStaffDeskPersist() {
	go staffDeskSaveLoop()
	raw, err := os.ReadFile(staffDeskPersistPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[fleet] load staff-desk: %v", err)
		}
		return
	}
	var snap staffDeskFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		log.Printf("[fleet] parse staff-desk: %v", err)
		return
	}
	staffDeskMu.Lock()
	staffChecksMem = snap.Checks
	deskChatsMem = snap.Chats
	staffDeskMu.Unlock()
	log.Printf("[fleet] staff-desk loaded: %d check(s), %d chat", len(snap.Checks), len(snap.Chats))
}

func staffDeskSaveLoop() {
	for range staffDeskSaveCh {
		saveStaffDeskSync()
	}
}

func saveStaffDesk() {
	select {
	case staffDeskSaveCh <- struct{}{}:
	default:
	}
}

func saveStaffDeskSync() {
	staffDeskMu.RLock()
	snap := staffDeskFile{
		Checks:    append([]staffCheckView(nil), staffChecksMem...),
		Chats:     append([]deskChatView(nil), deskChatsMem...),
		UpdatedAt: time.Now(),
	}
	staffDeskMu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(staffDeskPersistPath), 0o755); err != nil {
		log.Printf("[fleet] save staff-desk mkdir: %v", err)
		return
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("[fleet] save staff-desk json: %v", err)
		return
	}
	tmp := staffDeskPersistPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("[fleet] save staff-desk: %v", err)
		return
	}
	if err := os.Rename(tmp, staffDeskPersistPath); err != nil {
		log.Printf("[fleet] save staff-desk rename: %v", err)
	}
}

func ingestStaffCheck(username string, anarchy any, reason string) {
	nick := strings.TrimSpace(username)
	if nick == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	now := time.Now().UTC().Format(time.RFC3339)
	staffDeskMu.Lock()
	for i := range staffChecksMem {
		if strings.EqualFold(staffChecksMem[i].Username, nick) && staffChecksMem[i].Status == "live" {
			if reason != "" {
				staffChecksMem[i].Reason = reason
			}
			if anarchy != nil {
				staffChecksMem[i].Anarchy = anarchy
			}
			staffDeskMu.Unlock()
			saveStaffDesk()
			return
		}
	}
	row := staffCheckView{
		ID:       fmt.Sprintf("%s-%d", strings.ToLower(nick), time.Now().UnixMilli()),
		Username: nick,
		Anarchy:  anarchy,
		Reason:   reason,
		At:       now,
		Status:   "live",
	}
	staffChecksMem = append([]staffCheckView{row}, staffChecksMem...)
	if len(staffChecksMem) > staffCheckMax {
		staffChecksMem = staffChecksMem[:staffCheckMax]
	}
	staffDeskMu.Unlock()
	log.Printf("[fleet] staff-check %s an=%v", nick, anarchy)
	saveStaffDesk()
}

func endStaffCheck(username string) {
	nick := strings.TrimSpace(username)
	if nick == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	staffDeskMu.Lock()
	changed := false
	for i := range staffChecksMem {
		if strings.EqualFold(staffChecksMem[i].Username, nick) && staffChecksMem[i].Status == "live" {
			staffChecksMem[i].Status = "done"
			staffChecksMem[i].EndedAt = now
			changed = true
		}
	}
	staffDeskMu.Unlock()
	if changed {
		log.Printf("[fleet] staff-check end %s", nick)
		saveStaffDesk()
	}
}

func ingestDeskChat(username, from, text string, anarchy any) {
	nick := strings.TrimSpace(username)
	text = strings.TrimSpace(text)
	if nick == "" || text == "" {
		return
	}
	if len(text) > 800 {
		text = text[:800]
	}
	from = strings.TrimSpace(from)
	if len(from) > 32 {
		from = from[:32]
	}
	now := time.Now().UTC().Format(time.RFC3339)
	staffDeskMu.Lock()
	checkID := ""
	for _, c := range staffChecksMem {
		if strings.EqualFold(c.Username, nick) && c.Status == "live" {
			checkID = c.ID
			if anarchy == nil {
				anarchy = c.Anarchy
			}
			break
		}
	}
	row := deskChatView{
		ID:       fmt.Sprintf("c%d", time.Now().UnixMilli()),
		CheckID:  checkID,
		Username: nick,
		Anarchy:  anarchy,
		From:     from,
		Text:     text,
		At:       now,
	}
	deskChatsMem = append(deskChatsMem, row)
	if len(deskChatsMem) > deskChatMax {
		deskChatsMem = deskChatsMem[len(deskChatsMem)-deskChatMax:]
	}
	staffDeskMu.Unlock()
	saveStaffDesk()
}

func nickWasCalledToCheck(username string) bool {
	key := strings.ToLower(strings.TrimSpace(username))
	if key == "" {
		return false
	}
	staffDeskMu.RLock()
	defer staffDeskMu.RUnlock()
	for _, c := range staffChecksMem {
		if strings.ToLower(strings.TrimSpace(c.Username)) == key {
			return true
		}
	}
	return false
}

func tagBansFromStaffChecks(bans []bannedBotView) {
	for i := range bans {
		if nickWasCalledToCheck(bans[i].Username) {
			bans[i].Kind = "staff_check"
		}
	}
}

func listStaffChecksForAPI() []staffCheckView {
	staffDeskMu.RLock()
	checks := append([]staffCheckView(nil), staffChecksMem...)
	chats := append([]deskChatView(nil), deskChatsMem...)
	staffDeskMu.RUnlock()

	by := make(map[string][]deskChatView, 8)
	loose := make([]deskChatView, 0)
	for _, ch := range chats {
		if ch.CheckID == "" {
			loose = append(loose, ch)
			continue
		}
		by[ch.CheckID] = append(by[ch.CheckID], ch)
	}
	out := make([]staffCheckView, 0, len(checks))
	for _, c := range checks {
		c.Chat = trimChatTail(by[c.ID], deskChatPerCheckAPI)
		out = append(out, c)
	}
	if len(loose) > 0 {
		out = append(out, staffCheckView{
			ID:       "loose",
			Username: "без проверки",
			Status:   "done",
			At:       "",
			Chat:     trimChatTail(loose, deskChatPerCheckAPI),
		})
	}
	return out
}

func trimChatTail(in []deskChatView, n int) []deskChatView {
	if n <= 0 || len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}

func staffCheckLiveCount() int {
	staffDeskMu.RLock()
	defer staffDeskMu.RUnlock()
	n := 0
	for _, c := range staffChecksMem {
		if c.Status == "live" {
			n++
		}
	}
	return n
}

func handleStaffCheckHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Username string `json:"username"`
		Anarchy  any    `json:"anarchy"`
		Reason   string `json:"reason"`
		End      bool   `json:"end"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.End {
		endStaffCheck(body.Username)
	} else {
		ingestStaffCheck(body.Username, body.Anarchy, body.Reason)
	}
	fleetJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleDeskChatHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Username string `json:"username"`
		Anarchy  any    `json:"anarchy"`
		From     string `json:"from"`
		Text     string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ingestDeskChat(body.Username, body.From, body.Text, body.Anarchy)
	fleetJSON(w, http.StatusOK, map[string]any{"ok": true})
}
