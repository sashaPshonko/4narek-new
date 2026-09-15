#!/usr/bin/env python3
"""
SUPER SEARCH — realistic AH-p10 simulator + constrained algorithm search.

Goals vs V10:
- Market = real ah_book_lots p10 (10m window, n≥40), Sep+ only (book exists).
- Demand soft-penalty when sim price deeply under market (anti-underprice cheat).
- Score = OOS mean 24h profit with hard/soft under_frac constraint vs v9.
- Output readable chrom + full compare vs v9.

Does NOT change production Go.
"""
from __future__ import annotations

import json
import math
import os
import random
import sqlite3
import sys
import time
from collections import defaultdict
from copy import deepcopy
from dataclasses import asdict
from datetime import datetime, timedelta
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
from search_v10 import (
    Chrom,
    chrom_v9,
    chrom_hold,
    decide,
    sample_chrom,
    grid_chroms,
)

SEED = 42
BOOK_T0 = "2026-09-03"  # ah_book starts ~here
FOCUS = S.FOCUS_ITEMS
DB = os.environ.get("PRICING_DB", "/root/4narek-new/ml_data/pricing.db")
OUT = os.environ.get("SUPER_OUT", os.path.join(os.path.dirname(__file__), "super_search_results.json"))

# Under-price demand haircut (observational: deep under ≠ more sales)
UNDER_HAIRCUT = [
    (0.70, 0.35),
    (0.80, 0.55),
    (0.85, 0.70),
    (0.90, 0.85),
    (0.95, 0.95),
]


def under_sales_mult(ratio: Optional[float]) -> float:
    if ratio is None:
        return 1.0
    for thr, m in UNDER_HAIRCUT:
        if ratio < thr:
            return m
    return 1.0


def attach_real_ah_p10(con: sqlite3.Connection, rows: List[dict], window_min: int = 10, min_n: int = 40) -> List[dict]:
    """Attach real book p10 via minute bins (fast enough for Sep panel)."""
    if not rows:
        return rows
    items = sorted({r["item_id"] for r in rows})
    t0 = min(r["ts"] for r in rows)
    # load lots from BOOK_T0 to cover window before first cycle
    bins: Dict[str, Dict[str, List[int]]] = {it: defaultdict(list) for it in items}
    for it in items:
        for ts, price in con.execute(
            """SELECT ts, price FROM ah_book_lots
               WHERE item_id=? AND price>0 AND ts>=? AND ts<=?
               ORDER BY ts""",
            (it, BOOK_T0, max(r["ts"] for r in rows)),
        ):
            # minute key YYYY-MM-DDTHH:MM
            bins[it][ts[:16]].append(int(price))

    # pre-sort each bin once
    for it in items:
        for k, ps in bins[it].items():
            ps.sort()

    def p10_at(it: str, ts: str) -> Optional[int]:
        # collect prices from last window_min minutes ending at ts minute
        try:
            end = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        except Exception:
            return None
        prices: List[int] = []
        for dmin in range(window_min):
            t = end - timedelta(minutes=dmin)
            key = t.strftime("%Y-%m-%dT%H:%M")
            prices.extend(bins[it].get(key, ()))
        if len(prices) < min_n:
            return None
        prices.sort()
        return prices[len(prices) // 10]

    cache: Dict[Tuple[str, str], Optional[int]] = {}
    out = []
    for r in rows:
        it = r["item_id"]
        key = (it, r["ts"][:16])
        if key not in cache:
            cache[key] = p10_at(it, r["ts"])
        p10 = cache[key]
        rr = dict(r)
        rr["p10"] = p10
        rr["mkt"] = p10  # policies read mkt or p10
        if p10 and r["price_before"]:
            rr["ratio"] = r["price_before"] / p10
        else:
            rr["ratio"] = None
        # fallback sell-median if book thin — keep for gates but flag
        rr["mkt_source"] = "ah_p10" if p10 else "missing"
        out.append(rr)

    # fallback fill missing mkt with sell median so sim doesn't die
    filled = S.attach_p10_fast(con, [r for r in out if r["mkt"] is None])
    by_ts = {(r["item_id"], r["ts"]): r for r in filled}
    for r in out:
        if r["mkt"] is None:
            fb = by_ts.get((r["item_id"], r["ts"]))
            if fb and fb.get("mkt"):
                r["mkt"] = fb["mkt"]
                r["p10"] = fb["mkt"]
                r["ratio"] = r["price_before"] / r["mkt"] if r["mkt"] else None
                r["mkt_source"] = "sell_median_fallback"
    return out


def simulate_super(rows_by_item, ch: Chrom, dm: S.DemandModel) -> Dict[str, Any]:
    """Like search_v10.simulate but with under haircut on expected sales."""
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
        nac = max(1, int(dm.nac.get(it, 200_000) * ch.nac_mult))
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
            if obs.get("mkt_source") == "ah_p10":
                book_ok += 1

            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0
            pre = dm.expected_sales(it, st.price, mkt, st.held, obs["share"], obs["ts"])
            pre *= under_sales_mult(st.price / mkt if mkt else None)
            if abs(st.price - obs["price_before"]) <= (obs["step"] or 100_000):
                gs = 0.7 * obs["sales"] + 0.3 * pre
            else:
                gs = 0.0 if empty_idle else pre
            gb = dm.expected_buys(gs, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle)
            obs_g = {**obs, "sales": gs, "buys": gb}

            action, new_price = decide(st, obs_g, ch)

            exp = dm.expected_sales(it, new_price, mkt, st.held, obs["share"], obs["ts"])
            exp *= under_sales_mult(new_price / mkt if mkt else None)
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
        "profit_24h_median_m": (sorted(day_vals)[len(day_vals) // 2] / 1e6) if day_vals else 0,
        "profit_24h_min_m": (min(day_vals) / 1e6) if day_vals else 0,
        "profit_24h_max_m": (max(day_vals) / 1e6) if day_vals else 0,
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
    }


def constrained_score(m: dict, v9: dict, under_slack: float = 0.06, min_days: int = 2) -> Optional[float]:
    """Higher better. None = reject."""
    if m["n_days"] < min_days:
        return None
    if m["under"] > v9["under"] + under_slack:
        return None
    if m["deep_under"] > max(0.12, v9.get("deep_under", 0) + 0.05):
        return None
    # primary: mean 24h; soft penalty residual under
    pen = 80.0 * max(0.0, m["under"] - v9["under"])  # M per day equiv
    return m["profit_24h_mean_m"] - pen


def mutate(ch: Chrom, rng: random.Random, i: int) -> Chrom:
    c = deepcopy(ch)
    c.name = f"m{i}"
    c.seed_tag = i
    c.nac_mult = 1.0
    for _ in range(rng.randint(1, 4)):
        field = rng.choice(
            [
                "lo", "hi", "over", "dump", "step_mult", "up_mult", "down_soft_mult",
                "down_hard_mult", "min_sales_up", "catchup_gap", "down_block_ratio",
                "soft_down_when_over_mkt", "style", "empty_streak", "up_cd",
            ]
        )
        if field == "lo":
            c.lo = rng.choice([0.10, 0.12, 0.15, 0.18, 0.20])
            c.hi = max(c.hi, c.lo + 0.05)
        elif field == "hi":
            c.hi = c.lo + rng.choice([0.05, 0.07, 0.08, 0.10, 0.12])
            c.over = max(c.over, c.hi + 0.05)
            c.dump = max(c.dump, c.over + 0.05)
        elif field == "over":
            c.over = c.hi + rng.choice([0.05, 0.08, 0.10, 0.12])
            c.dump = max(c.dump, c.over + 0.05)
        elif field == "dump":
            c.dump = min(0.9, c.over + rng.choice([0.05, 0.10, 0.15, 0.20]))
        elif field == "step_mult":
            c.step_mult = rng.choice([0.5, 0.75, 1.0, 1.25, 1.5])
        elif field == "up_mult":
            c.up_mult = rng.choice([0.5, 1.0, 1.5, 2.0])
        elif field == "down_soft_mult":
            c.down_soft_mult = rng.choice([0.5, 1.0, 1.5])
        elif field == "down_hard_mult":
            c.down_hard_mult = rng.choice([1.0, 2.0, 3.0])  # no ×4 (V10 trap)
        elif field == "min_sales_up":
            c.min_sales_up = rng.choice([2, 3, 4, 5])
            c.night_sales_up = c.min_sales_up + 1
        elif field == "catchup_gap":
            c.catchup_gap = rng.choice([0.80, 0.90, 0.95, 1.0, 1.05])
        elif field == "down_block_ratio":
            c.down_block_ratio = rng.choice([0.85, 0.90, 0.95, 1.01])
        elif field == "soft_down_when_over_mkt":
            c.soft_down_when_over_mkt = rng.choice([True, True, False])
        elif field == "style":
            c.style = rng.choice(["corridor", "corridor", "aggressive_down", "market_follow", "velocity"])
        elif field == "empty_streak":
            c.empty_streak = rng.choice([1, 2, 3, 4])
        elif field == "up_cd":
            c.up_cd = rng.choice([1, 2, 3])
    return c


def chrom_r131() -> Chrom:
    return Chrom(
        name="r131_seed",
        lo=0.10,
        hi=0.15,
        over=0.23,
        dump=0.33,
        step_mult=0.75,
        up_mult=1.5,
        down_soft_mult=1.0,
        down_hard_mult=1.0,
        min_sales_up=4,
        night_sales_up=5,
        catchup_gap=0.70,
        down_block_ratio=1.01,
        soft_down_when_over_mkt=True,
        style="corridor",
        nac_mult=1.0,
    )


def walk_forward_folds(rows: List[dict], n_folds: int = 3) -> List[dict]:
    """Expanding WF: fit → val(≥3d) → oos(≥3d), OOS windows from the end."""
    days = sorted({r["ts"][:10] for r in rows})
    n = len(days)
    val_len = 3
    oos_len = max(3, n // (n_folds + 2))
    folds = []
    for i in range(n_folds):
        oos_end = n - i * max(1, oos_len - 1)
        oos_start = oos_end - oos_len
        val_end = oos_start
        val_start = val_end - val_len
        if val_start < 4 or oos_start >= oos_end or val_start >= val_end:
            continue
        train_fit = set(days[:val_start])
        val_days = set(days[val_start:val_end])
        oos_days = set(days[oos_start:oos_end])
        if len(train_fit) < 4 or len(val_days) < 3 or len(oos_days) < 3:
            continue
        folds.append(
            {
                "fold": i,
                "train_fit": train_fit,
                "val": val_days,
                "oos": oos_days,
                "train_fit_range": (min(train_fit), max(train_fit)),
                "val_range": (min(val_days), max(val_days)),
                "oos_range": (min(oos_days), max(oos_days)),
            }
        )
    return folds


def filter_days(rows, dayset):
    return [r for r in rows if r["ts"][:10] in dayset]


def make_dm(rows_fit, nac) -> S.DemandModel:
    dm = S.DemandModel()
    dm.fit(rows_fit, nac)
    return dm


def eval_chrom(ch, dm: S.DemandModel, rows_eval_by):
    return simulate_super(rows_eval_by, ch, dm)


def main():
    n_random = int(sys.argv[1]) if len(sys.argv) > 1 else 800
    n_mut = int(sys.argv[2]) if len(sys.argv) > 2 else 400
    rng = random.Random(SEED)
    t0 = time.time()
    print(f"=== SUPER SEARCH db={DB} random={n_random} mut={n_mut} ===", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row

    # Sep+ panel (book era)
    rows = S.load_panel(con, FOCUS, t0=BOOK_T0, t1="2026-09-17")
    print(f"raw cycles={len(rows)}", flush=True)
    print("attaching real AH p10…", flush=True)
    rows = attach_real_ah_p10(con, rows)
    ok = sum(1 for r in rows if r.get("mkt_source") == "ah_p10")
    print(f"cycles={len(rows)} ah_p10={ok} ({100*ok/max(len(rows),1):.1f}%) fallback={len(rows)-ok}", flush=True)
    nac = S.avg_nacenka(con, FOCUS)

    folds = walk_forward_folds(rows, n_folds=3)
    print("folds:", flush=True)
    for fd in folds:
        print(
            f"  fold{fd['fold']} fit={fd['train_fit_range']} "
            f"val={fd.get('val_range')} oos={fd['oos_range']}",
            flush=True,
        )

    # pool
    pool: List[Chrom] = [chrom_v9(), chrom_hold(), chrom_r131()]
    # mild grid around r131 / v9
    for lo, hi in [(0.10, 0.15), (0.12, 0.18), (0.15, 0.22), (0.18, 0.25)]:
        for soft in [True, False]:
            for gap in [0.85, 0.95, 1.0]:
                for dbr in [0.90, 0.95, 1.01]:
                    c = chrom_v9()
                    c.name = f"g_{lo}_{hi}_{int(soft)}_{gap}_{dbr}"
                    c.lo, c.hi = lo, hi
                    c.over, c.dump = hi + 0.08, hi + 0.20
                    c.soft_down_when_over_mkt = soft
                    c.catchup_gap = gap
                    c.down_block_ratio = dbr
                    c.down_hard_mult = 2.0  # capped
                    pool.append(c)
    pool.extend(sample_chrom(rng, i) for i in range(n_random))
    # mutate from seeds
    seeds = [chrom_v9(), chrom_r131()]
    for i in range(n_mut):
        pool.append(mutate(rng.choice(seeds), rng, 10_000 + i))
    # dedupe by name
    seen = set()
    uniq = []
    for c in pool:
        c.nac_mult = 1.0
        if c.down_hard_mult > 3.0:
            c.down_hard_mult = 3.0
        if c.name in seen:
            c.name = f"{c.name}_{c.seed_tag}"
        seen.add(c.name)
        uniq.append(c)
    pool = uniq
    print(f"pool={len(pool)}")

    all_winners = []
    fold_reports = []

    for fd in folds:
        fit_rows = filter_days(rows, fd["train_fit"])
        val_rows = filter_days(rows, fd["val"]) or fit_rows
        oos_rows = filter_days(rows, fd["oos"])
        if len(fit_rows) < 50 or len(oos_rows) < 30:
            print(f"  skip fold{fd['fold']} thin")
            continue

        dm = make_dm(fit_rows, nac)
        by_val = S.split_by_item(val_rows)
        by_oos = S.split_by_item(oos_rows)
        v9_val = eval_chrom(chrom_v9(), dm, by_val)
        v9 = eval_chrom(chrom_v9(), dm, by_oos)
        print(
            f"\nfold{fd['fold']} v9 OOS 24h={v9['profit_24h_mean_m']:.1f}M "
            f"under={v9['under']:.3f} book={v9['book_ok_frac']:.2f}"
        )

        # train select on val (v9_val cached once)
        scored = []
        for i, ch in enumerate(pool):
            m_val = eval_chrom(ch, dm, by_val)
            sc = constrained_score(m_val, v9_val)
            if sc is None:
                continue
            scored.append((sc, ch, m_val))
            if (i + 1) % 200 == 0:
                print(f"  … val-scored {i+1}/{len(pool)} kept={len(scored)}")
        scored.sort(key=lambda x: -x[0])
        finalists = scored[:40]
        print(f"  val survivors={len(scored)} finalists={len(finalists)}")

        oos_ranked = []
        v9_oos = v9
        for sc, ch, _ in finalists:
            m = eval_chrom(ch, dm, by_oos)
            sc2 = constrained_score(m, v9_oos)
            if sc2 is None:
                continue
            oos_ranked.append(
                {
                    "name": ch.name,
                    "score": round(sc2, 3),
                    "oos_24h_m": round(m["profit_24h_mean_m"], 2),
                    "oos_x_v9": round(m["profit_24h_mean_m"] / max(v9_oos["profit_24h_mean_m"], 1e-6), 3),
                    "under": round(m["under"], 3),
                    "deep_under": round(m["deep_under"], 3),
                    "over": round(m["over"], 3),
                    "ups": m["ups"],
                    "downs": m["downs"],
                    "per_sku_m": m["per_sku_m"],
                    "chrom": asdict(ch),
                    "v9_oos_24h_m": round(v9_oos["profit_24h_mean_m"], 2),
                    "v9_under": round(v9_oos["under"], 3),
                }
            )
        oos_ranked.sort(key=lambda x: (-x["score"], -x["oos_24h_m"]))
        if oos_ranked:
            b = oos_ranked[0]
            print(
                f"  BEST {b['name']} 24h={b['oos_24h_m']}M x_v9={b['oos_x_v9']} "
                f"under={b['under']} (v9 under={b['v9_under']})"
            )
            all_winners.append(b)
        fold_reports.append(
            {
                "fold": fd["fold"],
                "oos_range": fd["oos_range"],
                "v9": {
                    "oos_24h_m": round(v9["profit_24h_mean_m"], 2),
                    "under": round(v9["under"], 3),
                    "book_ok_frac": round(v9["book_ok_frac"], 3),
                },
                "top5": oos_ranked[:5],
                "n_oos_ok": len(oos_ranked),
            }
        )

    # aggregate by name
    by_name: Dict[str, List[dict]] = defaultdict(list)
    for fr in fold_reports:
        for row in fr.get("top5") or []:
            by_name[row["name"]].append(row)
    summary = []
    for name, rs in by_name.items():
        xs = [r["oos_x_v9"] for r in rs]
        summary.append(
            {
                "name": name,
                "n_folds": len(xs),
                "mean_oos_x_v9": round(sum(xs) / len(xs), 3),
                "min_oos_x_v9": round(min(xs), 3),
                "max_oos_x_v9": round(max(xs), 3),
                "mean_oos_24h_m": round(sum(r["oos_24h_m"] for r in rs) / len(rs), 2),
                "mean_under": round(sum(r["under"] for r in rs) / len(rs), 3),
                "chrom": rs[0]["chrom"],
            }
        )
    summary.sort(key=lambda x: (-x["min_oos_x_v9"], -x["mean_oos_x_v9"]))

    # algorithm in plain language
    def chrom_to_rules(ch: dict) -> List[str]:
        return [
            f"целевой склад: lo={ch['lo']:.2f} hi={ch['hi']:.2f} over={ch['over']:.2f} dump={ch['dump']:.2f}",
            f"↓ только если held>hi; soft×{ch['down_soft_mult']} hard×{ch['down_hard_mult']}; "
            f"underprice veto ratio<{ch['down_block_ratio']}",
            f"↑ demand: sales≥{ch['min_sales_up']} (ночь {ch['night_sales_up']}), held в (0,lo), up×{ch['up_mult']}",
            f"empty catchup: {'on' if ch['catchup_on'] else 'off'} gap={ch['catchup_gap']} streak≥{ch['empty_streak']}",
            f"soft ↓ when over market: {ch['soft_down_when_over_mkt']}",
            f"style={ch['style']} step×{ch['step_mult']}",
        ]

    best = summary[0] if summary else None
    elapsed = time.time() - t0
    out = {
        "meta": {
            "db": DB,
            "book_t0": BOOK_T0,
            "n_pool": len(pool),
            "n_random": n_random,
            "n_mut": n_mut,
            "elapsed_sec": round(elapsed, 1),
            "market": "ah_book_lots p10 10m n>=40 (+sell median fallback)",
            "demand": "bucket E[sales] × under_haircut",
            "constraint": "reject if under > v9_under+0.06 or deep_under high",
            "goal": "max constrained OOS mean 24h profit vs v9",
        },
        "fold_reports": fold_reports,
        "leaderboard": summary[:30],
        "best": best,
        "best_rules": chrom_to_rules(best["chrom"]) if best else [],
        "v9_baseline_note": "same sim/haircut/constraint path",
    }
    with open(OUT, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {OUT} in {elapsed:.0f}s")
    if best:
        print(
            f"BEST {best['name']} mean_x={best['mean_oos_x_v9']} "
            f"min_x={best['min_oos_x_v9']} under={best['mean_under']}"
        )
        for line in out["best_rules"]:
            print("  •", line)
    print("DONE")


if __name__ == "__main__":
    main()
