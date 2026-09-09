package main

import (
	"testing"
	"time"
)

func TestMarketRecoveryGapOK(t *testing.T) {
	step := 100_000
	// ratio ≤ 0.80
	if !marketRecoveryGapOK(800_000, 1_000_000, step) {
		t.Fatal("0.80 ratio should be trap")
	}
	if marketRecoveryGapOK(810_000, 1_000_000, step) {
		// 0.81 and gap_steps = 1.9 < 4 → not trap
		t.Fatal("0.81 with small gap should not be trap")
	}
	// gap steps ≥ 4 even if ratio > 0.80
	// our=900k, p10=1.3M → ratio=0.69 already; use our close to p10 with big step? 
	// our=960k p10=1.4M ratio=0.686; need ratio>0.80 and gap≥4:
	// our=850k p10=1M ratio=0.85, gap=(150k)/100k=1.5 < 4 → false
	if marketRecoveryGapOK(850_000, 1_000_000, step) {
		t.Fatal("shallow gap should not be trap")
	}
	// our=550k p10=1M ratio=0.55 → true via ratio
	if !marketRecoveryGapOK(550_000, 1_000_000, step) {
		t.Fatal("deep ratio should be trap")
	}
	// our=820k p10=1.3M ratio≈0.63; 
	// for ratio>0.80 AND gap≥4: our=1.05M, p10=1.3M, ratio=0.807, gap=2.5 — false
	if marketRecoveryGapOK(1_050_000, 1_300_000, step) {
		t.Fatal("ratio>0.80 and gap<4 should fail")
	}
	// our=900k p10=1.4M ratio≈0.643 via ratio
	// our=1.1M p10=1.5M ratio=0.733
	// Explicit gap≥4 with ratio just above 0.80 impossible if gap=(p10-our)/step ≥4 means our ≤ p10-4*step.
	// For p10=1M step=100k: our≤600k → ratio≤0.60 always. So OR second clause matters when step is large vs p10.
	// step=50k, our=850k, p10=1.05M: ratio=0.809, gap=4.0 → true via steps
	if !marketRecoveryGapOK(850_000, 1_050_000, 50_000) {
		t.Fatal("gap_steps≥4 should be trap even if ratio>0.80")
	}
}

func TestMarketRecoveryTrustAndBuyable(t *testing.T) {
	if !marketRecoveryTrustOK(15, 25) {
		t.Fatal("exact threshold should pass")
	}
	if marketRecoveryTrustOK(14, 25) || marketRecoveryTrustOK(15, 24) {
		t.Fatal("below threshold should fail")
	}
	if !marketRecoveryBuyableReached(850_000, 1_000_000) {
		t.Fatal("0.85*p10 should be buyable")
	}
	if marketRecoveryBuyableReached(849_000, 1_000_000) {
		t.Fatal("below 0.85 should not be buyable")
	}
}

func TestMarketRecoveryP10Stable(t *testing.T) {
	if !marketRecoveryP10Stable(nil) {
		t.Fatal("empty hist ok (cold start)")
	}
	if !marketRecoveryP10Stable([]int{100, 105}) {
		t.Fatal("5% drift ok")
	}
	if marketRecoveryP10Stable([]int{100, 130}) {
		t.Fatal("30% endpoint drift should fail")
	}
}

func TestMarketRecoveryHadPriceDown(t *testing.T) {
	acts := []string{"corridor_hold_dead", "corridor_hold_band", "corridor_price_down_soft"}
	if !marketRecoveryHadPriceDown(acts, 3) {
		t.Fatal("should see price_down")
	}
	acts2 := []string{"corridor_price_down_over", "hold", "hold", "hold"}
	if marketRecoveryHadPriceDown(acts2, 3) {
		t.Fatal("down outside last 3 should be ignored")
	}
}

func TestIsBPriceTrap(t *testing.T) {
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	in := marketRecoveryEvalIn{
		Item: "BOOTS", OurPrice: 700_000, Step: 100_000,
		Held: 0, Buys: 0, Sales: 0,
		WinnerAction: "corridor_hold_dead",
		Book:         book,
	}
	if !isBPriceTrap(in, []int{1_000_000, 1_010_000}, []string{"hold", "hold"}, nil, nil, nil) {
		t.Fatal("clean empty+deep should be B")
	}
	in.Held = 1
	if isBPriceTrap(in, []int{1_000_000}, nil, nil, nil, nil) {
		t.Fatal("held>0 not B")
	}
	in.Held = 0
	in.ManualLock = true
	if isBPriceTrap(in, []int{1_000_000}, nil, nil, nil, nil) {
		t.Fatal("manual lock not B")
	}
	in.ManualLock = false
	// C_sold_out: prior held
	if isBPriceTrap(in, []int{1_000_000, 1_000_000}, []string{"hold"}, []int{5}, []int{0}, []int{2}) {
		t.Fatal("prior held/sales → sold_out/thru not B")
	}
	// no trust
	in.Book.NSell = 5
	if isBPriceTrap(in, []int{1_000_000}, nil, nil, nil, nil) {
		t.Fatal("thin book not B")
	}
}

func TestEvaluateMarketRecoveryShadowStepsAndStops(t *testing.T) {
	resetMarketRecoveryShadowStateForTest()
	// avoid DB inserts
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()

	now := time.Now()
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	base := marketRecoveryEvalIn{
		Item: "TEST_SKU", Now: now, OurPrice: 700_000, Step: 50_000,
		Held: 0, Buys: 0, Sales: 0, WinnerAction: "corridor_hold_dead", Book: book,
	}

	o1 := evaluateMarketRecoveryShadow(base)
	if !o1.DidStep || o1.PredictedPrice != 750_000 || o1.RecoveryI != 1 {
		t.Fatalf("start step: %+v", o1)
	}
	// real price unchanged — we only return predicted
	if base.OurPrice != 700_000 {
		t.Fatal("input our must stay untouched")
	}

	base.Now = now.Add(10 * time.Minute)
	o2 := evaluateMarketRecoveryShadow(base)
	if !o2.DidStep || o2.PredictedPrice != 800_000 || o2.RecoveryI != 2 {
		t.Fatalf("second step: %+v", o2)
	}

	// buys → stop
	base.Now = now.Add(20 * time.Minute)
	base.Buys = 1
	o3 := evaluateMarketRecoveryShadow(base)
	if o3.DidStep || o3.StopReason != "buys" || o3.SessionActive {
		t.Fatalf("stop on buys: %+v", o3)
	}

	// after buys clear, still held=0 but prior had buys → sold_out/thru → no restart
	base.Buys = 0
	base.Now = now.Add(30 * time.Minute)
	o4 := evaluateMarketRecoveryShadow(base)
	if o4.DidStep {
		t.Fatalf("should not restart after recent buys activity: %+v", o4)
	}
}

func TestEvaluateMarketRecoveryStopsAtBuyable(t *testing.T) {
	resetMarketRecoveryShadowStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()

	now := time.Now()
	// our=800k, p10=1M, step=50k → buyable at 850k after 1 step
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	in := marketRecoveryEvalIn{
		Item: "BUYABLE_SKU", Now: now, OurPrice: 800_000, Step: 50_000,
		Held: 0, Buys: 0, Sales: 0, WinnerAction: "corridor_hold_dead", Book: book,
	}
	o := evaluateMarketRecoveryShadow(in)
	// predicted=850k = 0.85*p10 → stop buyable after step
	if o.PredictedPrice != 850_000 || o.StopReason != "buyable_zone" || o.SessionActive {
		t.Fatalf("should stop at buyable: %+v", o)
	}
}

func TestEvaluateMarketRecoveryNoPriceMutation(t *testing.T) {
	resetMarketRecoveryShadowStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()

	price := 600_000
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	in := marketRecoveryEvalIn{
		Item: "MUT", Now: time.Now(), OurPrice: price, Step: 100_000,
		Book: book, WinnerAction: "hold",
	}
	_ = evaluateMarketRecoveryShadow(in)
	if in.OurPrice != price {
		t.Fatalf("OurPrice mutated: %d", in.OurPrice)
	}
}

func TestEvaluateStopsOnTrustLostAndManual(t *testing.T) {
	resetMarketRecoveryShadowStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()

	now := time.Now()
	book := ahBookMarketRecoverySnap{MinAsk: 900_000, P10: 1_000_000, NSell: 20, NUUID: 40, OK: true}
	in := marketRecoveryEvalIn{
		Item: "TRUST", Now: now, OurPrice: 600_000, Step: 100_000,
		WinnerAction: "hold", Book: book,
	}
	if !evaluateMarketRecoveryShadow(in).DidStep {
		t.Fatal("expected start")
	}
	in.Now = now.Add(time.Minute)
	in.Book.NSell = 2
	in.Book.NUUID = 2
	o := evaluateMarketRecoveryShadow(in)
	if o.StopReason != "trust_lost" {
		t.Fatalf("trust_lost: %+v", o)
	}

	resetMarketRecoveryShadowStateForTest()
	in.Book = book
	in.ManualLock = false
	in.Now = now
	if !evaluateMarketRecoveryShadow(in).DidStep {
		t.Fatal("restart")
	}
	in.Now = now.Add(time.Minute)
	in.ManualLock = true
	o2 := evaluateMarketRecoveryShadow(in)
	if o2.StopReason != "manual_lock" {
		t.Fatalf("manual_lock: %+v", o2)
	}
}
