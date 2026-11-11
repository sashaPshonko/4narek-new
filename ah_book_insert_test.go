package main

import (
	"database/sql"
	"fmt"
	"testing"
)

func TestInsertAhBookBatchCompact(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/b.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := mlDB
	mlDB = db
	enchNameIDs = map[string]int64{}
	ahSellerIDCache = map[string]int64{}
	defer func() { mlDB = old }()
	initAhBookTable()
	var rows []ahBookWire
	for i := 0; i < 60; i++ {
		rows = append(rows, ahBookWire{Uuid: fmt.Sprintf("u%d", i), GoType: "netherite-1.21", ItemID: "sword7-1.21", Price: 1000 + i, Seller: "Nick",
			Enchants: []ItemEffect{{Name: "minecraft:sharpness", Lvl: 7}, {Name: "minecraft:looting", Lvl: 4}}})
	}
	insertAhBookBatch(rows)
	insertAhBookBatch(rows[:5]) // повтор → upsert
	var n int
	var c string
	db.QueryRow(`SELECT count(*) FROM ah_book_lots`).Scan(&n)
	db.QueryRow(`SELECT enchants_c FROM ah_book_lots WHERE lot_id = ?`, ahLotKey("u3")).Scan(&c)
	if n != 60 || c != "1:7,2:4" {
		t.Fatalf("n=%d c=%q", n, c)
	}
}
