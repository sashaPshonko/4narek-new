package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

type mcBotProxyRow struct {
	Username string `json:"username"`
	IP       string `json:"ip"`
}

type mcOwnerProxyRow struct {
	Username string `json:"username"`
	IP       string `json:"ip"`
}

// lookupMCNickSOCKS — SOCKS фермы/овнера для ника (ip.json / owner-ip.json). Пусто = нет слота.
func lookupMCNickSOCKS(nick string) string {
	nk := strings.ToLower(strings.TrimSpace(nick))
	if nk == "" {
		return ""
	}
	dir := fleetBotsDir()
	if dir == "" {
		return ""
	}
	root := filepath.Dir(dir)
	if u := lookupBotJSONSOCKS(dir, filepath.Join(root, "ip.json"), nk); u != "" {
		return u
	}
	return lookupOwnerJSONSOCKS(filepath.Join(root, "clan-owners.json"), filepath.Join(root, "owner-ip.json"), nk)
}

func lookupBotJSONSOCKS(botsDir, ipJSONPath, nickLower string) string {
	slots, err := os.ReadDir(botsDir)
	if err != nil {
		return ""
	}
	rawMap, err := os.ReadFile(ipJSONPath)
	if err != nil {
		return ""
	}
	var ipMap map[string]string
	if json.Unmarshal(rawMap, &ipMap) != nil {
		return ""
	}
	for _, e := range slots {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(botsDir, name))
		if err != nil {
			continue
		}
		var rows []mcBotProxyRow
		if json.Unmarshal(raw, &rows) != nil {
			continue
		}
		for _, row := range rows {
			if !strings.EqualFold(strings.TrimSpace(row.Username), nickLower) {
				continue
			}
			slot := strings.TrimSpace(row.IP)
			if slot == "" {
				return ""
			}
			return strings.TrimSpace(ipMap[slot])
		}
	}
	return ""
}

func lookupOwnerJSONSOCKS(ownersPath, ownerIPPath, nickLower string) string {
	rawOwners, err := os.ReadFile(ownersPath)
	if err != nil {
		return ""
	}
	rawIP, err := os.ReadFile(ownerIPPath)
	if err != nil {
		return ""
	}
	var owners map[string]json.RawMessage
	if json.Unmarshal(rawOwners, &owners) != nil {
		return ""
	}
	var ipMap map[string]string
	if json.Unmarshal(rawIP, &ipMap) != nil {
		return ""
	}
	for key, blob := range owners {
		if key == "myNick" {
			continue
		}
		var row mcOwnerProxyRow
		if json.Unmarshal(blob, &row) != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(row.Username), nickLower) {
			continue
		}
		slot := strings.TrimSpace(row.IP)
		if slot == "" {
			slot = key
		}
		return strings.TrimSpace(ipMap[slot])
	}
	return ""
}

var farmLoginRR uint32

func farmSlotUnused(slot string) bool {
	s := strings.TrimSpace(slot)
	switch s {
	case "507", "508", "509", "510":
		return true
	}
	for _, p := range []string{"507-", "508-", "509-", "510-"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func listFarmLoginSOCKS() []string {
	dir := fleetBotsDir()
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "ip.json"))
	if err != nil {
		return nil
	}
	var ipMap map[string]string
	if json.Unmarshal(raw, &ipMap) != nil {
		return nil
	}
	type pair struct{ slot, url string }
	var pairs []pair
	for slot, u := range ipMap {
		u = strings.TrimSpace(u)
		if u == "" || farmSlotUnused(slot) {
			continue
		}
		pairs = append(pairs, pair{slot, u})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].slot < pairs[j].slot })
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.url)
	}
	return out
}

// pickFarmLoginSOCKS — жилой SOCKS фермы для логина TG (не казахский xray). Пусто → xray.
func pickFarmLoginSOCKS() string {
	list := listFarmLoginSOCKS()
	if len(list) == 0 {
		return ""
	}
	i := atomic.AddUint32(&farmLoginRR, 1)
	return list[int(i-1)%len(list)]
}

func socksURLHost(proxyURL string) string {
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || u.Hostname() == "" {
		return normalizeProxyHost(proxyURL)
	}
	return u.Hostname()
}
