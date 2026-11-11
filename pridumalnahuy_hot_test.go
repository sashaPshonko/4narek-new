package main

import "testing"

func hotIn() pridumalnahuyInput {
	return pridumalnahuyInput{
		Hot: true, Price: 1_000_000, Step: 50_000,
		HotDownSalesMax: 4, HotDownStockMin: 11, HotUpSalesMax: 6, HotUpStockMax: 8,
		BotsCount: 10, OnAHCategory: 10,
	}
}

func TestHotUpCountsOnAHNotInventory(t *testing.T) {
	in := hotIn()
	in.Sales, in.OnAHItem, in.InvItem, in.TotalStockItem = 2, 3, 6, 9 // всего 9 ≥ 8, но на АХ только 3
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_050_000 || d.Reason != "hot_low_stock" {
		t.Fatalf("на АХ мало → вверх, got %+v", d)
	}
	in.OnAHItem = 8
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("на АХ уже 8 → hold, got %+v", d)
	}
}

func TestHotPeakHoldsAndOverstockDown(t *testing.T) {
	in := hotIn()
	in.Sales, in.OnAHItem = 9, 2 // пик: цену не двигаем
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("пик продаж → hold, got %+v", d)
	}
	in.Sales, in.OnAHItem = 1, 12
	if d := pridumalnahuyDecide(in); d.NewPrice != 950_000 || d.Reason != "hot_overstock" {
		t.Fatalf("затоварка на АХ → вниз, got %+v", d)
	}
}

func TestHotSlotsTight(t *testing.T) {
	in := hotIn()
	in.Sales, in.OnAHItem, in.OnAHCategory = 1, 2, 48 // свободно 2, нужно 6
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price || d.Reason != "hot_slots_tight" {
		t.Fatalf("мало слотов → hold, got %+v", d)
	}
}
