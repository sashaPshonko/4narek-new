package main

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// mlAdjustSnapshot — состояние на момент решения Go (до async shadow).
type mlAdjustSnapshot struct {
	At              time.Time
	Item            string
	CategoryType    string
	GoAction        string
	PriceBefore     int
	NacenkaBefore   int
	GoPriceAfter    int
	GoNacenkaAfter  int
	Sales           int
	Buys            int
	TrySells        int
	ProfitNow       int
	OnAH            int
	TotalStock      int
	NormalSales     int
	NormalCount     int
	MinBuyHistory   int
	CanRaisePrice   bool
	BotsCategory    int
	PlayersOnline   int
}

func initMLShadowTable() {
	if mlDB == nil {
		return
	}
	_, _ = mlDB.Exec(`
CREATE TABLE IF NOT EXISTS ml_shadow (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	category_type TEXT NOT NULL,
	regime TEXT,
	go_action TEXT NOT NULL,
	ml_action TEXT,
	go_price_delta INTEGER NOT NULL,
	ml_price_delta INTEGER,
	go_nacenka_delta INTEGER NOT NULL,
	ml_nacenka_delta INTEGER,
	predicted_reward INTEGER,
	predicted_gain_vs_hold INTEGER,
	sales INTEGER NOT NULL,
	total_stock INTEGER NOT NULL,
	agrees INTEGER NOT NULL,
	response_json TEXT
)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_ml_shadow_ts ON ml_shadow(ts)`)
}

func shadowPayloadFromSnap(s mlAdjustSnapshot) map[string]any {
	return map[string]any{
		"action":           "predict",
		"item":             s.Item,
		"category_type":    s.CategoryType,
		"price":            s.PriceBefore,
		"nacenka":          s.NacenkaBefore,
		"stock_ah":         s.OnAH,
		"stock_inv":        s.TotalStock - s.OnAH,
		"sells_1":          s.Sales,
		"buys_1":           s.Buys,
		"try_sells_1":      s.TrySells,
		"profit_1":         s.ProfitNow,
		"normal_sales":     s.NormalSales,
		"normal_count":     s.NormalCount,
		"buy_min_history":  s.MinBuyHistory,
		"can_raise_price":  s.CanRaisePrice,
		"bots_category":    s.BotsCategory,
		"players_online":   s.PlayersOnline,
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	default:
		return 0
	}
}

func strFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func runMLShadowAsync(s mlAdjustSnapshot) {
	if !mlShadowEnabled() || mlDB == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mlPredictTimeout()+2*time.Second)
		defer cancel()

		resp, err := mlPredict(ctx, shadowPayloadFromSnap(s))
		if err != nil {
			log.Printf("[ML-SHADOW] %s: predict failed: %v", s.Item, err)
			return
		}
		if strFromAny(resp["action"]) == "error" {
			log.Printf("[ML-SHADOW] %s: %v", s.Item, resp["error"])
			return
		}

		mlPD := intFromAny(resp["price_delta"])
		mlND := intFromAny(resp["nacenka_delta"])

		goPriceDelta := s.GoPriceAfter - s.PriceBefore
		goNacDelta := s.GoNacenkaAfter - s.NacenkaBefore
		agrees := goPriceDelta == mlPD && goNacDelta == mlND

		raw, _ := json.Marshal(resp)

		mlDBMu.Lock()
		defer mlDBMu.Unlock()
		_, err = mlDB.Exec(`
INSERT INTO ml_shadow (
	ts, item_id, category_type, regime, go_action, ml_action,
	go_price_delta, ml_price_delta, go_nacenka_delta, ml_nacenka_delta,
	predicted_reward, predicted_gain_vs_hold,
	sales, total_stock, agrees, response_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.At.UTC().Format(time.RFC3339),
			s.Item, s.CategoryType, strFromAny(resp["regime"]),
			s.GoAction, strFromAny(resp["model_action"]),
			goPriceDelta, mlPD, goNacDelta, mlND,
			intFromAny(resp["predicted_reward"]),
			intFromAny(resp["predicted_gain_vs_hold"]),
			s.Sales, s.TotalStock,
			boolToInt(agrees),
			string(raw),
		)
		if err != nil {
			log.Printf("[ML-SHADOW] insert: %v", err)
			return
		}

		if !agrees {
			log.Printf("[ML-SHADOW] %s | Go: %s (%+d/%+d) | ML: %s (%+d/%+d) | reward~%d | %s",
				s.Item, s.GoAction, goPriceDelta, goNacDelta,
				strFromAny(resp["model_action"]), mlPD, mlND,
				intFromAny(resp["predicted_reward"]),
				strFromAny(resp["regime_label"]),
			)
		}
	}()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
