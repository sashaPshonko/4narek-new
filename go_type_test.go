package main

import "testing"

func TestCatalogTypeActiveIn(t *testing.T) {
	active := map[string]struct{}{
		netheriteArmorGoType: {},
	}
	if !catalogTypeActiveIn(active, "netherite_helmet-1.21") {
		t.Fatal("helmet should be active via netherite_armor")
	}
	if catalogTypeActiveIn(active, "позорная-броня-1.21") {
		t.Fatal("pozor should not match armor go type")
	}
	if !catalogTypeActiveIn(active, netheriteArmorGoType) {
		t.Fatal("direct armor go type")
	}
}
