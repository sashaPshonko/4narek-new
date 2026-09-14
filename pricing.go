package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const cheapBuyFractionThreshold = 0.50

// Слотов «Хранилище» на АХ у одного бота (4NAREK.mjs STORAGE_AH_SLOTS).
const ahStorageSlotsPerBot = 5

// Всего слотов у бота под лоты категории: инвентарь + АХ.
const botTotalSlots = 32

// stock_corridor_v8af — v8ae + empty_market_catchup (Sep 2026):
//   Explored ∧ held=sales=buys=0 ∧ thick book ∧ our/p10 < 0.85 streak≥2 → +1 UP;
//   не held=0→UP: без thick+gap cursor не армится; stop near 0.90 / max steps / up_cd.
// stock_corridor_v8ae — v8ad + cold-start price discovery (Sep 2026):
//   !PriceExplored ∧ held=sales=buys=0 ∧ thick book ∧ our/p10 < gap → +1 UP only;
//   Explored SKU: empty_inventory_up выкл; held=0 never auto-DOWN (grant);
//   Explored sticky after sell or abort (max cycles / near p10).
// stock_corridor_v8ad — v8ab + excess grant + Minimal post-grant C (Sep 2026):
//   grant: held≤hi → авто-↓ запрещён;
//   C HOLD filters: sales≥2∧excess=+1; sales=0∧streak1∧excess≤1∧buys=0;
//   C DOWN candidates (bypass idleHard only): sales=0∧(streak≥2 ∨ buys>0∧… ∨ excess≥3);
//   doi_cover сохранён; sales=1 и sales≥2∧excess≥2 — текущая логика без изменений.
// stock_corridor_v8ab — v8aa + empty_inventory_up (Sep 2026):
//   held=0 → exploration +1 step (sales/buys/night/book не обязательны);
//   cap: p10+nac / paid+5step / safety 24step; CorridorUpCooldown arm;
//   same-cycle empty_idle_ah_soft_down не откатывает этот ↑.
// stock_corridor_v8aa — v8z + inventory ↑ обратно (Sep 2026):
//   demand / recover / paid-climb ON: мало стока + мало покупок (sales≥buys / <<paid) → ↑;
//   книга / empty_idle / trusted_min / market_recovery / floor_escape ↑ по-прежнему OFF;
//   soft-↓ по книге остаётся.
// stock_corridor_v8z — v8y + все ↑ по книге/empty_idle выкл (Sep 2026):
//   ah_book / empty_book / trusted_min / market_recovery / floor_escape(+empty_idle) ↑ off;
//   soft-↓ по книге остаётся; ручная цена POST /sales/api/price (kind=set).
// stock_corridor_v8y — v8x + empty_idle_ah_soft_down (Sep 2026):
//   EMPTY_IDLE по-прежнему запрещает fill/ghost/dump ↓ через applyDown;
//   исключение — явная ветка empty_idle_ah_soft_down: толстая AH (≥40) + sell ≫ p10+nac
//   → тот же ahBookSoftDownApply; same-cycle alreadyDown блокирует FE/trusted/MR UP;
//   EmptyIdleMarketDownCooldown=3 + bookOK∧sell>p10+nac → anti-yoyo (нет empty_idle UP).
// stock_corridor_v8x — v8w + up_cd только на sales-driven ↑ (Sep 2026):
//   empty_idle / empty_book / floor_escape / floor / market_recovery / trusted_ah_min
//   не ставят CorridorUpCooldown и не копят UpStreak — иначе нормализация после
//   сброса/пола тормозит на ~20–30 мин между шагами. deep/skim/ah_book(held>0) — как были.
// stock_corridor_v8w — v8v + EMPTY_IDLE (Sep 2026):
//   held=sales=buys=0 → обычный DOWN запрещён; recovery UP:
//   trusted jump / иначе +1 (даже без book и не у пола). Не DOI/fill/paid/recover.
// stock_corridor_v8v — v8u + floor escape (Sep 2026):
//   idle-empty near_floor/deep_AH → +1; down_streak≥3 near_floor → +1;
//   deep pit (our/AH≤0.50) soft trust (≥5 sellers, ≥2 near) → trusted jump;
//   buys>sales / sales-on-floor special / recover·demand·paid — не трогаем.
// stock_corridor_v8u — v8t + Level-2 Policy F (sales≥1 only, Sep 2026):
//   DOI=held/sales: <3 HOLD; [3,5) OVER −2; [5,10) SOFT −1; ≥10 HOLD.
//   fill≥25% больше не основной soft-↓ при sales≥1. sales=0: legacy fill dump/over/soft.
//   Без fill≥50 rail. L1 trusted_AH_min / floor / disabled UP — без изменений.
// stock_corridor_v8t — v8s + Level1/Level2 split (audit Sep 2026):
//   L1: trusted_AH_min jump (held=0 buys=0 deep gap); demand/recover/paid-↑ off;
//       try≥5 не универсальный UP-veto; up_deep оставлен; corridor 18–25% без сдвига;
//       DOWN (soft/over/dump) и nacenka не трогаем.
// stock_corridor_v8s — v8r + up_skim выключен (→ hold_skim_disabled): matched HOLD
// стабильно лучше skim-↑; KEEP-подрежимов нет. up_paid / demand / recover / ↓ не трогаем.
// stock_corridor_v8r — v8q + held=0 может быть bullish: если глубокая книга AH выше нас,
// поднимаем медленно по 1 step (empty_book), а не ждём sale-подтверждение.
// stock_corridor_v8q — v8p + не пилить каталог при held=0 (stale/empty_fair/overcap):
// ↓ с призрака только если есть сток и try показывает отказ; пусто ≠ «дорого".
// ah_book soft-↓: всегда ≥40 uuid; на пустом стоке не больше −N×step за цикл.
// stock_corridor_v8p — v8o + ah_book-↑ только при живом разборе/held>0 + cap шагов;
// recover-↑ только при sales≥1; deep-bypass только sales>buys; probe×1.
// stock_corridor_v8o — v8n + skim lead sales≥buys+3, жёстче try-veto для skim,
// soft-↓ откат в полосе после неудачного ↑, up_cd=2.
// stock_corridor_v8n — v8m + ↓ с призрачного потолка (stale/overcap при недоборе);
// пустой held: AH soft-↓ с более тонкой книгой, buys не блокируют ↓.
// stock_corridor_v8m — v8l + recover-↑ только если цена занижена vs paid (не vacuum).
// stock_corridor_v8l — v8k + soft-↓ к p10+наценка+шаг (вместо клипа к витринам).
// stock_corridor_v8k — v8j + замок set_min (FunTime не поднимает каталог втупую).
// stock_corridor_v8j — v8i, но пол книги = min за цикл (~10 мин); soft-↓ раньше клип к витринам.
//
// stock_corridor_v8h — v8g + полоса на малом share (броня 1 тип / 6 id, share≈21):
// 18–25% давало lo/hi = 4–5, медиана held=2 → вечный недобор и deep-↑ (lo/2=2).
// Мин. ширина полосы вниз; deep только held=0. Over idle / paid-climb как в v8g.
// Demand / skim / noBuy без потолка. Цена < пола → поднимаем. Пустое не роняем.
//
// v7→v8 (29.07): в [lo,hi] всегда был hold → цена залипала в «удобном» равновесии
// (шлемы 1.2M вместо прибыльных ~2.5M). Skim: если сток в полосе и рынок реально
// разбирает (sales>buys, strong demand, без try/buy-veto) → ↑ пробуем дороже.
// Не ночью. Те же veto/cd/streak, что у demand-↑. Soft↓ при перестоке откатывает.
//
// v6→v7 (28.07): recover-↑ с пола; buy-veto только buys>sales.
// Онлайн в решение не входит.
//
// История: v1 15–25%; v2 try-veto; v3 lo=18% buy-veto; v4 weak_demand; v5 empty/night; v6–v7 …
const (
	stockBandLoFrac   = 0.18
	stockBandHiFrac   = 0.25
	stockSoftDownFrac = 0.28
	stockOverFrac     = 0.35
	stockDumpFrac     = 0.50
	// позор: цель тоньше, hard↓ раньше — sell-коридор иначе не сливает 60–99% fill.
	pozorBandLoFrac            = 0.10
	pozorBandHiFrac            = 0.18
	pozorSoftDownFrac          = 0.22
	pozorOverFrac              = 0.25
	pozorDumpFrac              = 0.40
	corridorMaxUpStreak        = 1 // не два ↑ подряд (только sales-driven ↑)
	corridorUpCooldownCycles   = 2 // после sales-driven ↑ ещё N циклов без ↑ (deep может обойти)
	corridorMinSalesForUp      = 3 // дневной пол спроса на ↑
	corridorNightMinSalesForUp = 4 // ночь 03–09 MSK
	corridorSoftDownEvery      = 1
	corridorHardDownStepMult   = 2 // over/dump: −step×2
	tryUpVetoMinTries          = 5
	tryUpVetoPerSale           = 2
	corridorSkimMinLead        = 3 // legacy порог lead; сам skim-↑ выключен (v8s)
	corridorSkimEnabled        = false // v8s: up_skim → HOLD (baggage vs matched HOLD)
	// v8aa: ↑ когда мало стока и боты мало покупают (sales≥buys / цена << paid).
	// Книга/empty_idle ↑ остаются выкл (v8z).
	corridorDemandUpEnabled  = true  // held<lo + sales>buys → up_demand
	corridorRecoverUpEnabled = true  // held<lo + price<<paid + sales≥1 → recover
	corridorPaidClimbEnabled = true  // в полосе price<<paid + buys≤sales → up_paid
	// recover-↑: только при цене << paid и sales≥1; без buys — короткая страховка.
	corridorMaxNoBuyUps       = 2 // recover-↑ подряд без buys → пауза (антиvacuum), без resume
	corridorRecoverProbeSteps = 1 // recover ≤ max(sell за TTL) + K×step
	corridorThinFleetMaxBots  = 2 // ≤ столько ботов в категории → мягче пороги спроса / recover
	corridorMaxIdleHardDowns = 1 // over/dump при sales=0: один щуп, не цепочка в пол
	corridorIdleHardDownsDump = 3 // при fill≥50% и sales=0 — до 3 hard-↓ (кирка иначе стоит в переполнении)
	corridorPaidBandGapSteps = 3 // в полосе цена ≤ paid−N×step → ↑ к якорю (и ночью)
	corridorMinBandSpan      = 4 // hi−lo; share=21 иначе полоса 4–5 шт. (позор не трогаем)
	ahBookRaiseWindow         = 10 * time.Minute // окно книги = типичный AnalysisTime цикл
	ahBookMinLotsInWindow     = 40               // уникальных uuid за цикл; меньше — скан тонкий, min/↑/↓ не считаем
	ahBookMinLotsWhenEmpty    = 12               // deprecated v8q: soft-↓ всегда ahBookMinLotsInWindow
	ahBookSoftDownSlackSteps  = 2                // soft-↓ только если sell > p10+наценка+2×step (мёртвая зона)
	ahBookMaxRaiseSteps       = 2                // ah_book-↑ не прыгает к min+наценка за цикл (0.84→2.4)
	// Sep 2026: книга AH наебывает — все ↑ «по рынку/книге/empty_idle» выкл.
	// soft-↓ по книге (corridor_price_down_ah_book / empty_idle_ah_soft_down) остаётся.
	// Ручная цена с /sales → LastManualKind=set (блок ↑↓ на AnalysisTime).
	ahBookPriceUpEnabled      = false // corridor_price_up_ah_book / empty_book
	floorEscapePriceUpEnabled = false // empty_idle / deep_ah / trusted_jump / near_floor bounce
	// После empty_idle_ah_soft_down: N циклов без empty_idle/trusted/MR recovery UP (anti-yoyo).
	emptyIdleMarketDownCooldownCycles = 3
	serverBoundLookCycles = 3 // окно закупок для set_min
	serverBoundMinBuys    = 2 // одна покупка — шум
)

// AdjustReport — итог цикла adjustPrice для TG/логов.
type AdjustReport struct {
	Item           string
	Action         string
	Reason         string
	Skipped        bool
	PriceBefore    int
	PriceAfter     int
	NacenkaBefore  int
	NacenkaAfter   int
	Sales          int
	Buys           int
	TrySells       int
	OnAH           int
	Inv            int
	Held           int
	NormalSales    int
	Share          int
	Free           int
	Need           int
	PriceFloor     int
	Step           int
	Cooldown       int
	NacenkaSumNow  int
	NacenkaSumPrev int
	GoodStreak     int
	BlockNacenkaUp bool
}

// typeRelistDisabled — go-типы в режиме «без перевыставления» (FLEET_ABSORB_TYPES, через запятую).
var typeRelistDisabled map[string]struct{}

func initFleetRelistFlags() {
	typeRelistDisabled = make(map[string]struct{})
	raw := strings.TrimSpace(os.Getenv("FLEET_ABSORB_TYPES"))
	if raw == "" {
		return
	}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			typeRelistDisabled[t] = struct{}{}
		}
	}
	if len(typeRelistDisabled) > 0 {
		types := make([]string, 0, len(typeRelistDisabled))
		for t := range typeRelistDisabled {
			types = append(types, t)
		}
		log.Printf("[FLEET] без перевыставления (absorb): %v", types)
	}
}

func isTypeRelistEnabled(minecraftType string) bool {
	if typeRelistDisabled == nil {
		return true
	}
	_, off := typeRelistDisabled[minecraftType]
	return !off
}

const (
	marketFloorWindow    = 10 * time.Minute
	marketFloorWindowMin = 9*time.Minute + 30*time.Second
	marketFloorMaxStale  = 2 * time.Minute
)

// ItemAdjustState — состояние контроллера цены между циклами.
type ItemAdjustState struct {
	GoodStreak            int  `json:"good_streak"`
	ExperimentCheck       bool `json:"experiment_check"`
	LastCycleProfit       int  `json:"last_cycle_profit"`
	LastCycleNacenkaSum   int  `json:"last_cycle_nacenka_sum"`
	StockVsSalesCooldown  int  `json:"stock_vs_sales_cooldown"`
	FillPriceCooldown     int  `json:"fill_price_cooldown"`
	CorridorUpStreak      int  `json:"corridor_up_streak"`        // подряд ↑ в stock_corridor
	CorridorDeadStreak    int  `json:"corridor_dead_streak"`      // подряд sales=0 && buys=0
	CorridorDownCooldown  int  `json:"corridor_down_cooldown"`    // циклы до следующего soft↓
	CorridorUpCooldown    int  `json:"corridor_up_cooldown"`      // циклы до следующего ↑
	CorridorNoBuyUpStreak int  `json:"corridor_no_buy_up_streak"` // ↑ подряд без buys (антиvacuum)
	IdleHardDownStreak    int  `json:"idle_hard_down_streak"`     // подряд over/dump при sales=0
	CorridorDownStreak    int  `json:"corridor_down_streak"`      // подряд price_down (HOLD не сбрасывает)
	FloorEscapeCooldown   int  `json:"floor_escape_cooldown"`     // циклы до следующего floor escape
	// После empty_idle_ah_soft_down: блочит empty_idle/trusted/MR UP на N циклов.
	EmptyIdleMarketDownCooldown int `json:"empty_idle_market_down_cooldown"`
	// empty_inventory_up (legacy): счётчик ↑ пока held==0. Для Explored SKU ветка выкл.
	EmptyInventoryClimbSteps  int `json:"empty_inventory_climb_steps"`
	EmptyInventoryAnchorPrice int `json:"empty_inventory_anchor_price"`
	LastCycleSales            int `json:"last_cycle_sales"` // sales прошлого цикла
	// Подряд циклов sales==0 при held>hi (для Minimal C). На решении: +1 за текущий цикл.
	ZeroSalesExcessStreak int `json:"zero_sales_excess_streak"`
	// Cold-start sell-price discovery (v8ae). Sticky Explored; не путать с обычным s0b0.
	PriceExplored            bool   `json:"price_explored"`
	PriceExplorationCycles   int    `json:"price_exploration_cycles"`
	PriceOriginKind          string `json:"price_origin_kind,omitempty"`  // base_seed|manual_set|backfill|…
	PriceOriginPrice         int    `json:"price_origin_price,omitempty"`
	// empty_market_catchup (v8af): подряд циклов thick+gap+empty на Explored SKU.
	EmptyMarketGapStreak int `json:"empty_market_gap_streak"`
}

func resolveNacenkaMin(cfg ItemConfig) int {
	if cfg.NacenkaMin > 0 {
		return cfg.NacenkaMin
	}
	return cfg.Nacenka
}

func getNacenka(item string) int {
	if cfg, ok := itemsConfig[item]; ok {
		return cfg.Nacenka
	}
	return 0
}

// ensureNacenkasInitialized — всегда выравнивает рантайм-наценки под items_config
// (stock_corridor не крутит nacenka; старые capital-значения из daily/runtime сбрасываем).
func ensureNacenkasInitialized() {
	if data.Nacenkas == nil {
		data.Nacenkas = make(map[string]int)
	}
	if dailyData.Nacenkas == nil {
		dailyData.Nacenkas = make(map[string]int)
	}
	if data.AdjustState == nil {
		data.AdjustState = make(map[string]ItemAdjustState)
	}
	for item, cfg := range itemsConfig {
		data.Nacenkas[item] = cfg.Nacenka
		dailyData.Nacenkas[item] = cfg.Nacenka
		if _, ok := data.AdjustState[item]; !ok {
			data.AdjustState[item] = ItemAdjustState{}
		}
	}
}

func priceUpdatePayloadLocked() PriceUpdate {
	p := filterPrices()
	p.Catalog = buildCatalogOut()
	return p
}

func priceUpdatePayload() PriceUpdate {
	mutex.RLock()
	defer mutex.RUnlock()
	return priceUpdatePayloadLocked()
}

// publishPriceUpdate — только без mutex.Lock в этой горутине (иначе дедлок RWMutex).
func publishPriceUpdate() {
	mutex.RLock()
	payload := priceUpdatePayloadLocked()
	mutex.RUnlock()
	select {
	case broadcast <- payload:
	default:
	}
}

func profitInWindow(item string, since time.Time) int {
	profit := 0
	for _, trade := range data.TradeHistory[item] {
		if !trade.Time.After(since) {
			continue
		}
		if trade.Price <= 0 {
			continue
		}
		switch trade.Type {
		case "buy":
			profit -= trade.Price
		case "sell":
			profit += trade.Price
		}
	}
	return profit
}

// nacenkaSumInWindow — сумма наценок по продажам в окне (успех эксперимента).
// Если у старых записей Nacenka=0 — берём текущую getNacenka как fallback.
func nacenkaSumInWindow(item string, since time.Time) int {
	sum := 0
	fallback := getNacenka(item)
	for _, trade := range data.TradeHistory[item] {
		if !trade.Time.After(since) {
			continue
		}
		if trade.Type != "sell" {
			continue
		}
		n := trade.Nacenka
		if n <= 0 {
			n = fallback
		}
		sum += n
	}
	return sum
}

func stockNormFromConfig(cfg ItemConfig) int {
	if cfg.NormalCount > 0 {
		return cfg.NormalCount
	}
	return cfg.NormalSales
}

// stockBandFracs — доли fill для stockTargets.
type stockBandFracs struct {
	lo, hi, soft, over, dump float64
}

func isPozorCategory(item string, cfg ItemConfig) bool {
	if strings.Contains(item, "позор") {
		return true
	}
	return strings.Contains(cfg.Type, "позор")
}

func stockBandFor(item string, cfg ItemConfig) stockBandFracs {
	if isPozorCategory(item, cfg) {
		return stockBandFracs{
			lo: pozorBandLoFrac, hi: pozorBandHiFrac, soft: pozorSoftDownFrac,
			over: pozorOverFrac, dump: pozorDumpFrac,
		}
	}
	return stockBandFracs{
		lo: stockBandLoFrac, hi: stockBandHiFrac, soft: stockSoftDownFrac,
		over: stockOverFrac, dump: stockDumpFrac,
	}
}

// stockTargets — пороги fill=held/share по полосе (default или позор).
func stockTargets(share int, band stockBandFracs) (lo, hi, soft, over, dump int) {
	if share <= 0 {
		return 1, 2, 3, 4, 5
	}
	lo = int(float64(share)*band.lo + 0.5)
	hi = int(float64(share)*band.hi + 0.5)
	soft = int(float64(share)*band.soft + 0.5)
	over = int(float64(share)*band.over + 0.5)
	dump = int(float64(share)*band.dump + 0.5)
	if lo < 1 {
		lo = 1
	}
	if hi <= lo {
		hi = lo + 1
	}
	// Обычный коридор: на малом share (броня после merge) не оставлять полосу в 1–2 слота.
	if band.lo >= stockBandLoFrac-1e-9 && hi-lo < corridorMinBandSpan {
		need := corridorMinBandSpan - (hi - lo)
		down := (need + 1) / 2
		up := need - down
		lo -= down
		hi += up
		if lo < 1 {
			hi += 1 - lo
			lo = 1
		}
	}
	if soft <= hi {
		soft = hi + 1
	}
	if over <= soft {
		over = soft + 1
	}
	if dump <= over {
		dump = over + 1
	}
	return lo, hi, soft, over, dump
}

// deepUnderstock — витрина пустая. lo/2 при share≈21 = held=2, это норма брони, не «глубокий недобор».
func deepUnderstock(totalHeld, targetLo int) bool {
	return totalHeld == 0 && targetLo > 0
}

// lastPaidSellMax — самая дорогая фактическая продажа в окне (не BasePrice, не пол закупа).
func lastPaidSellMax(trades []TradeLog, now time.Time, since time.Time) (maxPrice int, at time.Time) {
	for _, trade := range trades {
		if trade.Type != "sell" || trade.Price <= 0 {
			continue
		}
		if trade.Time.Before(since) || trade.Time.After(now) {
			continue
		}
		if trade.Price > maxPrice {
			maxPrice = trade.Price
			at = trade.Time
		}
	}
	return
}

// recoverPaidCap — recover может щупать чуть выше последней доказанной продажи.
func recoverPaidCap(lastPaid, step int) int {
	if lastPaid <= 0 || step <= 0 {
		return 0
	}
	return lastPaid + corridorRecoverProbeSteps*step
}

// recoverBlockedByPaidCap — нет продаж в окне или цена уже на/выше якоря+щуп.
func recoverBlockedByPaidCap(price, lastPaid, step int) bool {
	cap := recoverPaidCap(lastPaid, step)
	if cap <= 0 {
		return true
	}
	return price >= cap
}

// automaticDownAllowed — grant: held > targetHi → авто-↓ может рассматриваться.
func automaticDownAllowed(held, targetHi int) bool {
	return held > targetHi
}

func excessDepth(held, targetHi int) int {
	return held - targetHi
}

// zeroSalesExcessStreakNow — длина текущей серии sales==0 в excess (включая этот цикл).
func zeroSalesExcessStreakNow(sales, held, targetHi, prevStreak int) int {
	if sales != 0 || !automaticDownAllowed(held, targetHi) {
		return 0
	}
	if prevStreak < 0 {
		prevStreak = 0
	}
	return prevStreak + 1
}

// postGrantCBlockDown — Minimal C: запрет авто-↓ (поверх grant).
//  1) sales≥2 ∧ excess==+1
//  2) sales==0 ∧ streak==1 ∧ excess≤+1 ∧ buys==0
func postGrantCBlockDown(held, targetHi, sales, buys, zeroStreak int) bool {
	if !automaticDownAllowed(held, targetHi) {
		return false
	}
	d := excessDepth(held, targetHi)
	if sales >= 2 && d == 1 {
		return true
	}
	if sales == 0 && zeroStreak == 1 && d <= 1 && buys == 0 {
		return true
	}
	return false
}

// postGrantCDownCandidate — Minimal C: sales==0 + залежь/накопление →
// не давать idleHard отменить уже выбранный fill/dump/over ↓.
func postGrantCDownCandidate(held, targetHi, sales, buys, zeroStreak int) bool {
	if sales != 0 || !automaticDownAllowed(held, targetHi) {
		return false
	}
	d := excessDepth(held, targetHi)
	if zeroStreak >= 2 {
		return true
	}
	if buys > 0 && (d >= 2 || zeroStreak >= 1) {
		return true
	}
	if d >= 3 {
		return true
	}
	return false
}

// doiCover — days-of-inventory proxy for Level-2 Policy F (sales≥1).
func doiCover(held, sales int) float64 {
	if sales <= 0 {
		return 0
	}
	return float64(held) / float64(sales)
}

// doiCoverIntensity — Policy F: "hold" | "over" | "soft" (sales≥1).
// "hold" → corridor_hold_doi_cover (сохранено в v8ad).
func doiCoverIntensity(held, sales int) string {
	if sales <= 0 {
		return ""
	}
	doi := doiCover(held, sales)
	switch {
	case doi < 3:
		return "hold"
	case doi < 5:
		return "over"
	case doi < 10:
		return "soft"
	default:
		return "hold"
	}
}

// allowHardDown — over/dump. С продажами — всегда (fwd лучше hold). Без продаж — один щуп,
// не 4×−200к до пола (sword7). При сильном переполнении (fill≥50%) — до 3 щупов.
func allowHardDown(sales, idleStreak int, stockLoad float64) bool {
	if sales > 0 {
		return true
	}
	maxIdle := corridorMaxIdleHardDowns
	if stockLoad >= 0.50 {
		maxIdle = corridorIdleHardDownsDump
	}
	return idleStreak < maxIdle
}

// priceFarBelowPaid — в полосе сидим на полу, рынок только что платил дороже.
func priceFarBelowPaid(price, paidMax, step int) bool {
	if paidMax <= 0 || step <= 0 || price <= 0 {
		return false
	}
	return price <= paidMax-corridorPaidBandGapSteps*step
}

// ghostPriceDownOK — stale/empty/overcap ↓ только при живом стоке и отказе рынка на листинг.
// held=0 + 0 sales ≠ «мы дороже рынка» (часто просто нет капитала) — каталог не пилим.
func ghostPriceDownOK(held, sales, trySells int) bool {
	if held <= 0 {
		return false
	}
	return trySellsBlockUp(sales, trySells) || trySells >= tryUpVetoMinTries
}

func isGhostCatalogDown(label string) bool {
	switch label {
	case "corridor_price_down_stale", "corridor_price_down_overcap", "corridor_price_down_empty_fair":
		return true
	default:
		return false
	}
}

// corridorUpArmCooldown — up_cd/streak только для ↑ от живого разбора.
// Нормализация с пустого склада / к полу / к книге без стока кулдаун не ставит:
// иначе после сброса цена ползёт +1 раз в ~30 мин вместо лесенки каждый цикл.
func corridorUpArmCooldown(action string) bool {
	if !strings.Contains(action, "price_up") {
		return false
	}
	switch {
	case strings.Contains(action, "empty_idle"),
		strings.Contains(action, "empty_book"),
		strings.Contains(action, "floor_escape"),
		action == "corridor_price_up_floor",
		strings.Contains(action, "market_recovery"),
		strings.Contains(action, "trusted_ah_min"):
		return false
	default:
		// deep / skim / demand / recover / paid / ah_book (held>0)
		return true
	}
}

// serverFunTimeRaiseAnomalous — set_min: не поднимать каталог.
// Закупаем не меньше, чем продаём (≥2 buy за 3 цикла) — не ↑.
// Иначе не выше p10 книги + наценка (не сырой min: дампы).
func serverFunTimeRaiseAnomalous(ours, proposed int, item string, cycle time.Duration, now time.Time) bool {
	if proposed <= ours {
		return false
	}
	if serverMinBuyBlocksUp(item, cycle, now) {
		return true
	}
	since := now.Add(-ahBookRaiseWindow)
	p10, n, ok := ahBookP10Since(item, since)
	if !ok || n < ahBookMinLotsInWindow || p10 <= 0 {
		return false
	}
	return proposed > p10+getNacenka(item)
}

func serverMinBuyBlocksUp(item string, cycle time.Duration, now time.Time) bool {
	if item == "" {
		return false
	}
	if cycle <= 0 {
		cycle = 10 * time.Minute
	}
	since := now.Add(-time.Duration(serverBoundLookCycles) * cycle)
	buys := countRecentBuys(item, since)
	if buys < serverBoundMinBuys {
		return false
	}
	return buys >= countRecentSales(item, since)
}

// trySellsBlockUp — плохая конверсия наших try_sells (не buyer demand).
// v8t: try≥5 не универсальный бан. При sales≥2 нужен try≥3×sales (audit: try≥5&sales≥5 UP>HOLD).
func trySellsBlockUp(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	if sales >= 2 {
		return trySells >= 3*sales
	}
	return trySells >= tryUpVetoPerSale*maxInt(sales, 1)
}

// trySellsBlockSkim — жёстче veto только для profit-skim в полосе (try ≥ max(minTries, sales)).
func trySellsBlockSkim(sales, trySells int) bool {
	if trySells < tryUpVetoMinTries {
		return false
	}
	return trySells >= maxInt(sales, tryUpVetoMinTries)
}

// skimSalesLeadOK — запас продаж над покупками для skim-↑.
func skimSalesLeadOK(sales, buys int) bool {
	return sales >= buys+corridorSkimMinLead
}

// skimShouldRevert — после недавнего ↑ рынок не берёт: soft-↓ даже в полосе.
func skimShouldRevert(upCooldown, sales, buys, trySells int) bool {
	if upCooldown <= 0 {
		return false
	}
	if trySellsBlockUp(sales, trySells) {
		return true
	}
	return sales <= buys && trySells >= tryUpVetoMinTries
}

// isNightMSK — окно 03:00–08:59 Europe/Moscow (false-scarcity / реконнекты).
func isNightMSK(t time.Time) bool {
	msk := t.In(time.FixedZone("MSK", 3*60*60))
	h := msk.Hour()
	return h >= 3 && h < 9
}

// demandMinSalesForUp — порог sales на ↑ с учётом размера флота в категории.
// 2 бота (504 кирки): sales/цикл естественно ниже → днём достаточно 2, ночью 3.
func demandMinSalesForUp(botsInCategory int, night bool) int {
	if night {
		if botsInCategory > 0 && botsInCategory <= corridorThinFleetMaxBots {
			return 3
		}
		return corridorNightMinSalesForUp
	}
	if botsInCategory > 0 && botsInCategory <= corridorThinFleetMaxBots {
		return 2
	}
	return corridorMinSalesForUp
}

// demandStrongEnoughForUp — не поднимать на крошках.
// День: sales≥minSales или sales=2 два цикла подряд (если minSales≤2).
// Ночь 03–09 MSK: только sales≥minSales (sustained×2 ночью отключён).
func demandStrongEnoughForUp(sales, lastCycleSales, minSales int, night bool) bool {
	if sales >= minSales {
		return true
	}
	if night {
		return false
	}
	if minSales > 2 {
		return false
	}
	return sales >= 2 && lastCycleSales >= 2
}

// downWouldUndershoot — ↓ ускорит продажи; если уже один цикл sales утащит ниже lo — не режем.
// Только для soft↓; hard over/dump в v4 игнорируют этот guard.
func downWouldUndershoot(totalHeld, sales, targetLo int) bool {
	drain := maxInt(sales, 1)
	return totalHeld-drain < targetLo
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// categoryAhCapacityLocked — ёмкость хранилища АХ по типу (боты × 5 слотов). Только под mutex.Lock.
func categoryAhCapacityLocked(minecraftType string) int {
	bots := aggregateBotsPerTypeLocked()[minecraftType]
	if bots <= 0 {
		return 0
	}
	return bots * ahStorageSlotsPerBot
}

// maxReachableStockOnAHLocked — сколько лотов этого id может быть на АХ с учётом занятых слотов другими id.
// Только под mutex.Lock.
func maxReachableStockOnAHLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int) int {
	capacity := categoryAhCapacityLocked(cfg.Type)
	if capacity <= 0 {
		return onAH
	}

	occupiedOthers := 0
	for name, count := range ahCounts {
		if name == item || count <= 0 {
			continue
		}
		other, ok := itemsConfig[name]
		if !ok || other.Type != cfg.Type {
			continue
		}
		occupiedOthers += count
	}
	if occupiedOthers == 0 {
		return capacity
	}

	totalOccupied := onAH + occupiedOthers
	freeSlots := capacity - totalOccupied
	if freeSlots < 0 {
		freeSlots = 0
	}
	return onAH + freeSlots
}

// stockNormReachableOnAHLocked — сток-норма из конфига достижима на АХ (слоты не забиты другими id).
func stockNormReachableOnAHLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int, stockNorm int) bool {
	if !isTypeRelistEnabled(cfg.Type) {
		return true
	}
	if stockNorm <= 0 {
		return true
	}
	maxReachable := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
	return maxReachable >= stockNorm
}

// effectiveStockForOverstock — при перевыставлении переизбыток только по лотам на АХ (инвентарь — буфер).
func effectiveStockForOverstock(onAH, totalStock int, relistEnabled bool) int {
	if relistEnabled {
		return onAH
	}
	return totalStock
}

// allowSellPriceIncreaseLocked — не поднимать цену, если другие id забили слоты
// и предмет физически не может дойти до сток-нормы из конфига.
func allowSellPriceIncreaseLocked(item string, cfg ItemConfig, onAH int, ahCounts map[string]int, stockNorm int) bool {
	if !isTypeRelistEnabled(cfg.Type) {
		return true
	}
	if stockNorm <= 0 {
		return true
	}
	maxReachable := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
	return maxReachable >= stockNorm
}

// cheapBuyFraction — доля покупок ≥ на price_step ниже buy-потолка (sell − nacenka).
func cheapBuyFraction(item string, sellPrice, nacenka, step int, since time.Time) (float64, int) {
	hist := priceHistory[item]
	if hist == nil || len(hist.Records) == 0 {
		return 0, 0
	}
	buyMax := sellPrice - nacenka
	cheap, total := 0, 0
	for _, r := range hist.Records {
		if !r.Time.After(since) {
			continue
		}
		total++
		if buyMax-r.Price >= step {
			cheap++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(cheap) / float64(total), total
}

// applyMarketFloors — подтянуть sell-цену к мин. лоту на АХ (+ наценка), если ниже.
// Только при 0 продаж, 0 наличия и подтверждённом окне сбора ≥10 мин.
// ОТКЛЮЧЕНО: не повышаем цену, если она ниже мин. лота на АХ.
func applyMarketFloors(floors map[string]int, windowStartMs, windowEndMs, windowMs int64) {
	_ = floors
	_ = windowStartMs
	_ = windowEndMs
	_ = windowMs
	return

	/*
		if len(floors) == 0 {
			return
		}
		if windowStartMs <= 0 || windowEndMs <= 0 || windowEndMs <= windowStartMs {
			log.Printf("[MARKET_FLOOR] skip: нет метаданных окна")
			return
		}
		windowStart := time.UnixMilli(windowStartMs)
		windowEnd := time.UnixMilli(windowEndMs)
		span := windowEnd.Sub(windowStart)
		if span < marketFloorWindowMin {
			log.Printf("[MARKET_FLOOR] skip: окно %v < %v", span.Round(time.Second), marketFloorWindowMin)
			return
		}
		if windowMs > 0 && windowMs < int64(marketFloorWindow/time.Millisecond) {
			log.Printf("[MARKET_FLOOR] skip: window_ms=%d", windowMs)
			return
		}
		if time.Since(windowEnd) > marketFloorMaxStale {
			log.Printf("[MARKET_FLOOR] skip: устарело (конец окна %v назад)", time.Since(windowEnd).Round(time.Second))
			return
		}

		mutex.Lock()
		ensureNacenkasInitialized()
		changed := false
		now := time.Now()

		for item, floor := range floors {
			cfg, ok := itemsConfig[item]
			if !ok || floor <= 0 {
				continue
			}

			if !itemConfigActiveLocked(cfg) {
				continue
			}

			sales := countRecentSales(item, now.Add(-cfg.AnalysisTime))
			totalStock := getItemCount(item) + getInventoryCount(item)
			if sales != 0 || totalStock != 0 {
				continue
			}

			current := data.Prices[item]
			if current <= 0 {
				continue
			}

			nacenka := getNacenka(item)
			target := floor + nacenka
			marker := current % 100
			target = (target/100)*100 + marker

			if current >= target {
				continue
			}

			log.Printf("[MARKET_FLOOR] %s: %d → %d (лот %d + наценка %d, окно %v, sales=0 stock=0)",
				item, current, target, floor, nacenka, span.Round(time.Second))
			data.Prices[item] = target
			dailyData.Prices[item] = target
			lastPriceUpdate[item] = time.Now()
			changed = true
		}

		mutex.Unlock()

		if !changed {
			return
		}

		publishPriceUpdate()
		saveDailyDataNoMessageUpdate()
	*/
}

func sellPriceFloor(minBuy, nacenka int) int {
	// base_price не пол: только старт, если цены ещё нет.
	// Пол = дешёвая покупка из истории + наценка (без истории — только наценка).
	return minBuy + nacenka
}

// ahBookRaiseTarget — селл = самый дешёвый ask из выборки + наша наценка + шаг.
func ahBookRaiseTarget(minAsk, nacenka, step int) int {
	if minAsk <= 0 {
		return 0
	}
	if step < 0 {
		step = 0
	}
	return minAsk + nacenka + step
}

// ahBookRaiseTargetCapped — полный таргет книги, но не больше sell+N×step за цикл.
func ahBookRaiseTargetCapped(sell, minAsk, nacenka, step int) int {
	full := ahBookRaiseTarget(minAsk, nacenka, step)
	if full <= 0 {
		return 0
	}
	if step <= 0 || ahBookMaxRaiseSteps <= 0 {
		return full
	}
	cap := sell + ahBookMaxRaiseSteps*step
	if full > cap {
		return cap
	}
	return full
}

// ahBookSoftDownTarget — к p10+наценка+шаг, но не ниже min(книги без витрин)+наценка.
func ahBookSoftDownTarget(p10, minAsk, nacenka, step int) int {
	if p10 <= 0 || minAsk <= 0 {
		return 0
	}
	if step < 0 {
		step = 0
	}
	tgt := p10 + nacenka + step
	floor := minAsk + nacenka
	if tgt < floor {
		tgt = floor
	}
	return tgt
}

// shouldRaiseFromAhBook — селл ниже min(окно)+наценка.
// v8p: только при held>0 и живом разборе (sales>buys, sales≥min); иначе книга тянет в пустоту (нагрудник 0.84→2.4).
// dump / ↓ цикла / были buys / тонкий скан — не трогаем.
func shouldRaiseFromAhBook(sell, minAsk, nacenka, n, sales, buys, held int, dumpZone, alreadyDown, hadBuys bool) bool {
	if n < ahBookMinLotsInWindow || minAsk <= 0 || dumpZone || alreadyDown || hadBuys {
		return false
	}
	if held <= 0 {
		return false
	}
	if sales <= buys || sales < corridorMinSalesForUp {
		return false
	}
	return sell < minAsk+nacenka
}

// shouldSoftDownFromAhBook — селл явно выше p10+наценка и выше min+наценка; ↑ цикла не трогаем.
// Триггер с люфтом 2×step; цель не ниже min+наценка (p10 может быть занижен дампами).
// v8q: всегда ≥40 uuid (тонкая книга на held=0 больше не сливает каталог ночью).
func shouldRaiseEmptyFromAhBook(sell, p10, minAsk, nacenka, n, step, held, buys int, alreadyDown, dumpZone bool) bool {
	if held > 0 || buys > 0 || alreadyDown || dumpZone {
		return false
	}
	if n < ahBookMinLotsInWindow || p10 <= 0 || minAsk <= 0 || step <= 0 {
		return false
	}
	marketFloor := maxInt(p10+nacenka, minAsk+nacenka)
	return marketFloor >= sell+2*step
}

func emptyBookRaiseTarget(sell, p10, minAsk, nacenka, step int) int {
	if step <= 0 {
		return sell
	}
	marketFloor := maxInt(p10+nacenka, minAsk+nacenka)
	tgt := sell + step
	if tgt > marketFloor {
		tgt = marketFloor
	}
	return tgt
}

// shouldSoftDownFromAhBook — селл явно выше p10+наценка и выше min+наценка; ↑ цикла не трогаем.
// Триггер с люфтом 2×step; цель не ниже min+наценка (p10 может быть занижен дампами).
// v8q: всегда ≥40 uuid (тонкая книга на held=0 больше не сливает каталог ночью).
func shouldSoftDownFromAhBook(sell, p10, minAsk, nacenka, n, step int, alreadyUp, hadBuys bool, held int) bool {
	if n < ahBookMinLotsInWindow || p10 <= 0 || minAsk <= 0 || alreadyUp {
		return false
	}
	if hadBuys && held > 0 {
		return false
	}
	bookFloor := minAsk + nacenka
	if sell <= bookFloor {
		return false
	}
	if step <= 0 {
		return sell > p10+nacenka
	}
	return sell > p10+nacenka+ahBookSoftDownSlackSteps*step
}

// emptyIdleAhSoftDownOK — явный выход из тупика EMPTY_IDLE + высокая цена vs толстая книга.
// Не снимает !emptyIdle с ordinary softDownFromBook; только эта ветка.
func emptyIdleAhSoftDownOK(emptyIdle, bookOK, p10OK bool, sell, p10, minAsk, nacenka, n, step int) bool {
	if !emptyIdle || !bookOK || !p10OK {
		return false
	}
	return shouldSoftDownFromAhBook(sell, p10, minAsk, nacenka, n, step, false, false, 0)
}

// emptyIdleAboveMarketBlocksUp — fresh trusted book говорит sell > p10+nac:
// empty_idle recovery UP не должен поднимать каталог «потому что held=0».
// Thin/no book — не трогаем (caller проверяет bookOK/p10OK).
func emptyIdleAboveMarketBlocksUp(emptyIdle, bookOK, p10OK bool, sell, p10, nacenka int) bool {
	if !emptyIdle || !bookOK || !p10OK || p10 <= 0 {
		return false
	}
	return sell > p10+nacenka
}

// ahBookSoftDownApply — цель книги; при held=0 не больше −N×step за цикл (анти-пила пустого).
func ahBookSoftDownApply(sell, softDownTgt, priceFloor, step, held int) int {
	if softDownTgt <= 0 || softDownTgt >= sell {
		return sell
	}
	tgt := softDownTgt
	if tgt < priceFloor {
		tgt = priceFloor
	}
	if held <= 0 && step > 0 && ahBookMaxRaiseSteps > 0 {
		cap := sell - ahBookMaxRaiseSteps*step
		if cap < priceFloor {
			cap = priceFloor
		}
		if tgt < cap {
			tgt = cap
		}
	}
	if tgt >= sell {
		return sell
	}
	return tgt
}

// countItemsInCategoryLocked — сколько id в items_config с данным go-типом. Только под mutex.Lock.
func countItemsInCategoryLocked(minecraftType string) int {
	n := 0
	for _, conf := range itemsConfig {
		if conf.Type == minecraftType {
			n++
		}
	}
	return n
}

// itemSlotShareLocked — доля слотов категории на один предмет:
// (32 × боты_в_категории) / число_предметов_в_категории.
// Только под mutex.Lock.
func itemSlotShareLocked(minecraftType string) int {
	bots := aggregateBotsPerTypeLocked()[minecraftType]
	nItems := countItemsInCategoryLocked(minecraftType)
	if bots <= 0 || nItems <= 0 {
		return 0
	}
	return (botTotalSlots * bots) / nItems
}

// hasSpaceToCoverBuyDeficit — хватает ли свободной доли предмета, чтобы докупить (sales−buys).
// free = share − totalHeld; need = sales − buys (только при buys < sales).
func hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys int) (ok bool, free, need int) {
	if buys >= sales {
		return false, 0, 0
	}
	need = sales - buys
	if share <= 0 {
		return false, 0, need
	}
	free = share - totalHeld
	if free < 0 {
		free = 0
	}
	return free >= need, free, need
}

// blockNacenkaRaise — покупок меньше продаж и место есть → наценку не поднимаем никогда.
func blockNacenkaRaise(share, totalHeld, sales, buys int) bool {
	ok, _, _ := hasSpaceToCoverBuyDeficit(share, totalHeld, sales, buys)
	return ok
}

func actionReasonRU(action string) string {
	switch action {
	case "corridor_price_down_dump":
		return "corridor_v8h: held ≥ dump% → −цена×2 (без продаж — макс. 1 щуп подряд)"
	case "corridor_price_down_over":
		return "corridor_v8u: OVER −2 step (sales≥1: DOI cover 3≤DOI<5; sales=0: fill≥over%)"
	case "corridor_hold_over_idle":
		return "corridor_v8h: перезапас, sales=0 после idle-щупа — ждём продажу"
	case "corridor_hold_doi_cover":
		return "corridor_v8u L2 F: sales≥1 DOI<3 или DOI≥10 → HOLD (fill soft/over/dump не режем)"
	case "corridor_hold_no_excess":
		return "corridor_v8ad: held ≤ targetHi → авто-↓ запрещён (grant)"
	case "corridor_hold_post_grant_c":
		return "corridor_v8ad Minimal C: mild+live (+1∧sales≥2) или zero1 mild → HOLD"
	case "corridor_price_up_paid":
		return "corridor_v8h: в полосе цена << lastPaid → ↑ к якорю (и ночью)"
	case "corridor_price_down_soft":
		return "corridor_v8u: SOFT −1 step (sales≥1: DOI cover 5≤DOI<10; sales=0: held>hi)"
	case "corridor_price_down_hi":
		return "corridor_v3(legacy): held > hi → −цена"
	case "corridor_price_up_demand":
		return "corridor_v8: held < lo, сильный спрос (день≥3 / ночь≥4) → +цена (v8t: выкл)"
	case "corridor_price_up_deep":
		return "corridor_v8h: held=0 днём + сильный спрос → +цена (обход up_cd); v8t: единственный sales>buys ↑"
	case "corridor_price_up_empty_inventory":
		return "corridor_v8ab(legacy): held=0 empty_inventory ↑ (выкл для PriceExplored; cold-start вместо этого)"
	case "corridor_price_up_cold_start":
		return "corridor_v8ae: !Explored ∧ s0b0 ∧ thick book ∧ our≪p10 → разведочный +1 step"
	case "corridor_price_up_v9_demand":
		return "corridor_v9: understock ∧ sales≥threshold ∧ sales>buys ∧ ratio<1.05 → +1"
	case "corridor_price_up_v9_empty_catchup":
		return "corridor_v9: empty_streak≥2 ∧ ratio<0.80 ∧ price+step≤p10 → +1"
	case "corridor_price_down_v9_soft", "corridor_price_down_v9_over", "corridor_price_down_v9_dump":
		return "corridor_v9: excess held ∧ ratio≥0.90 → ↓"
	case "corridor_hold_v9_no_signal", "corridor_hold_v9_band":
		return "corridor_v9: нет сигнала UP/DOWN"
	case "corridor_hold_v9_low_stock_down_veto":
		return "corridor_v9: held≤hi → DOWN запрещён"
	case "corridor_hold_v9_underprice_down_veto":
		return "corridor_v9: ratio<0.90 → DOWN запрещён"
	case "corridor_hold_v9_demand_above_market":
		return "corridor_v9: demand был, но ratio≥1.05"
	case "corridor_hold_v9_catchup_no_gap", "corridor_hold_v9_catchup_cap":
		return "corridor_v9: empty catchup без gap / выше market"
	case "corridor_price_up_empty_market_catchup":
		return "corridor_v8af: Explored ∧ empty ∧ thick book ∧ our/p10<0.85 streak≥2 → +1 catchup"
	case "corridor_price_up_recover", "corridor_price_up_recover_deep":
		return "corridor_v8p: недобор + цена << paid + sales≥1 → recover (v8t: выкл)"
	case "corridor_hold_demand_disabled":
		return "corridor_v8t: был бы up_demand (sales>buys), audit matched ≪ HOLD → hold"
	case "corridor_hold_recover_disabled":
		return "corridor_v8t: был бы up_recover/deep, audit без устойчивого + → hold"
	case "corridor_hold_paid_disabled":
		return "corridor_v8t: был бы up_paid (paid≠market), audit → hold"
	case "corridor_price_up_skim":
		return "corridor_v8o(legacy): held в полосе + sales≥buys+lead → +цена (выкл в v8s)"
	case "corridor_hold_skim_disabled":
		return "corridor_v8s: сигнал skim был, но up_skim выключен → hold (matched HOLD лучше ↑)"
	case "corridor_price_up_trusted_ah_min", "corridor_price_up_trusted_ah_min_shadow":
		return "corridor_v8t L1: held=0 buys=0, our≪trusted_AH_min → jump to min (без nacenka)"
	case "corridor_price_up_floor_escape_empty":
		return "corridor_v8v: held=sales=buys=0 near_floor → +1 step (floor_escape_empty)"
	case "corridor_price_up_floor_escape_deep_ah":
		return "corridor_v8v: held=sales=buys=0 deep_vs_AH → +1 step (floor_escape_deep_ah)"
	case "corridor_price_up_floor_escape_down_streak":
		return "corridor_v8v: near_floor + down_streak≥3 → +1 step (floor_escape_down_streak)"
	case "corridor_price_up_floor_escape_trusted_jump":
		return "corridor_v8v: deep pit + trusted book → jump to AH min (floor_escape_trusted_jump)"
	case "corridor_price_up_empty_idle":
		return "corridor_v8w EMPTY_IDLE: held=sales=buys=0, book thin/absent → +1 step recovery"
	case "corridor_hold_empty_idle":
		return "corridor_v8w EMPTY_IDLE: ↓ blocked (нет стока/оборота ≠ bearish); ждём recovery UP"
	case "corridor_price_down_skim_revert":
		return "corridor_v8o: после ↑ try/buy показывают отказ → soft-↓ в полосе"
	case "corridor_price_up_floor":
		return "corridor_v8c: цена ниже пола (minBuy+наценка) → поднимаем"
	case "corridor_price_up_ah_book":
		return "corridor_v8p: селл < min(ah_book)+наценка, held>0, sales>buys → ≤+N×step к книге"
	case "corridor_price_up_market_recovery", "corridor_price_up_market_recovery_shadow":
		return "corridor_v8s+mr: B_price_trap held=buys=sales=0, our≪p10 (60m) → +1 step (не к p10)"
	case "corridor_price_up_empty_book":
		return "corridor_v8r: held=0 и глубокая книга выше нас ≥2 step → +1 step к рынку"
	case "corridor_price_down_ah_book":
		return "corridor_v8y: селл > p10+наценка, ≥40 uuid; held>0 или empty_idle_ah_soft_down → ≤−N×step/цикл"
	case "corridor_price_down_overcap":
		return "corridor_v8q: недобор + сток + try-отказ + цена ≥ paid+K×step → soft-↓"
	case "corridor_price_down_stale":
		return "corridor_v8q: нет sell в TTL, но held>0 и try-отказ → soft-↓ (не пилим пустое)"
	case "corridor_price_down_empty_fair":
		return "corridor_v8q: disabled — held=0 больше не ↓ (см. hold_empty)"
	case "corridor_hold_recover_ceiling":
		return "corridor_v8e: recover упёрся в max(sell за окно)+K×step"
	case "corridor_hold_recover_stale":
		return "corridor_v8e: нет продаж в окне — recover ↑ запрещён (якорь протух)"
	case "corridor_hold_recover_fair":
		return "corridor_v8m: недобор, но цена не << paid — recover ↑ запрещён (не vacuum-↑)"
	case "corridor_hold_recover_pause":
		return "corridor_v8e: слишком много recover-↑ без buys → пауза (без resume)"
	case "corridor_hold_band":
		return "corridor_v8: held в полосе, нет сигнала для skim → hold"
	case "corridor_hold_skim_veto":
		return "corridor_v8: в полосе, но try/buy/weak → skim ↑ запрещён"
	case "corridor_hold_hysteresis":
		return "corridor_v3(legacy): held 25–28% → hold"
	case "corridor_hold_filling":
		return "corridor_v8: held < lo, идут buys, sales=0 → hold (набираем сток)"
	case "corridor_hold_dead":
		return "corridor_v8: held < lo, sales=0 buys=0, не у пола → hold"
	case "corridor_hold_empty":
		return "corridor_v8: held=0 высоко над полом → ↑ запрещён (vacuum)"
	case "corridor_hold_weak_demand":
		return "corridor_v8: held < lo, спрос слабый → ↑ запрещён"
	case "corridor_hold_up_cap":
		return "corridor_v8: недобор, но лимит ↑ подряд → hold"
	case "corridor_hold_up_cd":
		return "corridor_v8: недобор, но ↑ на cooldown → hold"
	case "corridor_hold_try_veto":
		return "corridor_v8: недобор, но try_sells≫sales → ↑ запрещён"
	case "corridor_hold_buy_veto":
		return "corridor_v8: недобор, buys>sales → ↑ запрещён"
	case "corridor_hold_overshoot":
		return "corridor_v6: soft↓ пропустил бы ниже lo → hold"
	case "corridor_hold_down_cd":
		return "corridor_v6: soft↓ на cooldown → hold"
	case "price_down_buy_surge":
		return "surge: всплеск buys при стоке ≥ цели → −цена"
	// legacy (старые логи/БД)
	case "classic_price_up":
		return "classic(legacy): sales < normal && stock ≤ normal && onAH < normal → +цена"
	case "classic_price_down_weak_sales":
		return "classic(legacy): АХ > sales и АХ > нормы при слабых sales → −цена"
	case "classic_price_down_buy_excess":
		return "classic(legacy): buys > 2×sales и запас > нормы → −цена"
	case "classic_price_down_leader":
		return "classic(legacy): лидер категории, запас > 3×sales → −цена"
	case "oldoldold_price_up":
		return "oldoldold: АХ+инв < normal_sales → +цена"
	case "oldoldold_price_down":
		return "oldoldold: АХ > sales и АХ > нормы при слабых sales → −цена"
	case "capital_hold":
		return "капитал: hold — нет давления"
	case "capital_dump":
		return "капитал: dump — мёртвые sales / hog → −цена"
	case "capital_fill":
		return "капитал: fill — сток ниже цели → −наценка"
	case "capital_fill_price":
		return "капитал: +P — SOLO demand (спрос) или MULTI underbuy"
	case "capital_skim":
		return "капитал: skim — поток ок → +наценка"
	case "capital_rollback":
		return "капитал: откат неудачного skim"
	case "hold":
		return "hold"
	case "skip_inactive":
		return "пропуск: нет активных ботов этого типа"
	case "skip_manual":
		return "пропуск: min/max от оркестратора — цикл на паузе"
	case "hold_manual_min":
		return "hold: после min можно только ↑ — ↓ заблокирован"
	case "hold_manual_max":
		return "hold: после max можно только ↓ — ↑ заблокирован"
	case "hold_manual_set":
		return "hold: ручная цена с /sales — ↑↓ заблокированы на цикл"
	case "experiment_ok", "experiment_rollback", "experiment_start":
		return action
	default:
		if action == "" {
			return "нет решения"
		}
		return action
	}
}

// manualDirectionClampLocked — в окне AnalysisTime после set_min/set_max:
// min → блок ↓; max → блок ↑. Неизвестный kind → блок обоих (старое поведение).
// Только под mutex.Lock.
func manualDirectionClampLocked(item string, window time.Duration) (blockUp, blockDown bool) {
	t, ok := data.LastManualUpdate[item]
	if !ok || t.IsZero() || time.Since(t) >= window {
		return false, false
	}
	switch data.LastManualKind[item] {
	case "min":
		return false, true
	case "max":
		return true, false
	case "set":
		return true, true
	default:
		return true, true
	}
}

// BuySurgeEvent — мгновенное снижение цены из‑за всплеска покупок.
type BuySurgeEvent struct {
	Dropped     bool
	Item        string
	PriceBefore int
	PriceAfter  int
	SurgeCount  int
	Sales       int
	Threshold   int
	NormalSales int
	Step        int
}

// maybeBuySurgePriceDownLocked — на каждый buy: счётчик +=1;
// если сток уже ≥ верхней цели коридора и счётчик ≥ hi → −step, счётчик = 0.
// Не зависит от NormalSales. Только под mutex.Lock.
func maybeBuySurgePriceDownLocked(item string) BuySurgeEvent {
	ev := BuySurgeEvent{Item: item}
	cfg, ok := itemsConfig[item]
	if !ok || cfg.PriceStep <= 0 {
		return ev
	}
	if !itemConfigActiveLocked(cfg) {
		return ev
	}
	if _, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime); blockDown {
		return ev
	}

	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	data.BuySurgeCount[item]++
	surgeCount := data.BuySurgeCount[item]

	share := itemSlotShareLocked(cfg.Type)
	_, targetHi, soft, _, _ := stockTargets(share, stockBandFor(item, cfg))
	threshold := maxInt(4, soft)
	held := getItemCount(item) + getInventoryCount(item)

	now := time.Now()
	since := now.Add(-cfg.AnalysisTime)
	if t, ok := data.LastCycleAt[item]; ok && !t.IsZero() {
		elapsed := now.Sub(t)
		if elapsed > 0 && elapsed <= cfg.AnalysisTime+time.Minute {
			since = t
		}
	}
	sales := countRecentSales(item, since)

	ev.SurgeCount, ev.Sales, ev.Threshold = surgeCount, sales, threshold
	ev.NormalSales, ev.Step = cfg.NormalSales, cfg.PriceStep

	// Surge↓ только если уже выше гистерезиса soft (не с края полосы).
	if surgeCount < threshold || held < soft {
		return ev
	}
	// v8ad: grant + Minimal C (mild+live uses cycle sales; zero1-streak N/A on surge path).
	if !automaticDownAllowed(held, targetHi) || postGrantCBlockDown(held, targetHi, sales, 0, 0) {
		return ev
	}

	priceBefore := data.Prices[item]
	if priceBefore <= 0 {
		return ev
	}
	nacenka := getNacenka(item)
	minBuy := getMinPriceFromHistory(item)
	floor := sellPriceFloor(minBuy, nacenka)
	newPrice := priceBefore - cfg.PriceStep
	if newPrice < floor {
		newPrice = floor
	}
	if newPrice >= priceBefore {
		return ev
	}

	data.Prices[item] = newPrice
	dailyData.Prices[item] = newPrice
	lastPriceUpdate[item] = now
	data.BuySurgeCount[item] = 0

	ev.Dropped = true
	ev.PriceBefore = priceBefore
	ev.PriceAfter = newPrice
	log.Printf("[SURGE] %s: surge=%d thr=%d held=%d soft=%d sales=%d | цена %d→%d | surge→0",
		item, surgeCount, threshold, held, soft, sales, priceBefore, newPrice)
	return ev
}

func adjustPrice(item string) AdjustReport {
	cfg, ok := itemsConfig[item]
	if !ok {
		return AdjustReport{Item: item, Action: "skip", Reason: "нет в items_config", Skipped: true}
	}

	now := time.Now()
	// Сеть + sqlite до mutex.Lock — иначе клинит весь WS/HTTP на ping FunTime / БД.
	tryAdvanceCategoryMLOutcomes(cfg.Type, now)
	tryAdvanceCapitalForwards(now)
	onlineForCap, onlineMaxForML := fetchOnlineSnapshot()

	mutex.Lock()
	swordTimes[item] = now
	if data.LastCycleAt == nil {
		data.LastCycleAt = make(map[string]time.Time)
	}
	prevCycleAt := data.LastCycleAt[item]
	data.LastCycleAt[item] = now
	// новый цикл — только surge-счётчик, статы цикла не трогаем
	if data.BuySurgeCount == nil {
		data.BuySurgeCount = make(map[string]int)
	}
	delete(data.BuySurgeCount, item)
	lastUpdate := now.Add(-cfg.AnalysisTime)
	// продолжение прерванного цикла: окно от прошлого якоря (не если просрочили >1м)
	if !prevCycleAt.IsZero() {
		elapsed := now.Sub(prevCycleAt)
		if elapsed > 0 && elapsed <= cfg.AnalysisTime+time.Minute {
			lastUpdate = prevCycleAt
		}
	}

	rep := AdjustReport{Item: item, NormalSales: cfg.NormalSales, Step: cfg.PriceStep}

	if !itemConfigActiveLocked(cfg) {
		log.Printf("[SKIP] %s: тип %s — нет активных ботов", item, cfg.Type)
		mutex.Unlock()
		rep.Action = "skip_inactive"
		rep.Reason = actionReasonRU(rep.Action)
		rep.Skipped = true
		return rep
	}

	ensureNacenkasInitialized()

	sales := countRecentSales(item, lastUpdate)
	buys := countRecentBuys(item, lastUpdate)
	trySells := countRecentTrySells(item, lastUpdate)
	profitNow := profitInWindow(item, lastUpdate)
	state := data.AdjustState[item]

	newPrice := data.Prices[item]
	priceBefore := newPrice
	nacenka := getNacenka(item)
	nacenkaBefore := nacenka
	step := cfg.PriceStep
	minPrice := getMinPriceFromHistory(item)
	nacenkaSumNow := nacenkaSumInWindow(item, lastUpdate)
	nacenkaSumPrev := state.LastCycleNacenkaSum
	priceFloor := sellPriceFloor(minPrice, nacenka)
	paidSince := now.Add(-maxAnalysisRetain())
	paidMax, _ := lastPaidSellMax(data.TradeHistory[item], now, paidSince)
	overCap := recoverBlockedByPaidCap(priceBefore, paidMax, step)

	ahCounts := make(map[string]int)
	invCounts := make(map[string]int)

	for _, items := range clientItems {
		for name, count := range items {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				ahCounts[name] += count
			}
		}
	}
	for _, inv := range clientInventory {
		for name, count := range inv {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				invCounts[name] += count
			}
		}
	}

	stockNorm := stockNormFromConfig(cfg)

	onAH := ahCounts[item]
	invCount := invCounts[item]
	totalHeld := onAH + invCount

	share := itemSlotShareLocked(cfg.Type)
	free, need := 0, 0
	if buys < sales && share > 0 {
		free = share - totalHeld
		if free < 0 {
			free = 0
		}
		need = sales - buys
	}

	stockLoad := 0.0
	if share > 0 {
		stockLoad = float64(totalHeld) / float64(share)
	}
	underbuyOK := buys < sales && share > 0 && free >= need
	tryRatio := 0.0
	if sales > 0 {
		tryRatio = float64(trySells) / float64(sales)
	}

	changed := false
	action := ""
	var notes []string
	var experimentTG *experimentTelegramEvent

	if isPricingPolicyV9() {
		return adjustPriceV9(
			item, cfg, now, lastUpdate,
			sales, buys, trySells, profitNow,
			state,
			priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor,
			onAH, invCount, totalHeld, share, free, need, stockNorm,
			underbuyOK, tryRatio, stockLoad,
			onlineForCap, onlineMaxForML,
		)
	}

	// ═══════════════════════════════════════════════════════════════════
	// stock_corridor_v8u — L2 Policy F (sales≥1 DOI cover) + legacy sales=0 fill↓.
	// L1 trusted_AH_min / floor / disabled UP — без изменений в этом блоке.
	// (v8af path; не трогать при работе над v9)
	// ═══════════════════════════════════════════════════════════════════

	band := stockBandFor(item, cfg)
	targetLo, targetHi, targetSoft, targetOver, targetDump := stockTargets(share, band)
	prevCycleSales := state.LastCycleSales // до обновления в конце цикла
	zeroStreak := zeroSalesExcessStreakNow(sales, totalHeld, targetHi, state.ZeroSalesExcessStreak)
	cDownCandidate := postGrantCDownCandidate(totalHeld, targetHi, sales, buys, zeroStreak)
	nightMSK := isNightMSK(now)
	botsInCat := aggregateBotsPerTypeLocked()[cfg.Type]
	minSalesForUp := demandMinSalesForUp(botsInCat, nightMSK)

	if buys < sales {
		need = sales - buys
		free = share - totalHeld
		if free < 0 {
			free = 0
		}
		underbuyOK = share > 0 && free >= need
	} else {
		underbuyOK = false
	}

	if sales > 0 {
		tryRatio = float64(trySells) / float64(sales)
	} else if trySells > 0 {
		tryRatio = float64(trySells)
	} else {
		tryRatio = 0
	}

	applyDown := func(label, note string, stepMult int) {
		// Grant: held ≤ hi → авто-↓ запрещён.
		if !automaticDownAllowed(totalHeld, targetHi) {
			notes = append(notes, note+fmt.Sprintf(
				" · blocked no-excess (held=%d ≤ hi=%d → авто-↓ запрещён)", totalHeld, targetHi))
			if action == "" || action == "hold" || strings.Contains(action, "price_down") {
				action = "corridor_hold_no_excess"
			}
			return
		}
		// Minimal C: mild+live / zero1 mild → HOLD.
		if postGrantCBlockDown(totalHeld, targetHi, sales, buys, zeroStreak) {
			notes = append(notes, note+fmt.Sprintf(
				" · blocked post_grant_C (held=%d hi=%d excess=+%d sales=%d buys=%d zeroStreak=%d)",
				totalHeld, targetHi, excessDepth(totalHeld, targetHi), sales, buys, zeroStreak))
			if action == "" || action == "hold" || strings.Contains(action, "price_down") {
				action = "corridor_hold_post_grant_c"
			}
			return
		}
		// EMPTY_IDLE: нет стока и нет оборота — любой ↓ запрещён (не bearish).
		if isEmptyIdle(totalHeld, sales, buys) {
			notes = append(notes, note+" · blocked empty_idle (held=sales=buys=0 → ↓ запрещён)")
			if action == "" || action == "hold" {
				action = "corridor_hold_empty_idle"
			}
			return
		}
		// v8q belt: stale/overcap/empty_fair никогда не пилят пустой каталог.
		if isGhostCatalogDown(label) && !ghostPriceDownOK(totalHeld, sales, trySells) {
			notes = append(notes, note+" · blocked ghost-↓ (нужен held+try-отказ)")
			if action == "" || action == "hold" {
				action = "corridor_hold_recover_stale"
			}
			return
		}
		if stepMult < 1 {
			stepMult = 1
		}
		cand := priceBefore - step*stepMult
		if cand < priceFloor {
			cand = priceFloor
		}
		if cand < priceBefore {
			newPrice = cand
			action = label
			changed = true
			notes = append(notes, note)
		} else {
			notes = append(notes, note+" · floor")
			if action == "" {
				action = "hold"
			}
		}
	}
	applyUp := func(label, note string) {
		newPrice = priceBefore + step
		action = label
		changed = true
		notes = append(notes, note)
	}

	trySoftDown := func(label, note string) {
		if downWouldUndershoot(totalHeld, sales, targetLo) {
			action = "corridor_hold_overshoot"
			notes = append(notes, note+" · overshoot-guard")
			return
		}
		if state.CorridorDownCooldown > 0 {
			action = "corridor_hold_down_cd"
			notes = append(notes, fmt.Sprintf("%s · down_cd=%d", note, state.CorridorDownCooldown))
			return
		}
		applyDown(label, note, 1)
		if changed && strings.Contains(action, "price_down") {
			state.CorridorDownCooldown = corridorSoftDownEvery - 1
			if state.CorridorDownCooldown < 0 {
				state.CorridorDownCooldown = 0
			}
		}
	}

	switch {
	case share <= 0:
		action = "hold"
		notes = append(notes, "share=0 — нет базы для коридора")

	// ─── Level-2 Policy F (sales≥1): DOI cover before any fill-based soft/over/dump ───
	case sales >= 1 && doiCoverIntensity(totalHeld, sales) == "over":
		doi := doiCover(totalHeld, sales)
		applyDown("corridor_price_down_over",
			fmt.Sprintf("doi_cover F: DOI=%.3f held=%d sales=%d fill=%.1f%% → OVER −%d×step (3≤DOI<5) price %d",
				doi, totalHeld, sales, stockLoad*100, corridorHardDownStepMult, priceBefore),
			corridorHardDownStepMult)

	case sales >= 1 && doiCoverIntensity(totalHeld, sales) == "soft":
		doi := doiCover(totalHeld, sales)
		trySoftDown("corridor_price_down_soft",
			fmt.Sprintf("doi_cover F: DOI=%.3f held=%d sales=%d fill=%.1f%% → SOFT −1 step (5≤DOI<10) price %d",
				doi, totalHeld, sales, stockLoad*100, priceBefore))

	// sales≥1 + DOI HOLD, but fill would have triggered legacy soft/over/dump → explicit HOLD
	case sales >= 1 && (totalHeld >= targetDump || totalHeld >= targetOver || totalHeld > targetHi):
		doi := doiCover(totalHeld, sales)
		inten := doiCoverIntensity(totalHeld, sales)
		action = "corridor_hold_doi_cover"
		notes = append(notes, fmt.Sprintf(
			"doi_cover F: DOI=%.3f inten=%s held=%d sales=%d fill=%.1f%% → HOLD (legacy fill-↓ skipped; DOI<3 or DOI≥10)",
			doi, inten, totalHeld, sales, stockLoad*100))

	// ─── sales==0: legacy fill dump/over/soft (unchanged; C may bypass idleHard) ───
	case sales == 0 && totalHeld >= targetDump:
		if !allowHardDown(sales, state.IdleHardDownStreak, stockLoad) && !cDownCandidate {
			action = "corridor_hold_dump_idle"
			notes = append(notes, fmt.Sprintf("held=%d ≥ dump=%d sales=0 idleHard=%d — ждём продажу, не цепочку в пол", totalHeld, targetDump, state.IdleHardDownStreak))
		} else {
			note := fmt.Sprintf("held=%d ≥ dump=%d (%.0f%% share=%d ×%d)",
				totalHeld, targetDump, stockLoad*100, share, corridorHardDownStepMult)
			if cDownCandidate && !allowHardDown(sales, state.IdleHardDownStreak, stockLoad) {
				note += " · post_grant_C idleHard bypass"
			}
			applyDown("corridor_price_down_dump", note, corridorHardDownStepMult)
			state.CorridorDownCooldown = 0
		}

	case sales == 0 && totalHeld >= targetOver:
		if !allowHardDown(sales, state.IdleHardDownStreak, stockLoad) && !cDownCandidate {
			action = "corridor_hold_over_idle"
			notes = append(notes, fmt.Sprintf("held=%d ≥ over=%d sales=0 idleHard=%d — ждём продажу, не цепочку в пол", totalHeld, targetOver, state.IdleHardDownStreak))
		} else {
			note := fmt.Sprintf("held=%d ≥ over=%d (%.0f%% share=%d ×%d)",
				totalHeld, targetOver, stockLoad*100, share, corridorHardDownStepMult)
			if cDownCandidate && !allowHardDown(sales, state.IdleHardDownStreak, stockLoad) {
				note += " · post_grant_C idleHard bypass"
			}
			applyDown("corridor_price_down_over", note, corridorHardDownStepMult)
		}

	case sales == 0 && totalHeld > targetHi:
		trySoftDown("corridor_price_down_soft",
			fmt.Sprintf("held=%d > hi=%d (полоса [%d,%d] soft=%d share=%d)",
				totalHeld, targetHi, targetLo, targetHi, targetSoft, share))

	case totalHeld < targetLo:
		deep := deepUnderstock(totalHeld, targetLo)
		// Цена занижена vs свежий paid — иначе недобор ≠ повод recover-↑ (vacuum).
		underpriced := priceFarBelowPaid(priceBefore, paidMax, step)
		// deep обход cd: только с живым спросом (не underpriced-only — это vacuum deep-recover).
		bypassUpCD := deep && !nightMSK && sales > buys
		// buy-veto только когда реально набиваем сток сильнее продаж.
		// buys==sales (в т.ч. 1=1 при held≈0) — рынок забирает всё → ↑ можно.
		liveBuyVeto := buys > sales
		noBuyBlocked := state.CorridorNoBuyUpStreak >= corridorMaxNoBuyUps
		// recover: недобор + цена << paid + sales≥1, нет try/buy-veto, не ночь.
		recoverCDOK := state.CorridorUpCooldown == 0 || bypassUpCD
		recoverStreakOK := state.CorridorUpStreak < corridorMaxUpStreak || bypassUpCD
		recoverBase := !nightMSK &&
			recoverCDOK &&
			recoverStreakOK &&
			!trySellsBlockUp(sales, trySells) &&
			!liveBuyVeto &&
			!noBuyBlocked &&
			!overCap
		recoverOK := recoverBase && underpriced && sales >= 1
		switch {
		case trySellsBlockUp(sales, trySells):
			action = "corridor_hold_try_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d try=%d — рынок не берёт, ↑ запрещён",
				totalHeld, targetLo, sales, trySells))
		case liveBuyVeto:
			action = "corridor_hold_buy_veto"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d > sales=%d — набиваем сток, ↑ запрещён",
				totalHeld, targetLo, buys, sales))
		case sales > buys && state.CorridorUpCooldown > 0 && !bypassUpCD:
			action = "corridor_hold_up_cd"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d но up_cd=%d — пауза после ↑",
				totalHeld, targetLo, sales, state.CorridorUpCooldown))
		case sales > buys && !demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK):
			action = "corridor_hold_weak_demand"
			if nightMSK {
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d last=%d night — нужен sales≥%d, ↑ запрещён",
					totalHeld, targetLo, sales, prevCycleSales, minSalesForUp))
			} else {
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=%d last=%d — слабый спрос, ↑ запрещён",
					totalHeld, targetLo, sales, prevCycleSales))
			}
		case sales > buys && deep && !nightMSK && (state.CorridorUpStreak < corridorMaxUpStreak || bypassUpCD):
			applyUp("corridor_price_up_deep",
				fmt.Sprintf("held=0 < lo=%d sales=%d > buys=%d — deep-↑ (мало стока, мало покупок)",
					targetLo, sales, buys))
		case sales > buys && corridorDemandUpEnabled && !nightMSK &&
			(state.CorridorUpStreak < corridorMaxUpStreak || bypassUpCD) &&
			(state.CorridorUpCooldown == 0 || bypassUpCD):
			applyUp("corridor_price_up_demand",
				fmt.Sprintf("held=%d < lo=%d sales=%d > buys=%d — demand-↑ (мало стока, мало покупок)",
					totalHeld, targetLo, sales, buys))
		case sales > buys:
			action = "corridor_hold_demand_disabled"
			notes = append(notes, fmt.Sprintf(
				"held=%d < lo=%d sales=%d > buys=%d — был бы up_demand, выкл",
				totalHeld, targetLo, sales, buys))
		case recoverOK && corridorRecoverUpEnabled:
			label := "corridor_price_up_recover"
			note := fmt.Sprintf("held=%d < lo=%d sales=%d buys=%d price=%d << paid=%d cap=%d noBuyUps=%d/%d — recover-↑",
				totalHeld, targetLo, sales, buys, priceBefore, paidMax, recoverPaidCap(paidMax, step),
				state.CorridorNoBuyUpStreak, corridorMaxNoBuyUps)
			if bypassUpCD && (state.CorridorUpCooldown > 0 || state.CorridorUpStreak >= corridorMaxUpStreak) {
				label = "corridor_price_up_recover_deep"
				note = fmt.Sprintf("held=%d < lo=%d price=%d << paid=%d deep recover-↑ noBuyUps=%d/%d",
					totalHeld, targetLo, priceBefore, paidMax, state.CorridorNoBuyUpStreak, corridorMaxNoBuyUps)
			}
			applyUp(label, note)
		case recoverOK:
			action = "corridor_hold_recover_disabled"
			notes = append(notes, fmt.Sprintf(
				"held=%d < lo=%d sales=%d price=%d << paid=%d — был бы recover-↑, v8t hold",
				totalHeld, targetLo, sales, priceBefore, paidMax))
		case recoverBase && underpriced && sales < 1:
			action = "corridor_hold_recover_fair"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d price=%d << paid=%d но sales=0 — recover ↑ нет (нет разбора)",
				totalHeld, targetLo, priceBefore, paidMax))
		case recoverBase && !underpriced && totalHeld <= 0 && sales == 0:
			// v8q: пусто ≠ дорого. Призрак 3.8M чинит ah_book soft-↓, не пиление пола.
			action = "corridor_hold_empty"
			notes = append(notes, fmt.Sprintf("held=0 < lo=%d price=%d paid=%d — пусто, каталог не ↓ (нет стока ≠ выше рынка)",
				targetLo, priceBefore, paidMax))
		case recoverBase && !underpriced:
			action = "corridor_hold_recover_fair"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d price=%d paid=%d — недобор, но не << paid (−%d×step), recover ↑ нет",
				totalHeld, targetLo, priceBefore, paidMax, corridorPaidBandGapSteps))
		case overCap && paidMax <= 0:
			if ghostPriceDownOK(totalHeld, sales, trySells) && priceBefore > priceFloor+2*step {
				applyDown("corridor_price_down_stale",
					fmt.Sprintf("held=%d < lo=%d try=%d — нет sell за %s, рынок не берёт листинг → ↓",
						totalHeld, targetLo, trySells, maxAnalysisRetain().Round(time.Minute)), 1)
			} else {
				action = "corridor_hold_recover_stale"
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d — нет sell за %s, ↓ нет (нужен сток+try-отказ)",
					totalHeld, targetLo, maxAnalysisRetain().Round(time.Minute)))
			}
		case overCap:
			if ghostPriceDownOK(totalHeld, sales, trySells) {
				applyDown("corridor_price_down_overcap",
					fmt.Sprintf("held=%d < lo=%d try=%d price=%d ≥ paid %d + %d×step → ↓ с потолка",
						totalHeld, targetLo, trySells, priceBefore, paidMax, corridorRecoverProbeSteps), 1)
			} else {
				action = "corridor_hold_recover_ceiling"
				notes = append(notes, fmt.Sprintf("held=%d < lo=%d price=%d ≥ paid-cap — ↓ нет (held=0 или нет try-отказа)",
					totalHeld, targetLo, priceBefore))
			}
		case noBuyBlocked:
			action = "corridor_hold_recover_pause"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d — пауза recover: %d ↑ без buys (антиvacuum)",
				totalHeld, targetLo, state.CorridorNoBuyUpStreak))
		case totalHeld <= 0:
			action = "corridor_hold_empty"
			notes = append(notes, fmt.Sprintf("held=0 < lo=%d — пусто, recover сейчас недоступен", targetLo))
		case buys > 0:
			action = "corridor_hold_filling"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d buys=%d sales=0 — набираем сток", totalHeld, targetLo, buys))
		default:
			action = "corridor_hold_dead"
			notes = append(notes, fmt.Sprintf("held=%d < lo=%d sales=0 buys=0 — hold", totalHeld, targetLo))
		}

	default:
		// В полосе: не мёртвая зона. Если витрина стабильно разбирается —
		// пробуем ↑ (skim), иначе залипаем в дешёвом локальном оптимуме.
		// v8o: lead sales−buys, жёстче try-veto, soft-↓ откат после неудачного ↑.
		skimTryBlocked := trySellsBlockSkim(sales, trySells)
		skimOK := !nightMSK &&
			state.CorridorUpCooldown == 0 &&
			state.CorridorUpStreak < corridorMaxUpStreak &&
			skimSalesLeadOK(sales, buys) &&
			demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) &&
			!trySellsBlockUp(sales, trySells) &&
			!skimTryBlocked
		paidClimb := priceFarBelowPaid(priceBefore, paidMax, step) &&
			buys <= sales &&
			!trySellsBlockUp(sales, trySells) &&
			state.CorridorNoBuyUpStreak < corridorMaxNoBuyUps &&
			state.CorridorUpCooldown == 0
		switch {
		case skimShouldRevert(state.CorridorUpCooldown, sales, buys, trySells):
			trySoftDown("corridor_price_down_skim_revert",
				fmt.Sprintf("held=%d в [%d,%d] после ↑ sales=%d buys=%d try=%d — рынок не берёт → откат",
					totalHeld, targetLo, targetHi, sales, buys, trySells))
		case trySellsBlockUp(sales, trySells) || skimTryBlocked:
			action = "corridor_hold_skim_veto"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] sales=%d try=%d — рынок не берёт, skim ↑ запрещён",
				totalHeld, targetLo, targetHi, sales, trySells))
		case buys > sales:
			action = "corridor_hold_skim_veto"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] buys=%d > sales=%d — набиваем, skim ↑ запрещён",
				totalHeld, targetLo, targetHi, buys, sales))
		case paidClimb && corridorPaidClimbEnabled:
			applyUp("corridor_price_up_paid",
				fmt.Sprintf("held=%d в [%d,%d] price=%d << paid=%d night=%v — ↑ к якорю",
					totalHeld, targetLo, targetHi, priceBefore, paidMax, nightMSK))
		case paidClimb:
			action = "corridor_hold_paid_disabled"
			notes = append(notes, fmt.Sprintf(
				"held=%d в [%d,%d] price=%d << paid=%d — был бы up_paid, v8t hold (paid≠market)",
				totalHeld, targetLo, targetHi, priceBefore, paidMax))
		case nightMSK:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] night — skim выкл", totalHeld, targetLo, targetHi))
		case state.CorridorUpCooldown > 0:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] up_cd=%d — пауза skim",
				totalHeld, targetLo, targetHi, state.CorridorUpCooldown))
		case !demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) || !skimSalesLeadOK(sales, buys):
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] sales=%d buys=%d last=%d lead<%d — нет сигнала skim",
				totalHeld, targetLo, targetHi, sales, buys, prevCycleSales, corridorSkimMinLead))
		case skimOK:
			if corridorSkimEnabled {
				applyUp("corridor_price_up_skim",
					fmt.Sprintf("held=%d в [%d,%d] sales=%d ≥ buys=%d+%d last=%d — skim-↑ (probe прибыли)",
						totalHeld, targetLo, targetHi, sales, buys, corridorSkimMinLead, prevCycleSales))
			} else {
				// v8s: исторически up_skim ≪ matched HOLD; не подбираем новый порог.
				action = "corridor_hold_skim_disabled"
				notes = append(notes, fmt.Sprintf(
					"held=%d в [%d,%d] sales=%d ≥ buys=%d+%d last=%d — был бы skim-↑, v8s hold",
					totalHeld, targetLo, targetHi, sales, buys, corridorSkimMinLead, prevCycleSales))
			}
		default:
			action = "corridor_hold_band"
			notes = append(notes, fmt.Sprintf("held=%d в [%d,%d] share=%d", totalHeld, targetLo, targetHi, share))
		}
	}

	dumpZone := totalHeld >= targetDump
	alreadyDown := strings.Contains(action, "price_down")
	bookSince := now.Add(-ahBookRaiseWindow)
	mrBookSince := now.Add(-marketRecoveryBookWindow)
	// sqlite книги — вне mutex.Lock (иначе WS/sales клинят на ah_book + mlDBMu).
	mutex.Unlock()
	minAsk, bookN, bookOK := ahBookMinSince(item, bookSince)
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	mrBook := ahBookMarketRecoveryStats(item, mrBookSince)
	tmBook := ahBookTrustedSellerMin(item, bookSince, trustedMinDiscoveryNearSteps, step)
	wouldRaiseFromBook := bookOK && shouldRaiseFromAhBook(priceBefore, minAsk, nacenka, bookN, sales, buys, totalHeld, dumpZone, alreadyDown, buys > 0)
	wouldRaiseEmptyFromBook := bookOK && p10OK && shouldRaiseEmptyFromAhBook(priceBefore, p10, minAsk, nacenka, minInt(bookN, p10N), step, totalHeld, buys, alreadyDown, dumpZone)
	raiseFromBook := ahBookPriceUpEnabled && wouldRaiseFromBook
	raiseEmptyFromBook := ahBookPriceUpEnabled && wouldRaiseEmptyFromBook
	if !ahBookPriceUpEnabled && (wouldRaiseFromBook || wouldRaiseEmptyFromBook) {
		notes = append(notes, "ah_book/empty_book ↑ disabled (книга наебывает)")
	}
	var raiseTgt int
	if raiseFromBook {
		raiseTgt = ahBookRaiseTargetCapped(priceBefore, minAsk, nacenka, step)
	} else if raiseEmptyFromBook {
		raiseTgt = emptyBookRaiseTarget(priceBefore, p10, minAsk, nacenka, step)
	}
	emptyIdle := isEmptyIdle(totalHeld, sales, buys)
	// Обычный AH soft-↓: empty_idle по-прежнему блокирует.
	softDownFromBook := !emptyIdle && bookOK && p10OK &&
		shouldSoftDownFromAhBook(priceBefore, p10, minAsk, nacenka, p10N, step, raiseFromBook, buys > 0, totalHeld)
	// Явный выход: EMPTY_IDLE + толстая книга + цена ≫ рынок → тот же soft-↓ actuator.
	emptyIdleAhSoftDown := emptyIdleAhSoftDownOK(emptyIdle, bookOK, p10OK, priceBefore, p10, minAsk, nacenka, p10N, step)
	var softDownTgt int
	if softDownFromBook || emptyIdleAhSoftDown {
		softDownTgt = ahBookSoftDownTarget(p10, minAsk, nacenka, step)
	}
	mutex.Lock()
	// Cold-start discovery / empty_inventory (v8ae):
	//   Explored → empty_inventory_up выкл (обычный s0b0 не лезет вверх «потому что пусто»).
	//   !Explored → только cold-start +1 при thick book + deep gap (не multi-step).
	blockUpEI, _ := manualDirectionClampLocked(item, cfg.AnalysisTime)
	if totalHeld > 0 {
		state.EmptyInventoryClimbSteps = 0
		state.EmptyInventoryAnchorPrice = 0
		state.EmptyMarketGapStreak = 0
	}
	if sales > 0 {
		markPriceExploredLocked(&state, "sell")
		state.EmptyMarketGapStreak = 0
	}
	thickCold := coldStartThickBook(bookOK, p10OK, bookN, p10)
	if !state.PriceExplored {
		if state.PriceExplorationCycles >= coldStartMaxCycles {
			markPriceExploredLocked(&state, "max_cycles")
		} else if coldStartNearMarket(priceBefore, p10, thickCold) {
			markPriceExploredLocked(&state, "near_p10")
		}
	}
	// Catchup streak: только при рыночном evidence (thick+gap), не от held=0 alone.
	if emptyMarketCatchupEvidence(
		state.PriceExplored, totalHeld, sales, buys, priceBefore, p10, p10N, p10OK,
	) {
		state.EmptyMarketGapStreak++
	} else if state.PriceExplored {
		state.EmptyMarketGapStreak = 0
	}
	if !state.PriceExplored &&
		!strings.Contains(action, "price_up") && !strings.Contains(action, "price_down") &&
		canColdStartUp(
			state.PriceExplored, state.PriceExplorationCycles, totalHeld, sales, buys,
			priceBefore, step, p10, bookN, bookOK, p10OK, blockUpEI,
			state.CorridorUpCooldown, state.CorridorUpStreak, state.EmptyIdleMarketDownCooldown,
		) {
		state.PriceExplorationCycles++
		if state.PriceOriginKind == "" {
			state.PriceOriginKind = "cold_start"
			state.PriceOriginPrice = priceBefore
		}
		applyUp(coldStartAction, fmt.Sprintf(
			"cold_start ↑ +1: our=%d p10=%d ratio=%.2f uuid=%d cycles=%d/%d",
			priceBefore, p10, float64(priceBefore)/float64(p10), bookN,
			state.PriceExplorationCycles, coldStartMaxCycles,
		))
	} else if state.PriceExplored &&
		!strings.Contains(action, "price_up") && !strings.Contains(action, "price_down") &&
		canEmptyMarketCatchupUp(
			state.PriceExplored, totalHeld, sales, buys,
			state.EmptyMarketGapStreak, state.EmptyInventoryClimbSteps,
			priceBefore, step, p10, p10N, p10OK, blockUpEI,
			state.CorridorUpCooldown, state.CorridorUpStreak, state.EmptyIdleMarketDownCooldown,
		) {
		state.EmptyInventoryClimbSteps++
		applyUp(emptyMarketCatchupAction, fmt.Sprintf(
			"empty_market_catchup ↑ +1: our=%d p10=%d ratio=%.2f p10N=%d streak=%d climb=%d/%d",
			priceBefore, p10, float64(priceBefore)/float64(p10), p10N,
			state.EmptyMarketGapStreak, state.EmptyInventoryClimbSteps, emptyMarketCatchupMaxSteps,
		))
	} else if state.PriceExplored && totalHeld == 0 {
		// Explored + empty без thick+gap: HOLD (не empty_inventory_up).
	}
	// held=0: не multi-step ah_book raise (cold = +1 only; explored empty ≠ jump).
	if totalHeld == 0 {
		raiseFromBook = false
		raiseEmptyFromBook = false
	}
	if raiseFromBook && raiseTgt > newPrice {
		newPrice = raiseTgt
		action = "corridor_price_up_ah_book"
		changed = true
		notes = append(notes, fmt.Sprintf("ah_book min10=%d n=%d → селл %d < min+наценка %d → %d (cap +%d×step)",
			minAsk, bookN, priceBefore, minAsk+nacenka, raiseTgt, ahBookMaxRaiseSteps))
	} else if raiseEmptyFromBook && raiseTgt > newPrice {
		newPrice = raiseTgt
		action = "corridor_price_up_empty_book"
		changed = true
		notes = append(notes, fmt.Sprintf("held=0 ah_book p10=%d min=%d n=%d → рынок выше sell %d, медленный ↑ до %d",
			p10, minAsk, minInt(bookN, p10N), priceBefore, raiseTgt))
	}
	emptyIdleAhSoftDownFired := false
	// Same-cycle: corridor/empty_inventory ↑ уже поднял цену — empty_idle_ah_soft_down не откатывает.
	corridorAlreadyUp := strings.Contains(action, "price_up")
	// Grant + Minimal C: AH soft-↓ только если авто-↓ не запрещён.
	ahSoftDownOK := automaticDownAllowed(totalHeld, targetHi) &&
		!postGrantCBlockDown(totalHeld, targetHi, sales, buys, zeroStreak) &&
		(softDownFromBook || emptyIdleAhSoftDown)
	if ahSoftDownOK && softDownTgt > 0 && softDownTgt < newPrice && !corridorAlreadyUp {
		bookFloor := minAsk + nacenka
		if softDownTgt < bookFloor {
			softDownTgt = bookFloor
		}
		tgt := ahBookSoftDownApply(newPrice, softDownTgt, priceFloor, step, totalHeld)
		if tgt < newPrice {
			newPrice = tgt
			action = "corridor_price_down_ah_book"
			changed = true
			if emptyIdleAhSoftDown {
				emptyIdleAhSoftDownFired = true
				notes = append(notes, fmt.Sprintf(
					"empty_idle_ah_soft_down: p10=%d min=%d n=%d → селл %d → %d (пол min+наценка=%d; floor_escape UP blocked)",
					p10, minAsk, p10N, priceBefore, tgt, bookFloor))
			} else {
				notes = append(notes, fmt.Sprintf("ah_book p10=%d min=%d n=%d → селл %d → %d (пол min+наценка=%d held=%d)",
					p10, minAsk, p10N, priceBefore, tgt, bookFloor, totalHeld))
			}
		}
	} else if postGrantCBlockDown(totalHeld, targetHi, sales, buys, zeroStreak) && (softDownFromBook || emptyIdleAhSoftDown) {
		notes = append(notes, fmt.Sprintf(
			"ah_book soft-↓ blocked post_grant_C (held=%d hi=%d excess=+%d sales=%d)",
			totalHeld, targetHi, excessDepth(totalHeld, targetHi), sales))
	} else if !automaticDownAllowed(totalHeld, targetHi) && (softDownFromBook || emptyIdleAhSoftDown) {
		notes = append(notes, fmt.Sprintf(
			"ah_book soft-↓ blocked no-excess (held=%d ≤ hi=%d)", totalHeld, targetHi))
	} else if corridorAlreadyUp && emptyIdleAhSoftDown {
		notes = append(notes, "empty_idle_ah_soft_down skipped: already ↑ this cycle")
	}

	// EMPTY_IDLE / floor escape BEFORE standalone trusted jump / market_recovery:
	// held=sales=buys=0 → trusted jump or +1 (даже без book / thin AH);
	// near_floor+down_streak≥3 → +1. One UP max this cycle.
	// Raw tmBook.TrustedMin is only an AH *cap* inside evalFloorEscape when tmEv.WouldFire
	// (untrusted/thin AH must not cancel EMPTY_IDLE +1 via at_or_above_cap).
	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	manualLock := blockUp || blockDown
	alreadyDown = strings.Contains(action, "price_down")
	alreadyUp := strings.Contains(action, "price_up")
	suppressEmptyRecovery := suppressEmptyIdlePriceRecoveryLocked(cfg, totalHeld, sales, buys)
	if suppressEmptyRecovery {
		log.Printf("[EMPTY_IDLE] suppressed: presence_inactive reason=treasury_empty type=%s item=%s held=%d sales=%d buys=%d",
			cfg.Type, item, totalHeld, sales, buys)
		notes = append(notes, "EMPTY_IDLE suppressed: presence_inactive reason=treasury_empty")
	}
	// Anti-yoyo after A: блок empty_idle / trusted / MR recovery UP на N циклов.
	marketDownCooldownBlocksUp := state.EmptyIdleMarketDownCooldown > 0
	if marketDownCooldownBlocksUp {
		notes = append(notes, fmt.Sprintf(
			"empty_idle_market_down_cd=%d — recovery UP blocked (anti-yoyo)", state.EmptyIdleMarketDownCooldown))
	}
	// Fresh book: sell > p10+nac → empty_idle UP «из-за held=0» запрещён (thin/no book не трогаем).
	aboveMarketBlocksIdleUp := emptyIdleAboveMarketBlocksUp(emptyIdle, bookOK, p10OK, priceBefore, p10, nacenka)
	if aboveMarketBlocksIdleUp {
		notes = append(notes, fmt.Sprintf(
			"empty_idle UP blocked: sell %d > p10+nac %d (fresh book)", priceBefore, p10+nacenka))
	}
	recoveryUpBlocked := suppressEmptyRecovery || marketDownCooldownBlocksUp
	tmEv := evalTrustedMinDiscovery(priceBefore, step, totalHeld, buys, tmBook, manualLock)
	feEv := evalFloorEscape(
		priceBefore, step, priceFloor, tmBook.TrustedMin,
		totalHeld, sales, buys, state.CorridorDownStreak, state.FloorEscapeCooldown, 0,
		manualLock, alreadyUp || alreadyDown, tmEv.WouldFire, tmEv.WouldPrice,
	)
	if floorEscapePriceUpEnabled && !recoveryUpBlocked && feEv.WouldFire && feEv.WouldPrice > newPrice {
		// Не поднимать empty_idle, пока fresh book говорит, что мы уже выше рынка.
		if aboveMarketBlocksIdleUp && isEmptyIdle(totalHeld, sales, buys) {
			notes = append(notes, fmt.Sprintf("floor_escape %s skipped: above market", feEv.Reason))
		} else {
			newPrice = feEv.WouldPrice
			action = feEv.Action
			changed = true
			sellers := tmBook.UniqueSellers
			notes = append(notes, floorEscapeNote(feEv, totalHeld, sales, buys, stockLoad, sellers))
			log.Printf("[FLOOR_ESCAPE] %s: %s | %d→%d | held=%d sales=%d buys=%d fill=%.1f%% down_streak=%d trusted_AH_min=%d sellers=%d near=%d price/floor=%.3f price/ah=%.3f",
				item, feEv.Reason, priceBefore, newPrice, totalHeld, sales, buys, stockLoad*100,
				feEv.DownStreak, feEv.AHMin, sellers, tmBook.SellersNearMin, feEv.FloorRatio, feEv.AHRatio)
			if feEv.Action == floorEscapeActionTrustedJump {
				logTrustedMinDiscoveryJump(item, now, priceBefore, newPrice, step, totalHeld, buys, sales, tmEv)
				state.FloorEscapeCooldown = floorEscapeCooldownCycles
			} else {
				// empty_idle / near_floor / deep_ah — можно ↑ каждый цикл, пока пусто.
				state.FloorEscapeCooldown = 0
			}
		}
	} else if !floorEscapePriceUpEnabled && feEv.WouldFire {
		notes = append(notes, fmt.Sprintf("floor_escape %s ↑ disabled (книга/empty_idle наебывают)", feEv.Reason))
	}

	// Trusted AH-min jump (standalone): only if floor escape did not already UP.
	alreadyUp = strings.Contains(action, "price_up")
	alreadyDown = strings.Contains(action, "price_down")
	if !recoveryUpBlocked && trustedMinDiscoveryLiveEnabled && !alreadyUp && !alreadyDown && !manualLock {
		if tmEv.WouldFire && tmEv.WouldPrice > newPrice {
			newPrice = tmEv.WouldPrice
			action = trustedMinDiscoveryActionLive
			changed = true
			notes = append(notes, fmt.Sprintf(
				"trusted_ah_min: held=0 buys=0 our=%d → min=%d sellers=%d near=%d gap_steps=%.1f gap_ratio=%.3f",
				priceBefore, tmEv.TrustedMin, tmEv.UniqueSellers, tmEv.SellersNearMin, tmEv.GapSteps, tmEv.GapRatio))
			logTrustedMinDiscoveryJump(item, now, priceBefore, newPrice, step, totalHeld, buys, sales, tmEv)
		}
	}

	// B_price_trap live recovery: +1 step, без p10+nacenka, без прыжка к p10.
	// Не пересекается с уже выбранным ↑/↓ этого цикла (в т.ч. floor escape / trusted_ah_min).
	alreadyUp = strings.Contains(action, "price_up")
	alreadyDown = strings.Contains(action, "price_down")
	if !recoveryUpBlocked && marketRecoveryLiveEnabled && !alreadyUp && !alreadyDown && !manualLock &&
		marketRecoveryLiveShouldRaise(item, priceBefore, step, totalHeld, buys, sales, mrBook, manualLock) {
		tgt := priceBefore + step
		if tgt > newPrice {
			newPrice = tgt
			action = marketRecoveryActionLive
			changed = true
			notes = append(notes, fmt.Sprintf(
				"market_recovery B_price_trap: held=buys=sales=0 our=%d p10_60m=%d sellers=%d uuid=%d → +1 step → %d",
				priceBefore, mrBook.P10, mrBook.NSell, mrBook.NUUID, tgt))
		}
	}

	// После set_min: только ↑. После set_max: только ↓. Окно = AnalysisTime.
	if blockDown && newPrice < priceBefore {
		notes = append(notes, "manual min/set → ↓ запрещён")
		newPrice = priceBefore
		changed = false
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		} else {
			action = "hold_manual_min"
		}
	} else if blockUp && newPrice > priceBefore {
		notes = append(notes, "manual max/set → ↑ запрещён")
		newPrice = priceBefore
		changed = false
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		} else {
			action = "hold_manual_max"
		}
	}

	if action == "" {
		action = "hold"
	}

	if newPrice < priceFloor {
		newPrice = priceFloor
		if newPrice > priceBefore {
			changed = true
			action = "corridor_price_up_floor"
			notes = append(notes, fmt.Sprintf("цена %d < пола %d → поднимаем", priceBefore, priceFloor))
		} else if newPrice == priceBefore {
			notes = append(notes, "уже на полу")
		}
	}

	// Стрелки коридора + soft↓ cooldown + up-cooldown / last sales
	if strings.Contains(action, "price_up") {
		state.CorridorDeadStreak = 0
		state.CorridorDownCooldown = 0
		state.IdleHardDownStreak = 0
		state.CorridorDownStreak = 0
		if corridorUpArmCooldown(action) {
			state.CorridorUpStreak++
			state.CorridorUpCooldown = corridorUpCooldownCycles
		} else {
			// нормализация: не блокируем следующий цикл hold_up_cd / streak
			state.CorridorUpStreak = 0
			state.CorridorUpCooldown = 0
			notes = append(notes, "up_cd skipped (normalization ↑)")
		}
		isRecover := strings.Contains(action, "recover") || strings.Contains(action, "price_up_paid")
		if buys > 0 || !isRecover {
			state.CorridorNoBuyUpStreak = 0
		} else {
			state.CorridorNoBuyUpStreak++
		}
	} else {
		state.CorridorUpStreak = 0
		if sales == 0 && buys == 0 {
			state.CorridorDeadStreak++
		} else {
			state.CorridorDeadStreak = 0
			if buys > 0 {
				state.CorridorNoBuyUpStreak = 0
			}
		}
		if sales > 0 {
			state.IdleHardDownStreak = 0
		} else if strings.Contains(action, "price_down_over") || strings.Contains(action, "price_down_dump") {
			state.IdleHardDownStreak++
		}
		if strings.Contains(action, "price_down") {
			state.CorridorDownStreak++
		}
		// HOLD preserves CorridorDownStreak (escape after dump→park on floor).
		if state.CorridorUpCooldown > 0 {
			state.CorridorUpCooldown--
		}
		// Soft-cooldown тикает каждый цикл, кроме только что выставленного после soft↓ / skim_revert.
		if strings.Contains(action, "price_down_soft") || strings.Contains(action, "skim_revert") {
			// уже выставили CorridorDownCooldown в trySoftDown
		} else if state.CorridorDownCooldown > 0 {
			state.CorridorDownCooldown--
		}
		if strings.Contains(action, "price_down_over") || strings.Contains(action, "price_down_dump") {
			state.CorridorDownCooldown = 0
		}
	}

	// Floor-escape / empty-idle CD ticks on non-escape cycles (set to N when escape fired).
	if !strings.Contains(action, "floor_escape") && action != floorEscapeActionEmptyIdle && state.FloorEscapeCooldown > 0 {
		state.FloorEscapeCooldown--
	}

	// Anti-yoyo: после A — 3 цикла без empty_idle/trusted/MR UP; иначе тик.
	if emptyIdleAhSoftDownFired {
		state.EmptyIdleMarketDownCooldown = emptyIdleMarketDownCooldownCycles
	} else if state.EmptyIdleMarketDownCooldown > 0 {
		state.EmptyIdleMarketDownCooldown--
	}

	if state.StockVsSalesCooldown > 0 {
		state.StockVsSalesCooldown--
	}
	if state.FillPriceCooldown > 0 {
		state.FillPriceCooldown--
	}

	state.LastCycleSales = sales
	state.ZeroSalesExcessStreak = zeroStreak
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	if totalHeld > 0 {
		state.EmptyInventoryClimbSteps = 0
		state.EmptyInventoryAnchorPrice = 0
	}
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	// Наценку не меняем — фиксирована из items_config.
	nacenka = nacenkaBefore
	if newPrice != priceBefore {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
		changed = true
	}

	actionTaken := action
	reason := actionReasonRU(actionTaken)
	if len(notes) > 0 {
		reason = reason + " | " + strings.Join(notes, " · ")
	}

	if changed {
		log.Printf("[ADJUST] %s: %s | цена %d→%d | наценка %d | held %d/share %d fill=%.1f%% DOI=%.3f зона[%d,%d] soft=%d over=%d | продажи %d buys=%d try=%d | АХ %d инв %d | %s",
			item, action, priceBefore, newPrice, nacenka, totalHeld, share, stockLoad*100, doiCover(totalHeld, sales), targetLo, targetHi, targetSoft, targetOver, sales, buys, trySells, onAH, invCount, reason)
	} else {
		log.Printf("[HOLD] %s: %s | цена %d | наценка %d | held %d/share %d fill=%.1f%% DOI=%.3f зона[%d,%d] soft=%d | продажи %d buys=%d try=%d | АХ %d инв %d | %s",
			item, actionTaken, newPrice, nacenka, totalHeld, share, stockLoad*100, doiCover(totalHeld, sales), targetLo, targetHi, targetSoft, sales, buys, trySells, onAH, invCount, reason)
	}

	queueMLDecisionLocked(
		item, cfg, actionTaken,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now,
		onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy,
		Item:           item,
		Category:       cfg.Type,
		Action:         actionTaken,
		Winner:         actionTaken,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(targetHi),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		Share:          share,
		Free:           free,
		Need:           need,
		NormalSales:    cfg.NormalSales,
		NormalCount:    stockNorm,
		TryRatio:       tryRatio,
		StockLoad:      stockLoad,
		Underbuy:       underbuyOK,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   nacenka,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		PriceFloor:     priceFloor,
		Step:           step,
		Cooldown:       state.StockVsSalesCooldown,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		CheapFrac:      0,
		CheapN:         0,
		MinBuyHistory:  minPrice,
		BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     state.CorridorUpStreak,
		DumpBlockedCD:  false,
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}

	shadowSnap := mlAdjustSnapshot{}
	if mlShadowEnabled() {
		shadowSnap = mlAdjustSnapshot{
			At:             now,
			Item:           item,
			CategoryType:   cfg.Type,
			GoAction:       actionTaken,
			PriceBefore:    priceBefore,
			NacenkaBefore:  nacenkaBefore,
			GoPriceAfter:   newPrice,
			GoNacenkaAfter: nacenka,
			Sales:          sales,
			Buys:           buys,
			TrySells:       trySells,
			ProfitNow:      profitNow,
			OnAH:           onAH,
			TotalStock:     totalHeld,
			NormalSales:    cfg.NormalSales,
			NormalCount:    stockNorm,
			MinBuyHistory:  minPrice,
			CanRaisePrice: func() bool {
				if totalHeld >= targetLo || trySellsBlockUp(sales, trySells) || buys > sales {
					return false
				}
				if state.CorridorNoBuyUpStreak >= corridorMaxNoBuyUps {
					return false
				}
				deep := deepUnderstock(totalHeld, targetLo)
				under := priceFarBelowPaid(priceBefore, paidMax, step)
				bypass := deep && !nightMSK && sales > buys
				cdOK := state.CorridorUpCooldown == 0 || bypass
				streakOK := state.CorridorUpStreak < corridorMaxUpStreak || bypass
				if sales > buys {
					return demandStrongEnoughForUp(sales, prevCycleSales, minSalesForUp, nightMSK) && cdOK && streakOK
				}
				// recover: недобор без sales>buys — заниженная цена + sales≥1
				return !nightMSK && under && sales >= 1 && !overCap && cdOK && streakOK
			}(),
			BotsCategory:   aggregateBotsPerTypeLocked()[cfg.Type],
			PlayersOnline:  onlineForCap,
		}
	}

	rep = AdjustReport{
		Item:           item,
		Action:         actionTaken,
		Reason:         reason,
		Skipped:        false,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   nacenka,
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           totalHeld,
		NormalSales:    cfg.NormalSales,
		Share:          share,
		Free:           free,
		Need:           need,
		PriceFloor:     sellPriceFloor(minPrice, nacenka),
		Step:           step,
		Cooldown:       state.StockVsSalesCooldown,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		GoodStreak:     state.CorridorUpStreak,
		BlockNacenkaUp: underbuyOK,
	}

	needBroadcast := changed
	mutex.Unlock()

	logCapitalCycle(capitalRow)

	// Shadow-only B_price_trap recovery: не меняет newPrice / winner / broadcast.
	runMarketRecoveryShadow(item, now, newPrice, step, totalHeld, buys, sales, actionTaken, blockUp || blockDown)
	// Shadow-only LEVEL1 cap1/3/5 comparison on same episodes — не меняет цену.
	runCappedDiscoveryShadow(item, now, newPrice, step, totalHeld, buys, sales, trySells, actionTaken, blockUp || blockDown)
	// Diagnostic log for trusted-min (live winner may already be corridor_price_up_trusted_ah_min).
	runTrustedMinDiscoveryShadow(item, now, priceBefore, step, totalHeld, buys, sales, actionTaken, blockUp || blockDown)
	// Production experiment outcomes after trusted-min jump (1h / first buy / downs / stop).
	trackTrustedMinDiscoveryOutcome(item, now, totalHeld, buys, sales, profitNow, actionTaken, tmBook, newPrice, step, blockUp || blockDown)

	if experimentTG != nil {
		enqueueExperimentTelegram(*experimentTG)
	}

	if mlShadowEnabled() {
		runMLShadowAsync(shadowSnap)
	}

	if needBroadcast {
		publishPriceUpdate()
	}
	saveDailyDataNoMessageUpdate()
	return rep
}
