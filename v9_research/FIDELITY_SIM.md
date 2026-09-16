# Fidelity sim + hypothesis eval

Дата: 2026-09-16  
Код: `sim_fidelity.py`, `hyp_eval.py`  
Результаты: `hyp_eval_results.json`

## Что улучшили в sim

| Было (SUPER/V10) | Стало |
|---|---|
| sell-median / AH p10 без depth | AH p10 **+ book depth** (thin/ok/thick) |
| 70% logged blend почти всегда | pure model если цена сдвинулась >1 step |
| sales без cap по held | `sales ≤ held` |
| `on_ah`/`inv` из лога или inv=0 | `on_ah=min(held,share)`, `inv=max(0,held−share)` |
| random 1275 chroms | **4–6 осмысленных гипотез** vs v9 |

## Гипотезы

- **H1** / **H1b**: жёстче under DOWN veto (0.95 / 1.01)
- **H2**: catchup к p10, empty_streak=1
- **H3**: strict grant, без soft-↓ over market
- **H4**: чуть ниже inventory band (без aggressive down)
- **H5**: no soft-over + veto 0.95 + выше UP bar

## Запуск

```bash
cd ~/4narek-new/v9_research
PYTHONUNBUFFERED=1 PRICING_DB=/root/4narek-new/ml_data/pricing.db \
  python3 -u hyp_eval.py
```

Prod Go не трогаем, пока нет `robust=true`.
