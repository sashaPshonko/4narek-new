#!/usr/bin/env python3
"""
Full OOS compare: EKB + classic NormalSales + corridor generations + v9.

EKB rules from 4narek-new/ekb/ekb.go adjustPrice (exact).
Classic ≈ pre-corridor / capital_log classic_* labels.
Corridors approximated from PRICING_EXPERIMENTS.md.
"""
from __future__ import annotations

import json
import sys
from collections import defaultdict

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S

FOCUS = S.FOCUS_ITEMS
HARD = 2  # over/dump step mult


def band(share, lo_f=0.18, hi_f=0.25, soft_f=0.28, over_f=0.35, dump_f=0.50):
    share = max(share or 12, 1)
    lo = max(1, int(lo_f * share + 0.5))
    hi = max(lo + 1, int(hi_f * share + 0.5))
    soft = max(hi + 1, int(soft_f * share + 0.5))
    over = max(soft + 1, int(over_f * share + 0.5))
    dump = max(over + 1, int(dump_f * share + 0.5))
    return lo, hi, soft, over, dump


# ── Policies ─────────────────────────────────────────────────────────

def pol_hold(st, obs, dm):
    return "HOLD", st.price


def pol_ekb(st, obs, dm, normal_sales=None):
    """Exact EKB logic. held = onAH+inv; we split approx: onAH≈held, inv≈0 if held small,
    or onAH=held//2, inv=held-onAH when held large — use on_ah/inv from obs if present.
    """
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    N = normal_sales if normal_sales is not None else (obs.get("normal_sales") or 6)
    on_ah = obs.get("on_ah")
    inv = obs.get("inv")
    if on_ah is None or inv is None:
        # capital_cycles has on_ah/inv; fallback split
        on_ah = obs.get("onAH", st.held)
        inv = obs.get("invCount", 0)
        if "on_ah" not in obs and "onAH" not in obs:
            on_ah = st.held
            inv = 0

    price = st.price
    # UP: total stock < NormalSales*2
    if on_ah + inv < N * 2:
        return "UP", price + step
    # DOWN weak sales
    if (
        (on_ah > sales and on_ah > N)
        and (inv > sales * 3 and inv > N)
        and sales < N
    ):
        return "DOWN", max(step, price - step)
    # DOWN buy excess
    if (
        (on_ah > sales and on_ah > N)
        and (inv > sales * 3 and inv > N)
        and buys > sales
    ):
        return "DOWN", max(step, price - step)
    return "HOLD", price


def pol_classic_normalsales(st, obs, dm, normal_sales=None):
    """Pre-corridor classic: UP if sales < N and stock not bloated; DOWN if AH bloated + weak sales."""
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    N = normal_sales if normal_sales is not None else (obs.get("normal_sales") or 6)
    held = st.held
    on_ah = obs.get("on_ah", obs.get("onAH", held))

    if sales < N and held <= N and on_ah < N:
        return "UP", st.price + step
    if on_ah > sales and on_ah > N and sales < N:
        return "DOWN", max(step, st.price - step)
    if buys > 2 * max(sales, 1) and held > N:
        return "DOWN", max(step, st.price - step)
    return "HOLD", st.price


def pol_corridor_v1(st, obs, dm):
    """v1: held>hi →↓; held<lo + sales →↑"""
    lo, hi, soft, over, dump = band(obs["share"])
    step = obs["step"] or 100_000
    sales = obs["sales"]
    if st.held > hi:
        return "DOWN", max(step, st.price - step)
    if st.held < lo and sales >= 1 and st.held > 0:
        return "UP", st.price + step
    return "HOLD", st.price


def pol_corridor_v3(st, obs, dm):
    """v3: buy-veto, soft above hi, streak≈1 via up_cd"""
    lo, hi, soft, over, dump = band(obs["share"], 0.18, 0.25)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    if st.held >= dump:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held > hi:
        return "DOWN", max(step, st.price - step)
    if st.held < lo and st.held > 0 and sales >= 1 and sales > buys and st.up_cd == 0:
        return "UP", st.price + step
    return "HOLD", st.price


def pol_corridor_v4(st, obs, dm):
    """v4: weak_demand sales≥3, up_cd"""
    lo, hi, soft, over, dump = band(obs["share"], 0.18, 0.25)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    if st.held >= dump:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held > hi:
        return "DOWN", max(step, st.price - step)
    if st.held < lo and st.held > 0 and sales >= 3 and sales > buys and st.up_cd == 0:
        return "UP", st.price + step
    return "HOLD", st.price


def pol_corridor_v5(st, obs, dm):
    """v5: no UP when held=0; night sales≥4"""
    lo, hi, soft, over, dump = band(obs["share"], 0.18, 0.25)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    night = obs.get("night", False)
    min_s = 4 if night else 3
    if st.held >= dump:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held > hi:
        return "DOWN", max(step, st.price - step)
    if st.held <= 0:
        return "HOLD", st.price
    if st.held < lo and sales >= min_s and sales > buys and st.up_cd == 0:
        return "UP", st.price + step
    return "HOLD", st.price


def pol_corridor_v6(st, obs, dm):
    """v6: like v5 + deep-↑ when held very low bypasses nothing simple; hard↓×2 already"""
    return pol_corridor_v5(st, obs, dm)


def pol_v8_late(st, obs, dm):
    """Late v8 spirit: inventory downs + rare demand UP; no empty catchup; no underprice veto.
    Approximates thin-fill era (aggressive hold when empty)."""
    lo, hi, soft, over, dump = band(obs["share"], 0.18, 0.25)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    if st.held >= dump:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - HARD * step)
    if st.held > hi:
        return "DOWN", max(step, st.price - step)
    if st.held == 0:
        return "HOLD", st.price  # explored empty stuck
    if st.held < lo and sales >= 3 and sales > buys and st.up_cd == 0:
        return "UP", st.price + step
    return "HOLD", st.price


def pol_v9(st, obs, dm):
    return S.policy_v9_core(st, obs, dm, streak_need=2, gap=1.00, min_sales_up=3, down_block_ratio=0.90)


POLICIES = [
    ("ekb", pol_ekb),
    ("classic_ns", pol_classic_normalsales),
    ("corridor_v1", pol_corridor_v1),
    ("corridor_v3", pol_corridor_v3),
    ("corridor_v4", pol_corridor_v4),
    ("corridor_v5", pol_corridor_v5),
    ("corridor_v6", pol_corridor_v6),
    ("v8_late", pol_v8_late),
    ("v9", pol_v9),
    ("hold", pol_hold),
]


def drawdown(series):
    peak = cum = dd = 0.0
    for p in series:
        cum += p
        peak = max(peak, cum)
        if peak > 0:
            dd = max(dd, (peak - cum) / peak)
    return dd


def simulate(rows_by_item, policy_fn, dm):
    tot = {
        "profit": 0.0,
        "sales": 0.0,
        "ups": 0,
        "downs": 0,
        "cycles": 0,
        "under": 0,
        "over": 0,
        "held_sum": 0.0,
    }
    series = []
    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
        nac = dm.nac.get(it, 200_000)
        for obs in seq:
            if st.up_cd > 0:
                st.up_cd -= 1
            if st.down_cd > 0:
                st.down_cd -= 1
            # night UTC 0-6 ≈ MSK 3-9
            hour = int(obs["ts"][11:13]) if len(obs["ts"]) > 13 else 12
            obs = dict(obs)
            obs["night"] = 0 <= hour < 6
            # normal_sales from logged if any
            if "normal_sales" not in obs:
                obs["normal_sales"] = obs.get("NormalSales") or 6

            mkt = obs.get("mkt") or obs.get("p10")
            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0
            pre = dm.expected_sales(it, st.price, mkt, st.held, obs["share"], obs["ts"])
            if abs(st.price - obs["price_before"]) <= (obs["step"] or 100_000):
                gs = 0.7 * obs["sales"] + 0.3 * pre
            else:
                gs = 0.0 if empty_idle else pre
            gb = dm.expected_buys(gs, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle)
            obs_g = {**obs, "sales": gs, "buys": gb}

            action, new_price = policy_fn(st, obs_g, dm)

            exp = dm.expected_sales(it, new_price, mkt, st.held, obs["share"], obs["ts"])
            if empty_idle and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = float(obs["sales"])
                buys = float(obs["buys"])
            elif abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = 0.7 * obs["sales"] + 0.3 * exp
                buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
            else:
                sales = exp
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
            tot["held_sum"] += st.held
            series.append(p)
            if mkt:
                r = st.price / mkt
                if r < 0.85:
                    tot["under"] += 1
                if r > 1.10:
                    tot["over"] += 1
            if st.held == 0 and sales < 0.5 and buys < 0.5:
                st.empty_streak += 1
            else:
                st.empty_streak = 0
            tot["cycles"] += 1

    n = tot["cycles"] or 1
    hours = n / 6.0
    return {
        "profit_m": round(tot["profit"] / 1e6, 1),
        "pph_m": round(tot["profit"] / 1e6 / max(hours, 0.1), 2),
        "under_frac": round(tot["under"] / n, 3),
        "over_frac": round(tot["over"] / n, 3),
        "avg_held": round(tot["held_sum"] / n, 2),
        "ups": tot["ups"],
        "downs": tot["downs"],
        "cycles": tot["cycles"],
        "drawdown": round(drawdown(series), 3),
        "sales": round(tot["sales"], 0),
    }


def main():
    con = S.connect()
    print("loading panel…")
    rows = S.load_panel(con, FOCUS)
    # need on_ah, inv for EKB
    enriched = []
    for r in rows:
        rr = dict(r)
        # capital_cycles has on_ah, inv columns
        enriched.append(rr)
    # re-query with on_ah/inv
    q = f"""
    SELECT ts, policy, item_id, action, held, sales, buys, try_sells,
           price_before, price_after, step, share, stock_load,
           COALESCE(profit_now,0) AS profit_now,
           COALESCE(fwd_profit_1,0) AS fp1,
           COALESCE(fwd_profit_2,0) AS fp2,
           COALESCE(fwd_profit_3,0) AS fp3,
           nacenka_before, cycle_minutes,
           on_ah, inv, normal_sales
    FROM capital_cycles
    WHERE ts>='2026-07-15' AND ts<'2026-09-15'
      AND item_id IN ({','.join('?'*len(FOCUS))})
    ORDER BY item_id, ts
    """
    rows = [dict(r) for r in con.execute(q, FOCUS)]
    print(f"cycles={len(rows)}")
    rows = S.attach_p10_fast(con, rows)
    nac = S.avg_nacenka(con, FOCUS)

    ts = sorted(set(r["ts"] for r in rows))
    fold_size = len(ts) // 4
    all_folds = []

    print(f"\n{'model':14} | " + " | ".join(f"fold{f} profit" for f in range(3)) + " | mean p/h | mean under")
    print("-" * 100)

    summary = {}
    for name, fn in POLICIES:
        fold_stats = []
        for f in range(3):
            t_cut = ts[(f + 1) * fold_size]
            t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
            train = [r for r in rows if r["ts"] < t_cut]
            test = [r for r in rows if t_cut <= r["ts"] < t1]
            dm = S.DemandModel()
            dm.fit(train, nac)
            # map on_ah
            for r in test:
                r["on_ah"] = r.get("on_ah") if r.get("on_ah") is not None else r["held"]
                r["inv"] = r.get("inv") or 0
            te = simulate(S.split_by_item(test), fn, dm)
            fold_stats.append(te)
        mean_pph = sum(x["pph_m"] for x in fold_stats) / 3
        mean_under = sum(x["under_frac"] for x in fold_stats) / 3
        mean_profit = sum(x["profit_m"] for x in fold_stats) / 3
        summary[name] = {
            "folds": fold_stats,
            "mean_profit_m": round(mean_profit, 1),
            "mean_pph_m": round(mean_pph, 2),
            "mean_under": round(mean_under, 3),
            "wins_vs_v9": sum(
                1 for i, x in enumerate(fold_stats) if x["profit_m"] > summary.get("v9", {}).get("folds", [{}]*3)[i].get("profit_m", 1e18)
            )
            if name != "v9" and "v9" in summary
            else None,
        }
        profits = " | ".join(f"{x['profit_m']:8.1f}" for x in fold_stats)
        print(f"{name:14} | {profits} | {mean_pph:8.2f} | {mean_under:.3f}")

    # ranking by mean profit
    ranked = sorted(summary.items(), key=lambda kv: -kv[1]["mean_profit_m"])
    print("\n=== RANK by mean OOS profit ===")
    for i, (name, s) in enumerate(ranked, 1):
        mark = " ←" if name == "v9" else ""
        print(f"  {i:2}. {name:14} mean_profit={s['mean_profit_m']:8.1f}M  p/h={s['mean_pph_m']:6.2f}  under={s['mean_under']:.3f}{mark}")

    # per-fold winner
    print("\n=== Per-fold winner ===")
    for f in range(3):
        best = max(summary.items(), key=lambda kv: kv[1]["folds"][f]["profit_m"])
        v9p = summary["v9"]["folds"][f]["profit_m"]
        print(f"  fold{f}: {best[0]} ({best[1]['folds'][f]['profit_m']}M)  v9={v9p}M  Δ={v9p - best[1]['folds'][f]['profit_m']:+.1f}M vs winner")

    out = {
        "summary": {k: {kk: vv for kk, vv in v.items() if kk != "folds"} | {"folds": v["folds"]} for k, v in summary.items()},
        "rank": [n for n, _ in ranked],
        "notes": [
            "EKB = exact port of ekb/ekb.go adjustPrice",
            "classic_ns = NormalSales UP/DOWN (pre-corridor)",
            "corridor_v* = simplified from PRICING_EXPERIMENTS",
            "v8_late = empty stuck HOLD + inventory DOWN (no market catchup)",
            "v9 = production stock_corridor_v9",
            "Demand model sequential sim — relative ranking, not absolute cash",
        ],
    }
    path = "/tmp/v9_full_compare.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {path}")
    print("DONE")


if __name__ == "__main__":
    main()
