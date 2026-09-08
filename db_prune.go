package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	pruneAhBookAfter        = 7 * 24 * time.Hour
	pruneSnapshotsAfter     = 21 * 24 * time.Hour
	pruneTradesAfter        = 90 * 24 * time.Hour
	pruneCyclesAfter        = 90 * 24 * time.Hour
	pruneServerEventsAfter  = 90 * 24 * time.Hour
	pruneMLDecisionsAfter   = 60 * 24 * time.Hour
	pruneMLShadowAfter      = 30 * 24 * time.Hour
	pruneDailyJSONAfter     = 90 * 24 * time.Hour
	pruneBatchSize          = 8000
	pruneMaxBatchesPerTable = 40
)

func startMLPruneLoop() {
	goImmortal("mlPrune", func() {
		time.Sleep(3 * time.Minute)
		for {
			pruneMLDatabase()
			pruneOldDailyJSON()
			time.Sleep(6 * time.Hour)
		}
	})
	log.Printf("[ML] prune loop: ah_book %s, snapshots %s, trades/cycles %s",
		pruneAhBookAfter, pruneSnapshotsAfter, pruneTradesAfter)
}

func pruneMLDatabase() {
	if mlDB == nil {
		return
	}
	now := time.Now().UTC()
	jobs := []struct {
		name string
		sql  string
		cut  time.Time
	}{
		{"ah_book_lots", `DELETE FROM ah_book_lots WHERE rowid IN (SELECT rowid FROM ah_book_lots WHERE ts < ? LIMIT ?)`, now.Add(-pruneAhBookAfter)},
		{"stock_snapshots", `DELETE FROM stock_snapshots WHERE id IN (SELECT id FROM stock_snapshots WHERE ts < ? LIMIT ?)`, now.Add(-pruneSnapshotsAfter)},
		{"trade_events", `DELETE FROM trade_events WHERE id IN (SELECT id FROM trade_events WHERE ts < ? LIMIT ?)`, now.Add(-pruneTradesAfter)},
		{"server_price_events", `DELETE FROM server_price_events WHERE id IN (SELECT id FROM server_price_events WHERE ts < ? LIMIT ?)`, now.Add(-pruneServerEventsAfter)},
		{"ml_decisions", `DELETE FROM ml_decisions WHERE id IN (SELECT id FROM ml_decisions WHERE logged_ts < ? LIMIT ?)`, now.Add(-pruneMLDecisionsAfter)},
		{"ml_shadow", `DELETE FROM ml_shadow WHERE id IN (SELECT id FROM ml_shadow WHERE ts < ? LIMIT ?)`, now.Add(-pruneMLShadowAfter)},
		{"capital_cycles", `DELETE FROM capital_cycles WHERE id IN (
			SELECT id FROM capital_cycles
			WHERE ts < ? AND COALESCE(fwd_done,0) >= 3
			LIMIT ?)`, now.Add(-pruneCyclesAfter)},
	}
	for _, job := range jobs {
		n := pruneTableBatched(job.sql, job.cut, job.name)
		if n > 0 {
			log.Printf("[ML] prune %s: %d rows", job.name, n)
		}
	}
	mlDBMu.Lock()
	if err := compactMLDatabaseLocked(); err != nil {
		log.Printf("[ML] prune compact: %v", err)
	}
	mlDBMu.Unlock()
}

func pruneTableBatched(query string, cutoff time.Time, name string) int64 {
	cut := cutoff.Format(time.RFC3339)
	var total int64
	for i := 0; i < pruneMaxBatchesPerTable; i++ {
		mlDBMu.Lock()
		res, err := mlDB.Exec(query, cut, pruneBatchSize)
		mlDBMu.Unlock()
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				return total
			}
			log.Printf("[ML] prune %s: %v", name, err)
			return total
		}
		n, _ := res.RowsAffected()
		total += n
		if n == 0 {
			break
		}
	}
	return total
}

func pruneOldDailyJSON() {
	entries, err := os.ReadDir(".")
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-pruneDailyJSONAfter)
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "data_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(".", name)) == nil {
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("[ML] prune data_*.json: %d files", n)
	}
}
