package main

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Price        int    `json:"price"`
	Nacenka      int    `json:"nacenka"`
	NacenkaPct   float64 `json:"nacenka_pct"`
	ImpliedBuy   int    `json:"implied_buy"`
	Buys         int    `json:"buys"`
	Sells        int    `json:"sells"`
	TrySells     int    `json:"try_sells"`
	BuySum       int    `json:"buy_sum"`
	SellSum      int    `json:"sell_sum"`
	Profit       int    `json:"profit"`
	AvgBuy       int    `json:"avg_buy,omitempty"`
	AvgSell      int    `json:"avg_sell,omitempty"`
	RealizedSpread int  `json:"realized_spread,omitempty"`
	MarginPct    float64 `json:"margin_pct"`
	OnAH         int    `json:"on_ah"`
	Inv          int    `json:"inv"`
	Held         int    `json:"held"`
	LastCycleProfit int `json:"last_cycle_profit"`
	LastCycleSales  int `json:"last_cycle_sales"`
	CycleMinutes float64 `json:"cycle_minutes"`
	Window1h     salesWindowView `json:"window_1h"`
	WindowCycle  salesWindowView `json:"window_cycle"`
	LastTradeAt  *time.Time `json:"last_trade_at,omitempty"`
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
	OK        bool            `json:"ok"`
	Date      string          `json:"date"`
	UpdatedAt time.Time       `json:"updated_at"`
	Totals    salesWindowView `json:"totals"`
	StockAH   int             `json:"stock_ah"`
	StockInv  int             `json:"stock_inv"`
	ItemsLive int             `json:"items_live"`
	TopProfit string          `json:"top_profit_id,omitempty"`
	TopVolume string          `json:"top_volume_id,omitempty"`
	Items     []salesItemView `json:"items"`
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

	switch {
	case path == "/overview" && r.Method == http.MethodGet:
		salesJSON(w, http.StatusOK, buildSalesOverview())
		return

	case strings.HasPrefix(path, "/item/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/item/")
		id = strings.Trim(id, "/")
		if id == "" {
			salesJSONErr(w, http.StatusBadRequest, "missing item id")
			return
		}
		item, ok := buildSalesItemDetail(id)
		if !ok {
			salesJSONErr(w, http.StatusNotFound, "unknown item")
			return
		}
		salesJSON(w, http.StatusOK, item)
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

func buildSalesItemViewLocked(id string, cfg ItemConfig, now time.Time) salesItemView {
	price := data.Prices[id]
	nac := getNacenka(id)
	buys := data.BuyStats[id]
	sells := data.SellStats[id]
	tries := data.TrySellStats[id]
	buySum := data.BuySum[id]
	sellSum := data.SellSum[id]
	profit := sellSum - buySum
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
		Buys:            buys,
		Sells:           sells,
		TrySells:        tries,
		BuySum:          buySum,
		SellSum:         sellSum,
		Profit:          profit,
		OnAH:            onAH,
		Inv:             inv,
		Held:            onAH + inv,
		LastCycleProfit: st.LastCycleProfit,
		LastCycleSales:  st.LastCycleSales,
		CycleMinutes:    cfg.AnalysisTime.Minutes(),
	}
	if price > 0 && nac > 0 {
		v.NacenkaPct = float64(nac) * 100 / float64(price)
	}
	if buys > 0 {
		v.AvgBuy = buySum / buys
	}
	if sells > 0 {
		v.AvgSell = sellSum / sells
	}
	if v.AvgBuy > 0 && v.AvgSell > 0 {
		v.RealizedSpread = v.AvgSell - v.AvgBuy
	}
	if buySum > 0 {
		v.MarginPct = float64(profit) * 100 / float64(buySum)
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

func buildSalesOverview() salesOverview {
	now := time.Now()
	mutex.RLock()
	defer mutex.RUnlock()

	out := salesOverview{
		OK:        true,
		Date:      currentDay,
		UpdatedAt: now,
		Items:     make([]salesItemView, 0, len(itemsConfig)),
	}

	var topProfitID, topVolID string
	var topProfit int
	var topVol int
	haveTopProfit := false

	ids := make([]string, 0, len(itemsConfig))
	for id := range itemsConfig {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		cfg := itemsConfig[id]
		v := buildSalesItemViewLocked(id, cfg, now)
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
		if !haveTopProfit || v.Profit > topProfit {
			topProfit = v.Profit
			topProfitID = id
			haveTopProfit = true
		}
		vol := v.Buys + v.Sells
		if vol > topVol {
			topVol = vol
			topVolID = id
		}
	}
	if out.Totals.Buys > 0 {
		out.Totals.AvgBuy = out.Totals.BuySum / out.Totals.Buys
	}
	if out.Totals.Sells > 0 {
		out.Totals.AvgSell = out.Totals.SellSum / out.Totals.Sells
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

type salesItemDetail struct {
	OK     bool             `json:"ok"`
	Item   salesItemView    `json:"item"`
	Trades []salesTradeView `json:"trades"`
	Cycles []salesCycleView `json:"cycles"`
	PriceHistory []PriceRecord `json:"price_history,omitempty"`
}

func buildSalesItemDetail(id string) (salesItemDetail, bool) {
	now := time.Now()
	mutex.RLock()
	cfg, ok := itemsConfig[id]
	if !ok {
		mutex.RUnlock()
		return salesItemDetail{}, false
	}
	item := buildSalesItemViewLocked(id, cfg, now)
	trades := make([]salesTradeView, 0, 80)
	logs := data.TradeHistory[id]
	start := 0
	if len(logs) > 80 {
		start = len(logs) - 80
	}
	for _, t := range logs[start:] {
		trades = append(trades, salesTradeView{
			Time:    t.Time,
			Type:    t.Type,
			Price:   t.Price,
			Nacenka: t.Nacenka,
		})
	}
	var ph []PriceRecord
	if h := priceHistory[id]; h != nil && len(h.Records) > 0 {
		ph = append([]PriceRecord(nil), h.Records...)
	}
	mutex.RUnlock()

	cycles := querySalesCycles(id, 40)
	return salesItemDetail{
		OK:           true,
		Item:         item,
		Trades:       trades,
		Cycles:       cycles,
		PriceHistory: ph,
	}, true
}

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
