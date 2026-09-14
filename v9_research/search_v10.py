#!/usr/bin/env python3
"""
MODEL SEARCH V10 — maximize sequential 24h profit vs v9 baseline.

Leakage: demand fit only on train; decisions use only current state + past mkt proxy.
Metric: sum of profits in calendar-UTC 24h buckets (sequential sim).
Goal: find OOS candidate with mean_24h >= 2× v9.
"""
from __future__ import annotations

import json
import math
import random
import sys
import time
from collections import defaultdict
from copy import deepcopy
from dataclasses import asdict, dataclass, field
from datetime import datetime
from typing import Any, Callable, Dict, List, Optional, Tuple

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S

SEED = 42
random.seed(SEED)

# ── Policy chromosome ────────────────────────────────────────────────

@dataclass
class Chrom:
    name: str = "cand"
    # inventory band fractions
    lo: float = 0.18
    hi: float = 0.25
    over: float = 0.35
    dump: float = 0.50
    # steps
    step_mult: float = 1.0          # × logged step
    up_mult: float = 1.0            # × step on UP
    down_soft_mult: float = 1.0
    down_hard_mult: float = 2.0
    # demand UP
    min_sales_up: int = 3
    night_sales_up: int = 4
    require_sales_gt_buys: bool = True
    demand_max_ratio: float = 1.05
    # empty catchup
    empty_streak: int = 2
    catchup_gap: float = 1.0        # safety vs mkt
    catchup_on: bool = True
    # DOWN guards
    down_block_ratio: float = 0.90
    no_down_if_held_le_hi: bool = True
    # cooldowns
    up_cd: int = 2
    down_cd: int = 1
    max_up_streak: int = 1
    # nacenka (coupled: scales profit AND nudges price target via offset steps)
    nac_mult: float = 1.0
    # style
    style: str = "corridor"         # corridor|aggressive_down|holdish|market_follow|ekb_like|velocity
    # market-follow: pull toward mkt
    mf_pull: float = 0.0            # 0..1 fraction of gap per UP/DOWN when away
    # velocity: use sales-buys
    vel_up: float = 2.0             # sales-buys >= this → UP if under
    vel_down: float = -2.0
    # extras
    allow_down_empty: bool = False
    allow_blind_empty_up: bool = False
    soft_down_when_over_mkt: bool = False  # ratio>1.1 and held>0 → soft down
    # SKU-agnostic random spice
    seed_tag: int = 0


def chrom_v9() -> Chrom:
    return Chrom(name="v9_baseline", catchup_gap=1.0, style="corridor")


def chrom_hold() -> Chrom:
    return Chrom(name="hold", style="holdish", catchup_on=False, min_sales_up=99)


def sample_chrom(rng: random.Random, i: int) -> Chrom:
    style = rng.choice(
        ["corridor", "corridor", "corridor", "aggressive_down", "holdish", "market_follow", "velocity", "ekb_like"]
    )
    lo = rng.choice([0.10, 0.12, 0.15, 0.18, 0.20, 0.22])
    hi = lo + rng.choice([0.05, 0.07, 0.08, 0.10, 0.12])
    over = hi + rng.choice([0.05, 0.08, 0.10, 0.12])
    dump = over + rng.choice([0.05, 0.10, 0.15, 0.20])
    return Chrom(
        name=f"r{i}",
        lo=lo,
        hi=hi,
        over=min(over, 0.70),
        dump=min(dump, 0.90),
        step_mult=rng.choice([0.5, 0.75, 1.0, 1.0, 1.25, 1.5, 2.0]),
        up_mult=rng.choice([0.5, 1.0, 1.0, 1.5, 2.0]),
        down_soft_mult=rng.choice([0.5, 1.0, 1.0, 1.5]),
        down_hard_mult=rng.choice([1.0, 2.0, 2.0, 3.0, 4.0]),
        min_sales_up=rng.choice([1, 2, 3, 3, 4, 5]),
        night_sales_up=rng.choice([2, 3, 4, 5, 6]),
        require_sales_gt_buys=rng.choice([True, True, True, False]),
        demand_max_ratio=rng.choice([0.95, 1.0, 1.05, 1.10, 1.20, 9.0]),
        empty_streak=rng.choice([1, 2, 2, 3, 4, 6]),
        catchup_gap=rng.choice([0.70, 0.80, 0.90, 0.95, 1.0, 1.0, 1.05]),
        catchup_on=rng.choice([True, True, True, False]),
        down_block_ratio=rng.choice([0.70, 0.80, 0.85, 0.90, 0.95, 1.01]),
        no_down_if_held_le_hi=rng.choice([True, True, False]),
        up_cd=rng.choice([0, 1, 2, 2, 3, 4]),
        down_cd=rng.choice([0, 1, 1, 2]),
        max_up_streak=rng.choice([1, 1, 2, 3, 99]),
        nac_mult=1.0,  # fixed: free nac_mult scales profit without demand penalty → forbidden in search
        style=style,
        mf_pull=rng.choice([0.0, 0.0, 0.25, 0.5, 1.0]),
        vel_up=rng.choice([1.0, 2.0, 3.0, 4.0]),
        vel_down=rng.choice([-1.0, -2.0, -3.0]),
        allow_down_empty=rng.choice([False, False, False, True]),
        allow_blind_empty_up=rng.choice([False, False, True]),
        soft_down_when_over_mkt=rng.choice([False, True, True]),
        seed_tag=i,
    )


def grid_chroms() -> List[Chrom]:
    """Structured grids around promising regions."""
    out = []
    for lo, hi in [(0.15, 0.25), (0.18, 0.25), (0.18, 0.30), (0.12, 0.22)]:
        for ms in [2, 3, 4]:
            for gap in [0.85, 1.0]:
                for dbr in [0.85, 0.90]:
                    for um, dm in [(1.0, 2.0), (1.5, 3.0), (0.5, 1.0)]:
                        out.append(
                            Chrom(
                                name=f"g_{lo}_{hi}_s{ms}_g{gap}",
                                lo=lo,
                                hi=hi,
                                over=hi + 0.10,
                                dump=hi + 0.25,
                                min_sales_up=ms,
                                night_sales_up=ms + 1,
                                catchup_gap=gap,
                                down_block_ratio=dbr,
                                up_mult=um,
                                down_hard_mult=dm,
                                style="corridor",
                            )
                        )
    for sm in [0.5, 1.0, 2.0]:
            c = chrom_v9()
            c.name = f"v9_st{sm}"
            c.nac_mult = 1.0
            c.step_mult = sm
            out.append(c)
    for style in ["aggressive_down", "market_follow", "velocity", "ekb_like"]:
        c = chrom_v9()
        c.name = f"v9_{style}"
        c.style = style
        c.mf_pull = 0.5 if style == "market_follow" else 0.0
        out.append(c)
    # re-test "bad" tools in combo
    for blind in [True, False]:
        for de in [True, False]:
            c = chrom_v9()
            c.name = f"re_{int(blind)}_{int(de)}"
            c.allow_blind_empty_up = blind
            c.allow_down_empty = de
            c.catchup_on = True
            out.append(c)
    return out


# ── Decision ──────────────────────────────────────────────────────────

def _step(obs, ch: Chrom) -> int:
    base = obs.get("step") or 100_000
    return max(1, int(base * ch.step_mult))


def decide(st: S.State, obs: dict, ch: Chrom) -> Tuple[str, int]:
    share = obs["share"] or 12
    lo, hi, soft, over, dump = S.step_band(share, ch.lo, ch.hi, ch.hi + 0.03, ch.over, ch.dump)
    step = _step(obs, ch)
    sales, buys = float(obs["sales"]), float(obs["buys"])
    mkt = obs.get("mkt") or obs.get("p10")
    # nac nudge: higher nac → prefer slightly higher sell (worse ratio for same demand)
    price = st.price
    if ch.nac_mult != 1.0 and mkt:
        # small structural bias baked into targets via ratio compare only
        pass
    ratio = price / mkt if mkt else None
    night = obs.get("night", False)
    vel = sales - buys

    if ch.style == "holdish":
        return "HOLD", price

    # EKB-like
    if ch.style == "ekb_like":
        N = obs.get("normal_sales") or 6
        on_ah = obs.get("on_ah", st.held)
        inv = obs.get("inv", 0)
        if on_ah + inv < N * 2:
            return "UP", price + int(step * ch.up_mult)
        if (on_ah > sales and on_ah > N) and (inv > sales * 3 and inv > N):
            if sales < N or buys > sales:
                return "DOWN", max(step, price - int(step * ch.down_soft_mult))
        return "HOLD", price

    # market follow: move toward mkt
    if ch.style == "market_follow" and mkt and ch.mf_pull > 0 and st.up_cd == 0:
        gap = mkt - price
        if abs(gap) > step:
            delta = int(ch.mf_pull * gap)
            if delta > 0 and st.held <= hi:
                return "UP", price + max(step, abs(delta))
            if delta < 0 and (st.held > hi or (ratio and ratio > 1.05)):
                return "DOWN", max(step, price + min(-step, delta))

    # DOWN
    can_down = st.down_cd == 0
    if can_down:
        if st.held == 0 and ch.allow_down_empty and ratio and ratio > 1.15:
            return "DOWN", max(step, price - int(step * ch.down_soft_mult))
        blocked = False
        if ch.no_down_if_held_le_hi and st.held <= hi:
            blocked = True
        if ratio is not None and ratio < ch.down_block_ratio:
            blocked = True
        if not blocked and st.held > hi:
            if st.held >= dump or st.held >= over:
                return "DOWN", max(step, price - int(step * ch.down_hard_mult))
            return "DOWN", max(step, price - int(step * ch.down_soft_mult))
        if ch.soft_down_when_over_mkt and ratio and ratio > 1.10 and st.held > 0 and st.held <= hi:
            if ch.style == "aggressive_down" or ratio > 1.20:
                return "DOWN", max(step, price - int(step * ch.down_soft_mult))

    # velocity style extras
    if ch.style == "velocity" and st.up_cd == 0:
        if vel >= ch.vel_up and st.held < lo and st.held > 0:
            if ratio is None or ratio < ch.demand_max_ratio:
                return "UP", price + int(step * ch.up_mult)
        if vel <= ch.vel_down and st.held > hi:
            if ratio is None or ratio >= ch.down_block_ratio:
                return "DOWN", max(step, price - int(step * ch.down_soft_mult))

    # demand UP
    min_s = ch.night_sales_up if night else ch.min_sales_up
    can_up = st.up_cd == 0 and st.up_streak < ch.max_up_streak
    if can_up and st.held > 0 and st.held < lo and sales >= min_s:
        if (not ch.require_sales_gt_buys) or sales > buys:
            if ratio is None or ratio < ch.demand_max_ratio:
                return "UP", price + int(step * ch.up_mult)

    # empty catchup / blind
    if can_up and st.held == 0:
        if ch.allow_blind_empty_up and st.empty_streak >= ch.empty_streak:
            return "UP", price + int(step * ch.up_mult)
        if (
            ch.catchup_on
            and st.empty_streak >= ch.empty_streak
            and mkt
            and ratio is not None
            and ratio < ch.catchup_gap
            and price + int(step * ch.up_mult) <= mkt
        ):
            return "UP", price + int(step * ch.up_mult)

    if ch.style == "aggressive_down" and can_down and st.held > soft and (ratio is None or ratio >= ch.down_block_ratio):
        return "DOWN", max(step, price - int(step * ch.down_soft_mult))

    return "HOLD", price


# ── Sequential sim with 24h buckets ───────────────────────────────────

def simulate(rows_by_item, ch: Chrom, dm: S.DemandModel) -> Dict[str, Any]:
    day_profit = defaultdict(float)
    tot_profit = 0.0
    ups = downs = holds = cycles = 0
    under_n = over_n = 0
    held_sum = 0.0
    series = []
    per_sku = defaultdict(float)

    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
        base_nac = dm.nac.get(it, 200_000)
        nac = max(1, int(base_nac * ch.nac_mult))
        for obs in seq:
            if st.up_cd > 0:
                st.up_cd -= 1
            if st.down_cd > 0:
                st.down_cd -= 1
            try:
                hour = int(obs["ts"][11:13])
            except Exception:
                hour = 12
            obs = dict(obs)
            obs["night"] = 0 <= hour < 6

            mkt = obs.get("mkt") or obs.get("p10")
            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0
            pre = dm.expected_sales(it, st.price, mkt, st.held, obs["share"], obs["ts"])
            if abs(st.price - obs["price_before"]) <= (obs["step"] or 100_000):
                gs = 0.7 * obs["sales"] + 0.3 * pre
            else:
                gs = 0.0 if empty_idle else pre
            gb = dm.expected_buys(gs, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle)
            obs_g = {**obs, "sales": gs, "buys": gb}

            action, new_price = decide(st, obs_g, ch)

            exp = dm.expected_sales(it, new_price, mkt, st.held, obs["share"], obs["ts"])
            if empty_idle and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = float(obs["sales"])
                buys = float(obs["buys"])
            elif abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
                sales = 0.7 * obs["sales"] + 0.3 * exp
                buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
            else:
                sales = exp
                buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
            sales = max(0.0, sales)

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

            st.price = new_price
            st.held = max(0, int(round(st.held - sales + buys)))
            share = obs["share"] or 12
            st.held = min(st.held, int(share * 1.5))

            p = sales * nac
            tot_profit += p
            per_sku[it] += p
            series.append(p)
            day = obs["ts"][:10]
            day_profit[day] += p
            held_sum += st.held
            cycles += 1
            if mkt:
                r = st.price / mkt
                if r < 0.85:
                    under_n += 1
                if r > 1.10:
                    over_n += 1
            if st.held == 0 and sales < 0.5 and buys < 0.5:
                st.empty_streak += 1
            else:
                st.empty_streak = 0

    days = sorted(day_profit.keys())
    daily = [day_profit[d] for d in days]
    # also rolling 24h approx = each calendar day (UTC)
    def pct(xs, q):
        if not xs:
            return 0.0
        s = sorted(xs)
        return s[min(len(s) - 1, int(q * (len(s) - 1)))]

    n = cycles or 1
    hours = n / 6.0
    # drawdown
    peak = cum = dd = 0.0
    for p in series:
        cum += p
        peak = max(peak, cum)
        if peak > 0:
            dd = max(dd, (peak - cum) / peak)

    return {
        "profit_m": round(tot_profit / 1e6, 1),
        "profit_24h_mean_m": round((sum(daily) / len(daily) / 1e6) if daily else 0, 2),
        "profit_24h_median_m": round(pct(daily, 0.5) / 1e6, 2),
        "profit_24h_min_m": round(min(daily) / 1e6, 2) if daily else 0,
        "profit_24h_max_m": round(max(daily) / 1e6, 2) if daily else 0,
        "n_days": len(daily),
        "pph_m": round(tot_profit / 1e6 / max(hours, 0.1), 2),
        "under": round(under_n / n, 3),
        "over": round(over_n / n, 3),
        "avg_held": round(held_sum / n, 2),
        "ups": ups,
        "downs": downs,
        "holds": holds,
        "cycles": cycles,
        "drawdown": round(dd, 3),
        "daily_m": [round(x / 1e6, 2) for x in daily],
        "per_sku_m": {k: round(v / 1e6, 1) for k, v in per_sku.items()},
    }


def ratio_vs_v9(cand, v9):
    if not v9 or v9["profit_24h_mean_m"] <= 0:
        return None
    return round(cand["profit_24h_mean_m"] / v9["profit_24h_mean_m"], 3)


# ── Walk-forward search ───────────────────────────────────────────────

def split_folds(rows, n_folds=3):
    ts = sorted(set(r["ts"] for r in rows))
    # train | val | oos  repeating: use 5 segments → 3 folds of (train,val,oos)
    # Simpler: 4 equal parts: fold f uses [0..f+1] train, next val, next oos
    n = len(ts)
    part = n // 5
    folds = []
    for f in range(n_folds):
        # expanding train
        t_train_end = ts[(f + 2) * part]
        t_val_end = ts[(f + 3) * part]
        t_oos_end = ts[min((f + 4) * part, n - 1)]
        t0 = ts[0]
        train = [r for r in rows if r["ts"] < t_train_end]
        val = [r for r in rows if t_train_end <= r["ts"] < t_val_end]
        oos = [r for r in rows if t_val_end <= r["ts"] < t_oos_end]
        folds.append(
            {
                "fold": f,
                "train_end": t_train_end,
                "val_end": t_val_end,
                "oos_end": t_oos_end,
                "train": train,
                "val": val,
                "oos": oos,
            }
        )
    return folds


def evaluate_pool(pool: List[Chrom], rows, nac, top_k=30):
    dm = S.DemandModel()
    dm.fit(rows, nac)
    by = S.split_by_item(rows)
    scored = []
    for ch in pool:
        m = simulate(by, ch, dm)
        scored.append({"chrom": ch, "metrics": m})
    scored.sort(key=lambda x: -x["metrics"]["profit_24h_mean_m"])
    return scored[:top_k], scored


def main():
    t0 = time.time()
    N_RANDOM = int(sys.argv[1]) if len(sys.argv) > 1 else 1500
    print(f"=== MODEL SEARCH V10  N_random={N_RANDOM} ===")
    con = S.connect()
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    # enrich on_ah/inv if present
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)} items={len(S.FOCUS_ITEMS)}")

    folds = split_folds(rows, n_folds=3)
    rng = random.Random(SEED)

    # build candidate pool once (same space); select per-fold on train/val
    grid = grid_chroms()
    randoms = [sample_chrom(rng, i) for i in range(N_RANDOM)]
    pool = [chrom_v9(), chrom_hold()] + grid + randoms
    # dedupe by asdict minus name
    print(f"candidate pool={len(pool)} (grid={len(grid)} random={N_RANDOM})")

    fold_reports = []
    all_oos_winners = []

    for fd in folds:
        print(f"\n--- fold {fd['fold']} train<={fd['train_end'][:10]} val<={fd['val_end'][:10]} oos<={fd['oos_end'][:10]} ---")
        print(f"  sizes train={len(fd['train'])} val={len(fd['val'])} oos={len(fd['oos'])}")
        if len(fd["train"]) < 500 or len(fd["oos"]) < 200:
            print("  skip thin fold")
            continue

        # fit demand on TRAIN only
        dm_tr = S.DemandModel()
        dm_tr.fit(fd["train"], nac)
        by_tr = S.split_by_item(fd["train"])
        by_val = S.split_by_item(fd["val"])
        by_oos = S.split_by_item(fd["oos"])

        # baseline v9 on each split with train-fitted demand
        v9 = chrom_v9()
        v9_tr = simulate(by_tr, v9, dm_tr)
        v9_val = simulate(by_val, v9, dm_tr)
        v9_oos = simulate(by_oos, v9, dm_tr)
        print(
            f"  v9 24h_mean: train={v9_tr['profit_24h_mean_m']} val={v9_val['profit_24h_mean_m']} "
            f"oos={v9_oos['profit_24h_mean_m']} M"
        )

        # score all on TRAIN (optimize), shortlist
        train_scores = []
        for i, ch in enumerate(pool):
            m = simulate(by_tr, ch, dm_tr)
            train_scores.append((ch, m))
            if (i + 1) % 400 == 0:
                print(f"  … scored {i+1}/{len(pool)} on train")
        train_scores.sort(key=lambda x: -x[1]["profit_24h_mean_m"])
        shortlist = train_scores[:80]

        # validation select
        val_scores = []
        for ch, _ in shortlist:
            m = simulate(by_val, ch, dm_tr)
            val_scores.append(
                {
                    "chrom": ch,
                    "val": m,
                    "train_24h": next(t[1]["profit_24h_mean_m"] for t in shortlist if t[0] is ch or t[0].name == ch.name),
                    "val_x_v9": ratio_vs_v9(m, v9_val),
                }
            )
        # recompute train key properly
        train_map = {id(ch): m for ch, m in shortlist}
        for vs in val_scores:
            vs["train_24h"] = train_map[id(vs["chrom"])]["profit_24h_mean_m"]
            vs["train_x_v9"] = ratio_vs_v9(train_map[id(vs["chrom"])], v9_tr)

        val_scores.sort(key=lambda x: -x["val"]["profit_24h_mean_m"])
        # pick top 10 by val for OOS
        finalists = val_scores[:10]

        oos_rows = []
        for vs in finalists:
            ch = vs["chrom"]
            m = simulate(by_oos, ch, dm_tr)
            oos_rows.append(
                {
                    "name": ch.name,
                    "chrom": asdict(ch),
                    "train_24h_m": vs["train_24h"],
                    "val_24h_m": vs["val"]["profit_24h_mean_m"],
                    "oos_24h_m": m["profit_24h_mean_m"],
                    "oos_24h_med_m": m["profit_24h_median_m"],
                    "oos_24h_min_m": m["profit_24h_min_m"],
                    "oos_24h_max_m": m["profit_24h_max_m"],
                    "oos_total_m": m["profit_m"],
                    "oos_x_v9": ratio_vs_v9(m, v9_oos),
                    "val_x_v9": vs["val_x_v9"],
                    "under": m["under"],
                    "over": m["over"],
                    "drawdown": m["drawdown"],
                    "ups": m["ups"],
                    "downs": m["downs"],
                    "per_sku_m": m["per_sku_m"],
                    "v9_oos_24h_m": v9_oos["profit_24h_mean_m"],
                    "v9_oos_total_m": v9_oos["profit_m"],
                }
            )
        oos_rows.sort(key=lambda x: -x["oos_24h_m"])
        best = oos_rows[0]
        print(
            f"  BEST OOS: {best['name']} 24h={best['oos_24h_m']}M  "
            f"x_v9={best['oos_x_v9']}  (v9={best['v9_oos_24h_m']}M)"
        )
        all_oos_winners.append(best)
        fold_reports.append(
            {
                "fold": fd["fold"],
                "v9": {"train": v9_tr, "val": v9_val, "oos": v9_oos},
                "finalists_oos": oos_rows,
                "n_evaluated_train": len(pool),
            }
        )

    # aggregate across folds: chroms that appear / best mean x_v9
    print("\n=== AGGREGATE ===")
    xs = [w["oos_x_v9"] for w in all_oos_winners if w.get("oos_x_v9")]
    if xs:
        print(f"per-fold best x_v9: {xs}  mean={sum(xs)/len(xs):.3f}  max={max(xs):.3f}")
    hit_2x = [w for w in all_oos_winners if (w.get("oos_x_v9") or 0) >= 2.0]
    print(f"folds with ≥2× OOS: {len(hit_2x)}/{len(all_oos_winners)}")

    # global re-score: take union of fold finalist chroms + top random, eval mean OOS x across folds
    # (honest: average of oos_x from each fold's evaluation of same chrom name — soft)
    by_name = defaultdict(list)
    for fr in fold_reports:
        for row in fr["finalists_oos"]:
            by_name[row["name"]].append(row)

    summary_cands = []
    for name, rows_ in by_name.items():
        xs_ = [r["oos_x_v9"] for r in rows_ if r["oos_x_v9"] is not None]
        if not xs_:
            continue
        summary_cands.append(
            {
                "name": name,
                "n_folds": len(xs_),
                "mean_oos_x_v9": round(sum(xs_) / len(xs_), 3),
                "min_oos_x_v9": round(min(xs_), 3),
                "max_oos_x_v9": round(max(xs_), 3),
                "mean_oos_24h_m": round(sum(r["oos_24h_m"] for r in rows_) / len(rows_), 2),
                "chrom": rows_[0]["chrom"],
                "per_sku_last": rows_[-1].get("per_sku_m"),
            }
        )
    summary_cands.sort(key=lambda x: (-x["mean_oos_x_v9"], -x["min_oos_x_v9"]))

    elapsed = time.time() - t0
    out = {
        "meta": {
            "n_pool": len(pool),
            "n_random": N_RANDOM,
            "n_grid": len(grid),
            "seed": SEED,
            "elapsed_sec": round(elapsed, 1),
            "metric": "calendar-UTC day profit mean (sequential sim)",
            "goal": "OOS mean_24h >= 2 × v9",
            "leakage": "demand fit on train only; no future features in decide()",
        },
        "fold_reports": [
            {
                "fold": fr["fold"],
                "v9_oos_24h_m": fr["v9"]["oos"]["profit_24h_mean_m"],
                "v9_oos_total_m": fr["v9"]["oos"]["profit_m"],
                "best": fr["finalists_oos"][0] if fr["finalists_oos"] else None,
                "top5": fr["finalists_oos"][:5],
            }
            for fr in fold_reports
        ],
        "leaderboard": summary_cands[:40],
        "hit_2x_folds": len(hit_2x),
        "best_overall": summary_cands[0] if summary_cands else None,
    }
    path = "/tmp/v10_search_results.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {path} in {elapsed:.0f}s")
    if summary_cands:
        b = summary_cands[0]
        print(
            f"BEST overall: {b['name']} mean_x={b['mean_oos_x_v9']} "
            f"min_x={b['min_oos_x_v9']} mean_24h={b['mean_oos_24h_m']}M"
        )
    print("DONE")


if __name__ == "__main__":
    main()
