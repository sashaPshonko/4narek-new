package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Shadow-only LEVEL 1: compare capped discovery speeds on the SAME episodes.
// Does NOT change winner / production price / v8s rules.
//
// Arms (simultaneous virtual trajectories):
//   A virtual_cap1 = +1 step/cycle toward band
//   B virtual_cap3 = +3 step/cycle toward band
//   C virtual_cap5 = +5 step/cycle toward band
//
// Target is buyable/market band floor (0.85×trusted p10), NOT jump-to-p10 / not κ×p10.
// When virtual enters band → that arm stops (handoff to conceptual v8s).
// When real BUY or held appears → snapshot all three virtuals on that row (event_kind).

const (
	cappedDiscoveryBandFrac   = 0.85
	cappedDiscoveryOvershootF = 1.15
)

type cappedDiscoveryArm struct {
	Virtual        int
	Active         bool
	Cycles         int // discovery steps applied (cycles_since_arm for this arm)
	CyclesToBand   int // set once when entered_band
	StopReason     string
	ReachedBandAt  time.Time
	HitBand        bool
}

type cappedDiscoveryState struct {
	EpisodeID      string
	StartedAt      time.Time
	InitialOur     int
	InitialP10     int
	InitialRatio   float64
	Step           int
	TargetRef      int
	WallCycles     int // cycles_since_episode (shared)
	Arm1           cappedDiscoveryArm
	Arm3           cappedDiscoveryArm
	Arm5           cappedDiscoveryArm
	P10Hist        []int
	RecentActions  []string
	PriorHeld      []int
	PriorBuys      []int
	PriorSales     []int
	PendingRowID   int64
	RealBuyAt      time.Time
	RealBuyOur     int
	CyclesToRealBuy int
	// snapshot at first real buys>0 or held>0
	SnapDone       bool
	SnapKind       string // real_buy | real_held
	SnapV1         int
	SnapV3         int
	SnapV5         int
	SnapWall       int
}

var (
	cdMu    sync.Mutex
	cdState = map[string]*cappedDiscoveryState{}
	cdEpSeq uint64
)

func initCappedDiscoveryShadowTable() {
	if mlDB == nil {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
CREATE TABLE IF NOT EXISTS capped_discovery_shadow (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	item_id TEXT NOT NULL,
	episode_id TEXT NOT NULL,
	actual_price INTEGER NOT NULL,
	virtual_cap1 INTEGER NOT NULL,
	virtual_cap3 INTEGER NOT NULL,
	virtual_cap5 INTEGER NOT NULL,
	trusted_p10 INTEGER NOT NULL,
	target_band INTEGER NOT NULL,
	min_ask INTEGER NOT NULL,
	unique_sellers_noban INTEGER NOT NULL,
	uuid_noban INTEGER NOT NULL,
	our_over_p10 REAL NOT NULL,
	step INTEGER NOT NULL,
	held INTEGER NOT NULL,
	buys INTEGER NOT NULL,
	sales INTEGER NOT NULL,
	try_sells INTEGER NOT NULL,
	had_down INTEGER NOT NULL,
	gate_open INTEGER NOT NULL,
	active1 INTEGER NOT NULL,
	active3 INTEGER NOT NULL,
	active5 INTEGER NOT NULL,
	cycles_since_arm1 INTEGER NOT NULL,
	cycles_since_arm3 INTEGER NOT NULL,
	cycles_since_arm5 INTEGER NOT NULL,
	cycles_to_band1 INTEGER,
	cycles_to_band3 INTEGER,
	cycles_to_band5 INTEGER,
	cycles_since_episode INTEGER NOT NULL,
	stop1 TEXT,
	stop3 TEXT,
	stop5 TEXT,
	v1_over_p10 REAL,
	v3_over_p10 REAL,
	v5_over_p10 REAL,
	winner_action TEXT,
	initial_our INTEGER,
	initial_p10 INTEGER,
	initial_ratio REAL,
	event_kind TEXT,
	snap_v1 INTEGER,
	snap_v3 INTEGER,
	snap_v5 INTEGER,
	next_cycle_buys INTEGER,
	next_cycle_held INTEGER,
	next_cycle_sales INTEGER,
	cycles_to_real_buy INTEGER,
	notes TEXT
)`)
	if err != nil {
		log.Printf("[capped_discovery_shadow] schema: %v", err)
		return
	}
	// migrate older shadow schema if present
	ensureMLColumn(mlDB, "capped_discovery_shadow", "trusted_p10", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "target_band", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "try_sells", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "had_down", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_since_arm1", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_since_arm3", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_since_arm5", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_to_band1", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_to_band3", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_to_band5", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "cycles_since_episode", "INTEGER NOT NULL DEFAULT 0")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "event_kind", "TEXT")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "snap_v1", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "snap_v3", "INTEGER")
	ensureMLColumn(mlDB, "capped_discovery_shadow", "snap_v5", "INTEGER")
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_cds_ts ON capped_discovery_shadow(ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_cds_ep ON capped_discovery_shadow(episode_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_cds_item_ts ON capped_discovery_shadow(item_id, ts)`)
	_, _ = mlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_cds_event ON capped_discovery_shadow(event_kind)`)
}

func cdGet(item string) *cappedDiscoveryState {
	st, ok := cdState[item]
	if !ok {
		st = &cappedDiscoveryState{}
		cdState[item] = st
	}
	return st
}

func cappedDiscoveryBandFloor(p10 int) int {
	if p10 <= 0 {
		return 0
	}
	return int(cappedDiscoveryBandFrac * float64(p10))
}

func cappedDiscoveryAdvance(prev, step, capSteps, target int) int {
	if step <= 0 || capSteps <= 0 || target <= prev {
		return prev
	}
	nxt := prev + capSteps*step
	if nxt > target {
		nxt = target
	}
	return nxt
}

func cappedDiscoveryArmStop(virtual, p10, held, buys int, trustOK bool, manualLock bool, winner string, active bool) string {
	if !active {
		return ""
	}
	if buys > 0 {
		return "real_buys"
	}
	if held > 0 {
		return "real_held"
	}
	if manualLock {
		return "manual_lock"
	}
	if strings.Contains(winner, "price_down") {
		return "price_down"
	}
	if !trustOK || p10 <= 0 {
		return "trust_lost"
	}
	if virtual >= cappedDiscoveryBandFloor(p10) {
		return "entered_band"
	}
	return ""
}

// runCappedDiscoveryShadow — after real decision; never mutates price/winner.
func runCappedDiscoveryShadow(item string, now time.Time, ourPrice, step, held, buys, sales, trySells int, winnerAction string, manualLock bool) {
	if strings.TrimSpace(item) == "" || step <= 0 || ourPrice <= 0 {
		return
	}
	book := ahBookMarketRecoveryStats(item, now.Add(-marketRecoveryBookWindow))

	cdMu.Lock()
	defer cdMu.Unlock()
	st := cdGet(item)

	if st.PendingRowID > 0 {
		updateCappedDiscoveryNextCycle(st.PendingRowID, buys, held, sales)
		st.PendingRowID = 0
	}

	st.RecentActions = appendBoundedStr(st.RecentActions, winnerAction, marketRecoveryRecentDownN+2)
	st.PriorHeld = appendBoundedInt(st.PriorHeld, held, marketRecoveryPriorHistN)
	st.PriorBuys = appendBoundedInt(st.PriorBuys, buys, marketRecoveryPriorHistN)
	st.PriorSales = appendBoundedInt(st.PriorSales, sales, marketRecoveryPriorHistN)
	if book.OK && book.P10 > 0 {
		st.P10Hist = appendBoundedInt(st.P10Hist, book.P10, marketRecoveryP10HistN)
	}

	in := marketRecoveryEvalIn{
		Item:         item,
		Now:          now,
		OurPrice:     ourPrice,
		Step:         step,
		Held:         held,
		Buys:         buys,
		Sales:        sales,
		WinnerAction: winnerAction,
		ManualLock:   manualLock,
		Book:         book,
	}
	gate := isBPriceTrap(in, st.P10Hist, st.RecentActions,
		trimPriorExcludeCurrent(st.PriorHeld),
		trimPriorExcludeCurrent(st.PriorBuys),
		trimPriorExcludeCurrent(st.PriorSales),
	)

	trustOK := book.OK && marketRecoveryTrustOK(book.NSell, book.NUUID)
	p10 := book.P10
	target := cappedDiscoveryBandFloor(p10)
	st.TargetRef = target
	st.Step = step
	hadDown := strings.Contains(winnerAction, "price_down")

	anyActive := st.Arm1.Active || st.Arm3.Active || st.Arm5.Active

	if gate && !anyActive {
		cdEpSeq++
		st.EpisodeID = fmt.Sprintf("%s-%d-%d", item, now.Unix(), cdEpSeq)
		st.StartedAt = now
		st.InitialOur = ourPrice
		st.InitialP10 = p10
		st.WallCycles = 0
		if p10 > 0 {
			st.InitialRatio = float64(ourPrice) / float64(p10)
		}
		st.Arm1 = cappedDiscoveryArm{Virtual: ourPrice, Active: true}
		st.Arm3 = cappedDiscoveryArm{Virtual: ourPrice, Active: true}
		st.Arm5 = cappedDiscoveryArm{Virtual: ourPrice, Active: true}
		st.RealBuyAt = time.Time{}
		st.RealBuyOur = 0
		st.CyclesToRealBuy = 0
		st.SnapDone = false
		st.SnapKind = ""
		st.SnapV1, st.SnapV3, st.SnapV5 = 0, 0, 0
		anyActive = true
	}

	if !anyActive {
		return
	}

	st.WallCycles++

	// Snapshot virtuals at first real BUY or held (same market for all arms).
	eventKind := ""
	if !st.SnapDone {
		if buys > 0 {
			st.SnapDone = true
			st.SnapKind = "real_buy"
			st.SnapV1, st.SnapV3, st.SnapV5 = st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual
			st.SnapWall = st.WallCycles
			st.RealBuyAt = now
			st.RealBuyOur = ourPrice
			st.CyclesToRealBuy = st.WallCycles
			eventKind = "real_buy"
		} else if held > 0 {
			st.SnapDone = true
			st.SnapKind = "real_held"
			st.SnapV1, st.SnapV3, st.SnapV5 = st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual
			st.SnapWall = st.WallCycles
			eventKind = "real_held"
		}
	} else if buys > 0 && st.SnapKind == "real_buy" {
		eventKind = "real_buy"
	} else if held > 0 && st.SnapKind == "real_held" {
		eventKind = "real_held"
	}

	advance := func(arm *cappedDiscoveryArm, capSteps int) {
		if !arm.Active {
			return
		}
		if stop := cappedDiscoveryArmStop(arm.Virtual, p10, held, buys, trustOK, manualLock, winnerAction, true); stop != "" {
			arm.StopReason = stop
			arm.Active = false
			if stop == "entered_band" && !arm.HitBand {
				arm.HitBand = true
				arm.CyclesToBand = arm.Cycles
				arm.ReachedBandAt = now
			}
			return
		}
		before := arm.Virtual
		arm.Virtual = cappedDiscoveryAdvance(arm.Virtual, step, capSteps, target)
		if arm.Virtual > before {
			arm.Cycles++
		}
		if stop := cappedDiscoveryArmStop(arm.Virtual, p10, held, buys, trustOK, manualLock, winnerAction, true); stop != "" {
			arm.StopReason = stop
			arm.Active = false
			if stop == "entered_band" && !arm.HitBand {
				arm.HitBand = true
				arm.CyclesToBand = arm.Cycles
				arm.ReachedBandAt = now
			}
		}
	}

	// Advance only if not already snapping on buy/held this cycle — still advance first then stop via armStop.
	advance(&st.Arm1, 1)
	advance(&st.Arm3, 3)
	advance(&st.Arm5, 5)

	// If we snapped before advance, refresh snap to post-advance virtuals on the buy/held cycle.
	if eventKind == "real_buy" || eventKind == "real_held" {
		st.SnapV1, st.SnapV3, st.SnapV5 = st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual
	}

	logCappedDiscoveryRow(in, st, gate, trySells, hadDown, eventKind)

	if !st.Arm1.Active && !st.Arm3.Active && !st.Arm5.Active {
		log.Printf("[capped_discovery_shadow] episode end %s ep=%s our0=%d p10_0=%d v1=%d v3=%d v5=%d stop1=%s stop3=%s stop5=%s snap=%s/%d/%d/%d wall=%d",
			item, st.EpisodeID, st.InitialOur, st.InitialP10,
			st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual,
			st.Arm1.StopReason, st.Arm3.StopReason, st.Arm5.StopReason,
			st.SnapKind, st.SnapV1, st.SnapV3, st.SnapV5, st.WallCycles)
		st.Arm1 = cappedDiscoveryArm{}
		st.Arm3 = cappedDiscoveryArm{}
		st.Arm5 = cappedDiscoveryArm{}
	}
}

func cdMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ratioOr0(num, den int) float64 {
	if den <= 0 || num <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func nullableCyclesToBand(arm cappedDiscoveryArm) any {
	if arm.HitBand || arm.StopReason == "entered_band" {
		return arm.CyclesToBand
	}
	return nil
}

func logCappedDiscoveryRow(in marketRecoveryEvalIn, st *cappedDiscoveryState, gate bool, trySells int, hadDown bool, eventKind string) {
	if mlDB == nil {
		return
	}
	p10 := in.Book.P10
	gateI, downI := 0, 0
	if gate {
		gateI = 1
	}
	if hadDown {
		downI = 1
	}
	a1, a3, a5 := 0, 0, 0
	if st.Arm1.Active {
		a1 = 1
	}
	if st.Arm3.Active {
		a3 = 1
	}
	if st.Arm5.Active {
		a5 = 1
	}
	notes := fmt.Sprintf("shadow_only band=%.2f*p10 no_prod_mutation", cappedDiscoveryBandFrac)
	var cycBuy any
	if !st.RealBuyAt.IsZero() {
		cycBuy = st.CyclesToRealBuy
	}
	var snap1, snap3, snap5 any
	if st.SnapDone {
		snap1, snap3, snap5 = st.SnapV1, st.SnapV3, st.SnapV5
	}

	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	res, err := mlDB.Exec(`
INSERT INTO capped_discovery_shadow (
	ts, item_id, episode_id,
	actual_price, virtual_cap1, virtual_cap3, virtual_cap5,
	trusted_p10, target_band, min_ask, unique_sellers_noban, uuid_noban, our_over_p10, step,
	held, buys, sales, try_sells, had_down, gate_open,
	active1, active3, active5,
	cycles_since_arm1, cycles_since_arm3, cycles_since_arm5,
	cycles_to_band1, cycles_to_band3, cycles_to_band5,
	cycles_since_episode,
	stop1, stop3, stop5, v1_over_p10, v3_over_p10, v5_over_p10,
	winner_action, initial_our, initial_p10, initial_ratio,
	event_kind, snap_v1, snap_v3, snap_v5,
	cycles_to_real_buy, notes
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.Now.UTC().Format(time.RFC3339),
		in.Item,
		st.EpisodeID,
		in.OurPrice,
		st.Arm1.Virtual,
		st.Arm3.Virtual,
		st.Arm5.Virtual,
		p10,
		st.TargetRef,
		in.Book.MinAsk,
		in.Book.NSell,
		in.Book.NUUID,
		ratioOr0(in.OurPrice, p10),
		in.Step,
		in.Held,
		in.Buys,
		in.Sales,
		trySells,
		downI,
		gateI,
		a1, a3, a5,
		st.Arm1.Cycles, st.Arm3.Cycles, st.Arm5.Cycles,
		nullableCyclesToBand(st.Arm1), nullableCyclesToBand(st.Arm3), nullableCyclesToBand(st.Arm5),
		st.WallCycles,
		st.Arm1.StopReason, st.Arm3.StopReason, st.Arm5.StopReason,
		ratioOr0(st.Arm1.Virtual, p10),
		ratioOr0(st.Arm3.Virtual, p10),
		ratioOr0(st.Arm5.Virtual, p10),
		in.WinnerAction,
		st.InitialOur,
		st.InitialP10,
		st.InitialRatio,
		eventKind,
		snap1, snap3, snap5,
		cycBuy,
		notes,
	)
	if err != nil {
		log.Printf("[capped_discovery_shadow] insert %s: %v", in.Item, err)
		return
	}
	if id, err := res.LastInsertId(); err == nil {
		st.PendingRowID = id
	}
	log.Printf("[capped_discovery_shadow] %s ep=%s actual=%d v1=%d v3=%d v5=%d p10=%d band=%d event=%q wall=%d",
		in.Item, st.EpisodeID, in.OurPrice, st.Arm1.Virtual, st.Arm3.Virtual, st.Arm5.Virtual,
		p10, st.TargetRef, eventKind, st.WallCycles)
}

func updateCappedDiscoveryNextCycle(rowID int64, buys, held, sales int) {
	if mlDB == nil || rowID <= 0 {
		return
	}
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	_, err := mlDB.Exec(`
UPDATE capped_discovery_shadow
SET next_cycle_buys=?, next_cycle_held=?, next_cycle_sales=?
WHERE id=?`, buys, held, sales, rowID)
	if err != nil {
		log.Printf("[capped_discovery_shadow] next_cycle: %v", err)
	}
}

func resetCappedDiscoveryStateForTest() {
	cdMu.Lock()
	defer cdMu.Unlock()
	cdState = map[string]*cappedDiscoveryState{}
	cdEpSeq = 0
}
