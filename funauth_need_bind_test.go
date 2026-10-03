package main

import "testing"

func TestFunauthNeedBindSurvivesVerified(t *testing.T) {
	funauthNeedBindMu.Lock()
	funauthNeedBind = make(map[string]funauthNeedBindRow)
	funauthNeedBindMu.Unlock()

	funauthMarkNeedBind("Zelen_uTishiny", 504)
	if !funauthHasNeedBind("zelen_utishiny") {
		t.Fatal("expected need bind after mark")
	}
	// verified must not wipe the UI flag when FunTime already asked for /tg
	handleFunauthVerifiedWS("Zelen_uTishiny", 504)
	if !funauthHasNeedBind("zelen_uTishiny") {
		t.Fatal("verified cleared needsBind — UI would hide unbound nick")
	}
	funauthClearNeedBind("zelen_uTishiny")
	if funauthHasNeedBind("zelen_uTishiny") {
		t.Fatal("clear should remove need bind")
	}
}
