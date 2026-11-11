package main

import "testing"

func TestBookMinLotsDefault(t *testing.T) {
	if got := bookMinLotsForConfig(ItemConfig{}); got != 2 {
		t.Fatalf("по умолчанию 2, got %d", got)
	}
	if got := bookMinLotsForConfig(ItemConfig{BookMinLots: 1}); got != 1 {
		t.Fatalf("book_min_lots=1, got %d", got)
	}
}
