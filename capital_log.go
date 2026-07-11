package main

import (
	"log"
	"time"
)

// capital_v2 — 2026-07-11 night: жёстче fill_price, dump CD bypass при сильном давлении.
const capitalPolicy = "capital_v2"
const capitalForwardCycles = 3

// capitalPendingForward — ждём 3 следующих окна analysis_time и дописываем profit в capital_cycles.
type capitalPendingForward struct {
	ID            int64
	Item          string
	CategoryType  string
	DecisionAt    time.Time
	CycleDuration time.Duration
	WindowStart   time.Time // начало текущего незакрытого forward-окна
	OutcomeCycles int
}

var capitalPending []capitalPendingForward

func initCapitalTables() {
	if mlDB == nil {
		return
	}
	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS capital_cycles (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	policy TEXT NOT NULL,
	item_id TEXT NOT NULL,
	category_type TEXT NOT NULL,
	action TEXT NOT NULL,
	winner TEXT NOT NULL,
	dump REAL NOT NULL,
	fill REAL NOT NULL,
	skim REAL NOT NULL,
	threshold REAL NOT NULL,
	sales INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	try_sells INTEGER NOT NULL,
	on_ah INTEGER NOT NULL,
	inv INTEGER NOT NULL,
	held INTEGER NOT NULL,
	share INTEGER NOT NULL,
	free_slots INTEGER NOT NULL,
	need INTEGER NOT NULL,
	normal_sales INTEGER NOT NULL,
	normal_count INTEGER NOT NULL,
	try_ratio REAL NOT NULL,
	stock_load REAL NOT NULL,
	underbuy INTEGER NOT NULL,
	price_before INTEGER NOT NULL,
	price_after INTEGER NOT NULL,
	nacenka_before INTEGER NOT NULL,
	nacenka_after INTEGER NOT NULL,
	nacenka_sum_now INTEGER NOT NULL,
	nacenka_sum_prev INTEGER NOT NULL,
	price_floor INTEGER NOT NULL,
	step INTEGER NOT NULL,
	cooldown INTEGER NOT NULL,
	players_online INTEGER NOT NULL,
	notes TEXT
)`)
	extra := []struct{ col, decl string }{
		{"profit_now", "INTEGER"},
		{"cheap_frac", "REAL"},
		{"cheap_n", "INTEGER"},
		{"min_buy_history", "INTEGER"},
		{"bots_category", "INTEGER"},
		{"cycle_minutes", "REAL"},
		{"good_streak", "INTEGER"},
		{"dump_blocked_cd", "INTEGER"},
		{"fwd_done", "INTEGER"},
		{"fwd_profit_1", "INTEGER"},
		{"fwd_profit_2", "INTEGER"},
		{"fwd_profit_3", "INTEGER"},
		{"fwd_sells_1", "INTEGER"},
		{"fwd_sells_2", "INTEGER"},
		{"fwd_sells_3", "INTEGER"},
		{"fwd_buys_1", "INTEGER"},
		{"fwd_buys_2", "INTEGER"},
		{"fwd_buys_3", "INTEGER"},
		{"fwd_try_1", "INTEGER"},
		{"fwd_try_2", "INTEGER"},
		{"fwd_try_3", "INTEGER"},
		{"fwd_held_1", "INTEGER"},
		{"fwd_held_2", "INTEGER"},
		{"fwd_held_3", "INTEGER"},
		{"fwd_reward", "INTEGER"},
	}
	for _, c := range extra {
		ensureMLColumn(mlDB, "capital_cycles", c.col, c.decl)
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_capital_cycles_ts ON capital_cycles(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_capital_cycles_item ON capital_cycles(item_id)`)

	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS stock_snapshots (
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
	source TEXT NOT NULL
)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_stock_snapshots_ts ON stock_snapshots(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_stock_snapshots_item ON stock_snapshots(item_id)`)

	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS server_price_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	category_type TEXT NOT NULL,
	kind TEXT NOT NULL,
	price_before INTEGER NOT NULL,
	price_after INTEGER NOT NULL,
	delta INTEGER NOT NULL,
	nacenka INTEGER NOT NULL
)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_server_price_events_ts ON server_price_events(ts)`)
}

// CapitalCycleRow — снимок одного цикла CAPITAL для SQLite.
type CapitalCycleRow struct {
	Policy                                string
	Item, Category, Action, Winner, Notes string
	Dump, Fill, Skim, Threshold           float64
	Sales, Buys, TrySells                 int
	OnAH, Inv, Held                       int
	Share, Free, Need                     int
	NormalSales, NormalCount              int
	TryRatio, StockLoad                   float64
	Underbuy                              bool
	PriceBefore, PriceAfter               int
	NacenkaBefore, NacenkaAfter           int
	NacenkaSumNow, NacenkaSumPrev         int
	PriceFloor, Step, Cooldown            int
	PlayersOnline                         int
	ProfitNow                             int
	CheapFrac                             float64
	CheapN                                int
	MinBuyHistory                         int
	BotsCategory                          int
	CycleMinutes                          float64
	GoodStreak                            int
	DumpBlockedCD                         bool
	DecisionAt                            time.Time
	CycleDuration                         time.Duration
}

func logCapitalCycleLocked(row CapitalCycleRow) {
	if mlDB == nil {
		return
	}
	if row.Policy == "" {
		row.Policy = capitalPolicy
	}
	under, blocked := 0, 0
	if row.Underbuy {
		under = 1
	}
	if row.DumpBlockedCD {
		blocked = 1
	}
	ts := row.DecisionAt
	if ts.IsZero() {
		ts = time.Now()
	}
	tsStr := ts.UTC().Format(time.RFC3339)

	mlDBMu.Lock()
	res, err := mlDB.Exec(`
INSERT INTO capital_cycles (
	ts, policy, item_id, category_type, action, winner,
	dump, fill, skim, threshold,
	sales, buys, try_sells, on_ah, inv, held,
	share, free_slots, need, normal_sales, normal_count,
	try_ratio, stock_load, underbuy,
	price_before, price_after, nacenka_before, nacenka_after,
	nacenka_sum_now, nacenka_sum_prev, price_floor, step, cooldown,
	players_online, notes,
	profit_now, cheap_frac, cheap_n, min_buy_history, bots_category,
	cycle_minutes, good_streak, dump_blocked_cd, fwd_done
) VALUES (?,?,?,?,?,?, ?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?,?, ?,?, ?,?,?,?,?, ?,?,?, 0)`,
		tsStr, row.Policy, row.Item, row.Category, row.Action, row.Winner,
		row.Dump, row.Fill, row.Skim, row.Threshold,
		row.Sales, row.Buys, row.TrySells, row.OnAH, row.Inv, row.Held,
		row.Share, row.Free, row.Need, row.NormalSales, row.NormalCount,
		row.TryRatio, row.StockLoad, under,
		row.PriceBefore, row.PriceAfter, row.NacenkaBefore, row.NacenkaAfter,
		row.NacenkaSumNow, row.NacenkaSumPrev, row.PriceFloor, row.Step, row.Cooldown,
		row.PlayersOnline, row.Notes,
		row.ProfitNow, row.CheapFrac, row.CheapN, row.MinBuyHistory, row.BotsCategory,
		row.CycleMinutes, row.GoodStreak, blocked,
	)
	mlDBMu.Unlock()
	if err != nil {
		log.Printf("[ML] capital_cycles insert: %v", err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil || id <= 0 {
		return
	}
	cycle := row.CycleDuration
	if cycle <= 0 {
		if cfg, ok := itemsConfig[row.Item]; ok {
			cycle = cfg.AnalysisTime
		} else {
			cycle = 10 * time.Minute
		}
	}
	capitalPending = append(capitalPending, capitalPendingForward{
		ID:            id,
		Item:          row.Item,
		CategoryType:  row.Category,
		DecisionAt:    ts,
		CycleDuration: cycle,
		WindowStart:   ts,
		OutcomeCycles: 0,
	})

	logStockSnapshotCategoryLocked(row.Category, row.Item, "capital_cycle")
}

func logStockSnapshotCategoryLocked(categoryType, triggerItem, source string) {
	if mlDB == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	type row struct {
		item, cat     string
		ah, inv, held int
		price, nac    int
	}
	var rows []row
	for id, conf := range itemsConfig {
		if conf.Type != categoryType {
			continue
		}
		ah := getItemCount(id)
		inv := getInventoryCount(id)
		rows = append(rows, row{
			item: id, cat: conf.Type,
			ah: ah, inv: inv, held: ah + inv,
			price: data.Prices[id], nac: getNacenka(id),
		})
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	for _, r := range rows {
		_, _ = mlDB.Exec(`
INSERT INTO stock_snapshots (ts, item_id, category_type, on_ah, inv, held, price, nacenka, trigger_item, source)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
			ts, r.item, r.cat, r.ah, r.inv, r.held, r.price, r.nac, triggerItem, source,
		)
	}
}

func logServerPriceEventLocked(item, kind string, priceBefore, priceAfter int) {
	if mlDB == nil {
		return
	}
	cat := ""
	if cfg, ok := itemsConfig[item]; ok {
		cat = cfg.Type
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	nac := getNacenka(item)
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
INSERT INTO server_price_events (ts, item_id, category_type, kind, price_before, price_after, delta, nacenka)
VALUES (?,?,?,?,?,?,?,?)`,
		ts, item, cat, kind, priceBefore, priceAfter, priceAfter-priceBefore, nac,
	)
	if err != nil {
		log.Printf("[ML] server_price_events insert: %v", err)
	}
}

// tryAdvanceCapitalForwardsLocked — дописывает fwd_* в capital_cycles после 1..3 окон.
// Вызывать под mutex.Lock в начале adjustPrice (и можно с тикера).
func tryAdvanceCapitalForwardsLocked(now time.Time) {
	if mlDB == nil || len(capitalPending) == 0 {
		return
	}
	remain := capitalPending[:0]
	for _, p := range capitalPending {
		for p.OutcomeCycles < capitalForwardCycles {
			windowEnd := p.WindowStart.Add(p.CycleDuration)
			if now.Before(windowEnd) {
				break
			}
			st := tradeStatsBetween(p.Item, p.WindowStart, windowEnd)
			held := getItemCount(p.Item) + getInventoryCount(p.Item)
			n := p.OutcomeCycles + 1
			var query string
			switch n {
			case 1:
				query = `UPDATE capital_cycles SET fwd_profit_1=?, fwd_sells_1=?, fwd_buys_1=?, fwd_try_1=?, fwd_held_1=? WHERE id=?`
			case 2:
				query = `UPDATE capital_cycles SET fwd_profit_2=?, fwd_sells_2=?, fwd_buys_2=?, fwd_try_2=?, fwd_held_2=? WHERE id=?`
			default:
				query = `UPDATE capital_cycles SET fwd_profit_3=?, fwd_sells_3=?, fwd_buys_3=?, fwd_try_3=?, fwd_held_3=? WHERE id=?`
			}
			mlDBMu.Lock()
			_, err := mlDB.Exec(query, st.Profit, st.Sells, st.Buys, st.TrySells, held, p.ID)
			mlDBMu.Unlock()
			if err != nil {
				log.Printf("[ML] capital fwd update id=%d: %v", p.ID, err)
			}
			p.OutcomeCycles = n
			p.WindowStart = windowEnd
		}
		if p.OutcomeCycles >= capitalForwardCycles {
			finalizeCapitalForwardLocked(p.ID)
			continue
		}
		remain = append(remain, p)
	}
	capitalPending = remain
}

func finalizeCapitalForwardLocked(id int64) {
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	var p1, p2, p3 int
	err := mlDB.QueryRow(`SELECT COALESCE(fwd_profit_1,0), COALESCE(fwd_profit_2,0), COALESCE(fwd_profit_3,0) FROM capital_cycles WHERE id=?`, id).
		Scan(&p1, &p2, &p3)
	if err != nil {
		return
	}
	reward := int(float64(p1) + 0.8*float64(p2) + 0.6*float64(p3))
	_, _ = mlDB.Exec(`UPDATE capital_cycles SET fwd_done=1, fwd_reward=? WHERE id=?`, reward, id)
}
