package main

import (
	"testing"
)

func TestBook2ExpensiveSKU(t *testing.T) {
	if !book2ExpensiveSKU("megasword-яд3-1.21", 1_000_000) {
		t.Fatal("megasword by name")
	}
	if !book2ExpensiveSKU("pochti-megasword-1.21", 500_000) {
		t.Fatal("pochti by name")
	}
	if !book2ExpensiveSKU("sword7-1.21", 3_000_000) {
		t.Fatal("high sell is expensive")
	}
	if book2ExpensiveSKU("sword7-1.21", 1_000_000) {
		t.Fatal("volume sword7 not expensive by name/price")
	}
}
