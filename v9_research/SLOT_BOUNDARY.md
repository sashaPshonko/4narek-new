# Slot selectivity / margin boundary (5 AH slots × many SKUs)

**Status:** ROLLED BACK 2026-09-28T20:37Z — АХ опустел.

**Failed levers:** sword buy 0.85→0.80 + nac floor 300→400k.  
**Kept:** loot/sharp SKUs (`sword-sharp5-loot5`, `sharp6-loot5`, `sharp7-loot4`, plain sharp5/6).

## What happened

| window | buys (swords) |
|--------|----------------|
| 3h before cut | 142 |
| ~17m after cut | 13 |

На sword7 (p10~1.26M) пол 400k → buyMax≈0.68×p10. Книга: under 0.68 ~166 uuid/h vs under old~0.76 ~291 — мало для заполнения слотов + top-2 buy zone.

## Current policy (after rollback)

- buy **0.85×p10**
- nac floor **300k** (megasword 400k as before)
- sell **1.00×p10**

Next probe (if any): **one knob only** — either buy 0.82 **or** nac 350k, not both. Watch buys/h and free slots 2h before next step.
