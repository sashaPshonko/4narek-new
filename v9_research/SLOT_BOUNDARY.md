# book2 buyMax from live book (K-th cheapest)

**Status:** LIVE — `book2OptBuyMax`, не фикс 0.85 и не load-тык.

## Идея

sell ≈ p10. При фиксированном sell max Σ(sell−buy) на K слотах =
**K самых дешёвых** лотов с щелью ≥ softMin (JSON nac).

`buyMax` = цена K-го = перцентиль книги `#{p≤buyMax}/n`.

| | |
|--|--|
| K | fair `share` слотов; если `free>0` и меньше share → K=free |
| softMin | `nacenka` из JSON (baseline) |
| fallback | category buyMult если eligible пусто |

## Лог

`bookOpt q=… K=… elig=… buyMax=… (×p10)`
