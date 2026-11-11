package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestPruneAhBookKeepsFreshLots(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/p.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := mlDB
	mlDB = db
	defer func() { mlDB = old }()
	initAhBookTable()

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ins := func(uuid, item string, age time.Duration) {
		_, err := db.Exec(`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
			ahLotKey(uuid), now.Add(-age).Format(time.RFC3339), "t", item, 1000)
		if err != nil {
			t.Fatal(err)
		}
	}
	ins("a", "sword7-1.21", 1*time.Hour)  // свежий — остаётся
	ins("b", "sword7-1.21", 71*time.Hour) // почти на границе — остаётся
	ins("c", "sword7-1.21", 80*time.Hour) // старый — удаляется
	ins("d", "шлем-1.21", 5*24*time.Hour) // старый другого товара — удаляется
	ins("e", "шлем-1.21", 2*time.Hour)    // свежий — остаётся

	if got := pruneAhBookOnce(now, ahBookKeep); got != 2 {
		t.Fatalf("удалено %d, ждали 2", got)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM ah_book_lots`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("осталось %d (err %v), ждали 3", left, err)
	}
}
