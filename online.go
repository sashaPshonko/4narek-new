package main

import (
	"encoding/json"
	"log"
	"os"
	"time"

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
	type pingResult struct {
		json []byte
		err  error
	}
	ch := make(chan pingResult, 1)
	go func() {
		j, _, err := bot.PingAndList(mcServerAddr())
		ch <- pingResult{j, err}
	}()
	var respJSON []byte
	var err error
	select {
	case r := <-ch:
		respJSON, err = r.json, r.err
	case <-time.After(3 * time.Second):
		log.Printf("[online] ping %s: timeout 3s", mcServerAddr())
		return playersOnline, playersMax
	}
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
