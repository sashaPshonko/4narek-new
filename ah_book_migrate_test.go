package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Старая книга (uuid TEXT PRIMARY KEY) при старте пересоздаётся в лёгкую схему, новые лоты пишутся и апсертятся.
func TestInitAhBookTableMigratesOldSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := mlDB
	mlDB = db
	enchNameIDs = map[string]int64{}
	ahSellerIDCache = map[string]int64{}
	t.Cleanup(func() { mlDB = old; db.Close() })
	if _, err := db.Exec(`CREATE TABLE ah_book_lots (uuid TEXT PRIMARY KEY, ts TEXT NOT NULL, go_type TEXT NOT NULL,
		item_id TEXT NOT NULL, price INTEGER NOT NULL, durability REAL, seller TEXT, enchants_json TEXT, anarchy INTEGER, seen_by TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES ('1,2,3','2026-11-08T00:00:00Z','t','x',5)`); err != nil {
		t.Fatal(err)
	}
	initAhBookTable()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ah_book_lots`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("после миграции таблица пустая, n=%d err=%v", n, err)
	}
	if mlDBHasUUID := func() bool { mlDBMu.Lock(); defer mlDBMu.Unlock(); return ahBookHasColumn("uuid") }(); mlDBHasUUID {
		t.Fatal("колонка uuid должна исчезнуть")
	}
	rows := []ahBookWire{{Uuid: "1,2,3", GoType: "t", ItemID: "x", Price: 10, Seller: "a"}}
	insertAhBookBatch(rows)
	rows[0].Price = 7
	insertAhBookBatch(rows)
	var price int
	if err := db.QueryRow(`SELECT count(*), price FROM ah_book_lots`).Scan(&n, &price); err != nil || n != 1 || price != 7 {
		t.Fatalf("upsert: n=%d price=%d err=%v", n, price, err)
	}
	initAhBookTable() // повторный старт — без потерь
	if err := db.QueryRow(`SELECT count(*) FROM ah_book_lots`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("повторный init стёр данные, n=%d", n)
	}
}
