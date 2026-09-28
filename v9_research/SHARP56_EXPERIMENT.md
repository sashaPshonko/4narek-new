# Experiment: sword sharpness 5 / 6 as separate SKUs

**Start:** 2026-09-28T20:00Z (deploy `0b7dba30`)  
**Horizon:** 24–48h, then decide keep / tune / kill  
**Do not change** global book2 sword mults (buy 0.85 / sell 1.00) during phase 1.

## Why

Book pricing is per `item_id`. Sharp 5/6 never matched (only sharp≥7 kits) → zero book, zero buys.  
Same `netherite_sword-1.21` fleet → compete for 5 AH slots with sharp7. Hypothesis: their book p10 is lower and/or buy_r deeper → selective fill can raise effective margin without touching sword7 policy.

## What we added

| id | match | max sharp | cold base | nac floor |
|----|-------|-----------|-----------|-----------|
| `sword-sharp5-1.21` | sharpness ≥5 | 5 | 400015 | 300k |
| `sword-sharp6-1.21` | sharpness ≥6 | 6 | 550016 | 300k |

No other required enchants (any kit with that sharp level). sharp7 still hits `sword7` / mega / farm via their own effects+max.

## Phases

### Phase 1 — observe (24h+)

1. Deploy Go (`items_config`) + farm restart so workers get new catalog.
2. Do **not** retune buy/sell mults.
3. Every ~6–12h run:

```bash
python3 v9_research/sharp56_experiment.py
# or on VPS: python3 /root/4narek-new/v9_research/sharp56_experiment.py
```

**Go / kill criteria (phase 1):**

| signal | keep looking | kill SKU |
|--------|----------------|----------|
| book lots / 6h | ≥30 unique uuid | &lt;5 for 24h |
| thick book (30m n≥40) hours | ≥1 in 24h | never |
| buys | any is OK | — |
| slot steal | sharp7 try-sell / sales not collapsed | sharp7 sales ≪ baseline −30% with no sharp56 profit |

### Phase 2 — only if book thick

Compare FIFO buy_r / sell_r / hold / Σ vs `sword7-1.21` same window.  
If sharp56 buy_r med ≪ 0.85 and sells move: try **tighter** buy (e.g. 0.80) **only on these ids** (not global), or higher nac floor.  
If book thick but holds ≫ sword7: lower sell mult (e.g. 0.95) only on these ids — or kill.

## Metrics (script)

- `ah_book_lots`: n lots, n uuid, p10/p50 for sharp5, sharp6, sword7
- `trade_events`: buys/sells/try-sell, med buy, med sell
- slot pressure proxy: try-sell share sharp56 vs sword7

## Rollback

Remove the two keys from `items_config.json`, restart Go. In-flight AH lots sell out or unlist manually; no DB migration.
