package main

import (
	"testing"
	"time"
)

func TestServerMinBuyBlocksUp(t *testing.T) {
	now := time.Date(2026, 9, 2, 19, 13, 0, 0, time.UTC)
	item := "шлем-1.21"
	cycle := 10 * time.Minute
	prev := data.TradeHistory
	t.Cleanup(func() { data.TradeHistory = prev })
	data.TradeHistory = map[string][]TradeLog{
		item: {{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1}},
	}
	if serverMinBuyBlocksUp(item, cycle, now) {
		t.Fatal("одна покупка — шум")
	}
	data.TradeHistory = map[string][]TradeLog{
		item: {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-15 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-6 * time.Minute), Type: "sell", Price: 1},
			{Time: now.Add(-7 * time.Minute), Type: "sell", Price: 1},
			{Time: now.Add(-8 * time.Minute), Type: "sell", Price: 1},
		},
	}
	if serverMinBuyBlocksUp(item, cycle, now) {
		t.Fatal("продаём больше чем закупаем — FunTime ↑ не режем закупками")
	}
	data.TradeHistory = map[string][]TradeLog{
		item: {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-15 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-6 * time.Minute), Type: "sell", Price: 1},
		},
	}
	if !serverMinBuyBlocksUp(item, cycle, now) {
		t.Fatal("закуп не меньше продаж — не поднимаем")
	}
}

func TestServerFunTimeRaiseAnomalousNoBook(t *testing.T) {
	now := time.Date(2026, 9, 2, 19, 13, 0, 0, time.UTC)
	oldDB := mlDB
	mlDB = nil
	t.Cleanup(func() { mlDB = oldDB })
	cycle := 10 * time.Minute
	prev := data.TradeHistory
	data.TradeHistory = map[string][]TradeLog{}
	t.Cleanup(func() { data.TradeHistory = prev })
	if serverFunTimeRaiseAnomalous(1_599_911, 3_550_011, "шлем-1.21", cycle, now) {
		t.Fatal("без книги и без закупок — не угадываем")
	}
	data.TradeHistory = map[string][]TradeLog{
		"шлем-1.21": {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-12 * time.Minute), Type: "buy", Price: 1},
		},
	}
	if !serverFunTimeRaiseAnomalous(1_599_911, 3_550_011, "шлем-1.21", cycle, now) {
		t.Fatal("живой закуп — не поднимаем даже без книги")
	}
}

func TestStockTargetsSmallShareWidensDown(t *testing.T) {
	def := stockBandFracs{
		lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
		over: stockOverFrac, dump: stockDumpFrac,
	}
	lo, hi, _, _, _ := stockTargets(21, def)
	if lo != 2 || hi != 6 {
		t.Fatalf("armor share=21 band [%d,%d] want [2,6]", lo, hi)
	}
	lo48, hi48, _, _, _ := stockTargets(48, def)
	if lo48 < 8 || hi48-lo48 < corridorMinBandSpan {
		t.Fatalf("large share [%d,%d]", lo48, hi48)
	}
	poz := stockBandFracs{
		lo: pozorBandLoFrac, hi: pozorBandHiFrac, soft: pozorSoftDownFrac,
		over: pozorOverFrac, dump: pozorDumpFrac,
	}
	plo, phi, _, _, _ := stockTargets(10, poz)
	if plo != 1 || phi != 2 {
		t.Fatalf("pozor share=10 must stay 10–18%% [%d,%d]", plo, phi)
	}
}
