package main

import "testing"

func TestTopupTargetsFrom(t *testing.T) {
	views := []lowMoneyAnarchyView{
		{Anarchy: 503, Treasurer: "t503", NeedKK: 45, CoinsKK: 10},
		{Anarchy: 507, Treasurer: "", NeedKK: 45, CoinsKK: 0}, // казначей неизвестен/не онлайн — пропускаем
		{Anarchy: 508, Treasurer: "t508", NeedKK: 45, CoinsKK: 2},
	}
	got := topupTargetsFrom(views)
	if len(got) != 2 {
		t.Fatalf("ожидали 2 цели, получили %d: %+v", len(got), got)
	}
	// самая бедная первой
	if got[0].Anarchy != 508 || got[0].To != "t508" || got[1].Anarchy != 503 {
		t.Fatalf("порядок: %+v", got)
	}
}

func TestClampTopupKK(t *testing.T) {
	if clampTopupKK(50) != 50 || clampTopupKK(1) != topupMinKK || clampTopupKK(9999) != topupMaxKK {
		t.Fatal("clamp")
	}
}
