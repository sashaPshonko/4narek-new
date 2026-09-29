# book2: sell = нижняя граница продавцов

**Status:** LIVE

Цены целиком из книги 30м. SKU не баним.

## Рыночный якорь (sell)

**sell ≈ p10** среди *минимальных цен каждого продавца* (без ban).
Это нижний конкурентный край AH, не lot/uuid p10 (клоны витрины).

Нужно ≥3 sellers. Volume/fat порог: `mkt < 2.5M` = volume.

## Buy / nac

1. Пол softMin / per-SKU / global(fat only).
2. **Потолок nac** важнее abs softMin: volume `buy ≥ 0.70×mkt`, fat `≥ 0.55×mkt`.
3. Жёсткий 300k не душит buy, если mkt низкий (nac режется под ratio).
