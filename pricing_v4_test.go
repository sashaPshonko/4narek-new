package main

import (
	"strings"
	"testing"
)

func v4Base(held, sales, buys, price, step, share int) v4Input {
	return v4Input{
		Held: held, Sales: sales, Buys: buys,
		Price: price, Step: step, Share: share,
		PriceFloor: step,
		Band: stockBandFracs{
			lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
			over: stockOverFrac, dump: stockDumpFrac,
		},
	}
}

func TestV4DownOverstock(t *testing.T) {
	// share=100 → hi=25, over=35, dump=50
	in := v4Base(40, 1, 0, 2_000_000, 100_000, 100)
	d := v4Decide(in)
	if d.Action != "corridor_price_down_v4_over" {
		t.Fatalf("want over down got %+v", d)
	}
}

func TestV4DownDump(t *testing.T) {
	in := v4Base(55, 0, 0, 2_000_000, 100_000, 100)
	d := v4Decide(in)
	if d.Action != "corridor_price_down_v4_dump" {
		t.Fatalf("want dump down got %+v", d)
	}
}

func TestV4DownSoft(t *testing.T) {
	in := v4Base(30, 0, 0, 2_000_000, 100_000, 100)
	d := v4Decide(in)
	if d.Action != "corridor_price_down_v4_soft" {
		t.Fatalf("want soft down got %+v", d)
	}
}

func TestV4NoDownInBand(t *testing.T) {
	in := v4Base(20, 0, 0, 2_000_000, 100_000, 100)
	d := v4Decide(in)
	if strings.Contains(d.Action, "price_down") {
		t.Fatalf("band must not DOWN: %+v", d)
	}
}

func TestV4DemandUp(t *testing.T) {
	in := v4Base(5, 4, 0, 2_000_000, 100_000, 100) // lo=18
	d := v4Decide(in)
	if d.Action != "corridor_price_up_v4_demand" {
		t.Fatalf("want demand UP got %+v", d)
	}
}

func TestV4EmptyNoUp(t *testing.T) {
	in := v4Base(0, 0, 0, 1_000_000, 100_000, 100)
	d := v4Decide(in)
	if strings.Contains(d.Action, "price_up") {
		t.Fatalf("empty must not UP: %+v", d)
	}
}

func TestV4WeakDemandNoUp(t *testing.T) {
	in := v4Base(5, 2, 0, 2_000_000, 100_000, 100)
	d := v4Decide(in)
	if strings.Contains(d.Action, "price_up") {
		t.Fatalf("sales<3 must not UP: %+v", d)
	}
}

func TestV4NightNeedsMoreSales(t *testing.T) {
	in := v4Base(5, 3, 0, 2_000_000, 100_000, 100)
	in.Night = true
	d := v4Decide(in)
	if strings.Contains(d.Action, "price_up") {
		t.Fatalf("night sales=3 must not UP: %+v", d)
	}
}

func TestCapitalPolicyV4(t *testing.T) {
	if capitalPolicy != capitalPolicyV4 {
		t.Fatalf("active=%s want %s (rollback: capitalPolicyV9 / V8af)", capitalPolicy, capitalPolicyV4)
	}
}
