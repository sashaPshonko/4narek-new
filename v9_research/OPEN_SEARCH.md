# OPEN hyp search (nac + non-step jumps)

Дата: 2026-09-17  
Скрипт: `hyp_eval_open.py`  
Результаты: `hyp_open_results.json` (VPS `v9_research/`)  
Baseline: **v9_h1** (`down_block=0.95`)

## Политика поиска

Сняты запреты «как принято»:

- **nac_mult** 0.7–1.5 — пробуем, если эффективнее
- **прыжки не только ±price_step**: `market_follow` тянет цену к AH p10 на долю `mf_pull` за цикл
- multi-step / `step_mult` тоже в сетке

Отчёт честный: `late_x` (raw) **и** `margin_adj_x = late_profit / nac_mult / v9` — чтобы видеть чистую маржу vs алгоритм.

## Запуск

```bash
cd ~/4narek-new/v9_research
PYTHONUNBUFFERED=1 PRICING_DB=/root/4narek-new/ml_data/pricing.db \
  python3 -u hyp_eval_open.py
```

## Результат 2026-09-17

| Кандидат | late× | margin_adj× | under | заметка |
|---|---:|---:|---:|---|
| **MF mf_pull=1, nac=1** | **1.34** | **1.34** | 0.01 | чистый алгоритм, dominates |
| OPEN nac1.5 + MF | 2.21 | 1.47 | 0.005 | маржа + алгоритм |
| nac1.5 corridor only | 1.77 | 1.18 | 0.11 | больше маржа, under хуже MF |
| step_mult / catchup alone | ~1.0–1.05 | ~1.0 | ~0.13–0.15 | слабо |

**Вывод:** эффективнее всего **сдвиг к рынку (p10)**, не фиксированный step. Подъём наценки sim тоже любит — но это уже смена экономики лота, не только «умнее ходить».

**В prod пока не трогаем.** Дальше: `full_compare_all` на MF chrom, потом решение по Go (и отдельно — трогать ли nac).
