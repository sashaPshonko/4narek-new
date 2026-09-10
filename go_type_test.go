package main

import "testing"

func TestItemConfigActiveIn(t *testing.T) {
	bootsType := map[string]struct{}{"netherite_boots-1.21": {}}
	cfgBoots := ItemConfig{Name: "netherite_boots", Type: "netherite_boots-1.21"}
	if !itemConfigActiveIn(bootsType, cfgBoots) {
		t.Fatal("boots goType → boots item")
	}

	chestOnly := map[string]struct{}{"netherite_chestplate-1.21": {}}
	cfgChest := ItemConfig{Name: "netherite_chestplate", Type: "netherite_chestplate-1.21"}
	if !itemConfigActiveIn(chestOnly, cfgChest) {
		t.Fatal("chestplate goType → chestplate item")
	}
	if itemConfigActiveIn(chestOnly, cfgBoots) {
		t.Fatal("chestplate goType must not activate boots item")
	}

	// legacy catalog type still matched by piece activation
	legacyBoots := ItemConfig{Name: "netherite_boots", Type: netheriteArmorGoType}
	if !itemConfigActiveIn(bootsType, legacyBoots) {
		t.Fatal("piece goType → legacy armor catalog row")
	}

	armorOnly := map[string]struct{}{netheriteArmorGoType: {}}
	if !itemConfigActiveIn(armorOnly, cfgBoots) {
		t.Fatal("legacy netherite_armor active → piece catalog row")
	}

	pozor := ItemConfig{Name: "netherite_helmet", Type: "позорная-броня-1.21"}
	if itemConfigActiveIn(bootsType, pozor) {
		t.Fatal("pozor type separate from piece types")
	}
}
