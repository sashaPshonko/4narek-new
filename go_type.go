package main

import "time"

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

func itemConfigActiveIn(activeTypes map[string]struct{}, cfg ItemConfig) bool {
	if catalogTypeActiveIn(activeTypes, cfg.Type) {
		return true
	}
	if cfg.Type != netheriteArmorGoType {
		return false
	}
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
	if cfg.Type != netheriteArmorGoType {
		return time.Time{}
	}
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
