# book2: hybrid floors + seller-market anchor

**Status:** LIVE

Абсолютная щель `nac = sell − buyMax`. SKU не баним.

## Рыночный якорь (sell)

Не lot/uuid **p10** (раздут клонами витрины: sword7 uuid-p10≈1.5M при seller-edge≈0.75–0.9M).

**sell ≈ p20** среди *минимальных цен каждого продавца* за 30м, без ban-витрин.
Типично ближе к конкурентному краю AH, не к середине клонов.

## Полы

1. **Жёсткий** `300k` (+ JSON softMin).
2. **Per-SKU raise** — K-я щель внутри своей книги.
3. **Global** — только fat (`mkt ≥ 2.5M`). Volume global’ом не поднимаем.
4. **Потолок nac**: volume `buy ≥ 0.70×mkt`; fat `≥ 0.55×mkt`.
