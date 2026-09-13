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

func TestMarketRecoveryLiveShouldRaise(t *testing.T) {
	if !marketRecoveryLiveEnabled {
		t.Skip("market_recovery live ↑ disabled (книга наебывает)")
	}
	resetMarketRecoveryShadowStateForTest()
	bookTrap := ahBookMarketRecoverySnap{MinAsk: 1_800_000, P10: 2_000_000, NSell: 20, NUUID: 40, OK: true}
	// deep trap: our=1kk, p10=2kk
	if !marketRecoveryLiveShouldRaise("LIVE_TRAP", 1_000_000, 100_000, 0, 0, 0, bookTrap, false) {
		t.Fatal("1kk vs 2kk empty should raise")
	}
	// normal SKU near market
	bookNear := ahBookMarketRecoverySnap{MinAsk: 1_900_000, P10: 2_000_000, NSell: 20, NUUID: 40, OK: true}
	if marketRecoveryLiveShouldRaise("LIVE_NEAR", 1_900_000, 100_000, 0, 0, 0, bookNear, false) {
		t.Fatal("near p10 should NOT raise")
	}
	// held>0 — ordinary SKU
	if marketRecoveryLiveShouldRaise("LIVE_HELD", 1_000_000, 100_000, 3, 0, 0, bookTrap, false) {
		t.Fatal("held>0 should NOT raise")
	}
	// buys/sales
	if marketRecoveryLiveShouldRaise("LIVE_BUY", 1_000_000, 100_000, 0, 1, 0, bookTrap, false) {
		t.Fatal("buys>0 should NOT raise")
	}
	if marketRecoveryLiveShouldRaise("LIVE_SALE", 1_000_000, 100_000, 0, 0, 1, bookTrap, false) {
		t.Fatal("sales>0 should NOT raise")
	}
	// thin book — dead/idle
	thin := ahBookMarketRecoverySnap{MinAsk: 1_800_000, P10: 2_000_000, NSell: 5, NUUID: 10, OK: true}
	if marketRecoveryLiveShouldRaise("LIVE_THIN", 1_000_000, 100_000, 0, 0, 0, thin, false) {
		t.Fatal("thin book should NOT raise")
	}
	// manual lock
	if marketRecoveryLiveShouldRaise("LIVE_LOCK", 1_000_000, 100_000, 0, 0, 0, bookTrap, true) {
		t.Fatal("manual lock should NOT raise")
	}
	// already buyable zone
	if marketRecoveryLiveShouldRaise("LIVE_BUYABLE", 1_700_000, 100_000, 0, 0, 0, bookTrap, false) {
		t.Fatal("our>=0.85*p10 should NOT raise")
	}
}

func TestMarketRecoveryLiveOnlyFullConditions(t *testing.T) {
	resetMarketRecoveryShadowStateForTest()
	// seed prior activity → C_sold_out
	mrShadowMu.Lock()
	st := mrShadowGet("SOLD_OUT")
	st.PriorHeld = []int{4, 2, 0}
	st.PriorBuys = []int{0, 0, 0}
	st.PriorSales = []int{3, 1, 0}
	mrShadowMu.Unlock()
	book := ahBookMarketRecoverySnap{MinAsk: 1_800_000, P10: 2_000_000, NSell: 20, NUUID: 40, OK: true}
	if marketRecoveryLiveShouldRaise("SOLD_OUT", 1_000_000, 100_000, 0, 0, 0, book, false) {
		t.Fatal("prior held/sales → not B")
	}
	// seed recent price_down
	resetMarketRecoveryShadowStateForTest()
	mrShadowMu.Lock()
	st2 := mrShadowGet("AFTER_DOWN")
	st2.RecentActions = []string{"corridor_hold_dead", "corridor_price_down_soft", "corridor_hold_dead"}
	mrShadowMu.Unlock()
	if marketRecoveryLiveShouldRaise("AFTER_DOWN", 1_000_000, 100_000, 0, 0, 0, book, false) {
		t.Fatal("recent price_down → not raise")
	}
}

func TestMarketRecoveryLiveCommitNoPriceMutation(t *testing.T) {
	if !marketRecoveryLiveEnabled {
		t.Skip("market_recovery live ↑ disabled (книга наебывает)")
	}
	resetMarketRecoveryShadowStateForTest()
	old := mlDB
	mlDB = nil
	defer func() { mlDB = old }()
	price := 1_000_000
	in := marketRecoveryEvalIn{
		Item: "COMMIT", Now: time.Now(), OurPrice: price, Step: 100_000,
		WinnerAction: marketRecoveryActionLive,
		Book:         ahBookMarketRecoverySnap{MinAsk: 1_800_000, P10: 2_000_000, NSell: 20, NUUID: 40, OK: true},
	}
	marketRecoveryCommitAfterLive(in)
	if in.OurPrice != price {
		t.Fatal("commit must not mutate OurPrice")
	}
	// next peek at same price after buyable exhaust from commit? 1kk/2kk still trap
	// RecoveryI was incremented; still should raise if not exhausted
	if !marketRecoveryLiveShouldRaise("COMMIT", price, 100_000, 0, 0, 0, in.Book, false) {
		t.Fatal("still in trap should allow another cycle raise")
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
