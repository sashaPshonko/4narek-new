package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// book2 — max-profit якорь от живой книги.
// Окно 30м (не 10м цикл): иначе p10 скачет на тонком хвосте
// (фарм 10:06 p10_10=1.5 n=41 vs p10_30=1.2 → sell +40%).
// Эмпирика FIFO×hourly p10 (с 2026-09-03): buy-gate max Σ(sell−buy).
//
//	sword  → buy≤0.85×p10
//	armor  → buy≤1.00×p10 (на практике sell чуть выше книги)
//	pick   → buy≤0.95×p10
//
//	sell   = sellMult(cat) × p10
//	buyMax = buyMult(cat) × p10
//	nacenka = sell − buyMax
//
// Thin book → HOLD. За цикл не больше book2MaxSteps×step (кроме deep catch-up).

const (
	ahBook2Window   = 30 * time.Minute
	ahBook2MinLots  = 40
	book2MaxSteps   = 2               // обычный snap ±2 step
	book2DeepRatio  = 0.25            // |target−price|/price ≥25% → без cap (cold)
)

type bookCatMult struct {
	Sell float64
	Buy  float64
}

// bookProfitMultByType — базовые mults; buyEff крутит book2Adapt от загрузки АХ.
var bookProfitMultByType = map[string]bookCatMult{
	"netherite_sword-1.21":   {Sell: 1.00, Buy: 0.85},
	"netherite_armor-1.21":   {Sell: 1.05, Buy: 1.00}, // BEST buy-gate 1.00; sell>buy
	"netherite_pickaxe-1.21": {Sell: 1.00, Buy: 0.95},
	"позорная-броня-1.21":    {Sell: 1.20, Buy: 1.00},
}

var bookProfitMultDefault = bookCatMult{Sell: 1.00, Buy: 0.85}

// book2 adapt: AH full → уже buy (селективность); пусто/голод покупок → шире.
// Не оба рычага в край (0.80+400k умерло). EMA по go_type — без дёрганья каждый цикл.
const (
	book2AdaptPivot   = 0.70 // load ниже → loosen, выше → tighten
	book2AdaptGain    = 0.22 // Δbuy ≈ gain × (load − pivot)
	book2AdaptBuyMin  = 0.78 // жёстче FAIL-зоны не лезем одним buy
	book2AdaptBuyMax  = 0.92
	book2AdaptEMAAlpha = 0.35
)

// book2LoadEMA — сглаженная загрузка АХ по go_type (только под mutex).
var book2LoadEMA = map[string]float64{}

type book2AdaptIn struct {
	BaseBuy float64
	BaseNac int // из JSON, не runtime
	SoftMin int // абсолютный пол (NacenkaMin или 2/3 base)
	Load    float64
	NoRoom  bool // этому id некуда выставить
	Starve  bool // need>0 и есть место под докупку
}

type book2AdaptOut struct {
	BuyEff   float64
	NacFloor int
	Note     string
}

// book2AdaptBuy — чистая функция: load/signals → buyEff + nac floor.
func book2AdaptBuy(in book2AdaptIn) book2AdaptOut {
	base := in.BaseBuy
	if base <= 0 {
		base = 0.85
	}
	load := in.Load
	if load < 0 {
		load = 0
	}
	buy := base - book2AdaptGain*(load-book2AdaptPivot)
	if in.NoRoom {
		buy -= 0.03
	}
	if in.Starve {
		buy += 0.04
	}
	if buy < book2AdaptBuyMin {
		buy = book2AdaptBuyMin
	}
	if buy > book2AdaptBuyMax {
		buy = book2AdaptBuyMax
	}

	floor := in.BaseNac
	if floor <= 0 {
		floor = 300_000
	}
	soft := in.SoftMin
	if soft <= 0 {
		soft = floor * 2 / 3
		if soft < 200_000 {
			soft = 200_000
		}
	}
	// При низкой загрузке можно опустить пол к soft (набрать слоты).
	// При высокой — пол = base (не разжижаем щель ниже конфига).
	nacFloor := floor
	if load < 0.45 || in.Starve {
		nacFloor = soft
	}
	if nacFloor < soft {
		nacFloor = soft
	}

	return book2AdaptOut{
		BuyEff:   buy,
		NacFloor: nacFloor,
		Note: fmt.Sprintf(
			"adapt load=%.2f buyEff=%.3f (base=%.2f) nacFloor=%d noRoom=%v starve=%v",
			load, buy, base, nacFloor, in.NoRoom, in.Starve,
		),
	}
}

// book2AhLoadLocked — sumAH/capacity по типу + EMA. Только под mutex.
func book2AhLoadLocked(goType string, ahCounts map[string]int) float64 {
	cap := categoryAhCapacityLocked(goType)
	if cap <= 0 {
		return 0
	}
	sum := 0
	for name, c := range ahCounts {
		if c <= 0 {
			continue
		}
		other, ok := itemsConfig[name]
		if !ok || other.Type != goType {
			continue
		}
		sum += c
	}
	raw := float64(sum) / float64(cap)
	prev, ok := book2LoadEMA[goType]
	if !ok {
		book2LoadEMA[goType] = raw
		return raw
	}
	ema := (1-book2AdaptEMAAlpha)*prev + book2AdaptEMAAlpha*raw
	book2LoadEMA[goType] = ema
	return ema
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
	// Адаптив: базовый nac из JSON (itemsNacenkaBase), не runtime ratchet.
	// Полный АХ → buyEff↓; пусто/starve → buyEff↑ и мягкий пол.
	baseNac := nacenkaBaseLocked(item, cfg)
	softMin := cfg.NacenkaMin
	if softMin <= 0 {
		softMin = baseNac * 2 / 3
		if softMin < 200_000 {
			softMin = 200_000
		}
		if softMin > baseNac {
			softMin = baseNac
		}
	}
	load := book2AhLoadLocked(cfg.Type, ahCounts)
	noRoom := false
	if isTypeRelistEnabled(cfg.Type) {
		maxR := maxReachableStockOnAHLocked(item, cfg, onAH, ahCounts)
		noRoom = maxR <= onAH && onAH > 0
	}
	starve := need > 0 && underbuyOK
	mult := bookMultForType(cfg.Type)
	adapt := book2AdaptBuy(book2AdaptIn{
		BaseBuy: mult.Buy,
		BaseNac: baseNac,
		SoftMin: softMin,
		Load:    load,
		NoRoom:  noRoom,
		Starve:  starve,
	})
	buyEff := adapt.BuyEff
	nacMin := adapt.NacFloor

	bookSince := now.Add(-ahBook2Window)
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
	notes := []string{
		fmt.Sprintf(
			"book2 adapt 30m p10 sell×%.2f buyEff×%.3f(base×%.2f) p10=%d n=%d ok=%v type=%s onAH=%d sales=%d buys=%d price=%d nac=%d floor=%d | %s",
			mult.Sell, buyEff, mult.Buy, p10, p10N, p10OK, cfg.Type, onAH, sales, buys, price, nacenka, priceFloor, adapt.Note,
		),
	}

	if p10OK {
		sellT, nacT, src := bookTargetsFromLiveBookBuy(p10, step, priceBefore, priceFloor, nacMin, cfg.Type, buyEff)
		rawSell := sellT
		sellT = bookSnapWithMarker(book2ClampStep(priceBefore, sellT, step), step, priceBefore)
		if sellT != rawSell {
			notes = append(notes, fmt.Sprintf("step-cap %d→%d (max ±%d×step)", rawSell, sellT, book2MaxSteps))
			rawBuy := int(float64(p10)*buyEff + 0.5)
			buyMax := bookSnapWithMarker(rawBuy, step, priceBefore)
			if buyMax >= sellT && step > 0 {
				buyMax = sellT - step
			}
			if buyMax < 0 {
				buyMax = 0
			}
			nacT = sellT - buyMax
			if nacT < nacMin {
				nacT = nacMin
			}
			if nacT < 0 {
				nacT = 0
			}
		}
		buySrc = src
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

	// buyMax всегда ≤ buyEff×p10 (если книга есть) и < sell.
	if p10OK && p10 > 0 && newPrice > 0 {
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
			notes = append(notes, fmt.Sprintf("buy-cap p10×%.3f → nac %d→%d (buyMax=%d)", buyEff, newNac, wantNac, newPrice-wantNac))
			newNac = wantNac
			if !strings.Contains(action, "price_") && action != "book_nacenka_set" {
				decReason = "buy_cap_p10"
			}
		}
	} else if newPrice > 0 {
		// нет p10 — не покупаем у 85%+ от sell (stale mega/яд3)
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
