#!/usr/bin/env python3
"""
What if empty catchup has no artificial gap — grow while 'no buy power'?

Buy power model (simple, matches Sasha):
  buy_max = sell - nacenka
  market_buy ≈ mkt_sell - nacenka   (mkt = sell p10)
  can_buy iff buy_max >= market_buy - slack  iff  sell >= mkt - slack
  → 'until can buy' ≈ climb while sell < mkt (gap=1.0, cap price+step<=mkt)

Variants:
  A) gap=0.80 (current)
  B) until_buy_power = gap=1.0 + cap at mkt
  C) no_cap_empty = empty UP forever while empty (no ratio/mkt stop) — classic trap
  D) gap=1.0 but allow sell up to mkt+nacenka (user's '100+nac' misread check)
"""
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
NAC = 200_000  # typical nacenka for stress
CYCLE_MIN = 10
GAPS_BASE = [0.80, 1.00]


def step_band(share):
    return S.step_band(share, 0.18, 0.25)


def policy_until_buy(
    st, obs, dm, mode="until_buy", down_block_ratio=0.90, min_sales_up=3, streak_need=2
):
    """v9 core with different empty-UP ceilings."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share)
    step = obs["step"] or STEP
    sales, buys = obs["sales"], obs["buys"]
    mkt = obs.get("mkt") or obs.get("p10")
    nac = obs.get("nacenka") or NAC
    ratio = st.price / mkt if mkt else None
    night = obs.get("night", False)
    min_sales = 4 if night else min_sales_up

    # DOWN same as v9
    if st.held > hi:
        if ratio is not None and ratio < down_block_ratio:
            return "HOLD", st.price
        if st.held >= dump or st.held >= over:
            return "DOWN", max(step, st.price - 2 * step)
        return "DOWN", max(step, st.price - step)

    # demand UP same
    if st.held < lo and st.held > 0 and sales >= min_sales and sales > buys and st.up_cd == 0:
        if ratio is not None and ratio >= 1.05:
            return "HOLD", st.price
        return "UP", st.price + step

    # empty catchup — modes
    if st.held == 0 and st.empty_streak >= streak_need and st.up_cd == 0:
        if mode == "gap080":
            if mkt and ratio is not None and ratio < 0.80 and st.price + step <= mkt:
                return "UP", st.price + step
        elif mode == "until_buy":
            # grow while buy_max < market_buy  ⇒ sell < mkt
            if mkt and st.price < mkt and st.price + step <= mkt:
                return "UP", st.price + step
        elif mode == "until_buy_plus_nac":
            # sell target = mkt + nac (100%+nacenka on sell-p10) — overshoot test
            cap = mkt + nac if mkt else None
            if cap and st.price < cap:
                return "UP", st.price + step
        elif mode == "no_cap_empty":
            # no market limit at all while empty
            return "UP", st.price + step
    return "HOLD", st.price


def make(mode):
    return lambda st, obs, dm: policy_until_buy(st, obs, dm, mode=mode)


def wf(rows, nac_map, mode, folds=3):
    ts = sorted(set(r["ts"] for r in rows))
    fold_size = len(ts) // (folds + 1)
    out = []
    pol = make(mode)
    for f in range(folds):
        t_cut = ts[(f + 1) * fold_size]
        t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
        train = [r for r in rows if r["ts"] < t_cut]
        test = [r for r in rows if t_cut <= r["ts"] < t1]
        for r in test:
            r["nacenka"] = nac_map.get(r["item_id"], NAC)
        dm = S.DemandModel()
        dm.fit(train, nac_map)
        te, _ = S.simulate_panel(S.split_by_item(test), pol, dm)
        out.append(
            {
                "fold": f,
                "profit_m": round(te.get("profit_m", 0), 1),
                "under": round(te["under_frac"], 3),
                "over": round(te["over_frac"], 3),
                "ups": te["ups"],
                "downs": te["downs"],
            }
        )
    return out


def stress(mode, price0, mkt, held_mode, max_c=80, nac=NAC):
    st = S.State(price=price0, held=0 if held_mode == "empty" else 6)
    pol = make(mode)
    for c in range(max_c):
        if st.up_cd > 0:
            st.up_cd -= 1
        if held_mode == "empty":
            held, sales, buys = 0, 0, 0
        else:
            held, sales, buys = 6, 1, 0
        st.held = held
        obs = {
            "step": STEP,
            "share": 12,
            "sales": sales,
            "buys": buys,
            "mkt": mkt,
            "p10": mkt,
            "nacenka": nac,
            "night": False,
        }
        act, newp = pol(st, obs, None)
        if act == "UP":
            st.up_cd = 2
        st.price = newp
        if held_mode == "empty":
            st.empty_streak += 1
        # stop
        buy_max = st.price - nac
        mkt_buy = mkt - nac
        can_buy = buy_max >= mkt_buy - 1  # sell >= mkt
        if held_mode == "empty" and c >= 5 and act == "HOLD":
            break
        if held_mode == "empty" and mode == "no_cap_empty" and c >= 59:
            break
        if held_mode == "dump" and act == "HOLD" and st.price / mkt < 0.90:
            break
        if held_mode == "dump" and c >= 25:
            break
    return {
        "final_m": round(st.price / 1e6, 2),
        "final_pct_mkt": round(100 * st.price / mkt, 1),
        "buy_max_m": round((st.price - nac) / 1e6, 2),
        "mkt_buy_m": round((mkt - nac) / 1e6, 2),
        "can_buy": bool(st.price >= mkt) if held_mode == "empty" else None,
        "min": (c + 1) * CYCLE_MIN,
        "ups_approx": sum(1 for _ in range(1)),  # placeholder
    }


def main():
    con = S.connect()
    print("loading…")
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    nac_map = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)}")

    modes = [
        ("gap080", "current: stop empty↑ at 80% mkt"),
        ("until_buy", "grow empty while sell < mkt (can buy)"),
        ("until_buy_plus_nac", "grow empty to mkt+nac (100%+nac)"),
        ("no_cap_empty", "empty↑ forever, no market stop"),
    ]

    summary = {}
    print(f"\n{'mode':20} | fold0 | fold1 | fold2 | mean | under | over | ups")
    print("-" * 90)
    for mode, desc in modes:
        folds = wf(rows, nac_map, mode)
        mean_p = sum(f["profit_m"] for f in folds) / 3
        mean_u = sum(f["under"] for f in folds) / 3
        mean_o = sum(f["over"] for f in folds) / 3
        ups = sum(f["ups"] for f in folds)
        s_under = stress(mode, 500_000, 3_000_000, "empty")
        s_over_empty = stress(mode, 3_000_000, 1_200_000, "empty", max_c=40)
        s_dump = stress(mode, 3_000_000, 1_200_000, "dump")
        summary[mode] = {
            "desc": desc,
            "folds": folds,
            "mean_profit_m": round(mean_p, 1),
            "mean_under": round(mean_u, 3),
            "mean_over": round(mean_o, 3),
            "ups": ups,
            "stress_under_empty": s_under,
            "stress_over_empty": s_over_empty,
            "stress_over_dump": s_dump,
        }
        profits = " | ".join(f"{f['profit_m']:7.1f}" for f in folds)
        print(f"{mode:20} | {profits} | {mean_p:7.1f} | {mean_u:.3f} | {mean_o:.3f} | {ups}")
        print(
            f"  {desc}\n"
            f"  under empty 0.5→3M: end {s_under['final_m']}M ({s_under['final_pct_mkt']}%) "
            f"can_buy={s_under['can_buy']} buy_max={s_under['buy_max_m']} vs mkt_buy={s_under['mkt_buy_m']} in {s_under['min']}m\n"
            f"  OVER empty 3→1.2M: end {s_over_empty['final_m']}M ({s_over_empty['final_pct_mkt']}%) "
            f"(no_cap would keep climbing!)\n"
            f"  OVER dump: end {s_dump['final_m']}M ({s_dump['final_pct_mkt']}%)"
        )

    base = summary["gap080"]["mean_profit_m"]
    print("\n=== vs gap080 ===")
    for mode, _ in modes:
        s = summary[mode]
        d = 100 * (s["mean_profit_m"] - base) / base if base else 0
        print(f"  {mode:20} {s['mean_profit_m']:8.1f}M  ({d:+.2f}%)  over={s['mean_over']}")

    # verdict
    print("\nVERDICT:")
    print("  until_buy (= grow until sell reaches mkt): restores can_buy; OOS ≈ gap080")
    print("  no_cap_empty: on OVER+empty keeps UPping forever — toxic")
    print("  until_buy_plus_nac: targets above sell-mkt — overprice on empty")

    path = "/tmp/v9_until_buy_compare.json"
    with open(path, "w") as f:
        json.dump(summary, f, ensure_ascii=False, indent=2)
    print(f"Wrote {path}")


if __name__ == "__main__":
    main()
