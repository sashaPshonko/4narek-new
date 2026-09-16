#!/usr/bin/env python3
"""
FREE SUPER SEARCH — unrestricted algorithm discovery on fidelity sim.

Allowed (unlike V10/SUPER):
- nac_mult ≠ 1 (honest: margin = nac×nac_mult AND listed price floored by boosted min-sell)
- multi-step jumps: up/down ×1..5, gap-jump toward market
- full chrom space (bands, veto, catchup, cooldowns, styles except ekb)

Gate: dominates (folds ≥0.99×, late ≥1.05×, under ≤ v9+0.01) preferred;
      robust (min≥1.02 ∧ late≥1.02) if any.

Does NOT change production Go.
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
from dataclasses import asdict, fields
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom, decide, chrom_v9

SEED = 42
DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("FREE_OUT", os.path.join(os.path.dirname(__file__), "free_search_results.json"))


def chrom_v9_live() -> Chrom:
    """Post-H1 baseline (down_block 0.95)."""
    c = chrom_v9()
    c.name = "v9_h1"
    c.down_block_ratio = 0.95
    c.nac_mult = 1.0
    return c


def sample_free(rng: random.Random, i: int) -> Chrom:
    style = rng.choice(
        ["corridor", "corridor", "corridor", "aggressive_down", "market_follow", "velocity", "holdish"]
    )
    lo = rng.choice([0.08, 0.10, 0.12, 0.15, 0.18, 0.20, 0.22, 0.25])
    hi = lo + rng.choice([0.04, 0.05, 0.07, 0.08, 0.10, 0.12, 0.15])
    over = hi + rng.choice([0.05, 0.08, 0.10, 0.12, 0.15])
    dump = over + rng.choice([0.05, 0.10, 0.15, 0.20])
    return Chrom(
        name=f"f{i}",
        lo=lo,
        hi=min(hi, 0.55),
        over=min(over, 0.70),
        dump=min(dump, 0.90),
        step_mult=rng.choice([0.5, 0.75, 1.0, 1.25, 1.5, 2.0]),
        # multi-step allowed
        up_mult=rng.choice([0.5, 1.0, 1.5, 2.0, 3.0, 4.0, 5.0]),
        down_soft_mult=rng.choice([0.5, 1.0, 1.5, 2.0, 3.0]),
        down_hard_mult=rng.choice([1.0, 2.0, 3.0, 4.0, 5.0]),
        min_sales_up=rng.choice([1, 2, 3, 4, 5, 6]),
        night_sales_up=rng.choice([2, 3, 4, 5, 6, 7]),
        require_sales_gt_buys=rng.choice([True, True, False]),
        demand_max_ratio=rng.choice([0.95, 1.0, 1.05, 1.10, 1.20, 1.5]),
        empty_streak=rng.choice([1, 2, 3, 4, 6]),
        catchup_gap=rng.choice([0.85, 0.90, 0.95, 1.0, 1.0, 1.05]),
        catchup_on=rng.choice([True, True, True, False]),
        down_block_ratio=rng.choice([0.85, 0.90, 0.95, 0.95, 1.01]),
        no_down_if_held_le_hi=rng.choice([True, True, False]),
        up_cd=rng.choice([0, 1, 2, 3]),
        down_cd=rng.choice([0, 1, 2]),
        max_up_streak=rng.choice([1, 2, 3, 5, 99]),
        # FREE nac
        nac_mult=rng.choice([0.70, 0.85, 1.0, 1.0, 1.0, 1.10, 1.20, 1.35, 1.50]),
        style=style,
        mf_pull=rng.choice([0.0, 0.0, 0.25, 0.5, 0.75, 1.0]),
        vel_up=rng.choice([1.0, 2.0, 3.0, 4.0]),
        vel_down=rng.choice([-1.0, -2.0, -3.0]),
        allow_down_empty=rng.choice([False, False, True]),
        allow_blind_empty_up=rng.choice([False, False, True]),
        soft_down_when_over_mkt=rng.choice([False, True, True]),
        seed_tag=i,
    )


def mutate_free(ch: Chrom, rng: random.Random, i: int) -> Chrom:
    c = deepcopy(ch)
    c.name = f"m{i}"
    c.seed_tag = i
    for _ in range(rng.randint(1, 5)):
        field = rng.choice(
            [
                "lo", "hi", "over", "dump", "step_mult", "up_mult", "down_soft_mult",
                "down_hard_mult", "min_sales_up", "catchup_gap", "down_block_ratio",
                "soft_down_when_over_mkt", "style", "empty_streak", "up_cd", "nac_mult",
                "mf_pull", "demand_max_ratio", "no_down_if_held_le_hi",
            ]
        )
        if field == "lo":
            c.lo = rng.choice([0.08, 0.10, 0.12, 0.15, 0.18, 0.20])
            c.hi = max(c.hi, c.lo + 0.05)
        elif field == "hi":
            c.hi = c.lo + rng.choice([0.05, 0.08, 0.10, 0.12, 0.15])
            c.over = max(c.over, c.hi + 0.05)
            c.dump = max(c.dump, c.over + 0.05)
        elif field == "over":
            c.over = c.hi + rng.choice([0.05, 0.08, 0.10, 0.15])
            c.dump = max(c.dump, c.over + 0.05)
        elif field == "dump":
            c.dump = min(0.9, c.over + rng.choice([0.05, 0.10, 0.15, 0.20]))
        elif field == "step_mult":
            c.step_mult = rng.choice([0.5, 1.0, 1.5, 2.0])
        elif field == "up_mult":
            c.up_mult = rng.choice([1.0, 2.0, 3.0, 4.0, 5.0])
        elif field == "down_soft_mult":
            c.down_soft_mult = rng.choice([0.5, 1.0, 2.0, 3.0])
        elif field == "down_hard_mult":
            c.down_hard_mult = rng.choice([1.0, 2.0, 3.0, 4.0, 5.0])
        elif field == "min_sales_up":
            c.min_sales_up = rng.choice([2, 3, 4, 5])
            c.night_sales_up = c.min_sales_up + 1
        elif field == "catchup_gap":
            c.catchup_gap = rng.choice([0.90, 0.95, 1.0, 1.05])
        elif field == "down_block_ratio":
            c.down_block_ratio = rng.choice([0.90, 0.95, 1.01])
        elif field == "soft_down_when_over_mkt":
            c.soft_down_when_over_mkt = rng.choice([True, False])
        elif field == "style":
            c.style = rng.choice(["corridor", "corridor", "aggressive_down", "market_follow", "velocity"])
        elif field == "empty_streak":
            c.empty_streak = rng.choice([1, 2, 3, 4])
        elif field == "up_cd":
            c.up_cd = rng.choice([0, 1, 2, 3])
        elif field == "nac_mult":
            c.nac_mult = rng.choice([0.85, 1.0, 1.10, 1.20, 1.35])
        elif field == "mf_pull":
            c.mf_pull = rng.choice([0.0, 0.25, 0.5, 1.0])
            if c.mf_pull > 0:
                c.style = "market_follow"
        elif field == "demand_max_ratio":
            c.demand_max_ratio = rng.choice([1.0, 1.05, 1.10, 1.20])
        elif field == "no_down_if_held_le_hi":
            c.no_down_if_held_le_hi = rng.choice([True, False])
    return c


def simulate_free(rows_by_item, ch: Chrom, dm: F.BookDemand) -> Dict[str, Any]:
    """
    Like fidelity sim, but:
    - nac_mult changes margin AND min-sell floor (honest coupling)
    - multi-step already in decide via up_mult/down_*_mult
    """
    day_profit: Dict[str, float] = defaultdict(float)
    tot_profit = 0.0
    ups = downs = holds = cycles = 0
    under_n = over_n = deep_under_n = 0
    per_sku: Dict[str, float] = defaultdict(float)
    book_ok = 0

    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
        nac0 = max(1, int(dm.nac.get(it, 200_000)))
        margin = max(1, int(nac0 * ch.nac_mult))
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
            if obs.get("mkt_source") == "ah_p10":
                book_ok += 1

            step = max(1, int((obs["step"] or 100_000) * ch.step_mult))
            # Honest nac floor: can't list below ~ (price - old_nac) + new_margin
            # approx buy ≈ price_before - nac0 when at logged price
            approx_buy = max(step, (obs["price_before"] or st.price) - nac0)
            min_sell = approx_buy + margin
            if st.price < min_sell:
                st.price = min_sell

            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0

            def sales_at(price: int) -> float:
                w = F._blend_weight(price, obs["price_before"], obs["step"] or 100_000)
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
            # decide with step already in chrom.step_mult via _step(obs) — patch obs step
            obs_g = {**obs, "sales": gs, "buys": gb, "step": obs["step"] or 100_000}
            action, new_price = decide(st, obs_g, ch)
            new_price = max(new_price, min_sell)

            sales = sales_at(new_price)
            buys = dm.expected_buys(
                sales, st.held, share,
                logged_buys=obs["buys"],
                empty_idle=empty_idle and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000),
            )
            sales = max(0.0, min(sales, float(st.held) if st.held > 0 else 0.0))

            if action == "UP":
                ups += 1
                st.up_cd = ch.up_cd
                st.up_streak += 1
            elif action == "DOWN":
                downs += 1
                st.down_cd = ch.down_cd
                st.up_streak = 0
            else:
                holds += 1
                st.up_streak = 0

            st.price = max(new_price, min_sell)
            st.held = max(0, int(round(st.held - sales + buys)))
            st.held = min(st.held, int(share * 1.5))

            p = sales * margin  # nac × nac_mult
            tot_profit += p
            per_sku[it] += p
            day_profit[obs["ts"][:10]] += p
            cycles += 1
            if mkt:
                r = st.price / mkt
                if r < 0.85:
                    under_n += 1
                if r < 0.80:
                    deep_under_n += 1
                if r > 1.10:
                    over_n += 1

            if st.held == 0 and sales < 0.5 and buys < 0.5:
                st.empty_streak += 1
            else:
                st.empty_streak = 0

    days = sorted(day_profit.keys())
    day_vals = [day_profit[d] for d in days]
    mean_24h = sum(day_vals) / len(day_vals) if day_vals else 0.0
    return {
        "profit_m": tot_profit / 1e6,
        "profit_24h_mean_m": mean_24h / 1e6,
        "n_days": len(days),
        "ups": ups,
        "downs": downs,
        "holds": holds,
        "cycles": cycles,
        "under": under_n / cycles if cycles else 0,
        "deep_under": deep_under_n / cycles if cycles else 0,
        "over": over_n / cycles if cycles else 0,
        "book_ok_frac": book_ok / cycles if cycles else 0,
        "per_sku_m": {k: round(v / 1e6, 1) for k, v in sorted(per_sku.items(), key=lambda x: -x[1])},
        "days": days,
        "nac_mult": ch.nac_mult,
    }


def main():
    n_random = int(sys.argv[1]) if len(sys.argv) > 1 else 600
    n_mut = int(sys.argv[2]) if len(sys.argv) > 2 else 300
    rng = random.Random(SEED)
    t0 = time.time()
    print(f"=== FREE SUPER db={DB} random={n_random} mut={n_mut} ===", flush=True)

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

    base = chrom_v9_live()
    v9_late = simulate_free(by_late, base, dm_late)
    print(
        f"v9_h1 late 24h={v9_late['profit_24h_mean_m']:.1f} under={v9_late['under']:.3f}",
        flush=True,
    )
    v9_folds = []
    for fd, dm, by in fold_pack:
        m = simulate_free(by, base, dm)
        v9_folds.append(m)
        print(f"  fold{fd['fold']} v9 24h={m['profit_24h_mean_m']:.1f} under={m['under']:.3f}", flush=True)

    pool: List[Chrom] = [base, chrom_v9()]
    # seed grid: nac × veto × up_mult
    for nac_m in [0.85, 1.0, 1.15, 1.30]:
        for dbr in [0.90, 0.95, 1.01]:
            for um in [1.0, 2.0, 3.0, 5.0]:
                for soft in [True, False]:
                    c = chrom_v9_live()
                    c.name = f"g_{nac_m}_{dbr}_{um}_{int(soft)}"
                    c.nac_mult = nac_m
                    c.down_block_ratio = dbr
                    c.up_mult = um
                    c.soft_down_when_over_mkt = soft
                    pool.append(c)
    pool.extend(sample_free(rng, i) for i in range(n_random))
    seeds = [base, chrom_v9_live()]
    for i in range(n_mut):
        pool.append(mutate_free(rng.choice(seeds), rng, 20_000 + i))

    # dedupe names
    seen = set()
    uniq = []
    for c in pool:
        if c.style == "ekb_like":
            c.style = "corridor"
        if c.name in seen:
            c.name = f"{c.name}_{c.seed_tag}"
        seen.add(c.name)
        uniq.append(c)
    pool = uniq
    print(f"pool={len(pool)}", flush=True)

    # val screen on fold0 train→val if present, else OOS fold0
    scored = []
    fd0, dm0, by0 = fold_pack[0]
    v9_0 = v9_folds[0]
    for i, ch in enumerate(pool):
        m = simulate_free(by0, ch, dm0)
        if not F.constrained_ok(m, v9_0):
            continue
        x = m["profit_24h_mean_m"] / max(v9_0["profit_24h_mean_m"], 1e-6)
        scored.append((x, ch, m))
        if (i + 1) % 200 == 0:
            print(f"  … screened {i+1}/{len(pool)} kept={len(scored)}", flush=True)
    scored.sort(key=lambda t: -t[0])
    finalists = [t[1] for t in scored[:60]]
    # always keep base + high nac / multi-step seeds
    for c in pool[:20]:
        if c not in finalists:
            finalists.append(c)
    print(f"finalists={len(finalists)}", flush=True)

    results = []
    for ch in finalists:
        row: Dict[str, Any] = {"name": ch.name, "chrom": asdict(ch)}
        xs, oks = [], []
        for fi, (fd, dm, by) in enumerate(fold_pack):
            m = simulate_free(by, ch, dm)
            v9 = v9_folds[fi]
            x = m["profit_24h_mean_m"] / max(v9["profit_24h_mean_m"], 1e-6)
            ok = F.constrained_ok(m, v9) and x >= 0.98
            row[f"f{fd['fold']}_x"] = round(x, 3)
            row[f"f{fd['fold']}_under"] = round(m["under"], 3)
            row[f"f{fd['fold']}_ups"] = m["ups"]
            row[f"f{fd['fold']}_downs"] = m["downs"]
            xs.append(x)
            oks.append(ok)
        m = simulate_free(by_late, ch, dm_late)
        late_x = m["profit_24h_mean_m"] / max(v9_late["profit_24h_mean_m"], 1e-6)
        late_ok = F.constrained_ok(m, v9_late) and late_x >= 1.0
        row["late_x"] = round(late_x, 3)
        row["late_under"] = round(m["under"], 3)
        row["late_24h"] = round(m["profit_24h_mean_m"], 2)
        row["late_ups"] = m["ups"]
        row["late_downs"] = m["downs"]
        row["min_x"] = round(min(xs), 3)
        row["mean_x"] = round(sum(xs) / len(xs), 3)
        row["n_folds_ok"] = sum(1 for o in oks if o)
        row["robust"] = all(oks) and min(xs) >= 1.02 and late_x >= 1.02 and late_ok
        row["dominates"] = (
            all(oks) and min(xs) >= 0.99 and late_x >= 1.05 and late_ok
            and m["under"] <= v9_late["under"] + 0.01
        )
        results.append(row)

    results.sort(key=lambda r: (-int(r["robust"]), -int(r["dominates"]), -r["late_x"], -r["min_x"]))
    robust = [r for r in results if r["robust"]]
    dom = [r for r in results if r["dominates"] and r["name"] != "v9_h1"]
    elapsed = time.time() - t0

    def rules(ch: dict) -> List[str]:
        return [
            f"band lo={ch['lo']:.2f} hi={ch['hi']:.2f} over={ch['over']:.2f} dump={ch['dump']:.2f}",
            f"steps up×{ch['up_mult']} soft↓×{ch['down_soft_mult']} hard↓×{ch['down_hard_mult']} step×{ch['step_mult']}",
            f"nac×{ch['nac_mult']} veto↓<{ch['down_block_ratio']} catchup={ch['catchup_on']}@{ch['catchup_gap']}",
            f"style={ch['style']} mf_pull={ch['mf_pull']} soft_over_mkt={ch['soft_down_when_over_mkt']}",
        ]

    best = (robust[0] if robust else None) or (dom[0] if dom else None)
    out = {
        "meta": {
            "db": DB,
            "elapsed_sec": round(elapsed, 1),
            "n_pool": len(pool),
            "n_finalists": len(finalists),
            "baseline": "v9_h1 (down_block=0.95)",
            "free": "nac_mult + multi-step ×1..5 + full chrom",
            "v9_late_24h_m": round(v9_late["profit_24h_mean_m"], 2),
            "v9_late_under": round(v9_late["under"], 3),
        },
        "results_top30": results[:30],
        "robust": robust[:10],
        "dominates": dom[:10],
        "best": best,
        "best_rules": rules(best["chrom"]) if best else [],
    }
    with open(OUT, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {OUT} in {elapsed:.0f}s", flush=True)
    if best:
        tag = "ROBUST" if best.get("robust") else "DOMINATES"
        print(f"BEST {tag} {best['name']} late={best['late_x']} min={best['min_x']} nac×{best['chrom']['nac_mult']}", flush=True)
        for line in out["best_rules"]:
            print("  •", line, flush=True)
    else:
        print("NO WINNER beyond v9_h1", flush=True)
        if results:
            r = results[0]
            print(f"top {r['name']} late={r['late_x']} min={r['min_x']}", flush=True)
    print("DONE", flush=True)


if __name__ == "__main__":
    main()
