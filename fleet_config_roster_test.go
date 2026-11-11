package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestConfigRosterFiltersChecksAndBans(t *testing.T) {
	ws := new(websocket.Conn)
	t.Cleanup(func() { deleteClientConfigBots(ws) })

	bots := []orchBotNick{{Username: "Alpha", Anarchy: float64(504)}}
	setClientConfigBots(ws, &bots, 504)
	r := currentConfigRoster()

	if !rosterAllowsNick(r, "alpha", 504) {
		t.Fatal("ник из JSON должен быть виден")
	}
	if rosterAllowsNick(r, "beta", 504) {
		t.Fatal("ника нет в JSON — должен прятаться")
	}
	if !rosterAllowsNick(r, "gamma", 507) {
		t.Fatal("анарка без списка от орка не фильтруется")
	}

	checks := []staffCheckView{
		{ID: "1", Username: "alpha", Anarchy: 504},
		{ID: "2", Username: "beta", Anarchy: 504},
	}
	got := filterStaffChecksByRoster(checks, r)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("проверки: %+v", got)
	}

	// JSON обнулили: анарка известна, ников нет — всё прячется.
	empty := []orchBotNick{}
	setClientConfigBots(ws, &empty, 504)
	if rosterAllowsNick(currentConfigRoster(), "alpha", 504) {
		t.Fatal("после очистки JSON ник должен пропасть")
	}

	// орк не смог прочитать файл (nil) — список не трогаем.
	setClientConfigBots(ws, &bots, 504)
	setClientConfigBots(ws, nil, 504)
	if !rosterAllowsNick(currentConfigRoster(), "alpha", 504) {
		t.Fatal("nil не должен стирать прошлый список")
	}

	deleteClientConfigBots(ws)
	if currentConfigRoster() != nil {
		t.Fatal("после отключения орка список пуст — фильтр выключен")
	}
}
