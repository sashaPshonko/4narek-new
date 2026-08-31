package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestMergeClientOrchBots(t *testing.T) {
	old := clientOrchBots
	clientOrchBots = make(map[*websocket.Conn][]orchBotNick)
	t.Cleanup(func() { clientOrchBots = old })

	wsA := &websocket.Conn{}
	wsB := &websocket.Conn{}
	clientOrchBots[wsA] = []orchBotNick{
		{Username: "semaPila14", Anarchy: 504},
		{Username: "plavMost9", Anarchy: 504},
	}
	clientOrchBots[wsB] = []orchBotNick{
		{Username: "shayb5Ok3", Anarchy: 506},
	}
	r := mergeClientOrchBotsLocked()
	if !r.nickOnAnarchy(504, "semaPila14") || !r.nickOnAnarchy(504, "plavMost9") {
		t.Fatalf("504: %+v", r)
	}
	if !r.nickOnAnarchy(506, "shayb5Ok3") {
		t.Fatalf("506: %+v", r)
	}
	if r.nickOnAnarchy(502, "semaPila14") {
		t.Fatal("nick leaked to other anarchy")
	}
}
