package main

import (
	"strings"
	"testing"
)

func TestV10EmptyUpOneStep(t *testing.T) {
	d := v10Decide(v10Input{
		Held: 0, Sales: 0, Buys: 0, TrySells: 0,
		Price: 2_000_000, Step: 100_000, Share: 12,
		MultiFloor: 1_500_000, MultiFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if d.Action != "corridor_price_up_v10" || d.Reason != "empty" {
		t.Fatalf("got %s/%s want up/empty", d.Action, d.Reason)
	}
	if d.NewPrice != 2_100_000 {
		t.Fatalf("empty idle ↑ one step: got %d", d.NewPrice)
	}
}

func TestV10EmptyUpTowardTargetNotPastMid(t *testing.T) {
	d := v10Decide(v10Input{
		Held: 0, Sales: 2, Buys: 0, TrySells: 1,
		Price: 2_500_000, Step: 100_000, Share: 12,
		MultiFloor: 1_500_000, MultiFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if d.Action != "corridor_price_up_v10" {
		t.Fatalf("got %s/%s", d.Action, d.Reason)
	}
	if d.NewPrice > 3_000_000 {
		t.Fatalf("must not exceed mid: %d", d.NewPrice)
	}
	if d.NewPrice <= 2_500_000 {
		t.Fatalf("should climb: %d", d.NewPrice)
	}
}

func TestV10MidJump(t *testing.T) {
	// наличие в норме → жёсткий потолок
	d := v10Decide(v10Input{
		Held: 4, Price: 3_500_000, Step: 100_000, Share: 12,
		Nacenka: 300_000,
		BookMid: 3_000_000, BookMidOK: true,
		MultiFloor: 1_500_000, MultiFloorOK: true,
	})
	if d.Action != "corridor_price_down_v10_book_mid" || d.NewPrice != 3_000_000 {
		t.Fatalf("got %s %d", d.Action, d.NewPrice)
	}
}

func TestV10FillModeRelaxesMid(t *testing.T) {
	// пусто: до mid+nac не прибиваем; цель/↑ могут идти выше mid
	d := v10Decide(v10Input{
		Held: 0, Sales: 0, Buys: 0, TrySells: 0,
		Price: 3_100_000, Step: 100_000, Share: 12,
		Nacenka: 300_000,
		BookMid: 3_000_000, BookMidOK: true,
		MultiFloor: 1_500_000, MultiFloorOK: true,
	})
	if d.Action == "corridor_price_down_v10_book_mid" && d.Reason == "above_mid" {
		t.Fatalf("empty must not hard-snap to book mid: %s/%s %d", d.Action, d.Reason, d.NewPrice)
	}
	if d.NewPrice < 3_100_000 && d.Action == "corridor_price_down_v10_book_mid" {
		t.Fatalf("unexpected crush below fill cap: %d", d.NewPrice)
	}
}

func TestV10FillModeStillCapsExtreme(t *testing.T) {
	d := v10Decide(v10Input{
		Held: 0, Price: 4_000_000, Step: 100_000, Share: 12,
		Nacenka: 300_000,
		BookMid: 3_000_000, BookMidOK: true,
		MultiFloor: 1_500_000, MultiFloorOK: true,
	})
	if d.Action != "corridor_price_down_v10_book_mid" || d.NewPrice != 3_300_000 {
		t.Fatalf("got %s/%s %d want snap to mid+nac", d.Action, d.Reason, d.NewPrice)
	}
}

func TestV10FloorJump(t *testing.T) {
	d := v10Decide(v10Input{
		Held: 0, Price: 1_000_000, Step: 100_000, Share: 12,
		MultiFloor: 1_500_000, MultiFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if d.Action != "corridor_price_up_v10_book_floor" || d.NewPrice != 1_500_000 {
		t.Fatalf("got %s %d", d.Action, d.NewPrice)
	}
}

func TestV10UpVetoDeadNearCeiling(t *testing.T) {
	// близко к потолку полосы, витрина есть, продаж нет → veto ↑
	d := v10Decide(v10Input{
		Held: 0, Sales: 0, Buys: 0, TrySells: 5,
		Price: 3_200_000, Step: 100_000, Share: 12,
		MultiFloor: 1_500_000, MultiFloorOK: true,
		BookMid: 3_500_000, BookMidOK: true,
	})
	if d.Action != "corridor_hold_v10_up_veto" {
		t.Fatalf("got %s/%s want veto", d.Action, d.Reason)
	}
}

func TestV10NoDownWithoutExcess(t *testing.T) {
	// above target but held in band → no ↓
	d := v10Decide(v10Input{
		Held: 2, Sales: 0, Buys: 0, TrySells: 0,
		Price: 2_900_000, Step: 100_000, Share: 12,
		MultiFloor: 1_500_000, MultiFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if strings.Contains(d.Action, "price_down") {
		t.Fatalf("unexpected down: %s/%s", d.Action, d.Reason)
	}
}
