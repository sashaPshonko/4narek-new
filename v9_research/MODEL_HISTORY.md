# v9 pricing — история моделей (восстановлено из Git + PRICING_EXPERIMENTS + capital_cycles)

Источник: `PRICING_EXPERIMENTS.md`, комментарии `pricing.go`, `capitalPolicy` в БД.

## Эпохи (live policy string → окно в БД)

| Policy | Окно (UTC) | Циклов | Σ profit | p/h (М) |
|--------|------------|--------|----------|---------|
| classic_* | до ~15.07 | — | — | — |
| v1 | 15–18.07 | 5929 | 6.9B | ~100 |
| v2 | 18–19.07 | 2016 | 1.9B | — |
| v3 | 19–21.07 | 5235 | 10.3B | **190** |
| **v4** | 21–23.07 | 4318 | 8.9B | **199** |
| v5 | 23–24.07 | 2143 | 4.2B | — |
| **v6** | 24–28.07 | 6551 | 17.8B | **177** |
| v7 | 28–29.07 | 3223 | 2.2B | — |
| v8 | 29.07–17.08 | 28515 | 21.4B | 47 |
| v8b | 17.08–01.09 | 27073 | 24.2B | 69 |
| v8d…v8m | Sep early | short | — | шум |
| v8n | 06–09.09 | 4005 | 0.5B | 7.6 |
| v8x | 10–13.09 | 2355 | 0.7B | 10 |
| v8ae | 13–14.09 | 445 | 17M | 0.9 |
| v8af | 14.09+ | 70 | 7M | 2.9 |

p/h из observational compare на полном `capital_cycles` (14.09.2026).

## Экономическая политика по поколениям

### Classic (до коридора)
- **UP** если `sales < NormalSales` и сток/АХ не раздуты
- **DOWN** слабо связан с inventory
- Факт: UP ~44%, fwd после UP ≈ 0 / минус; HOLD/DOWN лучше
- Вывод: NormalSales ≠ цена; путали «мало продаж» с «надо ↑»

### Corridor v1–v3 — inventory core
- Цель: `held/share` в полосе ~15–25% → 18–25%
- **DOWN** при `held > hi` (soft/over/dump)
- **UP** при `held < lo` + продажи
- v2+: try-veto, dead hold
- v3+: buy-veto (`buys≥sales`), up_streak=1

### v4–v6 — лучший live p/h
- weak_demand (sales≥3 / night≥4)
- up_cooldown
- v5: **запрет ↑ при held=0**
- v6: deep-↑, hard↓×2, per-type полоса (позор)

### v7–v8 — recover / skim
- recover-↑ с пола; skim в полосе
- Исторически: fill тоньше, p/h ниже чем v4/v6

### v8k–v8af — AH book / empty / DOI
- Книга: p10, soft-↓, ban-filter min
- empty_idle, floor_escape, DOI cover, grant, cold_start, catchup
- Live late Sep: fill часто <5–15%, p/h рухнул
- Полезные идеи (для v9 проверки): **не ↓ с empty**, **не ↑ вслепую с empty**, **gap к рынку**

## Инварианты, которые v9 сохраняет
- Наценка фиксирована
- Floor = minBuy + nacenka
- Manual min/max clamp
- Share = слоты типа
- Цикл ≈ AnalysisTime

## Что НЕ тащить в v9 без доказательств
- NormalSales → UP (classic)
- Blind empty → UP
- Book → UP как основной драйвер
- Сложный DOI без market guard

## Эффективность решений (OOS sim) — журнал

Правило: при каждой смене pricing — `full_compare_all.py`, % vs все baseline, строка сюда.

### 2026-09-14 — `stock_corridor_v9` vs все
Источник: `full_compare.json` (3-fold WF, FOCUS). hold — артефакт, не baseline победы.

| vs | Δ mean profit | Δ p/h |
|----|--------------:|------:|
| corridor_v5/v6 | **+5.1%** | +4.8% |
| corridor_v4 / v8_late | +5.4% | +5.2% |
| corridor_v3 | +6.2% | +6.2% |
| **ekb** | **+13.0%** | +12.9% |
| classic_ns | +15.8% | +16.2% |
| corridor_v1 | +17.9% | +18.6% |
| mean of 7 actives | ~+10.3% | ~+10.4% |

Rank (active): v9 > v5/v6 > v4/v8_late > v3 > ekb > classic_ns > v1.

### 2026-09-14 — инструменты цены (observational fwd3 vs HOLD)

HOLD mean fwd3 ≈ **2.61M**. Источник: `instrument_efficacy.json`.

| Инструмент | n | Δ vs HOLD | Вердикт |
|------------|--:|----------:|---------|
| DOWN dump / over / soft-hi | 1k–3.7k | **+5.7…+8.8M** | **эффективен** |
| UP skim | 2242 | +1.4M | слабый+ |
| UP demand | 7235 | +0.5M | слабый+ |
| UP deep | 663 | ~0 | нейтрал |
| UP recover / recover_deep | 4719 | **−2.6…−3.3M** | **вреден** |
| UP empty_idle | 942 | −2.9M | вреден |
| UP ah_book | 36 | −9.4M | вреден (мало n) |
| UP floor / floor_escape | 116 | −1.7M | вреден |
| DOWN empty_fair / stale | 50–189 | −2.3…−2.6M | вреден |
| UP @ held=0 / 0/0/0 | ~4k | −2.3…−2.9M | вреден |
| UP @ held>0 ∧ sales≥3 | 8096 | +1.3M | эффективен |
| DOWN @ held=0 | 242 | −2.6M | бесполезен/вреден |

### 2026-09-14 — v9 catchup `gap` sweep
`gap_compare.py`: WF 3-fold + stress empty 0.5→3M.

| gap | mean OOS profit | vs 0.80 | empty final | under |
|----:|----------------:|--------:|------------:|------:|
| 0.75 | 12505M | −0.00% | 77% | 0.229 |
| **0.80** | 12506M | 0 | 80% | 0.229 |
| **0.85** | **12511M** | **+0.04%** | 87% | 0.222 |
| 0.90 | 12497M | −0.07% | 90% | 0.221 |
| 0.95 | 12491M | −0.12% | 97% | 0.220 |
| 1.00 | 12491M | −0.12% | **100%** | 0.220 |

OOS почти плоский. Чистый max profit → **0.85**. Против buy-trap (empty@80%) → **0.95 или 1.0** (−0.12% OOS). Рекомендация: **gap=0.95** (почти рынок, крошечный OOS cost).

### 2026-09-14 — empty↑ «пока нет покупательной способности»
`until_buy_compare.py` + fixed stress.

| режим | OOS vs 0.80 | empty under → | can_buy? | OVER empty |
|-------|------------:|---------------|----------|------------|
| gap080 | 0 | 80% | **нет** | HOLD |
| **until_buy** (рост пока sell&lt;mkt) | **−0.05%** | **100%** | **да** | HOLD |
| mkt+nac | +1.2%* | 107% | да | HOLD (уже выше) |
| no_cap (без лимита) | +0.8%* | 113%+ и дальше | да | **↑ бесконечно** |

\*sim over↑ — не доказательство лучше. **no_cap токсичен** на переоцен+empty. Оптимум логики закупа: **until_buy = gap 1.0**.

### Решение (Sasha 14.09): оптимальное условие empty catchup
**Расти по шагам, пока `sell < p10` (и `sell+step ≤ p10`), thick p10.**  
Эквивалент: `v9CatchupGapRatio = 1.0` — лимит «пока нет покупательной способности», не стоп на 0.80.  
Не делать: early stop 0.80; no_cap без рынка; цель p10+nacenka.

### 2026-09-14 — ideal check denser sweep (`gap_ideal_check.py`)
OOS max profit: **0.85** (+0.17% vs 1.0) — без can_buy.  
can_buy на empty under: **0.98 / 1.0 / 1.02 / 1.05** (выше 1.0 ≈ 1.0 из‑за `price+step≤p10`).  
**Идеал по логике закупа + OOS≈плоско: gap=1.0** (0.98 эквивалентен при step 100к).

### 2026-09-14 — buy-stop + p10 safety (`buy_stop_safety.py`) → **внедрено в код**
Стоп по покупкам (empty streak сбрасывается при buys>0) + safety p10.
| safety | OOS | broken (нет buys) | buys@90% |
|--------|----:|------------------:|---------:|
| p10 | 12515M | **100%** стоп | 90% стоп |
| none | +1.5% sim | уезжает 113%+ | 90% |
| p10+nac | +1% / over↑ | 107% | 90% |

**p10 — норм как предохранитель.** В коде: `v9CatchupGapRatio = 1.0`.
