package main

import (
	"strings"
	"testing"
)

func TestClassicBookFloorJump(t *testing.T) {
	d := classicBookDecide(classicBookInput{
		Sales: 5, Buys: 0, OnAH: 2, Inv: 0, NormalSales: 5,
		Price: 1_000_000, Step: 100_000, ShareHi: 5,
		BookFloor: 2_000_000, BookFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if d.Action != "classic_book_floor_jump" || d.NewPrice != 2_000_000 {
		t.Fatalf("want floor jump %+v", d)
	}
}

func TestClassicBookMidJump(t *testing.T) {
	d := classicBookDecide(classicBookInput{
		Sales: 5, Buys: 0, OnAH: 2, Inv: 0, NormalSales: 5,
		Price: 4_000_000, Step: 100_000, ShareHi: 5,
		BookFloor: 2_000_000, BookFloorOK: true,
		BookMid: 3_000_000, BookMidOK: true,
	})
	if d.Action != "classic_book_mid_jump" || d.NewPrice != 3_000_000 {
		t.Fatalf("want mid jump %+v", d)
	}
}

func TestClassicBookUpEmptyStock(t *testing.T) {
	d := classicBookDecide(classicBookInput{
		Sales: 0, Buys: 0, OnAH: 0, Inv: 0, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, ShareHi: 5,
		BookMid: 5_000_000, BookMidOK: true,
	})
	if d.Action != "classic_book_price_up" || d.NewPrice != 2_100_000 {
		t.Fatalf("empty must UP: %+v", d)
	}
}

func TestClassicBookNoUpAboveMarket(t *testing.T) {
	d := classicBookDecide(classicBookInput{
		Sales: 0, Buys: 0, OnAH: 1, Inv: 0, NormalSales: 5,
		Price: 2_200_000, Step: 100_000, ShareHi: 5,
		P10: 2_000_000, P10OK: true,
		BookMid: 5_000_000, BookMidOK: true,
	})
	if d.Action != "classic_book_hold_above_market" {
		t.Fatalf("ratio≥1.05 must block UP: %+v", d)
	}
}

func TestClassicBookGrantBlocksDown(t *testing.T) {
	// sales≥N → no UP; stock=3 ≤ shareHi=5 → classic DOWN запрещён даже при buys excess
	d := classicBookDecide(classicBookInput{
		Sales: 5, Buys: 20, OnAH: 3, Inv: 0, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, ShareHi: 5,
		BookFloor: 1_000_000, BookFloorOK: true,
		BookMid: 5_000_000, BookMidOK: true,
	})
	if strings.Contains(d.Action, "price_down") {
		t.Fatalf("grant must block DOWN on thin stock: %+v", d)
	}
}

func TestClassicBookDownAHWhenFat(t *testing.T) {
	// stock=16 >= N*3 so no UP; shareHi=5 so DOWN allowed
	d := classicBookDecide(classicBookInput{
		Sales: 2, Buys: 0, OnAH: 16, Inv: 0, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, ShareHi: 5,
		BookFloor: 1_000_000, BookFloorOK: true,
		BookMid: 5_000_000, BookMidOK: true,
	})
	if d.Action != "classic_book_price_down_ah" {
		t.Fatalf("want AH down %+v", d)
	}
}

func TestClassicBookLeaderDown(t *testing.T) {
	d := classicBookDecide(classicBookInput{
		Sales: 5, Buys: 0, OnAH: 10, Inv: 10, NormalSales: 5,
		Price: 2_000_000, Step: 100_000, ShareHi: 5,
		BookFloor: 1_000_000, BookFloorOK: true,
		BookMid: 5_000_000, BookMidOK: true,
		IsLeader: true,
	})
	if d.Action != "classic_book_price_down_leader" {
		t.Fatalf("want leader down %+v", d)
	}
}

func TestClassicBookNoBuyFloor(t *testing.T) {
	// DOWN to book floor only — no buy10m
	d := classicBookDecide(classicBookInput{
		Sales: 2, Buys: 0, OnAH: 16, Inv: 0, NormalSales: 5,
		Price: 1_050_000, Step: 100_000, ShareHi: 5,
		BookFloor: 1_000_000, BookFloorOK: true,
		BookMid: 5_000_000, BookMidOK: true,
	})
	if d.NewPrice != 1_000_000 {
		t.Fatalf("clamp to book floor only %+v", d)
	}
}
