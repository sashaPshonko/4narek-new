package main

import "testing"

func TestShouldReplaceCatalogFromPreviousDay(t *testing.T) {
	// Save/restore global itemsConfig used by pricesLookLikeFreshBaseSeed.
	prev := itemsConfig
	defer func() { itemsConfig = prev }()
	itemsConfig = map[string]ItemConfig{
		"a": {BasePrice: 100},
		"b": {BasePrice: 200},
	}

	baseToday := map[string]int{"a": 100, "b": 200}
	ghostYday := map[string]int{"a": 10_000_000, "b": 8_000_000}
	saneToday := map[string]int{"a": 1_200_000, "b": 1_300_000}

	if !shouldReplaceCatalogFromPreviousDay(baseToday, ghostYday) {
		t.Fatal("pure base-seed today + non-base yesterday must replace")
	}
	if shouldReplaceCatalogFromPreviousDay(saneToday, ghostYday) {
		t.Fatal("sane today must NOT be overwritten by yesterday ghosts (sum heuristic removed)")
	}
	if shouldReplaceCatalogFromPreviousDay(baseToday, baseToday) {
		t.Fatal("yesterday also base — nothing to seed")
	}
	if shouldReplaceCatalogFromPreviousDay(ghostYday, saneToday) {
		t.Fatal("non-base today must not replaceAll")
	}
}
