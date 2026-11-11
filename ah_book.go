package main

import (
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"time"
)

// skipAhBookBackfillOnInit — legacy test flag (ban backfill removed).
var skipAhBookBackfillOnInit bool

// ahLotKey — компактный ключ лота: FNV-64a от uuid (маска до 63 бит, чтобы помещался в INTEGER PRIMARY KEY).
// Коллизии на сотнях тысяч лотов практически невозможны (≈10^-8), а при совпадении просто обновится чужая строка кэша.
func ahLotKey(uuid string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uuid))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// ahBookHasColumn — есть ли колонка в текущей таблице книги (mlDBMu уже взят вызывающим).
func ahBookHasColumn(col string) bool {
	rows, err := mlDB.Query(`SELECT name FROM pragma_table_info('ah_book_lots')`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n == col {
			return true
		}
	}
	return false
}

// ah_lot — снимок чужого лота с АХ. В adjustPrice не входит.
func initAhBookTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	// Старая схема (uuid TEXT PRIMARY KEY, ~57 символов на лот + дубль в служебном индексе, enchants_json) весила
	// ~310 байт на лот. Книга — кэш на 3 суток, поэтому при старой схеме таблицу просто пересоздаём.
	if ahBookHasColumn("uuid") {
		log.Printf("[ah_book] старая схема книги — пересоздаю (кэш наполнится из сканов)")
		_, _ = mlDB.Exec(`DROP TABLE IF EXISTS ah_book_lots`)
	}
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS ah_book_lots (
	lot_id INTEGER PRIMARY KEY, -- 63-битный хеш uuid лота (ahLotKey): ключ и rowid в одном
	ts TEXT NOT NULL,
	go_type TEXT NOT NULL,
	item_id TEXT NOT NULL,
	price INTEGER NOT NULL,
	durability REAL,
	seller TEXT,
	seller_id INTEGER,
	enchants_c TEXT,
	anarchy INTEGER,
	seen_by TEXT
)`)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[ah_book] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ah_book_item_ts ON ah_book_lots(item_id, ts)`)
	_, _ = mlDB.Exec(`DROP INDEX IF EXISTS idx_ah_book_go_ts`)
	_, _ = mlDB.Exec(`DROP INDEX IF EXISTS idx_ah_book_seller_item_ts`)
	if err := ensureEnchNamesTableLocked(); err != nil {
		log.Printf("[ah_book] ench_names schema: %v", err)
	}
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
	// Пачками по 25 лотов в одной транзакции (один commit вместо 25), между пачками отпускаем mlDBMu —
	// иначе sales/adjust голодают за ah_lots flood. Словари (продавцы, зачарования) читаем ДО Begin:
	// соединение одно (MaxOpenConns=1), запрос внутри открытой транзакции заблокировал бы сам себя.
	const chunk = 25
	type prepared struct {
		r        ahBookWire
		uuid     string
		seller   string
		sellerID int64
		ench     string
		dur      any
	}
	for start := 0; start < len(keep); start += chunk {
		end := start + chunk
		if end > len(keep) {
			end = len(keep)
		}
		mlDBMu.Lock()
		batch := make([]prepared, 0, end-start)
		for _, r := range keep[start:end] {
			uuid := strings.TrimSpace(r.Uuid)
			if uuid == "" || r.ItemID == "" {
				continue
			}
			var dur any
			if r.Durability != nil {
				dur = *r.Durability
			}
			seller := strings.TrimSpace(r.Seller)
			batch = append(batch, prepared{
				r: r, uuid: uuid, seller: seller, dur: dur,
				sellerID: upsertAhSellerLocked(strings.ToLower(seller)),
				ench:     encodeEnchantsCompactLocked(r.Enchants),
			})
		}
		if len(batch) > 0 {
			tx, err := mlDB.Begin()
			if err != nil {
				log.Printf("[ah_book] begin: %v", err)
			} else {
				for _, p := range batch {
					if _, err := tx.Exec(
						`INSERT INTO ah_book_lots (lot_id, ts, go_type, item_id, price, durability, seller, seller_id, enchants_c, anarchy, seen_by)
						 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
						 ON CONFLICT(lot_id) DO UPDATE SET
						   ts = excluded.ts,
						   go_type = excluded.go_type,
						   item_id = excluded.item_id,
						   price = excluded.price,
						   durability = excluded.durability,
						   seller = excluded.seller,
						   seller_id = excluded.seller_id,
						   enchants_c = excluded.enchants_c,
						   anarchy = excluded.anarchy,
						   seen_by = excluded.seen_by`,
						ahLotKey(p.uuid), ts, p.r.GoType, p.r.ItemID, p.r.Price, p.dur, p.seller, p.sellerID, p.ench, anarchyInt(p.r.Anarchy), p.r.SeenBy,
					); err != nil {
						log.Printf("[ah_book] insert: %v", err)
					}
				}
				if err := tx.Commit(); err != nil {
					log.Printf("[ah_book] commit: %v", err)
				}
			}
		}
		mlDBMu.Unlock()
	}
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
//
//	buyEdge  = p10 — нижний край, до него боты должны доставать buyMax;
//	sellMkt  = p40 — конкурентный sell (не дамп p10 и не стена клонов lot-p10).
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

// ============ чистка книги АХ ============
//
// Раньше ah_book_lots не чистилась: за 9 суток она выросла до 4.5 ГБ, полные проходы по ней (бэкфиллы) держали
// mlDBMu по 30+ секунд, и страница с продажами зависала. Книге нужны только свежие окна (самое длинное — 24 ч),
// поэтому храним ahBookKeep и раз в час удаляем старьё по индексу (item_id, ts), по одному товару за раз:
// mlDBMu отпускается между товарами, а файл больше не растёт (место освобождается для новых вставок).

const (
	ahBookKeep          = 3 * 24 * time.Hour
	ahBookPruneEvery    = time.Hour
	ahBookPruneFirstRun = 5 * time.Minute
)

// pruneAhBookOnce удаляет лоты старше keep. Возвращает число удалённых строк.
func pruneAhBookOnce(now time.Time, keep time.Duration) int {
	if mlDB == nil {
		return 0
	}
	cutoff := now.Add(-keep).UTC().Format(time.RFC3339)
	mlDBMu.Lock()
	rows, err := mlDB.Query(`SELECT DISTINCT item_id FROM ah_book_lots`)
	var items []string
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				items = append(items, id)
			}
		}
		rows.Close()
	}
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[ah_book] prune: список товаров: %v", err)
		return 0
	}
	total := 0
	for _, id := range items {
		mlDBMu.Lock()
		res, err := mlDB.Exec(`DELETE FROM ah_book_lots WHERE item_id = ? AND ts < ?`, id, cutoff)
		mlDBMu.Unlock()
		if err != nil {
			log.Printf("[ah_book] prune %s: %v", id, err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			total += int(n)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return total
}

func startAhBookPruneLoop() {
	goImmortal("ahBookPrune", func() {
		time.Sleep(ahBookPruneFirstRun)
		for {
			if n := pruneAhBookOnce(time.Now(), ahBookKeep); n > 0 {
				log.Printf("[ah_book] prune: удалено %d лотов старше %v", n, ahBookKeep)
			}
			time.Sleep(ahBookPruneEvery)
		}
	})
}
