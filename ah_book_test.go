package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	skipAhBookBackfillOnInit = true
}

func TestIsFleetSellerLocked(t *testing.T) {
	old := fleetNickRoster
	fleetNickRoster = funauthRoster{
		503: {"rebr0_tv3": {}},
	}
	t.Cleanup(func() { fleetNickRoster = old })
	if !isFleetSellerLocked("rebr0_tv3") {
		t.Fatal("own nick")
	}
	if !isFleetSellerLocked("Rebr0_Tv3") {
		t.Fatal("case")
	}
	if isFleetSellerLocked("Beyermy") {
		t.Fatal("competitor")
	}
	if isFleetSellerLocked("") {
		t.Fatal("empty is unknown, not fleet")
	}
}

func TestShouldRaiseFromAhBook(t *testing.T) {
	n := ahBookMinLotsInWindow
	minAsk := 1_000_000
	nac := 300_000
	// живой разбор: held>0, sales≥3, buys=0
	if !shouldRaiseFromAhBook(400_000, minAsk, nac, n, 5, 0, 4, false, false, false) {
		t.Fatal("селл ниже min+наценка + разбор — поднимаем")
	}
	if shouldRaiseFromAhBook(minAsk+nac, minAsk, nac, n, 5, 0, 4, false, false, false) {
		t.Fatal("уже на min+наценка — не трогаем")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n-1, 5, 0, 4, false, false, false) {
		t.Fatal("мало uuid в окне — рано")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 5, 0, 4, true, false, false) {
		t.Fatal("dump — не поднимаем")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 5, 0, 4, false, true, false) {
		t.Fatal("уже ↓ в этом цикле")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 5, 0, 4, false, false, true) {
		t.Fatal("были покупки — не поднимаем")
	}
	// v8r: пусто — обычный ah_book-ап не трогаем, для этого отдельный empty_book путь.
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 0, 0, 0, false, false, false) {
		t.Fatal("held=0 sales=0 — не обычный ah_book raise")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 2, 0, 4, false, false, false) {
		t.Fatal("sales<minForUp — не поднимаем")
	}
	if shouldRaiseFromAhBook(400_000, minAsk, nac, n, 5, 6, 4, false, false, false) {
		t.Fatal("buys≥sales — не поднимаем")
	}
}


func TestShouldRaiseEmptyFromAhBook(t *testing.T) {
	n := ahBookMinLotsInWindow
	p10 := 1_600_000
	minAsk := 1_400_000
	nac := 300_000
	step := 100_000
	if !shouldRaiseEmptyFromAhBook(1_400_000, p10, minAsk, nac, n, step, 0, 0, false, false) {
		t.Fatal("held=0 and market 2+ steps above us — raise")
	}
	if shouldRaiseEmptyFromAhBook(1_800_000, p10, minAsk, nac, n, step, 0, 0, false, false) {
		t.Fatal("gap < 2 steps — no empty raise")
	}
	if shouldRaiseEmptyFromAhBook(1_400_000, p10, minAsk, nac, n-1, step, 0, 0, false, false) {
		t.Fatal("thin book — no empty raise")
	}
	if shouldRaiseEmptyFromAhBook(1_400_000, p10, minAsk, nac, n, step, 1, 0, false, false) {
		t.Fatal("with stock this path is disabled")
	}
	if shouldRaiseEmptyFromAhBook(1_400_000, p10, minAsk, nac, n, step, 0, 1, false, false) {
		t.Fatal("while buys already flowing, no empty raise")
	}
}

func TestEmptyBookRaiseTarget(t *testing.T) {
	if got := emptyBookRaiseTarget(1_400_000, 1_600_000, 1_400_000, 300_000, 100_000); got != 1_500_000 {
		t.Fatalf("step target got %d", got)
	}
	if got := emptyBookRaiseTarget(1_800_000, 1_600_000, 1_400_000, 300_000, 100_000); got != 1_900_000 {
		t.Fatalf("cap to +1 step got %d", got)
	}
}

func TestAhBookRaiseTarget(t *testing.T) {
	if got := ahBookRaiseTarget(1_000_000, 300_000, 100_000); got != 1_400_000 {
		t.Fatalf("got %d", got)
	}
	if ahBookRaiseTarget(0, 300_000, 100_000) != 0 {
		t.Fatal("пустой min")
	}
}

func TestAhBookRaiseTargetCapped(t *testing.T) {
	step := 100_000
	// полный таргет 1.4M, но с 400k только +2 step → 600k
	if got := ahBookRaiseTargetCapped(400_000, 1_000_000, 300_000, step); got != 600_000 {
		t.Fatalf("cap: got %d", got)
	}
	// уже близко — полный таргет
	if got := ahBookRaiseTargetCapped(1_300_000, 1_000_000, 300_000, step); got != 1_400_000 {
		t.Fatalf("near: got %d", got)
	}
}

func TestAhBookSoftDownTarget(t *testing.T) {
	if got := ahBookSoftDownTarget(2_000_000, 1_800_000, 300_000, 100_000); got != 2_400_000 {
		t.Fatalf("got %d", got)
	}
	// p10 занижен дампами — не ниже min+наценка
	if got := ahBookSoftDownTarget(400_000, 2_000_000, 300_000, 100_000); got != 2_300_000 {
		t.Fatalf("floor min+nac: got %d", got)
	}
	if ahBookSoftDownTarget(0, 2_000_000, 300_000, 100_000) != 0 {
		t.Fatal("пустой p10")
	}
	if ahBookSoftDownTarget(2_000_000, 0, 300_000, 100_000) != 0 {
		t.Fatal("пустой min")
	}
}

func TestShouldSoftDownFromAhBook(t *testing.T) {
	n := ahBookMinLotsInWindow
	p10 := 2_000_000
	minAsk := 1_800_000
	nac := 300_000
	step := 100_000
	held := 4
	// триггер: > p10+nac+2×step = 2.5M и > min+nac = 2.1M
	if !shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("селл явно выше рынка — снижаем")
	}
	if shouldSoftDownFromAhBook(2_500_000, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("на границе мёртвой зоны — не трогаем")
	}
	if shouldSoftDownFromAhBook(2_100_000, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("уже на min+наценка — не ниже")
	}
	if shouldSoftDownFromAhBook(2_000_000, p10, minAsk, nac, n, step, false, false, held) {
		t.Fatal("ниже/на min+наценка — не ↓")
	}
	if shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, n-1, step, false, false, held) {
		t.Fatal("мало лотов — рано")
	}
	if shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, n, step, true, false, held) {
		t.Fatal("уже ↑ из книги в этом цикле")
	}
	if shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, n, step, false, true, held) {
		t.Fatal("были покупки при held>0 — не снижаем")
	}
	// v8q: held=0 тоже только при полной книге (≥40), тонкая ночная больше не сливает
	if shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, ahBookMinLotsWhenEmpty, step, false, true, 0) {
		t.Fatal("пусто + тонкая книга — soft-↓ запрещён")
	}
	if !shouldSoftDownFromAhBook(2_600_000, p10, minAsk, nac, n, step, false, true, 0) {
		t.Fatal("пусто + полная книга + buys — soft-↓ можно (выше рынка)")
	}
}

func TestAhBookSoftDownApplyEmptyCap(t *testing.T) {
	step := 100_000
	floor := 400_000
	// пусто: 3.5M → цель 1.0M, но за цикл только −2 step
	if got := ahBookSoftDownApply(3_500_000, 1_000_000, floor, step, 0); got != 3_300_000 {
		t.Fatalf("empty cap: got %d", got)
	}
	// со стоком — полный таргет
	if got := ahBookSoftDownApply(3_500_000, 2_400_000, floor, step, 5); got != 2_400_000 {
		t.Fatalf("with stock: got %d", got)
	}
}

func TestAhBookMinOfLastN(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	if _, err := db.Exec(`CREATE TABLE ah_book_lots (
		uuid TEXT PRIMARY KEY, ts TEXT NOT NULL, go_type TEXT, item_id TEXT,
		price INTEGER, durability REAL, seller TEXT, enchants_json TEXT, anarchy INTEGER, seen_by TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ah_book_seller_bans (
		seller TEXT PRIMARY KEY, ts TEXT NOT NULL, item_id TEXT NOT NULL, n INTEGER NOT NULL, window_sec INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	mlDB = db
	item := "меч-1.21"
	base := time.Now().UTC()
	for i := 0; i < 49; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
			fmt.Sprintf("u-%d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			"sword", item, 900_000+i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := ahBookMinOfLastN(item, 50); ok {
		t.Fatal("49 строк — рано")
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"cheap", base.Add(50*time.Second).Format(time.RFC3339), "sword", item, 500_000)
	if err != nil {
		t.Fatal(err)
	}
	minP, ok := ahBookMinOfLastN(item, 50)
	if !ok || minP != 500_000 {
		t.Fatalf("min=%d ok=%v want 500000", minP, ok)
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price) VALUES (?,?,?,?,?)`,
		"newer-high", base.Add(120*time.Second).Format(time.RFC3339), "sword", item, 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// 50 newest: cheap still in window (49 old + cheap + newer = 51, drop oldest)
	minP, ok = ahBookMinOfLastN(item, 50)
	if !ok || minP != 500_000 {
		t.Fatalf("after high min=%d ok=%v", minP, ok)
	}
}

func TestAhBookWallBanSameSKU(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()

	item := "sword7-1.21"
	var rows []ahBookWire
	for i := 0; i < 2; i++ {
		rows = append(rows, ahBookWire{
			Uuid: fmt.Sprintf("w-%d", i), GoType: "netherite_sword-1.21", ItemID: item,
			Price: 2_200_000, Seller: "Laymix777",
		})
	}
	insertAhBookBatch(rows)
	var bans int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans`).Scan(&bans)
	if bans != 0 {
		t.Fatalf("2 лота — ещё не бан, got %d", bans)
	}
	insertAhBookBatch([]ahBookWire{{
		Uuid: "w-2", GoType: "netherite_sword-1.21", ItemID: item, Price: 2_200_000, Seller: "Laymix777",
	}})
	if err := db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans WHERE seller = 'laymix777'`).Scan(&bans); err != nil || bans != 1 {
		t.Fatalf("3-й лот того же SKU — бан, bans=%d err=%v", bans, err)
	}
	// другой SKU того же ника — не второй бан
	insertAhBookBatch([]ahBookWire{{
		Uuid: "helm-0", GoType: "netherite_armor-1.21", ItemID: "шлем-1.21", Price: 2_200_000, Seller: "Laymix777",
	}})
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_seller_bans`).Scan(&bans)
	if bans != 1 {
		t.Fatalf("бан навсегда один раз, got %d", bans)
	}
}

func TestAhBookMinSkipsBannedSeller(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()

	item := "шлем-1.21"
	base := time.Now().UTC()
	for i := 0; i < 50; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("ok-%d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			"armor", item, 2_000_000, "player")
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("wall-%d", i), base.Add(time.Duration(100+i)*time.Second).Format(time.RFC3339),
			"armor", item, 100_000, "WallBot")
		if err != nil {
			t.Fatal(err)
		}
	}
	backfillAhBookSellerBans()
	minP, ok := ahBookMinOfLastN(item, 50)
	if !ok || minP != 2_000_000 {
		t.Fatalf("витрина в min не входит: min=%d ok=%v", minP, ok)
	}
	var nLots int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ah_book_lots WHERE seller = 'WallBot'`).Scan(&nLots)
	if nLots != 6 {
		t.Fatalf("лоты витрины остаются в книге, got %d", nLots)
	}
}

func TestAhBookTimesHitWall(t *testing.T) {
	base := time.Now()
	var times []time.Time
	for i := 0; i < 3; i++ {
		times = append(times, base.Add(time.Duration(i)*time.Minute))
	}
	if !ahBookTimesHitWall(times) {
		t.Fatal("3 за 2 минуты — стена")
	}
	times[2] = base.Add(20 * time.Minute)
	if ahBookTimesHitWall(times) {
		t.Fatal("3-й через 20 мин — не стена")
	}
}

func TestAhBookUpsertRefreshesTsAndPrice(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "шлем-1.21"
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		"same", old, "netherite_armor-1.21", item, 2_200_000, "Laymix777")
	if err != nil {
		t.Fatal(err)
	}
	insertAhBookBatch([]ahBookWire{{
		Uuid: "same", GoType: "netherite_armor-1.21", ItemID: item, Price: 2_100_000, Seller: "Laymix777",
	}})
	var n, price int
	var ts string
	_ = db.QueryRow(`SELECT COUNT(*), price, ts FROM ah_book_lots WHERE uuid = 'same'`).Scan(&n, &price, &ts)
	if n != 1 {
		t.Fatalf("повтор не плодит строк, got %d", n)
	}
	if price != 2_100_000 {
		t.Fatalf("цена с АХ обновилась, got %d", price)
	}
	if ts <= old {
		t.Fatalf("ts должен стать свежим: old=%s new=%s", old, ts)
	}
}

func TestAhBookMinSince(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "шлем-1.21"
	now := time.Now().UTC()
	for i := 0; i < ahBookMinLotsInWindow; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("n-%d", i), now.Add(-time.Minute).Format(time.RFC3339), "armor", item, 1_000_000+i, "p")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		"old", now.Add(-time.Hour).Format(time.RFC3339), "armor", item, 100_000, "p")
	if err != nil {
		t.Fatal(err)
	}
	minP, n, ok := ahBookMinSince(item, now.Add(-ahBookRaiseWindow))
	if !ok || n != ahBookMinLotsInWindow || minP != 1_000_000 {
		t.Fatalf("min=%d n=%d ok=%v", minP, n, ok)
	}
}

func TestServerFunTimeRaiseAnomalousBookFloor(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "book.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })
	mlDB = db
	initAhBookTable()
	item := "шлем-1.21"
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	for i := 0; i < ahBookMinLotsInWindow; i++ {
		_, err := db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("wall-%d", i), ts, "armor", item, 2_200_000, "shop")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO ah_book_lots (uuid, ts, go_type, item_id, price, seller) VALUES (?,?,?,?,?,?)`,
		"dump", ts, "armor", item, 300_000, "dump")
	if err != nil {
		t.Fatal(err)
	}
	prevCfg := itemsConfig
	itemsConfig = map[string]ItemConfig{item: {Nacenka: 300_000}}
	t.Cleanup(func() { itemsConfig = prevCfg })
	prevHist := data.TradeHistory
	data.TradeHistory = map[string][]TradeLog{}
	t.Cleanup(func() { data.TradeHistory = prevHist })
	cycle := 10 * time.Minute
	wantN := ahBookMinLotsInWindow + 1
	p10, n, ok := ahBookP10Since(item, now.Add(-ahBookRaiseWindow))
	if !ok || n != wantN || p10 != 2_200_000 {
		t.Fatalf("p10=%d n=%d ok=%v want 2.2M n=%d (дамп не должен быть полом)", p10, n, ok, wantN)
	}
	cap := p10 + 300_000
	if !serverFunTimeRaiseAnomalous(1_600_000, 3_550_000, item, cycle, now) {
		t.Fatal("глюк выше p10+наценка")
	}
	if serverFunTimeRaiseAnomalous(1_600_000, 1_900_000, item, cycle, now) {
		t.Fatal("1.9 < p10+наценка — сырой min бы отсёк, p10 нет")
	}
	if !serverFunTimeRaiseAnomalous(1_600_000, cap+1, item, cycle, now) {
		t.Fatal("↑ над p10+наценка")
	}
	if serverFunTimeRaiseAnomalous(3_500_000, 2_000_000, item, cycle, now) {
		t.Fatal("это ↓ с глюка, не блокируем")
	}
	data.TradeHistory = map[string][]TradeLog{
		item: {
			{Time: now.Add(-5 * time.Minute), Type: "buy", Price: 1},
			{Time: now.Add(-12 * time.Minute), Type: "buy", Price: 1},
		},
	}
	if !serverFunTimeRaiseAnomalous(1_600_000, 1_900_000, item, cycle, now) {
		t.Fatal("закуп ≥ продаж — не поднимаем даже ниже p10")
	}
}
