package main

import (
	"encoding/json"
	"testing"
)

func lmBot(name string, bal float64, items string, live bool) orchBotNick {
	b := orchBotNick{Username: name, Anarchy: 510, Live: live, OnAnarchy: live}
	b.Balance = &bal
	if items != "" {
		b.Items = json.RawMessage(items)
	}
	return b
}

func TestLowMoneyAssess(t *testing.T) {
	prices := map[string]int{"sword7-1.21": 1_800_000}

	// новые аккаунты: у всех 0, товара нет → надо закинуть 45кк (15кк × 3)
	v, low := lowMoneyAssess(510, []orchBotNick{lmBot("a", 0, "", true), lmBot("b", 0, "", true), lmBot("c", 0, "", false)}, prices, 1)
	if !low || v.TopUpKK != 45 || len(v.PayTo) != 2 {
		t.Fatalf("новые: low=%v topup=%v payTo=%v", low, v.TopUpKK, v.PayTo)
	}

	// один бедный рядом с богатыми — оркестратор выровняет сам
	if _, low := lowMoneyAssess(502, []orchBotNick{lmBot("a", 2_000_000, "", true), lmBot("b", 60_000_000, "", true), lmBot("c", 20_000_000, "", true)}, prices, 1); low {
		t.Fatal("выравнивание должно справиться")
	}

	// денег мало, но на АХ много товара — вернётся сам
	if _, low := lowMoneyAssess(504, []orchBotNick{lmBot("a", 5_000_000, `{"sword7-1.21":20}`, true), lmBot("b", 5_000_000, "", true)}, prices, 1); low {
		t.Fatal("товар продастся — не повод заходить")
	}

	// забаненный не считается
	ban := lmBot("x", 0, "", false)
	ban.Banned = true
	if _, low := lowMoneyAssess(506, []orchBotNick{lmBot("a", 20_000_000, "", true), ban}, prices, 1); low {
		t.Fatal("забаненный бот не должен тянуть порог")
	}

	// инвиз-анархия: пороги в 3 раза ниже (keep 5кк)
	if _, low := lowMoneyAssess(509, []orchBotNick{lmBot("a", 6_000_000, "", true)}, prices, 1.0/3); low {
		t.Fatal("6кк на инвиз-боте — норма")
	}
	v, low = lowMoneyAssess(509, []orchBotNick{lmBot("a", 1_000_000, "", true)}, prices, 1.0/3)
	if !low || v.TopUpKK < 4 || v.TopUpKK > 5.01 {
		t.Fatalf("инвиз 1кк: low=%v topup=%v", low, v.TopUpKK)
	}

	// балансы ещё не пришли — не показываем
	nb := orchBotNick{Username: "a", Anarchy: 503, Live: true}
	if _, low := lowMoneyAssess(503, []orchBotNick{nb}, prices, 1); low {
		t.Fatal("без данных о балансе не показываем")
	}
}
