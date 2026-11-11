package main

import "testing"

func TestStockDemand(t *testing.T) {
	cases := []struct {
		rate, factor float64
		want         int
	}{
		{0, 1, 1},       // нет продаж — держим минимум одну штуку
		{0.4, 1, 1},     // sharp5: 0,4/ч → 0,6 → 1
		{1.5, 1, 2},     // megasword: 1,5/ч × 1 ч → 2
		{20.5, 1, 21},   // шлем: 20,5/ч → 21
		{20.5, 0.5, 11}, // ночью спрос вдвое меньше
		{500, 1, 60},    // потолок на один товар
	}
	for _, c := range cases {
		if got := stockDemand(c.rate, c.factor); got != c.want {
			t.Errorf("stockDemand(%v,%v)=%d want %d", c.rate, c.factor, got, c.want)
		}
	}
}

func TestStockOnlineFactor(t *testing.T) {
	if f := stockOnlineFactor(10000, 10000); f != 1 {
		t.Errorf("тот же онлайн → 1, got %v", f)
	}
	if f := stockOnlineFactor(14000, 7000); f < 1.4 || f > 1.5 {
		t.Errorf("онлайн ×2 → ~1,5 (потолок), got %v", f)
	}
	if f := stockOnlineFactor(2000, 14000); f != stockOnlineFactorMin {
		t.Errorf("ночь против пика → нижний предел %v, got %v", stockOnlineFactorMin, f)
	}
	if f := stockOnlineFactor(0, 5000); f != 1 {
		t.Errorf("нет онлайна → 1, got %v", f)
	}
}

func TestAllocateStockTargetsByValue(t *testing.T) {
	cands := []stockCand{
		{Item: "слабый", Demand: 5, Value: 100},
		{Item: "редкий-дорогой", Demand: 2, Value: 1000},
		{Item: "ходовой", Demand: 8, Value: 400},
	}
	// бюджет 8: по штуке каждому (3), остаток 5 по ценности: редкий +1, ходовой +4, слабый +0
	got := allocateStockTargets(cands, 8)
	if got["редкий-дорогой"] != 2 || got["ходовой"] != 5 || got["слабый"] != 1 {
		t.Fatalf("слоты по ценности: %+v", got)
	}
	// бюджета меньше числа товаров: каждому всё равно по одной, никто не выключается
	got = allocateStockTargets(cands, 2)
	if got["редкий-дорогой"] != 1 || got["ходовой"] != 1 || got["слабый"] != 1 {
		t.Fatalf("бюджет меньше товаров: %+v", got)
	}
	// хватает на всех — каждому полный спрос
	got = allocateStockTargets(cands, 100)
	if got["слабый"] != 5 || got["ходовой"] != 8 || got["редкий-дорогой"] != 2 {
		t.Fatalf("бюджета хватает: %+v", got)
	}
}

func TestBuildStockTargetsScenario(t *testing.T) {
	// ~3 часа истории: ходовой шлем, медленная кирка-мид, редкий мегасворд с высокой маржой
	items := []stockItemStat{
		{Item: "шлем", Sales: 60, SellSum: 60 * 1_300_000, SellCount: 60, BuySum: 60 * 900_000, BuyCount: 60, Nacenka: 500_000, ObservedHours: 3},
		{Item: "кирка-мид", Sales: 1, SellSum: 3_400_000, SellCount: 1, BuySum: 2_200_000, BuyCount: 1, Nacenka: 500_000, ObservedHours: 3},
		{Item: "мегасворд", Sales: 5, SellSum: 5 * 4_100_000, SellCount: 5, BuySum: 5 * 3_000_000, BuyCount: 5, Nacenka: 500_000, ObservedHours: 3},
	}
	inf := buildStockTargets(items, 100, 1)
	if inf["шлем"].Target != 20 || inf["кирка-мид"].Target != 1 || inf["мегасворд"].Target != 2 {
		t.Fatalf("цели при большом бюджете: шлем=%d мид=%d мега=%d",
			inf["шлем"].Target, inf["кирка-мид"].Target, inf["мегасворд"].Target)
	}
	// ночь: слотов мало (бюджет 5) — ходовой шлем урезан до остатка, остальные не теряют
	inf = buildStockTargets(items, 5, 1)
	if inf["мегасворд"].Target != 2 || inf["кирка-мид"].Target != 1 || inf["шлем"].Target != 2 {
		t.Fatalf("цели при бюджете 5: мега=%d мид=%d шлем=%d",
			inf["мегасворд"].Target, inf["кирка-мид"].Target, inf["шлем"].Target)
	}
	// слотов меньше, чем товаров: никто не выключен
	inf = buildStockTargets(items, 1, 1)
	for _, n := range []string{"шлем", "кирка-мид", "мегасворд"} {
		if inf[n].Target < 1 {
			t.Fatalf("%s не должен остаться без слота: %d", n, inf[n].Target)
		}
	}
}

func TestSqueezeWeakTargets(t *testing.T) {
	mk := func() map[string]stockTargetInfo {
		return map[string]stockTargetInfo{
			"strong": {Target: 9, Value: 900},
			"weak":   {Target: 9, Value: 100},
			"idle":   {Target: 1, Value: 0},
		}
	}
	free := mk()
	squeezeWeakTargets(free, 0.5)
	for k, v := range free {
		if v.Squeezed {
			t.Errorf("%s урезан при свободных слотах", k)
		}
	}
	tight := mk()
	squeezeWeakTargets(tight, 0.9)
	if tight["strong"].Squeezed || tight["idle"].Squeezed {
		t.Errorf("сильный и без продаж урезать нельзя: %+v", tight)
	}
	if !tight["weak"].Squeezed {
		t.Errorf("слабый при тесных слотах должен быть урезан: %+v", tight)
	}
}

func TestLendUnusedSlotsGoesToFilledItems(t *testing.T) {
	targets := map[string]int{"редкий-дорогой": 6, "ходовой": 3, "слабый": 1}
	demand := map[string]int{"редкий-дорогой": 6, "ходовой": 10, "слабый": 1}
	value := map[string]float64{"редкий-дорогой": 1000, "ходовой": 400, "слабый": 50}
	// редкий не набирается (на руках 0 из 6), ходовой упёрся в цель 3
	held := map[string]int{"редкий-дорогой": 0, "ходовой": 3, "слабый": 1}
	got := lendUnusedSlots(targets, demand, value, held)
	// простаивает 6 слотов, отдаём половину = 3 ходовому
	if got["ходовой"] != 6 || got["редкий-дорогой"] != 6 || got["слабый"] != 1 {
		t.Fatalf("lend: %+v", got)
	}
	// ходовой ещё не добрал до цели — ему слоты не отдаём
	held["ходовой"] = 1
	got = lendUnusedSlots(targets, demand, value, held)
	if got["ходовой"] != 3 {
		t.Fatalf("недобравший цель не получает: %+v", got)
	}
	// все набрались — занимать нечего
	held = map[string]int{"редкий-дорогой": 6, "ходовой": 3, "слабый": 1}
	got = lendUnusedSlots(targets, demand, value, held)
	if got["ходовой"] != 3 {
		t.Fatalf("простоя нет: %+v", got)
	}
}

// Тесты ниже восстановлены по смыслу после отката (исходный текст был удалён вместе с правкой decide).
// Фризов по запасу больше нет: закупку ограничивают только лимиты бота, а решение по запасу выше нормы —
// не повышать цену.
func TestDecideNoUpAndNoFreezeWhenStockAtOrAboveNorm(t *testing.T) {
	in := pridumalnahuyInput{
		NormalSales: 5, Price: 1_000_000, Step: 100_000,
		BookCeil: 2_000_000, BookCeilOK: true, BuyersFreeSlots: 100,
		HasStockTarget: true, StockTarget: 10,
	}
	for _, stock := range []int{5, 7, 10, 15, 40} {
		in.TotalStockItem = stock
		d := pridumalnahuyDecide(in)
		if d.Freeze || d.HardFreeze || d.NewPrice != in.Price {
			t.Fatalf("запас %d: ни фриза, ни повышения быть не должно: %+v", stock, d)
		}
	}
	in.TotalStockItem = 4 // ниже нормы → повышаем
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_100_000 {
		t.Fatalf("запас ниже нормы: UP, got %+v", d)
	}
}
