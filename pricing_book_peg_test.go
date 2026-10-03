package main

import "testing"

func TestBookPegDecide(t *testing.T) {
	d := bookPegDecide(4_000_000, 3_800_000, true)
	if d.Action != "book_peg_price_down" || d.NewPrice != 3_800_000 {
		t.Fatalf("down: %+v", d)
	}
	d = bookPegDecide(3_500_000, 3_800_000, true)
	if d.Action != "book_peg_price_up" || d.NewPrice != 3_800_000 {
		t.Fatalf("up: %+v", d)
	}
	d = bookPegDecide(3_800_000, 3_800_000, true)
	if d.Action != "book_peg_hold" {
		t.Fatalf("hold: %+v", d)
	}
	d = bookPegDecide(4_000_000, 0, false)
	if d.Action != "book_peg_hold_thin" || d.NewPrice != 4_000_000 {
		t.Fatalf("thin: %+v", d)
	}
}

func TestIsBookPegConfig(t *testing.T) {
	if !isBookPegConfig(ItemConfig{PriceMode: "book_peg"}) {
		t.Fatal("expected book_peg")
	}
	if isBookPegConfig(ItemConfig{}) {
		t.Fatal("empty mode")
	}
}
