package main

import (
	"log"
	"strings"
	"time"
)

// ah_lot — снимок чужого лота с АХ. В adjustPrice не входит.
func initAhBookTable() {
	if mlDB == nil {
		return
	}
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS ah_book_lots (
	uuid TEXT PRIMARY KEY,
	ts TEXT NOT NULL,
	go_type TEXT NOT NULL,
	item_id TEXT NOT NULL,
	price INTEGER NOT NULL,
	durability REAL,
	seller TEXT,
	enchants_json TEXT,
	anarchy INTEGER,
	seen_by TEXT
)`)
	if err != nil {
		log.Printf("[ah_book] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_item_ts ON ah_book_lots(item_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_go_ts ON ah_book_lots(go_type, ts)`)
}

func isFleetSellerLocked(seller string) bool {
	nick := strings.TrimSpace(seller)
	if nick == "" {
		return false
	}
	for _, set := range fleetNickRoster {
		for n := range set {
			if strings.EqualFold(n, nick) {
				return true
			}
		}
	}
	return false
}

func insertAhBookLotLocked(uuid, goType, itemID string, price int, durability *float64, seller, enchJSON string, anarchy int, seenBy string) {
	if mlDB == nil {
		return
	}
	uuid = strings.TrimSpace(uuid)
	if uuid == "" || itemID == "" {
		return
	}
	if isFleetSellerLocked(seller) {
		return
	}
	var dur any
	if durability != nil {
		dur = *durability
	}
	_, err := mlDB.Exec(
		`INSERT OR IGNORE INTO ah_book_lots (uuid, ts, go_type, item_id, price, durability, seller, enchants_json, anarchy, seen_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid, time.Now().UTC().Format(time.RFC3339), goType, itemID, price, dur, strings.TrimSpace(seller), enchJSON, anarchy, seenBy,
	)
	if err != nil {
		log.Printf("[ah_book] insert: %v", err)
	}
}
