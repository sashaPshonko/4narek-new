package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultMLDBPath = "ml_data/pricing.db"
	mlSchemaVersion = 6
	mlForwardCycles = 3
)

var (
	mlDB                *sql.DB
	mlDBMu              sync.Mutex
	mlDBPath            string
	mlPendingByCategory = make(map[string]*mlPendingDecision)
)

/*
schema v6 — один эксперимент по категории:

  lookback + price_context — server_min/max отдельно от rule adjust
  decision — только дельта adjustPrice; nacenka_context — наценка нашей логики
  forward  — 3 цикла: сток, онлайн, сделки, server clamps в окне, adjustments
  reward   — абсолютная прибыль (взвешенная сумма profit по окнам), не маржа

rule action / experiment_check — только SQLite action (отладка), не в JSON.
*/

type mlValueChange struct {
	Before int `json:"before"`
	After  int `json:"after"`
	Delta  int `json:"delta"`
}

type mlItemBounds struct {
	NacenkaMin    int  `json:"nacenka_min"`
	BuyMinHistory int  `json:"buy_min_history"`
	PriceFloor    int  `json:"price_floor"`
	AtNacenkaMin  bool `json:"at_nacenka_min"`
	AtPriceFloor  bool `json:"at_price_floor"`
}

type mlIntervention struct {
	Ts      string        `json:"ts"`
	Item    string        `json:"item"`
	Price   mlValueChange `json:"price"`
	Nacenka mlValueChange `json:"nacenka"`
	Bounds  mlItemBounds  `json:"bounds"`
}

type mlItemCycleSnapshot struct {
	StockAH  int              `json:"stock_ah"`
	StockInv int              `json:"stock_inv"`
	Trades   TradeWindowStats `json:"trades"`
}

type mlExternalPriceEvent struct {
	Ts          string `json:"ts"`
	Item        string `json:"item"`
	Kind        string `json:"kind"`
	PriceBefore int    `json:"price_before"`
	PriceAfter  int    `json:"price_after"`
	Delta       int    `json:"delta"`
	Note        string `json:"note,omitempty"`
}

type mlForwardCycle struct {
	Index             int                            `json:"cycle_index"`
	Start             string                         `json:"start"`
	End               string                         `json:"end"`
	PlayersOnline     int                            `json:"players_online"`
	PlayersMax        int                            `json:"players_max,omitempty"`
	TriggerItem       mlItemCycleSnapshot            `json:"trigger_item"`
	CategoryItems     map[string]mlItemCycleSnapshot `json:"category_items"`
	ServerPriceClamps []mlExternalPriceEvent         `json:"server_price_clamps,omitempty"`
	ProfitTrigger     int                            `json:"profit_trigger"`
	ProfitCategory    int                            `json:"profit_category"`
	DeltaTrigger      int                            `json:"delta_trigger_vs_before"`
	DeltaCategory     int                            `json:"delta_category_vs_before"`
	Adjustments       []mlIntervention               `json:"adjustments"`
}

type mlPendingDecision struct {
	CategoryType   string
	TriggerItem    string
	Action         string
	CycleDuration  time.Duration
	DecisionAt     time.Time
	BotsAtDecision int

	PriceBefore, PriceAfter     int
	NacenkaBefore, NacenkaAfter int
	ProfitBeforeTrigger       int
	ProfitBeforeCategory      int
	OnlineAtDecision          int
	OnlineMaxAtDecision       int
	LookbackJSON              string

	OutcomeCycles              int
	ForwardCycles              []mlForwardCycle
	CurrentWindowInterventions []mlIntervention
}

func initMLLog() {
	mlDBPath = os.Getenv("ML_DB_PATH")
	if mlDBPath == "" {
		mlDBPath = defaultMLDBPath
	}
	_ = os.MkdirAll(filepath.Dir(mlDBPath), 0755)

	db, err := sql.Open("sqlite", mlDBPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		log.Printf("[ML] open: %v", err)
		return
	}
	if err := db.Ping(); err != nil {
		log.Printf("[ML] ping: %v", err)
		_ = db.Close()
		return
	}
	db.SetMaxOpenConns(1) // один writer — меньше шанс порчи при гонках
	_, _ = db.Exec(`PRAGMA wal_autocheckpoint=1000`)

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS trade_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL, item_id TEXT NOT NULL, category_type TEXT NOT NULL,
		event_type TEXT NOT NULL, price INTEGER
	)`)
	// nacenka на сделку — для маржи sell−buy без join к decisions
	ensureMLColumn(db, "trade_events", "nacenka", "INTEGER")
	recoverTradeEventsIfBroken(db)

	_, _ = db.Exec(`
CREATE TABLE IF NOT EXISTS ml_decisions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	logged_ts TEXT NOT NULL,
	decision_ts TEXT NOT NULL,
	category_type TEXT NOT NULL,
	trigger_item TEXT NOT NULL,
	action TEXT NOT NULL,
	cycle_minutes REAL NOT NULL,
	reward_target INTEGER NOT NULL,
	delta_1 INTEGER NOT NULL,
	delta_2 INTEGER NOT NULL,
	delta_3 INTEGER NOT NULL,
	players_online INTEGER NOT NULL,
	bots_category INTEGER NOT NULL,
	payload_json TEXT NOT NULL
)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_ml_decisions_ts ON ml_decisions(logged_ts)`)

	mlDB = db
	initCapitalTables()
	initMLShadowTable()
	reloadCapitalPendingFromDB()
	log.Printf("[ML] SQLite %s (schema v%d + capital_cycles/fwd + stock_snapshots + server_price_events)", mlDBPath, mlSchemaVersion)
	if mlShadowEnabled() {
		log.Printf("[ML-SHADOW] включён → %s (Go правила + лог сравнения с ML)", mlWSURL())
	}
}

func ensureMLColumn(db *sql.DB, table, col, decl string) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return
		}
		if name == col {
			return
		}
	}
	_, _ = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + decl)
}

func categoryCycleSnapshotLocked(categoryType, triggerItem string, since, until time.Time) (mlItemCycleSnapshot, map[string]mlItemCycleSnapshot) {
	items := make(map[string]mlItemCycleSnapshot)
	var trigger mlItemCycleSnapshot
	for id, conf := range itemsConfig {
		if conf.Type != categoryType {
			continue
		}
		snap := mlItemCycleSnapshot{
			StockAH:  getItemCount(id),
			StockInv: getInventoryCount(id),
			Trades:   tradeStatsBetween(id, since, until),
		}
		items[id] = snap
		if id == triggerItem {
			trigger = snap
		}
	}
	return trigger, items
}

func mlValueChangeFrom(before, after int) mlValueChange {
	return mlValueChange{Before: before, After: after, Delta: after - before}
}

func itemBoundsAt(item string, cfg ItemConfig, sellPrice, nacenka int) mlItemBounds {
	buyMin := getMinPriceFromHistory(item)
	nacMin := resolveNacenkaMin(cfg)
	floor := buyMin + nacenka
	return mlItemBounds{
		NacenkaMin:    nacMin,
		BuyMinHistory: buyMin,
		PriceFloor:    floor,
		AtNacenkaMin:  nacenka <= nacMin,
		AtPriceFloor:  buyMin > 0 && sellPrice <= floor,
	}
}

func tradeInOpenInterval(t time.Time, since, until time.Time) bool {
	return t.After(since) && !t.After(until)
}

func profitBetween(item string, since, until time.Time) int {
	profit := 0
	for _, trade := range data.TradeHistory[item] {
		if !tradeInOpenInterval(trade.Time, since, until) {
			continue
		}
		switch trade.Type {
		case "buy":
			if trade.Price > 0 {
				profit -= trade.Price
			}
		case "sell":
			if trade.Price > 0 {
				profit += trade.Price
			}
		}
	}
	return profit
}

func categoryProfitBetween(categoryType string, since, until time.Time) int {
	total := 0
	for id, conf := range itemsConfig {
		if conf.Type == categoryType {
			total += profitBetween(id, since, until)
		}
	}
	return total
}

func tradeStatsBetween(item string, since, until time.Time) TradeWindowStats {
	var s TradeWindowStats
	for _, trade := range data.TradeHistory[item] {
		if !tradeInOpenInterval(trade.Time, since, until) {
			continue
		}
		switch trade.Type {
		case "buy":
			if trade.Price > 0 {
				s.Buys++
				s.Profit -= trade.Price
				s.BuyPrices = append(s.BuyPrices, trade.Price)
			}
		case "sell":
			if trade.Price > 0 {
				s.Sells++
				s.Profit += trade.Price
				s.SellPrices = append(s.SellPrices, trade.Price)
			}
		case "try-sell":
			s.TrySells++
		}
	}
	return s
}

type TradeWindowStats struct {
	Buys       int   `json:"buys"`
	Sells      int   `json:"sells"`
	TrySells   int   `json:"try_sells"`
	Profit     int   `json:"profit"`
	BuyPrices  []int `json:"buy_prices,omitempty"`
	SellPrices []int `json:"sell_prices,omitempty"`
}

type mlItemLookback struct {
	Price    int              `json:"price"`
	Nacenka  int              `json:"nacenka"`
	Bounds   mlItemBounds     `json:"bounds"`
	StockAH  int              `json:"stock_ah"`
	StockInv int              `json:"stock_inv"`
	Cycle1   TradeWindowStats `json:"cycle_1_before_decision"`
	Cycle2   TradeWindowStats `json:"cycle_2_before_decision"`
	Cycle3   TradeWindowStats `json:"cycle_3_before_decision"`
}

type mlNacenkaItemContext struct {
	Nacenka    int          `json:"nacenka"`
	NacenkaMin int          `json:"nacenka_min"`
	Bounds     mlItemBounds `json:"bounds"`
}

type mlTrainingRecord struct {
	SchemaVersion int    `json:"schema_version"`
	Training      struct {
		Policy string   `json:"policy"`
		Reward string   `json:"reward"`
		Notes  []string `json:"notes"`
	} `json:"training"`
	Meta     mlMeta `json:"meta"`
	Decision struct {
		Source      string        `json:"source"`
		TriggerItem string        `json:"trigger_item"`
		Price       mlValueChange `json:"price"`
		Nacenka     mlValueChange `json:"nacenka"`
		Bounds      mlItemBounds  `json:"bounds"`
	} `json:"decision"`
	PriceContext struct {
		Note                       string                 `json:"note"`
		ServerClampsBeforeDecision []mlExternalPriceEvent `json:"server_price_clamps_before_decision"`
		RuleSkippedServerClamp     bool                   `json:"rule_skipped_server_clamp_recently,omitempty"`
	} `json:"price_context"`
	NacenkaContext struct {
		Note    string                         `json:"note"`
		Trigger mlValueChange                  `json:"trigger_item"`
		Items   map[string]mlNacenkaItemContext `json:"category_items"`
	} `json:"nacenka_context"`
	Lookback struct {
		Note    string `json:"note"`
		Trigger struct {
			WindowEndingAtDecision TradeWindowStats `json:"window_ending_at_decision"`
		} `json:"trigger_item"`
		Category struct {
			Items map[string]mlItemLookback `json:"items"`
		} `json:"category"`
	} `json:"lookback"`
	Forward *struct {
		Note     string           `json:"note"`
		Timeline []mlForwardCycle `json:"timeline"`
		Reward   struct {
			Target              float64 `json:"target"`
			TotalTradesForward  int     `json:"total_trades_forward"`
			DeltaVsBaseline     float64 `json:"delta_vs_baseline_legacy"`
			Note                string  `json:"note"`
		} `json:"reward"`
	} `json:"forward,omitempty"`
}

type mlMeta struct {
	DecisionTs     string  `json:"decision_ts"`
	LoggedTs       string  `json:"logged_ts,omitempty"`
	CategoryType   string  `json:"category_type"`
	CycleMinutes   float64 `json:"cycle_minutes"`
	BotsCategory   int     `json:"bots_category"`
	PlayersOnline    int `json:"players_online,omitempty"`
	PlayersMax       int `json:"players_max,omitempty"`
	CausalityModel   string `json:"causality_model"`
}

func interventionFromAdjust(item string, priceBefore, priceAfter, nacenkaBefore, nacenkaAfter int, ts time.Time) mlIntervention {
	bounds := mlItemBounds{}
	if cfg, ok := itemsConfig[item]; ok {
		bounds = itemBoundsAt(item, cfg, priceAfter, nacenkaAfter)
	}
	return mlIntervention{
		Ts:      ts.UTC().Format(time.RFC3339),
		Item:    item,
		Price:   mlValueChangeFrom(priceBefore, priceAfter),
		Nacenka: mlValueChangeFrom(nacenkaBefore, nacenkaAfter),
		Bounds:  bounds,
	}
}

func buildLookbackJSONLocked(p *mlPendingDecision) string {
	cycle := p.CycleDuration
	decAt := p.DecisionAt
	items := make(map[string]mlItemLookback)
	for id, conf := range itemsConfig {
		if conf.Type != p.CategoryType {
			continue
		}
		price := data.Prices[id]
		nacenka := getNacenka(id)
		items[id] = mlItemLookback{
			Price:    price,
			Nacenka:  nacenka,
			Bounds:   itemBoundsAt(id, conf, price, nacenka),
			StockAH:  getItemCount(id),
			StockInv: getInventoryCount(id),
			Cycle1:   tradeStatsBetween(id, decAt.Add(-cycle), decAt),
			Cycle2:   tradeStatsBetween(id, decAt.Add(-2*cycle), decAt),
			Cycle3:   tradeStatsBetween(id, decAt.Add(-3*cycle), decAt),
		}
	}

	var rec mlTrainingRecord
	rec.SchemaVersion = mlSchemaVersion
	rec.Meta = mlMeta{
		DecisionTs:     decAt.UTC().Format(time.RFC3339),
		CategoryType:   p.CategoryType,
		CycleMinutes:   cycle.Minutes(),
		BotsCategory:   p.BotsAtDecision,
		PlayersOnline:  p.OnlineAtDecision,
		PlayersMax:     p.OnlineMaxAtDecision,
		CausalityModel: "decision=старт; forward.timeline = сток+сделки+онлайн за каждый цикл",
	}
	rec.Decision.TriggerItem = p.TriggerItem
	rec.Decision.Price = mlValueChangeFrom(p.PriceBefore, p.PriceAfter)
	rec.Decision.Nacenka = mlValueChangeFrom(p.NacenkaBefore, p.NacenkaAfter)
	if cfg, ok := itemsConfig[p.TriggerItem]; ok {
		rec.Decision.Bounds = itemBoundsAt(p.TriggerItem, cfg, p.PriceAfter, p.NacenkaAfter)
	}
	rec.Lookback.Note = "Контекст до decision_ts. Не цель."
	rec.Lookback.Trigger.WindowEndingAtDecision = tradeStatsBetween(p.TriggerItem, decAt.Add(-cycle), decAt)
	rec.Lookback.Category.Items = items
	enrichMLTrainingContextLocked(&rec, p)

	raw, _ := json.Marshal(rec)
	return string(raw)
}

func computeRewardDeltaLegacy(d1, d2, d3 int) int {
	return int(math.Round(float64(d1) + 0.8*float64(d2) + 0.6*float64(d3)))
}

var forwardProfitWeights = []float64{1.0, 0.8, 0.6}

// Абсолютная прибыль в монетах по forward-окнам (не маржа, не %).
func computeForwardProfitReward(cycles []mlForwardCycle) (profitWeighted int, totalTrades int) {
	sum := 0.0
	trades := 0
	for i, c := range cycles {
		if i < len(forwardProfitWeights) {
			sum += forwardProfitWeights[i] * float64(c.ProfitTrigger)
		}
		t := c.TriggerItem.Trades
		trades += t.Buys + t.Sells + t.TrySells
	}
	return int(math.Round(sum)), trades
}

func buildNacenkaCategoryContextLocked(categoryType string) map[string]mlNacenkaItemContext {
	out := make(map[string]mlNacenkaItemContext)
	for id, conf := range itemsConfig {
		if conf.Type != categoryType {
			continue
		}
		n := getNacenka(id)
		out[id] = mlNacenkaItemContext{
			Nacenka:    n,
			NacenkaMin: resolveNacenkaMin(conf),
			Bounds:     itemBoundsAt(id, conf, data.Prices[id], n),
		}
	}
	return out
}

func enrichMLTrainingContextLocked(rec *mlTrainingRecord, p *mlPendingDecision) {
	cycle := p.CycleDuration
	decAt := p.DecisionAt
	lookbackStart := decAt.Add(-3 * cycle)

	rec.Training.Policy = "Предсказать price/nacenka после adjustPrice; Y = абсолютная прибыль + объём сделок; не копировать rule action"
	rec.Training.Reward = "forward.reward.target"
	rec.Training.Notes = []string{
		"Маржа/наценка как % рынка не цель; важны монеты profit и число сделок.",
		"server_min/server_max — сервер и боты, резкий сдвиг цены не whimsy adjustPrice.",
		"decision.price/nacenka delta — только шаг pricing rule в этом цикле.",
		"Наценка меняется нашей логикой (пол, шаг); сервер min/max наценку не трогает.",
		"Имена экспериментов и rule action в JSON не передаются.",
	}

	rec.Decision.Source = "pricing_rule_adjust"
	rec.PriceContext.Note = "Резкие скачки цены смотри server_price_clamps; decision.price — только наш цикл."
	rec.PriceContext.ServerClampsBeforeDecision = externalEventsToML(p.CategoryType, lookbackStart, decAt)
	if t, ok := data.LastManualUpdate[p.TriggerItem]; ok && decAt.Sub(t) < cycle {
		rec.PriceContext.RuleSkippedServerClamp = true
	}

	rec.NacenkaContext.Note = "Наценка — параметр нашей pricing-логики (минимум, шаг). Меняется в adjustPrice, не через server min/max."
	rec.NacenkaContext.Trigger = mlValueChangeFrom(p.NacenkaBefore, p.NacenkaAfter)
	rec.NacenkaContext.Items = buildNacenkaCategoryContextLocked(p.CategoryType)
}

func buildFinalPayloadJSON(p *mlPendingDecision, loggedAt time.Time) string {
	var rec mlTrainingRecord
	if err := json.Unmarshal([]byte(p.LookbackJSON), &rec); err != nil {
		rec = mlTrainingRecord{SchemaVersion: mlSchemaVersion}
	}

	rec.Meta.LoggedTs = loggedAt.UTC().Format(time.RFC3339)
	rec.Meta.PlayersOnline = p.OnlineAtDecision
	rec.Meta.PlayersMax = p.OnlineMaxAtDecision
	rec.Forward = &struct {
		Note     string           `json:"note"`
		Timeline []mlForwardCycle `json:"timeline"`
		Reward   struct {
			Target             float64 `json:"target"`
			TotalTradesForward int     `json:"total_trades_forward"`
			DeltaVsBaseline    float64 `json:"delta_vs_baseline_legacy"`
			Note               string  `json:"note"`
		} `json:"reward"`
	}{
		Note:     "Каждый цикл: profit в монетах, сделки, сток, онлайн, server_price_clamps в окне.",
		Timeline: p.ForwardCycles,
	}

	var d1, d2, d3 int
	if len(p.ForwardCycles) > 0 {
		d1 = p.ForwardCycles[0].DeltaTrigger
	}
	if len(p.ForwardCycles) > 1 {
		d2 = p.ForwardCycles[1].DeltaTrigger
	}
	if len(p.ForwardCycles) > 2 {
		d3 = p.ForwardCycles[2].DeltaTrigger
	}
	profitReward, totalTrades := computeForwardProfitReward(p.ForwardCycles)
	rec.Forward.Reward.Target = float64(profitReward)
	rec.Forward.Reward.TotalTradesForward = totalTrades
	rec.Forward.Reward.DeltaVsBaseline = float64(computeRewardDeltaLegacy(d1, d2, d3))
	rec.Forward.Reward.Note = "target = взвешенная сумма profit trigger по 3 циклам (1.0,0.8,0.6). Маржа не используется."

	raw, err := json.Marshal(rec)
	if err != nil {
		return p.LookbackJSON
	}
	return string(raw)
}

func closeForwardWindowLocked(p *mlPendingDecision) {
	cycle := p.CycleDuration
	idx := p.OutcomeCycles
	since := p.DecisionAt.Add(time.Duration(idx) * cycle)
	until := p.DecisionAt.Add(time.Duration(idx+1) * cycle)

	triggerSnap, categorySnaps := categoryCycleSnapshotLocked(p.CategoryType, p.TriggerItem, since, until)
	profitTr := triggerSnap.Trades.Profit
	profitCat := categoryProfitBetween(p.CategoryType, since, until)

	adj := make([]mlIntervention, len(p.CurrentWindowInterventions))
	copy(adj, p.CurrentWindowInterventions)

	online, onlineMax := fetchOnlineSnapshot()
	p.ForwardCycles = append(p.ForwardCycles, mlForwardCycle{
		Index:             idx + 1,
		Start:             since.UTC().Format(time.RFC3339),
		End:               until.UTC().Format(time.RFC3339),
		PlayersOnline:     online,
		PlayersMax:        onlineMax,
		TriggerItem:       triggerSnap,
		CategoryItems:     categorySnaps,
		ServerPriceClamps: externalEventsToML(p.CategoryType, since, until),
		ProfitTrigger:     profitTr,
		ProfitCategory:    profitCat,
		DeltaTrigger:      profitTr - p.ProfitBeforeTrigger,
		DeltaCategory:     profitCat - p.ProfitBeforeCategory,
		Adjustments:       adj,
	})
	p.CurrentWindowInterventions = nil
	p.OutcomeCycles++
}

// tryAdvanceCategoryMLOutcomesLocked — в начале adjustPrice.
func tryAdvanceCategoryMLOutcomesLocked(categoryType string, now time.Time) {
	p, ok := mlPendingByCategory[categoryType]
	if !ok {
		return
	}
	cycle := p.CycleDuration
	if cycle <= 0 {
		cycle = 10 * time.Minute
	}

	for p.OutcomeCycles < mlForwardCycles {
		windowEnd := p.DecisionAt.Add(time.Duration(p.OutcomeCycles+1) * cycle)
		if now.Before(windowEnd) {
			break
		}
		closeForwardWindowLocked(p)
	}

	if p.OutcomeCycles >= mlForwardCycles {
		flushMLDecisionLocked(p, now)
		delete(mlPendingByCategory, categoryType)
	}
}

func flushMLDecisionLocked(p *mlPendingDecision, now time.Time) {
	if mlDB == nil {
		return
	}
	payload := buildFinalPayloadJSON(p, now)

	var d1, d2, d3 int
	if len(p.ForwardCycles) > 0 {
		d1 = p.ForwardCycles[0].DeltaTrigger
	}
	if len(p.ForwardCycles) > 1 {
		d2 = p.ForwardCycles[1].DeltaTrigger
	}
	if len(p.ForwardCycles) > 2 {
		d3 = p.ForwardCycles[2].DeltaTrigger
	}
	reward, _ := computeForwardProfitReward(p.ForwardCycles)

	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
INSERT INTO ml_decisions (
	logged_ts, decision_ts, category_type, trigger_item, action, cycle_minutes,
	reward_target, delta_1, delta_2, delta_3, players_online, bots_category, payload_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		now.UTC().Format(time.RFC3339), p.DecisionAt.UTC().Format(time.RFC3339),
		p.CategoryType, p.TriggerItem, p.Action, p.CycleDuration.Minutes(),
		reward, d1, d2, d3, p.OnlineAtDecision, p.BotsAtDecision, payload,
	)
	if err != nil {
		log.Printf("[ML] insert: %v", err)
	}
}

// recordMLInterventionLocked — adjustPrice в категории, пока собираем forward.
func recordMLInterventionLocked(
	p *mlPendingDecision,
	item string,
	priceBefore, priceAfter, nacenkaBefore, nacenkaAfter int,
	ts time.Time,
) {
	p.CurrentWindowInterventions = append(p.CurrentWindowInterventions,
		interventionFromAdjust(item, priceBefore, priceAfter, nacenkaBefore, nacenkaAfter, ts))
}

// queueMLDecisionLocked — конец adjustPrice.
func queueMLDecisionLocked(
	item string,
	cfg ItemConfig,
	action string,
	priceBefore, priceAfter, nacenkaBefore, nacenkaAfter int,
	now time.Time,
) {
	if pending, busy := mlPendingByCategory[cfg.Type]; busy {
		recordMLInterventionLocked(pending, item, priceBefore, priceAfter, nacenkaBefore, nacenkaAfter, now)
		return
	}

	cycle := cfg.AnalysisTime
	decAt := now
	beforeStart := decAt.Add(-cycle)

	online, onlineMax := fetchOnlineSnapshot()
	p := &mlPendingDecision{
		CategoryType:         cfg.Type,
		TriggerItem:          item,
		Action:               action,
		CycleDuration:        cycle,
		DecisionAt:           decAt,
		BotsAtDecision:       aggregateBotsPerTypeLocked()[cfg.Type],
		OnlineAtDecision:     online,
		OnlineMaxAtDecision:  onlineMax,
		PriceBefore:          priceBefore,
		PriceAfter:           priceAfter,
		NacenkaBefore:        nacenkaBefore,
		NacenkaAfter:         nacenkaAfter,
		ProfitBeforeTrigger:  profitBetween(item, beforeStart, decAt),
		ProfitBeforeCategory: categoryProfitBetween(cfg.Type, beforeStart, decAt),
	}
	p.LookbackJSON = buildLookbackJSONLocked(p)
	mlPendingByCategory[cfg.Type] = p
}

func logTradeEventML(item, eventType string, price int) {
	if mlDB == nil {
		return
	}
	cfg, ok := itemsConfig[item]
	category := ""
	if ok {
		category = cfg.Type
	}
	nac := 0
	if eventType == "sell" || eventType == "buy" {
		nac = getNacenka(item)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(
		`INSERT INTO trade_events (ts, item_id, category_type, event_type, price, nacenka) VALUES (?, ?, ?, ?, ?, ?)`,
		ts, item, category, eventType, price, nac,
	)
	if err != nil {
		log.Printf("[ML] trade_events insert %s %s: %v", item, eventType, err)
	}
}

// recoverTradeEventsIfBroken — если SQLite ругается, спасаем читаемые строки trade_events.
func recoverTradeEventsIfBroken(db *sql.DB) {
	rows, err := db.Query(`PRAGMA quick_check`)
	if err != nil {
		log.Printf("[ML] quick_check: %v — пробуем спасти trade_events", err)
	} else {
		ok := true
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil || s != "ok" {
				ok = false
				if s != "" && s != "ok" {
					log.Printf("[ML] quick_check: %s", s)
				}
			}
		}
		rows.Close()
		if ok {
			return
		}
	}

	bak := "trade_events_corrupt_" + time.Now().UTC().Format("20060102_150405")
	_, err = db.Exec(`ALTER TABLE trade_events RENAME TO "` + bak + `"`)
	if err != nil {
		log.Printf("[ML] rename trade_events: %v (создаём пустую)", err)
		_, _ = db.Exec(`DROP TABLE IF EXISTS trade_events`)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS trade_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL, item_id TEXT NOT NULL, category_type TEXT NOT NULL,
		event_type TEXT NOT NULL, price INTEGER, nacenka INTEGER
	)`)
	if err != nil {
		log.Printf("[ML] recreate trade_events: %v", err)
		return
	}
	ensureMLColumn(db, "trade_events", "nacenka", "INTEGER")

	srcRows, err := db.Query(`SELECT ts, item_id, category_type, event_type, price, COALESCE(nacenka,0) FROM "` + bak + `"`)
	if err != nil {
		log.Printf("[ML] read %s: %v — trade_events пустой", bak, err)
		return
	}
	defer srcRows.Close()
	copied := 0
	for srcRows.Next() {
		var ts, item, cat, et string
		var price, nac int
		if err := srcRows.Scan(&ts, &item, &cat, &et, &price, &nac); err != nil {
			log.Printf("[ML] trade_events recover stop after %d rows: %v", copied, err)
			break
		}
		if _, err := db.Exec(
			`INSERT INTO trade_events (ts, item_id, category_type, event_type, price, nacenka) VALUES (?,?,?,?,?,?)`,
			ts, item, cat, et, price, nac,
		); err != nil {
			log.Printf("[ML] trade_events recover insert stop after %d: %v", copied, err)
			break
		}
		copied++
	}
	log.Printf("[ML] trade_events: спасено %d строк из %s", copied, bak)
}
