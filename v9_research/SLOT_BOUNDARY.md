# Slot selectivity / margin boundary (5 AH slots × many SKUs)

**Status:** LIVE probe — buy **0.85** / nac floor **350k** (mega 400k).

## What we learned

Global buy×nac при **забитом АХ** — плато: cherry-pick top-N даёт **±0.3%** между
0.85/300 и 0.88/350. Шире buy не opt (разжижает unit). Уже 0.80+400k = голод.

Реальный рычаг при полном АХ — **селективность щели** (выше пол nac), один knob,
пока free slots ≈ 0. Не оба рычага сразу.

| lever | result |
|-------|--------|
| 0.80 + 400k | FAIL — АХ пустой |
| 0.88 + 300/250 | не opt при полном АХ |
| **0.85 + 350k** | probe: +unit, supply ещё ~76/h vs cap~25 |

## Current

- buy **0.85×p10**
- nac **350k** (megasword **400k**, bare 30k)
- sell **1.00×p10**

Watch 2h: buys/h, free slots. Если пустеет → откат nac 300k. Если всё ещё битком
и unit ок → следующий шаг nac 380 (всё ещё без смены buy).
