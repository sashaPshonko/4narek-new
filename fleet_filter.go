package main

import (
	"sort"
	"strings"

	"github.com/gorilla/websocket"
)

// fleetNickRoster — ники запущенных аккаунтов (bots/*.json + владельцы), не весь funauth roster.
var fleetNickRoster funauthRoster

// skipFleetRosterReload — тесты подставляют roster в память, без чтения файла.
var skipFleetRosterReload bool

func currentFleetRoster() funauthRoster {
	if skipFleetRosterReload {
		return fleetNickRoster
	}
	mutex.RLock()
	live := mergeClientOrchBotsLocked()
	mutex.RUnlock()
	if len(live) > 0 {
		fleetNickRoster = live
		return live
	}
	if r := loadFleetRunningNicks(); len(r) > 0 {
		fleetNickRoster = r
		return r
	}
	if r := loadFunauthRosterFile(false); len(r) > 0 {
		fleetNickRoster = r
	}
	return fleetNickRoster
}

// clientOrchestratorAnarchy — anarchy подключённого оркестратора (по presence).
var clientOrchestratorAnarchy = make(map[*websocket.Conn]int)

func inferOrchestratorAnarchy(banned []bannedBotView, owners []clanOwnerView) int {
	for _, o := range owners {
		if a := anarchyInt(o.Anarchy); a > 0 {
			return a
		}
	}
	for _, b := range banned {
		if a := anarchyInt(b.Anarchy); a > 0 {
			return a
		}
	}
	return 0
}

func setClientOrchestratorAnarchy(ws *websocket.Conn, anarchy int) {
	if anarchy <= 0 {
		return
	}
	clientOrchestratorAnarchy[ws] = anarchy
}

func deleteClientOrchestratorAnarchy(ws *websocket.Conn) {
	delete(clientOrchestratorAnarchy, ws)
}

// collectRunningAnarchiesLocked — анки с подключённым оркестратором (WS в clients).
func collectRunningAnarchiesLocked() map[int]struct{} {
	out := make(map[int]struct{})
	for ws, an := range clientOrchestratorAnarchy {
		if an <= 0 {
			continue
		}
		if _, ok := clients[ws]; !ok {
			continue
		}
		out[an] = struct{}{}
	}
	return out
}

func (r funauthRoster) nickOnAnarchy(anarchy int, nick string) bool {
	if anarchy <= 0 || len(r) == 0 {
		return false
	}
	nicks, ok := r[anarchy]
	if !ok {
		return false
	}
	_, ok = nicks[banUserKey(nick)]
	return ok
}

func isBannedVisibleInFleet(b bannedBotView, running map[int]struct{}, roster funauthRoster) bool {
	an := anarchyInt(b.Anarchy)
	if an <= 0 {
		return false
	}
	if len(running) > 0 {
		if _, ok := running[an]; !ok {
			return false
		}
	} else {
		// ни один оркестратор не онлайн — не показываем баны
		return false
	}
	u := strings.TrimSpace(b.Username)
	if u == "" {
		return false
	}
	return roster.nickOnAnarchy(an, u)
}

func filterBannedForFleet(all []bannedBotView, running map[int]struct{}, roster funauthRoster) []bannedBotView {
	if len(all) == 0 {
		return nil
	}
	out := make([]bannedBotView, 0, len(all))
	for _, b := range all {
		if isBannedVisibleInFleet(b, running, roster) {
			out = append(out, b)
		}
	}
	return out
}

func filterClanOwnersForFleet(all []clanOwnerView, _ map[int]struct{}, roster funauthRoster) []clanOwnerView {
	if len(all) == 0 {
		return nil
	}
	out := make([]clanOwnerView, 0, len(all))
	for _, o := range all {
		an := anarchyInt(o.Anarchy)
		if an <= 0 || strings.TrimSpace(o.Username) == "" {
			continue
		}
		if len(roster) == 0 || !roster.nickOnAnarchy(an, o.Username) {
			continue
		}
		out = append(out, o)
	}
	return out
}

func groupBannedByAnarchy(all []bannedBotView) []fleetAnarchyView {
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
	return anarchies
}
