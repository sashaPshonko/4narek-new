package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedTradeEvents(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close(); mlDB = nil })

	if _, err := db.Exec(`CREATE TABLE trade_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, item_id TEXT, category_type TEXT,
		event_type TEXT, price INTEGER, nacenka INTEGER, enchants_json TEXT,
		durability REAL, ref_price INTEGER)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	base := time.Now().UTC().Add(-3 * time.Hour)
	rows := []struct {
		off  time.Duration
		ev   string
		paid int
		dur  any
	}{
		{0, "buy", 800005, 1.0},                // план: 1100005-300000 = 800005
		{10 * time.Minute, "buy", 700005, 1.0}, // взяли дешевле плана
		{20 * time.Minute, "buy", 250000, 0.6}, // битый: fair ≈ 660005
		{30 * time.Minute, "sell", 1100005, 1.0},
		{40 * time.Minute, "try-sell", 0, nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO trade_events (ts,item_id,category_type,event_type,price,nacenka,durability,ref_price)
			 VALUES (?,?,?,?,?,?,?,?)`,
			base.Add(r.off).Format(time.RFC3339), "shtany-1.21", "leggings", r.ev, r.paid, 300000, r.dur, 1100005,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	mlDB = db
}

func TestSalesHTTPRoutes(t *testing.T) {
	seedTradeEvents(t)
	mux := http.NewServeMux()
	registerSalesHTTP(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/sales/", "/sales/api/overview?period=24h", "/sales/api/markups?period=24h"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s → %d", path, res.StatusCode)
			continue
		}
		if strings.HasPrefix(path, "/sales/api/") {
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("GET %s: не JSON: %v", path, err)
			} else if payload["ok"] != true {
				t.Errorf("GET %s: ok = %v", path, payload["ok"])
			}
		} else if !bytes.Contains(body, []byte("Наценка по факту")) {
			t.Errorf("GET %s: страница отдалась без нового блока наценок", path)
		}
	}

	res, err := http.Get(srv.URL + "/sales/api/item/nope")
	if err != nil {
		t.Fatalf("GET item: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("неизвестный предмет → %d, want 404", res.StatusCode)
	}
}

func TestFairSellPriceMatchesBotFormula(t *testing.T) {
	cases := []struct {
		base int
		dur  float64
		want int
	}{
		{1100005, 1.0, 1100005},
		{1100005, 0.5, 550005},
		{1100005, 0.49, 0},
		{1100005, 0.6, 660005},
		{0, 1.0, 0},
	}
	for _, c := range cases {
		if got := fairSellPrice(c.base, c.dur); got != c.want {
			t.Errorf("fairSellPrice(%d, %.2f) = %d, want %d", c.base, c.dur, got, c.want)
		}
	}
}

func TestQueryTradeAggregates(t *testing.T) {
	seedTradeEvents(t)
	aggs, ok := queryTradeAggregates(time.Now().Add(-24 * time.Hour))
	if !ok {
		t.Fatal("aggregates failed")
	}
	a := aggs["shtany-1.21"]
	if a == nil {
		t.Fatal("no aggregate for item")
	}
	if a.Buys != 3 || a.Sells != 1 || a.Tries != 1 {
		t.Errorf("counts = %d/%d/%d, want 3/1/1", a.Buys, a.Sells, a.Tries)
	}
	if want := 800005 + 700005 + 250000; a.BuySum != want {
		t.Errorf("buy sum = %d, want %d", a.BuySum, want)
	}
}

func TestMarkupStatsAccountForDurability(t *testing.T) {
	seedTradeEvents(t)
	st := computeMarkupStats("shtany-1.21", time.Now().Add(-24*time.Hour), 1100005)
	if st.Samples != 3 {
		t.Fatalf("samples = %d, want 3", st.Samples)
	}
	if st.Approx != 0 {
		t.Errorf("approx = %d, want 0", st.Approx)
	}
	// битый предмет не должен считаться сверхприбыльным: fair = 660005, а не 1100005
	if st.FactAbsAvg <= 0 {
		t.Errorf("fact markup = %d, want > 0", st.FactAbsAvg)
	}
	if st.FactPctAvg <= st.PlanPctAvg {
		t.Errorf("fact %.1f%% should beat plan %.1f%% (брали дешевле потолка)", st.FactPctAvg, st.PlanPctAvg)
	}
	if st.BrokenPct < 33 || st.BrokenPct > 34 {
		t.Errorf("broken share = %.1f%%, want ~33%%", st.BrokenPct)
	}
	if len(st.Bands) != 2 {
		t.Errorf("bands = %d, want 2 (целые + 50–60%%)", len(st.Bands))
	}
	if len(st.Timeline) == 0 || len(st.Hist) == 0 {
		t.Error("timeline/hist пустые")
	}
}

func TestMarkupStatsFallsBackToCurrentPrice(t *testing.T) {
	seedTradeEvents(t)
	if _, err := mlDB.Exec(`UPDATE trade_events SET ref_price = NULL`); err != nil {
		t.Fatalf("update: %v", err)
	}
	st := computeMarkupStats("shtany-1.21", time.Now().Add(-24*time.Hour), 1100005)
	if st.Approx != 3 {
		t.Errorf("approx = %d, want 3", st.Approx)
	}
	if st.Note == "" {
		t.Error("ожидали пометку о приблизительных данных")
	}

	facts := queryFactMarkups(time.Now().Add(-24*time.Hour), map[string]int{
		"shtany-1.21": 1100005,
	})
	fact := facts["shtany-1.21"]
	if fact == nil || fact.Samples != 3 {
		t.Fatalf("overview fact = %#v, ожидали 3 старые покупки через текущую цену", fact)
	}
	if fact.MarkupAbs <= 0 {
		t.Errorf("overview fact markup = %.0f, ожидали положительную сумму", fact.MarkupAbs)
	}
}
