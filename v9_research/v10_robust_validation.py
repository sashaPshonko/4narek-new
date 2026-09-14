#!/usr/bin/env python3
"""
V10 robust validation — NO production changes.
Observational elasticity + event-study + multi-demand re-eval of V10 vs baselines.
"""
from __future__ import annotations

import json
import math
import random
import sys
import time
from collections import defaultdict
from copy import deepcopy
from dataclasses import asdict
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, "/tmp/v9")
try:
    import sim_v9 as S
    import search_v10 as V10
except ImportError:
    sys.path.insert(0, ".")
    import sim_v9 as S
    import search_v10 as V10

SEED = 42
random.seed(SEED)

# Fixed chroms from V10 search (nac=1)
def chrom_r131():
    c = V10.Chrom(
        name="v10_r131",
        lo=0.10, hi=0.15, over=0.23, dump=0.33,
        step_mult=0.75, up_mult=1.5, down_soft_mult=1.0, down_hard_mult=1.0,
        min_sales_up=4, night_sales_up=4, demand_max_ratio=1.2,
        empty_streak=2, catchup_gap=0.7, catchup_on=True,
        down_block_ratio=1.01, no_down_if_held_le_hi=True,
        up_cd=1, down_cd=1, max_up_streak=1, nac_mult=1.0,
        style="corridor", soft_down_when_over_mkt=True,
    )
    return c

def chrom_r1100():
    c = V10.Chrom(
        name="v10_r1100",
        lo=0.10, hi=0.15, over=0.20, dump=0.25,
        step_mult=1.5, up_mult=0.5, down_soft_mult=0.5, down_hard_mult=4.0,
        min_sales_up=4, night_sales_up=6, demand_max_ratio=0.95,
        empty_streak=4, catchup_gap=0.95, catchup_on=True,
        down_block_ratio=1.01, no_down_if_held_le_hi=True,
        up_cd=2, down_cd=0, max_up_streak=1, nac_mult=1.0,
        style="aggressive_down", mf_pull=1.0, soft_down_when_over_mkt=True,
    )
    return c

def chrom_old_corridor():
    c = V10.chrom_v9()
    c.name = "old_corridor_v4ish"
    c.catchup_on = False
    c.down_block_ratio = 0.0  # no underprice veto
    c.min_sales_up = 3
    c.style = "corridor"
    c.soft_down_when_over_mkt = False
    return c


# ── Demand models ────────────────────────────────────────────────────

class DemandA(S.DemandModel):
    """Current V9/V10 model."""
    label = "A_current"


class DemandC:
    """Empirical bucket: store list of sales; sample mean (same as A but explicit distribution stats)."""
    label = "C_empirical"

    def __init__(self):
        self.samples = defaultdict(list)
        self.nac = {}
        self.default = 1.5

    def fit(self, rows, nac):
        self.nac = nac
        for r in rows:
            rb = S.ratio_bucket(r.get("ratio"))
            hb = S.held_bucket(r["held"], r["share"] or 12)
            night = "night" if 0 <= S.hour_utc(r["ts"]) < 6 else "day"
            self.samples[(r["item_id"], rb, hb, night)].append(float(r["sales"]))
        self.default = sum(r["sales"] for r in rows) / max(len(rows), 1)

    def expected_sales(self, item, price, mkt, held, share, ts) -> float:
        rb = S.ratio_bucket(price / mkt if mkt else None)
        hb = S.held_bucket(held, share or 12)
        night = "night" if 0 <= S.hour_utc(ts) < 6 else "day"
        key = (item, rb, hb, night)
        xs = self.samples.get(key)
        if xs:
            return sum(xs) / len(xs)
        # backoff
        for k2 in [(item, rb, hb, "day"), (item, rb, "band", night)]:
            if self.samples.get(k2):
                return sum(self.samples[k2]) / len(self.samples[k2])
        return self.default

    def expected_buys(self, sales, held, share, logged_buys=None, empty_idle=False) -> float:
        return S.DemandModel.expected_buys(self, sales, held, share, logged_buys, empty_idle)


class DemandB:
    """Nearest-neighbor: kNN in (ratio, fill, night) within item; mean next sales of neighbors."""
    label = "B_knn"

    def __init__(self, k=25):
        self.k = k
        self.by_item = defaultdict(list)  # (ratio, fill, night, sales)
        self.nac = {}
        self.default = 1.5

    def fit(self, rows, nac):
        self.nac = nac
        for r in rows:
            mkt = r.get("mkt") or r.get("p10")
            if not mkt or r["price_before"] <= 0:
                continue
            ratio = r["price_before"] / mkt
            fill = r["held"] / max(r["share"] or 12, 1)
            night = 1.0 if 0 <= S.hour_utc(r["ts"]) < 6 else 0.0
            self.by_item[r["item_id"]].append((ratio, fill, night, float(r["sales"])))
        self.default = sum(r["sales"] for r in rows) / max(len(rows), 1)

    def expected_sales(self, item, price, mkt, held, share, ts) -> float:
        pts = self.by_item.get(item) or []
        if not pts or not mkt:
            return self.default
        ratio = price / mkt
        fill = held / max(share or 12, 1)
        night = 1.0 if 0 <= S.hour_utc(ts) < 6 else 0.0
        # distance
        scored = []
        for pr, pf, pn, s in pts:
            d = (pr - ratio) ** 2 + (pf - fill) ** 2 + 0.25 * (pn - night) ** 2
            scored.append((d, s))
        scored.sort(key=lambda x: x[0])
        top = scored[: min(self.k, len(scored))]
        return sum(s for _, s in top) / len(top)

    def expected_buys(self, sales, held, share, logged_buys=None, empty_idle=False) -> float:
        return S.DemandModel.expected_buys(self, sales, held, share, logged_buys, empty_idle)


class DemandD:
    """Simple linear-ish: sales ~ a + b*ratio + c*fill + d*night (per item OLS)."""
    label = "D_ols"

    def __init__(self):
        self.coef = {}  # item -> (a,b,c,d)
        self.nac = {}
        self.default = 1.5

    def fit(self, rows, nac):
        self.nac = nac
        by = defaultdict(list)
        for r in rows:
            mkt = r.get("mkt") or r.get("p10")
            if not mkt:
                continue
            ratio = r["price_before"] / mkt
            fill = r["held"] / max(r["share"] or 12, 1)
            night = 1.0 if 0 <= S.hour_utc(r["ts"]) < 6 else 0.0
            by[r["item_id"]].append((1.0, ratio, fill, night, float(r["sales"])))
        self.default = sum(r["sales"] for r in rows) / max(len(rows), 1)
        for it, rows_ in by.items():
            # normal equations for 4 params
            n = len(rows_)
            if n < 20:
                continue
            # X'X and X'y
            xtx = [[0.0] * 4 for _ in range(4)]
            xty = [0.0] * 4
            for row in rows_:
                x = row[:4]
                y = row[4]
                for i in range(4):
                    xty[i] += x[i] * y
                    for j in range(4):
                        xtx[i][j] += x[i] * x[j]
            # ridge
            for i in range(4):
                xtx[i][i] += 1e-3
            try:
                coef = _solve4(xtx, xty)
                self.coef[it] = coef
            except Exception:
                pass

    def expected_sales(self, item, price, mkt, held, share, ts) -> float:
        if item not in self.coef or not mkt:
            return self.default
        a, b, c, d = self.coef[item]
        ratio = price / mkt
        fill = held / max(share or 12, 1)
        night = 1.0 if 0 <= S.hour_utc(ts) < 6 else 0.0
        return max(0.0, a + b * ratio + c * fill + d * night)

    def expected_buys(self, sales, held, share, logged_buys=None, empty_idle=False) -> float:
        return S.DemandModel.expected_buys(self, sales, held, share, logged_buys, empty_idle)


def _solve4(A, b):
    """Gaussian elimination 4x4."""
    M = [A[i][:] + [b[i]] for i in range(4)]
    for i in range(4):
        piv = max(range(i, 4), key=lambda r: abs(M[r][i]))
        M[i], M[piv] = M[piv], M[i]
        div = M[i][i] or 1e-12
        for j in range(i, 5):
            M[i][j] /= div
        for r in range(4):
            if r == i:
                continue
            f = M[r][i]
            for j in range(i, 5):
                M[r][j] -= f * M[i][j]
    return [M[i][4] for i in range(4)]


# DemandA inherits buys from S.DemandModel


# ── Observational analyses ───────────────────────────────────────────

def elasticity_table(rows):
    """sales by ratio bucket with quantiles."""
    buckets = defaultdict(list)
    for r in rows:
        if r.get("ratio") is None:
            continue
        rb = S.ratio_bucket(r["ratio"])
        buckets[rb].append(float(r["sales"]))
    out = {}
    order = ["lt70", "70_85", "85_95", "95_105", "105_120", "gt120", "na"]
    for rb in order:
        xs = sorted(buckets.get(rb, []))
        if not xs:
            continue
        n = len(xs)
        def q(p):
            return xs[min(n - 1, int(p * (n - 1)))]
        mean = sum(xs) / n
        # rough 95% CI mean
        var = sum((x - mean) ** 2 for x in xs) / max(n - 1, 1)
        se = math.sqrt(var / n)
        out[rb] = {
            "n": n,
            "mean": round(mean, 3),
            "median": round(q(0.5), 3),
            "p25": round(q(0.25), 3),
            "p75": round(q(0.75), 3),
            "p10": round(q(0.10), 3),
            "p90": round(q(0.90), 3),
            "std": round(math.sqrt(var), 3),
            "ci95_mean": [round(mean - 1.96 * se, 3), round(mean + 1.96 * se, 3)],
        }
    return out


def event_study(rows, horizons_cycles=(1, 3, 6, 18, 36, 72, 144)):
    """
    Price change events: price_after != price_before.
    Measure cumulative sales over next H cycles vs matched HOLD controls (same item, nearby).
    Simple before-after: mean sales rate in next H vs previous 3 cycles.
    """
    by_item = S.split_by_item(rows)
    events = []
    for it, seq in by_item.items():
        for i, r in enumerate(seq):
            if r["price_after"] == r["price_before"]:
                continue
            delta = r["price_after"] - r["price_before"]
            direction = "UP" if delta > 0 else "DOWN"
            # prior sales rate (up to 3 cycles)
            prev = seq[max(0, i - 3) : i]
            prev_rate = sum(x["sales"] for x in prev) / max(len(prev), 1)
            ev = {
                "item": it,
                "ts": r["ts"],
                "direction": direction,
                "delta": delta,
                "ratio": r.get("ratio"),
                "held": r["held"],
                "prev_rate": prev_rate,
                "horizons": {},
            }
            for H in horizons_cycles:
                fut = seq[i + 1 : i + 1 + H]
                if not fut:
                    continue
                rate = sum(x["sales"] for x in fut) / len(fut)
                ev["horizons"][str(H)] = {
                    "n": len(fut),
                    "sales_rate": rate,
                    "delta_rate": rate - prev_rate,
                    "profit_sum": sum(x.get("profit_now") or 0 for x in fut),
                }
            events.append(ev)

    # aggregate
    summary = {}
    for direction in ("UP", "DOWN"):
        subset = [e for e in events if e["direction"] == direction]
        summary[direction] = {"n_events": len(subset), "by_horizon": {}}
        for H in horizons_cycles:
            key = str(H)
            deltas = [e["horizons"][key]["delta_rate"] for e in subset if key in e["horizons"]]
            if not deltas:
                continue
            mean = sum(deltas) / len(deltas)
            se = math.sqrt(sum((x - mean) ** 2 for x in deltas) / max(len(deltas), 1) / len(deltas))
            summary[direction]["by_horizon"][key] = {
                "n": len(deltas),
                "mean_delta_sales_rate": round(mean, 4),
                "ci95": [round(mean - 1.96 * se, 4), round(mean + 1.96 * se, 4)],
                "minutes_approx": H * 10,
            }
    return {"n_events_total": len(events), "summary": summary}


def under_vs_profit_scan(rows, nac, dm_factory, n_random=80):
    """Rescore random chroms + v9 + v10; correlate under_frac with profit_24h."""
    rng = random.Random(SEED)
    pool = [V10.chrom_v9(), V10.chrom_hold(), chrom_r131(), chrom_r1100(), chrom_old_corridor()]
    pool += [V10.sample_chrom(rng, i) for i in range(n_random)]
    for ch in pool:
        ch.nac_mult = 1.0
    dm = dm_factory()
    dm.fit(rows, nac)
    by = S.split_by_item(rows)
    pts = []
    for ch in pool:
        m = V10.simulate(by, ch, dm)
        pts.append({
            "name": ch.name,
            "under": m["under"],
            "over": m["over"],
            "profit_24h_m": m["profit_24h_mean_m"],
            "profit_m": m["profit_m"],
            "style": ch.style,
            "lo": ch.lo,
            "hi": ch.hi,
        })
    # correlation under vs profit
    if len(pts) >= 5:
        us = [p["under"] for p in pts]
        ps = [p["profit_24h_m"] for p in pts]
        mu, mp = sum(us) / len(us), sum(ps) / len(ps)
        num = sum((u - mu) * (p - mp) for u, p in zip(us, ps))
        den = math.sqrt(sum((u - mu) ** 2 for u in us) * sum((p - mp) ** 2 for p in ps)) or 1
        corr = num / den
    else:
        corr = None
    pts.sort(key=lambda x: -x["profit_24h_m"])
    return {"corr_under_profit24h": round(corr, 3) if corr is not None else None, "points": pts}


# ── Multi-demand policy matrix ───────────────────────────────────────

def eval_policies(policies, rows_train, rows_test, nac, dm_factory):
    dm = dm_factory()
    dm.fit(rows_train, nac)
    by = S.split_by_item(rows_test)
    out = {}
    for ch in policies:
        m = V10.simulate(by, ch, dm)
        out[ch.name] = {
            "profit_24h_mean_m": m["profit_24h_mean_m"],
            "profit_m": m["profit_m"],
            "under": m["under"],
            "over": m["over"],
            "drawdown": m["drawdown"],
            "per_sku_m": m["per_sku_m"],
            "daily_m": m["daily_m"],
            "ups": m["ups"],
            "downs": m["downs"],
        }
    # ratios vs v9
    v9p = out.get("v9_baseline", {}).get("profit_24h_mean_m") or 1e-9
    for name, m in out.items():
        m["x_v9"] = round(m["profit_24h_mean_m"] / v9p, 3)
    return out


def ablation(base: V10.Chrom, rows_train, rows_test, nac, dm_factory):
    variants = [("full", base)]
    specs = [
        ("no_low_band", dict(lo=0.18, hi=0.25, over=0.35, dump=0.50)),
        ("no_soft_over_mkt", dict(soft_down_when_over_mkt=False)),
        ("no_aggr_style", dict(style="corridor")),
        ("restore_down_block", dict(down_block_ratio=0.90)),
        ("smaller_hard_down", dict(down_hard_mult=2.0)),
        ("bigger_up", dict(up_mult=1.0)),
    ]
    for name, kw in specs:
        c = deepcopy(base)
        c.name = f"{base.name}__{name}"
        for k, v in kw.items():
            setattr(c, k, v)
        variants.append((name, c))
    dm = dm_factory()
    dm.fit(rows_train, nac)
    by = S.split_by_item(rows_test)
    out = {}
    for name, ch in variants:
        m = V10.simulate(by, ch, dm)
        out[name] = {
            "profit_24h_mean_m": m["profit_24h_mean_m"],
            "under": m["under"],
            "x_full": None,
        }
    full = out["full"]["profit_24h_mean_m"] or 1e-9
    for name, m in out.items():
        m["delta_vs_full_m"] = round(m["profit_24h_mean_m"] - full, 2)
        m["x_full"] = round(m["profit_24h_mean_m"] / full, 3)
    return out


def bootstrap_days(daily_a, daily_b, n_boot=500):
    """Paired bootstrap on day profits lists (pad to min len)."""
    n = min(len(daily_a), len(daily_b))
    if n < 3:
        return None
    a, b = daily_a[:n], daily_b[:n]
    rng = random.Random(SEED)
    diffs = []
    wins = 0
    for _ in range(n_boot):
        idx = [rng.randrange(n) for _ in range(n)]
        da = sum(a[i] for i in idx) / n
        db = sum(b[i] for i in idx) / n
        diffs.append(da - db)
        if da > db:
            wins += 1
    diffs.sort()
    return {
        "mean_diff_m": round(sum(diffs) / len(diffs), 3),
        "ci95": [round(diffs[int(0.025 * n_boot)], 3), round(diffs[int(0.975 * n_boot)], 3)],
        "p_a_gt_b": round(wins / n_boot, 3),
        "n_days": n,
    }


def sku_leave_one_out(per_sku_v10, per_sku_v9):
    items = sorted(set(per_sku_v10) | set(per_sku_v9))
    rows = []
    for it in items:
        a = per_sku_v10.get(it, 0)
        b = per_sku_v9.get(it, 0)
        rows.append({"item": it, "v10": a, "v9": b, "delta": round(a - b, 1), "ratio": round(a / b, 3) if b else None})
    rows.sort(key=lambda x: -abs(x["delta"]))
    total_v10 = sum(per_sku_v10.values())
    total_v9 = sum(per_sku_v9.values()) or 1
    # drop top1, top2
    by_abs = sorted(items, key=lambda it: -abs(per_sku_v10.get(it, 0) - per_sku_v9.get(it, 0)))
    def ratio_without(drop):
        a = sum(v for k, v in per_sku_v10.items() if k not in drop)
        b = sum(v for k, v in per_sku_v9.items() if k not in drop) or 1
        return round(a / b, 3)
    return {
        "per_sku": rows,
        "overall_x": round(total_v10 / total_v9, 3),
        "x_without_top1": ratio_without(set(by_abs[:1])),
        "x_without_top2": ratio_without(set(by_abs[:2])),
        "median_sku_ratio": round(
            sorted([r["ratio"] for r in rows if r["ratio"]])[len(rows) // 2], 3
        ) if rows else None,
    }


def main():
    t0 = time.time()
    print("=== V10 ROBUST VALIDATION ===")
    con = S.connect()
    rows = S.load_panel(con, S.FOCUS_ITEMS)
    rows = S.attach_p10_fast(con, rows)
    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    print(f"cycles={len(rows)}")

    # time folds: more cuts
    ts = sorted(set(r["ts"] for r in rows))
    n = len(ts)
    # 4 OOS windows of ~12.5% each at the end of expanding trains
    folds = []
    for f in range(4):
        # train: 0 .. 40%+10%*f, val next 10%, oos next 12%
        a = int(0.35 * n + 0.08 * f * n)
        b = int(a + 0.10 * n)
        c = int(b + 0.12 * n)
        a, b, c = min(a, n - 3), min(b, n - 2), min(c, n - 1)
        if b <= a or c <= b:
            continue
        train = [r for r in rows if r["ts"] < ts[a]]
        val = [r for r in rows if ts[a] <= r["ts"] < ts[b]]
        oos = [r for r in rows if ts[b] <= r["ts"] < ts[c]]
        if len(train) < 2000 or len(oos) < 1500:
            continue
        folds.append({"fold": f, "train": train, "val": val, "oos": oos, "cut": (ts[a], ts[b], ts[c])})
        print(f"fold{f}: train={len(train)} val={len(val)} oos={len(oos)} oos=[{ts[b][:10]}..{ts[c][:10]}]")

    policies = [
        V10.chrom_v9(),
        chrom_old_corridor(),
        V10.chrom_hold(),
        chrom_r131(),
        chrom_r1100(),
    ]

    print("\n[1] Elasticity (full sample associative)")
    elast = elasticity_table(rows)
    for k, v in elast.items():
        print(f"  {k}: n={v['n']} mean={v['mean']} med={v['median']} ci={v['ci95_mean']}")

    print("\n[2] Event study ΔP → Δ sales rate")
    es = event_study(rows)
    print(json.dumps(es["summary"], ensure_ascii=False, indent=2)[:1500])

    print("\n[3] Under vs profit scan (Demand A)")
    uv = under_vs_profit_scan(rows, nac, DemandA, n_random=60)
    print(f"  corr(under, profit24h)={uv['corr_under_profit24h']}")
    print("  top5 by profit:", [(p["name"], p["profit_24h_m"], p["under"]) for p in uv["points"][:5]])
    print("  bottom5 under:", sorted(uv["points"], key=lambda x: -x["under"])[:3])

    demand_factories = [
        ("A_current", DemandA),
        ("B_knn", DemandB),
        ("C_empirical", DemandC),
        ("D_ols", DemandD),
    ]

    print("\n[4] Multi-demand matrix on folds")
    matrix = []
    for fd in folds:
        fold_row = {"fold": fd["fold"], "cut": fd["cut"], "models": {}}
        for dname, DCl in demand_factories:
            print(f"  fold{fd['fold']} {dname}…")
            res = eval_policies(policies, fd["train"], fd["oos"], nac, DCl)
            fold_row["models"][dname] = res
        matrix.append(fold_row)

    # print x_v9 summary
    print("\n  x_v9 summary:")
    for fr in matrix:
        for dname, res in fr["models"].items():
            for pname in ("v10_r131", "v10_r1100", "old_corridor_v4ish", "hold"):
                if pname in res:
                    print(f"    fold{fr['fold']} {dname} {pname}: x={res[pname]['x_v9']} under={res[pname]['under']}")

    print("\n[5] Ablation r1100 / r131 on last fold Demand A")
    ablations = {}
    if folds:
        fd = folds[-1]
        ablations["r1100"] = ablation(chrom_r1100(), fd["train"], fd["oos"], nac, DemandA)
        ablations["r131"] = ablation(chrom_r131(), fd["train"], fd["oos"], nac, DemandA)
        print("  r1100 ablation:", ablations["r1100"])

    print("\n[6] Bootstrap + SKU LOO (last fold, Demand A)")
    boot = {}
    sku = {}
    if folds:
        fd = folds[-1]
        res = eval_policies(policies, fd["train"], fd["oos"], nac, DemandA)
        boot["r1100_vs_v9"] = bootstrap_days(res["v10_r1100"]["daily_m"], res["v9_baseline"]["daily_m"])
        boot["r131_vs_v9"] = bootstrap_days(res["v10_r131"]["daily_m"], res["v9_baseline"]["daily_m"])
        boot["r1100_vs_old"] = bootstrap_days(res["v10_r1100"]["daily_m"], res["old_corridor_v4ish"]["daily_m"])
        boot["r1100_vs_hold"] = bootstrap_days(res["v10_r1100"]["daily_m"], res["hold"]["daily_m"])
        sku["r1100"] = sku_leave_one_out(res["v10_r1100"]["per_sku_m"], res["v9_baseline"]["per_sku_m"])
        sku["r131"] = sku_leave_one_out(res["v10_r131"]["per_sku_m"], res["v9_baseline"]["per_sku_m"])
        print("  boot r1100 vs v9", boot["r1100_vs_v9"])
        print("  sku r1100", {k: sku["r1100"][k] for k in ("overall_x", "x_without_top1", "x_without_top2", "median_sku_ratio")})

    # AH p10 feasibility sample (Sep only)
    print("\n[7] AH p10 coverage check")
    ah_n = con.execute(
        "SELECT COUNT(*), MIN(ts), MAX(ts), COUNT(DISTINCT item_id) FROM ah_book_lots"
    ).fetchone()
    ah_info = {"n": ah_n[0], "min_ts": ah_n[1], "max_ts": ah_n[2], "items": ah_n[3]}

    out = {
        "meta": {
            "elapsed_sec": round(time.time() - t0, 1),
            "n_cycles": len(rows),
            "n_folds": len(folds),
            "demand_models": [x[0] for x in demand_factories],
            "note": "Replay E (full AH Jul-Sep) impossible — book only from 2026-09-03",
        },
        "elasticity": elast,
        "event_study": es,
        "under_vs_profit": {
            "corr_under_profit24h": uv["corr_under_profit24h"],
            "top10": uv["points"][:10],
            "highest_under": sorted(uv["points"], key=lambda x: -x["under"])[:10],
        },
        "matrix": matrix,
        "ablations": ablations,
        "bootstrap": boot,
        "sku_loo": sku,
        "ah_book": ah_info,
    }
    path = "/tmp/results_v10_robust.json"
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {path} in {time.time()-t0:.0f}s")
    print("DONE")


if __name__ == "__main__":
    main()
