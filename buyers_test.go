package main

import (
	"encoding/json"
	"testing"
)

func rawCounts(m map[string]int) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func liveBot(goType string, bal *float64, age *int64) orchBotNick {
	return orchBotNick{GoType: goType, Live: true, OnAnarchy: true, Balance: bal, BalanceAgeS: age}
}

func TestCountBuyers(t *testing.T) {
	bots := []orchBotNick{
		liveBot("t", f64(1_000_000), i64(60)),       // не хватает
		liveBot("t", f64(9_000_000), i64(60)),       // может
		liveBot("t", nil, nil),                      // баланс неизвестен → может
		liveBot("t", f64(1), i64(99999)),            // баланс протух → может
		liveBot("other", f64(0), i64(1)),            // чужая категория
		{GoType: "t", Live: false, OnAnarchy: true}, // оффлайн
		{GoType: "t", Live: true, OnAnarchy: true, Banned: true},
	}
	full := liveBot("t", f64(9_000_000), i64(60)) // АХ 5 + инвентарь 3 = 8 → стоп-закуп
	full.Items = rawCounts(map[string]int{"a": 5})
	full.Inventory = rawCounts(map[string]int{"b": 3})
	bots = append(bots, full)
	notFull := liveBot("t", f64(9_000_000), i64(60)) // 7 < 8 → ещё может
	notFull.Items = rawCounts(map[string]int{"a": 5})
	notFull.Inventory = rawCounts(map[string]int{"b": 2})
	bots = append(bots, notFull)
	// слоты заняты другими товарами: лимит общий, поэтому тоже стоп-закуп
	other := liveBot("t", f64(9_000_000), i64(60))
	other.Items = rawCounts(map[string]int{"x": 4, "y": 1})
	other.Inventory = rawCounts(map[string]int{"z": 3})
	bots = append(bots, other)

	got := countBuyers(bots, "t", 3_000_000)
	want := buyerStats{Eligible: 7, Able: 4, Broke: 1, Full: 2, FreeSlots: 8 + 8 + 1 + 8}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// Регрессия: оркестратор присылал items списком, и разбор всего presence падал, из-за чего Go пропускал все товары.
func TestPresenceWithArrayItemsStillDecodes(t *testing.T) {
	raw := `{"bots":[{"username":"a","go_type":"t","live":true,"items":["x","y","x"],"inventory":[{"id":"z","count":2}]},
	{"username":"b","items":{"x":3},"inventory":[]}]}`
	var m struct {
		Bots []orchBotNick `json:"bots"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("presence не должен падать на формате items: %v", err)
	}
	a := botCounts(m.Bots[0].Items)
	if a["x"] != 2 || a["y"] != 1 {
		t.Fatalf("список строк: %v", a)
	}
	if botCounts(m.Bots[0].Inventory)["z"] != 2 {
		t.Fatal("список объектов с count")
	}
	if botCounts(m.Bots[1].Items)["x"] != 3 {
		t.Fatal("карта")
	}
	if botCounts(nil) != nil {
		t.Fatal("нет данных → nil")
	}
}

func TestDecideNoUpNoFreezeWhenBotSlotsShort(t *testing.T) {
	in := pridumalnahuyInput{
		NormalSales: 5, Price: 1_000_000, Step: 100_000,
		BookCeil: 2_000_000, BookCeilOK: true,
		TotalStockItem: 1, BuyersEligible: 3, BuyersAble: 3, BuyersFreeSlots: 3,
	}
	d := pridumalnahuyDecide(in)
	if d.NewPrice != in.Price || d.Reason != "inv_slots_tight" || d.Freeze {
		t.Fatalf("слотов 3 + запас 1 < нормы 5: цену не повышаем, got %d (%s)", d.NewPrice, d.Reason)
	}
	in.BuyersFreeSlots = 4
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_100_000 {
		t.Fatalf("слотов 4 + запас 1 = нормы 5: UP разрешён, got %d (%s)", d.NewPrice, d.Reason)
	}
}
