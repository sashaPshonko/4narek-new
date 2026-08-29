package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type bannedIPView struct {
	IP       string   `json:"ip"`
	Banned   bool     `json:"banned"`
	Known    bool     `json:"known"`
	BannedAt string   `json:"banned_at,omitempty"`
	Nicks    []string `json:"nicks,omitempty"`
	Note     string   `json:"note,omitempty"`
	Source   string   `json:"source,omitempty"`
}

type assignedProxyView struct {
	IP     string `json:"ip"`
	Slot   string `json:"slot"`
	Kind   string `json:"kind"` // bot | owner | sell
	Banned bool   `json:"banned"`
}

func normalizeProxyHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	s = strings.Trim(s, "[]")
	return s
}

func rememberBannedIPLocked(ip, nick, note, source, bannedAt string) {
	ip = normalizeProxyHost(ip)
	if ip == "" {
		return
	}
	if persistedBannedIPs == nil {
		persistedBannedIPs = make(map[string]bannedIPView)
	}
	prev := persistedBannedIPs[ip]
	out := prev
	out.IP = ip
	out.Banned = true
	out.Known = true
	if out.BannedAt == "" {
		out.BannedAt = firstNonEmpty(bannedAt, prev.BannedAt, time.Now().UTC().Format(time.RFC3339))
	}
	if note != "" {
		out.Note = note
	}
	if source != "" {
		out.Source = source
	} else if out.Source == "" {
		out.Source = "presence"
	}
	nk := strings.ToLower(strings.TrimSpace(nick))
	if nk != "" {
		seen := false
		for _, x := range out.Nicks {
			if strings.EqualFold(x, nk) {
				seen = true
				break
			}
		}
		if !seen {
			out.Nicks = append(append([]string{}, out.Nicks...), nick)
		}
	}
	persistedBannedIPs[ip] = out
}

func setBannedIP(ip string, banned bool, note, nick, source string) bannedIPView {
	ip = normalizeProxyHost(ip)
	now := time.Now().UTC().Format(time.RFC3339)
	fleetPersistMu.Lock()
	if persistedBannedIPs == nil {
		persistedBannedIPs = make(map[string]bannedIPView)
	}
	prev := persistedBannedIPs[ip]
	out := prev
	out.IP = ip
	out.Known = true
	out.Banned = banned
	if note != "" {
		out.Note = note
	}
	if source != "" {
		out.Source = source
	} else if out.Source == "" {
		out.Source = "manual"
	}
	if banned {
		if out.BannedAt == "" {
			out.BannedAt = now
		}
	}
	nk := strings.TrimSpace(nick)
	if nk != "" {
		seen := false
		for _, x := range out.Nicks {
			if strings.EqualFold(x, nk) {
				seen = true
				break
			}
		}
		if !seen {
			out.Nicks = append(append([]string{}, out.Nicks...), nk)
		}
	}
	if ip != "" {
		persistedBannedIPs[ip] = out
	}
	fleetPersistMu.Unlock()
	if ip != "" {
		saveFleetBanPersist()
	}
	return lookupBannedIP(ip)
}

func lookupBannedIP(raw string) bannedIPView {
	ip := normalizeProxyHost(raw)
	if ip == "" {
		return bannedIPView{Known: false, Banned: false}
	}
	fleetPersistMu.RLock()
	defer fleetPersistMu.RUnlock()
	if v, ok := persistedBannedIPs[ip]; ok {
		v.IP = ip
		v.Known = true
		return v
	}
	return bannedIPView{IP: ip, Known: false, Banned: false}
}

func listBannedIPs() []bannedIPView {
	fleetPersistMu.RLock()
	defer fleetPersistMu.RUnlock()
	out := make([]bannedIPView, 0, len(persistedBannedIPs))
	for _, v := range persistedBannedIPs {
		if !v.Banned {
			continue
		}
		v.Known = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func ipIsBanned(ip string) bool {
	ip = normalizeProxyHost(ip)
	if ip == "" {
		return false
	}
	fleetPersistMu.RLock()
	defer fleetPersistMu.RUnlock()
	v, ok := persistedBannedIPs[ip]
	return ok && v.Banned
}

func loadAssignedProxies() []assignedProxyView {
	var out []assignedProxyView
	seen := map[string]struct{}{}
	add := func(slot, kind, raw string) {
		ip := normalizeProxyHost(raw)
		if ip == "" {
			return
		}
		key := kind + "|" + slot + "|" + ip
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, assignedProxyView{
			IP:     ip,
			Slot:   slot,
			Kind:   kind,
			Banned: ipIsBanned(ip),
		})
	}

	dir := fleetBotsDir()
	parent := ""
	if dir != "" {
		parent = filepath.Dir(dir)
		addMapFile(filepath.Join(parent, "ip.json"), "bot", add)
		addMapFile(filepath.Join(parent, "owner-ip.json"), "owner", add)
	}
	for _, p := range []string{
		strings.TrimSpace(os.Getenv("FLEET_SELL_BOT")),
		filepath.Join("..", "sell", "sellbot", "bot.json"),
		"/root/sell/sellbot/bot.json",
	} {
		if p == "" {
			continue
		}
		addSellBotFile(p, add)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Slot != out[j].Slot {
			return out[i].Slot < out[j].Slot
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func addMapFile(path, kind string, add func(slot, kind, raw string)) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for slot, url := range m {
		add(slot, kind, url)
	}
}

func addSellBotFile(path string, add func(slot, kind, raw string)) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rows []struct {
		Username string `json:"username"`
		Proxy    string `json:"proxy"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for _, row := range rows {
		slot := row.Username
		if slot == "" {
			slot = "sell"
		}
		add(slot, "sell", row.Proxy)
	}
}

func markAssignedProxiesBanned() int {
	n := 0
	now := time.Now().UTC().Format(time.RFC3339)
	assigned := loadAssignedProxies()
	fleetPersistMu.Lock()
	for _, a := range assigned {
		if a.IP == "" {
			continue
		}
		rememberBannedIPLocked(a.IP, "", "текущий используемый прокси", "used-config", now)
		n++
	}
	fleetPersistMu.Unlock()
	saveFleetBanPersist()
	return n
}

type bannedIPReq struct {
	IP     string `json:"ip"`
	Banned *bool  `json:"banned"`
	Note   string `json:"note"`
	Nick   string `json:"nick"`
	Source string `json:"source"`
}

func handleBannedIPHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := firstNonEmpty(r.URL.Query().Get("ip"), r.URL.Query().Get("q"))
		fleetJSON(w, http.StatusOK, map[string]any{"ok": true, "result": lookupBannedIP(q)})
		return
	case http.MethodPost:
		var body bannedIPReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		ip := normalizeProxyHost(body.IP)
		if ip == "" {
			http.Error(w, "ip required", http.StatusBadRequest)
			return
		}
		banned := true
		if body.Banned != nil {
			banned = *body.Banned
		}
		src := body.Source
		if src == "" {
			src = "manual"
		}
		out := setBannedIP(ip, banned, body.Note, body.Nick, src)
		log.Printf("[fleet] banned-ip http %s banned=%v", ip, banned)
		fleetJSON(w, http.StatusOK, map[string]any{"ok": true, "result": out})
		return
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func handleMarkUsedIPsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	n := markAssignedProxiesBanned()
	fleetJSON(w, http.StatusOK, map[string]any{"ok": true, "marked": n, "banned_ips": listBannedIPs()})
}
