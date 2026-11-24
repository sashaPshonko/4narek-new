package main

import (
	"testing"
	"time"
)

// sword7: конфиг 300к, шаг 50к, пороги hot: затоварка 13, повышение цены 10.
func reg(cur, onAH, buys, sales, p25 int) nacRegInput {
	return nacRegInput{
		Base: 300_000, Cur: cur, Step: 50_000, Price: 1_486_000,
		OnAH: onAH, Buys: buys, Sales: sales,
		DownStockMin: 13, UpStockMax: 10,
		BuyP25: p25, BuyP25OK: p25 > 0,
		SinceChange: time.Hour,
	}
}

func TestRaiseWhenBuyingTooMuchWithBloatedStock(t *testing.T) {
	// на АХ 78 (>26) → r*=0.5; купили 150 на 154 продаж → ratio 0.97 > 0.625 → вверх
	d := nacRegDecide(reg(300_000, 78, 150, 154, 850_000))
	if d.New != 350_000 || d.Reason != "flow_up" {
		t.Fatalf("got %+v", d)
	}
}

func TestHoldWhenFlowAlreadyOnTarget(t *testing.T) {
	// купили 70 из 154 → 0.45: между 0.3 и 0.625 → не трогаем
	d := nacRegDecide(reg(450_000, 78, 70, 154, 850_000))
	if d.New != 450_000 || d.Reason != "hold" {
		t.Fatalf("got %+v", d)
	}
}

func TestLowerWhenThrottledTooHard(t *testing.T) {
	// купили 20 из 154 → 0.13 < 0.3 → вниз
	d := nacRegDecide(reg(450_000, 78, 20, 154, 850_000))
	if d.New != 400_000 || d.Reason != "flow_down" {
		t.Fatalf("got %+v", d)
	}
}

func TestNeverBelowBaseOnFlowDown(t *testing.T) {
	d := nacRegDecide(reg(300_000, 78, 5, 154, 850_000))
	if d.New != 300_000 || d.Reason != "hold" {
		t.Fatalf("got %+v", d)
	}
}

func TestCooldownBlocksChanges(t *testing.T) {
	in := reg(300_000, 78, 150, 154, 850_000)
	in.SinceChange = 10 * time.Minute
	if d := nacRegDecide(in); d.New != 300_000 || d.Reason != "cooldown" {
		t.Fatalf("got %+v", d)
	}
}

func TestFewSalesAtNightDoNotRaise(t *testing.T) {
	d := nacRegDecide(reg(300_000, 78, 5, 5, 850_000))
	if d.New != 300_000 || d.Reason != "hold_few_sales" {
		t.Fatalf("got %+v", d)
	}
}

func TestCapByMultiplierAndOwnBuys(t *testing.T) {
	// конфиг×1.5 = 450к
	d := nacRegDecide(reg(450_000, 78, 150, 154, 500_000))
	if d.New != 450_000 || d.Reason != "at_cap" {
		t.Fatalf("got %+v", d)
	}
	// цена 1486к − p25 покупок 1200к = 286к < конфига → потолок = конфиг
	d = nacRegDecide(reg(300_000, 78, 150, 154, 1_200_000))
	if d.New != 300_000 || d.Reason != "at_cap" {
		t.Fatalf("got %+v", d)
	}
}

func TestOverCapClampsImmediately(t *testing.T) {
	in := reg(600_000, 78, 70, 154, 850_000)
	in.SinceChange = 0 // даже в кулдауне
	if d := nacRegDecide(in); d.New != 450_000 || d.Reason != "over_cap" {
		t.Fatalf("got %+v", d)
	}
}

func TestStockNormalStepsBackToBase(t *testing.T) {
	d := nacRegDecide(reg(400_000, 12, 20, 40, 850_000))
	if d.New != 350_000 || d.Reason != "stock_ok_down" {
		t.Fatalf("got %+v", d)
	}
}

func TestStockLowResetsAtOnce(t *testing.T) {
	in := reg(450_000, 5, 20, 40, 850_000)
	in.SinceChange = 0
	if d := nacRegDecide(in); d.New != 300_000 || d.Reason != "stock_low_reset" {
		t.Fatalf("got %+v", d)
	}
}

func TestNoBuyHistoryNoRaise(t *testing.T) {
	d := nacRegDecide(reg(300_000, 78, 150, 154, 0))
	if d.New != 300_000 || d.Reason != "hold_no_buy_history" {
		t.Fatalf("got %+v", d)
	}
}

func TestFreeSlotsDoNotRaiseAndReturnToBase(t *testing.T) {
	in := reg(300_000, 78, 150, 154, 850_000)
	in.FreeSlots = 6
	if d := nacRegDecide(in); d.New != 300_000 || d.Reason != "free_slots_hold" {
		t.Fatalf("не должны поднимать при свободных слотах: %+v", d)
	}
	in = reg(400_000, 78, 150, 154, 850_000)
	in.FreeSlots = 6
	if d := nacRegDecide(in); d.New != 350_000 || d.Reason != "free_slots_down" {
		t.Fatalf("должны опускаться к конфигу: %+v", d)
	}
	in.FreeSlots = 3 // мало свободных — обычная логика потока
	if d := nacRegDecide(in); d.Reason != "flow_up" {
		t.Fatalf("got %+v", d)
	}
}
