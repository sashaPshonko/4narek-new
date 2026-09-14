#!/usr/bin/env python3
"""Per price-change instrument efficacy from capital_cycles (observational).

Instruments = action strings that move price (up/down) vs HOLD baseline.
Metric: mean (fwd1+fwd2+fwd3) in M, Δ vs global HOLD mean, and matched Δ
(same item, ±2h window hold neighbors — optional coarse).
"""
from __future__ import annotations

import json
import sqlite3
from collections import defaultdict

DB = "/root/4narek-new/ml_data/pricing.db"
T0, T1 = "2026-07-15", "2026-09-14"

# Bucket action → instrument family
BUCKETS = [
    ("DOWN dump", lambda a: "price_down_dump" in a or a.endswith("_dump")),
    ("DOWN over", lambda a: "price_down_over" in a),
    ("DOWN soft/hi", lambda a: "price_down_soft" in a or "price_down_hi" in a),
    ("DOWN empty_fair / soft_idle", lambda a: "price_down_empty" in a or "empty_idle" in a and "down" in a),
    ("DOWN stale", lambda a: "price_down_stale" in a),
    ("DOWN doi_cover", lambda a: "doi" in a and "down" in a),
    ("UP demand / weak_demand path", lambda a: "price_up_demand" in a),
    ("UP deep (low stock)", lambda a: "price_up_deep" in a and "recover" not in a and "floor" not in a),
    ("UP recover / recover_deep", lambda a: "price_up_recover" in a),
    ("UP skim", lambda a: "price_up_skim" in a),
    ("UP empty_idle", lambda a: "price_up_empty_idle" in a),
    ("UP empty_inventory / catchup", lambda a: "empty_catchup" in a or "empty_inventory" in a or "empty_market" in a),
    ("UP floor / floor_escape", lambda a: "floor" in a and "up" in a),
    ("UP ah_book / empty_book", lambda a: "ah_book" in a or "empty_book" in a),
    ("UP market_recovery", lambda a: "market_recovery" in a),
    ("UP paid / trusted_min", lambda a: "price_up_paid" in a or "trusted" in a),
    ("HOLD (all holds)", lambda a: "hold" in a or a == "hold"),
]


def bucket(action: str) -> str:
    a = action.lower()
    for name, fn in BUCKETS:
        try:
            if fn(a):
                return name
        except Exception:
            pass
    if "price_up" in a:
        return "UP other"
    if "price_down" in a:
        return "DOWN other"
    return "OTHER"


def main():
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row

    rows = list(
        con.execute(
            """
        SELECT action, item_id, held, sales, buys, fill, stock_load,
               COALESCE(profit_now,0) AS profit_now,
               COALESCE(fwd_profit_1,0) AS f1,
               COALESCE(fwd_profit_2,0) AS f2,
               COALESCE(fwd_profit_3,0) AS f3
        FROM capital_cycles
        WHERE ts>=? AND ts<? AND fwd_profit_1 IS NOT NULL
        """,
            (T0, T1),
        )
    )
    print(f"cycles={len(rows)}")

    # HOLD baseline
    holds = [r for r in rows if "price_up" not in r["action"] and "price_down" not in r["action"]]
    hold_fwd = sum(r["f1"] + r["f2"] + r["f3"] for r in holds) / max(len(holds), 1)
    print(f"HOLD n={len(holds)} mean_fwd3={hold_fwd/1e6:.4f}M")

    # Per exact action
    by_act = defaultdict(list)
    for r in rows:
        by_act[r["action"]].append(r)

    act_stats = []
    for act, rs in by_act.items():
        if len(rs) < 20:
            continue
        fwd = sum(x["f1"] + x["f2"] + x["f3"] for x in rs) / len(rs)
        now = sum(x["profit_now"] for x in rs) / len(rs)
        moves = "up" if "price_up" in act else ("down" if "price_down" in act else "hold")
        act_stats.append(
            {
                "action": act,
                "bucket": bucket(act),
                "move": moves,
                "n": len(rs),
                "fwd3_m": round(fwd / 1e6, 4),
                "now_m": round(now / 1e6, 4),
                "delta_vs_hold_m": round((fwd - hold_fwd) / 1e6, 4),
                "avg_held": round(sum(x["held"] for x in rs) / len(rs), 2),
                "avg_fill": round(sum(x["fill"] or 0 for x in rs) / len(rs), 3),
                "avg_sales": round(sum(x["sales"] for x in rs) / len(rs), 2),
            }
        )
    act_stats.sort(key=lambda x: -x["n"])

    # Per bucket (instruments)
    by_b = defaultdict(list)
    for r in rows:
        by_b[bucket(r["action"])].append(r)

    bucket_stats = []
    for b, rs in by_b.items():
        if len(rs) < 15:
            continue
        fwd = sum(x["f1"] + x["f2"] + x["f3"] for x in rs) / len(rs)
        now = sum(x["profit_now"] for x in rs) / len(rs)
        ups = sum(1 for x in rs if "price_up" in x["action"])
        dns = sum(1 for x in rs if "price_down" in x["action"])
        # verdict vs HOLD
        d = (fwd - hold_fwd) / 1e6
        if abs(d) < 0.05:
            verdict = "NEUTRAL"
        elif d >= 0.5:
            verdict = "STRONG+"
        elif d >= 0.05:
            verdict = "WEAK+"
        elif d <= -0.5:
            verdict = "STRONG-"
        else:
            verdict = "WEAK-"
        bucket_stats.append(
            {
                "instrument": b,
                "n": len(rs),
                "fwd3_m": round(fwd / 1e6, 4),
                "delta_vs_hold_m": round(d, 4),
                "delta_pct_vs_hold": round(100 * (fwd - hold_fwd) / abs(hold_fwd) if hold_fwd else 0, 1),
                "now_m": round(now / 1e6, 4),
                "avg_held": round(sum(x["held"] for x in rs) / len(rs), 2),
                "avg_fill": round(sum(x["fill"] or 0 for x in rs) / len(rs), 3),
                "avg_sales": round(sum(x["sales"] for x in rs) / len(rs), 2),
                "ups": ups,
                "downs": dns,
                "verdict": verdict,
            }
        )
    bucket_stats.sort(key=lambda x: -x["delta_vs_hold_m"])

    # Conditioned: DOWN by held bucket (empty / under / band / over / dump)
    down_rows = [r for r in rows if "price_down" in r["action"]]
    held_buckets = [("empty", lambda h, f: h == 0), ("under_fill<0.15", lambda h, f: h > 0 and (f or 0) < 0.15),
                    ("band_0.15-0.28", lambda h, f: 0.15 <= (f or 0) < 0.28),
                    ("over_0.28-0.45", lambda h, f: 0.28 <= (f or 0) < 0.45),
                    ("dump>=0.45", lambda h, f: (f or 0) >= 0.45)]
    down_cond = []
    for name, fn in held_buckets:
        rs = [r for r in down_rows if fn(r["held"], r["fill"])]
        if len(rs) < 20:
            continue
        fwd = sum(x["f1"] + x["f2"] + x["f3"] for x in rs) / len(rs)
        down_cond.append(
            {
                "when": f"DOWN @ {name}",
                "n": len(rs),
                "fwd3_m": round(fwd / 1e6, 4),
                "delta_vs_hold_m": round((fwd - hold_fwd) / 1e6, 4),
            }
        )

    up_rows = [r for r in rows if "price_up" in r["action"]]
    up_cond_defs = [
        ("held=0", lambda r: r["held"] == 0),
        ("held>0 sales>=3", lambda r: r["held"] > 0 and r["sales"] >= 3),
        ("held>0 sales<3", lambda r: r["held"] > 0 and r["sales"] < 3),
        ("sales=0 buys=0", lambda r: r["sales"] == 0 and r["buys"] == 0),
    ]
    up_cond = []
    for name, fn in up_cond_defs:
        rs = [r for r in up_rows if fn(r)]
        if len(rs) < 20:
            continue
        fwd = sum(x["f1"] + x["f2"] + x["f3"] for x in rs) / len(rs)
        up_cond.append(
            {
                "when": f"UP @ {name}",
                "n": len(rs),
                "fwd3_m": round(fwd / 1e6, 4),
                "delta_vs_hold_m": round((fwd - hold_fwd) / 1e6, 4),
            }
        )

    print("\n=== INSTRUMENTS (bucket) ranked by Δfwd3 vs HOLD ===")
    print(f"{'instrument':40} {'n':>6} {'fwd3':>8} {'Δhold':>8} {'%':>7} {'verdict':>8}")
    for s in bucket_stats:
        print(
            f"{s['instrument'][:40]:40} {s['n']:6} {s['fwd3_m']:8.3f} {s['delta_vs_hold_m']:+8.3f} "
            f"{s['delta_pct_vs_hold']:+6.1f}% {s['verdict']:>8}"
        )

    print("\n=== DOWN conditioned ===")
    for s in down_cond:
        print(f"  {s['when']:30} n={s['n']:5} fwd3={s['fwd3_m']:7.3f} Δ={s['delta_vs_hold_m']:+7.3f}")

    print("\n=== UP conditioned ===")
    for s in up_cond:
        print(f"  {s['when']:30} n={s['n']:5} fwd3={s['fwd3_m']:7.3f} Δ={s['delta_vs_hold_m']:+7.3f}")

    print("\n=== Top exact actions (n≥50) by Δ vs HOLD ===")
    movers = [a for a in act_stats if a["move"] != "hold" and a["n"] >= 50]
    movers.sort(key=lambda x: -x["delta_vs_hold_m"])
    for a in movers[:25]:
        print(f"  {a['action'][:50]:50} n={a['n']:5} Δ={a['delta_vs_hold_m']:+7.3f} fwd={a['fwd3_m']:7.3f}")
    print("  --- worst ---")
    for a in movers[-15:]:
        print(f"  {a['action'][:50]:50} n={a['n']:5} Δ={a['delta_vs_hold_m']:+7.3f} fwd={a['fwd3_m']:7.3f}")

    out = {
        "hold_fwd3_m": round(hold_fwd / 1e6, 4),
        "hold_n": len(holds),
        "buckets": bucket_stats,
        "down_conditioned": down_cond,
        "up_conditioned": up_cond,
        "actions": act_stats,
        "notes": [
            "Observational: selection bias (DOWN when excess already good)",
            "fwd3 = sum next 3 cycle profits logged",
            "Δ vs mean HOLD across all hold actions",
        ],
    }
    path = "/tmp/v9_instrument_efficacy.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {path}")


if __name__ == "__main__":
    main()
