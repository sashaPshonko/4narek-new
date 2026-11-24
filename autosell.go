package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Вкладка «Автопродажа»: sell-orch раз в 5 с шлёт по WS `sell_state` (активный селлер, балансы, очередь, заказы).
// Go ничего не считает сам — только хранит последний снимок и отдаёт его странице.
var (
	sellStateMu  sync.RWMutex
	sellStateRaw []byte
	sellStateAt  time.Time
	sellStateWS  *websocket.Conn
)

func ingestSellState(ws *websocket.Conn, raw []byte) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	sellStateMu.Lock()
	sellStateRaw, sellStateAt, sellStateWS = cp, time.Now(), ws
	sellStateMu.Unlock()
}

// sellStateDisconnect — оркестратор селла отключился: снимок остаётся, но помечается как «не на связи».
func sellStateDisconnect(ws *websocket.Conn) {
	sellStateMu.Lock()
	if sellStateWS == ws {
		sellStateWS = nil
	}
	sellStateMu.Unlock()
}

func sellStateView() map[string]any {
	sellStateMu.RLock()
	raw, at, ws := sellStateRaw, sellStateAt, sellStateWS
	sellStateMu.RUnlock()
	out := map[string]any{}
	if len(raw) == 0 {
		return map[string]any{"ok": true, "online": false, "empty": true}
	}
	_ = json.Unmarshal(raw, &out)
	age := time.Since(at)
	out["ok"] = true
	out["age_s"] = int(age.Seconds())
	out["online"] = ws != nil && age < 20*time.Second
	return out
}

func registerAutosellHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/autosell", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/autosell/", http.StatusFound)
	}))
	staticRoot, err := fs.Sub(autosellStaticFS, "autosell_static")
	if err != nil {
		log.Printf("[autosell] embed static: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.Handle("/autosell/", recoverHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/autosell/api/wait" {
			handleAutosellWait(w, r)
			return
		}
		if r.URL.Path == "/autosell/api/topup" {
			handleAutosellTopup(w, r)
			return
		}
		if r.URL.Path == "/autosell/api/rescue" {
			handleAutosellRescue(w, r)
			return
		}
		if r.URL.Path == "/autosell/api/state" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(sellStateView())
			return
		}
		r2 := *r
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, "/autosell")
		if u.Path == "" {
			u.Path = "/"
		}
		r2.URL = &u
		fileServer.ServeHTTP(w, &r2)
	})))
	startAutosellTopup()
	log.Printf("[autosell] страница готова: /autosell/")
}

// handleAutosellWait — кнопка «принять перевод»: просим sell-orch завести активного селлера и держать онлайн,
// пока ему не придёт перевод (on=false — снять). POST {"on":true|false}.
func handleAutosellWait(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "POST only"})
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	on := true
	if body.On != nil {
		on = *body.On
	}
	sellStateMu.RLock()
	ws := sellStateWS
	sellStateMu.RUnlock()
	if ws == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "sell-orch не на связи"})
		return
	}
	if err := wsWriteJSON(ws, map[string]any{"action": "sell_wait", "on": on}); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Printf("[autosell] кнопка «принять перевод» on=%v", on)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "on": on})
}

// handleAutosellRescue — перевести остаток с «залипшего» sell-бота на держателя. POST {"from":"nick","to":"nick"?}.
func handleAutosellRescue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "POST only"})
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body)
	from := strings.TrimSpace(body.From)
	if from == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "from required"})
		return
	}
	sellStateMu.RLock()
	ws := sellStateWS
	sellStateMu.RUnlock()
	if ws == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "sell-orch не на связи"})
		return
	}
	msg := map[string]any{"action": "sell_rescue", "from": from}
	if t := strings.TrimSpace(body.To); t != "" {
		msg["to"] = t
	}
	if err := wsWriteJSON(ws, msg); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Printf("[autosell] rescue %s → %q", from, body.To)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "from": from, "to": body.To})
}
