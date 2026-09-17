#!/usr/bin/env python3
"""
STRUCTURE SEARCH — bi-level prototype (research).

Outer: genetic programming over PPSL programs (structure).
Inner: random/TPE-style jitter of Const leaves (coefficients).

Reuses BookDemand + AH attach + walk_forward from sim_fidelity.
Baselines: v9_h1 Chrom, market-follow / corridor PPSL seeds, ULTRA winner chrom optional.

Does NOT change production Go. Locked final holdout peeked only once at end.
"""
from __future__ import annotations

import json
import os
import random
import sqlite3
import sys
import time
from collections import defaultdict
from copy import deepcopy
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom, chrom_v9, decide as decide_chrom
from search_free import chrom_v9_live
from search_ultra import simulate_ultra, dyn_nac_mult
import ppsl_lang as P

SEED = 20260917
DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("STRUCT_OUT", os.path.join(os.path.dirname(__file__), "structure_search_results.json"))
T1 = os.environ.get("STRUCT_T1", "2026-09-17")
# Locked final holdout — never used for selection
FINAL_HOLDOUT_DAYS = 3


def simulate_ppsl(rows_by_item, prog: P.Program, dm: F.BookDemand, nac_mult: float = 1.0) -> Dict[str, Any]:
    """Fidelity loop with PPSL decide instead of Chrom decide."""
    day_profit: Dict[str, float] = defaultdict(float)
    tot_profit = 0.0
    ups = downs = holds = cycles = 0
    under_n = 0
    per_sku: Dict[str, float] = defaultdict(float)
    nac_sum = 0.0

    # minimal state compatible with Features
    class St:
        __slots__ = ("price", "held", "up_cd", "down_cd", "up_streak", "empty_streak")

        def __init__(self, price, held):
            self.price = price
            self.held = held
            self.up_cd = 0
            self.down_cd = 0
            self.up_streak = 0
            self.empty_streak = 0

    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = St(price=seq[0]["price_before"], held=seq[0]["held"])
        nac0 = max(1, int(dm.nac.get(it, 200_000)))
        for obs in seq:
            if st.up_cd > 0:
                st.up_cd -= 1
            if st.down_cd > 0:
                st.down_cd -= 1
            hour = S.hour_utc(obs["ts"])
            obs = dict(obs)
            obs["night"] = 0 <= hour < 6
            share = obs["share"] or 12
            obs["on_ah"] = min(st.held, share)
            obs["inv"] = max(0, st.held - share)
            mkt = obs.get("mkt") or obs.get("p10")
            depth = obs.get("depth") or "ok"
            step = obs["step"] or 100_000
            nm = max(0.4, min(2.5, float(nac_mult)))
            nac_sum += nm
            margin = max(1, int(nac0 * nm))
            approx_buy = max(step, (obs["price_before"] or st.price) - nac0)
            min_sell = approx_buy + margin
            if st.price < min_sell:
                st.price = min_sell

            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0

            def sales_at(price: int) -> float:
                w = F._blend_weight(price, obs["price_before"], step)
                model = dm.expected_sales(it, price, mkt, st.held, share, obs["ts"], depth=depth)
                if empty_idle and w < 1.0:
                    return float(obs["sales"])
                if empty_idle and w >= 1.0:
                    return 0.0
                if w >= 1.0:
                    return model
                return (1.0 - w) * float(obs["sales"]) + w * model

            gs = sales_at(st.price)
            gb = dm.expected_buys(gs, st.held, share, logged_buys=obs["buys"], empty_idle=empty_idle)
            obs_g = {**obs, "sales": gs, "buys": gb}

            feat = P.features_from_obs(obs_g, st, nac0 * nm)
            action, new_price = P.eval_program(prog, feat, st.price, float(step))

            # hard safety: floor + max jump 15 steps
            new_price = max(min_sell, int(new_price))
            max_jump = int(15 * step)
            if abs(new_price - st.price) > max_jump:
                new_price = st.price + (max_jump if new_price > st.price else -max_jump)
                new_price = max(min_sell, new_price)

            sales = sales_at(new_price)
            buys = dm.expected_buys(
                sales, st.held, share,
                logged_buys=obs["buys"],
                empty_idle=empty_idle and abs(new_price - obs["price_before"]) <= step,
            )
            sales = max(0.0, min(sales, float(st.held) if st.held > 0 else 0.0))

            if action == "UP":
                ups += 1
                st.up_cd = 1
                st.up_streak += 1
            elif action == "DOWN":
                downs += 1
                st.down_cd = 0
                st.up_streak = 0
            else:
                holds += 1
                st.up_streak = 0

            st.price = new_price
            st.held = max(0, int(round(st.held - sales + buys)))
            st.held = min(st.held, int(share * 1.5))
            p = sales * (nac0 * nm)
            tot_profit += p
            per_sku[it] += p
            day_profit[obs["ts"][:10]] += p
            cycles += 1
            if mkt and st.price / mkt < 0.85:
                under_n += 1
            if st.held == 0 and sales < 0.5 and buys < 0.5:
                st.empty_streak += 1
            else:
                st.empty_streak = 0

    days = sorted(day_profit.keys())
    day_vals = [day_profit[d] for d in days]
    mean_24h = sum(day_vals) / len(day_vals) if day_vals else 0.0
    return {
        "profit_24h_mean_m": mean_24h / 1e6,
        "tot_profit_m": tot_profit / 1e6,
        "under": under_n / max(cycles, 1),
        "ups": ups,
        "downs": downs,
        "holds": holds,
        "cycles": cycles,
        "mean_nac_mult": nac_sum / max(cycles, 1),
        "per_sku_m": {k: v / 1e6 for k, v in per_sku.items()},
        "n_days": len(days),
    }


def score_vs(m: Dict[str, Any], v9: Dict[str, Any]) -> Tuple[float, float, float]:
    raw = m["profit_24h_mean_m"] / max(v9["profit_24h_mean_m"], 1e-6)
    scale = max(0.5, float(m.get("mean_nac_mult") or 1.0))
    adj = (m["profit_24h_mean_m"] / scale) / max(v9["profit_24h_mean_m"], 1e-6)
    return raw, adj, float(m["under"])


def inner_optimize(prog: P.Program, by, dm, v9m, rng: random.Random, trials: int = 8) -> Tuple[P.Program, float, Dict]:
    """Cheap inner loop: jitter constants, pick best adj on this fold."""
    best_p, best_fit, best_m = prog, -1e9, None
    for _ in range(trials):
        cand = P.mutate_consts(prog, rng) if _ > 0 else prog
        m = simulate_ppsl(by, cand, dm)
        raw, adj, under = score_vs(m, v9m)
        if under > v9m["under"] + 0.10 and raw < 1.1:
            continue
        cx = P.complexity(cand)
        fit = 0.55 * adj + 0.30 * raw - 0.02 * max(0, cx - 8) - 0.2 * max(0.0, under - v9m["under"])
        if fit > best_fit:
            best_fit, best_p, best_m = fit, cand, m
    return best_p, best_fit, best_m or simulate_ppsl(by, prog, dm)


def main():
    pop_n = int(sys.argv[1]) if len(sys.argv) > 1 else 40
    gens = int(sys.argv[2]) if len(sys.argv) > 2 else 5
    elite_n = int(sys.argv[3]) if len(sys.argv) > 3 else 10
    rng = random.Random(SEED)
    t0 = time.time()
    print(f"=== STRUCTURE SEARCH pop={pop_n} gens={gens} elite={elite_n} ===", flush=True)

    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1=T1)
    print(f"raw={len(rows)} attaching AH…", flush=True)
    rows = F.attach_ah_market(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    days = sorted({r["ts"][:10] for r in rows})
    if len(days) < FINAL_HOLDOUT_DAYS + 8:
        print("WARN: short calendar; holdout still locked", flush=True)
    holdout = set(days[-FINAL_HOLDOUT_DAYS:])
    searchable = set(days[:-FINAL_HOLDOUT_DAYS]) if len(days) > FINAL_HOLDOUT_DAYS else set(days)
    rows_s = F.filter_days(rows, searchable)
    print(f"days={len(days)} holdout={sorted(holdout)} searchable={len(searchable)}", flush=True)

    folds = F.walk_forward_folds(rows_s, n_folds=2)
    for fd in folds:
        print(f"  fold{fd['fold']} fit={fd['train_fit_range']} oos={fd['oos_range']}", flush=True)

    # late half of searchable for selection pressure
    sd = sorted(searchable)
    cut = max(3, len(sd) // 2)
    late_fit, late_eval = set(sd[:cut]), set(sd[cut:])
    dm_late = F.BookDemand()
    dm_late.fit(F.filter_days(rows_s, late_fit), nac)
    by_late = S.split_by_item(F.filter_days(rows_s, late_eval))

    fold_pack = []
    for fd in folds:
        dm = F.BookDemand()
        dm.fit(F.filter_days(rows_s, fd["train_fit"]), nac)
        by = S.split_by_item(F.filter_days(rows_s, fd["oos"]))
        fold_pack.append((fd, dm, by))

    # Chrom baselines on late
    v9 = chrom_v9_live()
    v9_late = simulate_ultra(by_late, v9, dm_late)
    print(f"v9_h1 late 24h={v9_late['profit_24h_mean_m']:.1f} under={v9_late['under']:.3f}", flush=True)

    seeds: List[Tuple[str, P.Program]] = [
        ("hold", P.prog_hold()),
        ("corridor", P.prog_corridor_simple()),
        ("under_veto", P.prog_under_veto_down()),
        ("mf_0.9", P.prog_market_follow(0.9)),
        ("mf_1.0", P.prog_market_follow(1.0)),
    ]
    print("--- seed programs (late vs v9) ---", flush=True)
    seed_scores = []
    for name, prog in seeds:
        m = simulate_ppsl(by_late, prog, dm_late)
        raw, adj, under = score_vs(m, v9_late)
        print(f"  {name}: raw×{raw:.3f} adj×{adj:.3f} under={under:.3f} cx={P.complexity(prog)} | {prog.pretty()[:80]}", flush=True)
        seed_scores.append({"name": name, "raw": raw, "adj": adj, "under": under, "pretty": prog.pretty(), "cx": P.complexity(prog)})

    # Outer GP
    pop: List[P.Program] = [p for _, p in seeds]
    while len(pop) < pop_n:
        pop.append(P.sample_program(rng, max_depth=3))

    history = []
    best_ever = None
    fd0, dm0, by0 = fold_pack[0] if fold_pack else (None, dm_late, by_late)
    v9_0 = simulate_ultra(by0, v9, dm0)

    for gen in range(gens):
        scored = []
        for i, prog in enumerate(pop):
            opt, fit, m = inner_optimize(prog, by0, dm0, v9_0, rng, trials=6)
            raw, adj, under = score_vs(m, v9_0)
            scored.append((fit, raw, adj, under, opt, m))
            if (i + 1) % 20 == 0:
                print(f"  gen{gen} … {i+1}/{len(pop)}", flush=True)
        scored.sort(key=lambda t: -t[0])
        elites = scored[:elite_n]
        top = elites[0]
        print(
            f"gen{gen} best fit={top[0]:.3f} raw×={top[1]:.3f} adj×={top[2]:.3f} under={top[3]:.3f} "
            f"cx={P.complexity(top[4])} | {top[4].pretty()[:100]}",
            flush=True,
        )
        history.append({"gen": gen, "fit": top[0], "raw": top[1], "adj": top[2], "pretty": top[4].pretty()})
        if best_ever is None or top[0] > best_ever[0]:
            best_ever = top

        # next pop
        nxt = [e[4] for e in elites]
        while len(nxt) < pop_n:
            if rng.random() < 0.4:
                nxt.append(P.sample_program(rng, max_depth=3))
            else:
                parent = rng.choice(elites)[4]
                nxt.append(P.mutate_consts(parent, rng, sigma=0.25))
                # structure mutate: replace with random sibling sometimes
                if rng.random() < 0.3:
                    nxt[-1] = P.sample_program(rng, max_depth=2)
        pop = nxt

    # Final OOS on elites across fold1 + late; then ONE peek at holdout
    print("=== multi-fold + late on best ===", flush=True)
    best_prog = best_ever[4]
    report = {"seeds": seed_scores, "history": history, "best_pretty": best_prog.pretty(), "best_cx": P.complexity(best_prog)}

    for label, dm, by in [("late", dm_late, by_late)] + [(f"fold{fd['fold']}", dm, by) for fd, dm, by in fold_pack]:
        m = simulate_ppsl(by, best_prog, dm)
        vref = simulate_ultra(by, v9, dm)
        raw, adj, under = score_vs(m, vref)
        report[label] = {"raw": raw, "adj": adj, "under": under, "profit": m["profit_24h_mean_m"]}
        print(f"  {label}: raw×{raw:.3f} adj×{adj:.3f} under={under:.3f} profit={m['profit_24h_mean_m']:.1f}", flush=True)

    print("=== FINAL HOLDOUT (one-shot) ===", flush=True)
    dm_h = F.BookDemand()
    # fit demand on all searchable (honest for holdout)
    dm_h.fit(rows_s, nac)
    by_h = S.split_by_item(F.filter_days(rows, holdout))
    m_h = simulate_ppsl(by_h, best_prog, dm_h)
    v_h = simulate_ultra(by_h, v9, dm_h)
    raw, adj, under = score_vs(m_h, v_h)
    report["holdout"] = {"raw": raw, "adj": adj, "under": under, "profit": m_h["profit_24h_mean_m"], "days": sorted(holdout)}
    print(f"  holdout {sorted(holdout)}: raw×{raw:.3f} adj×{adj:.3f} under={under:.3f}", flush=True)

    report["meta"] = {
        "elapsed_s": round(time.time() - t0, 1),
        "pop": pop_n,
        "gens": gens,
        "db": DB,
        "note": "structure GP + const jitter; holdout locked until end",
    }
    with open(OUT, "w") as f:
        json.dump(report, f, indent=2, ensure_ascii=False)
    print(f"WROTE {OUT}", flush=True)
    print(f"BEST: {best_prog.pretty()}", flush=True)


if __name__ == "__main__":
    main()
