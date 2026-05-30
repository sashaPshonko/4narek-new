package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/Tnze/go-mc/bot"
)

// mcServerAddr — host:port для Server List Ping (как mcstatus JavaServer.lookup).
// Переопределение: MC_SERVER_ADDR=mc.funtime.su:25565
func mcServerAddr() string {
	if a := os.Getenv("MC_SERVER_ADDR"); a != "" {
		return a
	}
	return "mc.funtime.su:25565"
}

func fetchOnlineSnapshot() (playersOnline, playersMax int) {
	playersOnline, playersMax = -1, -1
	respJSON, _, err := bot.PingAndList(mcServerAddr())
	if err != nil {
		log.Printf("[online] ping %s: %v", mcServerAddr(), err)
		return playersOnline, playersMax
	}
	var status struct {
		Players struct {
			Online int `json:"online"`
			Max    int `json:"max"`
		} `json:"players"`
	}
	if err := json.Unmarshal(respJSON, &status); err != nil {
		log.Printf("[online] parse: %v", err)
		return playersOnline, playersMax
	}
	return status.Players.Online, status.Players.Max
}

func getOnlineCount() int {
	online, _ := fetchOnlineSnapshot()
	return online
}
