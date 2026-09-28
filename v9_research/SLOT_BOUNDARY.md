# Slot selectivity / margin boundary (5 AH slots × many SKUs)

**Start:** 2026-09-28T20:20Z approx  
**Levers:** sword book2 buy **0.85→0.80**; config nac floor **300→400k** (bare unchanged).  
**New SKUs:** `sword-sharp5-loot5`, `sword-sharp6-loot5`, `sword-sharp7-loot4`.

## Why

With 5 slots/bot, extra SKUs don’t dilute p10 — they compete for slots.  
Book supply (6h med lots/h under gate, all swords):

| buy ≤ | lots/h |
|------|--------|
| 0.70×p10 | ~58 |
| 0.75 | ~82 |
| 0.80 | ~120 |
| 0.85 | ~240 |

Need ~50 fills/h if hold~0.4h and 20 slots. **Граница голода ~0.70**.  
300k floor on p10=1.2M already ≈ buy 0.75. **400k ≈ buy 0.67** — зонд у края.  
На mega (p10~3M) % от 0.80 даёт nac 600k > пола — там ужесточение именно mult.

Дешёвые sharp5 (p10~0.3M): пол 400k > щели → почти не берём. Категории **не трогаем** (книга копится).

## Watch 24–48h

```bash
python3 v9_research/sharp56_experiment.py   # + новые id в IDS при желании
# вручную: free slots / try-sell, med buy/p10, Σ
```

| сигнал | ок | откат |
|--------|----|-------|
| AH часто полный | да | — |
| AH часто пустой / idle | пережали | buy 0.80→0.85 или nac 400→300 |
| med buy/p10 упал, sales≈ | граница найдена | зафиксировать |
| sales ≪ baseline −30% | плохо | откат |

Откат: `Buy: 0.85`, nac 300k в `items_config` на мечах.
