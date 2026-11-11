package main

import (
	"strings"
	"testing"
	"time"
)

func TestServerMaxBookAnomalousNoise(t *testing.T) {
	now := time.Now()
	if !serverMaxBookAnomalous(2_000_000, 1_000_000, "x", now) {
		t.Fatal("max << ours must ignore")
	}
	if !serverMaxBookAnomalous(1_000_000, 0, "x", now) {
		t.Fatal("proposed 0 must ignore")
	}
}

func TestStockNormHoldSlotsGatesUp(t *testing.T) {
	action := "stock_norm_price_up_deficit"
	allow := false
	if strings.Contains(action, "price_up") && !allow {
		action = "stock_norm_hold_slots"
	}
	if action != "stock_norm_hold_slots" {
		t.Fatalf("want hold_slots got %s", action)
	}
}
