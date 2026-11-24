package main

// Настройки FunAuth, которые меняются в панели: сколько ботов фермы можно привязать к одному TG-аккаунту.
// По умолчанию 1 (один бот — один TG). Максимум 10: при 10 один TG обслуживает до 10 ботов.

import (
	"encoding/json"
	"log"
	"os"
)

const (
	funauthSettingsFile  = "funauth_settings.json"
	funauthMaxPerAccount = 10
)

type funauthSettings struct {
	PerAccount int `json:"perAccount"`
}

func clampPerAccount(n int) int {
	if n < 1 {
		return 1
	}
	if n > funauthMaxPerAccount {
		return funauthMaxPerAccount
	}
	return n
}

// perAccountLocked — под p.mu.
func (p *funauthPool) perAccountLocked() int {
	return clampPerAccount(p.perAccount)
}

func (p *funauthPool) perAccountLimit() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.perAccountLocked()
}

func (p *funauthPool) loadSettings() {
	raw, err := os.ReadFile(funauthSettingsFile)
	if err != nil {
		return
	}
	var s funauthSettings
	if json.Unmarshal(raw, &s) != nil {
		log.Printf("[funauth] %s: не разобрал, лимит 1", funauthSettingsFile)
		return
	}
	p.mu.Lock()
	p.perAccount = clampPerAccount(s.PerAccount)
	p.mu.Unlock()
	log.Printf("[funauth] ботов на один TG: %d", p.perAccountLimit())
}

// setPerAccount меняет лимит, сохраняет его и пересчитывает, какие TG закрыты.
func (p *funauthPool) setPerAccount(n int) int {
	n = clampPerAccount(n)
	p.mu.Lock()
	p.perAccount = n
	p.mu.Unlock()
	raw, _ := json.MarshalIndent(funauthSettings{PerAccount: n}, "", "  ")
	if err := os.WriteFile(funauthSettingsFile, raw, 0o644); err != nil {
		log.Printf("[funauth] не записал %s: %v", funauthSettingsFile, err)
	}
	log.Printf("[funauth] ботов на один TG: %d", n)
	p.syncAllRosterFull()
	return n
}
