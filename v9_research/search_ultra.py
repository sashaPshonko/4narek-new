#!/usr/bin/env python3
"""
ULTRA FREE SEARCH — discover pricing formula with no sacred cows.

Allowed:
  - price steps non-static (step_mult, up/down mults, mf_pull gap-jumps to AH p10)
  - price can move freely toward/away from market
  - nacenka non-static: fixed mult OR dynamic (inv_scale / gap_scale) per cycle
  - bands/vetoes/catchup/cooldowns all free

Method: multi-generation evolutionary search (population + crossover + mutate)
on fidelity AH-p10 simulator. Reports BOTH raw late× and margin_adj×
(= profit/nac_mult) so nac-scale artifacts are visible.

Does NOT change production Go.
"""
from __future__ import annotations

import json
import os
import random
import sqlite3
import sys
time = __import__("time")
from collections import defaultdict
from copy import deepcopy
from dataclasses import asdict
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom, chrom_v9, decide
from search_free import chrom_v9_live

SEED = 20260917
DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("ULTRA_OUT", os.path.join(os.path.dirname(__file__), "ultra_search_results.json"))
T1 = os.environ.get("ULTRA_T1", "2026-09-17")


def dyn_nac_mult(ch: Chrom, held: int, share: int, price: int, mkt: Optional[float]) -> float:
    """Per-cycle effective nac multiplier (non-static markup)."""
    base = float(ch.nac_mult or 1.0)
    amp = max(1.0, float(getattr(ch, "nac_amp", 1.0) or 1.0))
    mode = getattr(ch, "nac_dyn", "fixed") or "fixed"
    if mode == "fixed" or amp <= 1.001:
        return max(0.4, min(2.5, base))
    fill = held / max(share, 1)
    if mode == "inv_scale":
        # scarce → higher margin; overstock → compress
        if fill <= ch.lo:
            return max(0.4, min(2.5, base * amp))
        if fill >= ch.over:
            return max(0.4, min(2.5, base / amp))
        # interpolate in band
        mid = 0.5 * (ch.lo + ch.hi)
        if fill < mid:
            t = (fill - ch.lo) / max(mid - ch.lo, 1e-6)
            return max(0.4, min(2.5, base * (amp + (1.0 - amp) * t)))
        t = (fill - mid) / max(ch.over - mid, 1e-6)
        return max(0.4, min(2.5, base * (1.0 + (1.0 / amp - 1.0) * t)))
    if mode == "gap_scale" and mkt and mkt > 0:
        ratio = price / mkt
        if ratio < 0.88:
            return max(0.4, min(2.5, base / amp))  # deep under → shrink nac, climb easier
        if ratio > 1.12:
            return max(0.4, min(2.5, base * amp))  # over market → fatten margin / soft resist
        return max(0.4, min(2.5, base))
    return max(0.4, min(2.5, base))


def simulate_ultra(rows_by_item, ch: Chrom, dm: F.BookDemand) -> Dict[str, Any]:
    day_profit: Dict[str, float] = defaultdict(float)
    tot_profit = 0.0
    ups = downs = holds = cycles = 0
    under_n = over_n = deep_under_n = 0
    per_sku: Dict[str, float] = defaultdict(float)
    book_ok = 0
    nac_sum = 0.0

    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
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
            if obs.get("mkt_source") == "ah_p10":
                book_ok += 1

            nm = dyn_nac_mult(ch, st.held, share, st.price, mkt)
            nac_sum += nm
            margin = max(1, int(nac0 * nm))
            step = max(1, int((obs["step"] or 100_000) * ch.step_mult))
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
            obs_g = {**obs, "sales": gs, "buys": gb, "step": obs["step"] or 100_000}
            # temporary nac_mult for decide side-effects that read ch.nac_mult
            old_nm = ch.nac_mult
            ch.nac_mult = nm
            try:
                action, new_price = decide(st, obs_g, ch)
            finally:
                ch.nac_mult = old_nm
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

            p = sales * margin
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
        "mean_nac_mult": nac_sum / cycles if cycles else ch.nac_mult,
        "per_sku_m": {k: round(v / 1e6, 1) for k, v in sorted(per_sku.items(), key=lambda x: -x[1])[:12]},
        "days": days,
        "nac_mult": ch.nac_mult,
        "nac_dyn": getattr(ch, "nac_dyn", "fixed"),
    }


def u(rng: random.Random, a: float, b: float) -> float:
    return a + (b - a) * rng.random()


def sample_ultra(rng: random.Random, i: int) -> Chrom:
    style = rng.choice(
        ["corridor", "corridor", "market_follow", "market_follow", "market_follow",
         "aggressive_down", "velocity", "holdish"]
    )
    lo = u(rng, 0.06, 0.28)
    hi = lo + u(rng, 0.04, 0.18)
    over = hi + u(rng, 0.04, 0.18)
    dump = min(0.95, over + u(rng, 0.04, 0.25))
    mf = u(rng, 0.0, 1.0) if style == "market_follow" else rng.choice([0.0, 0.0, u(rng, 0.2, 1.0)])
    if mf > 0.15:
        style = "market_follow"
    nac_dyn = rng.choice(["fixed", "fixed", "inv_scale", "inv_scale", "gap_scale"])
    return Chrom(
        name=f"u{i}",
        lo=round(lo, 3),
        hi=round(min(hi, 0.55), 3),
        over=round(min(over, 0.75), 3),
        dump=round(dump, 3),
        step_mult=round(u(rng, 0.35, 3.0), 3),
        up_mult=round(u(rng, 0.5, 8.0), 3),
        down_soft_mult=round(u(rng, 0.5, 5.0), 3),
        down_hard_mult=round(u(rng, 1.0, 8.0), 3),
        min_sales_up=rng.randint(1, 7),
        night_sales_up=rng.randint(2, 9),
        require_sales_gt_buys=rng.random() < 0.65,
        demand_max_ratio=round(u(rng, 0.9, 1.6), 3),
        empty_streak=rng.randint(1, 6),
        catchup_gap=round(u(rng, 0.75, 1.08), 3),
        catchup_on=rng.random() < 0.75,
        down_block_ratio=round(u(rng, 0.75, 1.05), 3),
        no_down_if_held_le_hi=rng.random() < 0.7,
        up_cd=rng.randint(0, 4),
        down_cd=rng.randint(0, 3),
        max_up_streak=rng.choice([1, 2, 3, 5, 8, 99]),
        nac_mult=round(u(rng, 0.55, 2.0), 3),
        style=style,
        mf_pull=round(mf, 3),
        vel_up=round(u(rng, 1.0, 5.0), 2),
        vel_down=round(u(rng, -5.0, -1.0), 2),
        allow_down_empty=rng.random() < 0.2,
        allow_blind_empty_up=rng.random() < 0.2,
        soft_down_when_over_mkt=rng.random() < 0.65,
        nac_dyn=nac_dyn,
        nac_amp=round(u(rng, 1.05, 1.55), 3) if nac_dyn != "fixed" else 1.0,
        seed_tag=i,
    )


def crossover(a: Chrom, b: Chrom, rng: random.Random, i: int) -> Chrom:
    c = deepcopy(a)
    c.name = f"x{i}"
    c.seed_tag = i
    for f in (
        "lo", "hi", "over", "dump", "step_mult", "up_mult", "down_soft_mult", "down_hard_mult",
        "min_sales_up", "night_sales_up", "demand_max_ratio", "empty_streak", "catchup_gap",
        "catchup_on", "down_block_ratio", "up_cd", "down_cd", "max_up_streak", "nac_mult",
        "style", "mf_pull", "vel_up", "vel_down", "soft_down_when_over_mkt",
        "require_sales_gt_buys", "no_down_if_held_le_hi", "nac_dyn", "nac_amp",
        "allow_down_empty", "allow_blind_empty_up",
    ):
        if rng.random() < 0.5:
            setattr(c, f, getattr(b, f))
    if c.hi <= c.lo:
        c.hi = c.lo + 0.05
    if c.over <= c.hi:
        c.over = c.hi + 0.05
    if c.dump <= c.over:
        c.dump = min(0.95, c.over + 0.05)
    if c.mf_pull > 0.15:
        c.style = "market_follow"
    return c


def mutate_ultra(ch: Chrom, rng: random.Random, i: int) -> Chrom:
    c = deepcopy(ch)
    c.name = f"m{i}"
    c.seed_tag = i
    for _ in range(rng.randint(1, 6)):
        pick = rng.choice(
            [
                "bands", "step_mult", "up_mult", "down", "nac_mult", "nac_dyn", "mf_pull",
                "style", "catchup", "dbr", "cd", "sales",
            ]
        )
        if pick == "bands":
            c.lo = round(max(0.05, min(0.35, c.lo + u(rng, -0.04, 0.04))), 3)
            c.hi = round(max(c.lo + 0.04, min(0.6, c.hi + u(rng, -0.05, 0.05))), 3)
            c.over = round(max(c.hi + 0.04, min(0.8, c.over + u(rng, -0.05, 0.05))), 3)
            c.dump = round(max(c.over + 0.04, min(0.95, c.dump + u(rng, -0.05, 0.08))), 3)
        elif pick == "step_mult":
            c.step_mult = round(max(0.25, min(4.0, c.step_mult * u(rng, 0.6, 1.5))), 3)
        elif pick == "up_mult":
            c.up_mult = round(max(0.5, min(10.0, c.up_mult * u(rng, 0.5, 1.8))), 3)
        elif pick == "down":
            c.down_soft_mult = round(max(0.4, min(6.0, c.down_soft_mult * u(rng, 0.5, 1.6))), 3)
            c.down_hard_mult = round(max(1.0, min(10.0, c.down_hard_mult * u(rng, 0.5, 1.6))), 3)
        elif pick == "nac_mult":
            c.nac_mult = round(max(0.5, min(2.2, c.nac_mult * u(rng, 0.7, 1.35))), 3)
        elif pick == "nac_dyn":
            c.nac_dyn = rng.choice(["fixed", "inv_scale", "gap_scale"])
            c.nac_amp = round(u(rng, 1.05, 1.6), 3) if c.nac_dyn != "fixed" else 1.0
        elif pick == "mf_pull":
            c.mf_pull = round(max(0.0, min(1.0, c.mf_pull + u(rng, -0.35, 0.35))), 3)
            if c.mf_pull > 0.2:
                c.style = "market_follow"
        elif pick == "style":
            c.style = rng.choice(["corridor", "market_follow", "aggressive_down", "velocity"])
            if c.style == "market_follow" and c.mf_pull < 0.2:
                c.mf_pull = round(u(rng, 0.4, 1.0), 3)
        elif pick == "catchup":
            c.catchup_on = rng.random() < 0.8
            c.catchup_gap = round(max(0.7, min(1.1, c.catchup_gap + u(rng, -0.08, 0.08))), 3)
            c.empty_streak = rng.randint(1, 5)
        elif pick == "dbr":
            c.down_block_ratio = round(max(0.7, min(1.08, c.down_block_ratio + u(rng, -0.08, 0.08))), 3)
            c.soft_down_when_over_mkt = rng.random() < 0.7
        elif pick == "cd":
            c.up_cd = rng.randint(0, 4)
            c.down_cd = rng.randint(0, 3)
            c.max_up_streak = rng.choice([1, 2, 3, 5, 99])
        elif pick == "sales":
            c.min_sales_up = rng.randint(1, 6)
            c.night_sales_up = c.min_sales_up + rng.randint(0, 3)
            c.require_sales_gt_buys = rng.random() < 0.6
    return c


def score_pair(m: Dict[str, Any], v9: Dict[str, Any]) -> Tuple[float, float, float]:
    """raw_x, margin_adj_x, under."""
    raw = m["profit_24h_mean_m"] / max(v9["profit_24h_mean_m"], 1e-6)
    # strip average nac scale (dyn → use mean_nac_mult)
    scale = max(0.5, float(m.get("mean_nac_mult") or m.get("nac_mult") or 1.0))
    adj = (m["profit_24h_mean_m"] / scale) / max(v9["profit_24h_mean_m"], 1e-6)
    return raw, adj, float(m["under"])


def main():
    pop_n = int(sys.argv[1]) if len(sys.argv) > 1 else 120
    gens = int(sys.argv[2]) if len(sys.argv) > 2 else 8
    elite_n = int(sys.argv[3]) if len(sys.argv) > 3 else 24
    rng = random.Random(SEED)
    t0 = time.time()
    print(f"=== ULTRA FREE pop={pop_n} gens={gens} elite={elite_n} db={DB} ===", flush=True)

    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = S.load_panel(con, S.FOCUS_ITEMS, t0=F.BOOK_T0, t1=T1)
    print(f"raw={len(rows)} attaching AH…", flush=True)
    rows = F.attach_ah_market(con, rows)
    ah = sum(1 for r in rows if r.get("mkt_source") == "ah_p10")
    print(f"cycles={len(rows)} ah={ah} ({100 * ah / max(len(rows), 1):.1f}%)", flush=True)
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
    v9_late = simulate_ultra(by_late, base, dm_late)
    print(f"v9_h1 late 24h={v9_late['profit_24h_mean_m']:.1f} under={v9_late['under']:.3f}", flush=True)
    v9_folds = []
    for fd, dm, by in fold_pack:
        m = simulate_ultra(by, base, dm)
        v9_folds.append(m)
        print(f"  fold{fd['fold']} v9 24h={m['profit_24h_mean_m']:.1f} under={m['under']:.3f}", flush=True)

    # seed elites known from OPEN
    seeds: List[Chrom] = [base, chrom_v9()]
    for pull in [0.75, 1.0]:
        for um in [2.0, 5.0]:
            for nac_m in [1.0, 1.3, 1.5]:
                c = chrom_v9_live()
                c.name = f"seed_MF_n{nac_m}_p{pull}_u{um}"
                c.style = "market_follow"
                c.mf_pull = pull
                c.up_mult = um
                c.nac_mult = nac_m
                c.soft_down_when_over_mkt = True
                seeds.append(c)
    for nd in ["inv_scale", "gap_scale"]:
        c = chrom_v9_live()
        c.name = f"seed_dyn_{nd}"
        c.style = "market_follow"
        c.mf_pull = 1.0
        c.up_mult = 3.0
        c.nac_dyn = nd
        c.nac_amp = 1.3
        c.nac_mult = 1.0
        seeds.append(c)

    pop: List[Chrom] = list(seeds)
    while len(pop) < pop_n:
        pop.append(sample_ultra(rng, len(pop)))

    history = []
    best_ever = None

    for gen in range(gens):
        scored = []
        fd0, dm0, by0 = fold_pack[0]
        v9_0 = v9_folds[0]
        for i, ch in enumerate(pop):
            if ch.style == "ekb_like":
                ch.style = "corridor"
            m = simulate_ultra(by0, ch, dm0)
            raw, adj, under = score_pair(m, v9_0)
            # soft gate: not catastrophic under vs v9
            if under > v9_0["under"] + 0.08 and raw < 1.15:
                continue
            # fitness: prefer margin-adjusted, bonus for raw, penalty under
            fit = 0.55 * adj + 0.35 * raw - 0.25 * max(0.0, under - v9_0["under"])
            scored.append((fit, raw, adj, under, ch, m))
            if (i + 1) % 40 == 0:
                print(f"  gen{gen} … {i+1}/{len(pop)} kept={len(scored)}", flush=True)
        scored.sort(key=lambda t: -t[0])
        if not scored:
            print(f"gen{gen}: no survivors, resampling", flush=True)
            pop = [sample_ultra(rng, 10_000 + gen * 1000 + j) for j in range(pop_n)]
            continue

        elites = scored[:elite_n]
        top = elites[0]
        print(
            f"gen{gen} best={top[4].name} fit={top[0]:.3f} raw×={top[1]:.3f} adj×={top[2]:.3f} "
            f"under={top[3]:.3f} nac={top[4].nac_mult}/{top[4].nac_dyn} style={top[4].style} "
            f"mf={top[4].mf_pull} up={top[4].up_mult}",
            flush=True,
        )
        history.append({
            "gen": gen,
            "best": top[4].name,
            "fit": round(top[0], 4),
            "raw_x": round(top[1], 4),
            "adj_x": round(top[2], 4),
            "under": round(top[3], 4),
            "chrom": asdict(top[4]),
        })
        if best_ever is None or top[0] > best_ever[0]:
            best_ever = top

        # next gen: elites + crossover + mutate + fresh
        next_pop: List[Chrom] = [deepcopy(t[4]) for t in elites]
        while len(next_pop) < pop_n:
            r = rng.random()
            if r < 0.45 and len(elites) >= 2:
                a, b = rng.sample(elites, 2)
                next_pop.append(crossover(a[4], b[4], rng, 50_000 + gen * 1000 + len(next_pop)))
            elif r < 0.80:
                parent = rng.choice(elites)[4]
                next_pop.append(mutate_ultra(parent, rng, 60_000 + gen * 1000 + len(next_pop)))
            else:
                next_pop.append(sample_ultra(rng, 70_000 + gen * 1000 + len(next_pop)))
        pop = next_pop

    # full OOS on final elites
    print("=== final OOS on elites ===", flush=True)
    final_rows = []
    elite_chroms = [best_ever[4]] if best_ever else [base]
    # re-score last history chroms + seeds MF
    seen = {c.name for c in elite_chroms}
    for h in history[-3:]:
        # rebuild from dict
        pass
    for t in (best_ever and [best_ever] or []):
        pass

    # take last gen elites from history + re-eval top from final pop screen
    fd0, dm0, by0 = fold_pack[0]
    screen = []
    for ch in pop:
        m = simulate_ultra(by0, ch, dm0)
        raw, adj, under = score_pair(m, v9_folds[0])
        screen.append((0.55 * adj + 0.35 * raw, raw, adj, under, ch, m))
    screen.sort(key=lambda t: -t[0])
    candidates = []
    for t in screen[:30]:
        if t[4].name not in seen:
            candidates.append(t[4])
            seen.add(t[4].name)
    if best_ever:
        candidates = [best_ever[4]] + candidates

    for ch in candidates[:25]:
        row: Dict[str, Any] = {"name": ch.name, "chrom": asdict(ch)}
        xs, adjs = [], []
        for fi, (fd, dm, by) in enumerate(fold_pack):
            m = simulate_ultra(by, ch, dm)
            raw, adj, under = score_pair(m, v9_folds[fi])
            row[f"f{fd['fold']}_raw_x"] = round(raw, 3)
            row[f"f{fd['fold']}_adj_x"] = round(adj, 3)
            row[f"f{fd['fold']}_under"] = round(under, 3)
            xs.append(raw)
            adjs.append(adj)
        m_late = simulate_ultra(by_late, ch, dm_late)
        raw_l, adj_l, under_l = score_pair(m_late, v9_late)
        row["late_raw_x"] = round(raw_l, 3)
        row["late_adj_x"] = round(adj_l, 3)
        row["late_under"] = round(under_l, 3)
        row["late_ups"] = m_late["ups"]
        row["late_downs"] = m_late["downs"]
        row["mean_nac"] = round(m_late.get("mean_nac_mult", ch.nac_mult), 3)
        row["min_raw_x"] = round(min(xs), 3)
        row["min_adj_x"] = round(min(adjs), 3)
        row["dominates_adj"] = bool(min(adjs) >= 0.99 and adj_l >= 1.05 and under_l <= v9_late["under"] + 0.02)
        row["formula"] = (
            f"style={ch.style} mf_pull={ch.mf_pull} up×{ch.up_mult} step×{ch.step_mult} "
            f"nac×{ch.nac_mult} dyn={ch.nac_dyn} amp={ch.nac_amp} "
            f"bands={ch.lo}/{ch.hi}/{ch.over} dbr={ch.down_block_ratio}"
        )
        final_rows.append(row)
        print(
            f"  {ch.name}: late_raw×{raw_l:.3f} adj×{adj_l:.3f} under={under_l:.3f} "
            f"min_adj={min(adjs):.3f} | {row['formula']}",
            flush=True,
        )

    final_rows.sort(key=lambda r: (-r["late_adj_x"], -r["late_raw_x"], r["late_under"]))
    out = {
        "meta": {
            "kind": "ultra_free",
            "db": DB,
            "t1": T1,
            "pop": pop_n,
            "gens": gens,
            "elite": elite_n,
            "elapsed_s": round(time.time() - t0, 1),
            "v9_late_24h_m": v9_late["profit_24h_mean_m"],
            "v9_late_under": v9_late["under"],
            "note": "late_adj_x = profit/mean_nac / v9 — algorithm lift stripped of markup scale",
        },
        "history": history,
        "leaderboard": final_rows,
        "best": final_rows[0] if final_rows else None,
        "best_nac1_hint": next((r for r in final_rows if abs(r.get("mean_nac", 1) - 1.0) < 0.08), None),
    }
    with open(OUT, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWROTE {OUT}", flush=True)
    if final_rows:
        b = final_rows[0]
        print("BEST", b["name"], "late_adj×", b["late_adj_x"], "late_raw×", b["late_raw_x"], b["formula"], flush=True)
        if out["best_nac1_hint"]:
            h = out["best_nac1_hint"]
            print("BEST~nac1", h["name"], "adj×", h["late_adj_x"], h["formula"], flush=True)


if __name__ == "__main__":
    main()
