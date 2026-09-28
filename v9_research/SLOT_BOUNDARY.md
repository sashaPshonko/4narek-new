# Slot selectivity / margin boundary (5 AH slots × many SKUs)

**Status:** OPT live — sword buy **0.88** / nac floor **300k** (mega 400k).

**Failed (2026-09-28):** buy 0.85→0.80 + nac 300→400k — АХ опустел (142→13 buys).  
**Rollback patch:** 0.85/300 — восстановил объём.  
**Opt (2026-09-29):** buy **0.88**, nac пол **остаётся 300k**. Пол 250k отменили: при забитом АХ ниже пол только режет щель, объём уже на потолке.

**Kept:** loot/sharp SKUs (`sword-sharp5-loot5`, `sharp6-loot5`, `sharp7-loot4`, plain sharp5/6).

## Why

AH time-unsorted → чуть шире buy ловит underprice на странице.  
Пол nac при полном АХ = селективность по щели; опускать смысла нет.

## Current policy

- buy **0.88×p10**
- nac floor **300k** (megasword **400k**)
- sell **1.00×p10**

Watch 2h: buys/h, free slots, unit margin.
