package main

import (
	"strings"
	"testing"
)

func TestClassicUpLowSalesLowStock(t *testing.T) {
	d := classicDecide(classicInput{
		Sales: 2, Buys: 0, OnAH: 1, Inv: 0, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
	})
	if d.Action != "classic_price_up" {
		t.Fatalf("want UP got %+v", d)
	}
}

func TestClassicNoUpWhenStockHigh(t *testing.T) {
	// totalStock=16 >= 5*3=15 → no UP via rule 1
	d := classicDecide(classicInput{
		Sales: 2, Buys: 0, OnAH: 10, Inv: 6, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
	})
	if strings.Contains(d.Action, "price_up") {
		t.Fatalf("bloated stock must not UP: %+v", d)
	}
}

func TestClassicDownAHBloated(t *testing.T) {
	// stock >= N*3 чтобы не сработал UP; AH раздут + sales<N
	d := classicDecide(classicInput{
		Sales: 2, Buys: 0, OnAH: 16, Inv: 0, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
	})
	if d.Action != "classic_price_down_ah" {
		t.Fatalf("want AH down got %+v", d)
	}
}

func TestClassicDownBuyExcess(t *testing.T) {
	// sales=5 → rule2 needs sales<N false; buys=11 > 10, stock=6 > 5
	d := classicDecide(classicInput{
		Sales: 5, Buys: 11, OnAH: 3, Inv: 3, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
	})
	if d.Action != "classic_price_down_buys" {
		t.Fatalf("want buy-excess down got %+v", d)
	}
}

func TestClassicLeaderOversupply(t *testing.T) {
	// sales=5 = N so rule1/2 off; buys low; stock=20 > 5*3.5=17.5
	d := classicDecide(classicInput{
		Sales: 5, Buys: 0, OnAH: 10, Inv: 10, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
		IsLeader: true,
	})
	if d.Action != "classic_price_down_leader" {
		t.Fatalf("want leader down got %+v", d)
	}
}

func TestClassicNonLeaderNoOversupplyDown(t *testing.T) {
	d := classicDecide(classicInput{
		Sales: 5, Buys: 0, OnAH: 10, Inv: 10, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, PriceFloor: 100_000,
		IsLeader: false,
	})
	if strings.Contains(d.Action, "price_down") {
		t.Fatalf("non-leader must not dump: %+v", d)
	}
}

func TestCapitalPolicyClassic(t *testing.T) {
	if capitalPolicy != capitalPolicyClassic {
		t.Fatalf("active=%s want %s", capitalPolicy, capitalPolicyClassic)
	}
}
