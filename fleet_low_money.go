package main

// Блок «Мало денег» на /fleet: анархии фермы, куда реально надо зайти и закинуть денег.
//
// Считаем по анархии, а не по боту: оркестратор сам выравнивает деньги между своими ботами (у кого меньше 13кк —
// получает от тех, у кого больше), так что один бедный бот рядом с богатыми — не повод заходить.
//
// Анархия попадает в список, если одновременно:
//  1. денег на всех её ботах вместе меньше, чем 13кк × ботов (выравнивание не может поднять всех до рабочего уровня);
//  2. даже если продать весь товар на АХ и в инвентаре (считаем 80% от цены — продастся не всё и не сразу),
//     не набирается 15кк × ботов (KEEP_SUM) — то есть деньги не вернутся сами;
//  3. так держится не меньше 20 минут (не мигать между выдачами и сразу после переводов казначею).
//
// Боты в бане не считаются. Боты в перерыве считаются: их деньги никуда не делись. Для инвиз-анархии (invis.json)
// пороги уменьшаются так же, как в боте (keep_sum вместо 15кк).
//
// «Закинуть ≈» — сколько не хватает до 15кк на каждого бота, с округлением вверх до 5кк. Кидать любому боту,
// который сейчас на анархии: дальше оркестратор сам раскидает.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	lowMoneyPoorPerBot = 13_000_000.0 // как poor_below() в оркестраторе
	lowMoneyKeepPerBot = 15_000_000.0 // как KEEP_SUM_BASE в боте
	lowMoneyStockShare = 0.8
	lowMoneyHold       = 20 * time.Minute
	lowMoneyRoundTo    = 5_000_000.0
)

type lowMoneyBotView struct {
	Username  string   `json:"username"`
	BalanceKK *float64 `json:"balance_kk,omitempty"`
	Live      bool     `json:"live"`
	OnAnarchy bool     `json:"on_anarchy"`
	Stock     int      `json:"stock"` // штук товара на АХ + в инвентаре
}

type lowMoneyAnarchyView struct {
	Anarchy int               `json:"anarchy"`
	Bots    []lowMoneyBotView `json:"bots"`
	CoinsKK float64           `json:"coins_kk"`
	StockKK float64           `json:"stock_kk"`
	NeedKK  float64           `json:"need_kk"`
	TopUpKK float64           `json:"top_up_kk"`
	Since   time.Time         `json:"since"`
	PayTo   []string          `json:"pay_to"` // кто сейчас на анархии — кидать любому
	// Казначей анархии (если он сейчас на анархии): ему и платит автоселл. Пусто — казначей неизвестен или не онлайн.
	Treasurer string `json:"treasurer,omitempty"`
	Unknown   int    `json:"unknown_balances"`
	Invis     bool   `json:"invis,omitempty"`
}

// clientTreasurer — казначей анархии по каждому оркестратору (presence.treasurer). Под общим mutex.
var clientTreasurer = make(map[*websocket.Conn]string)

func setClientTreasurer(ws *websocket.Conn, nick string) {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		delete(clientTreasurer, ws)
		return
	}
	clientTreasurer[ws] = nick
}

var (
	lowMoneyMu    sync.Mutex
	lowMoneySince = map[int]time.Time{}

	invisScaleMu   sync.Mutex
	invisScaleAt   time.Time
	invisScaleByAn map[int]float64
)

// invisScale — во сколько раз пороги анархии меньше обычных (как ipc::balance::scale в боте).
func invisScale(anarchy int) float64 {
	invisScaleMu.Lock()
	defer invisScaleMu.Unlock()
	if time.Since(invisScaleAt) > 5*time.Minute || invisScaleByAn == nil {
		invisScaleAt = time.Now()
		invisScaleByAn = map[int]float64{}
		root := os.Getenv("FARM_ROOT")
		if root == "" {
			root = "../4narek-azalea"
		}
		if raw, err := os.ReadFile(filepath.Join(root, "invis.json")); err == nil {
			var cfg struct {
				Enabled   bool    `json:"enabled"`
				Anarchies []int   `json:"anarchies"`
				KeepSum   float64 `json:"keep_sum"`
			}
			if json.Unmarshal(raw, &cfg) == nil && cfg.Enabled {
				keep := cfg.KeepSum
				if keep <= 0 {
					keep = 5_000_000
				}
				for _, a := range cfg.Anarchies {
					invisScaleByAn[a] = keep / lowMoneyKeepPerBot
				}
			}
		}
	}
	if s, ok := invisScaleByAn[anarchy]; ok {
		return s
	}
	return 1
}

// lowMoneyAssess — чистая функция: оценка одной анархии. prices — цена продажи по id товара.
func lowMoneyAssess(anarchy int, bots []orchBotNick, prices map[string]int, scale float64) (lowMoneyAnarchyView, bool) {
	v := lowMoneyAnarchyView{Anarchy: anarchy, Invis: scale != 1}
	var coins, stock float64
	n := 0
	for _, b := range bots {
		if b.Banned {
			continue
		}
		n++
		row := lowMoneyBotView{Username: b.Username, Live: b.Live, OnAnarchy: b.OnAnarchy}
		if b.Balance != nil {
			coins += *b.Balance
			kk := math.Round(*b.Balance/100_000) / 10
			row.BalanceKK = &kk
		} else {
			v.Unknown++
		}
		for _, counts := range []map[string]int{botCounts(b.Items), botCounts(b.Inventory)} {
			for id, c := range counts {
				row.Stock += c
				stock += float64(c) * float64(prices[id])
			}
		}
		if b.Live && b.OnAnarchy {
			v.PayTo = append(v.PayTo, b.Username)
		}
		v.Bots = append(v.Bots, row)
	}
	if n == 0 || v.Unknown == n {
		return v, false // нет ботов или ещё не знаем ни одного баланса
	}
	stock *= lowMoneyStockShare
	poor := float64(n) * lowMoneyPoorPerBot * scale
	keep := float64(n) * lowMoneyKeepPerBot * scale
	v.CoinsKK = math.Round(coins/100_000) / 10
	v.StockKK = math.Round(stock/100_000) / 10
	v.NeedKK = math.Round(keep/100_000) / 10
	low := coins < poor && coins+stock < keep
	if !low {
		return v, false
	}
	gap := keep - coins
	step := lowMoneyRoundTo * scale
	v.TopUpKK = math.Ceil(gap/step) * step / 1_000_000
	sort.Strings(v.PayTo)
	return v, true
}

// collectLowMoneyLocked — mutex (общий) уже взят на чтение.
func collectLowMoneyLocked(now time.Time) []lowMoneyAnarchyView {
	byAn := map[int][]orchBotNick{}
	treasByAn := map[int]string{}
	for ws, rows := range clientOrchBots {
		tr := clientTreasurer[ws]
		for _, b := range rows {
			if a := anarchyInt(b.Anarchy); a > 0 {
				byAn[a] = append(byAn[a], b)
				if tr != "" && b.Username == tr && b.Live && b.OnAnarchy && !b.Banned {
					treasByAn[a] = tr
				}
			}
		}
	}
	lowMoneyMu.Lock()
	defer lowMoneyMu.Unlock()
	out := []lowMoneyAnarchyView{}
	seen := map[int]bool{}
	for a, bots := range byAn {
		seen[a] = true
		v, low := lowMoneyAssess(a, bots, data.Prices, invisScale(a))
		v.Treasurer = treasByAn[a]
		if !low {
			delete(lowMoneySince, a)
			continue
		}
		since, ok := lowMoneySince[a]
		if !ok {
			since = now
			lowMoneySince[a] = now
		}
		if now.Sub(since) < lowMoneyHold {
			continue
		}
		v.Since = since
		out = append(out, v)
	}
	for a := range lowMoneySince {
		if !seen[a] {
			delete(lowMoneySince, a) // оркестратор анархии отключился
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Anarchy < out[j].Anarchy })
	return out
}
