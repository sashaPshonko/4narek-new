#!/usr/bin/env python3
"""Counterfactual UP vs HOLD for v10 understock (esp. weak sales).

For each understock cycle: score 3-cycle profit if UP 1 step vs HOLD
(DemandModel — not logged action bias).

Grey zone of interest: held < lo AND sales < 2 (недобор + мало продаж).
Hard cases stay on rules (strong demand UP; dead-near-ceiling HOLD).

Usage:
  .venv/bin/python sim_v10_cf_up_hold.py
"""
from __future__ import annotations

import json
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
OUT_DIR = ROOT / "v10_up_hold_model"
OUT = OUT_DIR / "cf_summary.json"

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
    "pos_synth",  # (price-floor)/(mid-floor) with synth rails
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
    floor = int(0.75 * mkt) if mkt else max(step, price // 4)
    mid = int(1.05 * mkt) if mkt else price + 10 * step
    if mid <= floor:
        mid = floor + step
    pos = max(0.0, min(1.0, (price - floor) / (mid - floor)))
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
        "pos_synth": pos,
    }


def build_cf_dataset(rows_by_item, dm: S.DemandModel, max_load=0.18, horizon=3, weak_only=False):
    """Understock states: held/share < max_load and held > 0."""
    samples = []
    for item, seq in rows_by_item.items():
        for i, obs in enumerate(seq):
            held = int(obs["held"] or 0)
            share = max(obs["share"] or 12, 1)
            load = held / share
            if held <= 0 or load >= max_load:
                continue
            sales = int(obs["sales"] or 0)
            if weak_only and sales >= 2:
                continue
            step = max(int(obs["step"] or 100_000), 1)
            price = int(obs["price_before"] or step)
            mkt = obs.get("mkt") or obs.get("p10") or price
            mid = int(1.05 * mkt)
            p_up = min(mid, price + step)
            if p_up <= price:
                continue  # already at ceiling
            ph = horizon_profit(dm, seq, i, price, held, horizon)
            pu = horizon_profit(dm, seq, i, p_up, held, horizon)
            o1h = one_step_profit(dm, obs, price, held)
            o1u = one_step_profit(dm, obs, p_up, held)
            fr = feat_row(obs, held)
            samples.append(
                {
                    **fr,
                    "item": item,
                    "ts": obs["ts"],
                    "cf_hold": ph,
                    "cf_up": pu,
                    "delta": pu - ph,
                    "y": 1 if pu > ph + 1e-6 else 0,
                    "one_delta": o1u - o1h,
                    "weak": 1 if sales < 2 else 0,
                }
            )
    return samples


def mat(samples):
    X = np.array([[float(s[f]) for f in FEATS] for s in samples], dtype=np.float64)
    return np.nan_to_num(X, nan=0.0, posinf=1e6, neginf=0.0)


def rule_up_better(s: dict) -> bool:
    """Simple baselines for understock."""
    # dead near ceiling: try>0, sales=0, high pos → HOLD
    if s["pos_synth"] >= 0.85 and s["try_sells"] >= 3 and s["sales"] == 0:
        return False
    # below market → climb
    if s["ratio"] < 0.95:
        return True
    # weak sales understock default climb (rails idea) unless already high
    if s["sales"] < 2 and s["pos_synth"] < 0.85:
        return True
    # strong sales → UP
    if s["sales"] >= 3 and s["sales"] > s["buys"]:
        return True
    return False


def always_up(_: dict) -> bool:
    return True


def make_cf_up_policy(clf, tau=0.5):
    """On understock: UP if model P>=tau (or strong demand); veto dead-high. Excess: turn↓."""

    def policy(st: S.State, obs: dict, dm: S.DemandModel):
        share = obs["share"] or 12
        lo, hi, soft, over, dump = S.step_band(share)
        step = obs["step"] or 100_000
        sales, buys = obs["sales"], obs["buys"]
        load = st.held / max(share, 1)
        turn = sales / max(st.held, 1) if st.held > 0 else 0.0
        mkt = obs.get("mkt") or obs.get("p10") or st.price
        mid = int(1.05 * mkt)

        if st.held > hi:
            if turn < 0.08 or (st.held >= dump and turn < 0.25) or (
                st.held >= over and turn < 0.165
            ):
                mult = 2 if st.held >= over else 1
                return "DOWN", max(step, st.price - mult * step)
            return "HOLD", st.price

        if 0 < st.held < lo and st.up_cd == 0:
            fr = feat_row({**obs, "sales": sales, "buys": buys}, st.held)
            fr["held"] = st.held
            fr["load"] = load
            fr["turn"] = turn
            fr["doi"] = st.held / max(sales, 0.25)
            fr["stock_load"] = load
            fr["price"] = st.price
            # veto
            if fr["pos_synth"] >= 0.85 and (obs.get("try_sells") or 0) >= 3 and sales == 0:
                return "HOLD", st.price
            # strong demand → always UP (clear case)
            if sales >= 3 and sales > buys:
                return "UP", min(mid, st.price + step)
            x = np.array([[float(fr[f]) for f in FEATS]], dtype=np.float64)
            x = np.nan_to_num(x)
            p = float(clf.predict_proba(x)[0, 1])
            if p >= tau:
                return "UP", min(mid, st.price + step)
            return "HOLD", st.price

        return "HOLD", st.price

    return policy


def make_always_up_under_policy():
    def policy(st, obs, dm):
        share = obs["share"] or 12
        lo, hi, soft, over, dump = S.step_band(share)
        step = obs["step"] or 100_000
        sales, buys = obs["sales"], obs["buys"]
        turn = sales / max(st.held, 1) if st.held > 0 else 0.0
        mkt = obs.get("mkt") or obs.get("p10") or st.price
        mid = int(1.05 * mkt)
        if st.held > hi:
            if turn < 0.08 or st.held >= dump:
                return "DOWN", max(step, st.price - step)
            return "HOLD", st.price
        if 0 < st.held < lo and st.up_cd == 0:
            return "UP", min(mid, st.price + step)
        return "HOLD", st.price

    return policy


def make_v10_rails_policy():
    def policy(st, obs, dm):
        share = max(obs["share"] or 12, 1)
        step = obs["step"] or 100_000
        mkt = obs.get("mkt") or obs.get("p10") or st.price
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

    rows_sorted = sorted(rows, key=lambda r: r["ts"])
    cut = int(len(rows_sorted) * 0.65)
    train_rows = rows_sorted[:cut]
    test_rows = rows_sorted[cut:]
    dm = S.DemandModel()
    dm.fit(train_rows, nac)

    by_train = S.split_by_item(train_rows)
    by_test = S.split_by_item(test_rows)

    print("building UP CF labels (all understock)…")
    cf_train = build_cf_dataset(by_train, dm, max_load=0.18)
    cf_test = build_cf_dataset(by_test, dm, max_load=0.18)
    weak_train = [s for s in cf_train if s["weak"]]
    weak_test = [s for s in cf_test if s["weak"]]
    print(
        f"under CF train={len(cf_train)} test={len(cf_test)} "
        f"up_better train={np.mean([s['y'] for s in cf_train]):.3f} test={np.mean([s['y'] for s in cf_test]):.3f}"
    )
    print(
        f"weak-sales subset train={len(weak_train)} test={len(weak_test)} "
        f"up_better train={np.mean([s['y'] for s in weak_train]) if weak_train else 0:.3f} "
        f"test={np.mean([s['y'] for s in weak_test]) if weak_test else 0:.3f}"
    )

    tr = cf_train
    te = cf_test
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
    if len(set(ytr)) < 2:
        print("WARN: single class in train — abort")
        return
    clf.fit(Xtr, ytr)
    p_va = clf.predict_proba(Xva)[:, 1]
    p_te = clf.predict_proba(Xte)[:, 1]
    auc_va = roc_auc_score(yva, p_va) if len(set(yva)) > 1 else float("nan")
    auc_te = roc_auc_score(yte, p_te) if len(set(yte)) > 1 else float("nan")

    best = None
    for tau in np.linspace(0.3, 0.8, 26):
        pred = p_va >= tau
        gains = [s["cf_up"] if pr else s["cf_hold"] for s, pr in zip(va, pred)]
        score = float(np.mean(gains))
        rate = float(np.mean(pred))
        if rate < 0.05 or rate > 0.95:
            continue
        if best is None or score > best[0]:
            best = (score, float(tau), rate)
    tau = best[1] if best else 0.5

    base_hold = float(np.mean([s["cf_hold"] for s in te]))
    oracle = float(np.mean([max(s["cf_hold"], s["cf_up"]) for s in te]))
    always_gains = [s["cf_up"] for s in te]
    rule_gains = [s["cf_up"] if rule_up_better(s) else s["cf_hold"] for s in te]
    model_gains = [s["cf_up"] if p >= tau else s["cf_hold"] for s, p in zip(te, p_te)]
    always_cf = float(np.mean(always_gains))
    rule_cf = float(np.mean(rule_gains))
    model_cf = float(np.mean(model_gains))

    # weak-sales only metrics on test
    weak_idx = [i for i, s in enumerate(te) if s["weak"]]
    weak_metrics = None
    if weak_idx:
        wh = float(np.mean([te[i]["cf_hold"] for i in weak_idx]))
        wo = float(np.mean([max(te[i]["cf_hold"], te[i]["cf_up"]) for i in weak_idx]))
        wa = float(np.mean([te[i]["cf_up"] for i in weak_idx]))
        wr = float(np.mean([te[i]["cf_up"] if rule_up_better(te[i]) else te[i]["cf_hold"] for i in weak_idx]))
        wm = float(np.mean([te[i]["cf_up"] if p_te[i] >= tau else te[i]["cf_hold"] for i in weak_idx]))
        weak_metrics = {
            "n": len(weak_idx),
            "up_better_rate": float(np.mean([te[i]["y"] for i in weak_idx])),
            "hold": wh,
            "oracle": wo,
            "always_up": wa,
            "rule": wr,
            "model": wm,
            "model_vs_hold_pct": (wm / wh - 1) * 100 if wh else None,
            "always_vs_hold_pct": (wa / wh - 1) * 100 if wh else None,
            "rule_vs_hold_pct": (wr / wh - 1) * 100 if wh else None,
            "oracle_vs_hold_pct": (wo / wh - 1) * 100 if wh else None,
            "model_vs_always_pct": (wm / wa - 1) * 100 if wa else None,
        }

    policies = {
        "hold": S.policy_hold,
        "v9_core": lambda st, obs, dm: S.policy_v9_core(st, obs, dm),
        "always_up_under": make_always_up_under_policy(),
        "cf_up_model": make_cf_up_policy(clf, tau=tau),
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
            "up_better_rate_train": float(np.mean(ytr)),
            "up_better_rate_test": float(np.mean(yte)),
            "auc_val": auc_va,
            "auc_test": auc_te,
            "tau": tau,
            "test_mean_cf_hold": base_hold,
            "test_mean_cf_oracle": oracle,
            "test_mean_cf_always_up": always_cf,
            "test_mean_cf_rule": rule_cf,
            "test_mean_cf_model": model_cf,
            "model_vs_hold_pct": (model_cf / base_hold - 1) * 100 if base_hold else None,
            "always_vs_hold_pct": (always_cf / base_hold - 1) * 100 if base_hold else None,
            "rule_vs_hold_pct": (rule_cf / base_hold - 1) * 100 if base_hold else None,
            "oracle_vs_hold_pct": (oracle / base_hold - 1) * 100 if base_hold else None,
            "model_vs_always_pct": (model_cf / always_cf - 1) * 100 if always_cf else None,
        },
        "cf_weak_sales": weak_metrics,
        "trajectory_test": traj,
        "note": "CF UP vs HOLD on understock (load<0.18). Weak = sales<2. Synth rails 0.75–1.05×mkt.",
    }
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(summary, indent=2, ensure_ascii=False))
    try:
        import joblib

        joblib.dump({"clf": clf, "feats": FEATS, "tau": tau, "kind": "cf_up"}, OUT_DIR / "cf_model.joblib")
    except Exception as e:
        print("joblib", e)

    print(json.dumps(summary["cf"], indent=2))
    print("weak_sales:", json.dumps(weak_metrics, indent=2))
    print("trajectory x_v9:")
    for k, v in sorted(traj.items(), key=lambda kv: -kv[1]["profit"]):
        print(f"  {k:20} profit={v['profit']:.0f}  x_v9={v['x_v9']:.3f}  down={v['downs']} up={v['ups']}")
    print("wrote", OUT)


if __name__ == "__main__":
    main()
