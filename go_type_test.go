package main

import "testing"

func TestItemConfigActiveIn(t *testing.T) {
	armorOnly := map[string]struct{}{netheriteArmorGoType: {}}
	cfgBoots := ItemConfig{Name: "netherite_boots", Type: netheriteArmorGoType}
	if !itemConfigActiveIn(armorOnly, cfgBoots) {
		t.Fatal("netherite_armor active → boots item")
	}

	chestOnly := map[string]struct{}{"netherite_chestplate-1.21": {}}
	cfgChest := ItemConfig{Name: "netherite_chestplate", Type: netheriteArmorGoType}
	if !itemConfigActiveIn(chestOnly, cfgChest) {
		t.Fatal("chestplate goType → chestplate item")
	}
	if itemConfigActiveIn(chestOnly, cfgBoots) {
		t.Fatal("chestplate goType must not activate boots item")
	}

	pozor := ItemConfig{Name: "netherite_helmet", Type: "позорная-броня-1.21"}
	if itemConfigActiveIn(armorOnly, pozor) {
		t.Fatal("pozor type separate from netherite_armor")
	}
}
