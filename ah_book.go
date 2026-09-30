package main

import (
	"log"
	"sort"
	"strings"
	"time"
)

// skipAhBookBackfillOnInit — legacy test flag (ban backfill removed).
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
	ensureMLColumn(mlDB, "ah_book_lots", "seller_id", "INTEGER")
	if err := ensureAhSellersTableLocked(); err != nil {
		log.Printf("[ah_book] sellers schema: %v", err)
	}
	// Старая система банов витрин снята — таблицу дропаем, в расчётах больше не фильтруем.
	if _, err := mlDB.Exec(`DROP TABLE IF EXISTS ah_book_seller_bans`); err != nil {
		log.Printf("[ah_book] drop seller_bans: %v", err)
	}
	mlDBMu.Unlock()
	_ = skipAhBookBackfillOnInit
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
		sellerID := upsertAhSellerLocked(strings.ToLower(seller))
		_, err := mlDB.Exec(
			`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, durability, seller, seller_id, enchants_json, anarchy, seen_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(uuid) DO UPDATE SET
			   ts = excluded.ts,
			   go_type = excluded.go_type,
			   item_id = excluded.item_id,
			   price = excluded.price,
			   durability = excluded.durability,
			   seller = excluded.seller,
			   seller_id = excluded.seller_id,
			   enchants_json = excluded.enchants_json,
			   anarchy = excluded.anarchy,
			   seen_by = excluded.seen_by`,
			uuid, ts, r.GoType, r.ItemID, r.Price, dur, seller, sellerID, ench, anarchyInt(r.Anarchy), r.SeenBy,
		)
		if err != nil {
			log.Printf("[ah_book] insert: %v", err)
			continue
		}
		n++
		// Отпускаем mlDBMu — иначе sales/adjust голодают за ah_lots flood.
		if n%unlockEvery == 0 {
			mlDBMu.Unlock()
			mlDBMu.Lock()
		}
	}
	mlDBMu.Unlock()
}

// ahBookMarketRecoverySnap — книга за длинное окно для shadow market_recovery (не 10m ah_book).
type ahBookMarketRecoverySnap struct {
	MinAsk  int
	P10     int
	NSell   int // unique sellers по книге
	NUUID   int // unique uuid по книге
	NRows   int
	OK      bool
}

// ahBookMarketRecoveryStats — min/p10 + unique sellers/uuid за since…now, по книге.
// p10 по всем лотам окна (как ahBookP10Since); min/sellers/uuid — .
// Не использует nacenka. Не меняет пороги обычного 10m ah_book.
func ahBookMarketRecoveryStats(itemID string, since time.Time) ahBookMarketRecoverySnap {
	var out ahBookMarketRecoverySnap
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return out
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT a.price, lower(trim(coalesce(a.seller,''))), a.uuid
		 FROM ah_book_lots a
		 WHERE a.item_id = ? AND a.ts >= ? AND a.price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] market_recovery stats: %v", err)
		return out
	}
	allPrices := make([]int, 0, 256)
	byUUID := map[string]int{}
	sellers := map[string]struct{}{}
	for rows.Next() {
		var price int
		var seller, uuid string
		if err := rows.Scan(&price, &seller, &uuid); err != nil || price <= 0 {
			continue
		}
		allPrices = append(allPrices, price)
		out.NRows++
		if seller != "" {
			sellers[seller] = struct{}{}
		}
		if uuid == "" {
			continue
		}
		if prev, ok := byUUID[uuid]; !ok || price < prev {
			byUUID[uuid] = price
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()

	out.NSell = len(sellers)
	out.NUUID = len(byUUID)
	if out.NUUID == 0 || len(allPrices) == 0 {
		return out
	}
	minAsk := 0
	for _, p := range byUUID {
		if minAsk == 0 || p < minAsk {
			minAsk = p
		}
	}
	out.MinAsk = minAsk
	sort.Ints(allPrices)
	out.P10 = allPrices[len(allPrices)/10]
	out.OK = out.MinAsk > 0 && out.P10 > 0
	return out
}

// ahBookTrustedSellerMinSnap — sell-side min по независимым продавцам (не uuid-flood).
// На продавца берём его самый дешёвый лот; флот отфильтрован на insert.
// Не использует nacenka. Не меняет ahBookMinSince / 10m raise logic.
type ahBookTrustedSellerMinSnap struct {
	TrustedMin       int // min среди per-seller mins
	UniqueSellers    int
	SellersNearMin   int // sellers with min <= TrustedMin + nearSlack
	NUUID            int // informational
	OK               bool
}

// ahBookTrustedSellerMin — книга за since…now: per-seller min → global min + trust counts.
func ahBookTrustedSellerMin(itemID string, since time.Time, nearSlackSteps, step int) ahBookTrustedSellerMinSnap {
	var out ahBookTrustedSellerMinSnap
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return out
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT a.price, lower(trim(coalesce(a.seller,''))), a.uuid
		 FROM ah_book_lots a
		 WHERE a.item_id = ? AND a.ts >= ? AND a.price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] trusted seller min: %v", err)
		return out
	}
	bySeller := map[string]int{}
	uuids := map[string]struct{}{}
	for rows.Next() {
		var price int
		var seller, uuid string
		if err := rows.Scan(&price, &seller, &uuid); err != nil || price <= 0 {
			continue
		}
		if seller == "" {
			continue
		}
		if prev, ok := bySeller[seller]; !ok || price < prev {
			bySeller[seller] = price
		}
		if uuid != "" {
			uuids[uuid] = struct{}{}
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()

	out.UniqueSellers = len(bySeller)
	out.NUUID = len(uuids)
	if out.UniqueSellers == 0 {
		return out
	}
	minAsk := 0
	for _, p := range bySeller {
		if minAsk == 0 || p < minAsk {
			minAsk = p
		}
	}
	out.TrustedMin = minAsk
	near := minAsk
	if step > 0 && nearSlackSteps > 0 {
		near = minAsk + nearSlackSteps*step
	}
	for _, p := range bySeller {
		if p <= near {
			out.SellersNearMin++
		}
	}
	out.OK = out.TrustedMin > 0
	return out
}

// ahBookMinSince — min(price) по уникальным uuid SKU с ts≥since.
// n = COUNT(DISTINCT uuid); ok только при n ≥ ahBookMinLotsInWindow.
func ahBookMinSince(itemID string, since time.Time) (minPrice, n int, ok bool) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return 0, 0, false
	}
	mlDBMu.Lock()
	err := mlDB.QueryRow(`
SELECT COUNT(DISTINCT a.uuid), COALESCE(MIN(a.price), 0) FROM ah_book_lots a
WHERE a.item_id = ? AND a.ts >= ?`, itemID, since.UTC().Format(time.RFC3339)).Scan(&n, &minPrice)
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

// ahBookPricesSince — цены лотов SKU в окне, отсортированные по возрастанию.
func ahBookPricesSince(itemID string, since time.Time) (ps []int, n int) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return nil, 0
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT price FROM ah_book_lots WHERE item_id = ? AND ts >= ? AND price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] prices since: %v", err)
		return nil, 0
	}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			continue
		}
		if p > 0 {
			ps = append(ps, p)
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	n = len(ps)
	if n == 0 {
		return nil, 0
	}
	sort.Ints(ps)
	return ps, n
}

// ahBookUniquePricesSince — последняя цена каждого uuid в окне, по возрастанию.
// Для opt buyMax (K-й дешёвый), без раздува от повторных сканов.
func ahBookUniquePricesSince(itemID string, since time.Time) (ps []int, n int) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return nil, 0
	}
	ts := since.UTC().Format(time.RFC3339)
	mlDBMu.Lock()
	rows, err := mlDB.Query(`
SELECT b.price FROM ah_book_lots b
INNER JOIN (
  SELECT uuid, MAX(ts) AS mts FROM ah_book_lots
  WHERE item_id = ? AND ts >= ? AND price > 0
  GROUP BY uuid
) t ON b.uuid = t.uuid AND b.ts = t.mts AND b.item_id = ?
WHERE b.price > 0`, itemID, ts, itemID)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] unique prices: %v", err)
		return nil, 0
	}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			continue
		}
		if p > 0 {
			ps = append(ps, p)
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	n = len(ps)
	if n == 0 {
		return nil, 0
	}
	sort.Ints(ps)
	return ps, n
}

// ahBookPercentileSorted — q∈[0,1] по уже отсортированному срезу.
func ahBookPercentileSorted(sorted []int, q float64) int {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	k := q * float64(n-1)
	f := int(k)
	c := f + 1
	if c >= n {
		return sorted[n-1]
	}
	w := k - float64(f)
	return int(float64(sorted[f])*(1-w) + float64(sorted[c])*w + 0.5)
}

// ahBookSellerMinPricesSince — по одному мин. лоту на продавца в окне (по книге).
// Убирает раздув от 50 одинаковых uuid одного флота / повторных сканов.
func ahBookSellerMinPricesSince(itemID string, since time.Time) (ps []int, nSellers int) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return nil, 0
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT a.price, lower(trim(coalesce(a.seller,'')))
		 FROM ah_book_lots a
		 WHERE a.item_id = ? AND a.ts >= ? AND a.price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] seller mins: %v", err)
		return nil, 0
	}
	bySeller := map[string]int{}
	for rows.Next() {
		var price int
		var seller string
		if err := rows.Scan(&price, &seller); err != nil || price <= 0 {
			continue
		}
		if seller == "" {
			continue
		}
		if prev, ok := bySeller[seller]; !ok || price < prev {
			bySeller[seller] = price
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	if len(bySeller) == 0 {
		return nil, 0
	}
	ps = make([]int, 0, len(bySeller))
	for _, p := range bySeller {
		ps = append(ps, p)
	}
	sort.Ints(ps)
	return ps, len(ps)
}

// ahBookMarketAnchorsSince — якоря из per-seller min ():
//   buyEdge  = p10 — нижний край, до него боты должны доставать buyMax;
//   sellMkt  = p40 — конкурентный sell (не дамп p10 и не стена клонов lot-p10).
const (
	ahBookMarketBuyPct     = 0.10
	ahBookMarketSellPct    = 0.40
	ahBookMarketMinSellers = 3
	// Дорогие SKU: только селлеры с ≥N лотами одного item (не разовый лот).
	ahBookMultiSellerMinLots = 3
)

func ahBookMarketAnchorsSince(itemID string, since time.Time) (sellMkt, buyEdge, nSellers int, ok bool) {
	ps, n := ahBookSellerMinPricesSince(itemID, since)
	if n < ahBookMarketMinSellers {
		return 0, 0, n, false
	}
	buyEdge = ahBookPercentileSorted(ps, ahBookMarketBuyPct)
	sellMkt = ahBookPercentileSorted(ps, ahBookMarketSellPct)
	if buyEdge <= 0 || sellMkt <= 0 {
		return 0, 0, n, false
	}
	if sellMkt < buyEdge {
		sellMkt = buyEdge
	}
	return sellMkt, buyEdge, n, true
}

// ahBookMultiSellerMinPricesSince — мин. цена только у продавцов с ≥minLots лотов SKU в окне.
// «Селлер» = пачка одного меча, не разовый рандом. 
func ahBookMultiSellerMinPricesSince(itemID string, since time.Time, minLots int) (ps []int, nSellers int) {
	if mlDB == nil || strings.TrimSpace(itemID) == "" || since.IsZero() {
		return nil, 0
	}
	if minLots < 2 {
		minLots = ahBookMultiSellerMinLots
	}
	mlDBMu.Lock()
	rows, err := mlDB.Query(
		`SELECT a.price, lower(trim(coalesce(a.seller,'')))
		 FROM ah_book_lots a
		 WHERE a.item_id = ? AND a.ts >= ? AND a.price > 0`,
		itemID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] multi seller mins: %v", err)
		return nil, 0
	}
	type agg struct {
		n   int
		min int
	}
	bySeller := map[string]*agg{}
	for rows.Next() {
		var price int
		var seller string
		if err := rows.Scan(&price, &seller); err != nil || price <= 0 {
			continue
		}
		if seller == "" {
			continue
		}
		st, ok := bySeller[seller]
		if !ok {
			bySeller[seller] = &agg{n: 1, min: price}
			continue
		}
		st.n++
		if price < st.min {
			st.min = price
		}
	}
	_ = rows.Close()
	mlDBMu.Unlock()
	ps = make([]int, 0, len(bySeller))
	for _, st := range bySeller {
		if st.n >= minLots && st.min > 0 {
			ps = append(ps, st.min)
		}
	}
	if len(ps) == 0 {
		return nil, 0
	}
	sort.Ints(ps)
	return ps, len(ps)
}

// ahBookExpensiveBottomAnchorsSince — дорогие: sell/buyEdge = самое дно мульти-селлеров.
// Достаточно ≥1 селлера с пачкой (≥3 лота); не ждём «толпу» из 3+.
func ahBookExpensiveBottomAnchorsSince(itemID string, since time.Time) (sellMkt, buyEdge, nSellers int, ok bool) {
	ps, n := ahBookMultiSellerMinPricesSince(itemID, since, ahBookMultiSellerMinLots)
	if n < 1 {
		return 0, 0, n, false
	}
	bottom := ps[0] // abs min среди селлеров с ≥3 лотами
	if bottom <= 0 {
		return 0, 0, n, false
	}
	return bottom, bottom, n, true
}

// ahBookMarketAnchorSince — sell-якорь (p40 seller-mins). Для полов/volume gate.
func ahBookMarketAnchorSince(itemID string, since time.Time) (anchor, nSellers int, ok bool) {
	sellMkt, _, n, ok := ahBookMarketAnchorsSince(itemID, since)
	return sellMkt, n, ok
}

// ahBookP10Since — 10-й процентиль цен SKU в окне (витрины тоже).
// Для set_min: сырой min = дампы, медиана = витрины.
func ahBookP10Since(itemID string, since time.Time) (p10, n int, ok bool) {
	ps, n := ahBookPricesSince(itemID, since)
	if n < ahBookMinLotsInWindow {
		return 0, n, false
	}
	p10 = ps[n/10] // совместимость с прежним индексом ≈ p10
	if p10 <= 0 {
		return p10, n, false
	}
	return p10, n, true
}

// ahBookP5P10Since — p5 и p10 одной живой выборки (book2 buy/sell).
func ahBookP5P10Since(itemID string, since time.Time) (p5, p10, n int, ok bool) {
	ps, n := ahBookPricesSince(itemID, since)
	if n < ahBookMinLotsInWindow {
		return 0, 0, n, false
	}
	p5 = ahBookPercentileSorted(ps, 0.05)
	p10 = ps[n/10]
	if p10 <= 0 {
		return p5, p10, n, false
	}
	return p5, p10, n, true
}

// ahBookMinOfLastN — min(price) среди последних n лотов SKU (по ts).
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
