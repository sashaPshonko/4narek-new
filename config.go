package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

const itemsConfigPath = "items_config.json"

type itemEffectJSON struct {
	Name string `json:"name"`
	Lvl  int    `json:"lvl"`
}

type itemConfigJSON struct {
	Name            string           `json:"name"`
	Type            string           `json:"type"`
	MinecraftType   string           `json:"minecraft_type"` // legacy
	BasePrice       int              `json:"base_price"`
	NormalSales     int              `json:"normal_sales"`
	NormalCount     int              `json:"normal_count"`
	PriceStep       int              `json:"price_step"`
	AnalysisMinutes int              `json:"analysis_minutes"`
	Nacenka         int              `json:"nacenka"`
	Num             int              `json:"num"`
	Effects         []itemEffectJSON `json:"effects"`
	LoreMatch       string           `json:"lore_match,omitempty"`
}

func loadItemsConfig() error {
	raw, err := os.ReadFile(itemsConfigPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", itemsConfigPath, err)
	}

	var file map[string]itemConfigJSON
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse %s: %w", itemsConfigPath, err)
	}

	itemsConfig = make(map[string]ItemConfig, len(file))
	for id, entry := range file {
		itemType := entry.Type
		if itemType == "" {
			itemType = entry.MinecraftType
		}
		if itemType == "" {
			return fmt.Errorf("%s: item %q has no type", itemsConfigPath, id)
		}

		minutes := entry.AnalysisMinutes
		if minutes <= 0 {
			minutes = 10
		}

		effects := make([]ItemEffect, len(entry.Effects))
		for i, e := range entry.Effects {
			effects[i] = ItemEffect{Name: e.Name, Lvl: e.Lvl}
		}

		itemsConfig[id] = ItemConfig{
			ID:           id,
			Name:         entry.Name,
			Type:         itemType,
			BasePrice:    entry.BasePrice,
			NormalSales:  entry.NormalSales,
			NormalCount:  entry.NormalCount,
			PriceStep:    entry.PriceStep,
			AnalysisTime: time.Duration(minutes) * time.Minute,
			Nacenka:      entry.Nacenka,
			Num:          entry.Num,
			Effects:      effects,
			LoreMatch:    entry.LoreMatch,
		}
	}

	log.Printf("Загружено %d предметов из %s", len(itemsConfig), itemsConfigPath)
	return nil
}
