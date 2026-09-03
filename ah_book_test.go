package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

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

func TestShouldRaiseFromAhBook(t *testing.T) {
	n := ahBookRaiseSample
	minAsk := 1_000_000
	nac := 300_000
	if !shouldRaiseFromAhBook(400_000, minAsk, nac, n, false, false, false) {
		t.Fatal("селл ниже min+наценка — поднимаем")
	}
	if shouldRaiseFromAhBook(minAsk+nac, minAsk, nac, n, false, false, false) {
		t.Fatal("уже на min+наценка — не трогаем")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n-1, false, false, false) {
		t.Fatal("меньше 50 — рано")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, true, false, false) {
		t.Fatal("dump — не поднимаем")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, false, true, false) {
		t.Fatal("уже ↓ в этом цикле")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, false, false, true) {
		t.Fatal("были покупки — не поднимаем")
	}
}

func TestAhBookRaiseTarget(t *testing.T) {
	if got := ahBookRaiseTarget(1_000_000, 300_000, 100_000); got != 1_400_000 {
		t.Fatalf("got %d", got)
	}
	if ahBookRaiseTarget(0, 300_000, 100_000) != 0 {
		t.Fatal("пустой min")
	}
}

func TestAhBookMinOfLastN(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	if _, err := db.Exec(`CREATE TABLE ah_book_lots (
		uuid TEXT PRIMARY KEY, ts TEXT NOT NULL, go_type TEXT, item_id TEXT,
		price INTEGER, durability REAL, seller TEXT, enchants_json TEXT, anarchy INTEGER, seen_by TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ah_book_seller_bans (
		seller TEXT PRIMARY KEY, ts TEXT NOT NULL, item_id TEXT NOT NULL, n INTEGER NOT NULL, window_sec INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	mlDB = db
	item := "меч-1.21"
	base := time.Now().UTC()
	for i := 0; i < 49; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
			fmt.Sprintf("u-%d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			"sword", item, 900_000+i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := ahBookMinOfLastN(item, 50); ok {
		t.Fatal("49 строк — рано")
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"cheap", base.Add(50*time.Second).Format(time.RFC3339), "sword", item, 500_000)
	if err != nil {
		t.Fatal(err)
	}
	minP, ok := ahBookMinOfLastN(item, 50)
	if !ok || minP != 500_000 {
		t.Fatalf("min=%d ok=%v want 500000", minP, ok)
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"newer-high", base.Add(120*time.Second).Format(time.RFC3339), "sword", item, 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// 50 newest: cheap still in window (49 old + cheap + newer = 51, drop oldest)
	minP, ok = ahBookMinOfLastN(item, 50)
	if !ok || minP != 500_000 {
		t.Fatalf("after high min=%d ok=%v", minP, ok)
	}
}

func TestAhBookWallBanSameSKU(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()

	item := "sword7-1.21"
	var rows []ahBookWire
	for i := 0; i < 5; i++ {
		rows = append(rows, ahBookWire{
			Uuid: fmt.Sprintf("w-%d", i), GoType: "netherite_sword-1.21", ItemID: item,
			Price: 2_200_000, Seller: "Laymix777",
		})
	}
	insertAhBookBatch(rows)
	var bans int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans`).Scan(&bans)
	if bans != 0 {
		t.Fatalf("5 лотов — ещё не бан, got %d", bans)
	}
	insertAhBookBatch([]ahBookWire{{
		Uuid: "w-5", GoType: "netherite_sword-1.21", ItemID: item, Price: 2_200_000, Seller: "Laymix777",
	}})
	if err := db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans WHERE seller = 'laymix777'`).Scan(&bans); err != nil || bans != 1 {
		t.Fatalf("6-й лот того же SKU — бан, bans=%d err=%v", bans, err)
	}
	// другой SKU того же ника — не второй бан
	insertAhBookBatch([]ahBookWire{{
		Uuid: "helm-0", GoType: "netherite_armor-1.21", ItemID: "шлем-1.21", Price: 2_200_000, Seller: "Laymix777",
	}})
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans`).Scan(&bans)
	if bans != 1 {
		t.Fatalf("бан навсегда один раз, got %d", bans)
	}
}

func TestAhBookMinSkipsBannedSeller(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()

	item := "шлем-1.21"
	base := time.Now().UTC()
	for i := 0; i < 50; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("ok-%d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			"armor", item, 2_000_000, "player")
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("wall-%d", i), base.Add(time.Duration(100+i)*time.Second).Format(time.RFC3339),
			"armor", item, 100_000, "WallBot")
		if err != nil {
			t.Fatal(err)
		}
	}
	backfillAhBookSellerBans()
	minP, ok := ahBookMinOfLastN(item, 50)
	if !ok || minP != 2_000_000 {
		t.Fatalf("витрина в min не входит: min=%d ok=%v", minP, ok)
	}
	var nLots int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_lots WHERE seller = 'WallBot'`).Scan(&nLots)
	if nLots != 6 {
		t.Fatalf("лоты витрины остаются в книге, got %d", nLots)
	}
}

func TestAhBookTimesHitWall(t *testing.T) {
	base := time.Now()
	var times []time.Time
	for i := 0; i < 6; i++ {
		times = append(times, base.Add(time.Duration(i)*time.Minute))
	}
	if !ahBookTimesHitWall(times) {
		t.Fatal("6 за 5 минут — стена")
	}
	times[5] = base.Add(20 * time.Minute)
	if ahBookTimesHitWall(times) {
		t.Fatal("6-й через 20 мин — не стена")
	}
}
