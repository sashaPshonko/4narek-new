#!/usr/bin/env python3
"""Train DOWN-vs-HOLD scorer for v10 excess states.

Data: capital_cycles with fwd_profit_* (prefer 4narek-ml VPS copy).
Approach:
  1) Reward model R(x, action) ≈ fwd_profit_1+2+3
  2) Score δ = R(x, DOWN) − R(x, HOLD); ↓ if δ > τ
  3) Time-based OOS; compare vs turn-baseline and always-HOLD / logged action

Selection bias remains — treat as ranking prior, calibrate τ on OOS profit of
rows where logged action matches the policy (or report both).

Usage:
  .venv/bin/python train_v10_down_hold.py
  .venv/bin/python train_v10_down_hold.py --db /path/to/pricing.db
"""
from __future__ import annotations

import argparse
import json
import os
import sqlite3
from dataclasses import asdict
from datetime import datetime
from pathlib import Path

import numpy as np
from sklearn.ensemble import GradientBoostingClassifier, GradientBoostingRegressor
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import StandardScaler

ROOT = Path(__file__).resolve().parent
DEFAULT_DB = Path("/Users/sasha_pshonko/Documents/4narek/4narek-ml/data/pricing.db")
OUT_DIR = ROOT / "v10_down_hold_model"

FEATS = [
    "held",
    "share",
    "load",
    "sales",
    "buys",
    "try_sells",
    "turn",
    "doi",
    "try_ratio",
    "stock_load",
    "price",
    "step",
    "nacenka",
    "night",
    "fill",
]


def fam(action: str) -> str | None:
    a = (action or "").lower()
    if "price_down" in a or a.endswith("_down") or "_down_" in a:
        if "hold" in a and "down_cd" in a:
            return "HOLD"
        return "DOWN"
    if "hold" in a:
        return "HOLD"
    if "price_up" in a or "_up" in a:
        return "UP"
    return None


def load_excess(db: Path, min_load: float = 0.25):
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    rows = con.execute(
        """
        SELECT ts, item_id, action,
               held, share, sales, buys, try_sells, try_ratio, stock_load, fill,
               price_before, step, nacenka_before,
               fwd_profit_1, fwd_profit_2, fwd_profit_3
        FROM capital_cycles
        WHERE fwd_profit_1 IS NOT NULL
          AND held IS NOT NULL AND held > 0
          AND coalesce(stock_load, 0) >= ?
        ORDER BY ts
        """,
        (min_load,),
    ).fetchall()
    con.close()
    data = []
    for r in rows:
        f = fam(r["action"])
        if f not in ("DOWN", "HOLD"):
            continue
        held = int(r["held"] or 0)
        sales = int(r["sales"] or 0)
        buys = int(r["buys"] or 0)
        try_s = int(r["try_sells"] or 0)
        share = max(int(r["share"] or 0), 1)
        turn = sales / max(held, 1)
        doi = held / max(sales, 0.25)
        try_ratio = float(r["try_ratio"] or (try_s / max(sales, 1)))
        stock_load = float(r["stock_load"] or held / share)
        price = int(r["price_before"] or 0)
        step = max(int(r["step"] or 1), 1)
        nac = int(r["nacenka_before"] or 0)
        fill = float(r["fill"] or 0)
        ts = r["ts"] or ""
        night = 0
        try:
            hour = int(ts[11:13])
            # MSK ≈ UTC+3 → rough night 00–06 UTC as proxy for 03–09 MSK
            night = 1 if 0 <= hour < 6 else 0
        except Exception:
            pass
        f3 = float(r["fwd_profit_1"] or 0) + float(r["fwd_profit_2"] or 0) + float(r["fwd_profit_3"] or 0)
        data.append(
            {
                "ts": ts,
                "item": r["item_id"],
                "fam": f,
                "y_down": 1 if f == "DOWN" else 0,
                "fwd3": f3,
                "held": held,
                "share": share,
                "load": held / share,
                "sales": sales,
                "buys": buys,
                "try_sells": try_s,
                "turn": turn,
                "doi": doi,
                "try_ratio": try_ratio,
                "stock_load": stock_load,
                "price": price,
                "step": step,
                "nacenka": nac,
                "night": night,
                "fill": fill,
            }
        )
    return data


def matrix(rows, feats=FEATS):
    X = np.array([[float(r[f]) for f in feats] for r in rows], dtype=np.float64)
    X = np.nan_to_num(X, nan=0.0, posinf=1e6, neginf=0.0)
    return X


def time_split(rows, train_frac=0.6, val_frac=0.2):
    n = len(rows)
    i1 = int(n * train_frac)
    i2 = int(n * (train_frac + val_frac))
    return rows[:i1], rows[i1:i2], rows[i2:]


def turn_policy(r, turn_low=0.08, turn_high=0.25) -> int:
    """1 = DOWN, 0 = HOLD."""
    if r["turn"] < turn_low:
        return 1
    if r["stock_load"] >= 0.50 and r["turn"] < turn_high:
        return 1
    if r["stock_load"] >= 0.35 and r["turn"] < (turn_low + turn_high) / 2:
        return 1
    return 0


def eval_policy(rows, pred_down: np.ndarray, name: str):
    """Among rows where logged action equals policy, mean fwd3; also coverage."""
    agree = []
    would_down = []
    logged_down_fwd = []
    logged_hold_fwd = []
    for r, p in zip(rows, pred_down):
        want = int(p)
        would_down.append(want)
        if r["y_down"] == want:
            agree.append(r["fwd3"])
        if r["y_down"] == 1:
            logged_down_fwd.append(r["fwd3"])
        else:
            logged_hold_fwd.append(r["fwd3"])
    agree = np.array(agree) if agree else np.array([0.0])
    return {
        "name": name,
        "n": len(rows),
        "pred_down_rate": float(np.mean(would_down)) if would_down else 0.0,
        "agree_n": int(np.sum([r["y_down"] == int(p) for r, p in zip(rows, pred_down)])),
        "agree_mean_fwd3": float(np.mean(agree)),
        "logged_down_mean_fwd3": float(np.mean(logged_down_fwd)) if logged_down_fwd else None,
        "logged_hold_mean_fwd3": float(np.mean(logged_hold_fwd)) if logged_hold_fwd else None,
        # proxy: when policy says DOWN, mean of logged DOWN in that subset vs HOLD subset
        "when_pred_down_logged_down_fwd3": _subset_mean(rows, pred_down, want=1, logged=1),
        "when_pred_down_logged_hold_fwd3": _subset_mean(rows, pred_down, want=1, logged=0),
        "when_pred_hold_logged_down_fwd3": _subset_mean(rows, pred_down, want=0, logged=1),
        "when_pred_hold_logged_hold_fwd3": _subset_mean(rows, pred_down, want=0, logged=0),
    }


def _subset_mean(rows, pred, want, logged):
    xs = [r["fwd3"] for r, p in zip(rows, pred) if int(p) == want and r["y_down"] == logged]
    return float(np.mean(xs)) if xs else None


def train(db: Path, out_dir: Path):
    rows = load_excess(db)
    if len(rows) < 500:
        raise SystemExit(f"too few excess rows: {len(rows)} from {db}")
    train, val, test = time_split(rows)
    print(f"loaded excess DOWN/HOLD n={len(rows)} train={len(train)} val={len(val)} test={len(test)}")
    print(f"  span {rows[0]['ts']} → {rows[-1]['ts']}")

    Xtr, Xva, Xte = matrix(train), matrix(val), matrix(test)
    ytr = np.array([r["y_down"] for r in train])
    yva = np.array([r["y_down"] for r in val])
    yte = np.array([r["y_down"] for r in test])
    rtr = np.array([r["fwd3"] for r in train], dtype=np.float64)
    # clip rewards for stability
    lo, hi = np.percentile(rtr, [1, 99])
    rtr_c = np.clip(rtr, lo, hi)

    # A) classifier: P(logged DOWN | x) — propensity / imitation
    clf = Pipeline(
        [
            ("sc", StandardScaler()),
            ("lr", LogisticRegression(max_iter=2000, class_weight="balanced")),
        ]
    )
    clf.fit(Xtr, ytr)
    p_im_va = clf.predict_proba(Xva)[:, 1]
    p_im_te = clf.predict_proba(Xte)[:, 1]
    auc_va = roc_auc_score(yva, p_im_va) if len(set(yva)) > 1 else float("nan")
    auc_te = roc_auc_score(yte, p_im_te) if len(set(yte)) > 1 else float("nan")

    # B) reward models R(x|DOWN) and R(x|HOLD) via one model with action feature
    def aug(X, a):
        return np.hstack([X, a.reshape(-1, 1)])

    a_tr = ytr.astype(np.float64)
    reg = GradientBoostingRegressor(random_state=0, max_depth=3, n_estimators=150, learning_rate=0.05)
    reg.fit(aug(Xtr, a_tr), rtr_c)

    def delta_scores(X):
        r_down = reg.predict(aug(X, np.ones(len(X))))
        r_hold = reg.predict(aug(X, np.zeros(len(X))))
        return r_down - r_hold, r_down, r_hold

    # calibrate τ on val: maximize agree_mean_fwd3 among agree rows, with pred_down_rate in [0.15, 0.7]
    d_va, _, _ = delta_scores(Xva)
    best = None
    for tau in np.linspace(np.percentile(d_va, 20), np.percentile(d_va, 80), 25):
        pred = (d_va > tau).astype(int)
        ev = eval_policy(val, pred, f"tau={tau:.0f}")
        if not (0.15 <= ev["pred_down_rate"] <= 0.70):
            continue
        score = ev["agree_mean_fwd3"]
        if best is None or score > best[0]:
            best = (score, float(tau), ev)
    if best is None:
        tau = float(np.median(d_va))
        best = (0.0, tau, eval_policy(val, (d_va > tau).astype(int), "fallback"))
    else:
        tau = best[1]

    d_te, rd_te, rh_te = delta_scores(Xte)
    pred_model = (d_te > tau).astype(int)
    pred_turn = np.array([turn_policy(r) for r in test])
    pred_im = (p_im_te >= 0.5).astype(int)
    pred_always_hold = np.zeros(len(test), dtype=int)
    pred_always_down = np.ones(len(test), dtype=int)

    reports = [
        eval_policy(test, pred_model, "reward_delta"),
        eval_policy(test, pred_turn, "turn_baseline"),
        eval_policy(test, pred_im, "imitate_logged_down"),
        eval_policy(test, pred_always_hold, "always_hold"),
        eval_policy(test, pred_always_down, "always_down"),
    ]

    # matched-style: within turn buckets, how often model prefers DOWN when turn low
    by_turn = {}
    for r, p, d in zip(test, pred_model, d_te):
        b = "low" if r["turn"] < 0.08 else ("mid" if r["turn"] < 0.25 else "high")
        by_turn.setdefault(b, {"n": 0, "pred_down": 0, "delta_sum": 0.0})
        by_turn[b]["n"] += 1
        by_turn[b]["pred_down"] += int(p)
        by_turn[b]["delta_sum"] += float(d)
    for b, v in by_turn.items():
        v["pred_down_rate"] = v["pred_down"] / max(v["n"], 1)
        v["mean_delta"] = v["delta_sum"] / max(v["n"], 1)
        del v["delta_sum"]

    out_dir.mkdir(parents=True, exist_ok=True)
    # persist lightweight artifacts (coefficients + trees via joblib if available)
    try:
        import joblib

        joblib.dump(
            {
                "feats": FEATS,
                "clf_imitation": clf,
                "reg_reward": reg,
                "tau": tau,
                "reward_clip": [float(lo), float(hi)],
                "min_load": 0.25,
            },
            out_dir / "model.joblib",
        )
    except Exception as e:
        print("joblib save skip:", e)

    summary = {
        "db": str(db),
        "generated": datetime.utcnow().isoformat() + "Z",
        "n_total": len(rows),
        "n_train": len(train),
        "n_val": len(val),
        "n_test": len(test),
        "span": [rows[0]["ts"], rows[-1]["ts"]],
        "imitation_auc_val": auc_va,
        "imitation_auc_test": auc_te,
        "tau": tau,
        "val_best": best[2],
        "test_reports": reports,
        "test_by_turn": by_turn,
        "note": "Observational reward model — biased by logged policy; use as prior + turn baseline.",
    }
    (out_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False))
    print(json.dumps({"tau": tau, "auc_te": auc_te, "reports": reports, "by_turn": by_turn}, indent=2))
    print(f"wrote {out_dir / 'summary.json'}")
    return summary


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", type=Path, default=DEFAULT_DB)
    ap.add_argument("--out", type=Path, default=OUT_DIR)
    args = ap.parse_args()
    if not args.db.exists():
        raise SystemExit(f"missing db {args.db}")
    train(args.db, args.out)


if __name__ == "__main__":
    main()
