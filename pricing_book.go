package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// book2 — max-profit якорь от живой книги.
// Окно 30м. sell = sellMult×p10.
//
// Одна минимальная абсолютная маржа на всю категорию из книги:
//   minMarg = маржа K-й лучшей сделки (K≈free slots, при битом АХ K=3).
//   nac = max(itemSoftMin, minMarg); buyMax = sell − nac.
// Не баним SKU и не делим слоты по предметам — любой лот с щелью ≥ minMarg ок.

const (
	ahBook2Window     = 30 * time.Minute
	ahBook2MinLots    = 40
	book2MaxSteps     = 2    // обычный snap ±2 step
	book2DeepRatio    = 0.25 // |target−price|/price ≥25% → без cap (cold)
	book2MinMarginKFull = 3  // когда free=0: порог = 3-я лучшая сделка в книге
	book2CandFloor    = 100_000 // минимальная щель, чтобы лот попал в пул для порога
)

type bookCatMult struct {
	Sell float64
	Buy  float64
}

// bookProfitMultByType — sell mults; buy fallback если нет книги.
var bookProfitMultByType = map[string]bookCatMult{
	"netherite_sword-1.21":   {Sell: 1.00, Buy: 0.85},
	"netherite_armor-1.21":   {Sell: 1.05, Buy: 1.00},
	"netherite_pickaxe-1.21": {Sell: 1.00, Buy: 0.95},
	"позорная-броня-1.21":    {Sell: 1.20, Buy: 1.00},
}

var bookProfitMultDefault = bookCatMult{Sell: 1.00, Buy: 0.85}

// book2MarginCand — кандидат: абсолютная щель sell−price.
type book2MarginCand struct {
	Item   string
	Price  int
	Margin int
	Sell   int
}

// book2MinMarginFromCands — маржа K-й лучшей сделки (порог «лоты не хуже top-K»).
func book2MinMarginFromCands(cands []book2MarginCand, k int) (minMarg int, n int, ok bool) {
	if len(cands) == 0 {
		return 0, 0, false
	}
	if k < 1 {
		k = 1
	}
	sorted := append([]book2MarginCand(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Margin != sorted[j].Margin {
			return sorted[i].Margin > sorted[j].Margin
		}
		return sorted[i].Price < sorted[j].Price
	})
	if k > len(sorted) {
		k = len(sorted)
	}
	return sorted[k-1].Margin, len(sorted), true
}

type book2GlobalFloor struct {
	MinMargin int
	K         int
	NCands    int
	FullCap   int
	Free      int
	At        time.Time
	Note      string
	OK        bool
}

var book2GlobalFloorCache = map[string]book2GlobalFloor{}

const book2GlobalFloorTTL = 45 * time.Second

func book2CategoryOccupancyLocked(goType string, ahCounts map[string]int) (capacity, sumAH, free int) {
	capacity = categoryAhCapacityLocked(goType)
	if capacity < 1 {
		capacity = 1
	}
	for name, c := range ahCounts {
		if c <= 0 {
			continue
		}
		other, ok := itemsConfig[name]
		if !ok || other.Type != goType {
			continue
		}
		sumAH += c
	}
	free = capacity - sumAH
	if free < 0 {
		free = 0
	}
	return capacity, sumAH, free
}

// book2EnsureMinMarginLocked — порог маржи из книги категории; SQL вне mutex.
func book2EnsureMinMarginLocked(goType string, since time.Time, now time.Time, ahCounts map[string]int) book2GlobalFloor {
	fullCap, sumAH, free := book2CategoryOccupancyLocked(goType, ahCounts)
	k := free
	if k < 1 {
		k = book2MinMarginKFull
	}
	if prev, ok := book2GlobalFloorCache[goType]; ok && now.Sub(prev.At) < book2GlobalFloorTTL && prev.Free == free && prev.OK {
		return prev
	}
	type snap struct {
		id   string
		mult bookCatMult
	}
	var items []snap
	for id, cfg := range itemsConfig {
		if cfg.Type != goType {
			continue
		}
		items = append(items, snap{id: id, mult: bookMultForType(goType)})
	}
	mutex.Unlock()
	var cands []book2MarginCand
	for _, it := range items {
		ps, n := ahBookUniquePricesSince(it.id, since)
		if n < ahBook2MinLots {
			continue
		}
		p10 := ps[n/10]
		if p10 <= 0 {
			continue
		}
		sell := int(float64(p10)*it.mult.Sell + 0.5)
		if sell <= 0 {
			continue
		}
		for _, price := range ps {
			m := sell - price
			if m >= book2CandFloor {
				cands = append(cands, book2MarginCand{Item: it.id, Price: price, Margin: m, Sell: sell})
			}
		}
	}
	mutex.Lock()

	minMarg, nCands, ok := book2MinMarginFromCands(cands, k)
	out := book2GlobalFloor{
		MinMargin: minMarg,
		K:         k,
		NCands:    nCands,
		FullCap:   fullCap,
		Free:      free,
		At:        now,
		OK:        ok,
		Note: fmt.Sprintf(
			"minMarg=%d K=%d cands=%d free=%d/%d sumAH=%d",
			minMarg, k, nCands, free, fullCap, sumAH,
		),
	}
	book2GlobalFloorCache[goType] = out
	return out
}

// nacenkaBaseLocked — базовый nac из JSON (не runtime ratchet).
func nacenkaBaseLocked(item string, cfg ItemConfig) int {
	if itemsNacenkaBase != nil {
		if b, ok := itemsNacenkaBase[item]; ok && b > 0 {
			return b
		}
	}
	if cfg.NacenkaMin > 0 {
		return cfg.NacenkaMin
	}
	if cfg.Nacenka > 0 {
		return cfg.Nacenka
	}
	return 300_000
}

func bookMultForType(goType string) bookCatMult {
	if m, ok := bookProfitMultByType[goType]; ok {
		return m
	}
	return bookProfitMultDefault
}

func bookSnapWithMarker(target, step, priceBefore int) int {
	if target <= 0 {
		return priceBefore
	}
	if step > 0 {
		target = (target / step) * step
	}
	marker := 0
	if priceBefore > 0 {
		marker = priceBefore % 100
	}
	if marker > 0 {
		target = (target/100)*100 + marker
	}
	if target <= 0 {
		return priceBefore
	}
	return target
}

// bookTargetsFromLiveBook — sell/buy только от живой p10×mult (базовый buy категории).
func bookTargetsFromLiveBook(p10, step, priceBefore, priceFloor, nacenkaMin int, goType string) (sell, nac int, src string) {
	m := bookMultForType(goType)
	return bookTargetsFromLiveBookBuy(p10, step, priceBefore, priceFloor, nacenkaMin, goType, m.Buy)
}

// bookTargetsFromLiveBookBuy — как bookTargetsFromLiveBook, но buyMult задаётся снаружи (adapt).
func bookTargetsFromLiveBookBuy(p10, step, priceBefore, _priceFloor, nacenkaMin int, goType string, buyMult float64) (sell, nac int, src string) {
	if p10 <= 0 {
		return priceBefore, nacenkaMin, "no_p10"
	}
	m := bookMultForType(goType)
	if m.Sell <= 0 {
		m = bookProfitMultDefault
	}
	if buyMult <= 0 {
		buyMult = m.Buy
	}
	if buyMult <= 0 {
		buyMult = bookProfitMultDefault.Buy
	}
	if buyMult >= m.Sell {
		buyMult = m.Sell * 0.85
	}

	rawSell := int(float64(p10)*m.Sell + 0.5)
	sell = bookSnapWithMarker(rawSell, step, priceBefore)

	rawBuy := int(float64(p10)*buyMult + 0.5)
	buyMax := bookSnapWithMarker(rawBuy, step, priceBefore)
	if buyMax <= 0 {
		buyMax = rawBuy
	}
	if buyMax >= sell && step > 0 {
		buyMax = sell - step
	}
	if buyMax < 0 {
		buyMax = 0
	}

	nac = sell - buyMax
	if nac < nacenkaMin {
		nac = nacenkaMin
	}
	if nac < 0 {
		nac = 0
	}
	if sell-nac <= 0 && step > 0 {
		nac = sell - step
		if nac < nacenkaMin {
			nac = nacenkaMin
		}
		if nac < 0 {
			nac = 0
		}
	}
	src = fmt.Sprintf("live30m p10×sell%.2f/buy%.3f", m.Sell, buyMult)
	return sell, nac, src
}

// book2ClampStep — ограничивает прыжок за цикл, кроме глубокого under/over vs target.
func book2ClampStep(priceBefore, target, step int) int {
	if step <= 0 || priceBefore <= 0 || target <= 0 || target == priceBefore {
		return target
	}
	delta := target - priceBefore
	if delta < 0 {
		delta = -delta
	}
	deep := float64(delta)/float64(priceBefore) >= book2DeepRatio
	if deep {
		return target
	}
	maxDelta := book2MaxSteps * step
	if target > priceBefore+maxDelta {
		return priceBefore + maxDelta
	}
	if target < priceBefore-maxDelta {
		return priceBefore - maxDelta
	}
	return target
}

func setRuntimeNacenkaLocked(item string, nac int) {
	if nac < 0 {
		nac = 0
	}
	if cfg, ok := itemsConfig[item]; ok {
		cfg.Nacenka = nac
		itemsConfig[item] = cfg
	}
	if data.Nacenkas == nil {
		data.Nacenkas = make(map[string]int)
	}
	if dailyData.Nacenkas == nil {
		dailyData.Nacenkas = make(map[string]int)
	}
	data.Nacenkas[item] = nac
	dailyData.Nacenkas[item] = nac
}

func adjustPriceBook(
	item string,
	cfg ItemConfig,
	now time.Time,
	_ time.Time,
	sales, buys, trySells, profitNow int,
	state ItemAdjustState,
	priceBefore, nacenka, nacenkaBefore, step, minPrice, nacenkaSumNow, nacenkaSumPrev, priceFloor int,
	onAH, invCount, _held, share, free, need, stockNorm int,
	underbuyOK bool,
	tryRatio, stockLoad float64,
	onlineForCap, onlineMaxForML int,
	ahCounts map[string]int,
) AdjustReport {
	if step <= 0 {
		step = 1
	}
	price := priceBefore
	// book2: не поднимаем к legacy priceFloor (minBuy+runtime nac) — см. bookTargetsFromLiveBook.

	blockUp, blockDown := manualDirectionClampLocked(item, cfg.AnalysisTime)
	baseNac := nacenkaBaseLocked(item, cfg)
	softMin := cfg.NacenkaMin
	if softMin <= 0 {
		softMin = baseNac
	}
	if softMin <= 0 {
		softMin = 200_000
	}
	mult := bookMultForType(cfg.Type)

	bookSince := now.Add(-ahBook2Window)
	gFloor := book2EnsureMinMarginLocked(cfg.Type, bookSince, now, ahCounts)

	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBook2MinLots || p10 <= 0 {
		p10OK = false
	}
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	buySrc := ""
	buyEff := mult.Buy
	notes := []string{
		fmt.Sprintf(
			"book2 minMarg 30m p10 sell×%.2f p10=%d n=%d ok=%v type=%s onAH=%d sales=%d buys=%d softMin=%d | %s",
			mult.Sell, p10, p10N, p10OK, cfg.Type, onAH, sales, buys, softMin, gFloor.Note,
		),
	}

	if p10OK {
		rawSell := int(float64(p10)*mult.Sell + 0.5)
		sellT := bookSnapWithMarker(rawSell, step, priceBefore)
		sellT = bookSnapWithMarker(book2ClampStep(priceBefore, sellT, step), step, priceBefore)

		// Одна щель на категорию из книги + пол SKU. Все предметы могут покупаться.
		nacT := softMin
		if gFloor.OK && gFloor.MinMargin > nacT {
			nacT = gFloor.MinMargin
		}
		if nacT >= sellT && step > 0 {
			nacT = sellT - step
		}
		if nacT < softMin {
			nacT = softMin
		}
		if nacT < 0 {
			nacT = 0
		}
		buyMax := sellT - nacT
		if buyMax < 0 {
			buyMax = 0
		}
		buyEff = 0
		if p10 > 0 {
			buyEff = float64(buyMax) / float64(p10)
		}
		buySrc = fmt.Sprintf("minMarg=%d nac=%d buyMax=%d (%.3f×p10)", nacT, nacT, buyMax, buyEff)
		notes = append(notes, buySrc)

		newNac = nacT
		newPrice = sellT
		decReason = "book_snap"
		if newPrice > priceBefore {
			action = "book_price_up"
		} else if newPrice < priceBefore {
			action = "book_price_down"
		} else if newNac != nacenkaBefore {
			action = "book_nacenka_set"
		} else {
			action = "book_hold_at_target"
			decReason = "at_target"
		}
		notes = append(notes, fmt.Sprintf(
			"target sell=%d nac=%d buyMax=%d (p10=%d %s)",
			sellT, nacT, sellT-nacT, p10, buySrc,
		))
	}

	if blockDown && newPrice < priceBefore {
		newPrice = priceBefore
		action = "hold_manual_min"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↓ запрещён")
	}
	if blockUp && newPrice > priceBefore {
		newPrice = priceBefore
		action = "hold_manual_max"
		if data.LastManualKind[item] == "set" {
			action = "hold_manual_set"
		}
		notes = append(notes, "manual → ↑ запрещён")
	}

	nacMin := softMin
	if gFloor.OK && gFloor.MinMargin > nacMin {
		nacMin = gFloor.MinMargin
	}
	if p10OK && p10 > 0 && newPrice > 0 {
		wantNac := newNac
		if wantNac < nacMin {
			wantNac = nacMin
		}
		if wantNac >= newPrice && step > 0 {
			wantNac = newPrice - step
		}
		if wantNac < softMin {
			wantNac = softMin
		}
		if wantNac < 0 {
			wantNac = 0
		}
		if wantNac != newNac {
			notes = append(notes, fmt.Sprintf("nac clamp %d→%d (buyMax=%d)", newNac, wantNac, newPrice-wantNac))
			newNac = wantNac
			if !strings.Contains(action, "price_") && action != "book_nacenka_set" {
				decReason = "min_margin"
			}
		}
		buyEff = 0
		if p10 > 0 && newPrice > newNac {
			buyEff = float64(newPrice-newNac) / float64(p10)
		}
	} else if newPrice > 0 {
		buyCap := newPrice * 85 / 100
		if step > 0 && buyCap >= newPrice {
			buyCap = newPrice - step
		}
		if buyCap < 0 {
			buyCap = 0
		}
		wantNac := newPrice - buyCap
		if wantNac < softMin {
			wantNac = softMin
		}
		if wantNac > newNac {
			notes = append(notes, fmt.Sprintf("thin book buy-cap 85%% → nac %d→%d (buyMax=%d)", newNac, wantNac, newPrice-wantNac))
			newNac = wantNac
			decReason = "no_book_buy_cap"
		} else {
			notes = append(notes, "thin/absent book 30m → hold (ждём скан)")
		}
	}

	// намеренно без clamp к priceFloor — иначе book2 не может сесть к p10 ниже minBuy+nac

	setRuntimeNacenkaLocked(item, newNac)

	state.LastCycleSales = sales
	state.LastCycleProfit = profitNow
	state.LastCycleNacenkaSum = nacenkaSumNow
	if onAH > 0 {
		state.EmptyMarketGapStreak = 0
	} else if sales == 0 && buys == 0 {
		state.EmptyMarketGapStreak++
	} else {
		state.EmptyMarketGapStreak = 0
	}
	data.AdjustState[item] = state
	dailyData.AdjustState[item] = state

	priceChanged := newPrice != priceBefore
	nacChanged := newNac != nacenkaBefore
	if priceChanged {
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
	} else if nacChanged {
		dir = "NAC"
	}
	log.Printf("[BOOK2] %s: %s reason=%s | цена %d→%d nac %d→%d | p10=%d n=%d %s | %s",
		item, dir, decReason, priceBefore, newPrice, nacenkaBefore, newNac, p10, p10N, buySrc, action)

	queueMLDecisionLocked(
		item, cfg, action,
		priceBefore, newPrice, nacenkaBefore, newNac,
		now,
		onlineForCap, onlineMaxForML,
	)

	fair := relistFairStock(share)
	capitalRow := CapitalCycleRow{
		Policy:         capitalPolicy + "+book2",
		Item:           item,
		Category:       cfg.Type,
		Action:         action,
		Winner:         action,
		Dump:           0,
		Fill:           stockLoad,
		Skim:           0,
		Threshold:      float64(fair),
		Sales:          sales,
		Buys:           buys,
		TrySells:       trySells,
		OnAH:           onAH,
		Inv:            invCount,
		Held:           onAH,
		Share:          fair,
		Free:           free,
		Need:           need,
		NormalSales:    relistSalesNorm,
		NormalCount:    stockNorm,
		TryRatio:       tryRatio,
		StockLoad:      stockLoad,
		Underbuy:       underbuyOK,
		PriceBefore:    priceBefore,
		PriceAfter:     newPrice,
		NacenkaBefore:  nacenkaBefore,
		NacenkaAfter:   newNac,
		NacenkaSumNow:  nacenkaSumNow,
		NacenkaSumPrev: nacenkaSumPrev,
		PriceFloor:     priceFloor,
		Step:           step,
		Cooldown:       0,
		PlayersOnline:  onlineForCap,
		Notes:          strings.Join(notes, " · "),
		ProfitNow:      profitNow,
		MinBuyHistory:  minPrice,
		BotsCategory:   botsForGoTypeLocked(cfg.Type),
		CycleMinutes:   cfg.AnalysisTime.Minutes(),
		GoodStreak:     0,
		DecisionAt:     now,
		CycleDuration:  cfg.AnalysisTime,
	}

	needBroadcast := priceChanged || nacChanged
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
		NacenkaAfter:  newNac,
		Sales:         sales,
		Buys:          buys,
		TrySells:      trySells,
		NormalSales:   relistSalesNorm,
		Step:          step,
	}
}
