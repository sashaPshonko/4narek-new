package main

import "time"

// legacy merged type — ещё матчим, если оркестратор шлёт netherite_armor-1.21
const netheriteArmorGoType = "netherite_armor-1.21"

var pieceGoTypeToName = map[string]string{
	"netherite_helmet-1.21":     "netherite_helmet",
	"netherite_chestplate-1.21": "netherite_chestplate",
	"netherite_leggings-1.21":   "netherite_leggings",
	"netherite_boots-1.21":      "netherite_boots",
}

func catalogTypeActiveIn(activeTypes map[string]struct{}, catalogType string) bool {
	if _, ok := activeTypes[catalogType]; ok {
		return true
	}
	return false
}

// botsForGoTypeLocked — живые боты на go-типе; merged armor и piece-типы считают вместе.
// Каталог брони = netherite_armor-1.21 (шлем/нагрудник/штаны/ботинки); орк может слать
// либо merged, либо piece (508/509) — share/bots_category должны видеть весь флот брони.
func botsForGoTypeLocked(goType string) int {
	totals := aggregateBotsPerTypeLocked()
	n := totals[goType]
	if goType == netheriteArmorGoType {
		for piece := range pieceGoTypeToName {
			n += totals[piece]
		}
		return n
	}
	if _, isPiece := pieceGoTypeToName[goType]; isPiece {
		return n + totals[netheriteArmorGoType]
	}
	return n
}

func itemConfigActiveIn(activeTypes map[string]struct{}, cfg ItemConfig) bool {
	if catalogTypeActiveIn(activeTypes, cfg.Type) {
		return true
	}
	// legacy: орк активировал piece-тип, а в каталоге ещё netherite_armor
	if cfg.Type == netheriteArmorGoType {
		for goType, name := range pieceGoTypeToName {
			if cfg.Name != name {
				continue
			}
			if _, ok := activeTypes[goType]; ok {
				return true
			}
		}
		return false
	}
	// legacy: орк активировал merged armor, а в каталоге уже piece-типы
	if _, ok := activeTypes[netheriteArmorGoType]; ok {
		if _, isPiece := pieceGoTypeToName[cfg.Type]; isPiece {
			return true
		}
	}
	return false
}

func itemConfigActiveLocked(cfg ItemConfig) bool {
	for _, types := range clientActiveTypes {
		if itemConfigActiveIn(types, cfg) {
			return true
		}
	}
	return false
}

func typeActiveSinceForItemConfig(cfg ItemConfig) time.Time {
	if since, ok := typeActiveSince[cfg.Type]; ok && !since.IsZero() {
		return since
	}
	if cfg.Type == netheriteArmorGoType {
		for goType, name := range pieceGoTypeToName {
			if cfg.Name != name {
				continue
			}
			if since, ok := typeActiveSince[goType]; ok && !since.IsZero() {
				return since
			}
		}
		return typeActiveSince[netheriteArmorGoType]
	}
	if _, isPiece := pieceGoTypeToName[cfg.Type]; isPiece {
		if since, ok := typeActiveSince[netheriteArmorGoType]; ok && !since.IsZero() {
			return since
		}
	}
	return time.Time{}
}

func itemConfigWasActiveForWindowLocked(cfg ItemConfig, windowStart time.Time) bool {
	if !itemConfigActiveLocked(cfg) {
		return false
	}
	since := typeActiveSinceForItemConfig(cfg)
	if since.IsZero() {
		return false
	}
	return !since.After(windowStart)
}
