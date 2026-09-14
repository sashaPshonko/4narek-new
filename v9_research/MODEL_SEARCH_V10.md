# MODEL SEARCH V10 — поиск 2× profit/24h vs v9

Дата: 2026-09-14  
Код: `v9_research/search_v10.py`  
Сырьё: `v10_search_results_nac1.json` (честный прогон), `v10_search_results.json` (первый прогон с артефактом nac)  
Симулятор: sequential demand-model (`sim_v9.DemandModel`), market proxy = rolling median наших sell (как в v9 research).

---

## Baseline

Метрика: **mean calendar-UTC day profit** (последовательный сим по циклам → сумма по суткам → среднее).

| Fold | OOS window (approx) | v9 mean profit/24h |
|------|---------------------|-------------------:|
| 0 | ~16–25 Aug | **732M** |
| 1 | ~25 Aug – 14 Sep | **518M** |
| 2 | thin OOS | skipped |

v9 chrom: corridor lo/hi 0.18/0.25, catchup_gap=1.0, down_block=0.90, nac_mult=1.0.

---

## Search space

Разрешено менять (chromosome / DSL):

- inventory `lo/hi/over/dump`
- `step_mult`, `up_mult`, `down_soft_mult`, `down_hard_mult`
- demand UP: min_sales, night_sales, sales>buys, demand_max_ratio
- empty catchup: on/off, streak, gap
- DOWN guards: down_block_ratio, no_down_if_held≤hi, allow_down_empty
- cooldowns / max_up_streak
- styles: corridor | aggressive_down | holdish | market_follow | velocity | ekb_like
- market-follow pull, velocity thresholds
- soft_down_when_over_mkt, blind empty UP
- re-test «вредных» флагов в комбинациях
- **nac_mult**: в честном прогоне **зафиксирован = 1.0** (см. ниже)

Не делали (ещё): per-SKU отдельные политики, настоящий AH p10 в каждом цикле (дорого), RL, полная замена demand model.

---

## Number of experiments

| Run | Pool | Notes |
|-----|-----:|-------|
| #1 (discard for ranking) | 966 | random 800 + grid; **nac_mult∈{…1.5}** → ложный ≥2× |
| #2 (official) | **1357** | random **1200** + grid 155; **nac_mult=1.0 only** |

На fold: score all on train → top80 → val → top10 OOS.  
Demand fit **только на train**. Decide() без будущих sales/p10/inventory.

---

## Critical: nac_mult artifact

В run #1 почти все «2.0–2.3×» имели `nac_mult=1.5`: сим масштабирует `profit = sales × nac` **без** штрафа спроса → почти бесплатный ×1.5.

После `x / nac_mult` оставалось ~1.4–1.55× — это ближе к честному run #2.

**Вывод:** 2× из run #1 — **не принимать**. Дальше только nac=1.0.

---

## Best candidates (OOS, nac=1)

### Fold 0 — best `r131` → **1.315×** v9

| | |
|--|--|
| OOS 24h mean | 962M vs v9 732M |
| style | corridor |
| band | lo=0.10, hi=0.15, over=0.23, dump=0.33 |
| steps | step_mult=0.75, up=1.5, down soft/hard=1.0 |
| other | min_sales=4, down_block≈off (1.01), catchup_gap=0.7, soft_down_when_over_mkt=True |

### Fold 1 — best `r1100` → **1.468×** v9

| | |
|--|--|
| OOS 24h mean | 760M vs v9 518M |
| style | aggressive_down |
| band | lo=0.10, hi=0.15, over=0.20, dump=0.25 (**очень узкий/низкий**) |
| steps | step=1.5, up=0.5, down_soft=0.5, down_hard=**4** |
| other | down_block off, soft_down_when_over_mkt, under_frac высокий (0.48) |

### Aggregate

| | |
|--|--|
| folds ≥2× OOS | **0 / 2** |
| best raw OOS × | **1.47×** (один fold) |
| mean of fold-best × | **~1.39×** |

**Цель 2× на честном OOS — не достигнута.**

---

## Patterns among strong (>1.25×) candidates

Повторяется чаще, чем у v9:

1. **Ниже целевой fill** — lo/hi около 0.10–0.15 (v9 0.18–0.25).
2. **`soft_down_when_over_mkt`** — ↓ когда ratio≫1 даже в «полосе».
3. Часто **отключён underprice DOWN veto** (`down_block_ratio≈1.01`) → агрессивнее пилить excess/overprice.
4. **Небольшой UP step**, иногда **жёсткий DOWN ×3–4**.
5. Styles `aggressive_down` / узкий corridor выигрывают чаще hold/ekb.

Риск: высокий under_frac у лидеров fold1 — в live может быть «дешёвый оборот» артефактом demand-model (больше sales в under buckets).

---

## Per-SKU (fold1 winner r1100, OOS total M)

Только candidate totals (без поштучного v9 в этом дампе):

| SKU | cand profit M |
|-----|--------------:|
| штаны | 6635 |
| ботинки | 1745 |
| нагрудник | 1488 |
| шлем | 1299 |
| megasword | 919 |
| … | … |

Выигрыш не на одном SKU, но штаны доминируют по абсолюту (как обычно по объёму).

---

## Leakage / simulator caveats

1. Demand = hist E[sales|ratio,held,day/night]; counterfactual не FunTime.
2. Market ≠ live AH p10 (median наших sell).
3. Blend 70% logged sales when price≈logged → удерживает ближе к истории.
4. `nac_mult≠1` без связи с ценой = **запрещён** в поиске.
5. Абсолютные М относительны; **× vs v9 на том же симе** — главный сигнал.
6. Fold2 OOS слишком короткий — не использовали.

---

## Success levels

| Level | Result |
|-------|--------|
| 1.0× | v9 baseline |
| 1.25× | **да** — несколько кандидатов OOS |
| 1.5× | **почти** — max 1.47× на одном fold |
| **2.0×** | **нет** (честный nac=1) |
| 3×+ | только фейк nac_mult |

---

## Рекомендации (не prod cutover)

1. **Не внедрять** «2× модель» из run #1.
2. Исследовать как v10-кандидат (shadow): **ниже lo/hi + soft ↓ above market + осторожный UP** — цель ~1.3–1.45× в симе; сначала shadow/A-B на одном оркестраторе.
3. Следующий поиск:
   - запрет/штраф за under_frac;
   - отдельный constrained search: `under≤v9_under+ε`;
   - настоящий AH p10 в panel;
   - per-type / per-SKU chrom;
   - nac только как sell = buy_ref + nac (связанный demand).
4. v9 с catchup gap=1.0 остаётся разумным prod baseline, пока нет OOS ≥1.5× с низким under.

---

## Files

- `search_v10.py` — генератор + walk-forward  
- `v10_search_results_nac1.json` — честные результаты  
- `search_v10_nac1.log` — лог прогона (~11 мин, 1357 политик)

---

## Verdict

Широкий поиск (~1.3k политик, train/val/OOS) **не нашёл устойчивые 2× к v9** по mean profit/24h без читерства наценкой.  
Лучшее честное: **~1.3–1.5×** на отдельных OOS окнах, часто за счёт более агрессивного ↓ и более низкого целевого стока — с риском underprice в симе.
