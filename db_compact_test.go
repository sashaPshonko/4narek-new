package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func withTempMLDB(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.db")
	db, err := sql.Open("sqlite", mlOpenDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := mlDB
	mlDB = db
	return func() {
		_ = db.Close()
		mlDB = old
		_ = os.Remove(path)
	}
}

func TestCompactStockSnapshotsKeepsAllSKUs(t *testing.T) {
	defer withTempMLDB(t)()
	ensureCompactSchemaLocked()
	_, err := mlDB.Exec(`
CREATE TABLE stock_snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	category_type TEXT NOT NULL,
	on_ah INTEGER NOT NULL,
	inv INTEGER NOT NULL,
	held INTEGER NOT NULL,
	price INTEGER NOT NULL,
	nacenka INTEGER NOT NULL,
	trigger_item TEXT,
	source TEXT NOT NULL,
	cycle_id INTEGER
)`)
	if err != nil {
		t.Fatal(err)
	}
	ts := "2026-09-01T12:00:00Z"
	for i, sku := range []string{"a", "b", "c"} {
		_, err := mlDB.Exec(`
INSERT INTO stock_snapshots (ts, item_id, category_type, on_ah, inv, held, price, nacenka, trigger_item, source, cycle_id)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			ts, sku, "swords", i, i+1, i+2, 100+i, 10, "a", "capital_cycle", 7)
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := compactStockSnapshotGroups(10); n != 3 {
		t.Fatalf("moved %d, want 3", n)
	}
	var sets, rows, items int
	_ = mlDB.QueryRow(`SELECT COUNT(*) FROM stock_snapshot_sets`).Scan(&sets)
	_ = mlDB.QueryRow(`SELECT COUNT(*) FROM stock_snapshot_rows`).Scan(&rows)
	_ = mlDB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&items)
	if sets != 1 || rows != 3 {
		t.Fatalf("sets=%d rows=%d want 1/3", sets, rows)
	}
	if items != 3 {
		t.Fatalf("items=%d want 3", items)
	}
	if tableExistsLocked("stock_snapshots") {
		t.Fatal("old table should be dropped when empty")
	}
	var held int
	_ = mlDB.QueryRow(`SELECT held FROM stock_snapshot_rows WHERE item_id='c'`).Scan(&held)
	if held != 4 {
		t.Fatalf("held c=%d want 4", held)
	}
}

func TestCompactEmptyEnchantsNullsPlaceholders(t *testing.T) {
	defer withTempMLDB(t)()
	_, err := mlDB.Exec(`
CREATE TABLE trade_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	enchants_json TEXT
)`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = mlDB.Exec(`INSERT INTO trade_events (enchants_json) VALUES ('[]'), ('{}'), ('real')`)
	if n := compactEmptyEnchants(10); n != 2 {
		t.Fatalf("nulled %d want 2", n)
	}
	var kept string
	_ = mlDB.QueryRow(`SELECT enchants_json FROM trade_events WHERE id=3`).Scan(&kept)
	if kept != "real" {
		t.Fatalf("kept %q", kept)
	}
}
