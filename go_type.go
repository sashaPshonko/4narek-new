package main

import "math"

// legacy merged type — ещё матчим, если оркестратор шлёт netherite_armor-1.21
const netheriteArmorGoType = "netherite_armor-1.21"

var pieceGoTypeToName = map[string]string{
	"netherite_helmet-1.21":     "netherite_helmet",
	"netherite_chestplate-1.21": "netherite_chestplate",
	"netherite_leggings-1.21":   "netherite_leggings",
	"netherite_boots-1.21":      "netherite_boots",
}

func catalogTypeActiveIn(activeTypes map[string]struct{}, catalogType string) bool {
	if _, ok := activeTypes[catalogType]; ok {
		return true
	}
	return false
}

// botsForGoTypeLocked — боты на go-типе для share/ёмкости.
// Берём sticky (не схлопываем долю от разового вылета); сон по расписанию
// орк помечает scheduled_off — тогда sticky сразу = live; live=0 → 0.
// Merged armor и piece-типы считают вместе.
// mergedTypeTotal — значение для go-типа из карты totals с тем же слиянием брони и частей, что у ботов.
func mergedTypeTotal(totals map[string]int, goType string) int {
	n := totals[goType]
	if goType == netheriteArmorGoType {
		for piece := range pieceGoTypeToName {
			n += totals[piece]
		}
		return n
	}
	if _, isPiece := pieceGoTypeToName[goType]; isPiece {
		return n + totals[netheriteArmorGoType]
	}
	return n
}

// baseAHSlotsPerBot — слотов АХ у бота без аренды. С арендой у бота 10 (см. rent.rs в azalea).
const baseAHSlotsPerBot = 5

// slotsPerBotForGoTypeLocked — сколько слотов АХ в среднем у бота этого типа: 5, а доля арендованных поднимает
// среднее до 10. Нет данных от оркестратора (старая версия) — 5.
func slotsPerBotForGoTypeLocked(goType string) float64 {
	bots := mergedTypeTotal(aggregateBotsPerTypeLocked(), goType)
	slots := mergedTypeTotal(aggregateSlotsPerTypeLocked(), goType)
	if bots <= 0 || slots <= 0 {
		return baseAHSlotsPerBot
	}
	per := float64(slots) / float64(bots)
	if per < baseAHSlotsPerBot {
		return baseAHSlotsPerBot
	}
	return per
}

// slotsForGoTypeLocked — ёмкость АХ категории: боты (с тем же «липким» сглаживанием, что и везде) × средние
// слоты на бота. Раньше везде стояло «боты × 5»; теперь у ботов с арендой 10.
func slotsForGoTypeLocked(goType string) int {
	return int(math.Round(float64(botsForGoTypeLocked(goType)) * slotsPerBotForGoTypeLocked(goType)))
}

func botsForGoTypeLocked(goType string) int {
	totals := aggregateBotsPerTypeLocked()
	n := totals[goType]
	if goType == netheriteArmorGoType {
		for piece := range pieceGoTypeToName {
			n += totals[piece]
		}
		return stickyBotsForGoType(goType, n)
	}
	if _, isPiece := pieceGoTypeToName[goType]; isPiece {
		n = n + totals[netheriteArmorGoType]
		return stickyBotsForGoType(goType, n)
	}
	return stickyBotsForGoType(goType, n)
}

func itemConfigActiveIn(activeTypes map[string]struct{}, cfg ItemConfig) bool {
	if catalogTypeActiveIn(activeTypes, cfg.Type) {
		return true
	}
	// legacy: орк активировал piece-тип, а в каталоге ещё netherite_armor
	if cfg.Type == netheriteArmorGoType {
		for goType, name := range pieceGoTypeToName {
			if cfg.Name != name {
				continue
			}
			if _, ok := activeTypes[goType]; ok {
				return true
			}
		}
		return false
	}
	// legacy: орк активировал merged armor, а в каталоге уже piece-типы
	if _, ok := activeTypes[netheriteArmorGoType]; ok {
		if _, isPiece := pieceGoTypeToName[cfg.Type]; isPiece {
			return true
		}
	}
	return false
}

func itemConfigActiveLocked(cfg ItemConfig) bool {
	for _, types := range clientActiveTypes {
		if itemConfigActiveIn(types, cfg) {
			return true
		}
	}
	return false
}
