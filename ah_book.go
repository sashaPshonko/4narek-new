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

type ahBookWire struct {
	Uuid       string       `json:"uuid"`
	GoType     string       `json:"go_type"`
	ItemID     string       `json:"item_id"`
	Price      int          `json:"price"`
	Durability *float64     `json:"durability"`
	Seller     string       `json:"seller"`
	Enchants   []ItemEffect `json:"enchants"`
	Anarchy    any          `json:"anarchy"`
	SeenBy     string       `json:"seen_by"`
	enchJSON   string       `json:"-"`
}

func insertAhBookBatch(rows []ahBookWire) {
	if mlDB == nil || len(rows) == 0 {
		return
	}
	mutex.RLock()
	keep := make([]ahBookWire, 0, len(rows))
	for _, r := range rows {
		if isFleetSellerLocked(r.Seller) {
			continue
		}
		keep = append(keep, r)
	}
	mutex.RUnlock()
	ts := time.Now().UTC().Format(time.RFC3339)
	for _, r := range keep {
		uuid := strings.TrimSpace(r.Uuid)
		if uuid == "" || r.ItemID == "" {
			continue
		}
		ench := r.enchJSON
		if ench == "" {
			ench = tradeEnchantsJSON(r.Enchants)
		}
		var dur any
		if r.Durability != nil {
			dur = *r.Durability
		}
		_, err := mlDB.Exec(
			`INSERT OR IGNORE INTO ah_book_lots (uuid, ts, go_type, item_id, price, durability, seller, enchants_json, anarchy, seen_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid, ts, r.GoType, r.ItemID, r.Price, dur, strings.TrimSpace(r.Seller), ench, anarchyInt(r.Anarchy), r.SeenBy,
		)
		if err != nil {
			log.Printf("[ah_book] insert: %v", err)
		}
	}
}
