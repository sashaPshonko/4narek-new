# Slot selectivity — adaptive book2

**Status:** LIVE — `book2AdaptBuy` от загрузки АХ (не ручной тык).

## Идея

Фиксированный buy/nac при полном АХ — плато. Нужен контур:

| сигнал | действие |
|--------|----------|
| AH load → 1 (биток) | buyEff ↓ (селективнее) |
| AH load низкий / starve buys | buyEff ↑, nac floor → soft min |
| этому id noRoom | ещё −0.03 buyEff |

EMA по `go_type`. Buy clamp **[0.78, 0.92]** — не повторяем FAIL 0.80+400 оба сразу.
Nac floor = JSON baseline при полной загрузке; soft (⅔ base, ≥200k) только при голоде.

## База JSON

- мечи nac **300k** (mega **400k**) — baseline, не sticky runtime floor
- `itemsNacenkaBase` замораживается при load; runtime nac для ботов пишется отдельно

## Watch

Лог `[BOOK2] ... adapt load=… buyEff=…`. 2–4ч: load≈1 → buyEff~0.80–0.83; пустые слоты → buyEff растёт, buys/h не мрут.
