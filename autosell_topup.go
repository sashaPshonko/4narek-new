package main

// Пополнение анархий фермы с селла.
//
// Если на анархии мало денег (блок «Мало денег» в /fleet), активный селлер заходит на неё и платит казначею
// фиксированную сумму. Деньги получает только казначей — дальше он сам раскидывает их по ботам.
// Такой перевод для sell-orch считается обычной выдачей (лимит: две выдачи до передачи остатка).
//
// Go ничего не переводит: он раз в 20 секунд шлёт sell-orch настройки и список анархий (`sell_topup`),
// а решает, когда заходить, сам sell-orch (только когда нет заказов и передачи остатка).
// Настройки (вкл/выкл и сумма) меняются на вкладке «Автопродажа» и хранятся в autosell_topup.json.

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

const (
	topupFile      = "autosell_topup.json"
	topupMinKK     = 5.0
	topupMaxKK     = 500.0
	topupDefaultKK = 50.0
	topupPushEvery = 20 * time.Second
)

type topupSettings struct {
	Enabled  bool    `json:"enabled"`
	AmountKK float64 `json:"amount_kk"`
}

var (
	topupMu  sync.Mutex
	topupCfg = topupSettings{Enabled: false, AmountKK: topupDefaultKK}
)

type topupTarget struct {
	Anarchy int     `json:"anarchy"`
	To      string  `json:"to"`
	NeedKK  float64 `json:"need_kk"` // сколько не хватает до рабочего уровня (для порядка, не для суммы перевода)
}

func clampTopupKK(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return topupDefaultKK
	}
	return math.Min(topupMaxKK, math.Max(topupMinKK, v))
}

func loadTopupSettings() {
	raw, err := os.ReadFile(topupFile)
	if err != nil {
		return
	}
	var s topupSettings
	if json.Unmarshal(raw, &s) != nil {
		log.Printf("[autosell] %s: не разобрал, оставляю настройки по умолчанию", topupFile)
		return
	}
	s.AmountKK = clampTopupKK(s.AmountKK)
	topupMu.Lock()
	topupCfg = s
	topupMu.Unlock()
}

func saveTopupSettings(s topupSettings) {
	raw, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(topupFile, raw, 0o644); err != nil {
		log.Printf("[autosell] не записал %s: %v", topupFile, err)
	}
}

// topupTargetsFrom — анархии, куда стоит платить: из блока «Мало денег», только с известным казначеем,
// самые бедные первыми. Чистая функция.
func topupTargetsFrom(views []lowMoneyAnarchyView) []topupTarget {
	out := []topupTarget{}
	for _, v := range views {
		if v.Treasurer == "" {
			continue
		}
		out = append(out, topupTarget{Anarchy: v.Anarchy, To: v.Treasurer, NeedKK: math.Round((v.NeedKK-v.CoinsKK)*10) / 10})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NeedKK > out[j].NeedKK })
	return out
}

func currentTopupTargets() []topupTarget {
	mutex.RLock()
	views := collectLowMoneyLocked(time.Now())
	mutex.RUnlock()
	return topupTargetsFrom(views)
}

// pushTopupToSell шлёт sell-orch настройки и список анархий. Нет связи с sell-orch — молча пропускаем.
func pushTopupToSell() error {
	sellStateMu.RLock()
	ws := sellStateWS
	sellStateMu.RUnlock()
	if ws == nil {
		return nil
	}
	topupMu.Lock()
	cfg := topupCfg
	topupMu.Unlock()
	return wsWriteJSON(ws, map[string]any{
		"action":    "sell_topup",
		"enabled":   cfg.Enabled,
		"amount_kk": cfg.AmountKK,
		"targets":   currentTopupTargets(),
	})
}

func startAutosellTopup() {
	loadTopupSettings()
	go func() {
		for range time.Tick(topupPushEvery) {
			runSafe("autosell:topup-push", func() {
				if err := pushTopupToSell(); err != nil {
					log.Printf("[autosell] пополнение: не отправил sell-orch: %v", err)
				}
			})
		}
	}()
}

// handleAutosellTopup — GET: настройки и текущий список анархий; POST {"enabled":bool,"amount_kk":number}.
func handleAutosellTopup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		var body struct {
			Enabled  *bool    `json:"enabled"`
			AmountKK *float64 `json:"amount_kk"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad json"})
			return
		}
		topupMu.Lock()
		if body.Enabled != nil {
			topupCfg.Enabled = *body.Enabled
		}
		if body.AmountKK != nil {
			topupCfg.AmountKK = clampTopupKK(*body.AmountKK)
		}
		cfg := topupCfg
		topupMu.Unlock()
		saveTopupSettings(cfg)
		log.Printf("[autosell] пополнение анархий: enabled=%v сумма=%.0fкк", cfg.Enabled, cfg.AmountKK)
		_ = pushTopupToSell()
	} else if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "GET or POST"})
		return
	}
	topupMu.Lock()
	cfg := topupCfg
	topupMu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"enabled":   cfg.Enabled,
		"amount_kk": cfg.AmountKK,
		"min_kk":    topupMinKK,
		"max_kk":    topupMaxKK,
		"targets":   currentTopupTargets(),
	})
}
