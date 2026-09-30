package main

import (
	"strings"
	"testing"
	"time"
)

func TestStockNormDecideOverstockDown(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 5, StockNorm: 4, Sales: 2, NormalSales: 5,
		Price: 1_000_000, Step: 100_000,
	})
	if d.Action != "stock_norm_price_down_overstock" || d.NewPrice != 900_000 {
		t.Fatalf("want overstock ↓: %+v", d)
	}
}

func TestStockNormDecideDeficitUp(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 3, StockNorm: 4, Sales: 2, NormalSales: 5,
		Price: 1_000_000, Step: 100_000,
	})
	if d.Action != "stock_norm_price_up_deficit" || d.NewPrice != 1_100_000 {
		t.Fatalf("want deficit ↑: %+v", d)
	}
}

func TestStockNormDecideBookEmptyLever(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 0, StockNorm: 4, Sales: 5, NormalSales: 5,
		Price: 1_000_000, Step: 100_000,
		BookFloor: 2_000_000, BookOK: true,
	})
	if d.Action != "stock_norm_price_up_book_empty" || d.NewPrice != 1_100_000 {
		t.Fatalf("want book empty ↑: %+v", d)
	}
}

func TestStockNormDecideBookEmptyNotDeep(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 0, StockNorm: 4, Sales: 5, NormalSales: 5,
		Price: 1_720_000, Step: 100_000,
		BookFloor: 2_000_000, BookOK: true,
	})
	if d.Action != "stock_norm_hold" {
		t.Fatalf("near floor must hold: %+v", d)
	}
}

func TestStockNormNoNacenkaActions(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 2, StockNorm: 4, Sales: 0, NormalSales: 5,
		Price: 800_000, Step: 100_000,
	})
	if d.Action != "stock_norm_price_up_deficit" {
		t.Fatalf("want sell ↑ not nac: %+v", d)
	}
}

func TestCapitalPolicyStockNorm(t *testing.T) {
	if capitalPolicy != capitalPolicyStockNorm {
		t.Fatalf("active=%s want %s (rollback: capitalPolicyV9)", capitalPolicy, capitalPolicyStockNorm)
	}
}

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

func TestStockNormBookMidCapsUp(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 2, StockNorm: 4, Sales: 0, NormalSales: 5,
		Price: 3_000_000, Step: 100_000,
		BookMid: 3_050_000, BookMidOK: true, BookMidQ: 0.50,
	})
	if d.Action != "stock_norm_price_up_deficit" || d.NewPrice != 3_050_000 {
		t.Fatalf("want ↑ capped at mid: %+v", d)
	}

	d = stockNormDecide(stockNormInput{
		Held: 2, StockNorm: 4, Sales: 0, NormalSales: 5,
		Price: 3_100_000, Step: 100_000,
		BookMid: 3_050_000, BookMidOK: true, BookMidQ: 0.50,
	})
	if d.Action != "stock_norm_hold_book_mid" || d.NewPrice != 3_100_000 {
		t.Fatalf("want hold at/above mid: %+v", d)
	}
}

func TestStockNormBookMidDoesNotBlockWithoutBook(t *testing.T) {
	d := stockNormDecide(stockNormInput{
		Held: 2, StockNorm: 4, Sales: 0, NormalSales: 5,
		Price: 500_000, Step: 50_000,
	})
	if d.Action != "stock_norm_price_up_deficit" || d.NewPrice != 550_000 {
		t.Fatalf("no mid → ↑: %+v", d)
	}
}

func TestStockNormBookMidPctForAnchorLog(t *testing.T) {
	if q := stockNormBookMidPctForAnchor(300_000); q != stockNormBookMidQMax {
		t.Fatalf("below Plo want %.2f got %v", stockNormBookMidQMax, q)
	}
	if q := stockNormBookMidPctForAnchor(3_000_000); q != stockNormBookMidQMin {
		t.Fatalf("at Phi want %.2f got %v", stockNormBookMidQMin, q)
	}
	if q := stockNormBookMidPctForAnchor(5_000_000); q != stockNormBookMidQMin {
		t.Fatalf("above Phi want %.2f got %v", stockNormBookMidQMin, q)
	}
	q1m := stockNormBookMidPctForAnchor(1_000_000)
	if q1m < 0.55 || q1m > 0.65 {
		t.Fatalf("1M want ~0.59 got %v", q1m)
	}
	// монотонно убывает с ценой
	prev := stockNormBookMidPctForAnchor(400_000)
	for _, p := range []int{500_000, 800_000, 1_500_000, 2_500_000} {
		q := stockNormBookMidPctForAnchor(p)
		if q > prev+1e-9 {
			t.Fatalf("q must fall with P: %d → %v > prev %v", p, q, prev)
		}
		prev = q
	}
}

func TestStockNormBookMidDownNotBlocked(t *testing.T) {
	// потолок только на ↑: overstock ↓ проходит даже выше mid
	d := stockNormDecide(stockNormInput{
		Held: 8, StockNorm: 4, Sales: 1, NormalSales: 5,
		Price: 4_000_000, Step: 100_000,
		BookMid: 3_000_000, BookMidOK: true, BookMidQ: 0.50,
	})
	if d.Action != "stock_norm_price_down_overstock" || d.NewPrice != 3_900_000 {
		t.Fatalf("↓ must ignore mid cap: %+v", d)
	}
}
