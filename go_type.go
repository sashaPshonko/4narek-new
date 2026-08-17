package main

import "time"

const netheriteArmorGoType = "netherite_armor-1.21"

var netheriteArmorPieceTypes = map[string]struct{}{
	"netherite_helmet-1.21":     {},
	"netherite_chestplate-1.21": {},
	"netherite_leggings-1.21":   {},
	"netherite_boots-1.21":      {},
}

func isNetheriteArmorPieceType(minecraftType string) bool {
	_, ok := netheriteArmorPieceTypes[minecraftType]
	return ok
}

func catalogTypeActiveIn(activeTypes map[string]struct{}, minecraftType string) bool {
	if _, ok := activeTypes[minecraftType]; ok {
		return true
	}
	if isNetheriteArmorPieceType(minecraftType) {
		_, ok := activeTypes[netheriteArmorGoType]
		return ok
	}
	return false
}

func typeActiveSinceForCatalogType(minecraftType string) time.Time {
	if since, ok := typeActiveSince[minecraftType]; ok {
		return since
	}
	if isNetheriteArmorPieceType(minecraftType) {
		return typeActiveSince[netheriteArmorGoType]
	}
	return time.Time{}
}
