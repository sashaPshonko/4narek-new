package main

import (
	"testing"
	"time"
)

func timeNowForTest() time.Time { return time.Now() }

func bandIn() pridumalnahuyInput {
	return pridumalnahuyInput{
		Hot: true, Price: 1_500_000, Step: 50_000,
		HotDownSalesMax: 4, HotDownStockMin: 11, HotUpSalesMax: 6, HotUpStockMax: 8,
		Sales: 1, OnAHItem: 2, BotsCount: 10, SlotCapacity: 100, OnAHCategory: 20,
	}
}

// Дрейф вверх без привязки к рынку: ходовые ботинки ушли с 1.0 до 1.6 млн, и ночью продажи упали на 60–80%.
func TestHotNoRaiseAboveMarketCap(t *testing.T) {
	in := bandIn()
	in.PriceCap = 1_200_000 // перцентиль рынка
	in.Price = 1_190_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_190_000 || d.Reason != "hot_price_cap" {
		t.Fatalf("1.19+0.05 > потолка 1.2: не повышаем, got %+v", d)
	}
	in.Price = 1_100_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_150_000 || d.Reason != "hot_low_stock" {
		t.Fatalf("1.1+0.05 ≤ 1.2: повышаем как обычно, got %+v", d)
	}
	in.PriceCap = 0 // защита выключена
	in.Price = 1_500_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_550_000 {
		t.Fatalf("без потолка поведение прежнее, got %+v", d)
	}
}

func TestHotTetherPullsBackWhenFarAboveMarket(t *testing.T) {
	in := bandIn()
	in.PriceTether = 1_400_000
	in.PriceCap = 1_200_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_450_000 || d.Reason != "hot_above_market" {
		t.Fatalf("1.5 > поводка 1.4: шаг вниз, got %+v", d)
	}
	// продажи высокие (пик спроса) — поводок всё равно возвращает цену, пока она выше рынка
	in.Sales = 9
	if d := pridumalnahuyDecide(in); d.Reason != "hot_above_market" {
		t.Fatalf("поводок не зависит от продаж, got %+v", d)
	}
	// внутри полосы поводок молчит: цена между потолком и поводком — стоим
	in.Price, in.Sales = 1_300_000, 1
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_300_000 || d.Reason != "hot_price_cap" {
		t.Fatalf("между потолком и поводком: стоим, got %+v", d)
	}
}

func TestHotBandOnlyForHot(t *testing.T) {
	// обычная (не hot) логика полосу не использует
	in := pridumalnahuyInput{Price: 1_000_000, Step: 50_000, NormalSales: 5, PriceTether: 100_000, PriceCap: 100_000,
		BookCeil: 2_000_000, BookCeilOK: true, MultiFloor: 500_000, MultiFloorOK: true, TotalStockItem: 1, BuyersFreeSlots: 50}
	if d := pridumalnahuyDecide(in); d.Reason == "hot_above_market" || d.Reason == "hot_price_cap" {
		t.Fatalf("не-hot не должен упираться в полосу ходовых, got %+v", d)
	}
}

func TestHotMarketBandNeedsData(t *testing.T) {
	// нет книги/продавцов — ни потолка, ни поводка (защита не мешает)
	if c, th, _ := hotMarketBandNow(ItemConfig{ID: "x", Hot: true, HotPriceCapQ: 0.7, HotTetherQ: 0.88}, timeNowForTest()); c != 0 || th != 0 {
		t.Fatalf("нет книги: полосы нет, got cap=%d tether=%d", c, th)
	}
	if c, th, _ := hotMarketBandNow(ItemConfig{ID: "x", Hot: false, HotPriceCapQ: 0.7}, timeNowForTest()); c != 0 || th != 0 {
		t.Fatal("не hot: полосы нет")
	}
}

// Редкие предметы (починки, яд3, sharp6) на ходовой логике с нормой 1 лот на АХ: цена перестаёт расти, когда
// лот уже на АХ, и не уходит выше потолка рынка.
func rareIn() pridumalnahuyInput {
	return pridumalnahuyInput{
		Hot: true, Price: 3_800_000, Step: 100_000,
		HotDownSalesMax: 1, HotDownStockMin: 1, HotUpSalesMax: 2, HotUpStockMax: 1,
		Sales: 0, OnAHItem: 0, BotsCount: 10, SlotCapacity: 100, OnAHCategory: 20,
		PriceCap: 4_200_000, PriceTether: 5_000_000,
	}
}

func TestRareNormOneStopsRaisingWhenLotOnAH(t *testing.T) {
	in := rareIn()
	if d := pridumalnahuyDecide(in); d.NewPrice != 3_900_000 || d.Reason != "hot_low_stock" {
		t.Fatalf("на АХ 0 лотов — поднимаем, got %+v", d)
	}
	in.OnAHItem = 1
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("1 лот на АХ — норма выполнена, не растём, got %+v", d)
	}
	in.OnAHItem = 2 // 2 лота и нет продаж — немного опускаем
	if d := pridumalnahuyDecide(in); d.NewPrice != 3_700_000 || d.Reason != "hot_overstock" {
		t.Fatalf("2 лота без продаж — вниз, got %+v", d)
	}
}

func TestRareStopsAtMarketCap(t *testing.T) {
	in := rareIn()
	in.Price = 4_150_000
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price || d.Reason != "hot_price_cap" {
		t.Fatalf("упёрлись в потолок рынка 4.2 млн — не растём, got %+v", d)
	}
}
