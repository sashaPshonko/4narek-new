package main

import (
	"strings"

	"github.com/gorilla/websocket"
)

// clientTreasuryEmptyTypes — goType'ы, у которых на оркестраторе есть боты
// с presence inactive из‑за пустой казны (слоты вычищены, но воркер жив).
// Отличает TREASURY_EMPTY_INACTIVE от REAL_EMPTY для floor_escape / empty recovery.
var clientTreasuryEmptyTypes = make(map[*websocket.Conn]map[string]struct{})

func setClientTreasuryEmptyTypes(ws *websocket.Conn, types []string) {
	m := make(map[string]struct{}, len(types))
	for _, t := range types {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		m[t] = struct{}{}
	}
	clientTreasuryEmptyTypes[ws] = m
}

func clearClientTreasuryEmptyTypes(ws *websocket.Conn) {
	delete(clientTreasuryEmptyTypes, ws)
}

// catalogTypeTreasuryEmptyInactiveIn — тип помечен орком как treasury_empty inactive.
func catalogTypeTreasuryEmptyInactiveIn(inactive map[string]struct{}, catalogType string) bool {
	if _, ok := inactive[catalogType]; ok {
		return true
	}
	// legacy armor / piece — как в itemConfigActiveIn
	if catalogType == netheriteArmorGoType {
		for goType := range pieceGoTypeToName {
			if _, ok := inactive[goType]; ok {
				return true
			}
		}
		return false
	}
	if _, isPiece := pieceGoTypeToName[catalogType]; isPiece {
		if _, ok := inactive[netheriteArmorGoType]; ok {
			return true
		}
	}
	return false
}

func itemConfigTreasuryEmptyInactiveIn(inactive map[string]struct{}, cfg ItemConfig) bool {
	if catalogTypeTreasuryEmptyInactiveIn(inactive, cfg.Type) {
		return true
	}
	if cfg.Type == netheriteArmorGoType {
		for goType, name := range pieceGoTypeToName {
			if cfg.Name != name {
				continue
			}
			if _, ok := inactive[goType]; ok {
				return true
			}
		}
	}
	return false
}

// typeHasTreasuryEmptyInactiveLocked — любой орк сообщил treasury_empty inactive по типу item.
// Только под mutex.
func typeHasTreasuryEmptyInactiveLocked(cfg ItemConfig) bool {
	for _, inactive := range clientTreasuryEmptyTypes {
		if itemConfigTreasuryEmptyInactiveIn(inactive, cfg) {
			return true
		}
	}
	return false
}

// suppressEmptyIdlePriceRecoveryLocked — held=0 из‑за вычищенных treasury_empty слотов:
// не считать это доказательством «цена слишком низкая», не качать floor_escape / trusted jump / market_recovery.
// isEmptyIdle и запрет ↓ не меняем.
func suppressEmptyIdlePriceRecoveryLocked(cfg ItemConfig, held, sales, buys int) bool {
	if !isEmptyIdle(held, sales, buys) {
		return false
	}
	return typeHasTreasuryEmptyInactiveLocked(cfg)
}
