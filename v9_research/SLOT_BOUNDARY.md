# book2: sell p40 / buy ≥ p10 продавцов

**Status:** LIVE

Цены из книги 30м. SKU не баним.

## Якоря (per-seller min, без ban)

| | |
|---|---|
| **sell** | seller **p40** — конкурентный край (не дамп, не lot-клоны) |
| **buyMax** | ≥ seller **p10** — иначе AH пустой для закупа |

Нужно ≥3 sellers. Volume: `sellMkt < 2.5M`.

## Buy / nac

1. softMin / per-SKU / global(fat only).
2. **buyEdge важнее softMin**: nac режется, чтобы `buyMax ≥ seller-p10`.
3. Доп. пол ratio: volume buy≥0.85×sell, fat ≥0.70×sell.
4. Если p40 < p10+softMin — sell поднимаем до buyEdge+softMin.
