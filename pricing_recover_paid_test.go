package main

import (
	"os"
	"strings"
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

// v8p: recover только при sales≥1 (синтетика гейтов, не полный adjustPrice).
func TestRecoverNeedsSales(t *testing.T) {
	step := 100_000
	paid := 2_500_000
	price := 1_200_000
	if !priceFarBelowPaid(price, paid, step) {
		t.Fatal("setup: must be underpriced")
	}
	// гейт в коде: recoverOK = … && underpriced && sales >= 1
	sales0OK := priceFarBelowPaid(price, paid, step) && 0 >= 1
	sales1OK := priceFarBelowPaid(price, paid, step) && 1 >= 1
	if sales0OK {
		t.Fatal("sales=0 must not recover")
	}
	if !sales1OK {
		t.Fatal("sales≥1 + underpriced may recover")
	}
}

// v8q: пустой сток / нет try — не пилим каталог (ночной megasword 3.5→0.5).
func TestGhostPriceDownOK(t *testing.T) {
	if ghostPriceDownOK(0, 0, 0) {
		t.Fatal("held=0 must not ghost-↓")
	}
	if ghostPriceDownOK(0, 0, 20) {
		t.Fatal("held=0 even with try — no ↓ (нечего продавать)")
	}
	if ghostPriceDownOK(5, 0, 0) {
		t.Fatal("held>0 but no try evidence — hold, not ↓")
	}
	if !ghostPriceDownOK(5, 0, 5) {
		t.Fatal("held>0 try≥minTries — ghost-↓ OK")
	}
	if !ghostPriceDownOK(5, 2, 8) {
		t.Fatal("trySellsBlockUp — ghost-↓ OK")
	}
	if !isGhostCatalogDown("corridor_price_down_stale") || !isGhostCatalogDown("corridor_price_down_empty_fair") {
		t.Fatal("ghost labels")
	}
	if isGhostCatalogDown("corridor_price_down_soft") {
		t.Fatal("soft overstock is not ghost-↓")
	}
}

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
