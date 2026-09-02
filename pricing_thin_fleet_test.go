package main

import (
	"testing"
	"time"
)

func TestDemandMinSalesForUpThinFleet(t *testing.T) {
	if got := demandMinSalesForUp(2, false); got != 2 {
		t.Fatalf("day thin=%d want 2", got)
	}
	if got := demandMinSalesForUp(2, true); got != 3 {
		t.Fatalf("night thin=%d want 3", got)
	}
	if got := demandMinSalesForUp(3, false); got != corridorMinSalesForUp {
		t.Fatalf("day normal=%d want %d", got, corridorMinSalesForUp)
	}
}

func TestDemandStrongEnoughForUpWithMin(t *testing.T) {
	if !demandStrongEnoughForUp(2, 2, 2, false) {
		t.Fatal("sustained 2x2 day")
	}
	if demandStrongEnoughForUp(2, 1, 3, false) {
		t.Fatal("sales=2 < min 3")
	}
	if demandStrongEnoughForUp(2, 2, 3, true) {
		t.Fatal("night sales=2 < min 3")
	}
}

func TestServerBoundMuchAbove(t *testing.T) {
	ours := 1_599_911
	if !serverBoundMuchAbove(ours, 3_550_011) {
		t.Fatal("шлем 02.09 1.6→3.55")
	}
	if !serverBoundMuchAbove(1_430_011, 2_800_011) {
		t.Fatal("шлем 16.07 ~+96%")
	}
	if serverBoundMuchAbove(899_999, 1_110_099) {
		t.Fatal("штаны +23% — обычный мин, не скачок")
	}
	if serverBoundMuchAbove(ours, 1_450_000) {
		t.Fatal("ниже нашей — не raise")
	}
}

func TestServerFunTimeRaiseAnomalous(t *testing.T) {
	if serverFunTimeRaiseAnomalous(1_600_000, 3_000_000, "no-such", 10*time.Minute, time.Now()) {
		t.Fatal("нет сделок — не аномалия")
	}
	now := time.Date(2026, 9, 2, 19, 13, 0, 0, time.UTC)
	prev := data.TradeHistory
	data.TradeHistory = map[string][]TradeLog{
		"шлем-1.21": {
			{Time: now.Add(-5 * time.Minute), Type: "sell", Price: 1},
			{Time: now.Add(-12 * time.Minute), Type: "sell", Price: 1},
			{Time: now.Add(-20 * time.Minute), Type: "sell", Price: 1},
			{Time: now.Add(-6 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-18 * time.Minute), Type: "buy", Price: 1},
		},
	}
	t.Cleanup(func() { data.TradeHistory = prev })
	if !serverFunTimeRaiseAnomalous(1_599_911, 3_550_011, "шлем-1.21", 10*time.Minute, now) {
		t.Fatal("02.09: +122% при buy+sell за 3 цикла")
	}
}

func TestRecoverPaidCap(t *testing.T) {
	step := 100_000
	paid := 2_500_000
	if recoverPaidCap(paid, step) != paid+2*step {
		t.Fatalf("cap=%d", recoverPaidCap(paid, step))
	}
	if recoverBlockedByPaidCap(paid, paid, step) {
		t.Fatal("at last paid should still allow +K probe")
	}
	if !recoverBlockedByPaidCap(paid+2*step, paid, step) {
		t.Fatal("at cap must block")
	}
	if !recoverBlockedByPaidCap(1_200_000, 0, step) {
		t.Fatal("no sells → block recover")
	}
	// dump: current 1.2M, last paid 2.5M — recover must climb
	if recoverBlockedByPaidCap(1_200_000, paid, step) {
		t.Fatal("after dump below paid should recover")
	}
}
