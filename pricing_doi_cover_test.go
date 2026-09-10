package main

import "testing"

func TestDoiCoverIntensityBoundaries(t *testing.T) {
	// Exact boundaries from Policy F (sales≥1).
	cases := []struct {
		held, sales int
		want        string
		doiApprox   float64
	}{
		{299, 100, "hold", 2.99}, // DOI=2.99 → HOLD
		{3, 1, "over", 3.00},     // DOI=3.00 → OVER −2
		{499, 100, "over", 4.99}, // DOI=4.99 → OVER −2
		{5, 1, "soft", 5.00},     // DOI=5.00 → SOFT −1
		{999, 100, "soft", 9.99}, // DOI=9.99 → SOFT −1
		{10, 1, "hold", 10.00},   // DOI=10.00 → HOLD
		{20, 1, "hold", 20.00},
		{2, 1, "hold", 2.00},
		{4, 1, "over", 4.00},
		{9, 1, "soft", 9.00},
	}
	for _, tc := range cases {
		got := doiCoverIntensity(tc.held, tc.sales)
		doi := doiCover(tc.held, tc.sales)
		if got != tc.want {
			t.Fatalf("held=%d sales=%d DOI=%.4f: inten=%q want %q", tc.held, tc.sales, doi, got, tc.want)
		}
		if doi < tc.doiApprox-1e-9 || doi > tc.doiApprox+1e-9 {
			// allow float representation of 299/100 etc.
			if absFloat(doi-tc.doiApprox) > 1e-9 {
				t.Fatalf("DOI calc held=%d sales=%d got %.10f want ~%.2f", tc.held, tc.sales, doi, tc.doiApprox)
			}
		}
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestDoiCoverIntensitySalesZeroNAforF(t *testing.T) {
	if got := doiCoverIntensity(10, 0); got != "" {
		t.Fatalf("sales=0 must be N/A for F intensity, got %q", got)
	}
	if doiCover(10, 0) != 0 {
		t.Fatal("doiCover sales=0 → 0")
	}
}

func TestDoiCoverStepMapping(t *testing.T) {
	// Document actuator mapping: over → HardDownStepMult, soft → 1.
	if corridorHardDownStepMult != 2 {
		t.Fatalf("OVER must remain −%d×step", corridorHardDownStepMult)
	}
	cases := map[string]int{
		"over": corridorHardDownStepMult,
		"soft": 1,
		"hold": 0,
	}
	for inten, steps := range cases {
		_ = inten
		_ = steps
	}
	if doiCoverIntensity(3, 1) != "over" || corridorHardDownStepMult != 2 {
		t.Fatal("DOI=3 → over → −2 step")
	}
	if doiCoverIntensity(5, 1) != "soft" {
		t.Fatal("DOI=5 → soft → −1 step")
	}
}

func TestCapitalPolicyV8u(t *testing.T) {
	if capitalPolicy != "stock_corridor_v8x" {
		t.Fatalf("policy=%s want stock_corridor_v8x", capitalPolicy)
	}
}
