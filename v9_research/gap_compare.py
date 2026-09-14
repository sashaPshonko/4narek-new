#!/usr/bin/env python3
"""Compare v9 catchup gap ∈ {0.75..1.0}: OOS WF + under-empty stress."""
from __future__ import annotations

import json
import sys

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S

GAPS = [0.75, 0.80, 0.85, 0.90, 0.95, 1.00]
STEP = 100_000
CYCLE_MIN = 10


def wf_gap(rows, nac, gap, folds=3):
    ts = sorted(set(r["ts"] for r in rows))
    fold_size = len(ts) // (folds + 1)
    out = []
    for f in range(folds):
        t_cut = ts[(f + 1) * fold_size]
        t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
        train = [r for r in rows if r["ts"] < t_cut]
        test = [r for r in rows if t_cut <= r["ts"] < t1]
        dm = S.DemandModel()
        dm.fit(train, nac)
        pol = S.make_policy(
            "v9_core",
            streak_need=2,
            gap=gap,
            min_sales_up=3,
            down_block_ratio=0.90,
        )
        te, _ = S.simulate_panel(S.split_by_item(test), pol, dm)
        out.append(
            {
                "fold": f,
                "profit_m": round(te.get("profit_m", 0), 1),
                "pph_m": round(te.get("profit_m", 0) / max(te["cycles"] / 6.0, 0.1), 2),
                "under": round(te["under_frac"], 3),
                "over": round(te["over_frac"], 3),
                "ups": te["ups"],
                "downs": te["downs"],
                "cycles": te["cycles"],
            }
        )
    return out


def stress_under_empty(gap, price0=500_000, mkt=3_000_000, max_c=80):
    """Pure empty catchup path — how far / how fast."""
    st = S.State(price=price0, held=0)
    for c in range(max_c):
        if st.up_cd > 0:
            st.up_cd -= 1
        obs = {
            "step": STEP,
            "share": 12,
            "sales": 0,
            "buys": 0,
            "mkt": mkt,
            "p10": mkt,
            "night": False,
        }
        # empty streak like Go: decide uses current streak then we update
        act, newp = S.policy_v9_core(
            st, obs, None, streak_need=2, gap=gap, min_sales_up=3, down_block_ratio=0.90
        )
        if act == "UP":
            st.up_cd = 2
        st.price = newp
        st.empty_streak += 1  # still empty idle
        r = st.price / mkt
        # ceiling: ratio >= gap or next step would exceed mkt
        if c >= 5 and act == "HOLD" and (r >= gap - 1e-12 or st.price + STEP > mkt):
            break
    return {
        "final_m": round(st.price / 1e6, 2),
        "final_pct": round(100 * st.price / mkt, 1),
        "min": (c + 1) * CYCLE_MIN,
        "cycles": c + 1,
    }


def stress_over_dump(gap, price0=3_000_000, mkt=1_200_000, max_c=40):
    """Confirm gap doesn't break overstock DOWN path."""
    st = S.State(price=price0, held=6)
    for c in range(max_c):
        if st.up_cd > 0:
            st.up_cd -= 1
        obs = {
            "step": STEP,
            "share": 12,
            "sales": 1,
            "buys": 0,
            "mkt": mkt,
            "p10": mkt,
            "night": False,
        }
        act, newp = S.policy_v9_core(
            st, obs, None, streak_need=2, gap=gap, min_sales_up=3, down_block_ratio=0.90
        )
        st.price = newp
        st.held = 6
        if act == "HOLD" and st.price / mkt < 0.90:
            break
        if c >= 5 and act == "HOLD":
            break
    return {
        "final_m": round(st.price / 1e6, 2),
        "final_pct": round(100 * st.price / mkt, 1),
        "min": (c + 1) * CYCLE_MIN,
    }


def main():
    con = S.connect()
    print("loading…")
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)}")

    summary = {}
    print(f"\n{'gap':5} | fold0 | fold1 | fold2 | mean_profit | mean_p/h | under | over | ups")
    print("-" * 95)
    for gap in GAPS:
        folds = wf_gap(rows, nac, gap)
        mean_p = sum(f["profit_m"] for f in folds) / 3
        mean_ph = sum(f["pph_m"] for f in folds) / 3
        mean_u = sum(f["under"] for f in folds) / 3
        mean_o = sum(f["over"] for f in folds) / 3
        ups = sum(f["ups"] for f in folds)
        stress = stress_under_empty(gap)
        over = stress_over_dump(gap)
        summary[str(gap)] = {
            "folds": folds,
            "mean_profit_m": round(mean_p, 1),
            "mean_pph_m": round(mean_ph, 2),
            "mean_under": round(mean_u, 3),
            "mean_over": round(mean_o, 3),
            "ups_total": ups,
            "stress_under_empty": stress,
            "stress_over_dump": over,
            "delta_vs_080_pct": None,
        }
        profits = " | ".join(f"{f['profit_m']:7.1f}" for f in folds)
        print(
            f"{gap:5.2f} | {profits} | {mean_p:10.1f} | {mean_ph:7.2f} | {mean_u:.3f} | {mean_o:.3f} | {ups:4d}"
        )
        print(
            f"       stress empty→ {stress['final_m']}M ({stress['final_pct']}%) in {stress['min']}m | "
            f"over dump→ {over['final_m']}M ({over['final_pct']}%)"
        )

    base = summary["0.8"]["mean_profit_m"]
    for g, s in summary.items():
        s["delta_vs_080_pct"] = round(100 * (s["mean_profit_m"] - base) / base, 2) if base else 0

    # score: maximize mean profit; tie-break lower under on stress empty closer to 100, then lower over
    ranked = sorted(
        summary.items(),
        key=lambda kv: (
            -kv[1]["mean_profit_m"],
            -kv[1]["stress_under_empty"]["final_pct"],  # closer to market better for buy trap
            kv[1]["mean_over"],
            kv[1]["mean_under"],
        ),
    )
    print("\n=== RANK ===")
    for i, (g, s) in enumerate(ranked, 1):
        print(
            f"  {i}. gap={g}  profit={s['mean_profit_m']}M ({s['delta_vs_080_pct']:+.2f}% vs 0.80)  "
            f"p/h={s['mean_pph_m']}  under={s['mean_under']}  "
            f"empty_end={s['stress_under_empty']['final_pct']}%"
        )

    best = ranked[0][0]
    print(f"\nOPTIMAL (by OOS profit, then escape height): gap={best}")

    out = {"gaps": summary, "rank": [g for g, _ in ranked], "optimal": best}
    path = "/tmp/v9_gap_compare.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"Wrote {path}")


if __name__ == "__main__":
    main()
