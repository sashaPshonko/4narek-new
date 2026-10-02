package main

import (
	"database/sql"
	"io/fs"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pricingDecisionView — решение цикла в формате старых TG interval-stats.
type pricingDecisionView struct {
	ID            int64   `json:"id"`
	TS            string  `json:"ts"`
	Policy        string  `json:"policy"`
	Item          string  `json:"item"`
	Category      string  `json:"category"`
	Action        string  `json:"action"`
	ReasonRU      string  `json:"reason_ru"`
	Dir           string  `json:"dir"` // UP | DOWN | HOLD
	Sales         int     `json:"sales"`
	Buys          int     `json:"buys"`
	TrySells      int     `json:"try_sells"`
	NormalSales   int     `json:"normal_sales"`
	OnAH          int     `json:"on_ah"`
	Inv           int     `json:"inv"`
	Held          int     `json:"held"`
	Share         int     `json:"share"`
	Free          int     `json:"free"`
	Need          int     `json:"need"`
	PriceBefore   int     `json:"price_before"`
	PriceAfter    int     `json:"price_after"`
	Delta         int     `json:"delta"`
	NacenkaBefore int     `json:"nacenka_before"`
	NacenkaAfter  int     `json:"nacenka_after"`
	PriceFloor    int     `json:"price_floor"`
	Step          int     `json:"step"`
	Notes         string  `json:"notes"`
	ProfitNow     *int    `json:"profit_now,omitempty"`
	PlayersOnline int     `json:"players_online"`
	CycleMinutes  float64 `json:"cycle_minutes"`
}

type pricingBoardItem struct {
	ID         string  `json:"id"`
	Price      int     `json:"price"`
	Open       int     `json:"open"`
	Close      int     `json:"close"`
	Net        int     `json:"net"`
	NetPct     float64 `json:"net_pct"`
	Ups        int     `json:"ups"`
	Downs      int     `json:"downs"`
	Holds      int     `json:"holds"`
	Moves      int     `json:"moves"`
	LastDir    string  `json:"last_dir"`
	LastAction string  `json:"last_action"`
	LastReason string  `json:"last_reason"`
	LastTS     string  `json:"last_ts"`
	LastSales  int     `json:"last_sales"`
	LastNorm   int     `json:"last_norm"`
	LastBuys   int     `json:"last_buys"`
	LastOnAH   int     `json:"last_on_ah"`
	LastInv    int     `json:"last_inv"`
	LastHeld   int     `json:"last_held"`
	Spark      []int   `json:"spark"`
}

type pricingBoardSummary struct {
	Ups    int `json:"ups"`
	Downs  int `json:"downs"`
	Holds  int `json:"holds"`
	Moves  int `json:"moves"`
	Cycles int `json:"cycles"`
}

type pricingBoardMover struct {
	ID     string  `json:"id"`
	Net    int     `json:"net"`
	NetPct float64 `json:"net_pct"`
	Price  int     `json:"price"`
}

func registerPricingHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/pricing", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pricing/", http.StatusFound)
	}))

	staticRoot, err := fs.Sub(salesStaticFS, "sales_static")
	if err != nil {
		log.Printf("[pricing] embed: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.Handle("/pricing/", recoverHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/pricing/api/") {
			pricingAPI(w, r)
			return
		}
		if r.URL.Path == "/pricing/" || r.URL.Path == "/pricing/index.html" {
			b, err := salesStaticFS.ReadFile("sales_static/pricing.html")
			if err != nil {
				http.Error(w, "pricing.html missing", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(b)
			return
		}
		r2 := *r
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, "/pricing")
		if u.Path == "" {
			u.Path = "/"
		}
		r2.URL = &u
		fileServer.ServeHTTP(w, &r2)
	})))
	log.Printf("[pricing] dashboard ready at /pricing/")
}

func pricingAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pricing/api")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/board" && r.Method == http.MethodGet:
		period := r.URL.Query().Get("period")
		item := strings.TrimSpace(r.URL.Query().Get("item"))
		dir := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dir")))
		limit := 120
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 400 {
				limit = n
			}
		}
		board := buildPricingBoard(period, item, dir, limit)
		salesJSON(w, http.StatusOK, board)
		return
	case path == "/decisions" && r.Method == http.MethodGet:
		item := strings.TrimSpace(r.URL.Query().Get("item"))
		period := r.URL.Query().Get("period")
		limit := 80
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 400 {
				limit = n
			}
		}
		dir := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dir")))
		salesJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"policy":     capitalPolicy,
			"period":     period,
			"updated_at": time.Now(),
			"decisions":  queryPricingDecisions(item, period, dir, limit),
		})
		return
	case path == "/items" && r.Method == http.MethodGet:
		salesJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"items": pricingItemList(),
		})
		return
	default:
		salesJSONErr(w, http.StatusNotFound, "not found")
	}
}

func pricingItemList() []string {
	mutex.RLock()
	defer mutex.RUnlock()
	out := make([]string, 0, len(itemsConfig))
	for id := range itemsConfig {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func pricingPeriodSince(period string) (since time.Time, ok bool) {
	now := time.Now()
	switch strings.TrimSpace(period) {
	case "", "3h":
		return now.Add(-3 * time.Hour), true
	case "1h":
		return now.Add(-time.Hour), true
	case "6h":
		return now.Add(-6 * time.Hour), true
	case "24h":
		return now.Add(-24 * time.Hour), true
	case "7d":
		return now.Add(-7 * 24 * time.Hour), true
	case "all":
		return time.Time{}, false
	default:
		return now.Add(-3 * time.Hour), true
	}
}

func pricingDirOf(before, after int, action string) string {
	if after > before {
		return "UP"
	}
	if after < before {
		return "DOWN"
	}
	a := strings.ToLower(action)
	if strings.Contains(a, "price_up") {
		return "UP"
	}
	if strings.Contains(a, "price_down") {
		return "DOWN"
	}
	return "HOLD"
}

func queryPricingDecisions(item, period, dirFilter string, limit int) []pricingDecisionView {
	if mlDB == nil || limit <= 0 {
		return nil
	}
	since, useSince := pricingPeriodSince(period)

	mlDBMu.Lock()
	defer mlDBMu.Unlock()

	q := `
SELECT id, ts, COALESCE(policy,''), item_id, COALESCE(category_type,''), action,
	sales, buys, try_sells, COALESCE(normal_sales,0),
	on_ah, inv, held, COALESCE(share,0), COALESCE(free_slots,0), COALESCE(need,0),
	price_before, price_after, nacenka_before, nacenka_after,
	COALESCE(price_floor,0), COALESCE(step,0), COALESCE(notes,''),
	profit_now, COALESCE(players_online,0), COALESCE(cycle_minutes,0)
FROM capital_cycles
WHERE 1=1`
	args := make([]any, 0, 4)
	if item != "" {
		q += ` AND item_id = ?`
		args = append(args, item)
	}
	if useSince {
		q += ` AND ts >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit*3) // запас под dir-фильтр

	rows, err := mlDB.Query(q, args...)
	if err != nil {
		log.Printf("[pricing] decisions query: %v", err)
		return nil
	}
	defer rows.Close()

	out := make([]pricingDecisionView, 0, limit)
	for rows.Next() {
		var d pricingDecisionView
		var profit sql.NullInt64
		if err := rows.Scan(
			&d.ID, &d.TS, &d.Policy, &d.Item, &d.Category, &d.Action,
			&d.Sales, &d.Buys, &d.TrySells, &d.NormalSales,
			&d.OnAH, &d.Inv, &d.Held, &d.Share, &d.Free, &d.Need,
			&d.PriceBefore, &d.PriceAfter, &d.NacenkaBefore, &d.NacenkaAfter,
			&d.PriceFloor, &d.Step, &d.Notes,
			&profit, &d.PlayersOnline, &d.CycleMinutes,
		); err != nil {
			log.Printf("[pricing] decisions scan: %v", err)
			continue
		}
		d.Dir = pricingDirOf(d.PriceBefore, d.PriceAfter, d.Action)
		d.Delta = d.PriceAfter - d.PriceBefore
		if dirFilter == "UP" || dirFilter == "DOWN" || dirFilter == "HOLD" {
			if d.Dir != dirFilter {
				continue
			}
		}
		d.ReasonRU = actionReasonRU(d.Action)
		if profit.Valid {
			v := int(profit.Int64)
			d.ProfitNow = &v
		}
		out = append(out, d)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func buildPricingBoard(period, itemFilter, dirFilter string, feedLimit int) map[string]any {
	decisions := queryPricingDecisions(itemFilter, period, dirFilter, feedLimit)
	// Серии/карточки — без dir-фильтра, чтобы доска не пустела при «только ↑».
	rawForBoard := queryPricingDecisions(itemFilter, period, "", 400)

	type agg struct {
		open, close                             int
		ups, downs, holds                       int
		lastDir, lastAction, lastReason, lastTS string
		lastSales, lastNorm, lastBuys           int
		lastOnAH, lastInv, lastHeld             int
		spark                                   []int
	}
	byItem := map[string]*agg{}

	for i := len(rawForBoard) - 1; i >= 0; i-- {
		d := rawForBoard[i]
		a := byItem[d.Item]
		if a == nil {
			a = &agg{}
			byItem[d.Item] = a
		}
		if a.open == 0 && d.PriceBefore > 0 {
			a.open = d.PriceBefore
		}
		if d.PriceAfter > 0 {
			a.close = d.PriceAfter
			a.spark = append(a.spark, d.PriceAfter)
		}
		switch d.Dir {
		case "UP":
			a.ups++
		case "DOWN":
			a.downs++
		default:
			a.holds++
		}
	}
	for _, d := range rawForBoard {
		a := byItem[d.Item]
		if a == nil || a.lastTS != "" {
			continue
		}
		a.lastTS = d.TS
		a.lastDir = d.Dir
		a.lastAction = d.Action
		a.lastReason = d.ReasonRU
		a.lastSales = d.Sales
		a.lastNorm = d.NormalSales
		a.lastBuys = d.Buys
		a.lastOnAH = d.OnAH
		a.lastInv = d.Inv
		a.lastHeld = d.Held
	}

	mutex.RLock()
	livePrices := mapsCloneInt(data.Prices)
	cfgIDs := make([]string, 0, len(itemsConfig))
	for id := range itemsConfig {
		cfgIDs = append(cfgIDs, id)
	}
	mutex.RUnlock()
	sort.Strings(cfgIDs)

	items := make([]pricingBoardItem, 0, len(cfgIDs))
	sum := pricingBoardSummary{}
	var topUp, topDown *pricingBoardMover

	for _, id := range cfgIDs {
		if itemFilter != "" && id != itemFilter {
			continue
		}
		a := byItem[id]
		price := livePrices[id]
		it := pricingBoardItem{ID: id, Price: price}
		if a != nil {
			it.Open = a.open
			it.Close = a.close
			if it.Close == 0 {
				it.Close = price
			}
			if it.Open > 0 && it.Close > 0 {
				it.Net = it.Close - it.Open
				it.NetPct = 100 * float64(it.Net) / float64(it.Open)
			}
			it.Ups, it.Downs, it.Holds = a.ups, a.downs, a.holds
			it.Moves = a.ups + a.downs
			it.LastDir = a.lastDir
			it.LastAction = a.lastAction
			it.LastReason = a.lastReason
			it.LastTS = a.lastTS
			it.LastSales = a.lastSales
			it.LastNorm = a.lastNorm
			it.LastBuys = a.lastBuys
			it.LastOnAH = a.lastOnAH
			it.LastInv = a.lastInv
			it.LastHeld = a.lastHeld
			it.Spark = downsampleInts(a.spark, 48)
			sum.Ups += a.ups
			sum.Downs += a.downs
			sum.Holds += a.holds
			sum.Moves += it.Moves
			sum.Cycles += a.ups + a.downs + a.holds

			if it.Net != 0 {
				m := &pricingBoardMover{ID: id, Net: it.Net, NetPct: it.NetPct, Price: price}
				if it.Net > 0 && (topUp == nil || it.NetPct > topUp.NetPct) {
					topUp = m
				}
				if it.Net < 0 && (topDown == nil || it.NetPct < topDown.NetPct) {
					topDown = m
				}
			}
		}

		liveSword := strings.Contains(id, "sword") || strings.Contains(id, "mega") || strings.Contains(id, "pochti")
		if a == nil && !liveSword && itemFilter == "" {
			continue
		}
		items = append(items, it)
	}

	sort.SliceStable(items, func(i, j int) bool {
		ai, aj := absInt(items[i].Net), absInt(items[j].Net)
		if ai != aj {
			return ai > aj
		}
		if items[i].Moves != items[j].Moves {
			return items[i].Moves > items[j].Moves
		}
		return items[i].ID < items[j].ID
	})

	return map[string]any{
		"ok":         true,
		"policy":     capitalPolicy,
		"period":     period,
		"updated_at": time.Now(),
		"summary":    sum,
		"items":      items,
		"decisions":  decisions,
		"top_up":     topUp,
		"top_down":   topDown,
	}
}

func mapsCloneInt(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func downsampleInts(xs []int, max int) []int {
	if max <= 0 || len(xs) <= max {
		return xs
	}
	out := make([]int, 0, max)
	step := float64(len(xs)-1) / float64(max-1)
	for i := 0; i < max; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx >= len(xs) {
			idx = len(xs) - 1
		}
		out = append(out, xs[idx])
	}
	return out
}
