package main

import (
	"testing"
	"time"
)

func TestLastPaidSellMaxUsesHighWaterNotLatest(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	since := now.Add(-6 * time.Hour)
	trades := []TradeLog{
		{Time: now.Add(-5 * time.Hour), Type: "sell", Price: 2_500_000},
		{Time: now.Add(-20 * time.Minute), Type: "sell", Price: 1_200_000},
		{Time: now.Add(-10 * time.Minute), Type: "buy", Price: 900_000},
		{Time: now.Add(-7 * time.Hour), Type: "sell", Price: 4_000_000}, // stale
	}
	got, at := lastPaidSellMax(trades, now, since)
	if got != 2_500_000 {
		t.Fatalf("high-water=%d want 2500000 (dump must not collapse cap)", got)
	}
	if !at.Equal(now.Add(-5 * time.Hour)) {
		t.Fatalf("at=%s", at)
	}
}

func TestLastPaidSellMaxEmpty(t *testing.T) {
	now := time.Now()
	got, _ := lastPaidSellMax(nil, now, now.Add(-time.Hour))
	if got != 0 {
		t.Fatalf("empty=%d", got)
	}
}

// Recover gate: near-paid = fair (hold_recover_fair), not priceFarBelowPaid.
func TestRecoverUnderpriceGap(t *testing.T) {
	step := 100_000
	paid := 2_500_000
	if priceFarBelowPaid(2_400_000, paid, step) {
		t.Fatal("paid-1step is fair — recover must not treat as underpriced")
	}
	if !priceFarBelowPaid(2_000_000, paid, step) {
		t.Fatal("paid-5step is underpriced — recover allowed")
	}
}
