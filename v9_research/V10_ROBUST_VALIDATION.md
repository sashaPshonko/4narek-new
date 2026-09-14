# V10 Robust Validation

Дата: 2026-09-14  
Сырьё: `results_v10_robust.json`  
Код: `v10_robust_validation.py`  
**Production не менялся. V11 не искали.**

Главный вопрос:

> Насколько можно доверять выводу V10, что более низкий inventory target + более агрессивный DOWN повышают долгосрочную прибыль?

---

## Verdict (коротко)

### **LOW CONFIDENCE** (ближе к INVALID для «агрессивного» крыла V10)

- Мягкий кандидат **r131** (ниже band, но не extreme under) даёт ~**1.05–1.20×** v9 на нескольких demand models — скромно и нестабильно по абсолюту.
- «Чемпион» поиска **r1100** (aggressive_down, under часто **0.4–0.8**) **не держит** ранжирование across simulators; на части folds/models **проигрывает** v9; выигрыш fold3 **схлопывается** после выкидывания top-1/top-2 SKU.
- Observational elasticity: **глубокий under (lt70) — худшие sales**, не лучшие. Peak sales у **near-market 0.95–1.05**.
- Event-study: DOWN даёт **краткий** всплеск sales rate, к **6–24h эффект уходит в минус**.
- Full AH replay Jul–Sep **невозможен** (book с 2026-09-03).

Не внедрять V10. Не трактовать 1.47× как факт.

---

## 1. V10 vs v9 (multi-demand, 4 OOS folds)

`x = OOS mean profit/24h(candidate) / v9`

| Fold | Model | r131 | r1100 |
|------|-------|-----:|------:|
| 0 | A_current | **1.20** | 0.84 |
| 0 | B_knn | 1.05 | 0.90 |
| 0 | C_empirical | 1.09 | 0.96 |
| 0 | D_ols | 1.06 | 1.14 |
| 1 | A | 1.19 | 1.00 |
| 1 | B | 1.04 | 0.82 |
| 1 | C | 1.18 | 1.06 |
| 1 | D | 1.09 | 1.13 |
| 2 | A | 1.16 | 1.08 |
| 2 | B | 1.18 | 0.97 |
| 2 | C | 1.16 | 1.14 |
| 2 | D | 1.03 | 1.35 |
| 3 | A | 1.08 | **1.42** |
| 3 | B | 1.07 | 1.13 |
| 3 | C | 1.08 | 1.42 |
| 3 | D | 1.15 | 1.16 |

**Сохранение ранга `V10 > v9`:**  
- r131: часто да, обычно **<1.25×**.  
- r1100: **нет** — часто ≤1 или <1 на A/B.

---

## 2. V10 vs old corridor / HOLD

На тех же folds (A_current), r131 обычно ≥ old corridor; vs HOLD смешанно (иногда HOLD ≈ v9).  
r1100 часто хуже old на folds с умеренным under, иногда лучше на fold3 (том же, где SKU-концентрация).

Матрица в JSON: `matrix[*].models[*].{old_corridor_v4ish,hold,x_v9}`.

---

## 3. OOS folds

4 expanding walk-forward окна (Aug), demand fit только на train.  
Исходный V10 search имел 2 usable folds — здесь жёстче.

---

## 4. Demand models

| ID | Суть |
|----|------|
| A | текущий `E[sales\|ratio,held,TOD]` |
| B | kNN по (ratio, fill, night) |
| C | empirical buckets (как A, явные samples) |
| D | per-item OLS sales~ratio+fill+night |
| E replay | **не строили** — нет AH Jul–Aug |

V10 advantage **simulator-dependent**, особенно r1100.

---

## 5. Bootstrap / confidence

Fold3, Demand A, paired bootstrap по **7** суткам:

| Contrast | mean Δ 24h M | 95% CI | P(V10>base) |
|----------|-------------:|--------|------------:|
| r1100 vs v9 | +301 | [217, 396] | 1.00 |
| r131 vs v9 | +59 | [18, 102] | 1.00 |

**Оговорка:** n_days=7 — CI узкие «на бумаге», но окно короткое; не путать с сезонной устойчивостью.

---

## 6. SKU stability

**r1100 (fold3 A):**

| Metric | Value |
|--------|------:|
| overall × | 1.423 |
| без top-1 SKU | **1.031** |
| без top-2 | **0.997** |
| median SKU × | 1.079 |

→ Агрессивный V10 win **почти целиком от 1–2 крупных SKU** (штаны и соседние по объёму).

**r131:** overall 1.08; без top-1 всё ещё ~1.05; median ~1.08 — **шире по SKU**, слабее по величине.

---

## 7. Underpricing analysis

### Associative elasticity (sales vs ratio proxy)

| ratio bucket | n | mean sales | median | 95% CI mean |
|--------------|--:|-----------:|-------:|-------------|
| lt70 | 3281 | **0.49** | 0 | [0.43, 0.55] |
| 70–85 | 3257 | 2.58 | 1 | [2.45, 2.71] |
| 85–95 | 5816 | 2.95 | 2 | [2.85, 3.05] |
| **95–105** | 14730 | **3.44** | 2 | [3.37, 3.51] |
| 105–120 | 14749 | 3.26 | 2 | [3.19, 3.32] |
| gt120 | 10512 | 1.65 | 0 | [1.60, 1.71] |

Глубокий under **не** = максимум sales в данных. Это бьёт по гипотезе «V10 выигрывает, потому что симулятор любит дешёвое».

### under vs profit scan (random chroms, Demand A)

`corr(under_frac, profit_24h) ≈ **−0.36**`  
Политики с under≈0.9–0.95 **не** топ по profit.

Но r1100 всё равно сидит в under 0.4–0.8 на OOS — высокий under возможен без глобальной монотонности under→profit.

### Event-study (реальные ΔP)

После **DOWN** (n≈5086): sales rate vs pre **+1.61** на 10m, **+0.63** на 1h, ≈0 к 3h, **отрицательно** к 12–24h.  
После **UP** (n≈9814): sales rate **падает** на всех горизонтах до 24h.

Интерпретация: краткий dump-эффект есть; **хронический** under как 24h стратегия из event-study **не** подтверждается.

---

## 8. Ablation (r1100, fold3, Demand A)

| Variant | Δ vs full (M/24h) | under |
|---------|------------------:|------:|
| full | 0 | 0.42 |
| **no_low_band** (restore 0.18/0.25) | **−82** | 0.20 |
| smaller_hard_down (×2 not ×4) | −55 | 0.29 |
| restore_down_block 0.90 | −27 | 0.71* |
| bigger_up | −11 | 0.42 |
| no_soft_over_mkt | +18 | 0.39 |
| no_aggr_style→corridor | +20 | 0.39 |

\* restore_down_block странно поднял under — смотреть как хрупкость, не как «veto вреден».

Главный носитель edge на этом fold: **низкий band** + **жёсткий DOWN**, не soft_over_mkt.

---

## 9. Simulator-dependent effects (матрица)

| Policy | A | B | C | D | Стабильность |
|--------|--:|--:|--:|--:|--------------|
| r131 > v9 | чаще | чаще | чаще | чаще | **средняя**, × скромный |
| r1100 > v9 | иногда | редко | иногда | чаще | **низкая** |
| old > v9 | редко | редко | иногда | иногда | — |
| HOLD > v9 | иногда | ~ | ~ | редко | — |

**Если V10 выигрывает только на одном DemandModel — слабое доказательство.** Так и есть для r1100.

---

## 10. AH p10 / realism

`ah_book_lots`: ~4.4M rows, **2026-09-03 → 09-14**, 18 items.  
V10 search / основной panel Jul–Sep использовали **median our sells**.  
Sep-only AH replay — следующий research шаг; полный сезон — нет данных.

---

## 11. Ограничения

1. Demand всё ещё associative / reduced-form.  
2. Event-study без matching по held/liquidity — confounding политикой.  
3. Bootstrap 7 дней.  
4. Model E невозможен на полном горизонте.  
5. Old corridor — приближение, не точный бинарь prod Jul.

---

## 12. Ответ на главный вопрос

**Доверять тезису V10 «ниже target + агрессивный DOWN = больше 24h прибыли» — нельзя на уровне HIGH/MEDIUM.**

Класс: **LOW CONFIDENCE**.

Причины:

1. Агрессивный победитель поиска неустойчив across demand models и folds.  
2. Его крупный win объясняется **1–2 SKU**.  
3. Реальные данные: deep under ≠ max sales; DOWN boost **короткий**.  
4. Исходный sim (audit) имеет каналы bias, но «дешёвое всегда профит» **не** подтверждается corr under↔profit (−0.36) и elasticity.

Что можно сказать осторожно:

- Небольшое ужесточение corridor / осторожный soft-down **может** быть слегка плюсом (~1.05–1.2× в симе) — **не доказано для prod**.  
- Исходные **1.47× / 2×** — не использовать как план внедрения.

---

## Файлы

| File | Содержание |
|------|------------|
| `V10_SIMULATION_AUDIT.md` | аудит sim |
| `MARKET_DATA_INVENTORY.md` | схема БД |
| `V10_SIMULATION_IMPROVEMENT.md` | что улучшили в research |
| `V10_ROBUST_VALIDATION.md` | этот отчёт |
| `results_v10_robust.json` | сырые цифры |
| `v10_robust_validation.py` | код |

**Никакого production deploy. Никакого V11 search в этом этапе.**
