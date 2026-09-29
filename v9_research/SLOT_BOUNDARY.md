# book2: hybrid floors (per-SKU + fat-only global)

**Status:** LIVE

Абсолютная щель `nac = sell − buyMax`. SKU не баним.

## Полы

1. **Жёсткий** `300k` (+ JSON softMin).
2. **Per-SKU raise** — K-я щель внутри своей книги (`K = clamp(n/3, 3..12)`).
3. **Global** — общий порог из книги, но **только на fat** (`p10 ≥ 1.5M`).
   Volume (sword7/фарм) global’ом не поднимаем: «лоты с щелью ≥G» ≠ «купим на time-sorted AH».
4. **Потолок nac**: volume `buy ≥ 0.70×p10` (nac ≤ 0.30×p10); fat `buy ≥ 0.55×p10`.

Иначе снова 800–910k на всех → buy 0.2–0.4×p10 и пустые слоты.
