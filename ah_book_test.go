package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	skipAhBookBackfillOnInit = true
}

func TestIsFleetSellerLocked(t *testing.T) {
	old := fleetNickRoster
	fleetNickRoster = funauthRoster{
		503: {"rebr0_tv3": {}},
	}
	t.Cleanup(func() { fleetNickRoster = old })
	if !isFleetSellerLocked("rebr0_tv3") {
		t.Fatal("own nick")
	}
	if !isFleetSellerLocked("Rebr0_Tv3") {
		t.Fatal("case")
	}
	if isFleetSellerLocked("Beyermy") {
		t.Fatal("competitor")
	}
	if isFleetSellerLocked("") {
		t.Fatal("empty is unknown, not fleet")
	}
}

func TestAhBookUpsertRefreshesTsAndPrice(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "шлем-1.21"
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		ahLotKey("same"), old, "netherite_armor-1.21", item, 2_200_000, "Laymix777")
	if err != nil {
		t.Fatal(err)
	}
	insertAhBookBatch([]ahBookWire{{
		Uuid: "same", GoType: "netherite_armor-1.21", ItemID: item, Price: 2_100_000, Seller: "Laymix777",
	}})
	var n, price int
	var ts string
	_ = db.QueryRow(`SELECT COUNT(*), price, ts FROM ah_book_lots WHERE lot_id = ?`, ahLotKey("same")).Scan(&n, &price, &ts)
	if n != 1 {
		t.Fatalf("повтор не плодит строк, got %d", n)
	}
	if price != 2_100_000 {
		t.Fatalf("цена с АХ обновилась, got %d", price)
	}
	if ts <= old {
		t.Fatalf("ts должен стать свежим: old=%s new=%s", old, ts)
	}
}

func TestServerFunTimeRaiseAnomalousBookFloor(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "шлем-1.21"
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	for i := 0; i < ahBookMinLotsInWindow; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			ahLotKey(fmt.Sprintf("wall-%d", i)), ts, "armor", item, 2_200_000, "shop")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		ahLotKey("dump"), ts, "armor", item, 300_000, "dump")
	if err != nil {
		t.Fatal(err)
	}
	prevCfg := itemsConfig
	itemsConfig = map[string]ItemConfig{item: {Nacenka: 300_000}}
	t.Cleanup(func() { itemsConfig = prevCfg })
	prevHist := data.TradeHistory
	data.TradeHistory = map[string][]TradeLog{}
	t.Cleanup(func() { data.TradeHistory = prevHist })
	cycle := 10 * time.Minute
	wantN := ahBookMinLotsInWindow + 1
	p10, n, ok := ahBookP10Since(item, now.Add(-ahBookRaiseWindow))
	if !ok || n != wantN || p10 != 2_200_000 {
		t.Fatalf("p10=%d n=%d ok=%v want 2.2M n=%d (дамп не должен быть полом)", p10, n, ok, wantN)
	}
	cap := p10 + 300_000
	if !serverFunTimeRaiseAnomalous(1_600_000, 3_550_000, item, cycle, now) {
		t.Fatal("глюк выше p10+наценка")
	}
	if serverFunTimeRaiseAnomalous(1_600_000, 1_900_000, item, cycle, now) {
		t.Fatal("1.9 < p10+наценка — сырой min бы отсёк, p10 нет")
	}
	if !serverFunTimeRaiseAnomalous(1_600_000, cap+1, item, cycle, now) {
		t.Fatal("↑ над p10+наценка")
	}
	if serverFunTimeRaiseAnomalous(3_500_000, 2_000_000, item, cycle, now) {
		t.Fatal("это ↓ с глюка, не блокируем")
	}
	data.TradeHistory = map[string][]TradeLog{
		item: {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-12 * time.Minute), Type: "buy", Price: 1},
		},
	}
	if !serverFunTimeRaiseAnomalous(1_600_000, 1_900_000, item, cycle, now) {
		t.Fatal("закуп ≥ продаж — не поднимаем даже ниже p10")
	}
}

func TestAhBookMarketAnchorSellerP40(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mkt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "sword7-1.21"
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	for i := 0; i < 20; i++ {
		price := 700_000 + i*50_000
		_, err := db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			ahLotKey(fmt.Sprintf("s-%d", i)), ts, "sword", item, price, fmt.Sprintf("seller%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			ahLotKey(fmt.Sprintf("wall-%d", i)), ts, "sword", item, 2_500_000, "wallshop")
		if err != nil {
			t.Fatal(err)
		}
	}
	sellMkt, buyEdge, n, ok := ahBookMarketAnchorsSince(item, now.Add(-30*time.Minute))
	if !ok || n < 20 {
		t.Fatalf("ok=%v n=%d sell=%d buy=%d", ok, n, sellMkt, buyEdge)
	}
	// p10 buy edge ~700-800k; p40 sell ~900k-1.1M; not wall 2.5M
	if buyEdge < 700_000 || buyEdge > 900_000 {
		t.Fatalf("buyEdge=%d want ~0.7–0.9M", buyEdge)
	}
	if sellMkt < 850_000 || sellMkt > 1_300_000 {
		t.Fatalf("sellMkt=%d want competitive ~0.85–1.3M", sellMkt)
	}
	if sellMkt < buyEdge {
		t.Fatalf("sell %d < buyEdge %d", sellMkt, buyEdge)
	}
	lotP10, lotN, lotOK := ahBookP10Since(item, now.Add(-30*time.Minute))
	if !lotOK || lotN < 40 {
		t.Fatalf("lot p10 setup failed")
	}
	// wall clones inflate lot distribution toward 2.5M vs pure seller edge
	if lotP10 < 700_000 {
		t.Fatalf("lot-p10=%d unexpected", lotP10)
	}
	_ = lotP10
}

func TestAhBookExpensiveBottomMultiSellers(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "megasword-1.21"
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	// one-off cheap random — must NOT set the bottom
	_, err = db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		ahLotKey("once"), ts, "sword", item, 1_500_000, "random_once")
	if err != nil {
		t.Fatal(err)
	}
	// multi sellers: mins at 3.0 / 3.0 / 4.0 / 5.0 / 5.0 + fat mid
	multis := []struct {
		seller string
		prices []int
	}{
		{"pack_a", []int{3_000_000, 3_200_000, 3_500_000}},
		{"pack_b", []int{3_000_000, 3_100_000, 3_100_000}},
		{"pack_c", []int{4_000_000, 4_100_000, 4_200_000}},
		{"pack_d", []int{5_000_000, 5_000_000, 5_500_000}},
		{"pack_e", []int{5_000_000, 5_200_000, 6_000_000}},
		{"pack_f", []int{5_500_000, 5_500_000, 5_500_000}},
		{"pack_g", []int{6_000_000, 6_000_000, 6_000_000}},
	}
	u := 0
	for _, m := range multis {
		for _, p := range m.prices {
			u++
			_, err := db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
				ahLotKey(fmt.Sprintf("m-%d", u)), ts, "sword", item, p, m.seller)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	sell, buy, n, ok := ahBookExpensiveBottomAnchorsSince(item, now.Add(-30*time.Minute))
	if !ok || n != 7 {
		t.Fatalf("ok=%v n=%d sell=%d buy=%d", ok, n, sell, buy)
	}
	// abs min of multi mins = 3.0M (pack_a/b); random 1.5M ignored (1 lot)
	if sell < 2_900_000 || sell > 3_100_000 {
		t.Fatalf("sell=%d want abs multi bottom 3.0M", sell)
	}
	if buy != sell {
		t.Fatalf("buyEdge=%d want =sell for expensive bottom", buy)
	}

	// classic p40 still above multi low
	classicSell, _, _, cok := ahBookMarketAnchorsSince(item, now.Add(-30*time.Minute))
	if !cok || classicSell <= sell {
		t.Fatalf("classic sell=%d should be above multiLow=%d", classicSell, sell)
	}
}
