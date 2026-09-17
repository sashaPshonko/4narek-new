#!/usr/bin/env python3
"""
PPSL seed smoke vs Chrom baselines on fidelity AH panel (research).
Does NOT change production Go.
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_free import chrom_v9_live
from search_ultra import simulate_ultra
from search_structure import simulate_ppsl, score_vs
import ppsl_lang as P

DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("SEED_SMOKE_OUT", os.path.join(os.path.dirname(__file__), "ppsl_seed_smoke.json"))
T1 = os.environ.get("STRUCT_T1", "2026-09-17")


def main():
    t0 = time.time()
    print(f"=== PPSL SEED SMOKE db={DB} ===", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1=T1)
    rows = F.attach_ah_market(con, rows)
    rows = F.attach_seller_n(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    days = sorted({r["ts"][:10] for r in rows})
    cut = max(4, len(days) // 2)
    fit_d, eval_d = set(days[:cut]), set(days[cut:])
    dm = F.BookDemand()
    dm.fit(F.filter_days(rows, fit_d), nac)
    by = S.split_by_item(F.filter_days(rows, eval_d))
    v9 = chrom_v9_live()
    v9m = simulate_ultra(by, v9, dm)
    print(f"v9_h1 late 24h={v9m['profit_24h_mean_m']:.1f} under={v9m['under']:.3f}", flush=True)

    # ULTRA-like chrom (approx winner)
    ultra = chrom_v9_live()
    ultra.name = "ultra_approx_m68136"
    ultra.style = "market_follow"
    ultra.mf_pull = 0.944
    ultra.up_mult = 9.996
    ultra.step_mult = 0.53
    ultra.nac_mult = 2.2
    ultra.nac_dyn = "gap_scale"
    ultra.nac_amp = 1.162
    ultra.down_block_ratio = 0.855
    ultra.lo, ultra.hi, ultra.over = 0.154, 0.27, 0.366
    um = simulate_ultra(by, ultra, dm)
    ur, ua, uu = score_vs(um, v9m)
    print(f"ultra_approx raw×{ur:.3f} adj×{ua:.3f} under={uu:.3f} mean_nac={um.get('mean_nac_mult', um.get('nac_mult'))}", flush=True)

    seeds = [
        ("hold", P.prog_hold(), 1.0),
        ("corridor", P.prog_corridor_simple(), 1.0),
        ("under_veto", P.prog_under_veto_down(), 1.0),
        ("mf_0.9", P.prog_market_follow(0.9), 1.0),
        ("mf_0.94", P.prog_market_follow(0.94), 1.0),
        ("mf_1.0", P.prog_market_follow(1.0), 1.0),
        ("mf_0.94_nac2.2", P.prog_market_follow(0.94), 2.2),
    ]
    board = [
        {
            "name": "v9_h1",
            "kind": "chrom",
            "raw": 1.0,
            "adj": 1.0,
            "under": v9m["under"],
            "profit": v9m["profit_24h_mean_m"],
        },
        {
            "name": "ultra_approx",
            "kind": "chrom",
            "raw": ur,
            "adj": ua,
            "under": uu,
            "profit": um["profit_24h_mean_m"],
            "mean_nac": um.get("mean_nac_mult"),
        },
    ]
    sn = sum(1 for seq in by.values() for o in seq if (o.get("seller_n") or 0) > 0)
    print(f"seller_n>0 on eval obs: {sn}", flush=True)
    print("--- PPSL seeds ---", flush=True)
    for name, prog, nac_m in seeds:
        m = simulate_ppsl(by, prog, dm, nac_mult=nac_m)
        raw, adj, under = score_vs(m, v9m)
        print(
            f"  {name}: raw×{raw:.3f} adj×{adj:.3f} under={under:.3f} "
            f"profit={m['profit_24h_mean_m']:.1f} ups={m['ups']} downs={m['downs']} cx={P.complexity(prog)}",
            flush=True,
        )
        board.append(
            {
                "name": name,
                "kind": "ppsl",
                "raw": raw,
                "adj": adj,
                "under": under,
                "profit": m["profit_24h_mean_m"],
                "ups": m["ups"],
                "downs": m["downs"],
                "cx": P.complexity(prog),
                "pretty": prog.pretty(),
                "nac_mult": nac_m,
            }
        )
    out = {
        "board": board,
        "eval_days": sorted(eval_d),
        "fit_days": sorted(fit_d),
        "elapsed_s": round(time.time() - t0, 1),
        "seller_n_nonzero_obs": sn,
    }
    with open(OUT, "w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    print(f"WROTE {OUT}", flush=True)


if __name__ == "__main__":
    main()
