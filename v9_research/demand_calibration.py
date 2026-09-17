#!/usr/bin/env python3
"""
Demand OOS calibration for BookDemand (research).

Measures how well expected_sales(logged price) tracks logged sales on
walk-forward OOS — before trusting structure-search lift.
Does NOT change production Go.
"""
from __future__ import annotations

import json
import math
import os
import sqlite3
import sys
from collections import defaultdict
from typing import Any, Dict, List, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F

DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get(
    "DEMAND_CAL_OUT",
    os.path.join(os.path.dirname(__file__), "demand_calibration.json"),
)
T1 = os.environ.get("STRUCT_T1", "2026-09-17")


def _pearson(xs: List[float], ys: List[float]) -> float:
    n = len(xs)
    if n < 3:
        return float("nan")
    mx = sum(xs) / n
    my = sum(ys) / n
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    dx = math.sqrt(sum((x - mx) ** 2 for x in xs))
    dy = math.sqrt(sum((y - my) ** 2 for y in ys))
    if dx < 1e-12 or dy < 1e-12:
        return float("nan")
    return num / (dx * dy)


def calibrate_fold(rows_fit: List[dict], rows_oos: List[dict], nac: Dict[str, int]) -> Dict[str, Any]:
    dm = F.BookDemand()
    dm.fit(rows_fit, nac)
    pred, actual = [], []
    by_rb: Dict[str, List[Tuple[float, float]]] = defaultdict(list)
    by_depth: Dict[str, List[Tuple[float, float]]] = defaultdict(list)
    by_item: Dict[str, List[Tuple[float, float]]] = defaultdict(list)
    ah_only = 0
    for r in rows_oos:
        if r.get("held", 0) <= 0:
            continue
        mkt = r.get("mkt") or r.get("p10")
        depth = r.get("depth") or "ok"
        yhat = dm.expected_sales(
            r["item_id"], r["price_before"], mkt, r["held"], r["share"] or 12, r["ts"], depth=depth
        )
        y = float(r["sales"])
        pred.append(yhat)
        actual.append(y)
        rb = S.ratio_bucket(r.get("ratio"))
        by_rb[rb].append((yhat, y))
        by_depth[depth].append((yhat, y))
        by_item[r["item_id"]].append((yhat, y))
        if r.get("mkt_source") == "ah_p10":
            ah_only += 1

    def pack(pairs: List[Tuple[float, float]]) -> Dict[str, float]:
        if not pairs:
            return {"n": 0}
        yh = [a for a, _ in pairs]
        y = [b for _, b in pairs]
        mae = sum(abs(a - b) for a, b in pairs) / len(pairs)
        bias = sum(a - b for a, b in pairs) / len(pairs)
        return {
            "n": len(pairs),
            "mae": round(mae, 4),
            "bias_pred_minus_act": round(bias, 4),
            "mean_pred": round(sum(yh) / len(yh), 4),
            "mean_act": round(sum(y) / len(y), 4),
            "corr": round(_pearson(yh, y), 4) if len(pairs) >= 3 else None,
        }

    overall = pack(list(zip(pred, actual)))
    overall["ah_p10_share"] = round(ah_only / max(len(pred), 1), 3)
    return {
        "overall": overall,
        "by_ratio_bucket": {k: pack(v) for k, v in sorted(by_rb.items())},
        "by_depth": {k: pack(v) for k, v in sorted(by_depth.items())},
        "by_item": {k: pack(v) for k, v in sorted(by_item.items(), key=lambda x: -len(x[1]))},
    }


def main():
    print(f"=== DEMAND CALIBRATION db={DB} ===", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1=T1)
    print(f"raw={len(rows)} attaching AH…", flush=True)
    rows = F.attach_ah_market(con, rows)
    # seller_n if available
    if hasattr(F, "attach_seller_n"):
        rows = F.attach_seller_n(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    folds = F.walk_forward_folds(rows, n_folds=2)
    out = {"folds": [], "meta": {"n_rows": len(rows), "db": DB, "t0": F.BOOK_T0, "t1": T1}}
    for fd in folds:
        fit = F.filter_days(rows, fd["train_fit"])
        oos = F.filter_days(rows, fd["oos"])
        print(f"fold{fd['fold']} fit={fd['train_fit_range']} oos={fd['oos_range']} n_oos={len(oos)}", flush=True)
        cal = calibrate_fold(fit, oos, nac)
        o = cal["overall"]
        print(
            f"  overall mae={o.get('mae')} bias={o.get('bias_pred_minus_act')} "
            f"corr={o.get('corr')} mean_act={o.get('mean_act')} mean_pred={o.get('mean_pred')} "
            f"ah={o.get('ah_p10_share')}",
            flush=True,
        )
        for rb, p in cal["by_ratio_bucket"].items():
            if p.get("n", 0) >= 20:
                print(f"    rb={rb}: n={p['n']} mae={p['mae']} bias={p['bias_pred_minus_act']} corr={p['corr']}", flush=True)
        out["folds"].append({"fold": fd["fold"], "ranges": {"fit": fd["train_fit_range"], "oos": fd["oos_range"]}, **cal})

    # late half
    days = sorted({r["ts"][:10] for r in rows})
    cut = max(4, len(days) // 2)
    late = calibrate_fold(F.filter_days(rows, set(days[:cut])), F.filter_days(rows, set(days[cut:])), nac)
    out["late_half"] = late
    o = late["overall"]
    print(
        f"late_half mae={o.get('mae')} bias={o.get('bias_pred_minus_act')} corr={o.get('corr')}",
        flush=True,
    )
    with open(OUT, "w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    print(f"WROTE {OUT}", flush=True)


if __name__ == "__main__":
    main()
