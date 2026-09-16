#!/usr/bin/env python3
"""
Open hypothesis grid — no sacred cows.

Baseline: v9_h1 (down_block=0.95, nac=1).
Allowed: nac_mult ≠ 1, multi-step / gap jumps (market_follow), variable step_mult.

Reports for each chrom:
  late_x / fold_x — raw profit vs v9_h1 (what we care about if nac is "allowed")
  margin_adj_x — late profit / nac_mult  vs  v9 late (strips pure margin scale)
  under — underprice fraction

Does NOT change production Go.
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time
from copy import deepcopy
from dataclasses import asdict
from typing import Any, Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom, chrom_v9
from search_free import chrom_v9_live, simulate_free

DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("OPEN_OUT", os.path.join(os.path.dirname(__file__), "hyp_open_results.json"))


def make_open_hyps() -> List[Chrom]:
    out: List[Chrom] = []
    base = chrom_v9_live()
    out.append(base)

    # --- market_follow / gap jump to p10 (non-fixed step) ---
    for pull in [0.5, 0.75, 1.0]:
        for um in [1.0, 2.0, 3.0, 5.0]:
            for dbr in [0.95, 1.01]:
                c = chrom_v9_live()
                c.name = f"MF_p{pull}_u{um}_d{dbr}"
                c.style = "market_follow"
                c.mf_pull = pull
                c.up_mult = um
                c.down_soft_mult = max(1.0, um * 0.5)
                c.down_block_ratio = dbr
                c.soft_down_when_over_mkt = True
                out.append(c)

    # --- catchup snaps empty underprice toward market with big steps ---
    for um in [2.0, 3.0, 5.0]:
        for gap in [0.90, 0.95, 1.0]:
            c = chrom_v9_live()
            c.name = f"CU_u{um}_g{gap}"
            c.catchup_on = True
            c.catchup_gap = gap
            c.empty_streak = 1
            c.up_mult = um
            c.step_mult = 1.0
            out.append(c)

    # --- variable step_mult (base step ≠ catalog step) ---
    for sm in [0.5, 1.5, 2.0, 3.0]:
        for um in [1.0, 2.0, 3.0]:
            c = chrom_v9_live()
            c.name = f"STEP_s{sm}_u{um}"
            c.step_mult = sm
            c.up_mult = um
            c.down_soft_mult = um
            c.down_hard_mult = max(2.0, um)
            out.append(c)

    # --- nac free (user: try if effective) ---
    for nac in [0.70, 0.85, 1.15, 1.30, 1.50]:
        for style, pull in [("corridor", 0.0), ("market_follow", 1.0)]:
            c = chrom_v9_live()
            c.name = f"NAC_{nac}_{style[:2]}_p{pull}"
            c.nac_mult = nac
            c.style = style
            c.mf_pull = pull
            c.up_mult = 3.0 if style == "market_follow" else 1.0
            c.down_block_ratio = 0.95
            out.append(c)

    # --- combos: MF + nac + big step (most "open") ---
    for nac in [1.0, 1.2, 1.5]:
        for pull in [0.75, 1.0]:
            for um in [2.0, 5.0]:
                c = chrom_v9_live()
                c.name = f"OPEN_n{nac}_p{pull}_u{um}"
                c.nac_mult = nac
                c.style = "market_follow"
                c.mf_pull = pull
                c.up_mult = um
                c.down_soft_mult = 2.0
                c.down_hard_mult = 3.0
                c.down_block_ratio = 0.95
                c.soft_down_when_over_mkt = True
                c.catchup_on = True
                c.empty_streak = 1
                c.catchup_gap = 1.0
                out.append(c)

    # dedupe by name
    seen = set()
    uniq = []
    for c in out:
        if c.name in seen:
            continue
        seen.add(c.name)
        uniq.append(c)
    return uniq


def main():
    t0 = time.time()
    print(f"=== OPEN HYP EVAL db={DB} ===", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1="2026-09-17")
    print(f"raw={len(rows)} attaching AH…", flush=True)
    rows = F.attach_ah_market(con, rows)
    ah = sum(1 for r in rows if r.get("mkt_source") == "ah_p10")
    print(f"cycles={len(rows)} ah={ah} ({100*ah/max(len(rows),1):.1f}%)", flush=True)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)

    folds = F.walk_forward_folds(rows, n_folds=2)
    for fd in folds:
        print(f"  fold{fd['fold']} fit={fd['train_fit_range']} oos={fd['oos_range']}", flush=True)

    days = sorted({r["ts"][:10] for r in rows})
    cut = max(4, len(days) // 2)
    late_fit, late_eval = set(days[:cut]), set(days[cut:])
    dm_late = F.BookDemand()
    dm_late.fit(F.filter_days(rows, late_fit), nac)
    by_late = S.split_by_item(F.filter_days(rows, late_eval))

    fold_pack = []
    for fd in folds:
        dm = F.BookDemand()
        dm.fit(F.filter_days(rows, fd["train_fit"]), nac)
        by = S.split_by_item(F.filter_days(rows, fd["oos"]))
        fold_pack.append((fd, dm, by))

    hyps = make_open_hyps()
    base = next(h for h in hyps if h.name == "v9_h1")
    v9_late = simulate_free(by_late, base, dm_late)
    print(
        f"v9_h1 late 24h={v9_late['profit_24h_mean_m']:.1f}M under={v9_late['under']:.3f}",
        flush=True,
    )
    v9_folds = []
    for fd, dm, by in fold_pack:
        m = simulate_free(by, base, dm)
        v9_folds.append(m)
        print(f"  fold{fd['fold']} v9 24h={m['profit_24h_mean_m']:.1f} under={m['under']:.3f}", flush=True)

    print(f"hyps={len(hyps)}", flush=True)
    results: List[Dict[str, Any]] = []
    for i, ch in enumerate(hyps):
        row: Dict[str, Any] = {"name": ch.name, "chrom": asdict(ch)}
        xs, oks = [], []
        for fi, (fd, dm, by) in enumerate(fold_pack):
            m = simulate_free(by, ch, dm)
            v9 = v9_folds[fi]
            x = m["profit_24h_mean_m"] / max(v9["profit_24h_mean_m"], 1e-6)
            ok = F.constrained_ok(m, v9) and x >= 0.95
            row[f"f{fd['fold']}_x"] = round(x, 3)
            row[f"f{fd['fold']}_under"] = round(m["under"], 3)
            row[f"f{fd['fold']}_24h"] = round(m["profit_24h_mean_m"], 2)
            xs.append(x)
            oks.append(ok)
        m = simulate_free(by_late, ch, dm_late)
        late_x = m["profit_24h_mean_m"] / max(v9_late["profit_24h_mean_m"], 1e-6)
        late_ok = F.constrained_ok(m, v9_late) and late_x >= 0.98
        # margin-adjusted: strips linear nac scale (still keeps floor/decision effects of nac)
        margin_adj = (m["profit_24h_mean_m"] / max(ch.nac_mult, 1e-6)) / max(
            v9_late["profit_24h_mean_m"], 1e-6
        )
        row["late_x"] = round(late_x, 3)
        row["late_24h"] = round(m["profit_24h_mean_m"], 2)
        row["late_under"] = round(m["under"], 3)
        row["late_ups"] = m["ups"]
        row["late_downs"] = m["downs"]
        row["margin_adj_x"] = round(margin_adj, 3)
        row["nac_mult"] = ch.nac_mult
        row["min_x"] = round(min(xs), 3)
        row["mean_x"] = round(sum(xs) / len(xs), 3)
        row["n_folds_ok"] = sum(1 for o in oks if o)
        row["robust"] = all(oks) and min(xs) >= 1.02 and late_x >= 1.02 and late_ok
        row["dominates"] = (
            all(oks)
            and min(xs) >= 0.99
            and late_x >= 1.05
            and late_ok
            and m["under"] <= v9_late["under"] + 0.02
        )
        # "algo signal": margin_adj late ≥ 1.05 and folds not broken
        row["algo_signal"] = (
            all(oks) and min(xs) >= 0.98 and margin_adj >= 1.05 and m["under"] <= v9_late["under"] + 0.03
        )
        results.append(row)
        if (i + 1) % 20 == 0:
            print(f"  … {i+1}/{len(hyps)}", flush=True)

    results.sort(
        key=lambda r: (
            -int(r["robust"]),
            -int(r["dominates"]),
            -int(r["algo_signal"]),
            -r["late_x"],
            -r["margin_adj_x"],
        )
    )

    elapsed = time.time() - t0
    payload = {
        "meta": {
            "elapsed_s": round(elapsed, 1),
            "baseline": "v9_h1",
            "n_hyps": len(hyps),
            "note": "nac and non-fixed steps allowed; margin_adj_x = late/(nac_mult)/v9",
        },
        "v9_h1_late": v9_late,
        "robust": [r for r in results if r["robust"]][:15],
        "dominates": [r for r in results if r["dominates"] and r["name"] != "v9_h1"][:20],
        "algo_signal": [r for r in results if r["algo_signal"] and r["name"] != "v9_h1"][:20],
        "top_raw_late": results[:25],
        "top_margin_adj": sorted(results, key=lambda r: -r["margin_adj_x"])[:20],
        "all": results,
    }
    with open(OUT, "w") as f:
        json.dump(payload, f, ensure_ascii=False, indent=2)

    print(f"\n=== DONE {elapsed:.0f}s → {OUT} ===", flush=True)
    print(f"robust={len(payload['robust'])} dominates={len(payload['dominates'])} "
          f"algo_signal={len(payload['algo_signal'])}", flush=True)
    print("\nTOP raw late:", flush=True)
    for r in results[:12]:
        print(
            f"  {r['name']:28s} late×{r['late_x']:.2f} adj×{r['margin_adj_x']:.2f} "
            f"nac={r['nac_mult']} under={r['late_under']:.3f} "
            f"minF={r['min_x']:.2f} R={r['robust']} D={r['dominates']} A={r['algo_signal']}",
            flush=True,
        )
    print("\nTOP margin_adj (algo-ish):", flush=True)
    for r in payload["top_margin_adj"][:10]:
        print(
            f"  {r['name']:28s} adj×{r['margin_adj_x']:.2f} late×{r['late_x']:.2f} "
            f"nac={r['nac_mult']} under={r['late_under']:.3f}",
            flush=True,
        )


if __name__ == "__main__":
    main()
