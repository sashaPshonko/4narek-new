package main

import (
	"math"
	"testing"
	"time"
)

func TestLotScoreMatchesBotFormula(t *testing.T) {
	// те же числа, что в тесте бота: held 3 → √4 = 2, held 8 → √9 = 3
	if math.Abs(lotScore(600_000, 3)-300_000) > 1 || math.Abs(lotScore(600_000, 8)-200_000) > 1 {
		t.Fatal("формула расходится с ботом")
	}
	if lotScore(500_000, 0) <= lotScore(500_000, 6) {
		t.Fatal("больше запас — оценка ниже")
	}
}

func TestMedianLotScore(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	mk := func(ago time.Duration, s float64) lotScoreSample { return lotScoreSample{At: now.Add(-ago), Score: s} }
	few := []lotScoreSample{mk(time.Minute, 1), mk(2*time.Minute, 2)}
	if _, ok := medianLotScore(few, now); ok {
		t.Fatal("мало выборки — эталона быть не должно")
	}
	var list []lotScoreSample
	for i := 1; i <= 9; i++ { // 1..9, медиана 5
		list = append(list, mk(time.Duration(i)*time.Minute, float64(i)))
	}
	list = append(list, mk(5*time.Hour, 1000)) // старая — вне окна, не должна влиять
	m, ok := medianLotScore(list, now)
	if !ok || m != 5 {
		t.Fatalf("median=%v ok=%v", m, ok)
	}
}

func TestPruneLotScores(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	list := []lotScoreSample{{At: now.Add(-4 * time.Hour)}, {At: now.Add(-time.Hour)}}
	if got := pruneLotScores(list, now); len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
}
