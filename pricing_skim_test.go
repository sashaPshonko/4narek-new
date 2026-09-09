package main

import "testing"

func TestCorridorSkimDisabledInV8s(t *testing.T) {
	if corridorSkimEnabled {
		t.Fatal("v8s: corridorSkimEnabled must be false (up_skim → hold_skim_disabled)")
	}
}

func TestSkimSalesLeadOK(t *testing.T) {
	// Сегодняшний кейс 12 vs 7 — lead=5 ≥ 3 → сигнал lead ещё детектится (для veto/логов).
	if !skimSalesLeadOK(12, 7) {
		t.Fatal("12≥7+3 must allow skim")
	}
	// Слабый разбор: sales=5 buys=4 → lead=1 < 3.
	if skimSalesLeadOK(5, 4) {
		t.Fatal("5<4+3 must block skim")
	}
	if skimSalesLeadOK(3, 3) {
		t.Fatal("equal sales/buys must block skim")
	}
}

func TestTrySellsBlockSkim(t *testing.T) {
	// 7 try при 12 sell — старый veto (2×sales) не блокировал; skim-veto тоже (нужно ≥12).
	if trySellsBlockSkim(12, 7) {
		t.Fatal("7 try / 12 sales must not block skim")
	}
	if !trySellsBlockSkim(12, 12) {
		t.Fatal("try≥sales must block skim")
	}
	// После дампа: 4 sell / 20 try.
	if !trySellsBlockSkim(4, 20) {
		t.Fatal("20 try / 4 sales must block skim")
	}
	if trySellsBlockSkim(10, 4) {
		t.Fatal("try < minTries must not block")
	}
}

func TestSkimShouldRevert(t *testing.T) {
	// Цикл B после неудачного skim: sales=4 buys=11 try=20, up_cd>0.
	if !skimShouldRevert(2, 4, 11, 20) {
		t.Fatal("after ↑ with buys>sales and try≥5 → revert")
	}
	if !skimShouldRevert(1, 4, 4, 5) {
		t.Fatal("sales<=buys + try≥min → revert")
	}
	// try-veto path (sales=5 try=10 → 10≥2*5).
	if !skimShouldRevert(1, 5, 2, 10) {
		t.Fatal("trySellsBlockUp must trigger revert")
	}
	if skimShouldRevert(0, 4, 11, 20) {
		t.Fatal("no up_cd → no revert")
	}
	// После ↑ рынок всё ещё берёт — не откатываем.
	if skimShouldRevert(2, 12, 7, 7) {
		t.Fatal("healthy sales>buys without try-veto → no revert")
	}
}
