package main

import (
	"log"
	"strings"
	"time"
)

// Витрина: ≥N разных лотов одного SKU с одного ника за окно → вечный бан для нашего min.
// Сами строки в ah_book_lots не трогаем (олух / разбор витрин).
const (
	ahBookWallMinLots = 6
	ahBookWallWindow  = 15 * time.Minute
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
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_seller_item_ts ON ah_book_lots(seller, item_id, ts)`)
	if err := ensureAhBookSellerBanTable(); err != nil {
		log.Printf("[ah_book] ban schema: %v", err)
		return
	}
	backfillAhBookSellerBans()
}

func ensureAhBookSellerBanTable() error {
	if mlDB == nil {
		return nil
	}
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS ah_book_seller_bans (
	seller TEXT PRIMARY KEY,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	n INTEGER NOT NULL,
	window_sec INTEGER NOT NULL
)`)
	return err
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

func ahBookSellerKey(seller string) string {
	return strings.ToLower(strings.TrimSpace(seller))
}

func banAhBookSeller(seller, itemID string, n int) {
	key := ahBookSellerKey(seller)
	if mlDB == nil || key == "" || n < ahBookWallMinLots {
		return
	}
	res, err := mlDB.Exec(
		`INSERT OR IGNORE INTO ah_book_seller_bans (seller, ts, item_id, n, window_sec) VALUES (?,?,?,?,?)`,
		key, time.Now().UTC().Format(time.RFC3339), itemID, n, int(ahBookWallWindow.Seconds()),
	)
	if err != nil {
		log.Printf("[ah_book] ban insert %s: %v", key, err)
		return
	}
	aff, _ := res.RowsAffected()
	if aff == 1 {
		log.Printf("[ah_book] seller ban forever %s sku=%s n=%d window=%s", key, itemID, n, ahBookWallWindow)
	}
}

func countAhBookSellerSKUSince(seller, itemID string, since time.Time) int {
	if mlDB == nil {
		return 0
	}
	var n int
	err := mlDB.QueryRow(
		`SELECT COUNT(*) FROM ah_book_lots WHERE lower(trim(seller)) = ? AND item_id = ? AND ts >= ?`,
		ahBookSellerKey(seller), itemID, since.UTC().Format(time.RFC3339),
	).Scan(&n)
	if err != nil {
		log.Printf("[ah_book] wall count: %v", err)
		return 0
	}
	return n
}

func maybeBanAhBookWallSellers(pairs [][2]string) {
	if mlDB == nil || len(pairs) == 0 {
		return
	}
	since := time.Now().UTC().Add(-ahBookWallWindow)
	seen := map[string]struct{}{}
	for _, p := range pairs {
		seller, itemID := p[0], p[1]
		key := ahBookSellerKey(seller)
		if key == "" || itemID == "" {
			continue
		}
		sig := key + "\x00" + itemID
		if _, ok := seen[sig]; ok {
			continue
		}
		seen[sig] = struct{}{}
		n := countAhBookSellerSKUSince(seller, itemID, since)
		if n >= ahBookWallMinLots {
			banAhBookSeller(seller, itemID, n)
		}
	}
}

func ahBookTimesHitWall(times []time.Time) bool {
	if len(times) < ahBookWallMinLots {
		return false
	}
	for i := 0; i+ahBookWallMinLots-1 < len(times); i++ {
		if times[i+ahBookWallMinLots-1].Sub(times[i]) <= ahBookWallWindow {
			return true
		}
	}
	return false
}

func backfillAhBookSellerBans() {
	if mlDB == nil {
		return
	}
	rows, err := mlDB.Query(`
SELECT trim(seller), item_id, ts FROM ah_book_lots
WHERE trim(seller) != ''
ORDER BY lower(trim(seller)), item_id, ts`)
	if err != nil {
		log.Printf("[ah_book] ban backfill: %v", err)
		return
	}
	defer rows.Close()
	type grp struct {
		seller, item string
		times        []time.Time
	}
	var cur grp
	flush := func() {
		if ahBookTimesHitWall(cur.times) {
			banAhBookSeller(cur.seller, cur.item, len(cur.times))
		}
	}
	for rows.Next() {
		var seller, item, ts string
		if err := rows.Scan(&seller, &item, &ts); err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		if cur.seller != "" && (ahBookSellerKey(seller) != ahBookSellerKey(cur.seller) || item != cur.item) {
			flush()
			cur = grp{}
		}
		if cur.seller == "" {
			cur.seller, cur.item = seller, item
		}
		cur.times = append(cur.times, t)
	}
	if cur.seller != "" {
		flush()
	}
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
	pairs := make([][2]string, 0, len(keep))
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
		seller := strings.TrimSpace(r.Seller)
		_, err := mlDB.Exec(
			`INSERT OR IGNORE INTO ah_book_lots (uuid, ts, go_type, item_id, price, durability, seller, enchants_json, anarchy, seen_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid, ts, r.GoType, r.ItemID, r.Price, dur, seller, ench, anarchyInt(r.Anarchy), r.SeenBy,
		)
		if err != nil {
			log.Printf("[ah_book] insert: %v", err)
			continue
		}
		if seller != "" {
			pairs = append(pairs, [2]string{seller, r.ItemID})
		}
	}
	maybeBanAhBookWallSellers(pairs)
}

// ahBookMinOfLastN — min(price) среди последних n лотов SKU (по ts), без забаненных витрин.
// ok только при ровно n строках.
func ahBookMinOfLastN(itemID string, n int) (minPrice int, ok bool) {
	if mlDB == nil || n <= 0 || strings.TrimSpace(itemID) == "" {
		return 0, false
	}
	var cnt int
	var minP int
	err := mlDB.QueryRow(`
SELECT COUNT(*), COALESCE(MIN(price), 0) FROM (
	SELECT a.price FROM ah_book_lots a
	WHERE a.item_id = ?
	AND NOT EXISTS (
		SELECT 1 FROM ah_book_seller_bans b
		WHERE b.seller = lower(trim(a.seller))
	)
	ORDER BY a.ts DESC LIMIT ?
)`, itemID, n).Scan(&cnt, &minP)
	if err != nil {
		log.Printf("[ah_book] min last: %v", err)
		return 0, false
	}
	if cnt < n || minP <= 0 {
		return 0, false
	}
	return minP, true
}
