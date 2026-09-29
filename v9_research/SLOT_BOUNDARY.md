# book2: sell p40 / nac≥300k / buy ≥ p10

**Status:** LIVE

## Якоря (per-seller min, без ban)

| | |
|---|---|
| **sell** | `max(seller-p40, buyEdge+nac)` |
| **nac** | **≥ 300k** (softMin) — жёстко, не режем |
| **buyMax** | ≥ seller-p10 |

Если p40 мало для 300k над buyEdge — **поднимаем sell**, не режем наценку.
