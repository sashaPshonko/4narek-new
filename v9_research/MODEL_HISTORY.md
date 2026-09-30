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

### 2026-09-30 — live gate: treasury_empty + held>0 → BlockUp
Сигнал орка `treasury_empty_types` уже глушил empty_idle recovery при held=0.
Теперь при **held>0** и казне пустой у ботов типа — `adjustPriceV9` ставит `BlockUp` (ложный недобор из‑за денег ≠ повод ↑).
OOS sim не моделирует treasury → compare без Δ; policy string без смены (`stock_corridor_v9`).

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

### 2026-09-14 — prod rollback → `stock_corridor_v4` (эпоха v4–v6)
**Решение:** снять v9/late-v8 с боя; вернуть чистый inventory corridor (live peak ~199M/h Jul).  
Код: `pricing_v4.go`, `capitalPolicy = capitalPolicyV4`. Логика ≈ sim `corridor_v5`/`v6` (weak_demand≥3/ночь≥4, empty↑ запрет, hard↓×2, без book/catchup/DOI).

Источник цифр: `full_compare.log` (уже прогнанный WF). Prod ≈ corridor_v5 mean **11929.5M**.

| vs | Δ mean profit | Δ p/h | зачем смотрим |
|----|--------------:|------:|---|
| **v9** | **−4.8%** | −4.6% | sim хуже v9; live late Sep v9/v8 мёртвые — откат осознанный |
| corridor_v4 (sim) | +0.3% | +0.4% | почти то же |
| **ekb** | **+7.6%** | +7.8% | EKB живой, но OOS слабее peak corridor |
| classic_ns | +10.2% | +10.9% | |
| corridor_v1 | +12.2% | +13.2% | |
| hold | −5.0% | −4.1% | артефакт, не цель |

**Не EKB:** отдельный бинарь, empty↑/NormalSales-риск на over+empty; OOS −7.6% к peak corridor; live p/h Jul уступал v4.  
**Не V10:** robust LOW confidence.

### 2026-09-14 — prod → `classic_2026_02_22` (NormalSales=5)
**Решение Sasha:** вернуть алгоритм до `*-1.21` / до `enoughItems` — Exact `b96739c5` (22.02).  
Код: `pricing_classic.go`, `capitalPolicy = capitalPolicyClassic`. Во всех SKU `items_config.json`: **`normal_sales = 5`**.

Источник: `full_compare.log` — sim `classic_ns` mean **10828.4M**.

| vs | Δ mean profit | Δ p/h |
|----|--------------:|------:|
| v9 | **−13.6%** | −13.9% |
| corridor_v5/v6 | **−9.2%** | −9.8% |
| corridor_v4 | −9.0% | −9.5% |
| **ekb** | **−2.4%** | −2.8% |
| corridor_v1 | +1.8% | +2.1% |
| hold | −13.8% | −13.5% |

Осознанный откат на запрос (live curiosity / старый режим), не OOS-оптимум.

### 2026-09-14 — classic: пол = max(minBuy+nac, min buy 10м)
`classicEffectiveFloor`: sell не ниже самой дешёвой покупки за 10 минут (`priceHistory`). Без покупок в окне — прежний nac-floor.

### 2026-09-14 — prod → `stock_corridor_v9` (снова)
После classic-лесенки на тонких мечах (−27M/h). План: полный флот (1 sword / 1 pick / 3 armor shared). OOS: v9 +5% к v4/v5; live thin-fleet v4 был выше — принимаем ради market guards на толстом флоте.

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

### 2026-09-15 — SUPER search (AH-p10 sim + under constraint)
Скрипт: `search_super.py`. Пул 1275, Sep+ book era, WF 2 folds, haircut under.

**Не внедрять из raw leaderboard:** `r513` fold1 ×2.19 (book_ok 0.43) → late half **0.94× v9**.

**Артефакт:** `ekb_like` (даже с `on_ah=held`) при `inv=0` никогда не ↓ → UP-only. Исключён из пула/ранга.

**Robust gate:** multi-fold min≥1.0× **и** late-half ≥1.02×. Corridor multi-winners (`r91`/`r466`) на late **0.89–0.91×** — overfit.

**Вердикт:** на текущем SUPER-sim **нет** устойчивого победителя над v9 → prod не трогаем. Дальше: лучше demand/inventory counterfactual, не «ещё random».

### 2026-09-16 — fidelity sim + hyp eval
Код: `sim_fidelity.py` (AH p10+depth demand, pure CF при сдвиге цены, sales≤held, on_ah/inv split) + `hyp_eval.py` (H1–H5 vs v9).  
Robust gate: all folds ∧ min≥1.02 ∧ late≥1.02. Результаты → `hyp_eval_results.json`.

**Сигнал (dominates, не strict-robust):** `H1_under_veto_095` — fold0 **+12%**, fold1 0%, late **+16%**, under 0.76→0.63.  
**Внедрено в prod код 2026-09-16:** `v9DownBlockRatio = 0.95` (рестарт Go с панели).  
Параллельно: `search_free.py` — свободный поиск (nac×, multi-step ×1..5).

### 2026-09-16 — FREE SUPER (после H1)
Пул 998 на fidelity sim. Raw «robust» лидеры все с **nac×1.5** (+profit от маржи) — не внедрять.

**Policy-only (nac принудительно 1.0), late half:**  
`market_follow` + `mf_pull≈1` + multi-step up ×2–5 → **~+45%** vs v9_h1, under≈0.01–0.02.  
Кандидат следующего шага (не в prod): тянуть цену к p10 крупными шагами, не крутить nac.

### 2026-09-17 — OPEN hyp (nac + non-step jumps allowed)
Код: `hyp_eval_open.py`. Baseline `v9_h1`. Священные коровы сняты: nac× и прыжки к рынку (не только ±price_step) в пуле.  
Метрики: `late_x` (raw) и `margin_adj_x = late/(nac_mult)/v9` (насколько не только маржа).

**nac=1 (чистый алгоритм):** `market_follow` `mf_pull=1` → late **×1.34**, under 0.156→**0.011**, dominates folds (min≈1.01; strict robust 1.02 — нет).  
Размер up_mult 1…5 почти не важен — важен **прыжок к p10**, не множитель step.

**nac×1.5 + MF:** late **×2.21**, margin_adj **×1.47** — и маржа, и алгоритм (не чистый артефакт ×1.5).  
**Только step_mult / catchup multi-step:** ~0…+5%, under почти как v9 — слабо.

**Вердикт research:** следующий кандидат в Go — **market follow к AH p10** (gap-jump). Nac↑ — отдельное продуктовое решение (sim говорит да, но меняет экономику лота).  
До prod: `full_compare_all` на MF chrom + явное «залей».



### 2026-09-26 — v9 AH-relist share (buy+перевыстав, без набивки инвентаря)

**Контекст:** боты только покупают и бесконечно перевыставляют (5 слотов АХ/бот). Старый share `(32×bots)/nItems` давал chronic understock → demand/catchup ↑ раздувал sell и buy-потолок.

**Prod patch (`pricing.go` / `pricing_v9.go`):**
1. Relist-типы: share = `(5×bots)/nItems` (`ahStorageSlotsPerBot`)
2. Коридор `held` = **onAH** (инв не считается стоком)
3. ↑ veto если категория АХ полна или `maxReachable≤onAH`

**Боты (`4narek-old`):** enoughItems после перевыстава **не** сбрасывается; off только при свободном слоте 0–4 или «У Вас купили». Shift-ротация выкл.

**Compare:** полный OOS на старых capital_cycles (held=inv+AH, share×32) **не** apples-to-apples с AH-only. Unit: `TestV9BlockUpStopsDemand` + existing v9 suite PASS. Live A/B после рестарта Go с панели.

### 2026-09-26 — relist5 (норма sales=5, без коридора)

Для relist-типов вместо v9-corridor:
- `weak = sales < 5` за analysis window
- `onAH≥1 && weak` → ↓1 (veto если our/p10 < 0.95)
- `onAH==0 && weak && our/p10 < 0.90` → ↑1 ≤ p10
- АХ полон / нет слота → ↑ off
Policy tag в capital_cycles: `…+relist5`. Цель — собрать логи и подкрутить норму/пороги.

Update: fair = max(1, (5×bots)/nItems); ↓ if onAH≥fair, ↑ if onAH<fair (not bare onAH≥1).

Update: ↑ blocked when fair > maxReachable (other ids hold AH slots).

Update: UP without p10 ratio gate / p10 cap; still block ↑ if fair unreachable or AH full.

### 2026-09-26 — relist5: buy-surge ↓ off

На каждый buy больше не режем цену (старый коридор soft/hi). Relist типы — только цикл fair+sales<5.

### 2026-09-27 — relist5: ↑ book cap (p10)

Пустой onAH по-прежнему может ↑. Живая книга: не выше p10 (hold / clip).

### 2026-09-27 — book1 (relist types): sell+nacenka from AH p10

Empirics: buy≤0.80×p10, sell≈1.00×p10 → nacenka≈0.20×p10. Thick book snap; thin → hold.
Full WF OOS N/A (book-driven, not inventory corridor). Rollback: wire adjustPriceRelist.

### 2026-09-27 — book2 (relist): hist p90 buys + profit clamp

Цель: max total profit, не «магические 20%».

Данные (FIFO×hourly p10, мечи, с 03.09): buy-gate scan — пик net profit при buy≤0.85–0.90×p10
(0.80→926M, 0.85→962M, 0.90→981M; выше 0.95 bad_loss растёт). Win buy/p10 p90≈0.83, p95≈0.90;
sell/p10 p75≈1.00. Статичная nac ~0.22×p10 резала вход — смотрим buys с price < ref−nac.

Политика: sell=1.00×p10; buyMax=p90(покупок ниже gate, 7д), clamp [0.75, 0.88]×p10;
fallback 0.85×p10. nac=sell−buyMax. «95 buy / 90 sell» сырой — sell p90≈1.09 передерживает;
sell якорим к книге. Full WF OOS N/A. Rollback: bookBuyFallbackMult=0.80 или adjustPriceRelist.

### 2026-09-27 — book2 live-only (no 7d hist)

История покупок убрана: вайп / x2 за час / старт после недели простоя требуют только
актуальную книгу (~10м). sell=1.00×p10; buyMax=live book p5, clamp [0.75, 0.88]×p10;
fallback 0.85×p10. Первый толстый скан сразу переписывает устаревшие цены. Thin book → HOLD.

### 2026-09-27 — book2 max-profit mults (per category)

Убраны p5-clamp и «универсальные 0.75–0.88» (не argmax прибыли).

Live p10 × category mults по Σ(sell−buy) FIFO (с 03.09):
| type | sell×p10 | buy×p10 | BEST_NET buy-gate |
| sword | 1.00 | 0.90 | 0.90 |
| armor | 1.05 | 1.00 | 1.00 |
| pick | 1.00 | 0.95 | 0.95 |
| позорная | 1.20 | 1.00 | 1.00 |
| default | 1.00 | 0.90 | — |

Формула универсальна; множители — по категории. OOS corridor WF N/A.

### 2026-09-29 — book2 sword buy 0.90→0.85 (unit margin)

BA: конфиг nac=300k «как раньше» на sword7 (sell~1.1M) режет keep до ~21% вечерних
покупок → tot profit index **−60%**. Жёсткий пол 300k не берём.

Эмпирика gate (те же FIFO): 0.90→981M BEST_NET, 0.85→962M (−2% Σ). Факт вечера
unit ~+130–165k при 0.90; цель — щель ~15% sell (~165–200k на 1.1M) без обвала объёма.
Sword/default Buy **0.85**; armor/pick без изменений. Anti-overpay (buy-cap p10 / 85% no_book)
остаётся.

### 2026-09-29 — nac floor = config nacenka (5-slot BA)

Пересмотр: у бота **5 слотов AH** — bottleneck оборот слотов, не число AH-оферов.
Индекс «−60% tot» считал каждую покупку равноценной продаже; при полном АХ лишние
дешёвые входы всё равно не выставить. Селективность +300k (статичный `nacenka` из
`items_config`) заполняет слоты лучшей щелью. book2: `nacMin = max(NacenkaMin, Nacenka)`;
sell по-прежнему ~p10, buyMax = sell−nac (≥300k на мечах). Buy mult 0.85 остаётся
(на mega/высоких p10 щель может быть >300k).

### 2026-09-29 — experiment sharp5 / sharp6 SKUs

Отдельные id `sword-sharp5-1.21` / `sword-sharp6-1.21` (max_effects ровно 5/6).
Тот же `netherite_sword-1.21`, book2 mults без изменений. Протокол:
`v9_research/SHARP56_EXPERIMENT.md` + `sharp56_experiment.py`.

### 2026-09-29 — slot boundary: buy 0.80 + nac 400k + loot SKUs

5 слотов × много категорий → селективность. Sword buy **0.85→0.80**,
пол nac мечей **300→400k** (bare нет). SKU: sharp5/6+loot5, sharp7+loot4.
Книга: ~120 лотов/ч ≤0.80×p10 vs ~50 fills/h; край голода ~0.70.
Протокол: `v9_research/SLOT_BOUNDARY.md`.

### 2026-09-29 — rollback slot boundary (АХ опустел)

Факт ~20:20–20:37Z: buys **142/3h → 13** после cut; sword7 eff buy≈0.68 из‑за
пола 400k. Откат: buy **0.85**, nac пол **300k** (megasword остаётся 400k как было).
Loot/sharp SKUs остаются. Границу жать осторожнее — сначала только mult или только пол.

### 2026-09-29 — book2 sword opt 0.88 / nac 300k

Откат 0.85/300 был заплаткой. Page-sim тянул к 0.88/250 (+объём), но при **забитом АХ**
пол 250k только разжижает щель — слоты уже полные. Итог: buy **0.88**, nac пол
**300k** (mega 400k). FAIL 0.80/400 по-прежнему starve. Sell 1.00. Loot/sharp без изменений.

### 2026-09-29 — full-AH: plateau; probe nac 350k

Пересчёт: при CAP≈25 (полный АХ) cherry-pick Σ почти плоский (±0.3%) по всему
grid buy/nac. 0.88 не opt. Узкий 0.80/400 — starve. Реальный ход при полном АХ —
**выше nac, buy 0.85**: пол **350k** (один knob). Mega 400k. Дальше 380 только если
2h без пустых слотов.

### 2026-09-29 — book2Adapt: buyEff от AH load

Тык 300/350/400 отменён. `book2AdaptBuy(load, noRoom, starve)`:
buyEff = base − 0.22×(load−0.70), clamp [0.78, 0.92]; nac floor = JSON base
на полной загрузке, soft (⅔) при starve. EMA по go_type. `itemsNacenkaBase` не
ratchet'ится runtime nac. Sell по-прежнему ~p10.

### 2026-09-29 — book2OptBuyMax: перцентиль из книги

Вместо load-адапта: buyMax = K-й дешёвый unique-лот 30м с щелью ≥ softMin
(K≈share). Это argmax Σ(sell−buy) при cap K и фикс sell≈p10. q = доля книги ≤ buyMax.
Fallback buyMult если eligible=0.

### 2026-09-29 — book2 global margin across SKUs

Per-SKU K забивал слоты тонкой щелью. Теперь top (bots×5) сделок по абсолютной
марже между всеми SKU категории; SKU вне alloc → buyMax=0. Слот 500k > слот 200k.

### 2026-09-29 — global margin fill-safe

Ban без гарантии выкупа жирных → пустой АХ. Alloc только на free slots;
при free>0 buyMax=max(global, per-SKU fill); при free=0 — prefer / best-1, без buyMax=0.

### 2026-09-29 — book2 единый minMarg из книги

Вместо top-N alloc по SKU: `minMarg = K-я лучшая щель` (K=free, иначе 3).
`nac=max(softMin, minMarg)` на все предметы. Не баним SKU — только отсекаем
лоты тоньше порога.

### 2026-09-29 — minMarg K под набор 5 слотов

K = max(2×ёмкости bots×5, 2×free, 10). Запас ×2: иначе page не набирает
слоты. Тонкая книга → смягчение K.

### 2026-09-29 — book2 hybrid floors (per-SKU ↔ global)

Алгоритм сам выбирает:
- per-SKU: поднять пол по своей книге (fat → 500k+, volume → ~300k);
- global: поднять всем только если volume-ярус (p10\<1.5M) ещё кормит над порогом.

Без банов SKU. Жёсткий пол 300k.

### 2026-09-29 — book2: global не душит volume

Live: `global+800k` снова дал sword7/фарм buyMax≈0.40×p10 (как ночной 910k/0.23).
Фикс: global только на fat; volume/fat потолок nac (buy≥0.70 / ≥0.55 ×p10).

### 2026-09-29 — book2 sell якорь = p20 per-seller min

Lot/uuid p10 завышал рынок (клоны 1.5M+ при реальном крае продавцов ~0.75–0.9M).
Sell теперь от p20 минимальных цен продавцов (без ban). Volume ceiling 2.5M.

### 2026-09-29 — book2 sell = seller p10 + buy-ratio > softMin

p20/min12 не срабатывал (sword7 часто 11 sellers → no_book → залипание ~1.4M).
Якорь: **p10 seller-mins**, min **6** sellers. softMin 300k больше не поднимает nac
над потолком buy≥0.70×mkt — иначе при mkt~800k buy душили.

### 2026-09-29 — book2 sell p40 / buy ≥ seller p10

p10-as-sell (~700k) → buyMax~0.5M → 0 fillable sellers на AH.
Sell = **p40** seller-mins; buyMax жёстко ≥ **p10** seller-mins; volume buy≥0.85×sell.

### 2026-09-29 — book2 nac ≥300k снова жёсткий

Buy-ratio/buyEdge больше не режут softMin. Если buyEdge+300k > p40 — sell↑.

### 2026-09-30 — сняли expensive buy freeze

Не сливается ≠ стоп-закуп. Freeze/no_book buyMax=0 убран. Чинить sell (дно мульти), не отключать buy.
Остаётся thin-book buy-cap 85% как у volume.

### 2026-09-30 — volume buyMax ≤ buyEdge−200k (без hardcap 1.0M)

Hardcap sell=1.0M откатили: это не алгоритм. Buy глубже края (edge−200k) оставляем.
Дорогие: дно от ≥1 мульти-селлера (≥3 лота). Sword7→multiLow — отдельно, если скажет.

### 2026-09-30 — `stock_norm_july11` (live)

Откат к ядру `37e01ade` без крутилки наценки и без experiments.
- **DOWN:** `held > normal_count` ∧ `sales < NormalSales`
- **UP deficit:** `sales < NormalSales` ∧ `held ≤ normal_count`
- **Книга только рычаг:** empty ∧ price < 0.85×bookFloor → +1
- set_min / set_max — фильтр по книге (max: шум/стена)
- `normal_count` capped ≤4
- nacenka из конфига, не крутится

Rollback: `capitalPolicy = capitalPolicyV9` (+book2).
OOS full_compare vs v9 baselines — отдельно (эта политика не в sim_v9).

### 2026-09-30 — prod → `stock_corridor_v9` (+book2), UP-cap 0.88

Откат с `stock_norm_july11`. Мечи/relist → book2; остальное → v9 corridor.
Потолок ↑ с stock_norm (seller-p q=0.95→0.88) оставлен на v9 UP path.
enoughItems убран с ботов: АХ полон не стопает закуп.

