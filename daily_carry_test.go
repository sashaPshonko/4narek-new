package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPricesLookLikeFreshBaseSeed(t *testing.T) {
	itemsConfig = map[string]ItemConfig{
		"a-1.21": {BasePrice: 1000001},
		"b-1.21": {BasePrice: 2000002},
	}
	t.Cleanup(func() { itemsConfig = nil })

	if !pricesLookLikeFreshBaseSeed(map[string]int{"a-1.21": 1000001, "b-1.21": 2000002}) {
		t.Fatal("all base → seed")
	}
	if pricesLookLikeFreshBaseSeed(map[string]int{"a-1.21": 1000001, "b-1.21": 2100002}) {
		t.Fatal("one above base → not seed")
	}
	if pricesLookLikeFreshBaseSeed(map[string]int{"a-1.21": 1000001}) {
		t.Fatal("missing sku → not seed")
	}
}

func TestFindPreviousDailyFile(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	mustWrite := func(name string, prices map[string]int) {
		raw, _ := json.Marshal(DailyData{Date: name[5 : 5+10], Prices: prices})
		if err := os.WriteFile(name, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("data_2026-09-09.json", map[string]int{"x": 1})
	mustWrite("data_2026-09-10.json", map[string]int{"x": 2})
	mustWrite("data_2026-09-11.json", map[string]int{"x": 3})

	f, d, ok := findPreviousDailyFile("2026-09-11")
	if !ok || d != "2026-09-10" || f != "data_2026-09-10.json" {
		t.Fatalf("got %v %q %q", ok, f, d)
	}
	abs, _ := filepath.Abs(f)
	if _, err := os.Stat(abs); err != nil {
		t.Fatal(err)
	}
}
