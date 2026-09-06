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
	IPs        map[string]bannedIPView  `json:"ips"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

var (
	fleetPersistMu         sync.RWMutex
	persistedBannedBots    = make(map[string]bannedBotView)
	persistedClanOwnerBans = make(map[string]clanOwnerView)
	persistedBannedIPs     = make(map[string]bannedIPView)
)

func loadFleetBanPersist() {
	ensureFleetPersistLoop()
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
	if snap.IPs != nil {
		persistedBannedIPs = snap.IPs
	} else if persistedBannedIPs == nil {
		persistedBannedIPs = make(map[string]bannedIPView)
	}
	log.Printf("[fleet] bans loaded: %d bot(s), %d owner ban(s), %d ip(s)", len(persistedBannedBots), len(persistedClanOwnerBans), len(persistedBannedIPs))
}

func saveFleetBanPersist() {
	// Асинхронно: presence держит mutex.Lock, синхронный write на диск клинил весь Go.
	select {
	case fleetSaveReq <- struct{}{}:
	default:
	}
}

func saveFleetBanPersistSync() {
	fleetPersistMu.RLock()
	snap := fleetBansPersistFile{
		Bots:       cloneBannedBotMap(persistedBannedBots),
		ClanOwners: cloneClanOwnerMap(persistedClanOwnerBans),
		IPs:        cloneBannedIPMap(persistedBannedIPs),
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

func cloneBannedBotMap(in map[string]bannedBotView) map[string]bannedBotView {
	if in == nil {
		return map[string]bannedBotView{}
	}
	out := make(map[string]bannedBotView, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneClanOwnerMap(in map[string]clanOwnerView) map[string]clanOwnerView {
	if in == nil {
		return map[string]clanOwnerView{}
	}
	out := make(map[string]clanOwnerView, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBannedIPMap(in map[string]bannedIPView) map[string]bannedIPView {
	if in == nil {
		return map[string]bannedIPView{}
	}
	out := make(map[string]bannedIPView, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var (
	fleetSaveReq     = make(chan struct{}, 1)
	fleetSaveOnce    sync.Once
)

func ensureFleetPersistLoop() {
	fleetSaveOnce.Do(func() {
		go func() {
			for range fleetSaveReq {
				saveFleetBanPersistSync()
				// coalesce бурст presence
				for {
					select {
					case <-fleetSaveReq:
						continue
					default:
					}
					break
				}
				// один финальный flush после drain
				select {
				case <-fleetSaveReq:
					saveFleetBanPersistSync()
				default:
				}
			}
		}()
	})
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
	} else if prev.Reason != "" && !reasonIsChatBan(out.Reason) && reasonIsChatBan(prev.Reason) {
		out.Reason = prev.Reason
	}
	if out.Kind == "" && prev.Kind != "" {
		out.Kind = prev.Kind
	}
	if out.GoType == "" && prev.GoType != "" {
		out.GoType = prev.GoType
	}
	if anarchyInt(out.Anarchy) == 0 && anarchyInt(prev.Anarchy) != 0 {
		out.Anarchy = prev.Anarchy
	}
	if out.IP == "" && prev.IP != "" {
		out.IP = prev.IP
	}
	return out
}

func nickInCurrentRoster(roster funauthRoster, nick string, anarchy any) bool {
	if len(roster) == 0 {
		return true
	}
	an := anarchyInt(anarchy)
	if an > 0 {
		return roster.nickOnAnarchy(an, nick)
	}
	return roster.anarchyForNick(nick) > 0
}

func prunePersistedBansNotInRoster(roster funauthRoster) {
	if len(roster) == 0 {
		return
	}
	fleetPersistMu.Lock()
	changed := false
	for key, b := range persistedBannedBots {
		if nickInCurrentRoster(roster, b.Username, b.Anarchy) {
			continue
		}
		delete(persistedBannedBots, key)
		changed = true
	}
	for key, o := range persistedClanOwnerBans {
		if nickInCurrentRoster(roster, o.Username, o.Anarchy) {
			continue
		}
		delete(persistedClanOwnerBans, key)
		changed = true
	}
	fleetPersistMu.Unlock()
	if changed {
		saveFleetBanPersist()
	}
}

func prunePersistedOwnerBansNotInConfig(ownerRoster funauthRoster) {
	if len(ownerRoster) == 0 {
		return
	}
	fleetPersistMu.Lock()
	changed := false
	for key, o := range persistedClanOwnerBans {
		if ownerRoster.nickOnAnarchy(anarchyInt(o.Anarchy), o.Username) {
			continue
		}
		delete(persistedClanOwnerBans, key)
		changed = true
	}
	fleetPersistMu.Unlock()
	if changed {
		saveFleetBanPersist()
	}
}

func reasonIsChatBan(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "вы забанены") || strings.Contains(r, "проверка")
}

func ingestBannedBotsFromPresence(raw []bannedBotView) {
	if len(raw) == 0 {
		return
	}
	// Не звать currentFleetRoster(): он делает mutex.RLock, а presence держит mutex.Lock → deadlock, /fleet Failed to fetch.
	var roster funauthRoster
	if skipFleetRosterReload {
		roster = fleetNickRoster
	} else {
		roster = mergeClientOrchBotsLocked()
		if len(roster) == 0 {
			roster = loadFleetRunningNicks()
		}
		if len(roster) == 0 {
			roster = fleetNickRoster
		}
	}
	fleetPersistMu.Lock()
	for _, b := range raw {
		u := strings.TrimSpace(b.Username)
		if u == "" {
			continue
		}
		if !nickInCurrentRoster(roster, u, b.Anarchy) {
			if reasonIsChatBan(b.Reason) {
				rememberBannedIPLocked(b.IP, u, b.Reason, "presence", b.BannedAt)
			}
			continue
		}
		key := banUserKey(u)
		b.Username = u
		prev := persistedBannedBots[key]
		persistedBannedBots[key] = mergeBannedBotView(prev, b)
		if reasonIsChatBan(firstNonEmpty(b.Reason, prev.Reason)) {
			rememberBannedIPLocked(firstNonEmpty(b.IP, prev.IP), u, b.Reason, "presence", b.BannedAt)
		}
	}
	fleetPersistMu.Unlock()
	saveFleetBanPersist()
}

func ingestClanOwnersFromPresence(raw []clanOwnerView) {
	if len(raw) == 0 {
		return
	}
	roster := currentClanOwnerRoster()
	fleetPersistMu.Lock()
	for _, o := range raw {
		u := strings.TrimSpace(o.Username)
		if u == "" {
			continue
		}
		if len(roster) == 0 || !roster.nickOnAnarchy(anarchyInt(o.Anarchy), u) {
			if o.Banned || o.Status == "banned" {
				rememberBannedIPLocked(o.IP, u, o.Reason, "clan-owner", firstNonEmpty(o.BannedAt, o.CheckedAt))
			}
			continue
		}
		key := banUserKey(u)
		o.Username = u
		if o.Banned || o.Status == "banned" {
			rememberBannedIPLocked(o.IP, u, o.Reason, "clan-owner", firstNonEmpty(o.BannedAt, o.CheckedAt))
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
