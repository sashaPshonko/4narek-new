package main

import "testing"

// Мега и почти-мега идут по ходовой логике с нормой 2 лота на АХ (items_config.json: hot_up_stock_max=2,
// hot_down_stock_min=2): поднимать цену, только пока на АХ меньше двух и хватает слотов на недостающие;
// опускать, когда лежит больше двух и продаж нет.
func megaIn() pridumalnahuyInput {
	return pridumalnahuyInput{
		Hot: true, Price: 4_800_000, Step: 100_000,
		HotDownSalesMax: 1, HotDownStockMin: 2, HotUpSalesMax: 2, HotUpStockMax: 2,
		BotsCount: 10, SlotCapacity: 50, OnAHCategory: 30,
	}
}

func TestMegaNoRaiseWhenTwoOnAH(t *testing.T) {
	in := megaIn()
	in.Sales, in.OnAHItem = 0, 2
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("на АХ уже 2 лота: повышать нельзя, got %+v", d)
	}
}

func TestMegaRaiseWhenUnderNorm(t *testing.T) {
	in := megaIn()
	in.Sales, in.OnAHItem = 0, 1
	if d := pridumalnahuyDecide(in); d.NewPrice != 4_900_000 || d.Reason != "hot_low_stock" {
		t.Fatalf("на АХ 1 лот < нормы 2: повышаем, got %+v", d)
	}
	in.Sales = 2 // две продажи за цикл — спрос и так есть
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("2 продажи за цикл: не повышаем, got %+v", d)
	}
}

func TestMegaNoRaiseWhenNormUnreachable(t *testing.T) {
	// слотов хватает только на 1 добор (ёмкость 50, занято 49), а нужно добрать 2 → нельзя физически
	in := megaIn()
	in.Sales, in.OnAHItem, in.OnAHCategory = 0, 0, 49
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price || d.Reason != "hot_slots_tight" {
		t.Fatalf("норму 2 не выполнить (свободен 1 слот): держим, got %+v", d)
	}
}

func TestMegaDownWhenPileUpWithoutSales(t *testing.T) {
	in := megaIn()
	in.Sales, in.OnAHItem = 0, 3
	if d := pridumalnahuyDecide(in); d.NewPrice != 4_700_000 || d.Reason != "hot_overstock" {
		t.Fatalf("3 лота без продаж: опускаем, got %+v", d)
	}
	in.Sales = 1 // что-то продалось — не режем
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("есть продажа: не опускаем, got %+v", d)
	}
}
