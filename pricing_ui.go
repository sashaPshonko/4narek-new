package main

import (
	"database/sql"
	"io/fs"
	"log"
	"net/http"
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
	NacenkaBefore int     `json:"nacenka_before"`
	NacenkaAfter  int     `json:"nacenka_after"`
	PriceFloor    int     `json:"price_floor"`
	Step          int     `json:"step"`
	Notes         string  `json:"notes"`
	ProfitNow     *int    `json:"profit_now,omitempty"`
	PlayersOnline int     `json:"players_online"`
	CycleMinutes  float64 `json:"cycle_minutes"`
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
	case path == "/decisions" && r.Method == http.MethodGet:
		item := strings.TrimSpace(r.URL.Query().Get("item"))
		period := r.URL.Query().Get("period")
		limit := 80
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 300 {
				limit = n
			}
		}
		dir := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dir")))
		salesJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"policy":    capitalPolicy,
			"period":    period,
			"updated_at": time.Now(),
			"decisions": queryPricingDecisions(item, period, dir, limit),
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
