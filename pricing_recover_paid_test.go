package main

import (
	"os"
	"strings"
	"testing"
)

// Гарантия: empty_fair больше нигде не вызывается через applyDown.
func TestNoEmptyFairApplyDownInSource(t *testing.T) {
	b, err := os.ReadFile("pricing.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `applyDown("corridor_price_down_empty_fair"`) {
		t.Fatal("corridor_price_down_empty_fair must not be applied")
	}
}
