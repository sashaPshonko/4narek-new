package main

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type salesTradeView struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Price   int       `json:"price"`
	Nacenka int       `json:"nacenka,omitempty"`
}

type salesWindowView struct {
	Buys     int `json:"buys"`
	Sells    int `json:"sells"`
	TrySells int `json:"try_sells"`
	BuySum   int `json:"buy_sum"`
	SellSum  int `json:"sell_sum"`
	Profit   int `json:"profit"`
	AvgBuy   int `json:"avg_buy,omitempty"`
	AvgSell  int `json:"avg_sell,omitempty"`
}

type salesItemView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Price      int     `json:"price"`
	Nacenka    int     `json:"nacenka"`
	NacenkaPct float64 `json:"nacenka_pct"`
	ImpliedBuy int     `json:"implied_buy"`

	Buys     int `json:"buys"`
	Sells    int `json:"sells"`
	TrySells int `json:"try_sells"`
	BuySum   int `json:"buy_sum"`
	SellSum  int `json:"sell_sum"`
	Profit   int `json:"profit"`
	AvgBuy   int `json:"avg_buy,omitempty"`
	AvgSell  int `json:"avg_sell,omitempty"`

	RealizedSpread int     `json:"realized_spread,omitempty"`
	MarginPct      float64 `json:"margin_pct"`

	// Фактическая наценка: сколько реально заработали на купленном экземпляре
	// с поправкой на его прочность (битый предмет и продаётся дешевле).
	FactSamples  int     `json:"fact_samples"`
	FactMarkup   int     `json:"fact_markup"`
	FactMarkupPc float64 `json:"fact_markup_pct"`
	PlanMarkupPc float64 `json:"plan_markup_pct"`
	AvgDurab     float64 `json:"avg_durability"`

	OnAH int `json:"on_ah"`
	Inv  int `json:"inv"`
	Held int `json:"held"`

	LastCycleProfit int     `json:"last_cycle_profit"`
	LastCycleSales  int     `json:"last_cycle_sales"`
	CycleMinutes    float64 `json:"cycle_minutes"`

	Window1h    salesWindowView `json:"window_1h"`
	WindowCycle salesWindowView `json:"window_cycle"`
	LastTradeAt *time.Time      `json:"last_trade_at,omitempty"`
}

type salesCycleView struct {
	ID            int64   `json:"id"`
	TS            string  `json:"ts"`
	Action        string  `json:"action"`
	Sales         int     `json:"sales"`
	Buys          int     `json:"buys"`
	TrySells      int     `json:"try_sells"`
	OnAH          int     `json:"on_ah"`
	Inv           int     `json:"inv"`
	Held          int     `json:"held"`
	PriceBefore   int     `json:"price_before"`
	PriceAfter    int     `json:"price_after"`
	NacenkaBefore int     `json:"nacenka_before"`
	NacenkaAfter  int     `json:"nacenka_after"`
	ProfitNow     *int    `json:"profit_now,omitempty"`
	FwdReward     *int    `json:"fwd_reward,omitempty"`
	FwdProfit1    *int    `json:"fwd_profit_1,omitempty"`
	FwdProfit2    *int    `json:"fwd_profit_2,omitempty"`
	FwdProfit3    *int    `json:"fwd_profit_3,omitempty"`
	CycleMinutes  float64 `json:"cycle_minutes"`
}

type salesOverview struct {
	OK           bool            `json:"ok"`
	Date         string          `json:"date"`
	Period       string          `json:"period"`
	PeriodLabel  string          `json:"period_label"`
	Since        *time.Time      `json:"since,omitempty"`
	Source       string          `json:"source"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Totals       salesWindowView `json:"totals"`
	FactMarkup   int             `json:"fact_markup"`
	PlanMarkup   int             `json:"plan_markup"`
	FactMarkupPc float64         `json:"fact_markup_pct"`
	PlanMarkupPc float64         `json:"plan_markup_pct"`
	StockAH      int             `json:"stock_ah"`
	StockInv     int             `json:"stock_inv"`
	ItemsLive    int             `json:"items_live"`
	TopProfit    string          `json:"top_profit_id,omitempty"`
	TopVolume    string          `json:"top_volume_id,omitempty"`
	Items        []salesItemView `json:"items"`
}

func registerSalesHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/sales", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sales/", http.StatusFound)
	}))

	staticRoot, err := fs.Sub(salesStaticFS, "sales_static")
	if err != nil {
		log.Printf("[sales] embed static: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.Handle("/sales/", recoverHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sales/api/") {
			salesAPI(w, r)
			return
		}
		r2 := *r
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, "/sales")
		if u.Path == "" {
			u.Path = "/"
		}
		r2.URL = &u
		fileServer.ServeHTTP(w, &r2)
	})))
	log.Printf("[sales] dashboard ready at /sales/")
}

func salesAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/sales/api")
	if path == "" {
		path = "/"
	}
	period := r.URL.Query().Get("period")

	switch {
	case path == "/overview" && r.Method == http.MethodGet:
		salesJSON(w, http.StatusOK, buildSalesOverview(period))
		return

	case strings.HasPrefix(path, "/item/") && r.Method == http.MethodGet:
		id := strings.Trim(strings.TrimPrefix(path, "/item/"), "/")
		if id == "" {
			salesJSONErr(w, http.StatusBadRequest, "missing item id")
			return
		}
		item, ok := buildSalesItemDetail(id, period)
		if !ok {
			salesJSONErr(w, http.StatusNotFound, "unknown item")
			return
		}
		salesJSON(w, http.StatusOK, item)
		return

	case path == "/markups" && r.Method == http.MethodGet:
		salesJSON(w, http.StatusOK, buildSalesMarkups(r.URL.Query().Get("item"), period))
		return

	default:
		salesJSONErr(w, http.StatusNotFound, "not found")
	}
}

func salesJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func salesJSONErr(w http.ResponseWriter, code int, msg string) {
	salesJSON(w, code, map[string]any{"ok": false, "error": msg})
}

// ── период ───────────────────────────────────────────────────────────────────

var (
	salesLocOnce sync.Once
	salesLoc     *time.Location
)

func salesLocation() *time.Location {
	salesLocOnce.Do(func() {
		l, err := time.LoadLocation(timezone)
		if err != nil {
			l = time.UTC
		}
		salesLoc = l
	})
	return salesLoc
}

func salesPeriodSince(key string) (string, string, time.Time) {
	now := time.Now()
	switch key {
	case "1h":
		return "1h", "за последний час", now.Add(-time.Hour)
	case "24h":
		return "24h", "за 24 часа", now.Add(-24 * time.Hour)
	case "7d":
		return "7d", "за 7 дней", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", "за 30 дней", now.Add(-30 * 24 * time.Hour)
	case "all":
		return "all", "за всё время", time.Time{}
	default:
		loc := salesLocation()
		n := now.In(loc)
		return "today", "сегодня с 00:00", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	}
}

func salesTSBound(since time.Time) string {
	if since.IsZero() {
		return "0000"
	}
	return since.UTC().Format(time.RFC3339)
}

// Длинные периоды — это скан сотен тысяч строк, а фронт опрашивает раз в 7 секунд.
func salesPeriodTTL(period string) time.Duration {
	switch period {
	case "7d":
		return 30 * time.Second
	case "30d", "all":
		return 2 * time.Minute
	default:
		return 3 * time.Second
	}
}

type salesAggCache struct {
	at    time.Time
	aggs  map[string]*tradeAgg
	ok    bool
	facts map[string]*factMarkupAgg
}

var (
	salesCacheMu   sync.Mutex
	salesAggCached = map[string]*salesAggCache{}
	salesMarkCache = map[string]*salesMarkCacheEntry{}
)

type salesMarkCacheEntry struct {
	at    time.Time
	stats salesMarkupStats
}

func salesAggregates(period string, since time.Time) (map[string]*tradeAgg, bool, map[string]*factMarkupAgg) {
	ttl := salesPeriodTTL(period)
	salesCacheMu.Lock()
	if c, ok := salesAggCached[period]; ok && time.Since(c.at) < ttl {
		salesCacheMu.Unlock()
		return c.aggs, c.ok, c.facts
	}
	salesCacheMu.Unlock()

	mutex.RLock()
	fallbackPrices := make(map[string]int, len(data.Prices))
	for item, price := range data.Prices {
		fallbackPrices[item] = price
	}
	mutex.RUnlock()

	aggs, ok := queryTradeAggregates(since)
	facts := queryFactMarkups(since, fallbackPrices)

	salesCacheMu.Lock()
	salesAggCached[period] = &salesAggCache{at: time.Now(), aggs: aggs, ok: ok, facts: facts}
	salesCacheMu.Unlock()
	return aggs, ok, facts
}

func salesMarkups(item, period string, since time.Time, fallbackPrice int) salesMarkupStats {
	key := period + "|" + item
	ttl := salesPeriodTTL(period)
	salesCacheMu.Lock()
	if c, ok := salesMarkCache[key]; ok && time.Since(c.at) < ttl {
		salesCacheMu.Unlock()
		return c.stats
	}
	salesCacheMu.Unlock()

	stats := computeMarkupStats(item, since, fallbackPrice)

	salesCacheMu.Lock()
	salesMarkCache[key] = &salesMarkCacheEntry{at: time.Now(), stats: stats}
	salesCacheMu.Unlock()
	return stats
}

// ── прочность → честная цена продажи ─────────────────────────────────────────

// fairSellPrice повторяет priceWithDurability из items/slotInfo.mjs:
// битый предмет бот выставляет пропорционально остатку прочности, ниже 50% — не берёт.
func fairSellPrice(basePrice int, durability float64) int {
	if basePrice <= 0 {
		return 0
	}
	if durability <= 0 || durability >= 1 {
		return basePrice
	}
	if durability < 0.5 {
		return 0
	}
	marker := basePrice % 100
	price := int(math.Floor(float64(basePrice) * durability))
	return (price/100)*100 + marker
}

// ── агрегаты из trade_events ─────────────────────────────────────────────────

type tradeAgg struct {
	Buys, Sells, Tries int
	BuySum, SellSum    int
}

type factMarkupAgg struct {
	Samples   int
	MarkupAbs float64
	PlanAbs   float64
	MarkupPct float64
	PlanPct   float64
	Durab     float64
}

func queryTradeAggregates(since time.Time) (map[string]*tradeAgg, bool) {
	if mlDB == nil {
		return nil, false
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()

	rows, err := mlDB.Query(`
SELECT item_id, event_type, COUNT(*), COALESCE(SUM(price), 0)
FROM trade_events
WHERE ts >= ?
GROUP BY item_id, event_type`, salesTSBound(since))
	if err != nil {
		log.Printf("[sales] aggregates: %v", err)
		return nil, false
	}
	defer rows.Close()

	out := make(map[string]*tradeAgg, 64)
	for rows.Next() {
		var item, ev string
		var cnt, sum int
		if err := rows.Scan(&item, &ev, &cnt, &sum); err != nil {
			log.Printf("[sales] aggregates scan: %v", err)
			return nil, false
		}
		a := out[item]
		if a == nil {
			a = &tradeAgg{}
			out[item] = a
		}
		switch ev {
		case "buy":
			a.Buys, a.BuySum = cnt, sum
		case "sell":
			a.Sells, a.SellSum = cnt, sum
		case "try-sell":
			a.Tries = cnt
		}
	}
	return out, rows.Err() == nil
}

// queryFactMarkups — средняя фактическая наценка по каждому предмету.
// fair = ref_price * прочность (как её считает бот при выставлении лота),
// поэтому купленный полудохлый предмет не завышает наценку.
func queryFactMarkups(since time.Time, fallbackPrices map[string]int) map[string]*factMarkupAgg {
	if mlDB == nil {
		return nil
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()

	rows, err := mlDB.Query(`
SELECT item_id, price, COALESCE(nacenka, 0),
       COALESCE(durability, 1.0), COALESCE(ref_price, 0)
FROM trade_events
WHERE ts >= ? AND event_type = 'buy' AND price > 0`, salesTSBound(since))
	if err != nil {
		log.Printf("[sales] fact markups: %v", err)
		return nil
	}
	defer rows.Close()

	out := make(map[string]*factMarkupAgg, 64)
	for rows.Next() {
		var item string
		var paid, plan, ref int
		var dur float64
		if err := rows.Scan(&item, &paid, &plan, &dur, &ref); err != nil {
			log.Printf("[sales] fact markups scan: %v", err)
			continue
		}
		if ref <= 0 {
			ref = fallbackPrices[item]
		}
		if dur <= 0 || dur > 1 {
			dur = 1
		}
		fair := fairSellPrice(ref, dur)
		if fair <= 0 {
			continue
		}
		a := out[item]
		if a == nil {
			a = &factMarkupAgg{}
			out[item] = a
		}
		a.Samples++
		a.MarkupAbs += float64(fair - paid)
		a.PlanAbs += float64(plan)
		a.MarkupPct += float64(fair-paid) * 100 / float64(paid)
		if fair-plan > 0 {
			a.PlanPct += float64(plan) * 100 / float64(fair-plan)
		}
		a.Durab += dur
	}
	for _, a := range out {
		n := float64(a.Samples)
		a.MarkupAbs /= n
		a.PlanAbs /= n
		a.MarkupPct /= n
		a.PlanPct /= n
		a.Durab /= n
	}
	return out
}

// ── overview ─────────────────────────────────────────────────────────────────

func windowFromTrades(item string, since time.Time) salesWindowView {
	var w salesWindowView
	for _, trade := range data.TradeHistory[item] {
		if !trade.Time.After(since) || trade.Price <= 0 {
			continue
		}
		switch trade.Type {
		case "buy":
			w.Buys++
			w.BuySum += trade.Price
			w.Profit -= trade.Price
		case "sell":
			w.Sells++
			w.SellSum += trade.Price
			w.Profit += trade.Price
		case "try-sell":
			w.TrySells++
		}
	}
	if w.Buys > 0 {
		w.AvgBuy = w.BuySum / w.Buys
	}
	if w.Sells > 0 {
		w.AvgSell = w.SellSum / w.Sells
	}
	return w
}

func buildSalesItemViewLocked(id string, cfg ItemConfig, now time.Time, agg *tradeAgg, fact *factMarkupAgg) salesItemView {
	price := data.Prices[id]
	nac := getNacenka(id)
	onAH := getItemCount(id)
	inv := getInventoryCount(id)
	st := data.AdjustState[id]

	v := salesItemView{
		ID:              id,
		Name:            cfg.Name,
		Type:            cfg.Type,
		Price:           price,
		Nacenka:         nac,
		ImpliedBuy:      price - nac,
		OnAH:            onAH,
		Inv:             inv,
		Held:            onAH + inv,
		LastCycleProfit: st.LastCycleProfit,
		LastCycleSales:  st.LastCycleSales,
		CycleMinutes:    cfg.AnalysisTime.Minutes(),
	}

	if agg != nil {
		v.Buys, v.Sells, v.TrySells = agg.Buys, agg.Sells, agg.Tries
		v.BuySum, v.SellSum = agg.BuySum, agg.SellSum
	} else {
		v.Buys, v.Sells, v.TrySells = data.BuyStats[id], data.SellStats[id], data.TrySellStats[id]
		v.BuySum, v.SellSum = data.BuySum[id], data.SellSum[id]
	}
	v.Profit = v.SellSum - v.BuySum

	if price > 0 && nac > 0 {
		v.NacenkaPct = float64(nac) * 100 / float64(price-nac)
	}
	if v.Buys > 0 {
		v.AvgBuy = v.BuySum / v.Buys
	}
	if v.Sells > 0 {
		v.AvgSell = v.SellSum / v.Sells
	}
	if v.AvgBuy > 0 && v.AvgSell > 0 {
		v.RealizedSpread = v.AvgSell - v.AvgBuy
	}
	if v.BuySum > 0 {
		v.MarginPct = float64(v.Profit) * 100 / float64(v.BuySum)
	}
	if fact != nil && fact.Samples > 0 {
		v.FactSamples = fact.Samples
		v.FactMarkup = int(math.Round(fact.MarkupAbs))
		v.FactMarkupPc = fact.MarkupPct
		v.PlanMarkupPc = fact.PlanPct
		v.AvgDurab = fact.Durab
	}

	v.Window1h = windowFromTrades(id, now.Add(-time.Hour))
	cycleSince := data.LastCycleAt[id]
	if cycleSince.IsZero() {
		cycleSince = now.Add(-cfg.AnalysisTime)
		if cfg.AnalysisTime <= 0 {
			cycleSince = now.Add(-10 * time.Minute)
		}
	}
	v.WindowCycle = windowFromTrades(id, cycleSince)
	if t, ok := data.LastTrade[id]; ok && !t.IsZero() {
		tt := t
		v.LastTradeAt = &tt
	}
	return v
}

func buildSalesOverview(periodKey string) salesOverview {
	now := time.Now()
	period, label, since := salesPeriodSince(periodKey)
	aggs, aggOK, facts := salesAggregates(period, since)

	mutex.RLock()
	defer mutex.RUnlock()

	out := salesOverview{
		OK:          true,
		Date:        currentDay,
		Period:      period,
		PeriodLabel: label,
		Source:      "trade_events",
		UpdatedAt:   now,
		Items:       make([]salesItemView, 0, len(itemsConfig)),
	}
	if !since.IsZero() {
		s := since
		out.Since = &s
	}
	if !aggOK {
		aggs = nil
		out.Source = "counters"
		if period != "today" {
			out.PeriodLabel = label + " (нет БД — показываем счётчики за сегодня)"
		}
	}

	ids := make([]string, 0, len(itemsConfig))
	for id := range itemsConfig {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var topProfitID, topVolID string
	var topProfit, topVol int
	haveTopProfit := false
	var factWeighted, planWeighted, factPctWeighted, planPctWeighted, factWeight float64

	for _, id := range ids {
		cfg := itemsConfig[id]
		var agg *tradeAgg
		if aggs != nil {
			if a, ok := aggs[id]; ok {
				agg = a
			} else {
				agg = &tradeAgg{}
			}
		}
		v := buildSalesItemViewLocked(id, cfg, now, agg, facts[id])
		out.Items = append(out.Items, v)

		out.Totals.Buys += v.Buys
		out.Totals.Sells += v.Sells
		out.Totals.TrySells += v.TrySells
		out.Totals.BuySum += v.BuySum
		out.Totals.SellSum += v.SellSum
		out.Totals.Profit += v.Profit
		out.StockAH += v.OnAH
		out.StockInv += v.Inv
		if v.Buys > 0 || v.Sells > 0 || v.OnAH > 0 || v.Inv > 0 {
			out.ItemsLive++
		}
		if v.FactSamples > 0 {
			w := float64(v.FactSamples)
			factWeighted += float64(v.FactMarkup) * w
			planWeighted += facts[id].PlanAbs * w
			factPctWeighted += v.FactMarkupPc * w
			planPctWeighted += v.PlanMarkupPc * w
			factWeight += w
		}
		if !haveTopProfit || v.Profit > topProfit {
			topProfit, topProfitID, haveTopProfit = v.Profit, id, true
		}
		if vol := v.Buys + v.Sells; vol > topVol {
			topVol, topVolID = vol, id
		}
	}

	if out.Totals.Buys > 0 {
		out.Totals.AvgBuy = out.Totals.BuySum / out.Totals.Buys
	}
	if out.Totals.Sells > 0 {
		out.Totals.AvgSell = out.Totals.SellSum / out.Totals.Sells
	}
	if factWeight > 0 {
		out.FactMarkup = int(math.Round(factWeighted / factWeight))
		out.PlanMarkup = int(math.Round(planWeighted / factWeight))
		out.FactMarkupPc = factPctWeighted / factWeight
		out.PlanMarkupPc = planPctWeighted / factWeight
	}
	out.TopProfit = topProfitID
	out.TopVolume = topVolID

	sort.SliceStable(out.Items, func(i, j int) bool {
		if out.Items[i].Profit != out.Items[j].Profit {
			return out.Items[i].Profit > out.Items[j].Profit
		}
		vi := out.Items[i].Buys + out.Items[i].Sells
		vj := out.Items[j].Buys + out.Items[j].Sells
		if vi != vj {
			return vi > vj
		}
		return out.Items[i].ID < out.Items[j].ID
	})
	return out
}

// ── карточка предмета ────────────────────────────────────────────────────────

type salesItemDetail struct {
	OK           bool             `json:"ok"`
	Period       string           `json:"period"`
	PeriodLabel  string           `json:"period_label"`
	Item         salesItemView    `json:"item"`
	Trades       []salesTradeView `json:"trades"`
	Cycles       []salesCycleView `json:"cycles"`
	Markups      salesMarkupStats `json:"markups"`
	PriceHistory []PriceRecord    `json:"price_history,omitempty"`
}

func buildSalesItemDetail(id, periodKey string) (salesItemDetail, bool) {
	now := time.Now()
	period, label, since := salesPeriodSince(periodKey)
	aggs, aggOK, facts := salesAggregates(period, since)

	mutex.RLock()
	cfg, ok := itemsConfig[id]
	if !ok {
		mutex.RUnlock()
		return salesItemDetail{}, false
	}
	var agg *tradeAgg
	if aggOK {
		if a, found := aggs[id]; found {
			agg = a
		} else {
			agg = &tradeAgg{}
		}
	}
	item := buildSalesItemViewLocked(id, cfg, now, agg, facts[id])

	logs := data.TradeHistory[id]
	start := 0
	if len(logs) > 80 {
		start = len(logs) - 80
	}
	trades := make([]salesTradeView, 0, len(logs)-start)
	for _, t := range logs[start:] {
		trades = append(trades, salesTradeView{Time: t.Time, Type: t.Type, Price: t.Price, Nacenka: t.Nacenka})
	}
	var ph []PriceRecord
	if h := priceHistory[id]; h != nil && len(h.Records) > 0 {
		ph = append([]PriceRecord(nil), h.Records...)
	}
	fallbackPrice := data.Prices[id]
	mutex.RUnlock()

	return salesItemDetail{
		OK:           true,
		Period:       period,
		PeriodLabel:  label,
		Item:         item,
		Trades:       trades,
		Cycles:       querySalesCycles(id, 40),
		Markups:      salesMarkups(id, period, since, fallbackPrice),
		PriceHistory: ph,
	}, true
}

// ── реальные наценки ─────────────────────────────────────────────────────────

type markupBucket struct {
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Count int     `json:"count"`
}

type markupBand struct {
	Label     string  `json:"label"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
	Count     int     `json:"count"`
	AvgPaid   int     `json:"avg_paid"`
	AvgFair   int     `json:"avg_fair"`
	AvgMarkup int     `json:"avg_markup"`
	AvgPlan   int     `json:"avg_plan"`
	AvgPct    float64 `json:"avg_pct"`
	PlanPct   float64 `json:"plan_pct"`
}

type markupTimePoint struct {
	TS      time.Time `json:"ts"`
	Count   int       `json:"count"`
	FactAbs int       `json:"fact_abs"`
	PlanAbs int       `json:"plan_abs"`
	FactPct float64   `json:"fact_pct"`
	PlanPct float64   `json:"plan_pct"`
	AvgDur  float64   `json:"avg_dur"`
}

type salesMarkupStats struct {
	OK        bool    `json:"ok"`
	Item      string  `json:"item,omitempty"`
	Samples   int     `json:"samples"`
	Approx    int     `json:"approx"`
	Note      string  `json:"note,omitempty"`
	BrokenPct float64 `json:"broken_pct"`

	FactPctAvg float64 `json:"fact_pct_avg"`
	FactPctMed float64 `json:"fact_pct_median"`
	FactPctP10 float64 `json:"fact_pct_p10"`
	FactPctP90 float64 `json:"fact_pct_p90"`
	FactAbsAvg int     `json:"fact_abs_avg"`
	FactAbsMed int     `json:"fact_abs_median"`
	FactAbsP10 int     `json:"fact_abs_p10"`
	FactAbsP90 int     `json:"fact_abs_p90"`
	PlanPctAvg float64 `json:"plan_pct_avg"`
	PlanAbsAvg int     `json:"plan_abs_avg"`
	BonusAvg   int     `json:"bonus_avg"`
	AvgDurab   float64 `json:"avg_durability"`

	Hist     []markupBucket    `json:"hist"`
	DurHist  []markupBucket    `json:"dur_hist"`
	Bands    []markupBand      `json:"bands"`
	Timeline []markupTimePoint `json:"timeline"`
}

type markupSample struct {
	ts      time.Time
	paid    int
	fair    int
	plan    int
	dur     float64
	factPct float64
	planPct float64
}

func buildSalesMarkups(item, periodKey string) salesMarkupStats {
	period, _, since := salesPeriodSince(periodKey)
	fallback := 0
	if item != "" {
		mutex.RLock()
		fallback = data.Prices[item]
		mutex.RUnlock()
	}
	return salesMarkups(item, period, since, fallback)
}

func loadMarkupSamples(item string, since time.Time, fallbackPrice int) ([]markupSample, int) {
	if mlDB == nil {
		return nil, 0
	}
	const maxSamples = 25000
	query := `
SELECT ts, price, COALESCE(nacenka, 0), COALESCE(durability, 1.0), COALESCE(ref_price, 0)
FROM trade_events
WHERE ts >= ? AND event_type = 'buy' AND price > 0`
	args := []any{salesTSBound(since)}
	if item != "" {
		query += ` AND item_id = ?`
		args = append(args, item)
	}
	// свежие сделки важнее: на длинных периодах отрезаем хвост, а не голову
	query += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, maxSamples)

	mlDBMu.Lock()
	rows, err := mlDB.Query(query, args...)
	if err != nil {
		mlDBMu.Unlock()
		log.Printf("[sales] markup samples: %v", err)
		return nil, 0
	}
	defer mlDBMu.Unlock()
	defer rows.Close()

	out := make([]markupSample, 0, 512)
	approx := 0
	for rows.Next() {
		var tsRaw string
		var paid, nac, ref int
		var dur float64
		if err := rows.Scan(&tsRaw, &paid, &nac, &dur, &ref); err != nil {
			log.Printf("[sales] markup scan: %v", err)
			break
		}
		if ref <= 0 {
			// старые записи без ref_price: берём текущую цену, помечаем как приблизительные
			if fallbackPrice <= 0 {
				continue
			}
			ref = fallbackPrice
			approx++
		}
		if dur <= 0 || dur > 1 {
			dur = 1
		}
		fair := fairSellPrice(ref, dur)
		if fair <= 0 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, tsRaw)
		if err != nil {
			ts = time.Time{}
		}
		s := markupSample{
			ts:      ts.Local(),
			paid:    paid,
			fair:    fair,
			plan:    nac,
			dur:     dur,
			factPct: float64(fair-paid) * 100 / float64(paid),
		}
		if fair-nac > 0 {
			s.planPct = float64(nac) * 100 / float64(fair-nac)
		}
		out = append(out, s)
	}
	// вернулись в хронологический порядок — на нём строятся таймлайн и окна
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, approx
}

func computeMarkupStats(item string, since time.Time, fallbackPrice int) salesMarkupStats {
	samples, approx := loadMarkupSamples(item, since, fallbackPrice)
	st := salesMarkupStats{OK: true, Item: item, Samples: len(samples), Approx: approx}
	if len(samples) == 0 {
		st.Note = "нет покупок за период"
		return st
	}
	if approx > 0 {
		st.Note = "часть сделок записана до обновления — для них взята текущая цена продажи"
	}

	var sumFact, sumPlan, sumDur float64
	var sumAbs, sumPlanAbs, broken int
	pcts := make([]float64, 0, len(samples))
	amounts := make([]float64, 0, len(samples))
	for _, s := range samples {
		sumFact += s.factPct
		sumPlan += s.planPct
		sumDur += s.dur
		sumAbs += s.fair - s.paid
		sumPlanAbs += s.plan
		if s.dur < 0.999 {
			broken++
		}
		pcts = append(pcts, s.factPct)
		amounts = append(amounts, float64(s.fair-s.paid))
	}
	n := float64(len(samples))
	st.FactPctAvg = sumFact / n
	st.PlanPctAvg = sumPlan / n
	st.AvgDurab = sumDur / n
	st.FactAbsAvg = int(math.Round(float64(sumAbs) / n))
	st.PlanAbsAvg = int(math.Round(float64(sumPlanAbs) / n))
	st.BonusAvg = st.FactAbsAvg - st.PlanAbsAvg
	st.BrokenPct = float64(broken) * 100 / n

	sort.Float64s(pcts)
	st.FactPctMed = percentileSorted(pcts, 0.5)
	st.FactPctP10 = percentileSorted(pcts, 0.1)
	st.FactPctP90 = percentileSorted(pcts, 0.9)

	sort.Float64s(amounts)
	st.FactAbsMed = int(math.Round(percentileSorted(amounts, 0.5)))
	st.FactAbsP10 = int(math.Round(percentileSorted(amounts, 0.1)))
	st.FactAbsP90 = int(math.Round(percentileSorted(amounts, 0.9)))
	st.Hist = buildHistogram(amounts, 16)
	durs := make([]float64, 0, len(samples))
	for _, s := range samples {
		durs = append(durs, s.dur*100)
	}
	sort.Float64s(durs)
	st.DurHist = buildHistogram(durs, 12)
	st.Bands = buildDurabilityBands(samples)
	st.Timeline = buildMarkupTimeline(samples)
	return st
}

func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(p * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func buildHistogram(sorted []float64, buckets int) []markupBucket {
	if len(sorted) == 0 || buckets <= 0 {
		return nil
	}
	// хвосты обрезаем по 2%, иначе один выброс растягивает всю шкалу
	lo := percentileSorted(sorted, 0.02)
	hi := percentileSorted(sorted, 0.98)
	if hi <= lo {
		hi = lo + 1
	}
	step := (hi - lo) / float64(buckets)
	out := make([]markupBucket, buckets)
	for i := range out {
		out[i] = markupBucket{From: lo + step*float64(i), To: lo + step*float64(i+1)}
	}
	for _, v := range sorted {
		idx := int((v - lo) / step)
		if idx < 0 {
			idx = 0
		}
		if idx >= buckets {
			idx = buckets - 1
		}
		out[idx].Count++
	}
	return out
}

func buildDurabilityBands(samples []markupSample) []markupBand {
	type bandDef struct {
		label    string
		from, to float64
	}
	defs := []bandDef{
		{"целые (100%)", 0.999, 1.01},
		{"90–99%", 0.90, 0.999},
		{"80–90%", 0.80, 0.90},
		{"70–80%", 0.70, 0.80},
		{"60–70%", 0.60, 0.70},
		{"50–60%", 0.50, 0.60},
	}
	acc := make([]struct {
		count                      int
		paid, fair, markup, planAb int
		pct, planPct               float64
	}, len(defs))

	for _, s := range samples {
		for i, d := range defs {
			if s.dur >= d.from && s.dur < d.to {
				acc[i].count++
				acc[i].paid += s.paid
				acc[i].fair += s.fair
				acc[i].markup += s.fair - s.paid
				acc[i].planAb += s.plan
				acc[i].pct += s.factPct
				acc[i].planPct += s.planPct
				break
			}
		}
	}

	out := make([]markupBand, 0, len(defs))
	for i, d := range defs {
		a := acc[i]
		if a.count == 0 {
			continue
		}
		c := float64(a.count)
		out = append(out, markupBand{
			Label:     d.label,
			From:      d.from,
			To:        d.to,
			Count:     a.count,
			AvgPaid:   a.paid / a.count,
			AvgFair:   a.fair / a.count,
			AvgMarkup: a.markup / a.count,
			AvgPlan:   a.planAb / a.count,
			AvgPct:    a.pct / c,
			PlanPct:   a.planPct / c,
		})
	}
	return out
}

func buildMarkupTimeline(samples []markupSample) []markupTimePoint {
	if len(samples) == 0 {
		return nil
	}
	first, last := samples[0].ts, samples[len(samples)-1].ts
	span := last.Sub(first)
	if span <= 0 {
		span = time.Minute
	}
	const slots = 48
	step := span / slots
	if step < time.Minute {
		step = time.Minute
	}

	type acc struct {
		count              int
		fact, plan, durSum float64
		factAbs, planAbs   int
	}
	buckets := make(map[int64]*acc, slots)
	order := make([]int64, 0, slots)
	for _, s := range samples {
		key := s.ts.Sub(first) / step
		k := int64(key)
		a := buckets[k]
		if a == nil {
			a = &acc{}
			buckets[k] = a
			order = append(order, k)
		}
		a.count++
		a.fact += s.factPct
		a.plan += s.planPct
		a.factAbs += s.fair - s.paid
		a.planAbs += s.plan
		a.durSum += s.dur
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]markupTimePoint, 0, len(order))
	for _, k := range order {
		a := buckets[k]
		c := float64(a.count)
		out = append(out, markupTimePoint{
			TS:      first.Add(time.Duration(k) * step),
			Count:   a.count,
			FactAbs: int(math.Round(float64(a.factAbs) / c)),
			PlanAbs: int(math.Round(float64(a.planAbs) / c)),
			FactPct: a.fact / c,
			PlanPct: a.plan / c,
			AvgDur:  a.durSum / c,
		})
	}
	return out
}

// ── циклы ────────────────────────────────────────────────────────────────────

func querySalesCycles(itemID string, limit int) []salesCycleView {
	if mlDB == nil || limit <= 0 {
		return nil
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()

	rows, err := mlDB.Query(`
SELECT id, ts, action, sales, buys, try_sells, on_ah, inv, held,
	price_before, price_after, nacenka_before, nacenka_after,
	profit_now, fwd_reward, fwd_profit_1, fwd_profit_2, fwd_profit_3,
	COALESCE(cycle_minutes,0)
FROM capital_cycles
WHERE item_id = ?
ORDER BY id DESC
LIMIT ?`, itemID, limit)
	if err != nil {
		log.Printf("[sales] cycles query: %v", err)
		return nil
	}
	defer rows.Close()

	out := make([]salesCycleView, 0, limit)
	for rows.Next() {
		var c salesCycleView
		var profit, fwdR, f1, f2, f3 sql.NullInt64
		if err := rows.Scan(
			&c.ID, &c.TS, &c.Action, &c.Sales, &c.Buys, &c.TrySells, &c.OnAH, &c.Inv, &c.Held,
			&c.PriceBefore, &c.PriceAfter, &c.NacenkaBefore, &c.NacenkaAfter,
			&profit, &fwdR, &f1, &f2, &f3, &c.CycleMinutes,
		); err != nil {
			log.Printf("[sales] cycles scan: %v", err)
			continue
		}
		if profit.Valid {
			v := int(profit.Int64)
			c.ProfitNow = &v
		}
		if fwdR.Valid {
			v := int(fwdR.Int64)
			c.FwdReward = &v
		}
		if f1.Valid {
			v := int(f1.Int64)
			c.FwdProfit1 = &v
		}
		if f2.Valid {
			v := int(f2.Int64)
			c.FwdProfit2 = &v
		}
		if f3.Valid {
			v := int(f3.Int64)
			c.FwdProfit3 = &v
		}
		out = append(out, c)
	}
	return out
}
