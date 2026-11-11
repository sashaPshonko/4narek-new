package main

import "testing"

func TestCeilFromNotes(t *testing.T) {
	cases := map[string]int{
		"pridumalnahuy reason=x floor(p5)=700000 mid(p40)=1500000 frozen=false":   1500000,
		"pridumalnahuy reason=x пол(p5)=700000 потолок(p40)=1632000 frozen=false": 1632000,
		"нет потолка в этой записи":                                               0,
		"потолок(p15)=0": 0,
	}
	for in, want := range cases {
		if got := ceilFromNotes(in); got != want {
			t.Fatalf("%q: got %d want %d", in, got, want)
		}
	}
}
