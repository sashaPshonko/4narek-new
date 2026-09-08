package main

import "log"

func syncItemsCatalog() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS items (
	id TEXT PRIMARY KEY,
	category_type TEXT NOT NULL
)`)
	if err != nil {
		log.Printf("[ML] items schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_items_category ON items(category_type)`)
	if len(itemsConfig) == 0 {
		return
	}
	tx, err := mlDB.Begin()
	if err != nil {
		log.Printf("[ML] items tx: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO items (id, category_type) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET category_type = excluded.category_type`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("[ML] items prepare: %v", err)
		return
	}
	n := 0
	for id, cfg := range itemsConfig {
		if _, err := stmt.Exec(id, cfg.Type); err != nil {
			log.Printf("[ML] items upsert %s: %v", id, err)
			continue
		}
		n++
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("[ML] items commit: %v", err)
		return
	}
	log.Printf("[ML] items catalog: %d sku", n)
}

func ensureAhSellersTableLocked() error {
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS ah_sellers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	nick TEXT NOT NULL UNIQUE
)`)
	if err != nil {
		return err
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_sellers_nick ON ah_sellers(nick)`)
	return nil
}

func upsertAhSellerLocked(nick string) int64 {
	if nick == "" || mlDB == nil {
		return 0
	}
	_, err := mlDB.Exec(`INSERT OR IGNORE INTO ah_sellers (nick) VALUES (?)`, nick)
	if err != nil {
		return 0
	}
	var id int64
	if err := mlDB.QueryRow(`SELECT id FROM ah_sellers WHERE nick = ?`, nick).Scan(&id); err != nil {
		return 0
	}
	return id
}
