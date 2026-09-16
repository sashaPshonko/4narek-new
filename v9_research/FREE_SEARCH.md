# FREE SUPER search

Дата: 2026-09-16  
Скрипт: `search_free.py`  
Baseline: **v9_h1** (`down_block=0.95`, уже в `pricing_v9.go`)

## Что свободно

- **nac×** 0.7–1.5 (маржа × nac_mult + floor min-sell)
- **multi-step** up/down ×1..5
- bands / veto / catchup / cooldowns / market_follow gap-jump
- без `ekb_like`

## Запуск

```bash
cd ~/4narek-new/v9_research
PYTHONUNBUFFERED=1 PRICING_DB=/root/4narek-new/ml_data/pricing.db \
  python3 -u search_free.py 600 300
```

Результаты: `free_search_results.json`. Prod не трогать без dominates/robust + явного ОК.
