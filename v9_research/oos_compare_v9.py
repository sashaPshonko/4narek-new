#!/usr/bin/env python3
"""Focused OOS compare: classic inventory vs v8af-approx vs production v9."""
import json
import sys
from collections import defaultdict

sys.path.insert(0, "/tmp/v9")
# When run on VPS, sim is at /tmp/v9/sim_v9.py; locally import from same dir.
try:
    import sim_v9 as S
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S

FOCUS = S.FOCUS_ITEMS


def drawdown(profits_per_cycle):
    peak = 0.0
    dd = 0.0
    cum = 0.0
    for p in profits_per_cycle:
        cum += p
        if cum > peak:
            peak = cum
        if peak > 0:
            dd = max(dd, (peak - cum) / peak)
    return dd


def sim_with_series(rows_by_item, policy, dm):
    tot = {"profit": 0.0, "sales": 0.0, "ups": 0, "downs": 0, "cycles": 0, "under_frac": 0.0, "over_frac": 0.0}
    series = []
    for it, seq in rows_by_item.items():
        # replicate simulate_item but collect per-cycle profit
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
        nac = dm.nac.get(it, 200_000)
        under_n = over_n = 0
        for obs in seq:
            if st.up_cd > 0:
                st.up_cd -= 1
            if st.down_cd > 0:
                st.down_cd -= 1
            mkt = obs.get("mkt") or obs.get("p10")
            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0
            pre_sales = dm.expected_sales(obs["item_id"], st.price, mkt, st.held, obs["share"], obs["ts"])
            if abs(st.price - obs["price_before"]) <= (obs["step"] or 100_000):
                gate_sales = 0.7 * obs["sales"] + 0.3 * pre_sales
                gate_buys = dm.expected_buys(
                    gate_sales, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle
                )
            else:
                gate_sales = 0.0 if empty_idle else pre_sales
                gate_buys = dm.expected_buys(
                    gate_sales, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle
                )
            obs_gate = {**obs, "sales": gate_sales, "buys": gate_buys}
            action, new_price = policy(st, obs_gate, dm)
            exp_sales = dm.expected_sales(obs["item_id"], new_price, mkt, st.held, obs["share"], obs["ts"])
            if empty_idle and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = float(obs["sales"])
                buys = float(obs["buys"])
            elif abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = 0.7 * obs["sales"] + 0.3 * exp_sales
                buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
            else:
                sales = exp_sales
                buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
            sales = max(0.0, sales)
            if action == "UP":
                tot["ups"] += 1
                st.up_cd = 2
            elif action == "DOWN":
                tot["downs"] += 1
                st.down_cd = 1
            st.price = new_price
            st.held = max(0, int(round(st.held - sales + buys)))
            share = obs["share"] or 12
            st.held = min(st.held, int(share * 1.5))
            p = sales * nac
            tot["profit"] += p
            tot["sales"] += sales
            series.append(p)
            if mkt:
                r = st.price / mkt
                if r < 0.85:
                    under_n += 1
                if r > 1.10:
                    over_n += 1
            if st.held == 0 and sales < 0.5 and buys < 0.5:
                st.empty_streak += 1
            else:
                st.empty_streak = 0
            tot["cycles"] += 1
        n = len(seq)
        tot["under_frac"] += under_n
        tot["over_frac"] += over_n
    if tot["cycles"]:
        tot["under_frac"] /= tot["cycles"]
        tot["over_frac"] /= tot["cycles"]
        tot["profit_m"] = tot["profit"] / 1e6
        hours = tot["cycles"] / 6.0  # ~10m cycles
        tot["pph_m"] = tot["profit_m"] / max(hours, 0.1)
        tot["drawdown"] = drawdown(series)
    return tot


def main():
    con = S.connect()
    rows = S.load_panel(con, FOCUS)
    rows = S.attach_p10_fast(con, rows)
    nac = S.avg_nacenka(con, FOCUS)
    policies = [
        ("classic", "classic_inv", {"min_sales_up": 3, "lo_frac": 0.18, "hi_frac": 0.25}),
        ("v8af_approx", "v8af_approx", {}),
        ("v9", "v9_core", {"streak_need": 2, "gap": 0.80, "min_sales_up": 3, "down_block_ratio": 0.90}),
    ]
    folds = []
    ts = sorted(set(r["ts"] for r in rows))
    fold_size = len(ts) // 4
    for f in range(3):
        t_cut = ts[(f + 1) * fold_size]
        t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
        train = [r for r in rows if r["ts"] < t_cut]
        test = [r for r in rows if t_cut <= r["ts"] < t1]
        dm = S.DemandModel()
        dm.fit(train, nac)
        fold_res = {"fold": f, "cut": t_cut, "end": t1, "models": {}}
        for label, name, params in policies:
            pol = S.make_policy(name, **params)
            te = sim_with_series(S.split_by_item(test), pol, dm)
            fold_res["models"][label] = {
                "profit_m": round(te.get("profit_m", 0), 1),
                "pph_m": round(te.get("pph_m", 0), 2),
                "fill_proxy": round(1 - te["under_frac"] - te["over_frac"], 3),
                "under_frac": round(te["under_frac"], 3),
                "over_frac": round(te["over_frac"], 3),
                "drawdown": round(te.get("drawdown", 0), 3),
                "ups": te["ups"],
                "downs": te["downs"],
                "cycles": te["cycles"],
            }
        folds.append(fold_res)
        print(f"\n=== fold {f} {t_cut} .. {t1} ===")
        for label, m in fold_res["models"].items():
            print(
                f"  {label:12} profit={m['profit_m']:8.1f}M p/h={m['pph_m']:6.1f} "
                f"under={m['under_frac']:.3f} over={m['over_frac']:.3f} dd={m['drawdown']:.3f} "
                f"up={m['ups']} dn={m['downs']}"
            )

    out = "/tmp/v9_oos_compare.json"
    with open(out, "w") as f:
        json.dump({"folds": folds, "note": "v8af_approx is coarse inventory stand-in, not full v8af"}, f, indent=2)
    print("\nWrote", out)
    print("DONE")


if __name__ == "__main__":
    main()
