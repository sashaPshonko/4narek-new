package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type bannedBotView struct {
	Username string `json:"username"`
	Anarchy  any    `json:"anarchy"`
	GoType   string `json:"go_type,omitempty"`
	Role     string `json:"role,omitempty"`
	BannedAt string `json:"banned_at,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Source   string `json:"source,omitempty"` // оркестратор / анархия-группа
}

type fleetAnarchyView struct {
	Anarchy int             `json:"anarchy"`
	Banned  []bannedBotView `json:"banned"`
	Count   int             `json:"count"`
}

type fleetOverview struct {
	OK        bool               `json:"ok"`
	UpdatedAt time.Time          `json:"updated_at"`
	Total     int                `json:"total_banned"`
	Anarchies []fleetAnarchyView `json:"anarchies"`
	Banned    []bannedBotView    `json:"banned"`
}

// clientBannedBots — снимок забаненных с каждого WS-оркестратора.
var clientBannedBots = make(map[*websocket.Conn][]bannedBotView)

func registerFleetHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/fleet", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/fleet/", http.StatusFound)
	}))

	staticRoot, err := fs.Sub(fleetStaticFS, "fleet_static")
	if err != nil {
		log.Printf("[fleet] embed static: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.Handle("/fleet/", recoverHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/fleet/api/") {
			fleetAPI(w, r)
			return
		}
		r2 := *r
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, "/fleet")
		if u.Path == "" {
			u.Path = "/"
		}
		r2.URL = &u
		fileServer.ServeHTTP(w, &r2)
	})))
	log.Printf("[fleet] dashboard ready at /fleet/")
}

func fleetAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/fleet/api")
	if path == "/overview" && r.Method == http.MethodGet {
		fleetJSON(w, http.StatusOK, buildFleetOverview())
		return
	}
	fleetJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
}

func fleetJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func setClientBannedBots(ws *websocket.Conn, raw []bannedBotView) {
	if len(raw) == 0 {
		clientBannedBots[ws] = nil
		return
	}
	out := make([]bannedBotView, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, b := range raw {
		u := strings.TrimSpace(b.Username)
		if u == "" {
			continue
		}
		key := strings.ToLower(u)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		b.Username = u
		out = append(out, b)
	}
	clientBannedBots[ws] = out
}

func anarchyInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func buildFleetOverview() fleetOverview {
	now := time.Now()
	mutex.RLock()
	defer mutex.RUnlock()

	byUser := make(map[string]bannedBotView)
	for _, list := range clientBannedBots {
		for _, b := range list {
			key := strings.ToLower(b.Username)
			if prev, ok := byUser[key]; ok {
				// более свежий banned_at побеждает
				if b.BannedAt != "" && (prev.BannedAt == "" || b.BannedAt > prev.BannedAt) {
					byUser[key] = b
				}
				continue
			}
			byUser[key] = b
		}
	}

	all := make([]bannedBotView, 0, len(byUser))
	for _, b := range byUser {
		all = append(all, b)
	}
	sort.SliceStable(all, func(i, j int) bool {
		ai, aj := anarchyInt(all[i].Anarchy), anarchyInt(all[j].Anarchy)
		if ai != aj {
			return ai < aj
		}
		return all[i].Username < all[j].Username
	})

	groups := make(map[int][]bannedBotView)
	for _, b := range all {
		a := anarchyInt(b.Anarchy)
		groups[a] = append(groups[a], b)
	}
	keys := make([]int, 0, len(groups))
	for a := range groups {
		keys = append(keys, a)
	}
	sort.Ints(keys)

	anarchies := make([]fleetAnarchyView, 0, len(keys))
	for _, a := range keys {
		list := groups[a]
		anarchies = append(anarchies, fleetAnarchyView{
			Anarchy: a,
			Banned:  list,
			Count:   len(list),
		})
	}

	return fleetOverview{
		OK:        true,
		UpdatedAt: now,
		Total:     len(all),
		Anarchies: anarchies,
		Banned:    all,
	}
}
