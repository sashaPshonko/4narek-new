package main

import (
	"testing"
	"time"
)

func TestHotNoUpInWeakHour(t *testing.T) {
	base := pridumalnahuyInput{
		Price: 1500000, Step: 50000, Sales: 1, OnAHItem: 4,
		HotDownSalesMax: 2, HotDownStockMin: 12, HotUpSalesMax: 5, HotUpStockMax: 8,
		SlotCapacity: 100, OnAHCategory: 10,
	}
	weak := base
	weak.HourFrac = 0.3
	if d := pridumalnahuyDecideHot(weak); d.NewPrice != base.Price || d.Reason != "hot_low_hours" {
		t.Fatalf("в слабый час цена не должна расти: %+v", d)
	}
	strong := base
	strong.HourFrac = 0.9
	if d := pridumalnahuyDecideHot(strong); d.NewPrice <= base.Price || d.Reason != "hot_low_stock" {
		t.Fatalf("в сильный час повышение работает: %+v", d)
	}
	if d := pridumalnahuyDecideHot(base); d.NewPrice <= base.Price {
		t.Fatalf("без профиля часов повышение работает как раньше: %+v", d)
	}
}

func TestHotWeakHourStillAllowsTetherAndOverstock(t *testing.T) {
	in := pridumalnahuyInput{
		Price: 1500000, Step: 50000, Sales: 0, OnAHItem: 15, HourFrac: 0.2,
		HotDownSalesMax: 2, HotDownStockMin: 12, HotUpSalesMax: 5, HotUpStockMax: 8,
		SlotCapacity: 100, PriceTether: 1300000,
	}
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_above_market" || d.NewPrice >= in.Price {
		t.Fatalf("поводок работает и в слабый час: %+v", d)
	}
	in.PriceTether = 0
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" {
		t.Fatalf("затоварка работает и в слабый час: %+v", d)
	}
}

func TestHourProfileFromSales(t *testing.T) {
	var s [24]float64
	for h := range s {
		s[h] = 100
	}
	for h := 2; h <= 7; h++ {
		s[h] = 25 // ночь
	}
	p := hourProfileFromSales(s)
	if p[12] < 0.99 || p[12] > 1.01 {
		t.Fatalf("день ≈1, получили %.2f", p[12])
	}
	if p[4] > 0.3 || p[4] < 0.2 {
		t.Fatalf("ночь ≈0.25, получили %.2f", p[4])
	}
	// изолированный провал среди сильных часов сглаживается и не блокирует повышение
	var dip [24]float64
	for h := range dip {
		dip[h] = 100
	}
	dip[12] = 37
	if q := hourProfileFromSales(dip); q[12] < hotUpMinHourFrac {
		t.Fatalf("одиночный провал дня не должен блокировать повышение, получили %.2f", q[12])
	}
	var few [24]float64
	few[10], few[11] = 50, 60
	if q := hourProfileFromSales(few); q[10] != 0 {
		t.Fatalf("мало данных → нули, получили %.2f", q[10])
	}
}

func TestHotHourFracUsesMSK(t *testing.T) {
	hourProfileMu.Lock()
	old := hourProfile
	hourProfile = [24]float64{}
	hourProfile[12] = 0.77
	hourProfileMu.Unlock()
	defer func() { hourProfileMu.Lock(); hourProfile = old; hourProfileMu.Unlock() }()
	// 09:00 UTC = 12:00 МСК
	if f := hotHourFrac(time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)); f != 0.77 {
		t.Fatalf("12:00 МСК → 0.77, получили %.2f", f)
	}
}

func TestHotDownSalesLimitScalesWithHour(t *testing.T) {
	if got := hotDownSalesLimit(6, 0); got != 6 {
		t.Fatalf("нет профиля: порог фиксированный, got %v", got)
	}
	if got := hotDownSalesLimit(6, 1.0); got != 6 {
		t.Fatalf("пик: порог = значение из конфига, got %v", got)
	}
	if got := hotDownSalesLimit(6, 1.4); got != 6 {
		t.Fatalf("доля выше 1 обрезается, got %v", got)
	}
	if got := hotDownSalesLimit(6, 0.25); got != 1.5 {
		t.Fatalf("ночь: порог 6×0.25=1.5, got %v", got)
	}
}

func TestHotDownByHour(t *testing.T) {
	in := pridumalnahuyInput{
		Hot: true, Price: 1_540_000, Step: 50_000,
		HotDownSalesMax: 6, HotDownStockMin: 13, HotUpSalesMax: 8, HotUpStockMax: 10,
		BotsCount: 10, OnAHItem: 16,
	}
	// пик (доля 1): 5 продаж < 6 → вниз
	in.HourFrac, in.Sales = 1.0, 5
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" {
		t.Fatalf("пик, 5 продаж, 16 лотов: want hot_overstock, got %s", d.Reason)
	}
	// ночь (доля 0.3 → порог 1.8): те же 5 продаж — норма, стоим
	in.HourFrac = 0.3
	if d := pridumalnahuyDecideHot(in); d.Reason == "hot_overstock" {
		t.Fatalf("ночь, 5 продаж: это не затоварка, got %s", d.Reason)
	}
	// ночь, 1 продажа < 1.8 и 16 лотов → вниз
	in.Sales = 1
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" {
		t.Fatalf("ночь, 1 продажа, 16 лотов: want hot_overstock, got %s", d.Reason)
	}
	// профиля нет (0): фиксированный порог 6
	in.HourFrac, in.Sales = 0, 5
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" {
		t.Fatalf("без профиля 5<6 → вниз, got %s", d.Reason)
	}
}

func TestHotDownBlockedWhenPrevCycleWasNormal(t *testing.T) {
	in := pridumalnahuyInput{
		Hot: true, Price: 1_440_000, Step: 50_000,
		HotDownSalesMax: 4, HotDownStockMin: 13, HotUpSalesMax: 8, HotUpStockMax: 10,
		BotsCount: 10, Sales: 3, OnAHItem: 15, NormalSales: 4,
	}
	// прошлый цикл 9 продаж (≥ нормы 4): одиночный провал не снижает цену
	in.PrevSales = 9
	d := pridumalnahuyDecideHot(in)
	if d.Reason != "hot_down_prev_ok" || d.NewPrice != in.Price {
		t.Fatalf("прошлый цикл в норме → стоим, got %s %d", d.Reason, d.NewPrice)
	}
	// прошлый цикл тоже слабый (2 < 4): снижаем
	in.PrevSales = 2
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" || d.NewPrice != in.Price-in.Step {
		t.Fatalf("два слабых цикла подряд → вниз, got %s %d", d.Reason, d.NewPrice)
	}
	// NormalSales не задан: защиты нет
	in.PrevSales, in.NormalSales = 9, 0
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_overstock" {
		t.Fatalf("без нормы защиты нет, got %s", d.Reason)
	}
	// поводок защитой не блокируется
	in.NormalSales, in.PrevSales, in.PriceTether = 4, 9, 1_000_000
	if d := pridumalnahuyDecideHot(in); d.Reason != "hot_above_market" {
		t.Fatalf("поводок раньше защиты, got %s", d.Reason)
	}
}

func TestHotUpSalesLimit(t *testing.T) {
	if got := hotUpSalesLimit(8, 0); got != 8 {
		t.Fatalf("нет профиля: %v", got)
	}
	if got := hotUpSalesLimit(8, 0.6); got != 8 {
		t.Fatalf("обычный час: %v", got)
	}
	if got := hotUpSalesLimit(8, 0.9); got != 14 {
		t.Fatalf("горячий час: %v", got)
	}
}
