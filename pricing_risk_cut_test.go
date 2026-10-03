package main

import "testing"

func TestApplyRiskCut(t *testing.T) {
	cfg := ItemConfig{RiskCut: 200_000}
	if got := applyRiskCut(4_000_000, cfg); got != 3_800_000 {
		t.Fatalf("mid-cut: got %d", got)
	}
	if got := applyRiskCut(100_000, cfg); got != 1 {
		t.Fatalf("floor clamp: got %d", got)
	}
	if got := applyRiskCut(4_000_000, ItemConfig{}); got != 4_000_000 {
		t.Fatalf("zero cut: got %d", got)
	}
}
