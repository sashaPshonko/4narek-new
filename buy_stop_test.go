package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBuyStopDecide(t *testing.T) {
	const ceil = 5_000_000
	cases := []struct {
		name   string
		active bool
		price  int
		ok     bool
		want   bool
	}{
		{"цена ниже потолка, закупка идёт", false, 4_800_000, true, false},
		{"цена ровно на потолке — ещё не стоп", false, ceil, true, false},
		{"цена выше потолка — стоп", false, 5_100_000, true, true},
		{"стоп держится чуть ниже потолка (гистерезис)", true, 4_900_000, true, true},
		{"стоп снимается, когда цена на 3% ниже потолка", true, 4_800_000, true, false},
		{"потолка нет (мало продавцов) — защита не мешает", false, 9_000_000, false, false},
		{"потолка нет, стоп был — снимается", true, 9_000_000, false, false},
		{"нет цены", false, 0, true, false},
	}
	for _, c := range cases {
		if got := buyStopDecide(c.active, c.price, ceil, c.ok); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestBuyStopTurnsOffBuyingInCatalog(t *testing.T) {
	const id = "megasword-test"
	cfg := ItemConfig{ID: id, Nacenka: 600_000}
	prev := buyStopActive
	buyStopActive = map[string]bool{}
	defer func() { buyStopActive = prev }()

	if n := catalogNacenka(id, cfg); n != 600_000 && n != getNacenka(id) {
		t.Fatalf("без стопа наценка обычная, got %d", n)
	}
	if !setBuyStopLocked(id, true) {
		t.Fatal("первое включение стопа должно сообщить об изменении")
	}
	if setBuyStopLocked(id, true) {
		t.Fatal("повторное включение — не изменение (не рассылаем каталог зря)")
	}
	if n := catalogNacenka(id, cfg); n != noBuyNacenka {
		t.Fatalf("при стопе наценка «бесконечность» (бот не покупает), got %d", n)
	}
	if !setBuyStopLocked(id, false) {
		t.Fatal("снятие стопа — изменение")
	}
	if n := catalogNacenka(id, cfg); n == noBuyNacenka {
		t.Fatalf("стоп снят — закупка снова идёт, got %d", n)
	}
}

func TestBuyStopCeilNeedsData(t *testing.T) {
	// защиты нет в конфиге или нет книги (нет БД) — потолка нет, закупка не блокируется
	if _, _, ok := buyStopCeilNow(ItemConfig{ID: "x"}, time.Now()); ok {
		t.Fatal("buy_stop_q=0 → потолка нет")
	}
	if _, _, ok := buyStopCeilNow(ItemConfig{ID: "x", BuyStopQ: 0.8}, time.Now()); ok {
		t.Fatal("нет книги → потолка нет")
	}
}

// Конфиг: стоп-закуп только у дорогих, значения осмысленные, редкие кирки без защиты.
// «Починки» брони на коридорном ценообразовании (как 05.11) без buy_stop_q: потолок книги и так ограничивает цену.
func TestItemsConfigBuyStop(t *testing.T) {
	raw, err := os.ReadFile("items_config.json")
	if err != nil {
		t.Skip("нет items_config.json")
	}
	var cfg map[string]struct {
		BuyStopQ float64 `json:"buy_stop_q"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"megasword-1.21", "pochti-megasword-1.21", "кирка-мид-1.21", "меч-крушитель-1.21"} {
		if q := cfg[id].BuyStopQ; q <= 0 || q >= 1 {
			t.Errorf("%s: ждём buy_stop_q в (0,1), got %v", id, q)
		}
	}
	for _, id := range []string{"кирка-топ9-1.21", "кирка-топ10-1.21", "кирка-мега-бульдозер-1.21", "кирка-молот-тора-1.21", "sword7-1.21", "шлем-1.21"} {
		if cfg[id].BuyStopQ != 0 {
			t.Errorf("%s: защиты быть не должно, got %v", id, cfg[id].BuyStopQ)
		}
	}
}

func TestStockStopDecide(t *testing.T) {
	cases := []struct {
		held, cap int
		want      bool
	}{
		{0, 4, false}, {3, 4, false}, {4, 4, true}, {9, 4, true}, {5, 0, false},
	}
	for _, c := range cases {
		if got := stockStopDecide(c.held, c.cap); got != c.want {
			t.Errorf("held=%d cap=%d: got %v want %v", c.held, c.cap, got, c.want)
		}
	}
}

// Запас флота (АХ + инвентари всех оркестраторов) достиг нормы → закупка выключается и возвращается, когда запас упал.
func TestEvalStockBuyStops(t *testing.T) {
	ws := &websocket.Conn{}
	prevCfg, prevItems, prevInv, prevStop := itemsConfig, clientItems, clientInventory, buyStopStock
	itemsConfig = map[string]ItemConfig{
		"mega":  {ID: "mega", BuyStopHeld: 4},
		"plain": {ID: "plain"},                              // без защиты
		"nobuy": {ID: "nobuy", BuyStopHeld: 2, NoBuy: true}, // и так выключен
	}
	clientItems = map[*websocket.Conn]map[string]int{ws: {"mega": 2, "plain": 50}}
	clientInventory = map[*websocket.Conn]map[string]int{ws: {"mega": 1}}
	buyStopStock = map[string]bool{}
	defer func() {
		itemsConfig, clientItems, clientInventory, buyStopStock = prevCfg, prevItems, prevInv, prevStop
	}()

	if evalStockBuyStopsLocked() || buyStopStock["mega"] {
		t.Fatal("запас 3 < 4: закупка идёт, каталог рассылать не нужно")
	}
	clientInventory[ws]["mega"] = 2 // 2 на АХ + 2 в инвентаре = 4
	if !evalStockBuyStopsLocked() || !buyStopStock["mega"] {
		t.Fatal("запас 4 ≥ 4: стоп-закуп должен включиться")
	}
	if n := catalogNacenka("mega", itemsConfig["mega"]); n != noBuyNacenka {
		t.Fatalf("при стопе по запасу наценка «бесконечность», got %d", n)
	}
	if evalStockBuyStopsLocked() {
		t.Fatal("состояние не менялось — каталог повторно не рассылаем")
	}
	if buyStopStock["plain"] || buyStopStock["nobuy"] {
		t.Fatal("у предметов без buy_stop_held (или с no_buy) стопа по запасу быть не должно")
	}
	clientItems[ws]["mega"] = 1 // 1 + 2 = 3 < 4
	if !evalStockBuyStopsLocked() || buyStopStock["mega"] {
		t.Fatal("запас упал ниже нормы: закупка возвращается")
	}
}
