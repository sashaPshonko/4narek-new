#!/usr/bin/env python3
"""Counterfactual DOWN vs HOLD for v10 (sim demand model).

For each excess cycle: score profit if we DOWN 1 step vs HOLD (same held/mkt),
using DemandModel — not logged action bias.

Then:
  - train classifier on CF label (DOWN better)
  - walk-forward trajectory sim: CF-policy vs turn-baseline vs v9 vs HOLD

Usage:
  .venv/bin/python sim_v10_cf_down_hold.py
"""
from __future__ import annotations

import json
import sys
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
from sklearn.ensemble import GradientBoostingClassifier
from sklearn.metrics import roc_auc_score
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import StandardScaler

import sim_v9 as S
import policy_v10_rails as V10

ROOT = Path(__file__).resolve().parent
DB = Path("/Users/sasha_pshonko/Documents/4narek/4narek-ml/data/pricing.db")
OUT = ROOT / "v10_down_hold_model" / "cf_summary.json"

# expand focus with sharps (overflow SKUs)
FOCUS = list(dict.fromkeys(S.FOCUS_ITEMS + ["sword-sharp5-1.21", "sword-sharp6-1.21"]))

FEATS = [
    "held",
    "share",
    "load",
    "sales",
    "buys",
    "try_sells",
    "turn",
    "doi",
    "stock_load",
    "price",
    "step",
    "night",
    "ratio",
]


def one_step_profit(dm: S.DemandModel, obs: dict, price: int, held: int) -> float:
    mkt = obs.get("mkt") or obs.get("p10")
    empty_idle = held == 0 and obs["sales"] == 0 and obs["buys"] == 0
    exp = dm.expected_sales(obs["item_id"], price, mkt, held, obs["share"], obs["ts"])
    if empty_idle and abs(price - obs["price_before"]) <= (obs["step"] or 100_000):
        sales = float(obs["sales"])
    elif abs(price - obs["price_before"]) <= (obs["step"] or 100_000):
        sales = 0.7 * obs["sales"] + 0.3 * exp
    else:
        sales = exp
    sales = max(0.0, sales)
    nac = dm.nac.get(obs["item_id"], 200_000)
    return sales * nac


def horizon_profit(dm: S.DemandModel, seq: list, i: int, start_price: int, start_held: int, horizon=3) -> float:
    """Roll a few cycles holding price (no further policy) — inventory evolves."""
    price = start_price
    held = start_held
    profit = 0.0
    for j in range(i, min(i + horizon, len(seq))):
        obs = seq[j]
        mkt = obs.get("mkt") or obs.get("p10")
        empty_idle = held == 0 and obs["sales"] == 0 and obs["buys"] == 0
        exp = dm.expected_sales(obs["item_id"], price, mkt, held, obs["share"], obs["ts"])
        if empty_idle and abs(price - obs["price_before"]) <= (obs["step"] or 100_000):
            sales = float(obs["sales"])
            buys = float(obs["buys"])
        elif abs(price - obs["price_before"]) <= (obs["step"] or 100_000):
            sales = 0.7 * obs["sales"] + 0.3 * exp
            buys = dm.expected_buys(sales, held, obs["share"], logged_buys=obs["buys"])
        else:
            sales = exp
            buys = dm.expected_buys(sales, held, obs["share"], logged_buys=obs["buys"])
        sales = max(0.0, sales)
        nac = dm.nac.get(obs["item_id"], 200_000)
        profit += sales * nac
        held = max(0, int(round(held - sales + buys)))
        share = obs["share"] or 12
        held = min(held, int(share * 1.5))
    return profit


def feat_row(obs: dict, held: int) -> dict:
    share = max(obs["share"] or 12, 1)
    sales = float(obs["sales"] or 0)
    buys = float(obs["buys"] or 0)
    try_s = float(obs.get("try_sells") or 0)
    mkt = obs.get("mkt") or obs.get("p10")
    price = int(obs["price_before"] or 0)
    step = max(int(obs["step"] or 1), 1)
    turn = sales / max(held, 1)
    doi = held / max(sales, 0.25)
    night = 1 if 0 <= S.hour_utc(obs["ts"]) < 6 else 0
    ratio = (price / mkt) if mkt else 1.0
    return {
        "held": held,
        "share": share,
        "load": held / share,
        "sales": sales,
        "buys": buys,
        "try_sells": try_s,
        "turn": turn,
        "doi": doi,
        "stock_load": float(obs.get("stock_load") or held / share),
        "price": price,
        "step": step,
        "night": night,
        "ratio": ratio,
    }


def build_cf_dataset(rows_by_item, dm: S.DemandModel, min_load=0.25, horizon=3):
    samples = []
    for item, seq in rows_by_item.items():
        for i, obs in enumerate(seq):
            held = int(obs["held"] or 0)
            share = max(obs["share"] or 12, 1)
            if held / share < min_load:
                continue
            step = max(int(obs["step"] or 100_000), 1)
            price = int(obs["price_before"] or step)
            p_down = max(step, price - step)
            # 3-cycle CF with fixed price after first action
            ph = horizon_profit(dm, seq, i, price, held, horizon)
            pd = horizon_profit(dm, seq, i, p_down, held, horizon)
            # also one-step for diagnostics
            o1h = one_step_profit(dm, obs, price, held)
            o1d = one_step_profit(dm, obs, p_down, held)
            fr = feat_row(obs, held)
            samples.append(
                {
                    **fr,
                    "item": item,
                    "ts": obs["ts"],
                    "cf_hold": ph,
                    "cf_down": pd,
                    "delta": pd - ph,
                    "y": 1 if pd > ph + 1e-6 else 0,
                    "one_delta": o1d - o1h,
                }
            )
    return samples


def mat(samples):
    X = np.array([[float(s[f]) for f in FEATS] for s in samples], dtype=np.float64)
    return np.nan_to_num(X, nan=0.0, posinf=1e6, neginf=0.0)


def time_split(samples, train_frac=0.6, val_frac=0.2):
    samples = sorted(samples, key=lambda s: s["ts"])
    n = len(samples)
    i1 = int(n * train_frac)
    i2 = int(n * (train_frac + val_frac))
    return samples[:i1], samples[i1:i2], samples[i2:]


def make_cf_policy(clf, tau=0.5, turn_low=0.08):
    """Policy: on excess, DOWN if model P>tau OR turn very low; else HOLD. Else v9-ish under UP."""

    def policy(st: S.State, obs: dict, dm: S.DemandModel):
        share = obs["share"] or 12
        lo, hi, soft, over, dump = S.step_band(share)
        step = obs["step"] or 100_000
        sales, buys = obs["sales"], obs["buys"]
        load = st.held / max(share, 1)
        turn = sales / max(st.held, 1) if st.held > 0 else 0.0

        if st.held > hi:
            fr = feat_row({**obs, "sales": sales, "buys": buys}, st.held)
            # overwrite with sim state
            fr["held"] = st.held
            fr["load"] = load
            fr["turn"] = turn
            fr["doi"] = st.held / max(sales, 0.25)
            fr["stock_load"] = load
            fr["price"] = st.price
            x = np.array([[float(fr[f]) for f in FEATS]], dtype=np.float64)
            x = np.nan_to_num(x)
            p = float(clf.predict_proba(x)[0, 1])
            want = p >= tau or turn < turn_low or st.held >= dump
            if want:
                mult = 2 if st.held >= over else 1
                return "DOWN", max(step, st.price - mult * step)
            return "HOLD", st.price

        if st.held < lo and st.held > 0 and sales >= 3 and sales > buys and st.up_cd == 0:
            return "UP", st.price + step
        if st.held < lo and st.held > 0 and sales < 2 and st.up_cd == 0:
            # thin stock weak sales → still UP (v10 rails idea)
            return "UP", st.price + step
        return "HOLD", st.price

    return policy


def make_turn_policy(turn_low=0.08, turn_high=0.25):
    def policy(st, obs, dm):
        share = obs["share"] or 12
        lo, hi, soft, over, dump = S.step_band(share)
        step = obs["step"] or 100_000
        sales, buys = obs["sales"], obs["buys"]
        turn = sales / max(st.held, 1) if st.held > 0 else 0.0
        load = st.held / max(share, 1)
        if st.held > hi:
            if turn < turn_low or (st.held >= dump and turn < turn_high) or (
                st.held >= over and turn < (turn_low + turn_high) / 2
            ):
                mult = 2 if st.held >= over else 1
                return "DOWN", max(step, st.price - mult * step)
            return "HOLD", st.price
        if st.held < lo and st.held > 0 and st.up_cd == 0:
            if sales >= 3 and sales > buys:
                return "UP", st.price + step
            if sales < 2:
                return "UP", st.price + step
        return "HOLD", st.price

    return policy


def make_v10_rails_policy():
    def policy(st, obs, dm):
        share = max(obs["share"] or 12, 1)
        step = obs["step"] or 100_000
        mkt = obs.get("mkt") or obs.get("p10") or st.price
        # synthetic rails from mkt (no live book in this dump): floor=0.75 mkt, mid=1.05 mkt
        floor = int(0.75 * mkt)
        mid = int(1.05 * mkt)
        inp = V10.V10In(
            held=st.held,
            sales=int(round(obs["sales"])),
            buys=int(round(obs["buys"])),
            price=st.price,
            step=step,
            share=share,
            floor=floor,
            floor_ok=True,
            mid=mid,
            mid_ok=True,
            try_sells=int(obs.get("try_sells") or 0),
            night=0 <= S.hour_utc(obs["ts"]) < 6,
        )
        out = V10.v10_decide(inp)
        if "down" in out.action:
            return "DOWN", out.new_price
        if "up" in out.action:
            return "UP", out.new_price
        return "HOLD", out.new_price

    return policy


def main():
    S.DB = str(DB)
    con = S.connect()
    print("loading panel…", FOCUS)
    rows = S.load_panel(con, FOCUS, t0="2026-07-15", t1="2026-09-03")
    print("rows", len(rows))
    try:
        rows = S.attach_p10_fast(con, rows)
    except Exception as e:
        print("attach_p10_fast failed, using price as mkt proxy:", e)
        for r in rows:
            r["mkt"] = r.get("p10") or r["price_before"]
            r["ratio"] = (r["price_before"] / r["mkt"]) if r.get("mkt") else None
    nac = S.avg_nacenka(con, FOCUS)
    con.close()

    # time split for DM fit
    rows_sorted = sorted(rows, key=lambda r: r["ts"])
    cut = int(len(rows_sorted) * 0.65)
    train_rows = rows_sorted[:cut]
    test_rows = rows_sorted[cut:]
    dm = S.DemandModel()
    dm.fit(train_rows, nac)

    by_all = S.split_by_item(rows_sorted)
    by_test = S.split_by_item(test_rows)

    print("building CF labels on train…")
    by_train = S.split_by_item(train_rows)
    cf_train = build_cf_dataset(by_train, dm)
    cf_test = build_cf_dataset(by_test, dm)
    print(f"CF samples train={len(cf_train)} test={len(cf_test)} down_better_rate train={np.mean([s['y'] for s in cf_train]):.3f} test={np.mean([s['y'] for s in cf_test]):.3f}")

    tr, va, te = time_split(cf_train + cf_test)  # global time order across all
    # re-split strictly by ts relative to train cut
    tr = [s for s in cf_train]
    te = [s for s in cf_test]
    va = tr[int(len(tr) * 0.8) :]
    tr = tr[: int(len(tr) * 0.8)]

    Xtr, ytr = mat(tr), np.array([s["y"] for s in tr])
    Xva, yva = mat(va), np.array([s["y"] for s in va])
    Xte, yte = mat(te), np.array([s["y"] for s in te])

    clf = Pipeline(
        [
            ("sc", StandardScaler()),
            (
                "gb",
                GradientBoostingClassifier(
                    random_state=0, max_depth=3, n_estimators=120, learning_rate=0.05
                ),
            ),
        ]
    )
    clf.fit(Xtr, ytr)
    p_va = clf.predict_proba(Xva)[:, 1]
    p_te = clf.predict_proba(Xte)[:, 1]
    auc_va = roc_auc_score(yva, p_va) if len(set(yva)) > 1 else float("nan")
    auc_te = roc_auc_score(yte, p_te) if len(set(yte)) > 1 else float("nan")

    # calibrate tau on val: maximize mean CF delta when following policy on val samples
    best = None
    for tau in np.linspace(0.3, 0.8, 26):
        pred = p_va >= tau
        # realized CF profit if take DOWN when pred else HOLD
        gains = []
        for s, pr in zip(va, pred):
            gains.append(s["cf_down"] if pr else s["cf_hold"])
        score = float(np.mean(gains))
        rate = float(np.mean(pred))
        if rate < 0.1 or rate > 0.9:
            continue
        if best is None or score > best[0]:
            best = (score, float(tau), rate)
    tau = best[1] if best else 0.5

    # test CF regret
    base_hold = float(np.mean([s["cf_hold"] for s in te]))
    oracle = float(np.mean([max(s["cf_hold"], s["cf_down"]) for s in te]))
    turn_gains = []
    model_gains = []
    for s, p in zip(te, p_te):
        turn_down = s["turn"] < 0.08 or (s["load"] >= 0.5 and s["turn"] < 0.25)
        model_gains.append(s["cf_down"] if p >= tau else s["cf_hold"])
        turn_gains.append(s["cf_down"] if turn_down else s["cf_hold"])
    model_cf = float(np.mean(model_gains))
    turn_cf = float(np.mean(turn_gains))

    # full trajectory on test panel
    policies = {
        "hold": S.policy_hold,
        "v9_core": lambda st, obs, dm: S.policy_v9_core(st, obs, dm),
        "classic_inv": lambda st, obs, dm: S.policy_classic_inventory(st, obs, dm),
        "turn_baseline": make_turn_policy(),
        "cf_model": make_cf_policy(clf, tau=tau),
        "v10_rails_synth": make_v10_rails_policy(),
    }
    traj = {}
    for name, pol in policies.items():
        m, _per = S.simulate_panel(by_test, pol, dm)
        traj[name] = {
            "profit": m["profit"],
            "profit_per_cycle": m.get("profit_per_cycle", m["profit"] / max(m["cycles"], 1)),
            "ups": m["ups"],
            "downs": m["downs"],
            "cycles": m["cycles"],
            "under_frac": m["under_frac"],
            "over_frac": m["over_frac"],
        }
    v9p = traj["v9_core"]["profit"] or 1.0
    for name in traj:
        traj[name]["x_v9"] = traj[name]["profit"] / v9p

    summary = {
        "generated": datetime.now(timezone.utc).isoformat(),
        "db": str(DB),
        "focus": FOCUS,
        "cf": {
            "n_train": len(tr),
            "n_val": len(va),
            "n_test": len(te),
            "down_better_rate_train": float(np.mean(ytr)),
            "down_better_rate_test": float(np.mean(yte)),
            "auc_val": auc_va,
            "auc_test": auc_te,
            "tau": tau,
            "test_mean_cf_hold": base_hold,
            "test_mean_cf_oracle": oracle,
            "test_mean_cf_model": model_cf,
            "test_mean_cf_turn": turn_cf,
            "model_vs_hold_pct": (model_cf / base_hold - 1) * 100 if base_hold else None,
            "turn_vs_hold_pct": (turn_cf / base_hold - 1) * 100 if base_hold else None,
            "oracle_vs_hold_pct": (oracle / base_hold - 1) * 100 if base_hold else None,
        },
        "trajectory_test": traj,
        "note": "CF uses DemandModel; synth rails for v10_rails = 0.75–1.05×mkt (no AH book in dump).",
    }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(summary, indent=2, ensure_ascii=False))
    try:
        import joblib

        joblib.dump({"clf": clf, "feats": FEATS, "tau": tau, "kind": "cf"}, OUT.parent / "cf_model.joblib")
    except Exception as e:
        print("joblib", e)

    print(json.dumps(summary["cf"], indent=2))
    print("trajectory x_v9:")
    for k, v in sorted(traj.items(), key=lambda kv: -kv[1]["profit"]):
        print(f"  {k:20} profit={v['profit']:.0f}  x_v9={v['x_v9']:.3f}  down={v['downs']} up={v['ups']}")
    print("wrote", OUT)


if __name__ == "__main__":
    main()
