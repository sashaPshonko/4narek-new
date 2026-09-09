package main

import (
	"log"
	"strings"
	"time"
)

// Trusted AH-min discovery: held=0 && buys=0 && our ≪ trusted_AH_min → price = trusted_AH_min.
// Jump only up. No p10, no nacenka, no κ. Uses ah_book_lots + seller bans (same bot scan).
// Live winner: corridor_price_up_trusted_ah_min. Shadow table still logs diagnostics.

const (
	trustedMinDiscoveryLiveEnabled = true
	trustedMinDiscoveryActionLive  = "corridor_price_up_trusted_ah_min"
	trustedMinDiscoveryActionShadow = "corridor_price_up_trusted_ah_min_shadow"

	trustedMinDiscoveryMinSellers = 15 // independent sellers in window
	trustedMinDiscoveryMinNear    = 3  // sellers confirming the min band
	trustedMinDiscoveryNearSteps  = 2  // near = min + 2×step

	// Deep pit vs trusted min (same numbers as market_recovery, reference = trusted min not p10).
	trustedMinDiscoveryRatioMax    = 0.80 // our/min ≤ 0.80
	trustedMinDiscoveryGapMinSteps = 4    // (min−our)/step ≥ 4
)

func initTrustedMinDiscoveryShadowTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS trusted_min_discovery_shadow (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	action TEXT NOT NULL,
	our_price INTEGER NOT NULL,
	trusted_ah_min INTEGER NOT NULL,
	unique_sellers INTEGER NOT NULL,
	sellers_near_min INTEGER NOT NULL,
	uuid_noban INTEGER NOT NULL,
	step INTEGER NOT NULL,
	gap_abs INTEGER NOT NULL,
	gap_steps REAL NOT NULL,
	gap_pct REAL NOT NULL,
	would_fire INTEGER NOT NULL,
	would_price INTEGER NOT NULL,
	trust_ok INTEGER NOT NULL,
	skip_reason TEXT,
	held INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	sales INTEGER NOT NULL,
	winner_action TEXT,
	notes TEXT
)`)
	if err != nil {
		log.Printf("[trusted_min_discovery_shadow] schema: %v", err)
		return
	}
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_ts ON trusted_min_discovery_shadow(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_item_ts ON trusted_min_discovery_shadow(item_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tmds_fire ON trusted_min_discovery_shadow(would_fire, ts)`)
}

type trustedMinDiscoveryEval struct {
	WouldFire      bool
	WouldPrice     int
	TrustedMin     int
	UniqueSellers  int
	SellersNearMin int
	NUUID          int
	GapAbs         int
	GapSteps       float64
	GapPct         float64
	TrustOK        bool
	SkipReason     string
}

// trustedMinDiscoveryGapOK — глубокая яма относительно trusted AH min (не p10).
func trustedMinDiscoveryGapOK(our, trustedMin, step int) bool {
	if our <= 0 || trustedMin <= 0 || step <= 0 {
		return false
	}
	if float64(our)/float64(trustedMin) <= trustedMinDiscoveryRatioMax {
		return true
	}
	gapSteps := float64(trustedMin-our) / float64(step)
	return gapSteps >= float64(trustedMinDiscoveryGapMinSteps)
}

// evalTrustedMinDiscovery — pure decision. Target = trusted seller min (no nacenka). Only raises.
func evalTrustedMinDiscovery(our, step, held, buys int, book ahBookTrustedSellerMinSnap, manualLock bool) trustedMinDiscoveryEval {
	out := trustedMinDiscoveryEval{
		TrustedMin:     book.TrustedMin,
		UniqueSellers:  book.UniqueSellers,
		SellersNearMin: book.SellersNearMin,
		NUUID:          book.NUUID,
	}
	if manualLock {
		out.SkipReason = "manual_lock"
		return out
	}
	if held != 0 {
		out.SkipReason = "held_not_empty"
		return out
	}
	if buys != 0 {
		out.SkipReason = "had_buys"
		return out
	}
	if our <= 0 || step <= 0 {
		out.SkipReason = "bad_our_or_step"
		return out
	}
	if !book.OK || book.TrustedMin <= 0 {
		out.SkipReason = "no_book"
		return out
	}
	if book.UniqueSellers < trustedMinDiscoveryMinSellers {
		out.SkipReason = "thin_sellers"
		return out
	}
	if book.SellersNearMin < trustedMinDiscoveryMinNear {
		out.SkipReason = "min_unconfirmed"
		return out
	}
	out.TrustOK = true
	out.GapAbs = book.TrustedMin - our
	out.GapSteps = float64(out.GapAbs) / float64(step)
	out.GapPct = float64(out.GapAbs) / float64(our) * 100

	if !(book.TrustedMin > our+step) {
		if our >= book.TrustedMin {
			out.SkipReason = "our_at_or_above_min"
		} else {
			out.SkipReason = "gap_too_small"
		}
		return out
	}
	if !trustedMinDiscoveryGapOK(our, book.TrustedMin, step) {
		out.SkipReason = "gap_not_deep"
		return out
	}
	out.WouldFire = true
	out.WouldPrice = book.TrustedMin
	return out
}

// runTrustedMinDiscoveryShadow — diagnostic log; never mutates price. Live path is in adjustPrice.
func runTrustedMinDiscoveryShadow(item string, now time.Time, ourPrice, step, held, buys, sales int, winnerAction string, manualLock bool) {
	if strings.TrimSpace(item) == "" || ourPrice <= 0 || step <= 0 {
		return
	}
	book := ahBookTrustedSellerMin(item, now.Add(-ahBookRaiseWindow), trustedMinDiscoveryNearSteps, step)
	ev := evalTrustedMinDiscovery(ourPrice, step, held, buys, book, manualLock)

	if mlDB == nil {
		return
	}
	fire := 0
	trust := 0
	if ev.WouldFire {
		fire = 1
	}
	if ev.TrustOK {
		trust = 1
	}
	notes := "diag; live=corridor_price_up_trusted_ah_min; target=trusted_seller_min no_nacenka"

	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
INSERT INTO trusted_min_discovery_shadow (
	ts, item_id, action, our_price, trusted_ah_min, unique_sellers, sellers_near_min, uuid_noban,
	step, gap_abs, gap_steps, gap_pct, would_fire, would_price, trust_ok, skip_reason,
	held, buys, sales, winner_action, notes
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		now.UTC().Format(time.RFC3339),
		item,
		trustedMinDiscoveryActionShadow,
		ourPrice,
		ev.TrustedMin,
		ev.UniqueSellers,
		ev.SellersNearMin,
		ev.NUUID,
		step,
		ev.GapAbs,
		ev.GapSteps,
		ev.GapPct,
		fire,
		ev.WouldPrice,
		trust,
		ev.SkipReason,
		held, buys, sales,
		winnerAction,
		notes,
	)
	if err != nil {
		log.Printf("[trusted_min_discovery_shadow] insert %s: %v", item, err)
		return
	}
	if ev.WouldFire && winnerAction != trustedMinDiscoveryActionLive {
		log.Printf("[trusted_min_discovery_shadow] WOULD %s our=%d → min=%d sellers=%d near=%d gap_steps=%.1f gap_pct=%.1f winner=%s",
			item, ourPrice, ev.TrustedMin, ev.UniqueSellers, ev.SellersNearMin, ev.GapSteps, ev.GapPct, winnerAction)
	}
}
