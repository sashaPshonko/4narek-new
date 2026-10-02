package main

import (
	"sort"
	"strings"

	"github.com/gorilla/websocket"
)

// fleetNickRoster — ники для funauth (часто live presence с орка).
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
	return fleetNickRoster
}

// banConfigRoster — ники из bots/*.json для фильтра/prune банов.
// Live presence с орка сюда нельзя: забаненных там часто уже нет → UI пустой
// при persisted>0, плюс prune сносит fleet_bans.json.
func banConfigRoster() funauthRoster {
	if skipFleetRosterReload {
		return fleetNickRoster
	}
	if r := loadFleetRunningNicks(); len(r) > 0 {
		return r
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

// collectRunningAnarchiesLocked — анки с живым WS-оркестратором.
// Раньше только clientOrchestratorAnarchy: если карта пуста/устарела — UI прятал
// все баны (persisted>0, total_banned=0), хотя presence уже пришёл.
func collectRunningAnarchiesLocked() map[int]struct{} {
	out := make(map[int]struct{})
	for ws := range clients {
		if an, ok := clientOrchestratorAnarchy[ws]; ok && an > 0 {
			out[an] = struct{}{}
		}
		for _, row := range clientOrchBots[ws] {
			if a := anarchyInt(row.Anarchy); a > 0 {
				out[a] = struct{}{}
			}
		}
		for _, o := range clientClanOwners[ws] {
			if a := anarchyInt(o.Anarchy); a > 0 {
				out[a] = struct{}{}
			}
		}
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
	u := strings.TrimSpace(b.Username)
	if u == "" {
		return false
	}
	if len(running) > 0 {
		if _, ok := running[an]; !ok {
			return false
		}
	} else if len(clients) == 0 {
		// совсем нет орков — не показываем
		return false
	}
	// roster пуст (ещё не пришёл presence) — всё равно покажем бан живой анки
	if len(roster) == 0 {
		return true
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
