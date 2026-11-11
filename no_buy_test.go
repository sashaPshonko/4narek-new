package main

import "testing"

func TestCatalogNacenkaNoBuy(t *testing.T) {
	if got := catalogNacenka("x", ItemConfig{NoBuy: true}); got != noBuyNacenka {
		t.Fatalf("no_buy: got %d", got)
	}
}
