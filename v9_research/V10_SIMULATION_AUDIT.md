# V10 Simulation Audit

Дата: 2026-09-14  
Код: `sim_v9.py`, `search_v10.py`  
Цель аудита: понять, насколько OOS «V10 ~1.3–1.5× v9» может быть артефактом симулятора.  
**Production не менялся.**

---

## 1. Какие данные реально используются

| Источник | Поля | Роль в V9/V10 sim |
|----------|------|-------------------|
| `capital_cycles` | ts, item_id, action, held, sales, buys, price_before/after, step, share, profit_now, nacenka_*, on_ah/inv (частично), cycle_minutes, fwd_* | **Основной panel** траектории |
| `trade_events` (sell) | ts, item_id, price, nacenka | **Market proxy** (`attach_p10_fast`: rolling median наших sell) + avg nacenka |
| Chromosome / policy | пороги, steps, styles | Решение UP/DOWN/HOLD |

V10 search **не** подключает в цикл: `ah_book_lots` raw p10, sellers/UUID, bans, stock snapshots, DOI из книги, ml_decisions.

---

## 2. Proxy vs observed

| Сигнал | Тип | Комментарий |
|--------|-----|-------------|
| Наша цена, held, sales, buys цикла | **observed** (logged) | Из `capital_cycles` |
| Market `mkt` / `p10` в V10 | **proxy** | Median **наших** последних sell, не AH p10 |
| Ratio = price/mkt | **derived proxy** | Зависит от качества mkt |
| Counterfactual sales при цене ≠ logged | **synthetic** | `DemandModel.expected_sales` |
| Buys при understock | **synthetic** | refill к ~22% share (кроме empty_idle) |
| Profit | **semi-synthetic** | `sales_sim × avg_nacenka` |
| AH lots / sellers / p10 book | **available in DB, unused in V10 loop** | Только в `attach_p10` (медленный путь, search не использует) |

---

## 3. Наблюдаемые данные (что симулятор «видит» в момент t)

На цикле t policy получает:

- `st.price`, `st.held`, empty_streak, cooldowns (sim state)
- из obs: share, step, logged sales/buys (для blend), ts→night, mkt proxy
- **не** видит: будущие sales, будущий p10, fwd_profit_*, следующие действия

---

## 4. Где используются реальные исторические значения

1. **Инициализация** цены/held с первого цикла окна.  
2. **Fit DemandModel** на train: средние sales по buckets `(item, ratio_bucket, held_bucket, day/night)` из **исторических** циклов.  
3. **Blend 70/30**: если sim-цена ≈ `price_before` (±1 step) → `0.7*logged_sales + 0.3*model`.  
4. **empty_idle**: sales/buys = logged (обычно 0), без artificial restock.  
5. **Календарные сутки** для profit/24h — по ts циклов.

---

## 5. Где синтетика

1. Любая цена далеко от logged → sales = `E[sales|…]` целиком.  
2. Inventory: `held' = clamp(held - sales + buys)`.  
3. Buys refill formula (кроме empty_idle).  
4. Market не эволюционирует от наших действий (mkt берётся из истории sell median, привязанной к ts панели, не к sim-цене).  
5. Нет FunTime set_min, multi-bot shocks, treasury, captcha downtime.

---

## 6. Моделируемые зависимости price → demand → inventory

```text
sim_price / mkt_proxy → ratio_bucket
held/share → held_bucket
ts → day/night
→ DemandModel → E[sales]
→ buys ≈ f(sales, held, share)  [synthetic refill]
→ held updates
→ profit += sales * nac
→ next decide(st, …)
```

Есть **петля**: низкая цена → (часто) выше E[sales] в under-buckets → быстрее слив held → чаще зона DOWN/UP по inventory → снова цена.

---

## 7. Что НЕ моделируется

- Реакция **чужого** AH (lots, sellers) на нашу цену  
- Liquidity / thickness книги  
- Seller bans / dump concentration  
- Endogenous market: наш dump не двигает «чужой» p10 в симе  
- Buy-side capacity / captcha / bot outages  
- Nacenka как trade-off (при nac_mult≠1 был артефакт; сейчас фикс 1.0)  
- Cross-SKU capital / slot competition между предметами  
- True causal effect ΔP → Δsales (только associative buckets)

---

## 8. Potential leakage

| Риск | Оценка |
|------|--------|
| Future sales в decide() | **Нет** — не передаются |
| Demand fit на OOS | **Нет** в корректном WF (fit train only) |
| Market proxy использует sell ≤ ts | **Ок по времени**; но sell — наши, не внешний рынок |
| Blend с logged sales | **Мягкий leakage к истории**: политика, держащая цену у logged, получает «бесплатно» реальные sales; политика, уехавшая далеко, полностью на модели |
| fwd_* в capital_cycles | **Не используются** в sim loop |
| Selection на val затем report OOS | Стандартно; fold2 thin — слабость дизайна folds |

**Самый опасный механизм:** blend 70% logged при near-logged цене + полная модель при underprice уезде → асимметрия в пользу политик, которые либо копируют историю, либо уходят в under-buckets с высоким train E[sales].

---

## 9. Selection bias

1. **Policy-induced states in train:** underprice buckets часто заполнены циклами, где DOWN уже случился из-за excess / DOI — sales там могут быть высокими по другим причинам.  
2. **FOCUS 10 SKU** — объёмные; штаны доминируют.  
3. **Survivorship of thick cycles** — тонкий рынок / сбои меньше представлены.  
4. **V10 shortlist:** top по train 24h → val → OOS; оптимизация под тот же DemandModel.

---

## 10. Систематическое завышение дешёвых цен

`ratio_bucket`: lt70, 70_85, … — на train `global_by_ratio` / item rates часто выше sales при низком ratio (если исторически underprice коррелировал с распродажами).

Counterfactual «поставить 0.7× рынка при том же held» подставляет **среднее sales из всех прошлых underprice циклов**, включая:

- вынужденный dump при забитом складе;  
- ночные/акционные режимы;  
- ошибки, после которых товар уходил дёшево.

Симулятор **не штрафует** маржу за underprice (nac фиксирован) → profit ≈ volume × const → **volume bias = profit bias**.

V10 лидеры с `under_frac ~0.48` — прямой красный флаг этого канала.

---

## 11. Завышение агрессивного DOWN

1. DOWN → цена↓ → ratio↓ → bucket с большим E[sales].  
2. Быстрее падает held → снова «нужен» DOWN при excess thresholds (особенно узкий lo/hi).  
3. `soft_down_when_over_mkt` + `down_block≈off` усиливают путь в under.  
4. Hard DOWN ×3–4 быстрее загоняет в дешёвые buckets за меньше циклов.

Это согласуется с паттерном V10 winners (низкий band, aggressive_down, soft over-mkt).

---

## 12. Наиболее далёкие от реального рынка части

1. **Market = median our sells** вместо AH p10/lots.  
2. **Demand = статические bucket averages** без causal identification.  
3. **Synthetic buys refill** (кроме empty_idle).  
4. **Нет обратной связи** наших действий на чужую книгу.  
5. **Profit без ценового риска** (недобор маржи / неликвидность закупа при слишком низкой sell).

---

## DemandModel и counterfactual

Формула:

```text
E[sales | item, ratio_bucket(price/mkt), held_bucket, day/night]
```

### Что она умеет

- Описывать **ассоциацию** «в истории при таких buckets sales были в среднем X».  
- Давать гладкий ответ при малом уходе цены внутри соседних buckets.

### Чего она не умеет надёжно

Ответить на:

> «Если **в этом же** состоянии (тот же held, тот же внешний рынок, та же причина) поставить цену X вместо исторической Y — сколько будет sales?»

Потому что:

1. **Confounding:** цена выбиралась политикой, зависящей от held/sales/DOI.  
2. **mkt proxy** не равен внешнему рынку.  
3. **Нет within-state randomization**; event-study по ΔP нужен отдельно (см. robust validation).  
4. Buckets грубые (6 ratio × 5 held × 2 TOD) — много гетерогенности внутри ячейки.

**Вердикт аудита:** текущий sim **полезен для относительного ranking похожих inventory-политик**, но **слаб для доказательства**, что «ниже target + агрессивный DOWN» реально дают +30–50% денег. Особенно подозрителен канал underprice→volume.

---

## Inventory conservation check (код)

```text
held := max(0, round(held - sales + buys))
held := min(held, 1.5 * share)
```

- Отрицательный held отсекается.  
- Продажи при held=0: model может дать sales>0 если не empty_idle; empty_idle форсирует logged (0).  
- Artificial restock на empty_idle **выключен** (fix V9 research) — хорошо.  
- AH on_ah vs inv в V10 loop **не симулируются отдельно** (только total held).

---

## Связь с результатом V10

| Наблюдение V10 | Совместимо с bias sim? |
|----------------|------------------------|
| 1.3–1.5× vs v9 | Да, возможен overstated |
| Высокий under_frac у лидеров | **Сильный** признак volume bias |
| Низкий lo/hi + aggressive DOWN | Усиливает путь в under buckets |
| 2× с nac_mult=1.5 | Доказанный артефакт (отброшен) |

Следующий этап (robust validation): независимые demand models, event-study, under vs profit, AH p10 sample, bootstrap — **без** внедрения V10.
