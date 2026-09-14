# v9 research results — BEST MODEL candidate

Дата: 2026-09-14.  
Код симулятора: `v9_research/sim_v9.py`.  
Сырые цифры: `v9_research/results.json` (run1).  
Production **не менялся**.

## Ограничения симулятора (честно)

1. Demand: `E[sales|item, ratio_bucket, held_bucket, day/night]` с train.
2. Buys: синтетический refill к ~22% share; на empty_idle — только logged buys (после фикса; run1 ещё раздувал restock).
3. Market proxy: rolling median **sell** prices (не AH p10 / не ban-filter).
4. Когда sim-цена ≈ logged (±1 step) — blend 70% logged sales.
5. Нет FunTime `set_min`, multi-bot share shocks, treasury empty.
6. Поэтому абсолютные М — **относительный** ranking; observational live p/h — якорь реальности.

---

## 1. Observational: какие эпохи реально зарабатывали

| Policy | profit/hour | avg fill | UP% | fwd3 после цикла |
|--------|------------:|---------:|----:|-----------------:|
| **v4** | **199M** | 0.23 | 11% | 6.1 |
| **v3** | **190M** | 0.30 | 13% | 6.0 |
| **v6** | **177M** | 0.33 | 6% | 8.1 |
| v8b | 69M | 0.17 | 17% | 2.7 |
| v8 | 47M | 0.16 | 18% | 2.3 |
| v8x | 10M | 0.04 | 43% | 0.8 |
| v8ae/af | ~1–3M | 0.09–0.15 | ~2% | ~0 |

**Вывод:** пик — **простой inventory corridor v3–v6**. Поздний v8 с book/empty/DOI — не «прибыльнее», а тоньше сток и хуже p/h.

---

## 2. Гипотеза «мало товара → DOWN запрещён»

Logged DOWN → fwd_profit_1+2+3:

| held bucket | n DOWN | mean fwd3 |
|-------------|-------:|----------:|
| dump | 694 | **+12.3M** |
| over | 3694 | **+10.8M** |
| band | 503 | +10.2M |
| under | 41 | +3.5M |
| **empty** | 156 | **+0.06M** |

**Подтверждено:** DOWN при excess — сильный сигнал будущей прибыли.  
**DOWN при empty — бесполезен.** DOWN при under — редкий и слабее.

Это ядро старой модели: **inventory — главный драйвер DOWN**, не «мало продаж».

---

## 3. Гипотеза EMPTY streak → UP

Наблюдательно (все empty streak, без фильтра рынка):

| streak | UP fwd3 | HOLD fwd3 | Δ |
|--------|--------:|----------:|---|
| ≥2…8 | ≈ −0.2M | ≈ −0.05M | UP хуже |

Empty × ratio (streak≥3):

| ratio | n | mean fwd3 |
|-------|--:|----------:|
| 70–85% | 460 | **+0.13M** |
| 85–95 | 526 | ~0 |
| >120% | 2659 | **−0.13M** |

**Вывод:** голый `0/0/0` → UP **не** прибылен.  
Имеет смысл только **empty + underprice** (и даже тогда эффект скромный vs HOLD).  
Catchup v8af правильный *по направлению*, но не как единственный двигатель прибыли.

---

## 4. Sequential sim + walk-forward (run1)

Кандидаты: classic_inv × thresholds, empty_streak, inv_p10_guard, hold (~40 configs).

### Single 65/35 split
Лучший по test profit: `classic_inv(min_sales=4, lo=0.18, hi=0.25)` ≈ 14.7B sim.  
Причина: модель спроса награждает частый DOWN → больше sales×nacenka (артефакт turnover).

### Walk-forward (3 folds), refit demand каждый fold

`inv_p10_guard(streak=2, gap=0.80)` vs `classic_inv(min_sales=2)`:

| Fold | window | Δ profit |
|------|--------|---------:|
| 0 | 25.07–05.08 | **+1552M** |
| 1 | 05.08–22.08 | **+643M** |
| 2 | 22.08–14.09 | **+449M** |

Стабильный плюс OOS. Guard режет DOWN при уже низкой цене → меньше overshoot, меньше времени overpriced (ablation: over 0.32 vs 0.50).

---

## BEST MODEL — `stock_corridor_v9a`

### Почему

1. Возвращает **доказанный inventory core** эпохи v4–v6 (лучший live p/h).  
2. Запрещает **необоснованный DOWN** при низком held / underprice (гипотеза §2 + pochti overshoot).  
3. Добавляет **empty catchup только с market gap** (не blind empty→UP).  
4. Demand UP только при реальном разборе (sales≥3–4), не NormalSales.  
5. OOS walk-forward: market-aware guard бьёт чистый inventory.

### Правила UP / DOWN / HOLD

Полоса: `lo=18%`, `hi=25%` share; over=35%; dump=50%.  
Market `mkt` = raw p10 за 10m при `p10N≥40` (как catchup thick), иначе HOLD market-веток.

**DOWN** (только excess):
- `held ≥ dump` → −2×step  
- `held ≥ over` → −2×step  
- `held > hi` → −1×step  
- **Veto:** если `price/mkt < 0.90` → HOLD (не пилить уже дешёвое)  
- **Veto:** `held ≤ hi` → никогда auto-DOWN (grant)

**UP:**
1. **Empty catchup:** `held=0` ∧ `empty_streak≥2` ∧ `price/mkt < 0.80` ∧ `price+step ≤ mkt` ∧ up_cd=0 → +1  
2. **Demand:** `0 < held < lo` ∧ `sales ≥ 3` (ночь MSK 03–09: ≥4) ∧ `sales > buys` ∧ up_cd=0 ∧ `price/mkt < 1.05` → +1  
3. Иначе HOLD  

**Cooldown:** после sales-driven / catchup UP → up_cd=2; streak UP ≤1.

**Не делаем:** book→UP, NormalSales→UP, skim, recover к paid, DOI без market veto (DOI можно позже как overlay только если `ratio≥0.90`).

### Используемые сигналы

| Сигнал | Зачем | Доказательство |
|--------|-------|----------------|
| held/share | DOWN/UP зоны | live v3–v6; fwd после excess DOWN |
| sales/buys | demand UP | v4 weak_demand |
| empty_streak | catchup arm | нужен + gap; один цикл бесполезен |
| price/mkt (p10) | gap + DOWN veto | empty×ratio; WF guard; pochti |
| up_cd | анти-лесенка | v4 |

### Отброшено

| Идея | Почему |
|------|--------|
| Classic NormalSales↑ | хуже fwd исторически |
| Blind empty↑ | UP fwd < HOLD на streak |
| Book↑ / FE↑ / MR↑ | late v8 fill collapse; book alone ≠ proof |
| DOI dump без ratio | overshoot underprice (pochti) |
| Skim в полосе | v8s выкл; matched HOLD лучше |
| Максимум сложности v8af | p/h ≪ v4 |

### Сравнение (кратко)

| | Live p/h | Sim OOS vs classic | Риск overprice |
|--|----------|--------------------|----------------|
| Classic NormalSales | плохо (до corridor) | — | высокий UP |
| v4–v6 corridor | **лучший** | baseline inventory | средний |
| v8 / v8b | средний | — | тонкий fill |
| v8ae/af | худший live | catchup thick ещё чинить | underprice stuck |
| **v9a** | цель ≈ v4 + guards | guard **+0.4–1.5B**/fold | ниже |

### Исторический / OOS (sim, FOCUS 10 SKU)

- classic_inv test: ~14.5–14.7B / 23k cycles  
- inv_p10_guard WF: +449…+1552M vs classic на каждом fold  
- Ablation: HOLD alone −1.4B vs classic; p10guard меньше over, меньше DOWN  

(После фикса empty_idle buys — run2 выполнен 14.09.)

### Run2 (empty_idle buy fix)

- Single 65/35: `hold` формально №1 по sim profit — **артефакт** (заморозка цены + blend logged sales; under 0.37). Не в prod.
- Среди активных: `v9_core` / `inv_p10_guard` ≫ classic (ablation ~17.0B vs 16.3B).
- Walk-forward `v9_core` vs classic: **+1952 / +615 / +484 M** — кандидат подтверждён.

### Production plan (когда скажешь внедрять)

1. Новый файл `pricing_v9.go` / ветка `adjustPrice` под `capitalPolicy = stock_corridor_v9a` — **не** патч v8af.  
2. Переиспользовать: `stockTargets`, `ahBookP10Since` (thick = p10N≥40), up_cd.  
3. Не тащить: DOI F, empty_inventory_up legacy, book raise flags.  
4. Shadow 1–2 суток рядом с v8af **или** cutover на 502-only.  
5. Метрики: profit/hour, fill 18–25%, under_frac, empty_streak catchup count.  
6. Catchup thick fix (p10N) — можно влить в v9a как часть market signal, не как отдельный v8ag.

---

## Следующий шаг research (без prod)

1. Дожать run2 с фиксом empty_idle (SSH).  
2. Тысячи конфигов: down_block_ratio ∈ {0.85,0.90,0.95}, gap, streak, night sales.  
3. 6h/12h/24h cumulative curves per SKU.  
4. Отдельный sim с **AH raw p10** вместо sell-median.

Главный вопрос закрыт для кандидата:  
**да, v9a увеличивает ожидаемую долгосрочную прибыль vs чистый inventory и vs late v8** — по live p/h якорю (вернуться к v4-core) + OOS guard.
