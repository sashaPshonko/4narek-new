package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestSuppressEmptyIdlePriceRecovery_TreasuryEmpty(t *testing.T) {
	cfg := ItemConfig{Name: "netherite_sword", Type: "netherite_sword-1.21"}

	mutex.Lock()
	prev := clientTreasuryEmptyTypes
	clientTreasuryEmptyTypes = make(map[*websocket.Conn]map[string]struct{})
	defer func() {
		clientTreasuryEmptyTypes = prev
		mutex.Unlock()
	}()

	if suppressEmptyIdlePriceRecoveryLocked(cfg, 0, 0, 0) {
		t.Fatal("REAL_EMPTY without treasury flag must not suppress")
	}

	// nil conn key is fine for unit test — only type set matters
	setClientTreasuryEmptyTypes(nil, []string{"netherite_sword-1.21"})
	if !suppressEmptyIdlePriceRecoveryLocked(cfg, 0, 0, 0) {
		t.Fatal("held=0 + treasury_empty_types must suppress floor_escape recovery")
	}
	if suppressEmptyIdlePriceRecoveryLocked(cfg, 1, 0, 0) {
		t.Fatal("held>0 must not suppress even with treasury flag")
	}
	if suppressEmptyIdlePriceRecoveryLocked(cfg, 0, 1, 0) {
		t.Fatal("sales>0 is not empty_idle — must not suppress via this path")
	}
	clearClientTreasuryEmptyTypes(nil)
	if suppressEmptyIdlePriceRecoveryLocked(cfg, 0, 0, 0) {
		t.Fatal("after clear, REAL_EMPTY again")
	}
}

func TestItemConfigTreasuryEmptyInactive_LegacyArmor(t *testing.T) {
	inactive := map[string]struct{}{"netherite_helmet-1.21": {}}
	cfg := ItemConfig{Name: "netherite_helmet", Type: netheriteArmorGoType}
	if !itemConfigTreasuryEmptyInactiveIn(inactive, cfg) {
		t.Fatal("legacy armor cfg should match piece treasury_empty type")
	}
}
