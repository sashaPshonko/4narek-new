# ULTRA FREE search

Дата: 2026-09-17  
Скрипт: `search_ultra.py`  
Результаты: `ultra_search_results.json`

## Свобода

- **шаги цены** не фиксированы: `step_mult`, `up/down_mult`, `mf_pull` (прыжок к AH p10)
- **наценка** не фиксирована: `nac_mult` + динамика `nac_dyn=fixed|inv_scale|gap_scale`
- bands / catchup / veto / CD — всё в геноме

## Метод

Многопоколенческий evolutionary search (elite + crossover + mutate + fresh) на fidelity AH-p10 sim.

Метрики:

- `late_raw_x` — сырой profit vs v9_h1  
- `late_adj_x` — profit / mean_nac vs v9 (честный алгоритм без «просто выше nac»)

## Запуск

```bash
cd ~/4narek-new/v9_research
PYTHONUNBUFFERED=1 PRICING_DB=/root/4narek-new/ml_data/pricing.db \
  python3 -u search_ultra.py 150 10 30
# args: pop gens elite
```

Prod Go **не** трогать без dominates_adj + full_compare_all + явного ОК.
