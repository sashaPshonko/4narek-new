package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

func ensureCompactSchema() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	ensureCompactSchemaLocked()
}

func ensureCompactSchemaLocked() {
	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS items (
	id TEXT PRIMARY KEY,
	category_type TEXT NOT NULL
)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_items_category ON items(category_type)`)
	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS stock_snapshot_sets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	trigger_item TEXT,
	source TEXT NOT NULL,
	cycle_id INTEGER
)`)
	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS stock_snapshot_rows (
	set_id INTEGER NOT NULL,
	item_id TEXT NOT NULL,
	on_ah INTEGER NOT NULL,
	inv INTEGER NOT NULL,
	held INTEGER NOT NULL,
	price INTEGER NOT NULL,
	nacenka INTEGER NOT NULL,
	PRIMARY KEY (set_id, item_id)
)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_snap_sets_ts ON stock_snapshot_sets(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_snap_sets_cycle ON stock_snapshot_sets(cycle_id)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_snap_rows_item ON stock_snapshot_rows(item_id)`)
	ensureMLColumn(mlDB, "ml_decisions", "payload_gz", "BLOB")
}

func startMLCompactLoop() {
	goImmortal("mlCompact", func() {
		time.Sleep(90 * time.Second)
		idle := 0
		for {
			n := compactHistoryOnce()
			if n == 0 {
				idle++
				if idle == 1 {
					log.Printf("[ML] compact: старые снимки/payloads переписаны (VACUUM с панели сожмёт файл)")
				}
				time.Sleep(6 * time.Hour)
				continue
			}
			idle = 0
			time.Sleep(1500 * time.Millisecond)
		}
	})
	log.Printf("[ML] compact history: снимки категории → set/rows, gzip ml_decisions")
}

func compactHistoryOnce() int {
	return compactHistoryBatch(20, 40, 400, 400)
}

func compactHistoryBatch(snapGroups, payloads, enchants, sellers int) int {
	if mlDB == nil {
		return 0
	}
	n := 0
	n += compactStockSnapshotGroups(snapGroups)
	n += compactMLDecisionPayloads(payloads)
	n += compactEmptyEnchants(enchants)
	n += backfillAhSellerIDs(sellers)
	return n
}

func compactStockSnapshotGroups(limit int) int {
	if limit <= 0 || mlDB == nil {
		return 0
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	if !tableExistsLocked("stock_snapshots") {
		return 0
	}
	ensureCompactSchemaLocked()
	type grp struct {
		ts, trigger, source string
		cycleID             sql.NullInt64
	}
	rows, err := mlDB.Query(`
SELECT ts, COALESCE(trigger_item,''), COALESCE(source,''), MIN(cycle_id)
FROM stock_snapshots
GROUP BY ts, COALESCE(trigger_item,''), COALESCE(source,'')
LIMIT ?`, limit)
	if err != nil {
		log.Printf("[ML] compact snapshots list: %v", err)
		return 0
	}
	var groups []grp
	for rows.Next() {
		var g grp
		if err := rows.Scan(&g.ts, &g.trigger, &g.source, &g.cycleID); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	rows.Close()
	moved := 0
	for _, g := range groups {
		var cycle any
		if g.cycleID.Valid {
			cycle = g.cycleID.Int64
		}
		res, err := mlDB.Exec(`
INSERT INTO stock_snapshot_sets (ts, trigger_item, source, cycle_id) VALUES (?,?,?,?)`,
			g.ts, g.trigger, g.source, cycle)
		if err != nil {
			log.Printf("[ML] compact set insert: %v", err)
			continue
		}
		setID, err := res.LastInsertId()
		if err != nil || setID <= 0 {
			continue
		}
		itemRows, err := mlDB.Query(`
SELECT item_id, on_ah, inv, held, price, nacenka, COALESCE(category_type,'')
FROM stock_snapshots
WHERE ts = ? AND COALESCE(trigger_item,'') = ? AND COALESCE(source,'') = ?`,
			g.ts, g.trigger, g.source)
		if err != nil {
			log.Printf("[ML] compact rows: %v", err)
			continue
		}
		type skuRow struct {
			item, cat                 string
			ah, inv, held, price, nac int
		}
		var skus []skuRow
		for itemRows.Next() {
			var r skuRow
			if err := itemRows.Scan(&r.item, &r.ah, &r.inv, &r.held, &r.price, &r.nac, &r.cat); err != nil {
				continue
			}
			if r.item == "" {
				continue
			}
			skus = append(skus, r)
		}
		itemRows.Close()
		for _, r := range skus {
			if r.cat != "" {
				_, _ = mlDB.Exec(`INSERT OR IGNORE INTO items (id, category_type) VALUES (?, ?)`, r.item, r.cat)
			}
			_, _ = mlDB.Exec(`
INSERT OR REPLACE INTO stock_snapshot_rows (set_id, item_id, on_ah, inv, held, price, nacenka)
VALUES (?,?,?,?,?,?,?)`, setID, r.item, r.ah, r.inv, r.held, r.price, r.nac)
			moved++
		}
		_, _ = mlDB.Exec(`
DELETE FROM stock_snapshots
WHERE ts = ? AND COALESCE(trigger_item,'') = ? AND COALESCE(source,'') = ?`,
			g.ts, g.trigger, g.source)
	}
	if moved > 0 {
		log.Printf("[ML] compact snapshots: +%d sku-строк в set/rows", moved)
	}
	var left int
	_ = mlDB.QueryRow(`SELECT COUNT(*) FROM stock_snapshots`).Scan(&left)
	if left == 0 {
		_, _ = mlDB.Exec(`DROP TABLE IF EXISTS stock_snapshots`)
		log.Printf("[ML] compact snapshots: старая таблица пуста, drop")
	}
	return moved
}

func compactMLDecisionPayloads(limit int) int {
	if mlDB == nil || limit <= 0 {
		return 0
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	ensureCompactSchemaLocked()
	rows, err := mlDB.Query(`
SELECT id, payload_json FROM ml_decisions
WHERE payload_gz IS NULL AND payload_json IS NOT NULL AND length(payload_json) > 200
LIMIT ?`, limit)
	if err != nil {
		if !isNoSuchColumn(err) {
			log.Printf("[ML] compact payload list: %v", err)
		}
		return 0
	}
	type rec struct {
		id      int64
		payload string
	}
	var list []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			continue
		}
		list = append(list, r)
	}
	rows.Close()
	n := 0
	for _, r := range list {
		gz, err := gzipBytes([]byte(r.payload))
		if err != nil || len(gz) == 0 {
			continue
		}
		if _, err := mlDB.Exec(`UPDATE ml_decisions SET payload_gz = ?, payload_json = '' WHERE id = ?`, gz, r.id); err != nil {
			log.Printf("[ML] compact payload %d: %v", r.id, err)
			continue
		}
		n++
	}
	if n > 0 {
		log.Printf("[ML] compact ml_decisions: gzip %d payload", n)
	}
	return n
}

func compactEmptyEnchants(limit int) int {
	if mlDB == nil || limit <= 0 {
		return 0
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	res, err := mlDB.Exec(`
UPDATE trade_events SET enchants_json = NULL
WHERE id IN (
	SELECT id FROM trade_events
	WHERE enchants_json IS NOT NULL AND enchants_json IN ('', '[]', '{}', 'null')
	LIMIT ?
)`, limit)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

func backfillAhSellerIDs(limit int) int {
	if mlDB == nil || limit <= 0 {
		return 0
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	if err := ensureAhSellersTableLocked(); err != nil {
		return 0
	}
	rows, err := mlDB.Query(`
SELECT uuid, trim(seller) FROM ah_book_lots
WHERE seller IS NOT NULL AND trim(seller) != '' AND (seller_id IS NULL OR seller_id = 0)
LIMIT ?`, limit)
	if err != nil {
		return 0
	}
	type lot struct {
		uuid, seller string
	}
	var lots []lot
	for rows.Next() {
		var l lot
		if err := rows.Scan(&l.uuid, &l.seller); err != nil {
			continue
		}
		lots = append(lots, l)
	}
	rows.Close()
	n := 0
	for _, l := range lots {
		id := upsertAhSellerLocked(strings.ToLower(strings.TrimSpace(l.seller)))
		if id == 0 {
			continue
		}
		if _, err := mlDB.Exec(`UPDATE ah_book_lots SET seller_id = ? WHERE uuid = ?`, id, l.uuid); err != nil {
			continue
		}
		n++
	}
	return n
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(gz []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func tableExistsLocked(name string) bool {
	return tableExistsOn(mlDB, name)
}

func tableExistsOn(db *sql.DB, name string) bool {
	if db == nil {
		return false
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil && n > 0
}

func isNoSuchColumn(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such column") || strings.Contains(s, "has no column")
}

func runCompactDBCLI() {
	path := mlDBPath
	if path == "" {
		path = os.Getenv("ML_DB_PATH")
	}
	if path == "" {
		path = defaultMLDBPath
	}
	mlDBPath = path
	db, err := sql.Open("sqlite", mlOpenDSN(path))
	if err != nil {
		log.Fatalf("[compact] open: %v", err)
	}
	db.SetMaxOpenConns(1)
	mlDB = db
	ensureCompactSchema()
	total := 0
	for {
		n := compactHistoryBatch(200, 400, 5000, 2000)
		total += n
		if n == 0 {
			break
		}
	}
	_ = db.Close()
	mlDB = nil
	log.Printf("[compact] rewritten %d rows — vacuum…", total)
	runVacuumDBCLI()
}
