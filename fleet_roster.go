package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Ники из bots/*.json присылает сам оркестратор в presence (config_bots).
// Go файлы фермы не читает: по этому списку решает, какие баны и проверки показывать.

var (
	configRosterMu sync.RWMutex
	// configBotsByWS — последний присланный список каждого оркестратора.
	configBotsByWS = make(map[*websocket.Conn]funauthRoster)
	// configRosterMerged — объединение по всем подключённым оркестраторам.
	// Анарка есть в карте, даже если в её JSON не осталось ников.
	configRosterMerged funauthRoster
)

// setClientConfigBots — nil bots (оркестратор не смог прочитать JSON) оставляет прошлый список.
func setClientConfigBots(ws *websocket.Conn, bots *[]orchBotNick, anarchy int) {
	if ws == nil || bots == nil {
		return
	}
	r := make(funauthRoster)
	if anarchy > 0 {
		r[anarchy] = make(map[string]struct{})
	}
	for _, row := range *bots {
		addFleetNick(r, anarchyInt(row.Anarchy), row.Username)
	}
	configRosterMu.Lock()
	configBotsByWS[ws] = r
	rebuildConfigRosterLocked()
	configRosterMu.Unlock()
}

func deleteClientConfigBots(ws *websocket.Conn) {
	configRosterMu.Lock()
	if _, ok := configBotsByWS[ws]; ok {
		delete(configBotsByWS, ws)
		rebuildConfigRosterLocked()
	}
	configRosterMu.Unlock()
}

func rebuildConfigRosterLocked() {
	merged := make(funauthRoster)
	for _, r := range configBotsByWS {
		for an, nicks := range r {
			if merged[an] == nil {
				merged[an] = make(map[string]struct{}, len(nicks))
			}
			for nk := range nicks {
				merged[an][nk] = struct{}{}
			}
		}
	}
	if len(merged) == 0 {
		merged = nil
	}
	configRosterMerged = merged
}

func currentConfigRoster() funauthRoster {
	configRosterMu.RLock()
	defer configRosterMu.RUnlock()
	return configRosterMerged
}

// rosterAllowsNick — показывать ли ник. Анарка, от которой оркестратор ещё
// не присылал список, не фильтруется (иначе отключённый орк стирал бы чужие баны).
func rosterAllowsNick(roster funauthRoster, nick string, anarchy any) bool {
	if len(roster) == 0 {
		return true
	}
	if an := anarchyInt(anarchy); an > 0 {
		if _, known := roster[an]; !known {
			return true
		}
		return roster.nickOnAnarchy(an, nick)
	}
	return roster.anarchyForNick(nick) > 0
}

type orchBotNick struct {
	Username  string `json:"username"`
	Anarchy   any    `json:"anarchy"`
	GoType    string `json:"go_type,omitempty"`
	Success   bool   `json:"success"`
	OnAnarchy bool   `json:"on_anarchy"`
	Banned    bool   `json:"banned"`
	Unstable  bool   `json:"unstable"`
	Live      bool   `json:"live"`
	// Баланс и запасы бота (от оркестратора): по ним Go решает, может ли бот сейчас что-то купить.
	Balance     *float64 `json:"balance,omitempty"`
	BalanceAgeS *int64   `json:"balance_age_s,omitempty"`
	// Карта или список (формат зависит от версии оркестратора): разбираем сами (botCounts), чтобы необязательное
	// поле не роняло разбор всего presence.
	Items     json.RawMessage `json:"items,omitempty"`
	Inventory json.RawMessage `json:"inventory,omitempty"`
	// Слотов АХ у бота: 5, а пока у него арендованы дополнительные слоты — 10 (0 = оркестратор не прислал, считаем 5).
	Slots       int   `json:"slots,omitempty"`
	RentUntilMs int64 `json:"rent_until_ms,omitempty"`
}

// clientOrchBots — список ников с оркестратора (fleet/presence).
var clientOrchBots = make(map[*websocket.Conn][]orchBotNick)

func mergeClientOrchBotsLocked() funauthRoster {
	out := make(funauthRoster)
	for _, rows := range clientOrchBots {
		for _, row := range rows {
			addFleetNick(out, anarchyInt(row.Anarchy), row.Username)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyLiveOrchRosterLocked() {
	r := mergeClientOrchBotsLocked()
	if len(r) == 0 {
		r = loadFleetRunningNicks()
	}
	n := 0
	for _, set := range r {
		n += len(set)
	}
	if rosterNickCount(fleetNickRoster) != n || len(fleetNickRoster) != len(r) {
		logOrchRoster(r, n)
	}
	fleetNickRoster = r
	if funauthPoolInst != nil {
		funauthPoolInst.replaceRoster(r)
	}
}

func rosterNickCount(r funauthRoster) int {
	n := 0
	for _, set := range r {
		n += len(set)
	}
	return n
}

func logOrchRoster(r funauthRoster, nicks int) {
	if len(r) == 0 {
		return
	}
	log.Printf("[funauth] roster from orchestrators: %d anarchy(ies), %d nicks", len(r), nicks)
}

func setClientOrchBots(ws *websocket.Conn, bots []orchBotNick) {
	if ws == nil {
		return
	}
	if len(bots) == 0 {
		delete(clientOrchBots, ws)
	} else {
		clientOrchBots[ws] = bots
	}
	applyLiveOrchRosterLocked()
}

func deleteClientOrchBots(ws *websocket.Conn) {
	if _, ok := clientOrchBots[ws]; !ok {
		return
	}
	delete(clientOrchBots, ws)
	applyLiveOrchRosterLocked()
}

// fleetNickRoster — ники для funauth (часто live presence с орка).
var fleetNickRoster funauthRoster

// skipFleetRosterReload — тесты подставляют roster в память, без чтения файла.
var skipFleetRosterReload bool

// banConfigRoster — ники из bots/*.json, которые присылает оркестратор (config_bots).
// Live presence (bots) сюда нельзя: забаненных там часто уже нет → UI пустой
// при persisted>0, плюс prune сносит fleet_bans.json.
func banConfigRoster() funauthRoster {
	if skipFleetRosterReload {
		return fleetNickRoster
	}
	return currentConfigRoster()
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
	// Живой presence уже прислал этот бан — показываем всегда.
	// Иначе при пустом clientOrchestratorAnarchy было total=0 при banned=3.
	if banLiveOnConnectedOrch(u) {
		return true
	}
	if len(running) > 0 {
		if _, ok := running[an]; !ok {
			return false
		}
	} else if len(clients) == 0 {
		return false
	}
	// roster пуст (ещё не пришёл presence) — всё равно покажем бан живой анки
	if len(roster) == 0 {
		return true
	}
	return roster.nickOnAnarchy(an, u)
}

func banLiveOnConnectedOrch(username string) bool {
	key := banUserKey(username)
	if key == "" {
		return false
	}
	for ws, list := range clientBannedBots {
		if _, ok := clients[ws]; !ok {
			continue
		}
		for _, b := range list {
			if banUserKey(b.Username) == key {
				return true
			}
		}
	}
	return false
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

func fleetBotsDir() string {
	if d := strings.TrimSpace(os.Getenv("FLEET_BOTS_DIR")); d != "" {
		return d
	}
	for _, d := range []string{
		filepath.Join("..", "4narek-1.12", "bots"),
		"bots",
		"/root/4narek-1.12/bots",
	} {
		st, err := os.Stat(d)
		if err == nil && st.IsDir() {
			return d
		}
	}
	return ""
}

func addFleetNick(r funauthRoster, anarchy int, nick string) {
	nk := strings.ToLower(strings.TrimSpace(nick))
	if r == nil || anarchy <= 0 || nk == "" {
		return
	}
	if r[anarchy] == nil {
		r[anarchy] = make(map[string]struct{})
	}
	r[anarchy][nk] = struct{}{}
}

type fleetBotJSONRow struct {
	Username string `json:"username"`
	Anarchy  any    `json:"anarchy"`
}

func loadFleetBotsJSON(dir string, out funauthRoster) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var rows []fleetBotJSONRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			continue
		}
		for _, row := range rows {
			addFleetNick(out, anarchyInt(row.Anarchy), row.Username)
		}
	}
}

func loadFleetClanOwnersJSON(path string, out funauthRoster) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var blobs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blobs); err != nil {
		log.Printf("[fleet] clan-owners.json: %v", err)
		return
	}
	for key, blob := range blobs {
		blob = bytes.TrimSpace(blob)
		if len(blob) == 0 || blob[0] != '{' {
			continue
		}
		var row fleetBotJSONRow
		if err := json.Unmarshal(blob, &row); err != nil {
			continue
		}
		an := anarchyInt(row.Anarchy)
		if an <= 0 {
			an = anarchyInt(key)
		}
		addFleetNick(out, an, row.Username)
	}
}

func nickIsClanOwnerOf(nick string, anarchy int) bool {
	nk := strings.ToLower(strings.TrimSpace(nick))
	if nk == "" || anarchy <= 0 {
		return false
	}
	r := currentClanOwnerRoster()
	if r == nil || r[anarchy] == nil {
		return false
	}
	_, ok := r[anarchy][nk]
	return ok
}

func nickIsFarmBot(nick string) bool {
	nk := strings.ToLower(strings.TrimSpace(nick))
	if nk == "" {
		return false
	}
	out := make(funauthRoster)
	if dir := fleetBotsDir(); dir != "" {
		loadFleetBotsJSON(dir, out)
	}
	for an, set := range fleetNickRoster { // ники, присланные оркестраторами (Go отдельно от фермы)
		for n := range set {
			addFleetNick(out, an, n)
		}
	}
	for _, set := range out {
		if _, ok := set[nk]; ok {
			return true
		}
	}
	return false
}

func fleetOwnersFile() string {
	if p := strings.TrimSpace(os.Getenv("FLEET_OWNERS_FILE")); p != "" {
		return p
	}
	dir := fleetBotsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), "clan-owners.json")
}

// liveOwners — овнеры кланов, пришедшие от оркестраторов по WS (presence.clan_owners). Отдельный замок:
// currentClanOwnerRoster зовут и под общим mutex, и без него.
var (
	liveOwnersMu sync.RWMutex
	liveOwners   funauthRoster
)

// refreshLiveOwnersLocked пересобирает liveOwners из clientClanOwners (вызывать под общим mutex).
func refreshLiveOwnersLocked() {
	r := make(funauthRoster)
	for _, list := range clientClanOwners {
		for _, o := range list {
			addFleetNick(r, anarchyInt(o.Anarchy), o.Username)
		}
	}
	liveOwnersMu.Lock()
	liveOwners = r
	liveOwnersMu.Unlock()
}

func liveOwnersCopy() funauthRoster {
	liveOwnersMu.RLock()
	defer liveOwnersMu.RUnlock()
	if len(liveOwners) == 0 {
		return nil
	}
	out := make(funauthRoster, len(liveOwners))
	for an, set := range liveOwners {
		m := make(map[string]struct{}, len(set))
		for k := range set {
			m[k] = struct{}{}
		}
		out[an] = m
	}
	return out
}

// currentClanOwnerRoster — овнеры кланов: файл clan-owners.json (если Go стоит рядом с фермой), иначе то, что
// оркестраторы сами прислали по WS. Так Go работает отдельно от фермы, без доступа к её файлам.
func currentClanOwnerRoster() funauthRoster {
	if skipFleetRosterReload {
		return fleetNickRoster
	}
	if path := fleetOwnersFile(); path != "" {
		out := make(funauthRoster)
		loadFleetClanOwnersJSON(path, out)
		if len(out) > 0 {
			return out
		}
	}
	return liveOwnersCopy()
}

// loadFleetRunningNicks — ники из bots/*.json + clan-owners.json (текущий запуск).
func loadFleetRunningNicks() funauthRoster {
	dir := fleetBotsDir()
	if dir == "" {
		return nil
	}
	out := make(funauthRoster)
	loadFleetBotsJSON(dir, out)
	if path := fleetOwnersFile(); path != "" {
		loadFleetClanOwnersJSON(path, out)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func loadFleetNickRoster() {
	if r := loadFleetRunningNicks(); len(r) > 0 {
		fleetNickRoster = r
		n := 0
		for _, set := range r {
			n += len(set)
		}
		log.Printf("[fleet] running nicks: %d anarchy(ies), %d nicks from %s", len(r), n, fleetBotsDir())
		return
	}
	log.Printf("[fleet] running nicks: пусто (нет %s)", fleetBotsDir())
}
