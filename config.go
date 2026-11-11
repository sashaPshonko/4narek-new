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
	Name              string           `json:"name"`
	Type              string           `json:"type"`
	MinecraftType     string           `json:"minecraft_type"` // legacy
	BasePrice         int              `json:"base_price"`
	NormalSales       int              `json:"normal_sales"`
	NormalCount       int              `json:"normal_count"`
	PriceStep         int              `json:"price_step"`
	AnalysisMinutes   int              `json:"analysis_minutes"`
	Nacenka           int              `json:"nacenka"`
	NacenkaMin        int              `json:"nacenka_min"`
	Num               int              `json:"num"`
	Effects           []itemEffectJSON `json:"effects"`
	ForbiddenEffects  []itemEffectJSON `json:"forbidden_effects,omitempty"`
	MaxEffects        []itemEffectJSON `json:"max_effects,omitempty"`
	ExactEffects      bool             `json:"exact_effects,omitempty"`
	LoreMatch         string           `json:"lore_match,omitempty"`
	BookCeilQ         float64          `json:"book_ceil_q,omitempty"`
	BookMidQLegacy    float64          `json:"book_mid_q,omitempty"` // старое имя ключа, читаем для совместимости со старыми конфигами
	BookFloorQ        float64          `json:"book_floor_q,omitempty"`
	BookMinLots       int              `json:"book_min_lots,omitempty"`
	BookFloorMinLots  int              `json:"book_floor_min_lots,omitempty"`
	FloorJump         bool             `json:"floor_jump,omitempty"`
	NoBuy             bool             `json:"no_buy,omitempty"`
	BookCeilQNight    float64          `json:"book_ceil_q_night,omitempty"`
	RiskCut           int              `json:"risk_cut,omitempty"`
	PriceMode         string           `json:"price_mode,omitempty"`
	BookPegQ          float64          `json:"book_peg_q,omitempty"`
	BookPegMinSellers int              `json:"book_peg_min_sellers,omitempty"`
	BuyStopQ          float64          `json:"buy_stop_q,omitempty"`
	BuyStopMinSellers int              `json:"buy_stop_min_sellers,omitempty"`
	BuyStopHeld       int              `json:"buy_stop_held,omitempty"`
	HotPriceCapQ      float64          `json:"hot_price_cap_q,omitempty"`
	HotTetherQ        float64          `json:"hot_tether_q,omitempty"`
	Hot               bool             `json:"hot,omitempty"`
	HotDownSalesMax   int              `json:"hot_down_sales_max,omitempty"`
	HotDownStockMin   int              `json:"hot_down_stock_min,omitempty"`
	HotUpSalesMax     int              `json:"hot_up_sales_max,omitempty"`
	HotUpStockMax     int              `json:"hot_up_stock_max,omitempty"`
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
	itemsNacenkaBase = make(map[string]int, len(file))
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
		forbidden := make([]ItemEffect, len(entry.ForbiddenEffects))
		for i, e := range entry.ForbiddenEffects {
			forbidden[i] = ItemEffect{Name: e.Name, Lvl: e.Lvl}
		}
		maxEffects := make([]ItemEffect, len(entry.MaxEffects))
		for i, e := range entry.MaxEffects {
			maxEffects[i] = ItemEffect{Name: e.Name, Lvl: e.Lvl}
		}
		itemsConfig[id] = ItemConfig{
			ID:                id,
			Name:              entry.Name,
			Type:              itemType,
			BasePrice:         entry.BasePrice,
			NormalSales:       entry.NormalSales,
			NormalCount:       entry.NormalCount,
			PriceStep:         entry.PriceStep,
			AnalysisTime:      time.Duration(minutes) * time.Minute,
			Nacenka:           entry.Nacenka,
			NacenkaMin:        entry.NacenkaMin,
			Num:               entry.Num,
			Effects:           effects,
			ForbiddenEffects:  forbidden,
			MaxEffects:        maxEffects,
			ExactEffects:      entry.ExactEffects,
			LoreMatch:         entry.LoreMatch,
			BookCeilQ:         firstPositive(entry.BookCeilQ, entry.BookMidQLegacy),
			BookFloorQ:        entry.BookFloorQ,
			BookMinLots:       entry.BookMinLots,
			BookFloorMinLots:  entry.BookFloorMinLots,
			FloorJump:         entry.FloorJump,
			NoBuy:             entry.NoBuy,
			BookCeilQNight:    entry.BookCeilQNight,
			RiskCut:           entry.RiskCut,
			PriceMode:         entry.PriceMode,
			BookPegQ:          entry.BookPegQ,
			BookPegMinSellers: entry.BookPegMinSellers,
			BuyStopQ:          entry.BuyStopQ,
			BuyStopMinSellers: entry.BuyStopMinSellers,
			BuyStopHeld:       entry.BuyStopHeld,
			HotPriceCapQ:      entry.HotPriceCapQ,
			HotTetherQ:        entry.HotTetherQ,
			Hot:               entry.Hot,
			HotDownSalesMax:   entry.HotDownSalesMax,
			HotDownStockMin:   entry.HotDownStockMin,
			HotUpSalesMax:     entry.HotUpSalesMax,
			HotUpStockMax:     entry.HotUpStockMax,
		}
		itemsNacenkaBase[id] = entry.Nacenka
	}

	log.Printf("Загружено %d предметов из %s", len(itemsConfig), itemsConfigPath)
	return nil
}

// firstPositive — первое положительное значение (0, если таких нет).
func firstPositive(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
