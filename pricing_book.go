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
// Global margin: среди свободных слотов категории предпочитаем сделки с
// большей абсолютной щелью. НО: пока free>0 — никогда не баним SKU
// (fill-floor = K-й дешёвый этого предмета). Ban без гарантии выкупа
// «жирных» лотов оставляет АХ пустым. При free=0 — селективность
// (победители / иначе best-1 этого SKU).

const (
	ahBook2Window  = 30 * time.Minute
	ahBook2MinLots = 40
	book2MaxSteps  = 2    // обычный snap ±2 step
	book2DeepRatio = 0.25 // |target−price|/price ≥25% → без cap (cold)
)

type bookCatMult struct {
	Sell float64
	Buy  float64
}

// bookProfitMultByType — sell mults; buyMax теперь из book2OptBuyMax (книга), не фикс 0.85.
// Buy в таблице — fallback если книга тонкая / opt не сработал.
var bookProfitMultByType = map[string]bookCatMult{
	"netherite_sword-1.21":   {Sell: 1.00, Buy: 0.85},
	"netherite_armor-1.21":   {Sell: 1.05, Buy: 1.00},
	"netherite_pickaxe-1.21": {Sell: 1.00, Buy: 0.95},
	"позорная-броня-1.21":    {Sell: 1.20, Buy: 1.00},
}

var bookProfitMultDefault = bookCatMult{Sell: 1.00, Buy: 0.85}

// book2OptBuyMax — max-margin buy из живой книги при лимите K слотов (один SKU).
// Для категории сообща слотов см. book2AllocateByMargin.
func book2OptBuyMax(sortedUnique []int, sell, softMin, capK int) (buyMax int, q float64, nElig int, ok bool) {
	if sell <= 0 || len(sortedUnique) == 0 {
		return 0, 0, 0, false
	}
	if softMin < 0 {
		softMin = 0
	}
	if capK < 1 {
		capK = 1
	}
	elig := make([]int, 0, len(sortedUnique))
	for _, p := range sortedUnique {
		if p > 0 && sell-p >= softMin {
			elig = append(elig, p)
		}
	}
	nElig = len(elig)
	if nElig == 0 {
		return 0, 0, 0, false
	}
	idx := capK - 1
	if idx >= nElig {
		idx = nElig - 1
	}
	buyMax = elig[idx]
	q = book2BookPercentile(sortedUnique, buyMax)
	return buyMax, q, nElig, true
}

// book2BookPercentile — доля лотов книги с price ≤ buyMax.
func book2BookPercentile(sorted []int, buyMax int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	n := 0
	for _, p := range sorted {
		if p <= buyMax {
			n++
		}
	}
	return float64(n) / float64(len(sorted))
}

// book2MarginCand — кандидат на слот: абсолютная щель sell−price.
type book2MarginCand struct {
	Item   string
	Price  int
	Margin int
	Sell   int
}

// book2AllocateByMargin — глобально: top-cap сделок по абсолютной марже между SKU.
// buyMax[item] = макс. цена среди взятых лотов этого id; нет в map / 0 → не покупаем.
// Так слот с щелью 500k бьёт слот с 200k, даже если это разные предметы.
func book2AllocateByMargin(cands []book2MarginCand, cap int) (buyMax map[string]int, slots map[string]int) {
	buyMax = make(map[string]int)
	slots = make(map[string]int)
	if cap < 1 || len(cands) == 0 {
		return buyMax, slots
	}
	sorted := append([]book2MarginCand(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Margin != sorted[j].Margin {
			return sorted[i].Margin > sorted[j].Margin
		}
		return sorted[i].Price < sorted[j].Price
	})
	if cap > len(sorted) {
		cap = len(sorted)
	}
	for _, c := range sorted[:cap] {
		slots[c.Item]++
		if prev, ok := buyMax[c.Item]; !ok || c.Price > prev {
			buyMax[c.Item] = c.Price
		}
	}
	return buyMax, slots
}

type book2GlobalAlloc struct {
	BuyMax map[string]int
	Slots  map[string]int
	Cap    int // на сколько сделок режем (обычно = free slots)
	FullCap int
	Free   int
	Taken  int
	At     time.Time
	Note   string
}

// book2GlobalCache — alloc по go_type (под mutex).
var book2GlobalCache = map[string]book2GlobalAlloc{}

const book2GlobalCacheTTL = 45 * time.Second

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

// book2EnsureGlobalAllocLocked — кэш; SQL вне mutex.
// Режем top по марже только на СВОБОДНЫЕ слоты (не на всю ёмкость) —
// иначе теория «20 жирных в книге» запрещает покупки, а слоты пустые.
func book2EnsureGlobalAllocLocked(goType string, since time.Time, now time.Time, ahCounts map[string]int) book2GlobalAlloc {
	fullCap, sumAH, free := book2CategoryOccupancyLocked(goType, ahCounts)
	allocCap := free
	if allocCap < 1 {
		allocCap = 1 // АХ битый — одна ротация самой жирной сделки
	}
	if prev, ok := book2GlobalCache[goType]; ok && now.Sub(prev.At) < book2GlobalCacheTTL && prev.BuyMax != nil && prev.Free == free {
		return prev
	}
	type snap struct {
		id      string
		softMin int
		mult    bookCatMult
	}
	var items []snap
	for id, cfg := range itemsConfig {
		if cfg.Type != goType {
			continue
		}
		soft := cfg.NacenkaMin
		if soft <= 0 {
			soft = nacenkaBaseLocked(id, cfg)
		}
		if soft <= 0 {
			soft = 200_000
		}
		items = append(items, snap{id: id, softMin: soft, mult: bookMultForType(goType)})
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
			if m >= it.softMin {
				cands = append(cands, book2MarginCand{Item: it.id, Price: price, Margin: m, Sell: sell})
			}
		}
	}
	mutex.Lock()

	buyMax, slots := book2AllocateByMargin(cands, allocCap)
	taken := 0
	for _, s := range slots {
		taken += s
	}
	type kv struct {
		id string
		n  int
	}
	var top []kv
	for id, n := range slots {
		top = append(top, kv{id, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
	noteParts := make([]string, 0, 4)
	for i, t := range top {
		if i >= 4 {
			break
		}
		noteParts = append(noteParts, fmt.Sprintf("%s×%d", t.id, t.n))
	}
	out := book2GlobalAlloc{
		BuyMax:  buyMax,
		Slots:   slots,
		Cap:     allocCap,
		FullCap: fullCap,
		Free:    free,
		Taken:   taken,
		At:      now,
		Note: fmt.Sprintf(
			"globalMarg free=%d/%d sumAH=%d alloc=%d taken=%d cands=%d [%s]",
			free, fullCap, sumAH, allocCap, taken, len(cands), strings.Join(noteParts, " "),
		),
	}
	book2GlobalCache[goType] = out
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
	fillK := share
	if fillK < 1 {
		fillK = 1
	}
	if free > 0 && free < fillK {
		fillK = free
	}

	bookSince := now.Add(-ahBook2Window)
	gAlloc := book2EnsureGlobalAllocLocked(cfg.Type, bookSince, now, ahCounts)

	mutex.Unlock()
	p10, p10N, p10OK := ahBookP10Since(item, bookSince)
	if p10N < ahBook2MinLots || p10 <= 0 {
		p10OK = false
	}
	uniq, _ := ahBookUniquePricesSince(item, bookSince)
	mutex.Lock()

	action := "book_hold"
	decReason := "no_book"
	newPrice := price
	newNac := nacenka
	buySrc := ""
	buyEff := mult.Buy
	gSlots := gAlloc.Slots[item]
	notes := []string{
		fmt.Sprintf(
			"book2 globalMarg 30m p10 sell×%.2f p10=%d n=%d ok=%v type=%s onAH=%d sales=%d buys=%d softMin=%d fillK=%d | %s | thisSlots=%d",
			mult.Sell, p10, p10N, p10OK, cfg.Type, onAH, sales, buys, softMin, fillK, gAlloc.Note, gSlots,
		),
	}

	if p10OK {
		rawSell := int(float64(p10)*mult.Sell + 0.5)
		sellT := bookSnapWithMarker(rawSell, step, priceBefore)
		sellT = bookSnapWithMarker(book2ClampStep(priceBefore, sellT, step), step, priceBefore)

		// fill-floor: per-SKU K-й дешёвый — гарантия, что можем занять слот,
		// даже если «жирные» лоты других SKU из книги так и не купятся.
		fillBuy, _, _, fillOK := book2OptBuyMax(uniq, sellT, softMin, fillK)
		bestBuy, _, _, bestOK := book2OptBuyMax(uniq, sellT, softMin, 1) // лучшая сделка этого SKU
		gBuy, hasG := gAlloc.BuyMax[item]

		optBuy := 0
		mode := ""
		if gAlloc.Free > 0 {
			// Пустые слоты → НИКОГДА не баним SKU. Берём max(global, fill).
			if hasG && gBuy > 0 {
				optBuy = gBuy
				mode = "prefer+fill"
			}
			if fillOK && fillBuy > optBuy {
				optBuy = fillBuy
				if mode == "" {
					mode = "fill"
				} else {
					mode = "prefer+fill"
				}
			}
			if optBuy <= 0 && fillOK {
				optBuy = fillBuy
				mode = "fill"
			}
		} else {
			// АХ битый → селективность: победители global, иначе только лучший лот SKU (не ban).
			if hasG && gBuy > 0 {
				optBuy = gBuy
				mode = "full-prefer"
			} else if bestOK {
				optBuy = bestBuy
				mode = "full-best1"
			}
		}

		nacT := softMin
		if optBuy > 0 && optBuy < sellT {
			buyMax := bookSnapWithMarker(optBuy, step, priceBefore)
			if buyMax >= sellT && step > 0 {
				buyMax = sellT - step
			}
			if buyMax < 0 {
				buyMax = 0
			}
			nacT = sellT - buyMax
			if nacT < softMin {
				nacT = softMin
			}
			buyEff = float64(buyMax) / float64(p10)
			buySrc = fmt.Sprintf("%s buyMax=%d (%.3f×p10) gSlots=%d free=%d", mode, buyMax, buyEff, gSlots, gAlloc.Free)
			notes = append(notes, buySrc)
		} else {
			_, nacFb, src := bookTargetsFromLiveBookBuy(p10, step, priceBefore, priceFloor, softMin, cfg.Type, mult.Buy)
			nacT = nacFb
			buyEff = mult.Buy
			buySrc = src + " fallback"
			notes = append(notes, "opt empty → fallback buy×"+fmt.Sprintf("%.2f", mult.Buy))
		}
		if nacT < softMin {
			nacT = softMin
		}
		if nacT < 0 {
			nacT = 0
		}

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
	if p10OK && p10 > 0 && newPrice > 0 && buyEff > 0 {
		rawBuy := int(float64(p10)*buyEff + 0.5)
		buyMax := bookSnapWithMarker(rawBuy, step, newPrice)
		if buyMax <= 0 {
			buyMax = rawBuy
		}
		if buyMax >= newPrice && step > 0 {
			buyMax = newPrice - step
		}
		if buyMax < 0 {
			buyMax = 0
		}
		wantNac := newPrice - buyMax
		if wantNac < nacMin {
			wantNac = nacMin
		}
		if wantNac < 0 {
			wantNac = 0
		}
		if wantNac != newNac {
			notes = append(notes, fmt.Sprintf("buy-cap → nac %d→%d (buyMax=%d)", newNac, wantNac, newPrice-wantNac))
			newNac = wantNac
			if !strings.Contains(action, "price_") && action != "book_nacenka_set" {
				decReason = "buy_cap_p10"
			}
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
		if wantNac < nacMin {
			wantNac = nacMin
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
