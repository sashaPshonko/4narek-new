package main

import (
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

// Витрина: ≥N разных лотов одного SKU с одного ника за окно → вечный бан для нашего min.
// Сами строки в ah_book_lots не трогаем (олух / разбор витрин).
const (
	ahBookWallMinLots = 3
	ahBookWallWindow  = 15 * time.Minute
)

// skipAhBookBackfillOnInit — тесты; иначе async backfill гоняется с db.Exec без mlDBMu.
var skipAhBookBackfillOnInit bool

// ah_lot — снимок чужого лота с АХ. В adjustPrice не входит.
func initAhBookTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
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
		mlDBMu.Unlock()
		log.Printf("[ah_book] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_item_ts ON ah_book_lots(item_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_go_ts ON ah_book_lots(go_type, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_seller_item_ts ON ah_book_lots(seller, item_id, ts)`)
	err = ensureAhBookSellerBanTableLocked()
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[ah_book] ban schema: %v", err)
		return
	}
	if skipAhBookBackfillOnInit {
		return
	}
	// Backfill на старте больше не гоняем: держит mlDBMu, гоняется с initCapitalTables
	// (HTTP уже поднят) и вешает /sales. Живые витрины — maybeBanAhBookWallSellers.
	// Разовый прогон: AH_BOOK_BACKFILL=1.
	if os.Getenv("AH_BOOK_BACKFILL") == "1" {
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logPanic("ahBookBackfill", recovered)
				}
			}()
			backfillAhBookSellerBans()
		}()
		return
	}
	log.Printf("[ah_book] startup backfill off (AH_BOOK_BACKFILL=1 to force)")
}

func ensureAhBookSellerBanTable() error {
	if mlDB == nil {
		return nil
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	return ensureAhBookSellerBanTableLocked()
}

func ensureAhBookSellerBanTableLocked() error {
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
	mlDBMu.Lock()
	res, err := mlDB.Exec(
		`INSERT OR IGNORE INTO ah_book_seller_bans (seller, ts, item_id, n, window_sec) VALUES (?,?,?,?,?)`,
		key, time.Now().UTC().Format(time.RFC3339), itemID, n, int(ahBookWallWindow.Seconds()),
	)
	mlDBMu.Unlock()
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
	mlDBMu.Lock()
	err := mlDB.QueryRow(
		`SELECT COUNT(*) FROM ah_book_lots WHERE lower(trim(seller)) = ? AND item_id = ? AND ts >= ?`,
		ahBookSellerKey(seller), itemID, since.UTC().Format(time.RFC3339),
	).Scan(&n)
	mlDBMu.Unlock()
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
	mlDBMu.Lock()
	rows, err := mlDB.Query(`
SELECT trim(seller), item_id, ts FROM ah_book_lots
WHERE trim(seller) != ''
ORDER BY lower(trim(seller)), item_id, ts`)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] ban backfill: %v", err)
		return
	}
	type hit struct {
		seller, itemID string
		times          []time.Time
	}
	var cur hit
	flush := func() {
		if ahBookTimesHitWall(cur.times) {
			// ban without re-entering mlDBMu — already held
			key := ahBookSellerKey(cur.seller)
			if key == "" || len(cur.times) < ahBookWallMinLots {
				return
			}
			res, err := mlDB.Exec(
				`INSERT OR IGNORE INTO ah_book_seller_bans (seller, ts, item_id, n, window_sec) VALUES (?,?,?,?,?)`,
				key, time.Now().UTC().Format(time.RFC3339), cur.itemID, len(cur.times), int(ahBookWallWindow.Seconds()),
			)
			if err != nil {
				log.Printf("[ah_book] ban insert %s: %v", key, err)
				return
			}
			if aff, _ := res.RowsAffected(); aff == 1 {
				log.Printf("[ah_book] seller ban forever %s sku=%s n=%d window=%s", key, cur.itemID, len(cur.times), ahBookWallWindow)
			}
		}
	}
	for rows.Next() {
		var seller, itemID, tsStr string
		if err := rows.Scan(&seller, &itemID, &tsStr); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			ts, _ = time.Parse(time.RFC3339Nano, tsStr)
		}
		if cur.seller != "" && (cur.seller != seller || cur.itemID != itemID) {
			flush()
			cur = hit{}
		}
		cur.seller, cur.itemID = seller, itemID
		cur.times = append(cur.times, ts)
	}
	if cur.seller != "" {
		flush()
	}
	_ = rows.Close()
	mlDBMu.Unlock()
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
	const unlockEvery = 25
	n := 0
	mlDBMu.Lock()
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
			`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, durability, seller, enchants_json, anarchy, seen_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(uuid) DO UPDATE SET
			   ts = excluded.ts,
			   go_type = excluded.go_type,
			   item_id = excluded.item_id,
			   price = excluded.price,
			   durability = excluded.durability,
			   seller = excluded.seller,
			   enchants_json = excluded.enchants_json,
			   anarchy = excluded.anarchy,
			   seen_by = excluded.seen_by`,
			uuid, ts, r.GoType, r.ItemID, r.Price, dur, seller, ench, anarchyInt(r.Anarchy), r.SeenBy,
		)
		if err != nil {
			log.Printf("[ah_book] insert: %v", err)
			continue
		}
		if seller != "" {
			pairs = append(pairs, [2]string{seller, r.ItemID})
		}
		n++
		// Отпускаем mlDBMu — иначе sales/adjust голодают за ah_lots flood.
		if n%unlockEvery == 0 {
			mlDBMu.Unlock()
			mlDBMu.Lock()
		}
	}
	mlDBMu.Unlock()
	maybeBanAhBookWallSellers(pairs)
}

// ahBookMinSince — min(price) по уникальным uuid SKU с ts≥since, без забаненных витрин.
// n = COUNT(DISTINCT uuid); ok только при n ≥ ahBookMinLotsInWindow.
func ahBookMinSince(itemID string, since time.Time) (minPrice, n int, ok bool) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return 0, 0, false
	}
	mlDBMu.Lock()
	err := mlDB.QueryRow(`
SELECT COUNT(DISTINCT a.uuid), COALESCE(MIN(a.price), 0) FROM ah_book_lots a
WHERE a.item_id = ? AND a.ts >= ?
AND NOT EXISTS (
	SELECT 1 FROM ah_book_seller_bans b
	WHERE b.seller = lower(trim(a.seller))
)`, itemID, since.UTC().Format(time.RFC3339)).Scan(&n, &minPrice)
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[ah_book] min since: %v", err)
		return 0, 0, false
	}
	if n < ahBookMinLotsInWindow || minPrice <= 0 {
		return minPrice, n, false
	}
	return minPrice, n, true
}

// ahBookP10Since — 10-й процентиль цен SKU в окне (витрины тоже).
// Для set_min: сырой min = дампы, медиана = витрины.
func ahBookP10Since(itemID string, since time.Time) (p10, n int, ok bool) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return 0, 0, false
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT price FROM ah_book_lots WHERE item_id = ? AND ts >= ? AND price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] p10 since: %v", err)
		return 0, 0, false
	}
	var ps []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			continue
		}
		ps = append(ps, p)
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	n = len(ps)
	if n < ahBookMinLotsInWindow {
		return 0, n, false
	}
	sort.Ints(ps)
	p10 = ps[n/10]
	if p10 <= 0 {
		return p10, n, false
	}
	return p10, n, true
}

// ahBookMinOfLastN — min(price) среди последних n лотов SKU (по ts), без забаненных витрин.
// ok только при ровно n строках.
func ahBookMinOfLastN(itemID string, n int) (minPrice int, ok bool) {
	if mlDB == nil || n <= 0 || strings.TrimSpace(itemID) == "" {
		return 0, false
	}
	var cnt int
	var minP int
	mlDBMu.Lock()
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
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[ah_book] min last: %v", err)
		return 0, false
	}
	if cnt < n || minP <= 0 {
		return 0, false
	}
	return minP, true
}
