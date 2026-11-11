package main

import "testing"

func TestNightByOnline(t *testing.T) {
	if !nightByOnline(3400, 12) {
		t.Fatalf("онлайн 3,4 тыс. — ночь независимо от часа")
	}
	if nightByOnline(12600, 3) {
		t.Fatalf("онлайн 12,6 тыс. — день независимо от часа")
	}
	if !nightByOnline(0, 23) || !nightByOnline(0, 5) || nightByOnline(0, 14) {
		t.Fatalf("без замера онлайна — по часам 22–09 UTC")
	}
}
