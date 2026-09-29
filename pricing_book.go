package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// book2 — цены целиком из живой книги.
// Окно 30м.
//
// Дешёвые / volume (sword5/7 и т.п.):
//   sell = max(seller-p40, buyEdge+nac); buyEdge = seller-p10; nac ≥ softMin.
//   Если buyEdge+nac > p40 — поднимаем sell (место под наценку).
//
// Дорогие (mega/pochti/яд / sell≥volume ceiling):
//   sell = нормальный низ мульти-селлеров (≥3 лота одного SKU), не p40 витрин;
//   buyMax = sell − nac; sell НЕ поднимаем под buyEdge+nac (иначе снова уезжаем вверх).
// SKU не баним.

const (
	ahBook2Window        = 30 * time.Minute
	ahBook2MinLots       = 40
	book2MaxSteps        = 2
	book2DeepRatio       = 0.25
	book2CandFloor       = 100_000
	book2FillSupplyMult  = 2
	book2MinMarginKFloor = 10
	book2NacFloorAbs     = 300_000
	book2VolumeP10Max    = 2_500_000 // sword7/фарм часто ~1.2–2.0M — не путать с mega
	// Потолок nac для *полов* volume (не ломает abs softMin 300k на sell).
	book2VolumeMinBuyRatio = 0.85
	book2FatMinBuyRatio    = 0.70
	// Дорогие SKU: без живой книги / sell<<книги — buyMax=0 (не копить меги вслепую).
	book2ExpensiveBuyFreezeRatio = 0.85
)

type bookCatMult struct {
	Sell float64
	Buy  float64
}

var bookProfitMultByType = map[string]bookCatMult{
	"netherite_sword-1.21":   {Sell: 1.00, Buy: 0.85},
	"netherite_armor-1.21":   {Sell: 1.05, Buy: 1.00},
	"netherite_pickaxe-1.21": {Sell: 1.00, Buy: 0.95},
	"позорная-броня-1.21":    {Sell: 1.20, Buy: 1.00},
}

var bookProfitMultDefault = bookCatMult{Sell: 1.00, Buy: 0.85}

// book2ExpensiveSKU — мега/почти/яд и любой каталог ≥ volume ceiling.
func book2ExpensiveSKU(item string, sellPrice int) bool {
	if sellPrice >= book2VolumeP10Max {
		return true
	}
	id := strings.ToLower(item)
	return strings.Contains(id, "megasword") || strings.Contains(id, "pochti")
}

// book2FreezeBuyNac — nac так, что buyMax=0 (боты не закупают).
func book2FreezeBuyNac(sell int) int {
	if sell <= 0 {
		return 0
	}
	return sell
}

type book2MarginCand struct {
	Item   string
	Price  int
	Margin int
	Sell   int
}

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

func book2CountMarginsAtLeast(cands []book2MarginCand, floor int) int {
	n := 0
	for _, c := range cands {
		if c.Margin >= floor {
			n++
		}
	}
	return n
}

// book2SkuRaiseK — сколько «лучших» сделок SKU оставляем над его полом.
func book2SkuRaiseK(nElig int) int {
	if nElig <= 0 {
		return 1
	}
	k := nElig / 3
	if k < 3 {
		k = 3
	}
	if k > 12 {
		k = 12
	}
	if k > nElig {
		k = nElig
	}
	return k
}

func book2IsVolumeP10(p10 int) bool {
	return p10 > 0 && p10 < book2VolumeP10Max
}

// book2MaxNacForP10 — потолок щели для per-SKU/global *полов* (не для softMin).
func book2MaxNacForP10(p10 int) int {
	if p10 <= 0 {
		return 0
	}
	ratio := book2FatMinBuyRatio
	if book2IsVolumeP10(p10) {
		ratio = book2VolumeMinBuyRatio
	}
	maxNac := p10 - int(float64(p10)*ratio+0.5)
	if maxNac < 0 {
		return 0
	}
	return maxNac
}

// book2PickNac — жёсткий пол softMin/skuFloor. Buy-ratio и buyEdge НЕ режут nac:
// если места мало — поднимаем sell (см. adjust), а не наценку.
func book2PickNac(sellT, wantFloor, softMin, step int) int {
	nac := wantFloor
	if softMin > 0 && nac < softMin {
		nac = softMin
	}
	if sellT > 0 {
		lim := sellT - 1
		if step > 0 {
			lim = sellT - step
		}
		if lim < 0 {
			lim = 0
		}
		if nac > lim {
			nac = lim
		}
	}
	if nac < 0 {
		return 0
	}
	return nac
}

// book2MinSellForNac — sell не ниже buyEdge+nac, иначе buyMax < buyEdge при полном softMin.
func book2MinSellForNac(buyEdge, nac, step int) int {
	if buyEdge <= 0 {
		return 0
	}
	minSell := buyEdge + nac
	if step > 0 && nac < step {
		minSell = buyEdge + step
	}
	return minSell
}

type book2FloorPlan struct {
	FloorByItem map[string]int
	GlobalMarg  int
	GlobalOn    bool
	FullCap     int
	Free        int
	NCands      int
	At          time.Time
	Note        string
	OK          bool
}

var book2FloorPlanCache = map[string]book2FloorPlan{}

const book2FloorPlanTTL = 45 * time.Second

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

// book2PlanFloors — чистая логика полов (для тестов).
// itemBase: JSON/абс пол; itemP10: для яруса volume vs fat.
func book2PlanFloors(
	byItem map[string][]book2MarginCand,
	itemBase map[string]int,
	itemP10 map[string]int,
	fullCap, free int,
) book2FloorPlan {
	supplyNeed := fullCap * book2FillSupplyMult
	if free*book2FillSupplyMult > supplyNeed {
		supplyNeed = free * book2FillSupplyMult
	}
	if supplyNeed < book2MinMarginKFloor {
		supplyNeed = book2MinMarginKFloor
	}

	var all, volume []book2MarginCand
	floors := make(map[string]int)
	for id, cands := range byItem {
		base := book2NacFloorAbs
		if b := itemBase[id]; b > base {
			base = b
		}
		// кандидаты для raise — только уже выше базового пола
		var elig []book2MarginCand
		for _, c := range cands {
			if c.Margin >= base {
				elig = append(elig, c)
			}
			if c.Margin >= book2NacFloorAbs {
				all = append(all, c)
				if itemP10[id] > 0 && itemP10[id] < book2VolumeP10Max {
					volume = append(volume, c)
				}
			}
		}
		floor := base
		if len(elig) > 0 {
			ki := book2SkuRaiseK(len(elig))
			if f, _, ok := book2MinMarginFromCands(elig, ki); ok && f > floor {
				floor = f
			}
		}
		// volume/fat: не даём щели съесть buy ниже min ratio
		if p10 := itemP10[id]; p10 > 0 {
			if cap := book2MaxNacForP10(p10); floor > cap {
				floor = cap
			}
		}
		if floor < base {
			floor = base
		}
		floors[id] = floor
	}

	g, nAll, okG := book2MinMarginFromCands(all, supplyNeed)
	if okG && g < book2NacFloorAbs {
		g = book2NacFloorAbs
	}
	volNeed := supplyNeed / 4
	if volNeed < 8 {
		volNeed = 8
	}
	// Global: max пол для FAT, если volume ещё показывает лоты над порогом.
	// На volume SKU global НЕ накладываем — иначе 800k при sell≈1.2M → buy 0.4×p10.
	globalOn := false
	globalG := 0
	if nAll >= supplyNeed && len(volume) >= volNeed {
		uniq := append([]book2MarginCand(nil), volume...)
		sort.SliceStable(uniq, func(i, j int) bool {
			return uniq[i].Margin > uniq[j].Margin
		})
		for _, c := range uniq {
			f := c.Margin
			if f < book2NacFloorAbs {
				break
			}
			if book2CountMarginsAtLeast(all, f) >= supplyNeed && book2CountMarginsAtLeast(volume, f) >= volNeed {
				globalG = f
				globalOn = true
				break
			}
		}
	}
	if globalOn {
		g = globalG
		for id := range floors {
			if book2IsVolumeP10(itemP10[id]) {
				continue
			}
			floor := floors[id]
			if g > floor {
				floor = g
			}
			if p10 := itemP10[id]; p10 > 0 {
				if cap := book2MaxNacForP10(p10); floor > cap {
					floor = cap
				}
			}
			floors[id] = floor
		}
	} else if !okG {
		g = 0
	}

	mode := "perSKU"
	if globalOn {
		mode = fmt.Sprintf("global+%d", g)
	}
	// краткий топ полов для лога
	type kv struct {
		id string
		f  int
	}
	var top []kv
	for id, f := range floors {
		top = append(top, kv{id, f})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].f > top[j].f })
	parts := make([]string, 0, 4)
	for i, t := range top {
		if i >= 4 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%dk", t.id, t.f/1000))
	}

	return book2FloorPlan{
		FloorByItem: floors,
		GlobalMarg:  g,
		GlobalOn:    globalOn,
		FullCap:     fullCap,
		Free:        free,
		NCands:      nAll,
		OK:          len(floors) > 0,
		Note: fmt.Sprintf(
			"floors mode=%s cands=%d vol=%d need=%d free=%d/%d [%s]",
			mode, nAll, len(volume), supplyNeed, free, fullCap, strings.Join(parts, " "),
		),
	}
}

// book2EnsureFloorPlanLocked — кэш; SQL вне mutex.
func book2EnsureFloorPlanLocked(goType string, since time.Time, now time.Time, ahCounts map[string]int) book2FloorPlan {
	fullCap, _, free := book2CategoryOccupancyLocked(goType, ahCounts)
	if prev, ok := book2FloorPlanCache[goType]; ok && now.Sub(prev.At) < book2FloorPlanTTL && prev.Free == free && prev.OK {
		return prev
	}
	type snap struct {
		id   string
		base int
		mult bookCatMult
	}
	var items []snap
	for id, cfg := range itemsConfig {
		if cfg.Type != goType {
			continue
		}
		base := nacenkaBaseLocked(id, cfg)
		if base < book2NacFloorAbs {
			base = book2NacFloorAbs
		}
		items = append(items, snap{id: id, base: base, mult: bookMultForType(goType)})
	}
	mutex.Unlock()
	byItem := make(map[string][]book2MarginCand)
	itemP10 := make(map[string]int)
	itemBase := make(map[string]int)
	for _, it := range items {
		itemBase[it.id] = it.base
		mkt, nSell, ok := ahBookMarketAnchorSince(it.id, since)
		if !ok || nSell < ahBookMarketMinSellers || mkt <= 0 {
			byItem[it.id] = nil
			continue
		}
		itemP10[it.id] = mkt
		sell := int(float64(mkt)*it.mult.Sell + 0.5)
		if sell <= 0 {
			continue
		}
		// кандидаты щели — по unique uuid (больше точек), якорь sell от seller-market
		ps, n := ahBookUniquePricesSince(it.id, since)
		if n < ahBook2MinLots {
			byItem[it.id] = nil
			continue
		}
		var cands []book2MarginCand
		for _, price := range ps {
			m := sell - price
			if m >= book2CandFloor {
				cands = append(cands, book2MarginCand{Item: it.id, Price: price, Margin: m, Sell: sell})
			}
		}
		byItem[it.id] = cands
	}
	mutex.Lock()

	plan := book2PlanFloors(byItem, itemBase, itemP10, fullCap, free)
	plan.At = now
	book2FloorPlanCache[goType] = plan
	return plan
}

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
	if softMin < book2NacFloorAbs {
		softMin = book2NacFloorAbs
	}
	mult := bookMultForType(cfg.Type)

	bookSince := now.Add(-ahBook2Window)
	plan := book2EnsureFloorPlanLocked(cfg.Type, bookSince, now, ahCounts)
	skuFloor := softMin
	if plan.OK {
		if f, ok := plan.FloorByItem[item]; ok && f > skuFloor {
			skuFloor = f
		}
	}

	mutex.Unlock()
	// Дешёвые: sell=p40 / buyEdge=p10. Дорогие: низ мульти-селлеров (≥3 лота).
	sellMkt, buyEdge, mktN, mktOK := ahBookMarketAnchorsSince(item, bookSince)
	anchorMode := "p40seller"
	expensiveBottom := book2ExpensiveSKU(item, 0)
	if !expensiveBottom && mktOK {
		expensiveBottom = book2ExpensiveSKU(item, sellMkt)
	}
	if expensiveBottom {
		s2, b2, n2, ok2 := ahBookExpensiveBottomAnchorsSince(item, bookSince)
		if ok2 && s2 > 0 {
			sellMkt, buyEdge, mktN, mktOK = s2, b2, n2, true
			anchorMode = "multiLow"
		}
	}
	if !mktOK || sellMkt <= 0 {
		mktOK = false
	}
	p10, p10N, p10OK := sellMkt, mktN, mktOK
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	buySrc := ""
	buyEff := mult.Buy
	notes := []string{
		fmt.Sprintf(
			"book2 hybrid 30m sell×%.2f mkt(%s)=%d buyEdge=%d sellers=%d ok=%v type=%s onAH=%d sales=%d buys=%d softMin=%d skuFloor=%d | %s",
			mult.Sell, anchorMode, p10, buyEdge, p10N, p10OK, cfg.Type, onAH, sales, buys, softMin, skuFloor, plan.Note,
		),
	}

	if p10OK {
		nacWant := skuFloor
		if softMin > 0 && nacWant < softMin {
			nacWant = softMin
		}
		rawSell := int(float64(p10)*mult.Sell + 0.5)
		// volume: sell ≥ buyEdge+nac. expensive multiLow — нет, иначе низ снова уедет вверх.
		if anchorMode != "multiLow" {
			if minSell := book2MinSellForNac(buyEdge, nacWant, step); minSell > rawSell {
				rawSell = minSell
			}
		}
		sellT := bookSnapWithMarker(rawSell, step, priceBefore)
		sellT = bookSnapWithMarker(book2ClampStep(priceBefore, sellT, step), step, priceBefore)
		if anchorMode != "multiLow" {
			if minSell := book2MinSellForNac(buyEdge, nacWant, step); sellT < minSell {
				sellT = bookSnapWithMarker(minSell, step, priceBefore)
			}
		}

		nacT := book2PickNac(sellT, skuFloor, softMin, step)
		buyMax := sellT - nacT
		if buyMax < 0 {
			buyMax = 0
		}
		buyEff = 0
		if p10 > 0 {
			buyEff = float64(buyMax) / float64(p10)
		}
		modeTag := "perSKU"
		if plan.GlobalOn {
			modeTag = fmt.Sprintf("global+%d", plan.GlobalMarg)
		}
		buySrc = fmt.Sprintf("%s floor=%d nac=%d buyMax=%d buyEdge=%d (%.3f×mkt)", modeTag, skuFloor, nacT, buyMax, buyEdge, buyEff)
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
			"target sell=%d nac=%d buyMax=%d (mkt=%d buyEdge=%d %s)",
			sellT, nacT, sellT-nacT, p10, buyEdge, buySrc,
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

	nacMin := book2PickNac(newPrice, skuFloor, softMin, step)
	if p10OK && p10 > 0 && newPrice > 0 {
		wantNac := newNac
		if wantNac < nacMin {
			wantNac = nacMin
		}
		wantNac = book2PickNac(newPrice, wantNac, softMin, step)
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
		// sell сильно под книгой (manual max / дамп) — не докупать в яму
		if book2ExpensiveSKU(item, p10) && newPrice*100 < int(book2ExpensiveBuyFreezeRatio*100)*p10 {
			fr := book2FreezeBuyNac(newPrice)
			if fr > newNac {
				notes = append(notes, fmt.Sprintf(
					"expensive under-book sell=%d mkt=%d → buy freeze nac %d→%d",
					newPrice, p10, newNac, fr,
				))
				newNac = fr
				decReason = "under_book_buy_freeze"
			}
		}
	} else if newPrice > 0 {
		if book2ExpensiveSKU(item, newPrice) {
			// Меги без 30m-книги: раньше buy-cap 85% от sticky sell → яд3/мега копились вслепую.
			fr := book2FreezeBuyNac(newPrice)
			notes = append(notes, fmt.Sprintf(
				"expensive no_book → buy freeze nac %d→%d (buyMax=0)", newNac, fr,
			))
			newNac = fr
			decReason = "no_book_buy_freeze"
		} else {
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
