# Ценообразование 4narek: эксперименты classic → stock_corridor_v4

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

## stock_corridor_v4 (21.07.2026) ← текущий

**Проблемы, которые закрываем:**
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

**Онлайн:** по-прежнему только в лог / ML snapshot. В ветку решения **не входит** — на текущих данных не улучшает отбор ↑ лучше, чем порог sales + cooldown.

Новые action labels: `corridor_hold_weak_demand`, `corridor_hold_up_cd`.

Policy: `stock_corridor_v4`.

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
- новые hold: `weak_demand`, `up_cd`

---

## Что сознательно не делали (и почему)

| Идея | Статус |
|------|--------|
| Крутить nacenka в коридоре | Отвергнуто: маржа/закуп отделены от sell-контроля |
| Жёсткий бан ↑ ночью по часам | Хрупко к смене онлайна; заменено порогом sales |
| ↑ от `players_online` | corr с UP≈0; отложено, пока sales-гейт не доказан в проде |
| ML как основной контроллер | Shadow/лог есть; live-решение — corridor rules |
| Вернуть classic NormalSales | Хуже по fwd на истории |

---

## Файлы

- `4narek-new/pricing.go` — алгоритм
- `4narek-new/capital_log.go` — `capitalPolicy`, запись циклов
- `4narek-new/online.go` — снимок онлайна (лог)
- Canvases (разборы): `pricing-corridor-v3`, `false-scarcity-up`, `online-pricing-dependency`, `pricing-algo-retrospective`

---

## Чеклист следующего эксперимента (v5?)

Только после ≥1–2 ночей на v4:
1. Стало ли меньше climb 03–09 MSK при живых ботах?
2. Не упал ли дневной объём продаж на предметах с реальным разбором (sales≥5)?
3. Если weak_demand режет слишком много дневных ↑ — снизить пол до 2 **или** привязать порог к `share` (`max(2, share/20)`).
4. Онлайн снова рассматривать только если после v4 ночной мусор останется при sales≥3.
