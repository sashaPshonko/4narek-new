#!/usr/bin/env python3
"""Is gap=1.0 / until_buy really ideal? Dense sweep + fold wins + stress."""
from __future__ import annotations

import json
import sys

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S

STEP = 100_000
CYCLE_MIN = 10
NAC = 200_000

# candidates: gap on v9_core, plus special modes
GAPS = [0.80, 0.85, 0.90, 0.95, 0.98, 1.00, 1.02, 1.05]


def wf_v9(rows, nac, gap, folds=3):
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
        # night flag
        for r in test:
            try:
                h = int(r["ts"][11:13])
            except Exception:
                h = 12
            r["night"] = 0 <= h < 6
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
                "pph": round(te.get("profit_m", 0) / max(te["cycles"] / 6.0, 0.1), 3),
                "under": round(te["under_frac"], 4),
                "over": round(te["over_frac"], 4),
                "ups": te["ups"],
                "downs": te["downs"],
                "cycles": te["cycles"],
            }
        )
    return out


def stress_empty(gap, p0=500_000, mkt=3_000_000, max_c=120):
    st = S.State(price=p0, held=0)
    ups = 0
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
        act, newp = S.policy_v9_core(
            st, obs, None, streak_need=2, gap=gap, min_sales_up=3, down_block_ratio=0.90
        )
        if act == "UP":
            ups += 1
            st.up_cd = 2
        st.price = newp
        st.empty_streak += 1
        r = st.price / mkt
        if c >= 8 and act == "HOLD" and st.up_cd == 0:
            if r >= gap - 1e-12 or st.price + STEP > mkt:
                # for gap>1.0, stop when price+step > mkt still binds in policy
                if gap <= 1.0 or st.price + STEP > mkt:
                    if gap <= 1.0 or r >= 1.0:
                        break
            if gap > 1.0 and st.price >= mkt * min(gap, 1.05) - 1:
                # policy allows ratio < gap and price+step <= mkt — so max is still mkt
                break
        if st.price + STEP > mkt and act == "HOLD":
            break
    can_buy = st.price >= mkt  # buy_max >= mkt - nac
    return {
        "final_m": round(st.price / 1e6, 2),
        "pct": round(100 * st.price / mkt, 1),
        "can_buy": can_buy,
        "min": (c + 1) * CYCLE_MIN,
        "ups": ups,
    }


def main():
    con = S.connect()
    print("loading…")
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)}")

    results = {}
    print(f"\n{'gap':5} | {'f0':>8} {'f1':>8} {'f2':>8} | mean | Δ1.00 | under | over | can_buy | empty%")
    print("-" * 100)

    for gap in GAPS:
        folds = wf_v9(rows, nac, gap)
        mean_p = sum(x["profit_m"] for x in folds) / 3
        mean_u = sum(x["under"] for x in folds) / 3
        mean_o = sum(x["over"] for x in folds) / 3
        st = stress_empty(gap)
        results[str(gap)] = {
            "folds": folds,
            "mean_profit_m": round(mean_p, 1),
            "mean_under": round(mean_u, 4),
            "mean_over": round(mean_o, 4),
            "stress": st,
            "fold_profits": [x["profit_m"] for x in folds],
        }
        # fill delta later
        print(
            f"{gap:5.2f} | "
            + " ".join(f"{x['profit_m']:8.1f}" for x in folds)
            + f" | {mean_p:7.1f} | {'':6} | {mean_u:.3f} | {mean_o:.3f} | "
            f"{str(st['can_buy']):5} | {st['pct']:5.1f}%"
        )

    base = results["1.0"]["mean_profit_m"]
    for g, s in results.items():
        s["delta_vs_100_pct"] = round(100 * (s["mean_profit_m"] - base) / base, 3)

    # fold wins vs 1.0
    print("\n=== Fold wins vs gap=1.00 ===")
    p100 = results["1.0"]["fold_profits"]
    for g, s in results.items():
        wins = sum(1 for a, b in zip(s["fold_profits"], p100) if a > b + 0.05)
        loss = sum(1 for a, b in zip(s["fold_profits"], p100) if a < b - 0.05)
        print(
            f"  gap={g:>4}: mean {s['mean_profit_m']:8.1f}M ({s['delta_vs_100_pct']:+.3f}% vs 1.0)  "
            f"folds beat 1.0: {wins}/3  lose: {loss}/3  "
            f"empty→{s['stress']['pct']}% can_buy={s['stress']['can_buy']}"
        )

    # ranking: primary can_buy on stress, then mean profit, then lower over, then lower under
    ranked = sorted(
        results.items(),
        key=lambda kv: (
            0 if kv[1]["stress"]["can_buy"] else 1,
            -kv[1]["mean_profit_m"],
            kv[1]["mean_over"],
            kv[1]["mean_under"],
            abs(kv[1]["stress"]["pct"] - 100),
        ),
    )
    print("\n=== RANK (can_buy first, then OOS profit) ===")
    for i, (g, s) in enumerate(ranked, 1):
        mark = " ←" if g == "1.0" else ""
        print(
            f"  {i}. gap={g}  profit={s['mean_profit_m']}M  "
            f"can_buy={s['stress']['can_buy']}  empty={s['stress']['pct']}%  "
            f"over={s['mean_over']} under={s['mean_under']}{mark}"
        )

    best = ranked[0][0]
    # is 1.0 ideal?
    best_profit = max(results.items(), key=lambda kv: kv[1]["mean_profit_m"])
    can_buy_gaps = [g for g, s in results.items() if s["stress"]["can_buy"]]
    print("\n=== CONCLUSION ===")
    print(f"Best OOS profit alone: gap={best_profit[0]} ({best_profit[1]['mean_profit_m']}M)")
    print(f"Gaps that restore can_buy on empty under: {can_buy_gaps}")
    print(f"Rank#1 with can_buy priority: gap={best}")
    if best == "1.0":
        print("gap=1.0 IS optimal under (can_buy + profit) criteria.")
    else:
        d = results["1.0"]["mean_profit_m"] - results[best]["mean_profit_m"]
        print(
            f"gap=1.0 is NOT rank#1; gap={best} wins. "
            f"1.0 vs that: {results['1.0']['mean_profit_m']} vs {results[best]['mean_profit_m']} ({d:+.1f}M)"
        )
    # note on gap>1: policy still has price+step<=mkt so cannot exceed mkt
    print(
        "Note: v9_core always has price+step<=mkt on catchup, so gap>1.0 ≈ gap=1.0 for empty climb."
    )

    out = {
        "results": results,
        "rank": [g for g, _ in ranked],
        "best_by_can_buy_then_profit": best,
        "best_oos_profit": best_profit[0],
        "ideal_claim_1_0": best == "1.0" or results["1.0"]["mean_profit_m"] >= best_profit[1]["mean_profit_m"] - 20,
    }
    path = "/tmp/v9_gap_ideal_check.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"Wrote {path}")


if __name__ == "__main__":
    main()
