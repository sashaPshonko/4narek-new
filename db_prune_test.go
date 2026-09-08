package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPruneAhBookOldRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	db, err := sql.Open("sqlite", mlOpenDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ah_book_lots (
		uuid TEXT PRIMARY KEY, ts TEXT NOT NULL, go_type TEXT, item_id TEXT, price INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"old", old, "t", "sku", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"new", fresh, "t", "sku", 2); err != nil {
		t.Fatal(err)
	}

	prev := mlDB
	mlDB = db
	t.Cleanup(func() { mlDB = prev })
	n := pruneTableBatched(
		`DELETE FROM ah_book_lots WHERE rowid IN (SELECT rowid FROM ah_book_lots WHERE ts < ? LIMIT ?)`,
		time.Now().UTC().Add(-7*24*time.Hour),
		"ah_book_lots",
	)
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ah_book_lots`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("left %d, want 1", left)
	}
}
