package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

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
	dir := fleetBotsDir()
	if dir == "" {
		return false
	}
	out := make(funauthRoster)
	loadFleetBotsJSON(dir, out)
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

func currentClanOwnerRoster() funauthRoster {
	if skipFleetRosterReload {
		return fleetNickRoster
	}
	path := fleetOwnersFile()
	if path == "" {
		return nil
	}
	out := make(funauthRoster)
	loadFleetClanOwnersJSON(path, out)
	if len(out) == 0 {
		return nil
	}
	return out
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
