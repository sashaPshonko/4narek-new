package main

import "testing"

func TestNoBookPausesBuyAndHoldsPrice(t *testing.T) {
	in := pridumalnahuyInput{Sales: 0, NormalSales: 5, TotalStockItem: 1, Price: 5_000_000, Step: 100_000,
		BuyersFreeSlots: 100, NoBook: true}
	dec := pridumalnahuyDecide(in)
	if !dec.PauseBuy || dec.NewPrice != in.Price || dec.Reason != "no_book" || dec.Freeze {
		t.Fatalf("без книги: ждём паузу закупки и цену без изменений, получили %+v", dec)
	}
	// без паузы тот же товар шёл бы вверх (stock < N, места хватает) — это и блокируем
	in.NoBook = false
	dec = pridumalnahuyDecide(in)
	if dec.PauseBuy || dec.NewPrice <= in.Price {
		t.Fatalf("с книгой должен идти обычный UP, получили %+v", dec)
	}
}

func TestFreezeNeverLowersNacenka(t *testing.T) {
	item := "тест-дорогой"
	if itemsConfig == nil {
		itemsConfig = map[string]ItemConfig{}
		defer func() { itemsConfig = nil }()
	}
	oldCfg, had := itemsConfig[item]
	itemsConfig[item] = ItemConfig{ID: item, Nacenka: 1_400_000}
	oldNac, oldDaily := data.Nacenkas, dailyData.Nacenkas
	data.Nacenkas, dailyData.Nacenkas = map[string]int{}, map[string]int{}
	defer func() {
		if had {
			itemsConfig[item] = oldCfg
		} else {
			delete(itemsConfig, item)
		}
		data.Nacenkas, dailyData.Nacenkas = oldNac, oldDaily
	}()
	st := freezeNacenkaLocked(item, ItemAdjustState{}, itemsConfig[item], 1_400_000, pridumalnahuyFrozenNacenka)
	if !st.NacenkaFrozen || st.NacenkaOriginal != 1_400_000 {
		t.Fatalf("state: %+v", st)
	}
	if got := itemsConfig[item].Nacenka; got < 2_800_000 {
		t.Fatalf("фриз у дорогого товара должен поднять наценку выше 1,4 млн (≥2,8 млн), получили %d", got)
	}
	// дешёвый товар: обычное значение фриза
	itemsConfig[item] = ItemConfig{ID: item, Nacenka: 300_000}
	freezeNacenkaLocked(item, ItemAdjustState{}, itemsConfig[item], 300_000, pridumalnahuyFrozenNacenka)
	if got := itemsConfig[item].Nacenka; got != pridumalnahuyFrozenNacenka {
		t.Fatalf("дешёвый товар: ждали %d, получили %d", pridumalnahuyFrozenNacenka, got)
	}
}

func TestSoftFreezeNacenka(t *testing.T) {
	// ходовой дешёвый товар: лот ~0,9 млн, наценка 300к → 40% = 360к (выше 1,2× = 360к)
	if got := softFreezeNacenka(300_000, 900_000); got != 360_000 {
		t.Fatalf("шлем: ждали 360к, получили %d", got)
	}
	// дорогой товар: лот 3,4 млн, наценка 500к → 40% = 1,36 млн, но не выше полного стопа 1,36?
	if got := softFreezeNacenka(500_000, 3_400_000); got != 1_000_000 {
		t.Fatalf("мегасворд: потолок мягкого фриза — полный стоп (1 млн), получили %d", got)
	}
	// дорогой лот с высокой наценкой: мягкая наценка не выше полного стопа (2× исходной)
	if got := softFreezeNacenka(1_400_000, 9_500_000); got != 2_800_000 {
		t.Fatalf("меч-ориг: 40%% лота 3,8 млн выше 2,8 млн → ограничено 2,8 млн, получили %d", got)
	}
	// нет данных о цене лота → полный стоп
	if got := softFreezeNacenka(300_000, 0); got != pridumalnahuyFrozenNacenka {
		t.Fatalf("без данных → полный стоп, получили %d", got)
	}
	// наценка выше 40% цены лота: не ниже 1,2× исходной
	if got := softFreezeNacenka(600_000, 1_000_000); got != 720_000 {
		t.Fatalf("наценка уже выше 40%%: ждали 720к, получили %d", got)
	}
}

func TestHardFreezeNacenkaIsAboveSoft(t *testing.T) {
	if hardFreezeNacenka(300_000) != pridumalnahuyFrozenNacenka {
		t.Fatalf("минимум %d", pridumalnahuyFrozenNacenka)
	}
	if hardFreezeNacenka(800_000) != 1_600_000 {
		t.Fatal("не ниже удвоенной исходной")
	}
	if soft := softFreezeNacenka(300_000, 640_000); soft >= hardFreezeNacenka(300_000) {
		t.Fatalf("мягкий (%d) должен быть ниже полного", soft)
	}
}
