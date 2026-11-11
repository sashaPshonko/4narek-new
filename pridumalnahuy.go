package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// ИНВАРИАНТ (решение владельца, не пересматривать): наценку из items_config.json НЕ СНИЖАЕМ
// и не подгоняем под текущую цену. Политика двигает только цену продажи; лимит покупки
// (цена − наценка) от этого может стать нулевым или низким. Это нормально.
//
// В частности, sword-sharp5/6 и все «позор» — не про стабильную прибыль, а про разовые
// выгодные случаи: покупаем, когда лот подвернулся достаточно дёшево, и зарабатываем как со
// средних предметов. Нулевой лимит покупки у них при низкой цене — не баг.
// Наценка меняется только заморозкой закупки (повышение, см. freezeNacenkaLocked) и
// возвратом к исходному значению после разморозки.

// jumpConfirmCycles — сколько циклов подряд разрыв с потолком должен держаться, чтобы сработал прыжок.
const jumpConfirmCycles = 2

// floorJumpConfirmCycles — сколько циклов подряд цена должна быть ниже пола, чтобы прыгнуть к нему.
const floorJumpConfirmCycles = 2

const (
	capitalPolicyPridumalnahuy = "pridumalnahuy_2026_10"
	pridumalnahuyFrozenNacenka = 1_000_000
	// pridumalnahuyNoBookNacenka — товар без данных книги АХ: цену не двигаем и ничего не
	// покупаем (наценка выше любой цены), пока книга не наберётся.
	pridumalnahuyNoBookNacenka = 1_000_000_000
)

func isPricingPolicyPridumalnahuy() bool {
	return capitalPolicy == capitalPolicyPridumalnahuy
}

type pridumalnahuyInput struct {
	Sales, TrySells, NormalSales int

	// SlotCapacity — слотов АХ у ботов категории (5 на бота, 10 у ботов с арендой). 0 → BotsCount*5.
	BotsCount    int
	SlotCapacity int
	// PriceCap — ходовые: выше этой цены повышение блокируется; PriceTether — выше неё цена плавно возвращается вниз
	// (0 = нет). Считаются по книге (перцентили минимумов продавцов), см. buy_stop.go.
	PriceCap    int
	PriceTether int
	// HourFrac — «доходность» текущего часа суток (МСК) относительно типичного пика (0 = нет данных). Ниже
	// hotUpMinHourFrac ходовым не повышаем, см. hour_gate.go.
	HourFrac           float64
	OnAHCategory       int
	TotalStockCategory int

	OnAHItem       int
	InvItem        int
	TotalStockItem int

	Price, Step        int
	MultiFloor         int
	MultiFloorOK       bool
	BookCeil           int
	BookCeilOK         bool
	BlockUp, BlockDown bool

	// NightMode — ночной режим предмета: прыжок к потолку при любом разрыве, без подтверждения и порога наценки.
	NightMode bool

	// FloorJumpTarget — куда прыгнуть вверх, если цена ниже пола (0 = прыжок к полу выключен/некуда).
	// FloorJumpConfirmed — цена ниже пола floorJumpConfirmCycles циклов подряд.
	FloorJumpTarget    int
	FloorJumpConfirmed bool

	// JumpConfirmed — разрыв «цена выше потолка больше чем на наценку» держится jumpConfirmCycles циклов подряд.
	// Без подтверждения прыжка нет: потолок на тонкой книге иногда проваливается на один цикл (шум).
	JumpConfirmed bool

	// Nacenka — наценка товара (для замороженной — исходная). Прыжок к потолку только если цена выше
	// потолка больше чем на наценку: меньший разрыв — шум книги, лоты по лимиту (цена − наценка) в убыток
	// не уходят. 0 или ≥1e8 (заморозка без исходной) = порога нет, прыжок как раньше.
	Nacenka int

	NacenkaFrozen bool

	// NoBook — по товару нет данных книги АХ (BookCeil не посчитан).
	NoBook bool

	// StockTarget — динамическая цель запаса (stock_target.go): заменяет норму N там, где
	// решается «хватит ли запаса». HasStockTarget=false → цель не посчитана, работает N.
	StockTarget    int
	HasStockTarget bool

	// Покупатели категории (buyers.go): есть живые боты, но ни один не может купить
	// (нет денег или слоты заняты) → повышение цены ничего не даст.
	BuyersEligible, BuyersAble, BuyersBroke, BuyersFull int
	// BuyersFreeSlots — свободные слоты ботов, которые могут купить (лимит бота общий на все товары).
	// Повышать цену имеет смысл, только если слотов хватит добрать запас этого товара до нормы N.
	BuyersFreeSlots int

	// Hot — ходовой товар: прайсинг без книги и потолков (см. pridumalnahuyDecideHot).
	Hot bool
	// Пороги hot-логики. 0 → hot_misconfigured (hold). Явные числа из конфига или ничего.
	HotDownSalesMax int
	HotDownStockMin int
	// PrevSales — продажи прошлого цикла. Защита от «обвала»: если они были не ниже NormalSales, одно слабое
	// измерение цену не снижает (hot_down_prev_ok). NormalSales=0 → защиты нет.
	PrevSales     int
	HotUpSalesMax int
	HotUpStockMax int
}

type pridumalnahuyDecision struct {
	Action   string
	NewPrice int
	Reason   string
	Freeze   bool
	// HardFreeze — запас в несколько раз выше цели: закупка стоит полностью, мягкий фриз не пускает даже исключительные лоты.
	HardFreeze bool
	Unfreeze   bool
	// PauseBuy — книги нет: цену не трогаем, закупку блокируем (наценка NoBook).
	PauseBuy bool
}

// clampUpToCeil — цена не выше потолка.
// Возвращает (цена, ok=true если двигаться можно).
func clampUpToCeil(price, step, ceil int, ceilOK bool) (int, bool) {
	p := price + step
	if ceilOK && ceil > 0 && p > ceil {
		p = ceil
	}
	return p, p > price
}

// clampDownToFloor — цена не ниже пола.
// Возвращает (цена, ok=true если двигаться можно).
func clampDownToFloor(price, step, floor int, floorOK bool) (int, bool) {
	p := price - step
	if floorOK && floor > 0 && p < floor {
		p = floor
	}
	return p, p < price
}

// pridumalnahuyReasonRU — человекочитаемая причина из dec.Reason.
// Используется и в логе, и в CapitalCycleRow, и в AdjustReport.
func pridumalnahuyReasonRU(reason string) string {
	switch reason {
	case "hot_slots_tight":
		return "pridumalnahuy: hot — на АХ категории не хватит свободных лотов добрать запас → hold"
	case "no_signal":
		return "pridumalnahuy: нет сигнала → hold"
	case "no_book":
		return "pridumalnahuy: нет данных книги → вне прайсинга, закупка стоит"
	case "below_floor_jump":
		return "pridumalnahuy: цена ниже пола 2 цикла подряд → прыжок вверх к полу"
	case "above_ceiling_jump":
		return "pridumalnahuy: цена выше потолка книги больше чем на наценку 2 цикла подряд → прыжок к потолку"
	case "sales_above_normal":
		return "pridumalnahuy: продажи за цикл ≥ нормы → hold"
	case "sales_above_normal_try_overheat":
		return "pridumalnahuy: продажи ≥ нормы, попыток ≥ продаж×20 и запас ≥ нормы → −шаг"
	case "sales_above_normal_try_overheat_at_floor":
		return "pridumalnahuy: продажи ≥ нормы, попыток ≥ продаж×20, цена у пола → hold"
	case "try_conversion_low":
		return "pridumalnahuy: попыток > продаж×7 и запас ≥ нормы → −шаг"
	case "try_conversion_low_at_floor":
		return "pridumalnahuy: попыток > продаж×7 и запас ≥ нормы, цена у пола → hold"
	case "stock_at_norm_no_up":
		return "pridumalnahuy: продажи ниже нормы, запас уже ≥ нормы → hold"
	case "inv_slots_tight":
		return "pridumalnahuy: свободных слотов не хватит довести запас до нормы → hold"
	case "stock_below_normal":
		return "pridumalnahuy: продажи ниже нормы, запас ниже нормы, слотов хватает → +шаг"
	case "stock_below_normal_at_ceil":
		return "pridumalnahuy: запас ниже нормы, цена у потолка → hold"
	case "hot_hold":
		return "pridumalnahuy: hot — продажи/запас в норме → hold"
	case "hot_overstock":
		return "pridumalnahuy: hot — продаж мало, запас выше порога → −шаг"
	case "hot_low_stock":
		return "pridumalnahuy: hot — продаж мало, запас ниже порога → +шаг"
	case "hot_above_market":
		return "pridumalnahuy: hot — цена выше рынка (поводок), плавно возвращаю → −шаг"
	case "hot_price_cap":
		return "pridumalnahuy: hot — цена уже на уровне рынка (потолок повышения) → hold"
	case "hot_misconfigured":
		return "pridumalnahuy: hot — пороги не заданы в конфиге → hold"
	default:
		return "pridumalnahuy: " + reason
	}
}

// pridumalnahuyDecide — решение по цене товара за цикл. Все данные — за ЦИКЛ (sales, tries = продажи и попытки
// продажи за последний цикл). Проверки сверху вниз, срабатывает первая:
//
//  1. нет книги                                    → цену держим, закупка стоит (no_book)
//  2. цена выше потолка книги больше чем на наценку, и так 2 цикла подряд → прыжок к потолку (above_ceiling_jump);
//     (ночной режим: любой разрыв, без подтверждения)
//     выше потолка, но не дальше наценки: прыжка нет, рост запрещён
//     2б. (floor_jump) цена ниже пола 2 цикла подряд → прыжок вверх к полу (below_floor_jump)
//  3. продажи ≥ нормы N:
//     попыток ≥ продаж×20 и запас ≥ N            → шаг вниз (sales_above_normal_try_overheat)
//     иначе                                      → HOLD (sales_above_normal)
//  4. попыток > продаж×7 и запас ≥ N               → шаг вниз (try_conversion_low), не ниже пола
//  5. продажи ниже N:
//     запас ≥ N                                  → HOLD (stock_at_norm_no_up)
//     слотов не хватит довести запас до N        → HOLD (inv_slots_tight)
//     иначе                                      → шаг вверх (stock_below_normal), не выше потолка
//
// Попыток при расчёте «> продаж×7» берётся не меньше 8 (при нуле продаж одна случайная попытка цену не роняет).
// Фризов нет: HOLD значит только «цену не меняем», закупку ограничивают лимиты бота.
//
// Для hot-предметов (in.Hot == true) — отдельная логика pridumalnahuyDecideHot:
// книга и потолки игнорируются, цена двигается только по продажам и запасу.
func pridumalnahuyDecide(in pridumalnahuyInput) pridumalnahuyDecision {
	if in.Hot {
		return pridumalnahuyDecideHot(in)
	}
	price := in.Price
	step := in.Step
	if step <= 0 {
		step = 1
	}
	N := in.NormalSales
	if N <= 0 {
		N = 1
	}

	out := pridumalnahuyDecision{
		Action:   "corridor_hold_pridumalnahuy_no_signal",
		NewPrice: price,
		Reason:   "no_signal",
	}

	downStep := func(reason, holdReason string) pridumalnahuyDecision {
		newP, ok := clampDownToFloor(price, step, in.MultiFloor, in.MultiFloorOK)
		if !ok {
			out.Action = "corridor_hold_pridumalnahuy_no_up"
			out.Reason = holdReason
			return out
		}
		out.Action = "corridor_price_down_pridumalnahuy"
		out.NewPrice = newP
		out.Reason = reason
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}

	// 1. Нет книги — нет прайсинга: цену не двигаем, закупку блокируем, пока книга не наберётся.
	if in.NoBook {
		out.Action = "corridor_hold_pridumalnahuy_no_book"
		out.Reason = "no_book"
		out.PauseBuy = true
		return out
	}

	// 2. Цена выше потолка книги больше чем на наценку 2 цикла подряд (JumpConfirmed) -> прыжок к потолку.
	// Выше потолка, но не дальше наценки: цену держим, рост запрещён (clampUpToCeil), вниз — по правилам 3–4.
	if !in.BlockDown && in.BookCeilOK && in.BookCeil > 0 && price > in.BookCeil &&
		(in.NightMode || (in.JumpConfirmed &&
			(in.Nacenka <= 0 || in.Nacenka >= 100_000_000 || price-in.BookCeil > in.Nacenka))) {
		out.Action = "corridor_price_down_pridumalnahuy_book_ceiling"
		out.NewPrice = in.BookCeil
		out.Reason = "above_ceiling_jump"
		return out
	}

	// 2б. Цена ниже пола 2 цикла подряд (предмет «мёртвый») -> прыжок вверх к полу (только floor_jump-предметы).
	if !in.BlockUp && in.FloorJumpTarget > price && in.FloorJumpConfirmed {
		out.Action = "corridor_price_up_pridumalnahuy_floor_jump"
		out.NewPrice = in.FloorJumpTarget
		out.Reason = "below_floor_jump"
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}

	// 3. Продажи в норме: цену не трогаем, пока попыток не стало слишком много (≥ 20× продаж).
	if in.Sales >= N {
		if in.TrySells >= in.Sales*20 && in.TotalStockItem >= N {
			return downStep("sales_above_normal_try_overheat", "sales_above_normal_try_overheat_at_floor")
		}
		out.Action = "corridor_hold_pridumalnahuy_sellthrough"
		out.Reason = "sales_above_normal"
		return out
	}

	// 4. Попыток много, продаж мало и запас уже выше нормы -> цена завышена.
	salesBase := in.Sales
	if salesBase < 1 {
		salesBase = 1
	}
	if in.TrySells > salesBase*7 && in.TotalStockItem >= N {
		return downStep("try_conversion_low", "try_conversion_low_at_floor")
	}

	// 5. Продажи ниже нормы.
	if in.TotalStockItem >= N {
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "stock_at_norm_no_up"
		return out
	}
	freeSlots := in.BuyersFreeSlots
	if freeSlots < 0 {
		freeSlots = 0
	}
	if freeSlots+in.TotalStockItem < N {
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "inv_slots_tight"
		return out
	}
	newP, ok := clampUpToCeil(price, step, in.BookCeil, in.BookCeilOK)
	if ok {
		out.Action = "corridor_price_up_pridumalnahuy"
		out.NewPrice = newP
		out.Reason = "stock_below_normal"
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}
	out.Action = "corridor_hold_pridumalnahuy_no_up"
	out.Reason = "stock_below_normal_at_ceil"
	return out
}

func adjustPricePridumalnahuy(
	item string,
	cfg ItemConfig,
	now time.Time,
	_ time.Time,
	sales, buys, trySells, profitNow int,
	state ItemAdjustState,
	priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor int,
	onAH, invCount, totalHeld, share, free, need, stockNorm int,
	underbuyOK bool,
	tryRatio, stockLoad float64,
	onlineForCap, onlineMaxForML int,
	ahCounts, invCounts map[string]int,
) AdjustReport {

	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)

	onAHCategory := 0
	invCategory := 0
	for name, cnt := range ahCounts {
		if c, ok := itemsConfig[name]; ok && c.Type == cfg.Type {
			onAHCategory += cnt
		}
	}
	for name, cnt := range invCounts {
		if c, ok := itemsConfig[name]; ok && c.Type == cfg.Type {
			invCategory += cnt
		}
	}
	totalStockCategory := onAHCategory + invCategory

	bots := botsForGoTypeLocked(cfg.Type)

	// Пол и потолок по окну 3 часа (ahBookFloorWindow, ahBookCeilWindow); если пол выше потолка — прижимаем к потолку ниже.
	mutex.Unlock()
	multiFloor, multiFloorQ, _ := stockNormBookCatchupFloorNow(cfg, now)
	ownP25, ownOK := 0, false
	if cfg.FloorJump {
		ownP25, ownOK = ownSalesQuantile(item, now, floorJumpOwnSalesWindow, 0.25, floorJumpOwnSalesMin)
	}
	bookCeil, bookCeilQ, bookCeilOK := stockNormBookCeilNow(cfg, now)
	// потолок для стоп-закупа дорогих (buy_stop.go): считаем здесь же, пока mutex отпущен (идёт в БД)
	buyStopCeil, buyStopN, buyStopOK := buyStopCeilNow(cfg, now)
	// рыночная полоса ходовых: потолок повышения и поводок (тоже по книге, тоже вне mutex)
	hotPriceCap, hotPriceTether, hotBandN := hotMarketBandNow(cfg, now)
	mutex.Lock()
	if bookCeilOK && multiFloor > bookCeil {
		multiFloor = bookCeil
	}

	nacenkaFrozen := state.NacenkaFrozen

	// Покупатели: хватит ли ботам денег на закупку по лимиту после повышения.
	// Замороженная наценка (1e6..1e9) не должна искажать лимит — берём исходную.
	buyNacenka := nacenka
	if nacenkaFrozen && state.NacenkaOriginal > 0 {
		buyNacenka = state.NacenkaOriginal
	}
	needBalance := priceBefore + step - buyNacenka
	if needBalance < 0 {
		needBalance = 0
	}
	buyers := categoryBuyersLocked(cfg.Type, needBalance)

	tgtInfo, hasTarget := stockTargetLocked(item, now)

	// Подтверждение прыжка: сколько циклов подряд цена выше потолка больше чем на наценку.
	aboveFar := bookCeilOK && bookCeil > 0 && priceBefore > bookCeil &&
		(buyNacenka <= 0 || buyNacenka >= 100_000_000 || priceBefore-bookCeil > buyNacenka)
	if aboveFar {
		state.AboveCeilCycles++
	} else {
		state.AboveCeilCycles = 0
	}

	// Прыжок к полу: подтверждение 2 циклами и цель.
	floorTarget := 0
	if cfg.FloorJump && multiFloor > 0 && priceBefore < multiFloor {
		state.BelowFloorCycles++
		floorTarget = floorJumpTarget(priceBefore, multiFloor, state.PrevFloor, ownP25, ownOK, bookCeil, bookCeilOK)
	} else {
		state.BelowFloorCycles = 0
	}
	state.PrevFloor = multiFloor

	nightMode := cfg.BookCeilQNight > 0 && isNightNow(now)

	dec := pridumalnahuyDecide(pridumalnahuyInput{
		NightMode:   nightMode,
		Sales:       sales,
		TrySells:    trySells,
		NormalSales: cfg.NormalSales,

		BotsCount:          bots,
		SlotCapacity:       slotsForGoTypeLocked(cfg.Type),
		PriceCap:           hotPriceCap,
		PriceTether:        hotPriceTether,
		HourFrac:           hotHourFrac(now),
		PrevSales:          state.LastCycleSales,
		OnAHCategory:       onAHCategory,
		TotalStockCategory: totalStockCategory,

		OnAHItem:       onAH,
		InvItem:        invCount,
		TotalStockItem: totalHeld,

		Price:        priceBefore,
		Step:         step,
		MultiFloor:   multiFloor,
		MultiFloorOK: multiFloor > 0,
		BookCeil:     bookCeil,
		BookCeilOK:   bookCeilOK,
		BlockUp:      blockUp,
		BlockDown:    blockDown,

		FloorJumpTarget:    floorTarget,
		FloorJumpConfirmed: state.BelowFloorCycles >= floorJumpConfirmCycles,
		JumpConfirmed:      state.AboveCeilCycles >= jumpConfirmCycles,
		Nacenka:            buyNacenka,
		NacenkaFrozen:      nacenkaFrozen,
		NoBook:             !bookCeilOK,

		StockTarget:    tgtInfo.Target,
		HasStockTarget: hasTarget,

		BuyersEligible: buyers.Eligible,
		BuyersAble:     buyers.Able,
		BuyersBroke:    buyers.Broke,
		BuyersFull:     buyers.Full,

		BuyersFreeSlots: buyers.FreeSlots,

		Hot:             cfg.Hot,
		HotDownSalesMax: cfg.HotDownSalesMax,
		HotDownStockMin: cfg.HotDownStockMin,
		HotUpSalesMax:   cfg.HotUpSalesMax,
		HotUpStockMax:   cfg.HotUpStockMax,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf(
			"pridumalnahuy reason=%s sales=%d N=%d try=%d onAHcat=%d invcat=%d totalcat=%d slots=%d onAHitem=%d invItem=%d totalItem=%d bots=%d пол(p%.0f)=%d потолок(p%.0f)=%d frozen=%v hot=%v",
			dec.Reason, sales, cfg.NormalSales, trySells,
			onAHCategory, invCategory, totalStockCategory,
			buyers.FreeSlots, onAH, invCount, totalHeld, bots,
			multiFloorQ*100, multiFloor, bookCeilQ*100, bookCeil,
			nacenkaFrozen, cfg.Hot,
		),
	}

	unfroze := false
	paused := false

	notes = append(notes, fmt.Sprintf(
		"цикл: продаж=%d (норма %d) попыток=%d (порог понижения >%d, перегрев ≥%d) запас=%d слотов свободно=%d",
		sales, cfg.NormalSales, trySells, 7*maxInt(sales, 1), 20*sales, totalHeld, buyers.FreeSlots))

	notes = append(notes, fmt.Sprintf(
		"покупатели: ботов=%d могут=%d без денег=%d стоп-закуп=%d свободно слотов=%d (нужно ≥%d)",
		buyers.Eligible, buyers.Able, buyers.Broke, buyers.Full, buyers.FreeSlots, needBalance))

	if sv, ref, ok := slotValueLocked(item, now); ok {
		notes = append(notes, fmt.Sprintf(
			"ценность слота: %.0f ₽/слот·ч (категория %.0f; продаж %d, открыто %d, слот-часов %.1f) режим=%s",
			sv.Value, ref, sv.Sells, sv.Open, sv.SlotHours, slotMode(sv, ref)))
	}

	if hasTarget {
		notes = append(notes, fmt.Sprintf(
			"цель запаса T=%d (N=%d, спрос %d, %.1f прод/ч ×онлайн %.2f, маржа %.0f, ценность %.0f, бюджет %d)",
			tgtInfo.Target, cfg.NormalSales, tgtInfo.Demand, tgtInfo.RatePerHour, tgtInfo.OnlineFactor,
			tgtInfo.Margin, tgtInfo.Value, tgtInfo.Budget))
	}

	if cfg.Hot && (hotPriceCap > 0 || hotPriceTether > 0) {
		notes = append(notes, fmt.Sprintf("рынок (продавцов %d): повышать до %d, поводок выше %d", hotBandN, hotPriceCap, hotPriceTether))
	}
	if cfg.Hot {
		notes = append(notes, fmt.Sprintf(
			"hot-пороги: down(sales<%d×час, stock>%d), up(sales<%d, stock<%d)",
			cfg.HotDownSalesMax, cfg.HotDownStockMin, cfg.HotUpSalesMax, cfg.HotUpStockMax))
	}
	if nightMode {
		notes = append(notes, fmt.Sprintf("НОЧНОЙ РЕЖИМ: потолок q=%.0f%%, прыжок к потолку при любом разрыве", cfg.BookCeilQNight*100))
	}
	if dec.PauseBuy {
		state = freezeNacenkaLocked(item, state, cfg, nacenkaBefore, pridumalnahuyNoBookNacenka)
		nacenka = pridumalnahuyNoBookNacenka
		paused = true
		notes = append(notes, fmt.Sprintf("NO-BOOK: вне прайсинга, закупка стоит (nacenka → %d, orig %d)", pridumalnahuyNoBookNacenka, state.NacenkaOriginal))
	} else if dec.Freeze {
		kind, mode := "FREEZE", pridumalnahuyFrozenNacenka
		if dec.HardFreeze {
			kind, mode = "HARD-FREEZE", pridumalnahuyHardFreeze
		}
		state = freezeNacenkaLocked(item, state, cfg, nacenkaBefore, mode)
		nacenka = itemsConfig[item].Nacenka
		notes = append(notes, fmt.Sprintf("%s nacenka → %d (orig %d)", kind, nacenka, state.NacenkaOriginal))
	} else if nacenkaFrozen || dec.Unfreeze {
		restore := state.NacenkaOriginal
		if restore <= 0 {
			restore = cfg.Nacenka
		}
		c := itemsConfig[item]
		c.Nacenka = restore
		itemsConfig[item] = c
		data.Nacenkas[item] = restore
		dailyData.Nacenkas[item] = restore
		state.NacenkaFrozen = false
		state.NacenkaOriginal = 0
		nacenka = restore
		unfroze = true
		notes = append(notes, fmt.Sprintf("UNFREEZE nacenka → %d", restore))
	}

	if blockDown && strings.Contains(action, "price_down") {
		newPrice = priceBefore
		action = "hold_manual_min"
		notes = append(notes, "manual min/set → ↓ запрещён")
	}
	if blockUp && strings.Contains(action, "price_up") {
		newPrice = priceBefore
		action = "hold_manual_max"
		notes = append(notes, "manual max/set → ↑ запрещён")
	}

	if dec.Reason == "above_ceiling_jump" {
		state.AboveCeilCycles = 0 // прыгнули: счёт подтверждения заново
	}
	if dec.Reason == "below_floor_jump" {
		state.BelowFloorCycles = 0
	}
	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	// стоп-закуп дорогих: цена выше потолка книги → закупка выключена (саму цену это не трогает)
	buyStopChanged := false
	if cfg.BuyStopQ > 0 {
		stop := buyStopDecide(buyStopActive[item], newPrice, buyStopCeil, buyStopOK)
		if setBuyStopLocked(item, stop) {
			buyStopChanged = true
			logBuyStopChange(item, stop, newPrice, buyStopCeil, buyStopN, cfg.BuyStopQ)
		}
		if buyStopOK {
			notes = append(notes, fmt.Sprintf("стоп-закуп: цена %d, потолок p%.0f=%d (продавцов %d) → закупка %s",
				newPrice, cfg.BuyStopQ*100, buyStopCeil, buyStopN, map[bool]string{true: "ВЫКЛ", false: "вкл"}[stop]))
		}
	}

	changed := newPrice != priceBefore
	if changed {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
	}

	// Причина — всегда из dec.Reason. Если сработал manual clamp, action
	// изменился на hold_manual_*, но reason всё равно должен отражать
	// исходное решение (выше уже добавлена строка в notes).
	reason := pridumalnahuyReasonRU(dec.Reason)
	if len(notes) > 0 {
		reason = reason + " | " + strings.Join(notes, " · ")
	}

	dir := "HOLD"
	if strings.Contains(action, "price_up") {
		dir = "UP"
	} else if strings.Contains(action, "price_down") {
		dir = "DOWN"
	}
	log.Printf(
		"[PRIDUMALNAHUY] %s: %s reason=%s | цена %d→%d | nacenka %d→%d | %s",
		item, dir, dec.Reason, priceBefore, newPrice, nacenkaBefore, nacenka, action,
	)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, nacenka,
		now, onlineForCap, onlineMaxForML,
	)

	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy,
		Item:           item,
		Category:       cfg.Type,
		Action:         action,
		Winner:         action,
		Fill:           stockLoad,
		Threshold:      float64(cfg.NormalSales),
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
		PriceFloor:     multiFloor,
		Step:           step,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   bots,
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}

	needBroadcast := changed || dec.Freeze || dec.Unfreeze || unfroze || paused || buyStopChanged

	mutex.Unlock()
	logCapitalCycle(capitalRow)

	if needBroadcast {
		publishPriceUpdate()
	}
	saveDailyDataNoMessageUpdate()

	return AdjustReport{
		Item:          item,
		Action:        action,
		Reason:        reason,
		PriceBefore:   priceBefore,
		PriceAfter:    newPrice,
		NacenkaBefore: nacenkaBefore,
		NacenkaAfter:  nacenka,
		Sales:         sales,
		Buys:          buys,
		TrySells:      trySells,
		OnAH:          onAH,
		Inv:           invCount,
		Held:          totalHeld,
		NormalSales:   cfg.NormalSales,
		Share:         share,
		Free:          free,
		Need:          need,
		PriceFloor:    multiFloor,
		Step:          step,
	}
}

// exceptionalMarginFrac — «исключительный» лот: ожидаемая маржа ≥ 40% цены лота.
// По истории такие лоты дают реальную маржу ~54%, уходят за ~9 минут и почти не бывают
// убыточными (1–2%), поэтому мягкий фриз их пропускает.
const exceptionalMarginFrac = 0.40

// pridumalnahuyHardFreeze — условное значение для freezeNacenkaLocked: полный стоп закупки (см. hardFreezeNacenka).
const pridumalnahuyHardFreeze = -2

// hardFreezeNacenka — наценка полного стопа: не ниже 1 000 000 и не ниже удвоенной исходной.
func hardFreezeNacenka(orig int) int {
	h := pridumalnahuyFrozenNacenka
	if v := 2 * orig; v > h {
		h = v
	}
	return h
}

// softFreezeNacenka — наценка при заморозке: закупка идёт только на исключительные лоты.
// Нет данных о цене лота → полный стоп как раньше (не ниже удвоенной исходной).
func softFreezeNacenka(orig int, avgBuy float64) int {
	hard := hardFreezeNacenka(orig)
	if avgBuy <= 0 {
		return hard
	}
	v := int(avgBuy * exceptionalMarginFrac)
	if m := orig * 6 / 5; v < m {
		v = m
	}
	if v > hard {
		v = hard
	}
	return v
}

// freezeNacenkaLocked — тормоз закупки. value == pridumalnahuyFrozenNacenka → мягкий фриз
// (только исключительные лоты); иное значение (пауза «нет книги») ставится как есть.
// Исходную наценку запоминаем в state.NacenkaOriginal. mutex уже взят.
func freezeNacenkaLocked(item string, state ItemAdjustState, cfg ItemConfig, nacenkaNow, value int) ItemAdjustState {
	if !state.NacenkaFrozen {
		state.NacenkaOriginal = cfg.Nacenka
		if state.NacenkaOriginal <= 0 {
			state.NacenkaOriginal = nacenkaNow
		}
	}
	if value == pridumalnahuyHardFreeze {
		value = hardFreezeNacenka(state.NacenkaOriginal)
	} else if value == pridumalnahuyFrozenNacenka {
		var avgBuy float64
		if inf, ok := stockTargetLocked(item, time.Now()); ok {
			avgBuy = inf.AvgBuy
		}
		value = softFreezeNacenka(state.NacenkaOriginal, avgBuy)
	}
	c := itemsConfig[item]
	c.Nacenka = value
	itemsConfig[item] = c
	ensureNacenkasInitialized()
	data.Nacenkas[item] = value
	dailyData.Nacenkas[item] = value
	state.NacenkaFrozen = true
	return state
}

// pridumalnahuyDecideHot — прайсинг ходовых товаров (hot).
// Книга и потолки игнорируются полностью: цена двигается только по продажам и запасу.
//
// Пороги берутся ТОЛЬКО из конфига (HotDownSalesMax, HotDownStockMin, HotUpSalesMax, HotUpStockMax).
// Никаких fallback на N: если порог не задан (0) — hot_misconfigured, hold. Явные числа или ничего.
//
//  1. Sales < HotDownSalesMax×(доля часа) && OnAHItem > HotDownStockMin && прошлый цикл был ниже нормы → −шаг
//     (затоварка на рынке: продаж мало для этого часа суток, на АХ висит больше порога).
//  2. Sales < HotUpSalesMax && OnAHItem < HotUpStockMax && есть место на АХ категории → +шаг
//     (дефицит: продаж мало, на АХ этого предмета меньше порога, и на АХ категории есть свободные слоты).
//  3. Иначе → hold.
//
// И снижение, и повышение смотрят на OnAH (реальное предложение на рынке): продажи и попытки коррелируют
// с лотами на АХ (r≈0.3–0.5), а с инвентарём — нет; товар в инвентаре не продаётся и спрос не набирает.
//
// Ёмкость ботов считается по АХ КАТЕГОРИИ: слоты ботов занимает то, что выставлено на АХ
// (инвентарь ещё не выставлен). Свободные лоты = SlotCapacity − OnAHCategory (5 слотов на бота, 10 с арендой).
func pridumalnahuyDecideHot(in pridumalnahuyInput) pridumalnahuyDecision {
	price := in.Price
	step := in.Step
	if step <= 0 {
		step = 1
	}

	out := pridumalnahuyDecision{
		Action:   "corridor_hold_pridumalnahuy_hot",
		NewPrice: price,
		Reason:   "hot_hold",
	}

	// Пороги обязательны. Не заданы — не угадываем, стоим.
	if in.HotDownSalesMax <= 0 || in.HotDownStockMin <= 0 ||
		in.HotUpSalesMax <= 0 || in.HotUpStockMax <= 0 {
		out.Reason = "hot_misconfigured"
		return out
	}

	// 0. Поводок: цена ушла слишком высоко относительно рынка → плавно возвращаем вниз (шаг за цикл), даже если
	// затоварки ещё нет: на такой цене продажи падают (ботинки/штаны/нагрудник: −60…80% на 40–55% выше рынка).
	if in.PriceTether > 0 && price > in.PriceTether {
		newP := price - step
		if newP < 0 {
			newP = 0
		}
		out.Action = "corridor_price_down_pridumalnahuy_hot"
		out.NewPrice = newP
		out.Reason = "hot_above_market"
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}

	// 1. Затоварка: продаж мало, на АХ выше порога → вниз.
	// «Мало» зависит от часа суток: HotDownSalesMax — порог в пиковый час, в остальные он умножается на долю часа
	// (hour_gate.go, не выше 1). Ночью 4 продажи за цикл — норма, днём в пик — провал.
	if float64(in.Sales) < hotDownSalesLimit(in.HotDownSalesMax, in.HourFrac) && in.OnAHItem > in.HotDownStockMin {
		// защита: прошлый цикл был в норме — одиночный провал продаж (19:14: 9 продаж → 19:24: 3) не повод снижать цену
		if in.NormalSales > 0 && in.PrevSales >= in.NormalSales {
			out.Reason = "hot_down_prev_ok"
			return out
		}
		newP := price - step
		if newP < 0 {
			newP = 0
		}
		out.Action = "corridor_price_down_pridumalnahuy_hot"
		out.NewPrice = newP
		out.Reason = "hot_overstock"
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}

	// 2. Дефицит: продаж мало, запас этого предмета ниже порога, и на АХ категории хватит места → вверх.
	// Ёмкость ботов: по 5 слотов на живого бота. Свободные лоты = ёмкость − то, что уже на АХ категории
	// (инвентарь не считается: он ещё не выставлен и слоты не занимает). Если свободных лотов меньше,
	// чем нужно добрать до HotUpStockMax именно этого предмета, повышение бессмысленно.
	if float64(in.Sales) < hotUpSalesLimit(in.HotUpSalesMax, in.HourFrac) && in.OnAHItem < in.HotUpStockMax {
		// слабый час суток (продаж в этот час обычно меньше половины пика): продаж мало из-за нехватки покупателей,
		// а не из-за дефицита — поднимать цену не надо
		if in.HourFrac > 0 && in.HourFrac < hotUpMinHourFrac {
			out.Reason = "hot_low_hours"
			return out
		}
		capacity := in.SlotCapacity
		if capacity <= 0 {
			capacity = in.BotsCount * baseAHSlotsPerBot
		}
		freeLots := capacity - in.OnAHCategory
		need := in.HotUpStockMax - in.OnAHItem
		if freeLots < need {
			out.Reason = "hot_slots_tight"
			return out
		}
		// потолок повышения: цена уже на уровне рынка (перцентиль HotPriceCapQ) — выше не поднимаем
		if in.PriceCap > 0 && price+step > in.PriceCap {
			out.Reason = "hot_price_cap"
			return out
		}
		out.Action = "corridor_price_up_pridumalnahuy_hot"
		out.NewPrice = price + step
		out.Reason = "hot_low_stock"
		if in.NacenkaFrozen {
			out.Unfreeze = true
		}
		return out
	}

	// 3. Иначе — hold.
	return out
}
