# Slot selectivity / margin boundary (5 AH slots × many SKUs)

**Status:** OPT live — sword buy **0.88** / nac floor **250k** (mega 400k).

**Failed (2026-09-28):** buy 0.85→0.80 + nac 300→400k — АХ опустел (142→13 buys).  
**Rollback patch:** 0.85/300 — восстановил объём, не max profit.  
**Opt (2026-09-29):** page-snapshot + slot-capped book → **0.88/250** (+~29% page Σ vs 0.85/300; FAIL −30%).

**Kept:** loot/sharp SKUs (`sword-sharp5-loot5`, `sharp6-loot5`, `sharp7-loot4`, plain sharp5/6).

## Why not tighten

AH не отсортирован по цене. Узкий buy-gate режет случайные underprice на странице;
5 слотов уже селектируют (берём что проходит). Ужимание 0.80/400 = голод слотов.
Шире 0.88 + пол 250k = больше fills без dump sell.

## Current policy

- buy **0.88×p10**
- nac floor **250k** (megasword **400k**)
- sell **1.00×p10**

Watch 2h: buys/h, free slots, sword7 eff buy/p10. Не жать оба рычага сразу.
