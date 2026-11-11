package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Эталон оценки лота «сейчас» для бота: медиана оценок свежих покупок флота по категории.
//
// Бот считает оценку лота как маржа / √(held+1) (ah.rs, lot_score) и сверх лимита в 8 штук берёт только лоты, чья
// оценка заметно выше типичной у флота в этот момент (scoreRef). Сравнение именно с текущим рынком, а не с
// фиксированным числом: ночью, при просадке рынка или перекосе по товарам эталон сам смещается.
// Оценка покупки считается так же, как у бота: маржа = текущая цена продажи − цена покупки, held — штук на руках.
const (
	lotScoreRefWindow     = time.Hour
	lotScoreRefMinSamples = 8 // меньше свежих покупок — эталона нет, бот работает по запасному правилу
	lotScoreRefMaxKeep    = 400
)

type lotScoreSample struct {
	At    time.Time
	Score float64
}

var (
	lotScoreMu     sync.Mutex
	lotScoreByType = map[string][]lotScoreSample{}
)

// lotScore — та же формула, что у бота.
func lotScore(margin float64, held int) float64 {
	if held < 0 {
		held = 0
	}
	return margin / math.Sqrt(float64(held)+1)
}

// recordLotScoreLocked — mutex данных взят. Вызывается на каждую покупку.
func recordLotScoreLocked(item string, buyPrice int, now time.Time) {
	cfg, ok := itemsConfig[item]
	if !ok || buyPrice <= 0 {
		return
	}
	sell := data.Prices[item]
	margin := float64(sell - buyPrice)
	if margin <= 0 {
		return
	}
	held := getItemCount(item) + getInventoryCount(item)
	s := lotScoreSample{At: now, Score: lotScore(margin, held)}
	lotScoreMu.Lock()
	defer lotScoreMu.Unlock()
	list := append(lotScoreByType[cfg.Type], s)
	list = pruneLotScores(list, now)
	lotScoreByType[cfg.Type] = list
}

func pruneLotScores(list []lotScoreSample, now time.Time) []lotScoreSample {
	cut := now.Add(-lotScoreRefWindow)
	i := 0
	for i < len(list) && list[i].At.Before(cut) {
		i++
	}
	list = list[i:]
	if len(list) > lotScoreRefMaxKeep {
		list = list[len(list)-lotScoreRefMaxKeep:]
	}
	return list
}

// medianLotScore — чистая функция: медиана свежих оценок; ok=false, если их мало.
func medianLotScore(list []lotScoreSample, now time.Time) (float64, bool) {
	cut := now.Add(-lotScoreRefWindow)
	var xs []float64
	for _, s := range list {
		if !s.At.Before(cut) {
			xs = append(xs, s.Score)
		}
	}
	if len(xs) < lotScoreRefMinSamples {
		return 0, false
	}
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2], true
	}
	return (xs[n/2-1] + xs[n/2]) / 2, true
}

// lotScoreRef — эталон по категории (0 — данных мало). Не требует mutex данных.
func lotScoreRef(goType string, now time.Time) float64 {
	lotScoreMu.Lock()
	defer lotScoreMu.Unlock()
	m, ok := medianLotScore(lotScoreByType[goType], now)
	if !ok {
		return 0
	}
	return m
}
