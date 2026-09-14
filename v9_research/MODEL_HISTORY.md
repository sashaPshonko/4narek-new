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
