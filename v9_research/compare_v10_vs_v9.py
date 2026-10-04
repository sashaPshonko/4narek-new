#!/usr/bin/env python3
"""OOS compare: v10 rails (± CF grey models) vs v9 and classic baselines.

Usage:
  .venv/bin/python compare_v10_vs_v9.py
"""
from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

import sim_v9 as S
import full_compare_all as FC
import policy_v10_rails as V10
from v10_cf_scorers import CFScorers

ROOT = Path(__file__).resolve().parent
DB = Path("/Users/sasha_pshonko/Documents/4narek/4narek-ml/data/pricing.db")
OUT = ROOT / "results_v10_vs_v9.json"

FOCUS = list(dict.fromkeys(S.FOCUS_ITEMS + ["sword-sharp5-1.21", "sword-sharp6-1.21"]))


def main():
    S.DB = str(DB)
    scorers = CFScorers()  # ↓ tau floored at 0.55
    scorers_cal = CFScorers(down_tau_floor=0.0)  # raw calibrated (↓≈0.3)

    policies = [
        ("ekb", FC.pol_ekb),
        ("classic_ns", FC.pol_classic_normalsales),
        ("corridor_v4", FC.pol_corridor_v4),
        ("corridor_v5", FC.pol_corridor_v5),
        ("corridor_v6", FC.pol_corridor_v6),
        ("v8_late", FC.pol_v8_late),
        ("v9", FC.pol_v9),
        ("hold", FC.pol_hold),
        ("v10_rails", V10.make_sim_policy(scorers=None)),
        ("v10_cf", V10.make_sim_policy(scorers=scorers)),
        ("v10_cf_cal", V10.make_sim_policy(scorers=scorers_cal)),
    ]

    con = S.connect()
    print("loading panel…", FOCUS)
    q = f"""
    SELECT ts, policy, item_id, action, held, sales, buys, try_sells,
           price_before, price_after, step, share, stock_load,
           COALESCE(profit_now,0) AS profit_now,
           nacenka_before, cycle_minutes,
           on_ah, inv, normal_sales
    FROM capital_cycles
    WHERE ts>='2026-07-15' AND ts<'2026-09-03'
      AND item_id IN ({','.join('?'*len(FOCUS))})
    ORDER BY item_id, ts
    """
    rows = [dict(r) for r in con.execute(q, FOCUS)]
    print(f"cycles={len(rows)}")
    try:
        rows = S.attach_p10_fast(con, rows)
    except Exception as e:
        print("attach_p10_fast failed:", e)
        for r in rows:
            r["mkt"] = r.get("p10") or r["price_before"]
    nac = S.avg_nacenka(con, FOCUS)
    con.close()

    ts = sorted(set(r["ts"] for r in rows))
    fold_size = max(1, len(ts) // 4)

    print(f"\n{'model':14} | " + " | ".join(f"fold{f}" for f in range(3)) + " | mean$ | p/h | under")
    print("-" * 110)

    summary = {}
    for name, fn in policies:
        fold_stats = []
        for f in range(3):
            t_cut = ts[(f + 1) * fold_size]
            t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
            train = [r for r in rows if r["ts"] < t_cut]
            test = [r for r in rows if t_cut <= r["ts"] < t1]
            dm = S.DemandModel()
            dm.fit(train, nac)
            for r in test:
                r["on_ah"] = r.get("on_ah") if r.get("on_ah") is not None else r["held"]
                r["inv"] = r.get("inv") or 0
            te = FC.simulate(S.split_by_item(test), fn, dm)
            fold_stats.append(te)
        mean_pph = sum(x["pph_m"] for x in fold_stats) / 3
        mean_under = sum(x["under_frac"] for x in fold_stats) / 3
        mean_profit = sum(x["profit_m"] for x in fold_stats) / 3
        summary[name] = {
            "folds": fold_stats,
            "mean_profit_m": round(mean_profit, 1),
            "mean_pph_m": round(mean_pph, 2),
            "mean_under": round(mean_under, 3),
        }
        profits = " | ".join(f"{x['profit_m']:8.1f}" for x in fold_stats)
        print(f"{name:14} | {profits} | {mean_profit:7.1f} | {mean_pph:5.2f} | {mean_under:.3f}")

    v9p = summary["v9"]["mean_profit_m"] or 1.0
    v9ph = summary["v9"]["mean_pph_m"] or 1.0
    ranked = sorted(summary.items(), key=lambda kv: -kv[1]["mean_profit_m"])
    print("\n=== RANK by mean OOS profit (× vs v9) ===")
    for i, (name, s) in enumerate(ranked, 1):
        x = s["mean_profit_m"] / v9p
        xph = s["mean_pph_m"] / v9ph
        mark = " ←" if name == "v9" else ""
        print(
            f"  {i:2}. {name:14} profit={s['mean_profit_m']:8.1f}M ({x:+.1%} vs v9)  "
            f"p/h={s['mean_pph_m']:6.2f} ({xph:+.1%})  under={s['mean_under']:.3f}{mark}"
        )

    pct_vs = {}
    for name, s in summary.items():
        pct_vs[name] = {
            "vs_v9_profit_pct": round((s["mean_profit_m"] / v9p - 1) * 100, 2),
            "vs_v9_pph_pct": round((s["mean_pph_m"] / v9ph - 1) * 100, 2),
        }

    out = {
        "generated": datetime.now(timezone.utc).isoformat(),
        "db": str(DB),
        "focus": FOCUS,
        "window": "2026-07-15 .. 2026-09-03",
        "cf_taus": {
            "v10_cf": {"down": scorers.down_tau, "up": scorers.up_tau, "cal_down": scorers.cal_down_tau},
            "v10_cf_cal": {"down": scorers_cal.down_tau, "up": scorers_cal.up_tau},
        },
        "summary": summary,
        "rank": [n for n, _ in ranked],
        "pct_vs_v9": pct_vs,
        "notes": [
            "v10_rails = floor/mid synth 0.75–1.05×mkt + sell-through rules, no CF",
            "v10_cf = rails + grey CF (↓ tau floored ≥0.55, ↑ calibrated)",
            "v10_cf_cal = rails + CF raw calibrated taus (↓≈0.3)",
            "Relative ranking under DemandModel — not absolute cash",
        ],
    }
    OUT.write_text(json.dumps(out, indent=2, ensure_ascii=False))
    print("wrote", OUT)


if __name__ == "__main__":
    main()
