#!/usr/bin/env python3
"""
Empty escape: primary stop = buys appeared; safety cap = ?
Compare safety rails: none / p10 / 0.95p10 / 1.05p10 / p10+nac / median-sell proxy.
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
CYCLE = 10
NAC = 200_000


def band(share):
    return S.step_band(share, 0.18, 0.25)


def policy_buy_stop(st, obs, dm, safety="p10", down_block=0.90, min_sales=3, streak=2):
    """v9 inventory + empty↑ until buys OR safety cap."""
    share = obs["share"] or 12
    lo, hi, _, over, dump = band(share)
    step = obs["step"] or STEP
    sales, buys = obs["sales"], obs["buys"]
    mkt = obs.get("mkt") or obs.get("p10")
    nac = obs.get("nacenka") or NAC
    ratio = st.price / mkt if mkt else None

    if st.held > hi:
        if ratio is not None and ratio < down_block:
            return "HOLD", st.price
        if st.held >= dump or st.held >= over:
            return "DOWN", max(step, st.price - 2 * step)
        return "DOWN", max(step, st.price - step)

    night = obs.get("night", False)
    ms = 4 if night else min_sales
    if st.held < lo and st.held > 0 and sales >= ms and sales > buys and st.up_cd == 0:
        if ratio is not None and ratio >= 1.05:
            return "HOLD", st.price
        return "UP", st.price + step

    # empty catchup: primary = no buys yet; safety = cap
    if st.held == 0 and st.empty_streak >= streak and st.up_cd == 0:
        if buys > 0:
            return "HOLD", st.price  # can buy — stop
        # safety caps
        cap = None
        if safety == "none":
            cap = None
        elif safety == "p10" and mkt:
            cap = mkt
        elif safety == "p10_95" and mkt:
            cap = int(mkt * 0.95)
        elif safety == "p10_105" and mkt:
            cap = int(mkt * 1.05)
        elif safety == "p10_plus_nac" and mkt:
            cap = mkt + nac
        elif safety == "gap080" and mkt:
            cap = int(mkt * 0.80)
        if cap is not None and st.price + step > cap:
            return "HOLD", st.price
        if cap is not None and st.price >= cap:
            return "HOLD", st.price
        return "UP", st.price + step
    return "HOLD", st.price


def make(safety):
    return lambda st, obs, dm: policy_buy_stop(st, obs, dm, safety=safety)


def wf(rows, nac_map, safety, folds=3):
    ts = sorted(set(r["ts"] for r in rows))
    fs = len(ts) // (folds + 1)
    out = []
    pol = make(safety)
    for f in range(folds):
        t_cut = ts[(f + 1) * fs]
        t1 = ts[min((f + 2) * fs, len(ts) - 1)]
        train = [r for r in rows if r["ts"] < t_cut]
        test = [r for r in rows if t_cut <= r["ts"] < t1]
        for r in test:
            r["nacenka"] = nac_map.get(r["item_id"], NAC)
            try:
                h = int(r["ts"][11:13])
            except Exception:
                h = 12
            r["night"] = 0 <= h < 6
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
            }
        )
    return out


def stress(safety, p0, mkt, scenario, max_c=80):
    """
    scenarios:
      normal_empty — never buys (climb to safety)
      buys_at_90 — at sell>=0.9*mkt inject buys=1 each cycle (stop on buys)
      broken_bots — never buys, long run (runaway test)
    """
    st = S.State(price=p0, held=0)
    pol = make(safety)
    ups = 0
    for c in range(max_c):
        if st.up_cd > 0:
            st.up_cd -= 1
        buys = 0
        if scenario == "buys_at_90" and st.price >= 0.90 * mkt:
            buys = 1
        st.held = 0
        obs = {
            "step": STEP,
            "share": 12,
            "sales": 0,
            "buys": buys,
            "mkt": mkt,
            "p10": mkt,
            "nacenka": NAC,
            "night": False,
        }
        # if buys, empty streak should break for realism — but catchup checks buys first
        act, newp = pol(st, obs, None)
        if act == "UP":
            ups += 1
            st.up_cd = 2
        st.price = newp
        if buys > 0:
            st.empty_streak = 0
        else:
            st.empty_streak += 1
        if c >= 6 and act == "HOLD" and st.up_cd == 0:
            break
        if scenario == "broken_bots" and c >= 59:
            break
    return {
        "final_m": round(st.price / 1e6, 2),
        "pct": round(100 * st.price / mkt, 1),
        "ups": ups,
        "min": (c + 1) * CYCLE,
    }


def main():
    con = S.connect()
    print("loading…")
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    nac_map = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)}")

    safeties = [
        ("gap080", "old early stop 80%"),
        ("p10", "safety = p10 (proposed)"),
        ("p10_95", "safety = 0.95*p10"),
        ("p10_105", "safety = 1.05*p10"),
        ("p10_plus_nac", "safety = p10+nac"),
        ("none", "NO safety — only stop on buys"),
    ]

    summary = {}
    print(f"\n{'safety':14} | mean OOS | under | over | ups | broken→ | buys@90→ | empty→")
    print("-" * 100)
    for name, desc in safeties:
        folds = wf(rows, nac_map, name)
        mean_p = sum(f["profit_m"] for f in folds) / 3
        mean_u = sum(f["under"] for f in folds) / 3
        mean_o = sum(f["over"] for f in folds) / 3
        ups = sum(f["ups"] for f in folds)
        br = stress(name, 500_000, 3_000_000, "broken_bots", max_c=60)
        b90 = stress(name, 500_000, 3_000_000, "buys_at_90", max_c=80)
        em = stress(name, 500_000, 3_000_000, "normal_empty", max_c=80)
        summary[name] = {
            "desc": desc,
            "mean_profit_m": round(mean_p, 1),
            "mean_under": round(mean_u, 3),
            "mean_over": round(mean_o, 3),
            "ups": ups,
            "folds": folds,
            "broken": br,
            "buys_at_90": b90,
            "empty_no_buys": em,
        }
        print(
            f"{name:14} | {mean_p:8.1f} | {mean_u:.3f} | {mean_o:.3f} | {ups:4d} | "
            f"{br['final_m']:5.2f}M({br['pct']:5.1f}%) | "
            f"{b90['final_m']:5.2f}M({b90['pct']:5.1f}%) | "
            f"{em['final_m']:5.2f}M({em['pct']:5.1f}%)"
        )
        print(f"  {desc}")

    base = summary["p10"]["mean_profit_m"]
    print("\n=== vs safety=p10 ===")
    for name, _ in safeties:
        s = summary[name]
        d = 100 * (s["mean_profit_m"] - base) / base if base else 0
        print(
            f"  {name:14} {s['mean_profit_m']:8.1f}M ({d:+.2f}%)  "
            f"broken_cap={s['broken']['pct']}%  early_buy_stop={s['buys_at_90']['pct']}%"
        )

    print("\n=== Is p10 a good safety? ===")
    p = summary["p10"]
    none = summary["none"]
    print(
        f"  If bots broken (no buys ever): p10 stops at {p['broken']['pct']}% mkt; "
        f"none goes to {none['broken']['pct']}% (runaway)."
    )
    print(
        f"  If buys appear at 90% mkt: p10 stops at {p['buys_at_90']['pct']}% "
        f"(buy-stop works before safety)."
    )
    print(
        f"  OOS: p10={p['mean_profit_m']}M over={p['mean_over']}; "
        f"p10+nac={summary['p10_plus_nac']['mean_profit_m']}M over={summary['p10_plus_nac']['mean_over']}; "
        f"none={none['mean_profit_m']}M over={none['mean_over']}."
    )
    # verdict
    if none["broken"]["pct"] > 150 and p["broken"]["pct"] <= 105:
        print("  VERDICT: p10 OK as safety rail — caps runaway; OOS ≈ peers.")
    if summary["p10_plus_nac"]["mean_over"] > p["mean_over"] + 0.02:
        print("  p10+nac: higher over in OOS — worse safety for 'other reasons'.")
    if abs(summary["p10_105"]["mean_profit_m"] - p["mean_profit_m"]) < 30:
        print("  1.05*p10 ≈ p10 on OOS; little gain, more room to overshoot if broken.")

    path = "/tmp/v9_buy_stop_safety.json"
    with open(path, "w") as f:
        json.dump(summary, f, ensure_ascii=False, indent=2)
    print(f"Wrote {path}")


if __name__ == "__main__":
    main()
