package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	capitalPolicyPridumalnahuy = "pridumalnahuy_2026_10"
	pridumalnahuyFrozenNacenka = 1_000_000
)

func isPricingPolicyPridumalnahuy() bool {
	return capitalPolicy == capitalPolicyPridumalnahuy
}

type pridumalnahuyInput struct {
	Sales, TrySells, NormalSales int

	BotsCount          int
	OnAHCategory       int
	TotalStockCategory int
	CapacityCategory   int

	OnAHItem       int
	InvItem        int
	TotalStockItem int

	Price, Step        int
	MultiFloor         int
	MultiFloorOK       bool
	BookMid            int
	BookMidOK          bool
	BlockUp, BlockDown bool

	NacenkaFrozen bool
}

type pridumalnahuyDecision struct {
	Action   string
	NewPrice int
	Reason   string
	Freeze   bool
	Unfreeze bool
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

func pridumalnahuyDecide(in pridumalnahuyInput) pridumalnahuyDecision {
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

	// --- Рельсы книги (приоритетнее всего) ---
	// if !in.BlockUp && in.MultiFloorOK && in.MultiFloor > 0 && price < in.MultiFloor {
	// 	out.Action = "corridor_price_up_pridumalnahuy_book_floor"
	// 	out.NewPrice = in.MultiFloor
	// 	out.Reason = "below_book_floor_jump"
	// 	return out
	// }
	if !in.BlockDown && in.BookMidOK && in.BookMid > 0 && price > in.BookMid {
		out.Action = "corridor_price_down_pridumalnahuy_book_mid"
		out.NewPrice = in.BookMid
		out.Reason = "above_book_mid_jump"
		return out
	}

	// --- 0. sales >= N, но trySells ≥ sales*20 → DOWN (перегрев попыток при норм. продажах) ---
	if in.Sales >= N && in.TrySells >= in.Sales*20 {
		newP, ok := clampDownToFloor(price, step, in.MultiFloor, in.MultiFloorOK)
		if ok {
			out.Action = "corridor_price_down_pridumalnahuy"
			out.NewPrice = newP
			out.Reason = "sales_above_normal_try_overheat"
			if in.NacenkaFrozen {
				out.Unfreeze = true
			}
			return out
		}
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "sales_above_normal_try_overheat_at_floor"
		return out
	}

	// --- 1. sales >= N → HOLD ---
	if in.Sales >= N {
		out.Action = "corridor_hold_pridumalnahuy_sellthrough"
		out.Reason = "sales_above_normal"
		return out
	}

	// --- 2. trySells > sales*7 (и > N) → DOWN (не ниже пола) ---
	if in.TrySells > in.Sales*7 && in.TrySells > N {
		newP, ok := clampDownToFloor(price, step, in.MultiFloor, in.MultiFloorOK)
		if ok {
			out.Action = "corridor_price_down_pridumalnahuy"
			out.NewPrice = newP
			out.Reason = "try_sells_excess"
			if in.NacenkaFrozen {
				out.Unfreeze = true
			}
			return out
		}
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "try_sells_excess_at_floor"
		return out
	}

	// --- NEW: перегрев наличия по предмету → UP запрещён ---
	// Если total > sales*2 И total > N, ветки UP (3 и 5) не срабатывают.
	// DOWN/HOLD выше уже отработали, FREEZE ниже не затрагивается.
	if in.TotalStockItem > in.Sales*2 && in.TotalStockItem > N {
		out.Action = "corridor_hold_pridumalnahuy_stock_overheat"
		out.Reason = "stock_overheat_no_up"
		return out
	}

	// --- 3. (5*bots - onAHCategory) + onAHItem >= N → UP (не выше потолка) ---
	freeAHCategory := 5*in.BotsCount - in.OnAHCategory
	if freeAHCategory < 0 {
		freeAHCategory = 0
	}
	if freeAHCategory+in.OnAHItem >= N {
		newP, ok := clampUpToCeil(price, step, in.BookMid, in.BookMidOK)
		if ok {
			out.Action = "corridor_price_up_pridumalnahuy"
			out.NewPrice = newP
			out.Reason = "ah_slots_with_item_above_normal"
			if in.NacenkaFrozen {
				out.Unfreeze = true
			}
			return out
		}
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "ah_slots_at_ceil"
		return out
	}

	// --- 4. invItem >= N → FREEZE ---
	if in.InvItem >= N {
		out.Action = "corridor_hold_pridumalnahuy_freeze"
		out.Reason = "inv_above_normal"
		out.Freeze = true
		return out
	}

	// --- 5. свободных слотов хватает на норму → UP, иначе FREEZE ---
	freeSlotsCategory := in.CapacityCategory - in.TotalStockCategory
	if freeSlotsCategory < 0 {
		freeSlotsCategory = 0
	}
	if freeSlotsCategory+in.TotalStockItem >= N {
		newP, ok := clampUpToCeil(price, step, in.BookMid, in.BookMidOK)
		if ok {
			out.Action = "corridor_price_up_pridumalnahuy"
			out.NewPrice = newP
			out.Reason = "slots_free"
			if in.NacenkaFrozen {
				out.Unfreeze = true
			}
			return out
		}
		out.Action = "corridor_hold_pridumalnahuy_no_up"
		out.Reason = "slots_free_at_ceil"
		return out
	}
	out.Action = "corridor_hold_pridumalnahuy_freeze"
	out.Reason = "inv_slots_tight"
	out.Freeze = true
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
	capacityCategory := bots * botTotalSlots

	bookSince := now.Add(-ahBookRaiseWindow)
	mutex.Unlock()
	multiFloor, multiFloorQ, _ := stockNormBookCatchupFloor(cfg, bookSince)
	bookMid, bookMidQ, bookMidOK := stockNormBookMid(cfg, now.Add(-ahBook2Window))
	mutex.Lock()

	nacenkaFrozen := state.NacenkaFrozen

	dec := pridumalnahuyDecide(pridumalnahuyInput{
		Sales:              sales,
		TrySells:           trySells,
		NormalSales:        cfg.NormalSales,

		BotsCount:          bots,
		OnAHCategory:       onAHCategory,
		TotalStockCategory: totalStockCategory,
		CapacityCategory:   capacityCategory,

		OnAHItem:       onAH,
		InvItem:        invCount,
		TotalStockItem: totalHeld,

		Price:        priceBefore,
		Step:         step,
		MultiFloor:   multiFloor,
		MultiFloorOK: multiFloor > 0,
		BookMid:      bookMid,
		BookMidOK:    bookMidOK,
		BlockUp:      blockUp,
		BlockDown:    blockDown,

		NacenkaFrozen: nacenkaFrozen,
	})

	newPrice := dec.NewPrice
	action := dec.Action
	notes := []string{
		fmt.Sprintf(
			"pridumalnahuy reason=%s sales=%d N=%d try=%d onAHcat=%d invcat=%d totalcat=%d cap=%d onAHitem=%d invItem=%d totalItem=%d bots=%d floor(p%.0f)=%d mid(p%.0f)=%d frozen=%v",
			dec.Reason, sales, cfg.NormalSales, trySells,
			onAHCategory, invCategory, totalStockCategory,
			capacityCategory, onAH, invCount, totalHeld, bots,
			multiFloorQ*100, multiFloor, bookMidQ*100, bookMid,
			nacenkaFrozen,
		),
	}

	if dec.Freeze {
		if !nacenkaFrozen {
			state.NacenkaOriginal = cfg.Nacenka
			if state.NacenkaOriginal <= 0 {
				state.NacenkaOriginal = nacenkaBefore
			}
		}
		c := itemsConfig[item]
		c.Nacenka = pridumalnahuyFrozenNacenka
		itemsConfig[item] = c
		data.Nacenkas[item] = pridumalnahuyFrozenNacenka
		dailyData.Nacenkas[item] = pridumalnahuyFrozenNacenka
		state.NacenkaFrozen = true
		nacenka = pridumalnahuyFrozenNacenka
		notes = append(notes, fmt.Sprintf("FREEZE nacenka → %d (orig %d)", pridumalnahuyFrozenNacenka, state.NacenkaOriginal))
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

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	changed := newPrice != priceBefore
	if changed {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		lastPriceUpdate[item] = now
	}

	reason := actionReasonRU(action)
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

	needBroadcast := changed || dec.Freeze || dec.Unfreeze

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