# SUPER SEARCH

Дата старта: 2026-09-15  
Скрипт: `v9_research/search_super.py`  
Результаты: `super_search_results.json` (+ `super_search.log` на VPS)

## Зачем

V10 искал на sim с sell-median «рынком» и без штрафа under → чемпионы с `down×4` / deep under, которые не доверяем.

SUPER:

1. **Рынок** = реальный `ah_book_lots` p10 (окно 10m, n≥40), Sep+ (book era). Fallback sell-median только если book тонкий.
2. **Demand** = bucket E[sales] × **under haircut** (ratio&lt;0.90 режет ожидаемые продажи).
3. **Constraint** vs v9: reject если `under > v9+0.06` или deep_under высокий.
4. **Цель** = max OOS mean 24h profit при constraint; ранг по multi-fold survival → min lift → mean.

## Запуск

```bash
cd ~/4narek-new/v9_research
PYTHONUNBUFFERED=1 PRICING_DB=/root/4narek-new/ml_data/pricing.db \
  SUPER_OUT=./super_search_results.json \
  python3 -u search_super.py 800 400
```

Не трогает prod Go.

## Чтение результата

- `best` / `best_rules` — кандидат в человекочитаемых правилах
- `leaderboard` — chroms, пережившие несколько WF folds
- `fold_reports[].top5` — OOS на каждом окне vs v9

Порог «внедрять»: `min_oos_x_v9 ≥ 1.05` на ≥2 folds **и** under не хуже v9+0.03. Иначе — оставить v9, крутить симулятор дальше.

## Ловушки (уже пойманы)

1. **Val &lt;3 дней** → survivors=0 (fixed WF).
2. **Leaderboard по одному fold** → r513 «×2» (fold1, book 43%) — late half проигрывает v9.
3. **`ekb_like`** → UP-only (logged on_ah или inv=0). Исключён из поиска.
4. **Robust gate:** multi-fold min≥1.0 **и** late-half ≥1.02. Без этого — overfit.
5. Cross-check: `super_crosscheck.json`.

## Вердикт 2026-09-15

На AH-p10 + under-haircut **устойчивого алгоритма лучше v9 не найдено**. Prod остаётся на v9.
