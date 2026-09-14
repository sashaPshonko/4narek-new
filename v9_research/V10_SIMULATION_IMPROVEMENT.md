# V10 Simulation Improvement

Дата: 2026-09-14  
**Production не менялся.** Изменения только research.

---

## Что сделано (research-only)

1. **Аудит** текущего симулятора → `V10_SIMULATION_AUDIT.md`  
2. **Инвентарь БД** → `MARKET_DATA_INVENTORY.md`  
3. **Robust harness** → `v10_robust_validation.py`:
   - observational elasticity + event-study ΔP→Δsales;
   - under_frac vs profit scan;
   - 4 demand models (A current / B kNN / C empirical / D OLS);
   - жёстче walk-forward folds;
   - V10 vs v9 / old corridor / HOLD;
   - ablation, bootstrap по дням, SKU leave-one-out;
   - проверка покрытия AH book.

4. **Явный запрет** `nac_mult≠1` в validation (как в честном V10 search).

---

## Что сознательно НЕ сделано (ещё)

| Идея | Почему не в этом прогоне |
|------|---------------------------|
| Полный AH p10 в каждом цикле Jul–Sep | Book только с 2026-09-03 |
| Model E full market replay | Нет AH за Jul–Aug |
| Gradient boosting demand | OLS/kNN сначала; GB — следующий шаг если нужно |
| Исправление production Demand | Вне scope |

---

## Улучшения realism vs V10 search

| Было в V10 search | Теперь в robust |
|-------------------|-----------------|
| 1 DemandModel | 4 независимых |
| 2 OOS folds (1 thin skip) | до 4 expanding folds |
| Только ×v9 | + old corridor + HOLD |
| Один коэффициент 1.47× | bootstrap CI, P(win), SKU LOO |
| Не проверяли under↔profit | corr + scatter points |
| Не было event-study | before/after sales rate по горизонтам |

---

## Рекомендуемые следующие улучшения simulator (не сделаны)

1. Sep-only panel с **настоящим** `ahBookP10` + lots/sellers.  
2. Event-study matched controls (same held/liquidity).  
3. Demand с штрафом маржи / buy-capacity при deep under.  
4. Отключить или ослабить 70% logged blend при оценке counterfactual политик.
