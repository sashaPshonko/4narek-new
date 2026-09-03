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

func TestServerFunTimeRaiseAnomalous(t *testing.T) {
	now := time.Date(2026, 9, 2, 19, 13, 0, 0, time.UTC)
	prev := data.TradeHistory
	data.TradeHistory = map[string][]TradeLog{
		"шлем-1.21": {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-12 * time.Minute), Type: "buy", Price: 1},
		},
		"штаны-1.21": {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-8 * time.Minute), Type: "buy", Price: 1},
		},
	}
	t.Cleanup(func() { data.TradeHistory = prev })
	cycle := 10 * time.Minute
	if !serverFunTimeRaiseAnomalous(1_599_911, 3_550_011, "шлем-1.21", cycle, now) {
		t.Fatal("шлем +2кк при закупках")
	}
	if serverFunTimeRaiseAnomalous(600_005, 950_005, "штаны-1.21", cycle, now) {
		t.Fatal("штаны +350к — пол, не скачок")
	}
	if serverFunTimeRaiseAnomalous(1_599_911, 3_550_011, "пусто", cycle, now) {
		t.Fatal("нет закупок — могли занизить, мин принимаем")
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

func TestHardDownHasSales(t *testing.T) {
	if hardDownHasSales(0) {
		t.Fatal("без продаж не дампим")
	}
	if !hardDownHasSales(1) {
		t.Fatal("с продажей over можно")
	}
}

func TestShouldEaseStaleUnsold(t *testing.T) {
	if shouldEaseStaleUnsold(0, 0, 0, 5, 0, true) {
		t.Fatal("пустое не роняем")
	}
	if shouldEaseStaleUnsold(1, 0, 0, 0, 0, true) {
		t.Fatal("первый цикл без try — ещё hold")
	}
	if !shouldEaseStaleUnsold(1, 0, 0, 0, 2, true) {
		t.Fatal("висит на АХ — −step")
	}
	if !shouldEaseStaleUnsold(1, 0, 0, 3, 0, true) {
		t.Fatal("3 мёртвых цикла — −step")
	}
	if shouldEaseStaleUnsold(1, 0, 0, 3, 0, false) {
		t.Fatal("recover не застрял")
	}
}
