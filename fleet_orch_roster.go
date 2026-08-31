package main

import (
	"log"

	"github.com/gorilla/websocket"
)

type orchBotNick struct {
	Username string `json:"username"`
	Anarchy  any    `json:"anarchy"`
}

// clientOrchBots — список ников с оркестратора (fleet/presence), не funauth_roster.json.
var clientOrchBots = make(map[*websocket.Conn][]orchBotNick)

func mergeClientOrchBotsLocked() funauthRoster {
	out := make(funauthRoster)
	for _, rows := range clientOrchBots {
		for _, row := range rows {
			addFleetNick(out, anarchyInt(row.Anarchy), row.Username)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyLiveOrchRosterLocked() {
	r := mergeClientOrchBotsLocked()
	if len(r) == 0 {
		r = loadFleetRunningNicks()
	}
	n := 0
	for _, set := range r {
		n += len(set)
	}
	if rosterNickCount(fleetNickRoster) != n || len(fleetNickRoster) != len(r) {
		logOrchRoster(r, n)
	}
	fleetNickRoster = r
	if funauthPoolInst != nil {
		funauthPoolInst.replaceRoster(r)
	}
}

func rosterNickCount(r funauthRoster) int {
	n := 0
	for _, set := range r {
		n += len(set)
	}
	return n
}

func logOrchRoster(r funauthRoster, nicks int) {
	if len(r) == 0 {
		return
	}
	log.Printf("[funauth] roster from orchestrators: %d anarchy(ies), %d nicks", len(r), nicks)
}

func setClientOrchBots(ws *websocket.Conn, bots []orchBotNick) {
	if ws == nil {
		return
	}
	if len(bots) == 0 {
		delete(clientOrchBots, ws)
	} else {
		clientOrchBots[ws] = bots
	}
	applyLiveOrchRosterLocked()
}

func deleteClientOrchBots(ws *websocket.Conn) {
	if _, ok := clientOrchBots[ws]; !ok {
		return
	}
	delete(clientOrchBots, ws)
	applyLiveOrchRosterLocked()
}
