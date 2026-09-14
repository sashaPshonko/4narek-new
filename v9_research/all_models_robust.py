#!/usr/bin/env python3
"""
All-era policies under robust multi-demand OOS (same harness as V10 robust).
Answer: does v9 lose to classic/corridor/ekb when sim is stressed?
NO production changes.
"""
from __future__ import annotations

import json
import sys
import time
from collections import defaultdict

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
    import search_v10 as V10
    import full_compare_all as FC
    from v10_robust_validation import DemandA, DemandB, DemandC, DemandD
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S
    import search_v10 as V10
    import full_compare_all as FC
    from v10_robust_validation import DemandA, DemandB, DemandC, DemandD


def wrap(name, fn):
    """Adapt full_compare_all policy(st,obs) -> Chrom-like via lambda for V10.simulate need Chrom.
    Instead: run FC.simulate path with DemandModel.
    """
    return name, fn


def eval_fc_policy(name, fn, rows_train, rows_test, nac, DCl):
    dm = DCl()
    dm.fit(rows_train, nac)
    # FC.simulate expects dm with .nac and expected_*; DemandA etc have that
    by = S.split_by_item(rows_test)
    # use V10.simulate with a Chrom that calls via decide? Easier: use FC.simulate
    # But FC.simulate uses its own loop — pass dm
    m = FC.simulate(by, fn, dm)
    # normalize keys
    return {
        "name": name,
        "profit_m": m["profit_m"],
        "pph_m": m["pph_m"],
        "under": m["under_frac"],
        "over": m["over_frac"],
        "ups": m["ups"],
        "downs": m["downs"],
        "avg_held": m["avg_held"],
        "sales": m["sales"],
        # approx 24h: total profit / (cycles/144) if ~10m cycles → days = cycles/144
        "profit_24h_mean_m": round(m["profit_m"] / max((m["cycles"] / 144.0), 0.1), 2),
        "cycles": m["cycles"],
    }


def main():
    t0 = time.time()
    con = S.connect()
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    # enrich on_ah/inv for ekb
    q = f"""
    SELECT ts, item_id, on_ah, inv, normal_sales FROM capital_cycles
    WHERE ts>='2026-07-15' AND item_id IN ({','.join('?'*len(S.FOCUS_ITEMS))})
    """
    extra = {(r[0], r[1]): r for r in con.execute(q, S.FOCUS_ITEMS)}
    for r in rows:
        e = extra.get((r["ts"], r["item_id"]))
        if e:
            r["on_ah"] = e[2] if e[2] is not None else r["held"]
            r["inv"] = e[3] or 0
            r["normal_sales"] = e[4] or 6
        else:
            r["on_ah"] = r["held"]
            r["inv"] = 0
            r["normal_sales"] = 6
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)

    # also chrom policies from V10
    chroms = [
        V10.chrom_v9(),
        V10.chrom_hold(),
    ]
    # map FC policies
    fc_pols = list(FC.POLICIES)

    ts = sorted(set(r["ts"] for r in rows))
    n = len(ts)
    folds = []
    for f in range(4):
        a = int(0.35 * n + 0.08 * f * n)
        b = int(a + 0.10 * n)
        c = int(b + 0.12 * n)
        a, b, c = min(a, n - 3), min(b, n - 2), min(c, n - 1)
        if c <= b:
            continue
        train = [r for r in rows if r["ts"] < ts[a]]
        oos = [r for r in rows if ts[b] <= r["ts"] < ts[c]]
        if len(train) < 2000 or len(oos) < 1500:
            continue
        folds.append((f, train, oos, ts[b][:10], ts[c][:10]))

    demand = [("A", DemandA), ("B", DemandB), ("C", DemandC), ("D", DemandD)]
    results = {"folds": [], "live_note": {}}

    for f, train, oos, t0s, t1s in folds:
        print(f"\n=== fold{f} OOS {t0s}..{t1s} train={len(train)} oos={len(oos)} ===")
        fold_out = {"fold": f, "oos": [t0s, t1s], "by_demand": {}}
        for dname, DCl in demand:
            rows_out = []
            for name, fn in fc_pols:
                m = eval_fc_policy(name, fn, train, oos, nac, DCl)
                rows_out.append(m)
            # add v10 chroms via V10.simulate
            dm = DCl()
            dm.fit(train, nac)
            by = S.split_by_item(oos)
            for ch in chroms:
                m = V10.simulate(by, ch, dm)
                rows_out.append({
                    "name": ch.name,
                    "profit_m": m["profit_m"],
                    "pph_m": m["pph_m"],
                    "under": m["under"],
                    "over": m["over"],
                    "ups": m["ups"],
                    "downs": m["downs"],
                    "avg_held": m["avg_held"],
                    "sales": None,
                    "profit_24h_mean_m": m["profit_24h_mean_m"],
                    "cycles": m["cycles"],
                })
            # rank by profit_m
            rows_out.sort(key=lambda x: -x["profit_m"])
            # x vs v9
            v9 = next((x for x in rows_out if x["name"] in ("v9", "v9_baseline")), None)
            v9p = (v9 or {}).get("profit_m") or 1e-9
            for x in rows_out:
                x["x_v9"] = round(x["profit_m"] / v9p, 3)
            fold_out["by_demand"][dname] = rows_out
            print(f"  {dname} TOP5:")
            for i, x in enumerate(rows_out[:5], 1):
                print(f"    {i}. {x['name']:16} profit={x['profit_m']:8.1f}M x_v9={x['x_v9']:.3f} under={x['under']:.3f} p/h={x['pph_m']}")
            # where is v9
            for i, x in enumerate(rows_out, 1):
                if x["name"] in ("v9", "v9_baseline"):
                    print(f"    v9 rank #{i}/{len(rows_out)} x=1.0 under={x['under']:.3f}")
        results["folds"].append(fold_out)

    # aggregate mean x_v9 and mean rank across fold×demand for each policy
    agg = defaultdict(list)
    for fr in results["folds"]:
        for dname, rows_out in fr["by_demand"].items():
            for i, x in enumerate(rows_out, 1):
                agg[x["name"]].append({"x": x["x_v9"], "rank": i, "profit": x["profit_m"], "under": x["under"], "demand": dname, "fold": fr["fold"]})

    summary = []
    for name, xs in agg.items():
        summary.append({
            "name": name,
            "n": len(xs),
            "mean_x_v9": round(sum(z["x"] for z in xs) / len(xs), 3),
            "median_x_v9": round(sorted(z["x"] for z in xs)[len(xs) // 2], 3),
            "min_x_v9": round(min(z["x"] for z in xs), 3),
            "max_x_v9": round(max(z["x"] for z in xs), 3),
            "mean_rank": round(sum(z["rank"] for z in xs) / len(xs), 2),
            "mean_under": round(sum(z["under"] for z in xs) / len(xs), 3),
            "beats_v9_frac": round(sum(1 for z in xs if z["x"] > 1.02) / len(xs), 3),
        })
    summary.sort(key=lambda z: -z["mean_x_v9"])
    print("\n=== AGGREGATE mean x_v9 across fold×demand ===")
    for i, s in enumerate(summary, 1):
        print(f"  {i:2}. {s['name']:16} mean_x={s['mean_x_v9']:.3f} med={s['median_x_v9']:.3f} "
              f"min={s['min_x_v9']:.3f} rank={s['mean_rank']:.1f} beat_v9={s['beats_v9_frac']:.0%} under={s['mean_under']:.3f}")

    results["aggregate"] = summary
    results["meta"] = {"elapsed": round(time.time() - t0, 1), "n_folds": len(folds), "demands": 4}
    path = "/tmp/all_models_robust.json"
    with open(path, "w") as f:
        json.dump(results, f, ensure_ascii=False, indent=2)
    print(f"Wrote {path}")
    print("DONE")


if __name__ == "__main__":
    main()
