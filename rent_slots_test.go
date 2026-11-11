package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

// withSlotFleet подставляет флот: ботов и слоты по go-типу, как их присылает оркестратор.
func withSlotFleet(t *testing.T, goType string, bots, slots int) func() {
	t.Helper()
	ws := &websocket.Conn{}
	prevBots, prevSlots := clientBotsPerType, clientSlotsPerType
	clientBotsPerType = map[*websocket.Conn]map[string]int{ws: {goType: bots}}
	clientSlotsPerType = map[*websocket.Conn]map[string]int{}
	if slots > 0 {
		clientSlotsPerType[ws] = map[string]int{goType: slots}
	}
	return func() { clientBotsPerType, clientSlotsPerType = prevBots, prevSlots }
}

func TestSlotsCapacityFollowsRent(t *testing.T) {
	const typ = "netherite-1.21"

	// оркестратор не прислал слоты (старая версия): боты × 5
	restore := withSlotFleet(t, typ, 6, 0)
	if got := slotsForGoTypeLocked(typ); got != 30 {
		t.Fatalf("нет данных о слотах: ждём 6×5=30, got %d", got)
	}
	restore()

	// 6 ботов, у 2 арендованы слоты: 4×5 + 2×10 = 40
	restore = withSlotFleet(t, typ, 6, 40)
	if got := slotsForGoTypeLocked(typ); got != 40 {
		t.Fatalf("2 из 6 ботов с арендой: ждём 40, got %d", got)
	}
	restore()

	// у всех аренда: 6×10 = 60
	restore = withSlotFleet(t, typ, 6, 60)
	if got := slotsForGoTypeLocked(typ); got != 60 {
		t.Fatalf("у всех аренда: ждём 60, got %d", got)
	}
	restore()

	// битые данные (слотов меньше, чем 5 на бота) не уменьшают ёмкость ниже базовой
	restore = withSlotFleet(t, typ, 6, 12)
	if got := slotsForGoTypeLocked(typ); got != 30 {
		t.Fatalf("слотов меньше базы: ждём 30, got %d", got)
	}
	restore()
}

func TestBuyersStopHeldUsesBotSlots(t *testing.T) {
	// бот с арендой (10 слотов) не «полный» при 9 предметах и берёт ещё: 13 − 9 = 4 штуки
	rented := orchBotNick{GoType: "t", Live: true, OnAnarchy: true, Slots: 10, Items: []byte(`{"a":7}`), Inventory: []byte(`{"a":2}`)}
	base := orchBotNick{GoType: "t", Live: true, OnAnarchy: true, Slots: 5, Items: []byte(`{"a":7}`), Inventory: []byte(`{"a":2}`)}
	legacy := orchBotNick{GoType: "t", Live: true, OnAnarchy: true, Items: []byte(`{"a":7}`), Inventory: []byte(`{"a":2}`)}

	st := countBuyers([]orchBotNick{rented}, "t", 0)
	if st.Full != 0 || st.Able != 1 || st.FreeSlots != 4 {
		t.Fatalf("бот с арендой, 9 предметов: ждём able=1 free=4, got %+v", st)
	}
	st = countBuyers([]orchBotNick{base}, "t", 0)
	if st.Full != 1 || st.Able != 0 {
		t.Fatalf("бот без аренды, 9 предметов ≥ 8: стоп-закуп, got %+v", st)
	}
	// старая версия оркестратора (slots не пришёл) считается как 5 слотов
	st = countBuyers([]orchBotNick{legacy}, "t", 0)
	if st.Full != 1 {
		t.Fatalf("без поля slots считаем 5 слотов: стоп-закуп, got %+v", st)
	}
}

func hotBase() pridumalnahuyInput {
	return pridumalnahuyInput{
		Hot: true, Price: 1_000_000, Step: 50_000,
		HotDownSalesMax: 4, HotDownStockMin: 11, HotUpSalesMax: 6, HotUpStockMax: 8,
		Sales: 1, OnAHItem: 2,
	}
}

func TestHotSlotsTightUsesRentedCapacity(t *testing.T) {
	// 10 ботов, на АХ категории 48 лотов, нужно добрать 6
	in := hotBase()
	in.BotsCount, in.OnAHCategory = 10, 48
	if d := pridumalnahuyDecide(in); d.Reason != "hot_slots_tight" {
		t.Fatalf("без данных о слотах ёмкость 10×5=50, свободно 2 < 6 → tight, got %+v", d)
	}
	// у тех же ботов аренда: ёмкость 100, свободно 52 → можно повышать
	in.SlotCapacity = 100
	if d := pridumalnahuyDecide(in); d.Reason != "hot_low_stock" || d.NewPrice != 1_050_000 {
		t.Fatalf("с арендой ёмкость 100: цену повышаем, got %+v", d)
	}
}
