package main

import (
	"math"
	"testing"
	"time"
)

func TestFifoSlotValue(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Hour)
	since := t0.Add(-time.Hour)

	trades := []TradeLog{
		{Time: t0, Type: "buy", Price: 1_000_000},
		{Time: t0.Add(time.Hour), Type: "sell", Price: 1_500_000},         // +500k за 1 ч
		{Time: t0.Add(2 * time.Hour), Type: "buy", Price: 1_000_000},      // ещё открыт 8 ч
		{Time: t0.Add(30 * time.Minute), Type: "buy", Price: 1_000_000},   // порядок в срезе перемешан намеренно
		{Time: t0.Add(150 * time.Minute), Type: "sell", Price: 1_200_000}, // закроет покупку в t0+30м: +200k за 2 ч
	}
	inf := fifoSlotValue(trades, since, now, -1, 0, 0)
	if inf.Sells != 2 || inf.Open != 1 {
		t.Fatalf("sells=%d open=%d", inf.Sells, inf.Open)
	}
	if math.Abs(inf.Profit-700_000) > 1 {
		t.Fatalf("profit=%v", inf.Profit)
	}
	// слот-часы: 1 + 2 + открытый лот с t0+2ч до now (8 ч) = 11
	if math.Abs(inf.SlotHours-11) > 0.01 {
		t.Fatalf("slotHours=%v", inf.SlotHours)
	}
	if inf.Known { // всего 2 продажи < slotValueMinSells — оценка считается неизвестной
		t.Fatalf("2 продаж мало для оценки: %+v", inf)
	}
	if math.Abs(inf.Profit/inf.SlotHours-700_000.0/11) > 1 {
		t.Fatalf("profit/slotHours=%v", inf.Profit/inf.SlotHours)
	}
}

func TestFifoSlotValueFastBeatsSlow(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := t0.Add(24 * time.Hour)
	since := t0.Add(-time.Hour)
	// быстрый: маржа 15%, 10 минут; медленный: маржа 50%, 8 часов
	fast := []TradeLog{}
	for i := 0; i < 20; i++ {
		b := t0.Add(time.Duration(i) * time.Hour)
		fast = append(fast, TradeLog{Time: b, Type: "buy", Price: 1_000_000}, TradeLog{Time: b.Add(10 * time.Minute), Type: "sell", Price: 1_150_000})
	}
	slow := []TradeLog{
		{Time: t0, Type: "buy", Price: 1_000_000},
		{Time: t0.Add(8 * time.Hour), Type: "sell", Price: 1_500_000},
	}
	f := fifoSlotValue(fast, since, now, -1, 0, 0)
	s := fifoSlotValue(slow, since, now, -1, 0, 0)
	if !(f.Profit/f.SlotHours > s.Profit/s.SlotHours) {
		t.Fatalf("быстрый товар должен быть ценнее на слот: fast=%+v slow=%+v", f, s)
	}
}

func TestFifoSlotValueIgnoresSellsBeforeWindowAndUnmatched(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	trades := []TradeLog{
		{Time: t0.Add(-100 * time.Hour), Type: "sell", Price: 5_000_000}, // купили до истории
		{Time: t0.Add(-60 * time.Hour), Type: "buy", Price: 1_000_000},
		{Time: t0.Add(-59 * time.Hour), Type: "sell", Price: 1_100_000}, // вне окна 48 ч
	}
	inf := fifoSlotValue(trades, t0.Add(-48*time.Hour), t0, -1, 0, 0)
	if inf.Sells != 0 || inf.Profit != 0 || inf.Known {
		t.Fatalf("%+v", inf)
	}
}

func TestFifoSlotValueCapsOpenLotsByHeld(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Hour)
	var trades []TradeLog
	for i := 0; i < 5; i++ { // 5 покупок, продаж нет
		trades = append(trades, TradeLog{Time: t0.Add(time.Duration(i) * time.Hour), Type: "buy", Price: 1_000_000})
	}
	if inf := fifoSlotValue(trades, t0.Add(-time.Hour), now, -1, 0, 0); inf.Open != 5 {
		t.Fatalf("без ограничения open=%d", inf.Open)
	}
	inf := fifoSlotValue(trades, t0.Add(-time.Hour), now, 2, 0, 0)
	if inf.Open != 2 {
		t.Fatalf("на руках 2, open=%d", inf.Open)
	}
	// остались две самые свежие покупки: с t0+3ч и t0+4ч → 7 + 6 = 13 слот-часов
	if math.Abs(inf.SlotHours-13) > 0.01 {
		t.Fatalf("slotHours=%v", inf.SlotHours)
	}
	if inf := fifoSlotValue(trades, t0.Add(-time.Hour), now, 0, 0, 0); inf.Open != 0 || inf.SlotHours != 0 {
		t.Fatalf("на руках 0: %+v", inf)
	}
}

func TestFifoSlotValueDecayWeightsRecentSales(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	since := now.Add(-72 * time.Hour)
	mk := func(sellAgo time.Duration, profit int) []TradeLog {
		sell := now.Add(-sellAgo)
		return []TradeLog{
			{Time: sell.Add(-time.Hour), Type: "buy", Price: 1_000_000},
			{Time: sell, Type: "sell", Price: 1_000_000 + profit},
		}
	}
	var trades []TradeLog
	// 3 старых хороших продажи (+1M за 1 ч, 36 ч назад) и 3 свежие плохие (−100k за 1 ч, час назад)
	for i := 0; i < 3; i++ {
		trades = append(trades, mk(36*time.Hour+time.Duration(i)*2*time.Hour, 1_000_000)...)
		trades = append(trades, mk(time.Hour+time.Duration(i)*10*time.Minute, -100_000)...)
	}
	flat := fifoSlotValue(trades, since, now, -1, 0, 0)
	decayed := fifoSlotValue(trades, since, now, -1, 0, 12*time.Hour)
	if !(decayed.Value < flat.Value) {
		t.Fatalf("с затуханием свежие плохие продажи должны тянуть оценку вниз: flat=%v decayed=%v", flat.Value, decayed.Value)
	}
}

func TestFifoSlotValueSkipsSellsOfPreEpochLots(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := t0.Add(24 * time.Hour)
	var trades []TradeLog
	// на старте на руках 2 лота (купили до отсчёта) → первые 2 продажи их закрывают, не наши покупки
	trades = append(trades, TradeLog{Time: t0.Add(time.Hour), Type: "buy", Price: 1_000_000})
	trades = append(trades, TradeLog{Time: t0.Add(2 * time.Hour), Type: "sell", Price: 9_000_000}) // старый лот
	trades = append(trades, TradeLog{Time: t0.Add(3 * time.Hour), Type: "sell", Price: 9_000_000}) // старый лот
	trades = append(trades, TradeLog{Time: t0.Add(4 * time.Hour), Type: "sell", Price: 1_100_000}) // наш лот
	inf := fifoSlotValue(trades, t0, now, -1, 2, 0)
	// без skipSells первая продажа (9M) закрыла бы нашу покупку и дала бы ложную прибыль 8M
	if inf.Sells != 1 || math.Abs(inf.Profit-100_000) > 1 {
		t.Fatalf("%+v", inf)
	}
}

func TestTradesSinceAndMode(t *testing.T) {
	e := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	got := tradesSince([]TradeLog{{Time: e.Add(-time.Minute)}, {Time: e}, {Time: e.Add(time.Hour)}}, e)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	known := func(v float64) slotValueInfo { return slotValueInfo{Value: v, Known: true} }
	cases := []struct {
		inf  slotValueInfo
		ref  float64
		want string
	}{
		{slotValueInfo{}, 100_000, slotModeUnknown},
		{known(120_000), 100_000, slotModeStable},
		{known(50_000), 100_000, slotModeStable},        // ровно половина средней
		{known(49_000), 100_000, slotModeOpportunistic}, // ниже половины
		{known(-10_000), 100_000, slotModeOpportunistic},
		{known(10_000), 0, slotModeStable}, // эталона нет — по знаку
	}
	for _, c := range cases {
		if got := slotMode(c.inf, c.ref); got != c.want {
			t.Errorf("%+v ref=%v: got %s want %s", c.inf, c.ref, got, c.want)
		}
	}
}
