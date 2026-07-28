# Ценообразование 4narek: эксперименты classic → stock_corridor_v6

Документ для быстрого погружения. Код: `4narek-new/pricing.go`, политика в логе: `capital_log.go` → `capitalPolicy`. Метрики: `ml_data/pricing.db` → таблица `capital_cycles`.

## Цель

Держать **sell-цену** так, чтобы сток `held = onAH + inv` жил в коридоре относительно `share` (слоты флота под тип). Наценку **не крутим** (фиксирована в `items_config`). Сохраняем: price floor (minBuy+nacenka), manual min/max.

Оптимум по истории: после **DOWN** и **HOLD** forward-profit обычно лучше, чем после **UP**. Значит ↑ должен быть редким и только на реальном разборе витрины.

---

## Classic (`classic_*`, до ~15.07.2026)

**Идея:** если `sales < NormalSales` и сток/АХ не раздуты → ↑.

**Факт (разбор ~14–15.07):**
- UP ~44% циклов, HOLD ~35%, DOWN ~17%
- После UP fwd profit ≈ 0 / слабо отрицательный; после HOLD/DOWN — миллионы плюсом
- `try_sells` = попытки **выставить** на АХ (сторона продавца), не спрос покупателя — в classic почти не использовался
- Пустой/слабый рынок и «недобор относительно N» путались → цена ползла вверх

**Вывод:** управлять не «догонять NormalSales», а **fill = held/share**.

---

## stock_corridor_v1 (~15–17.07)

**Идея:** коридор fill ≈ **15–25%** share.
- `held > hi` → ↓
- `held < lo` + есть продажи → ↑
- иначе HOLD
- nacenka не трогаем

**Плюс:** резко меньше бессмысленных ↑ vs classic.  
**Минус:** пустая витрина всё ещё могла давать ↑; мало защиты от «цена уже высокая».

Policy string: `stock_corridor_v1`.

---

## stock_corridor_v2 (~17–19.07)

**Добавки:**
- **try-veto:** много `try_sells` при малых sales → ↑ запрещён (`corridor_hold_try_veto`)
- **dead hold:** sales=0 и buys=0 при недоборе → не разгонять (`corridor_hold_dead`)
- пороги soft/over/dump, hysteresis вокруг полосы
- up-streak лимит (сначала до 3 подряд)

**Факт (≥18.07 в ретроспективе):** UP ~19%, HOLD доминирует. ↑ всё ещё слабый по fwd (−0.6M vs +2.7M после ↓). Fill часто ниже целевой зоны; в одном срезе колонка Fill в БД была 0 (баг логирования, потом чинили).

Policy: `stock_corridor_v2`.

---

## stock_corridor_v3 (~19–21.07)

**Изменения vs v2:**
- `lo` **15% → 18%** (полоса 18–25%)
- **buy-veto:** `buys ≥ sales` → ↑ запрещён (нет чистого разбора)
- **soft↓ сразу выше hi** (не HOLD в 25–28%)
- `corridorMaxUpStreak = 1` (не два ↑ подряд)
- Fill в капитал-лог починен

**Факт (19–21.07, live DB):**
- UP ~11–15%, HOLD ~75–79% — лучше classic
- fwd после UP всё ещё слабее HOLD/DOWN
- **Ночь 21.07 ~03–09 MSK:** zero-activity ~27%, sales ~1.2, held просел → серия `up_demand` на sales=1–2, held=0 («разбирают витрину»)
  - Пример: `бульдозер-1` 1.0M → 1.9M за ночь, утром held 6–7 и серия ↓ обратно
  - Штаны/ботинки/кирка — тот же паттерн (+0.6…1.1M climb)
- `players_online` логируется; **corr(online, UP%) ≈ 0**, corr(online, sales) ≈ 0.49 (в основном суточный профиль)
- Решение: онлайн **не обязателен** для оптимального ↑ — режет сила спроса и паузы, не headcount

Policy: `stock_corridor_v3`.

---

## stock_corridor_v4 (21–23.07.2026)

**Проблемы, которые закрывали:**
1. Ложный дефицит / ночной разгон на крошках sales
2. Ползучий ↑ через ↑–hold–↑
3. Залипание hard-over из‑за overshoot-guard утром при перестоке

**Правила ↑ (held < lo), по порядку veto:**
1. try-veto (как v3)
2. buy-veto (как v3)
3. **weak_demand:** ↑ только если `sales ≥ 3` **или** (`sales ≥ 2` и прошлый цикл тоже `≥ 2`)
4. **up_cooldown:** после ↑ ещё **2** цикла без ↑ (`CorridorUpCooldown`)
5. up_streak ≤ 1
6. иначе `corridor_price_up_demand`

**↓:**
- soft (> hi): как v3, с overshoot-guard
- **over (≥ 35%) и dump (≥ 50%): всегда ↓**, overshoot-guard **снят** (v4)

Новые action labels: `corridor_hold_weak_demand`, `corridor_hold_up_cd`.

Policy: `stock_corridor_v4`.

### Live-разбор v4 (снимок 23.07.2026)

Источник: `VACUUM INTO` с Go VPS → `4narek-ml/data/pricing.db`.  
Окно v4: **21.07 19:40 – 23.07 15:01 MSK**, **4174** цикла. Сравнение с v3 (19–21.07, 5235).

| метрика | v3 | **v4** | вердикт |
|--------|---:|-------:|---------|
| UP % | 12.7 | **10.7** | чуть реже ↑ |
| HOLD % | 77.1 | **81.8** | ок |
| DOWN % | 10.1 | 7.5 | меньше ↓ (сток тоньше) |
| avg sales / цикл | 4.09 | 4.07 | объём не просел |
| avg profit_now / цикл | 1.98M | **2.04M** | не хуже |
| fwd после UP (M) | 1.39 | **1.76** | ↑ качественнее |
| fwd после HOLD / DOWN | 1.76 / 4.73 | 1.76 / **4.96** | паритет / лучше |
| zero-activity ночь 03–09 | 31.2% | **22.9%** | лучше |
| UP% ночь 03–09 | 12.4 | **8.0** | цель чеклиста ✓ |
| sales≤2 среди ночных ↑ | 63.6% (91/143) | **16.3% (15/92)** | weak_demand работает |
| sales≥3 среди всех ↑ | 76% | **93%** | ✓ |
| слабые ночные climb (sales_sum&lt;40) | **7** (до +0.9M) | **2** (до +0.2M) | цель ✓ |
| fill в полосе 18–25% | 23% | **17%** | чаще недобор |
| fill &lt; 18% | 42% | **57%** | сток тоньше |

Новые action (v4 only): `weak_demand` 208 (5%), `up_cd` 230 (5.5%) — в основном режут ночной мусор (weak: avg sales 1.3, 57% ночью). Днём weak_demand почти только sales=1–2 (не режет живой спрос ≥3).

Дневной спрос (sales≥5): UP% 19.4 → 18.9 — **не убит**. fwd после дневного UP даже выше (2.34M vs 1.68M у v3 day).

**Чеклист v4:** закрыт по ночи/объёму. Осталось: ночной ↑ fwd&lt;0, тонкий fill, позор на floor.

Canvas: `pricing-corridor-v4`.

---

## stock_corridor_v5 (23–24.07.2026)

**Проблемы из live v4:**
1. Ночной ↑ (даже при sales≥3) давал fwd ≈ −0.42M
2. ↑ при held=0 — пустая витрина ≠ «надо поднять цену»
3. Fill часто ниже lo (up_cd=2 + жёсткие veto)

**Правила ↑ vs v4:**
1. try-veto / buy-veto — без изменений
2. **empty:** `held ≤ 0` → `corridor_hold_empty` (↑ запрещён)
3. **weak_demand:** день как v4 (sales≥3 или sustained≥2); **ночь 03–09 MSK: только sales≥4**, sustained×2 ночью выключен
4. **up_cooldown = 1** (было 2) — быстрее отвечать на реальный разбор
5. streak ≤ 1

↓ без изменений (soft / over / dump).

Новый label: `corridor_hold_empty`.

Policy: `stock_corridor_v5`.

**Live (до ~24.07 14:30 MSK):** ночной UP% 3.4%, fwd ночного ↑ ≈ +1.8M (цель ✓). Fill всё ещё тонкий (~65% циклов <18%). Позор сверху (штаны ~99%).

---

## stock_corridor_v7 (28.07.2026) ← текущий

**Проблема live v6 после ручного дампа:** цены на BasePrice/floor с `held=0` не поднимались.
`buys≥sales` ловило `0≥0` → вечный `buy_veto`; empty/weak резали ↑.

**Правила vs v6:**
1. **buy-veto** только при живом обороте (`buys+sales>0`)
2. **recover-↑** днём: цена < max(floor+12·step, 2×BasePrice), сток < lo, нет try-veto → ↑ с up_cd (даже без sales)
3. Высокий vacuum (held=0 далеко над полом) по-прежнему без ↑

Policy: `stock_corridor_v7`.

---

## stock_corridor_v6 (24.07.2026)

**Проблемы из live v5:**
1. Fill часто < lo — up_cd тормозит восстановление при глубоком недоборе
2. Позор не сливается универсальной полосой 18–25% / over 35%
3. Симметричный step: hard↓ медленный на перестоке

**Правила vs v5:**
1. **per-type полоса:** позор lo/hi/soft/over/dump ≈ **10 / 18 / 22 / 25 / 40%**; остальное как v5 (18/25/28/35/50)
2. **deep-↑:** днём при `held ≤ lo/2` + сильный спрос → ↑ даже на `up_cd` (`corridor_price_up_deep`)
3. **hard↓ ×2:** over/dump режут `step×2`; soft↓ по-прежнему ×1
4. empty / night weak_demand / try/buy veto — без изменений

Policy: `stock_corridor_v6`.

**Сознательно не в v6:** стоп-закуп позора на floor (buy-path ботов).

---

## Инварианты (не ломать)

| Что | Где |
|-----|-----|
| Наценка фиксирована | `ensureNacenkasInitialized`, adjust не меняет nacenka |
| Floor продажи | `sellPriceFloor` / minBuy history |
| Manual min → только ↑, max → только ↓ | `manualDirectionClampLocked` |
| Цикл = `AnalysisTime` предмета | forward в `capital_cycles` на 3 цикла |
| Share = слоты типа во флоте | `itemSlotShareLocked` |

---

## Как смотреть результаты

```bash
# на Go VPS
sqlite3 ~/4narek-new/ml_data/pricing.db \
  "SELECT policy, action, COUNT(*) FROM capital_cycles
   WHERE ts >= '2026-07-21' GROUP BY 1,2 ORDER BY 3 DESC;"
```

Безопасный снимок без остановки процесса: `VACUUM INTO` → `pricing-export.db`, потом scp.

Полезные срезы:
- доля UP/HOLD/DOWN по дням и policy
- avg `fwd_profit_3` после UP vs HOLD vs DOWN
- ночные окна UTC 00–06 (= 03–09 MSK): zero-activity %, climb по item
- новые hold: `weak_demand`, `up_cd`, `empty` (v5)

---

## Что сознательно не делали (и почему)

| Идея | Статус |
|------|--------|
| Крутить nacenka в коридоре | Отвергнуто: маржа/закуп отделены от sell-контроля |
| Жёсткий бан ↑ ночью (0 ↑) | Отвергнуто; в v5 — **мягкий** порог sales≥4 ночью 03–09 MSK |
| ↑ от `players_online` | corr с UP≈0; отложено |
| Стоп-закуп позора на floor | Отложено: buy-path ботов, не sell-коридор |
| ML как основной контроллер | Shadow/лог есть; live-решение — corridor rules |
| Вернуть classic NormalSales | Хуже по fwd на истории |

---

## Файлы

- `4narek-new/pricing.go` — алгоритм
- `4narek-new/capital_log.go` — `capitalPolicy`, запись циклов
- `4narek-new/online.go` — снимок онлайна (лог)
- Canvases (разборы): `pricing-corridor-v4`, `pricing-corridor-v3`, `false-scarcity-up`, `online-pricing-dependency`, `pricing-algo-retrospective`

---

## Чеклист следующего эксперимента (после v6)

После ≥1 суток на v6:
1. fill&lt;lo%: стало меньше за счёт deep-↑?
2. позор: доля циклов ≥over / dump↑, held штанов-позор вниз с ~99%?
3. fwd после `corridor_price_up_deep` ≥0 и не хуже обычного ↑?
4. hard↓×2 не уводит в floor-lock чаще обычного?
5. Стоп-закуп позора — отдельный эксперимент (buy-path).
