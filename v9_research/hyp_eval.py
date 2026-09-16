#!/usr/bin/env python3
"""
Evaluate focused pricing hypotheses on fidelity simulator.

Usage:
  PRICING_DB=/root/4narek-new/ml_data/pricing.db python3 -u hyp_eval.py

Output: hyp_eval_results.json
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time
from dataclasses import asdict
from typing import Any, Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom

DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("HYP_OUT", os.path.join(os.path.dirname(__file__), "hyp_eval_results.json"))


def eval_one(ch: Chrom, dm: F.BookDemand, by_rows) -> dict:
    return F.simulate_fidelity(by_rows, ch, dm)


def main():
    t0 = time.time()
    print(f"=== HYP EVAL db={DB} ===", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row

    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1="2026-09-17")
    print(f"raw={len(rows)} attaching AH…", flush=True)
    rows = F.attach_ah_market(con, rows)
    ah = sum(1 for r in rows if r.get("mkt_source") == "ah_p10")
    print(f"cycles={len(rows)} ah_p10={ah} ({100*ah/max(len(rows),1):.1f}%)", flush=True)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)

    hyps = F.make_hypotheses()
    folds = F.walk_forward_folds(rows, n_folds=2)
    print("folds:", flush=True)
    for fd in folds:
        print(f"  fold{fd['fold']} fit={fd['train_fit_range']} val={fd['val_range']} oos={fd['oos_range']}", flush=True)

    # late half
    days = sorted({r["ts"][:10] for r in rows})
    cut = max(4, len(days) // 2)
    late_fit, late_eval = set(days[:cut]), set(days[cut:])
    dm_late = F.BookDemand()
    dm_late.fit(F.filter_days(rows, late_fit), nac)
    by_late = S.split_by_item(F.filter_days(rows, late_eval))
    v9_late = eval_one(next(h for h in hyps if h.name == "v9"), dm_late, by_late)
    print(
        f"v9 late 24h={v9_late['profit_24h_mean_m']:.1f}M under={v9_late['under']:.3f} "
        f"book={v9_late['book_ok_frac']:.2f} pure_cf={v9_late['pure_cf_frac']:.2f}",
        flush=True,
    )

    fold_dms = []
    for fd in folds:
        dm = F.BookDemand()
        dm.fit(F.filter_days(rows, fd["train_fit"]), nac)
        by_oos = S.split_by_item(F.filter_days(rows, fd["oos"]))
        v9 = eval_one(next(h for h in hyps if h.name == "v9"), dm, by_oos)
        fold_dms.append((fd, dm, by_oos, v9))
        print(
            f"fold{fd['fold']} v9 OOS 24h={v9['profit_24h_mean_m']:.1f} under={v9['under']:.3f}",
            flush=True,
        )

    results = []
    for ch in hyps:
        row: Dict[str, Any] = {"name": ch.name, "chrom": asdict(ch)}
        xs, oks = [], []
        for fd, dm, by_oos, v9 in fold_dms:
            m = eval_one(ch, dm, by_oos)
            x = m["profit_24h_mean_m"] / max(v9["profit_24h_mean_m"], 1e-6)
            ok = F.constrained_ok(m, v9) and x >= 0.98  # allow tiny noise
            row[f"f{fd['fold']}_x"] = round(x, 3)
            row[f"f{fd['fold']}_24h"] = round(m["profit_24h_mean_m"], 2)
            row[f"f{fd['fold']}_under"] = round(m["under"], 3)
            row[f"f{fd['fold']}_ups"] = m["ups"]
            row[f"f{fd['fold']}_downs"] = m["downs"]
            row[f"f{fd['fold']}_ok"] = ok
            xs.append(x)
            oks.append(ok)
        m = eval_one(ch, dm_late, by_late)
        late_x = m["profit_24h_mean_m"] / max(v9_late["profit_24h_mean_m"], 1e-6)
        late_ok = F.constrained_ok(m, v9_late) and late_x >= 1.0
        row["late_x"] = round(late_x, 3)
        row["late_24h"] = round(m["profit_24h_mean_m"], 2)
        row["late_under"] = round(m["under"], 3)
        row["late_ups"] = m["ups"]
        row["late_downs"] = m["downs"]
        row["late_ok"] = late_ok
        row["n_folds_ok"] = sum(1 for o in oks if o)
        row["min_x"] = round(min(xs), 3) if xs else None
        row["mean_x"] = round(sum(xs) / len(xs), 3) if xs else None
        # robust: all folds ok with min_x>=1.02 and late>=1.02
        row["robust"] = (
            all(oks)
            and (min(xs) if xs else 0) >= 1.02
            and late_x >= 1.02
            and late_ok
        )
        # soft-robust: never worse on folds, clear late lift
        row["dominates"] = (
            all(oks)
            and (min(xs) if xs else 0) >= 0.99
            and late_x >= 1.05
            and late_ok
            and m["under"] <= v9_late["under"] + 0.01
        )
        results.append(row)
        print(
            f"{ch.name:24} min_x={row['min_x']} late={row['late_x']} "
            f"folds_ok={row['n_folds_ok']} robust={row['robust']} dom={row['dominates']} "
            f"under_late={row['late_under']}",
            flush=True,
        )

    robust = [r for r in results if r["robust"]]
    robust.sort(key=lambda r: (-r["late_x"], -r["min_x"]))
    dominates = [r for r in results if r.get("dominates") and r["name"] != "v9"]
    dominates.sort(key=lambda r: (-r["late_x"], -r["min_x"]))
    # also rank by late among constraint-ok
    candidates = [r for r in results if r["late_ok"] and r["n_folds_ok"] >= len(folds) and r["name"] != "v9"]
    candidates.sort(key=lambda r: (-r["late_x"], -r["min_x"]))

    elapsed = time.time() - t0
    best = robust[0] if robust else (dominates[0] if dominates else None)
    out = {
        "meta": {
            "db": DB,
            "elapsed_sec": round(elapsed, 1),
            "n_cycles": len(rows),
            "ah_p10_frac": round(ah / max(len(rows), 1), 3),
            "sim": "sim_fidelity BookDemand + pure CF when price moves",
            "robust_gate": "all folds ok ∧ min_x≥1.02 ∧ late≥1.02 ∧ under constraint",
            "dominates_gate": "folds ok ∧ min≥0.99 ∧ late≥1.05 ∧ under≤v9+0.01",
            "v9_late_24h_m": round(v9_late["profit_24h_mean_m"], 2),
            "v9_late_under": round(v9_late["under"], 3),
        },
        "folds": [
            {"fold": fd["fold"], "oos": fd["oos_range"], "v9_24h": round(v9["profit_24h_mean_m"], 2), "v9_under": round(v9["under"], 3)}
            for fd, _, _, v9 in fold_dms
        ],
        "results": results,
        "robust_winners": robust,
        "dominates": dominates,
        "best": best,
        "best_note": (
            None
            if best
            else "NO_WINNER — keep v9"
        ),
        "near_misses": candidates[:5],
    }
    with open(OUT, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {OUT} in {elapsed:.0f}s", flush=True)
    if best:
        tag = "ROBUST" if best.get("robust") else "DOMINATES"
        print(f"BEST {tag} {best['name']} late={best['late_x']} min={best['min_x']}", flush=True)
    else:
        print("NO WINNER", flush=True)
        if candidates:
            print(f"near: {candidates[0]['name']} late={candidates[0]['late_x']} min={candidates[0]['min_x']}", flush=True)
    print("DONE", flush=True)


if __name__ == "__main__":
    main()
