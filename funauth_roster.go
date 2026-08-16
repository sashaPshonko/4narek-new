package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
)

const funauthRosterFile = "funauth_roster.json"

// anarchy → lowercased nicks (owner + bots).
type funauthRoster map[int]map[string]struct{}

func loadFunauthRoster() funauthRoster {
	path := funauthRosterFile
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[funauth] roster %s: %v (anarchy=1 TG без лимита по составу)", path, err)
		return nil
	}
	var rawMap map[string][]string
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		log.Printf("[funauth] roster parse: %v", err)
		return nil
	}
	out := make(funauthRoster, len(rawMap))
	for key, nicks := range rawMap {
		an, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || an <= 0 {
			continue
		}
		set := make(map[string]struct{}, len(nicks))
		for _, n := range nicks {
			nk := strings.ToLower(strings.TrimSpace(n))
			if nk != "" {
				set[nk] = struct{}{}
			}
		}
		if len(set) > 0 {
			out[an] = set
		}
	}
	log.Printf("[funauth] roster: %d anarchy(ies)", len(out))
	return out
}

func (r funauthRoster) anarchyForNick(nick string) int {
	if len(r) == 0 {
		return 0
	}
	key := strings.ToLower(strings.TrimSpace(nick))
	for an, nicks := range r {
		if _, ok := nicks[key]; ok {
			return an
		}
	}
	return 0
}

func (r funauthRoster) size(anarchy int) int {
	if anarchy <= 0 {
		return 0
	}
	return len(r[anarchy])
}

func (r funauthRoster) complete(anarchy int, nickToAccount map[string]string, accountID string) bool {
	need := r[anarchy]
	if len(need) == 0 || accountID == "" {
		return false
	}
	for nick := range need {
		if nickToAccount[nick] != accountID {
			return false
		}
	}
	return true
}
