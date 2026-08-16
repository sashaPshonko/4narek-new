package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const fleetBansPersistPath = "ml_data/fleet_bans.json"

type fleetBansPersistFile struct {
	Bots       map[string]bannedBotView `json:"bots"`
	ClanOwners map[string]clanOwnerView `json:"clan_owners"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

var (
	fleetPersistMu         sync.RWMutex
	persistedBannedBots    = make(map[string]bannedBotView)
	persistedClanOwnerBans = make(map[string]clanOwnerView)
)

func loadFleetBanPersist() {
	raw, err := os.ReadFile(fleetBansPersistPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[fleet] load bans: %v", err)
		}
		return
	}
	var snap fleetBansPersistFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		log.Printf("[fleet] parse bans: %v", err)
		return
	}
	fleetPersistMu.Lock()
	defer fleetPersistMu.Unlock()
	if snap.Bots != nil {
		persistedBannedBots = snap.Bots
	}
	if snap.ClanOwners != nil {
		persistedClanOwnerBans = snap.ClanOwners
	}
	log.Printf("[fleet] bans loaded: %d bot(s), %d owner ban(s)", len(persistedBannedBots), len(persistedClanOwnerBans))
}

func saveFleetBanPersist() {
	fleetPersistMu.RLock()
	snap := fleetBansPersistFile{
		Bots:       persistedBannedBots,
		ClanOwners: persistedClanOwnerBans,
		UpdatedAt:  time.Now(),
	}
	fleetPersistMu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(fleetBansPersistPath), 0o755); err != nil {
		log.Printf("[fleet] save bans mkdir: %v", err)
		return
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("[fleet] save bans marshal: %v", err)
		return
	}
	tmp := fleetBansPersistPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("[fleet] save bans write: %v", err)
		return
	}
	if err := os.Rename(tmp, fleetBansPersistPath); err != nil {
		_ = os.WriteFile(fleetBansPersistPath, raw, 0o644)
	}
}

func banUserKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func mergeBannedBotView(prev, next bannedBotView) bannedBotView {
	out := next
	if prev.Username != "" && out.Username == "" {
		out.Username = prev.Username
	}
	if out.BannedAt == "" || (prev.BannedAt != "" && prev.BannedAt > out.BannedAt) {
		if prev.BannedAt != "" {
			out.BannedAt = prev.BannedAt
		}
	}
	if out.Reason == "" && prev.Reason != "" {
		out.Reason = prev.Reason
	}
	if out.GoType == "" && prev.GoType != "" {
		out.GoType = prev.GoType
	}
	if out.Role == "" && prev.Role != "" {
		out.Role = prev.Role
	}
	if anarchyInt(out.Anarchy) == 0 && anarchyInt(prev.Anarchy) != 0 {
		out.Anarchy = prev.Anarchy
	}
	return out
}

func ingestBannedBotsFromPresence(raw []bannedBotView) {
	if len(raw) == 0 {
		return
	}
	fleetPersistMu.Lock()
	for _, b := range raw {
		u := strings.TrimSpace(b.Username)
		if u == "" {
			continue
		}
		key := banUserKey(u)
		b.Username = u
		prev := persistedBannedBots[key]
		persistedBannedBots[key] = mergeBannedBotView(prev, b)
	}
	fleetPersistMu.Unlock()
	saveFleetBanPersist()
}

func ingestClanOwnersFromPresence(raw []clanOwnerView) {
	if len(raw) == 0 {
		return
	}
	fleetPersistMu.Lock()
	for _, o := range raw {
		u := strings.TrimSpace(o.Username)
		if u == "" {
			continue
		}
		key := banUserKey(u)
		o.Username = u
		if o.Role == "" {
			o.Role = "clan_owner"
		}
		if o.Banned || o.Status == "banned" {
			prev, had := persistedClanOwnerBans[key]
			merged := o
			if had {
				if merged.BannedAt == "" {
					merged.BannedAt = prev.BannedAt
				}
				if merged.Reason == "" {
					merged.Reason = prev.Reason
				}
				if merged.CheckedAt == "" {
					merged.CheckedAt = prev.CheckedAt
				}
			}
			persistedClanOwnerBans[key] = merged
			continue
		}
		if o.Status == "ok" && o.CheckedAt != "" {
			delete(persistedClanOwnerBans, key)
		}
	}
	fleetPersistMu.Unlock()
	saveFleetBanPersist()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func mergeLiveAndPersistedBanned() map[string]bannedBotView {
	fleetPersistMu.RLock()
	defer fleetPersistMu.RUnlock()
	byUser := make(map[string]bannedBotView, len(persistedBannedBots)+8)
	for key, b := range persistedBannedBots {
		byUser[key] = b
	}
	for _, list := range clientBannedBots {
		for _, b := range list {
			key := banUserKey(b.Username)
			if key == "" {
				continue
			}
			if prev, ok := byUser[key]; ok {
				byUser[key] = mergeBannedBotView(prev, b)
				continue
			}
			byUser[key] = b
		}
	}
	return byUser
}

func applyPersistedClanOwnerBans(owners []clanOwnerView) []clanOwnerView {
	fleetPersistMu.RLock()
	defer fleetPersistMu.RUnlock()
	if len(persistedClanOwnerBans) == 0 {
		return owners
	}
	byUser := make(map[string]clanOwnerView, len(owners)+len(persistedClanOwnerBans))
	for _, o := range owners {
		byUser[banUserKey(o.Username)] = o
	}
	for key, persisted := range persistedClanOwnerBans {
		live, ok := byUser[key]
		if !ok {
			byUser[key] = persisted
			continue
		}
		if live.Status == "ok" && live.CheckedAt != "" &&
			(persisted.CheckedAt == "" || live.CheckedAt > persisted.CheckedAt) {
			continue
		}
		if live.Status == "error" || live.Status == "pending" || live.Banned || live.Status == "banned" {
			merged := live
			merged.Status = "banned"
			merged.Banned = true
			if merged.BannedAt == "" {
				merged.BannedAt = persisted.BannedAt
			}
			if merged.Reason == "" {
				merged.Reason = persisted.Reason
			}
			if merged.CheckedAt == "" {
				merged.CheckedAt = persisted.CheckedAt
			}
			byUser[key] = merged
		}
	}
	out := make([]clanOwnerView, 0, len(byUser))
	for _, o := range byUser {
		out = append(out, o)
	}
	return out
}
