#!/usr/bin/env python3
"""
v9 pricing research — sequential offline simulation + walk-forward search.

Limitations (honest):
- Demand model: E[sales|item, ratio_bucket, held_bucket, hour] from history.
  Counterfactual sales when price differs from logged are model-based, not exact.
- Buys modeled as refill toward share when sales occur + understock.
- Profit ≈ sales * avg_nacenka[item] (from sell events).
- Does NOT replay full AH book dynamics or FunTime set_min.

Goal: maximize cumulative trajectory profit out-of-sample.
"""
from __future__ import annotations

import itertools
import json
import math
import random
import sqlite3
import time
from collections import defaultdict
from dataclasses import dataclass, field
from typing import Callable, Dict, List, Optional, Tuple

DB = "/root/4narek-new/ml_data/pricing.db"
OUT = "/tmp/v9_research_results.json"
SEED = 42
random.seed(SEED)

# Top SKUs by cycle volume (stable enough for demand fit)
FOCUS_ITEMS = [
    "штаны-1.21",
    "ботинки-1.21",
    "шлем-1.21",
    "нагрудник-1.21",
    "фарм-1.21",
    "sword7-1.21",
    "pochti-megasword-1.21",
    "megasword-1.21",
    "кирка-эфф7-1.21",
    "кирка-бульд-маг-1.21",
]

# Policies with enough live data for observational comparison
COMPARE_POLICIES = [
    "stock_corridor_v1",
    "stock_corridor_v3",
    "stock_corridor_v4",
    "stock_corridor_v6",
    "stock_corridor_v8",
    "stock_corridor_v8b",
    "stock_corridor_v8n",
    "stock_corridor_v8x",
    "stock_corridor_v8ae",
    "stock_corridor_v8af",
]


def connect():
    con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    return con


# ── helpers ──────────────────────────────────────────────────────────

def hour_utc(ts: str) -> int:
    try:
        return int(ts[11:13])
    except Exception:
        return 12


def ratio_bucket(r: Optional[float]) -> str:
    if r is None:
        return "na"
    if r < 0.70:
        return "lt70"
    if r < 0.85:
        return "70_85"
    if r < 0.95:
        return "85_95"
    if r < 1.05:
        return "95_105"
    if r < 1.20:
        return "105_120"
    return "gt120"


def held_bucket(h: int, share: int) -> str:
    if share <= 0:
        share = 12
    f = h / share
    if h <= 0:
        return "empty"
    if f < 0.18:
        return "under"
    if f <= 0.28:
        return "band"
    if f < 0.50:
        return "over"
    return "dump"


# ── Phase 0: load panel ──────────────────────────────────────────────

def load_panel(con, items, t0="2026-07-15", t1="2026-09-15"):
    q = f"""
    SELECT ts, policy, item_id, action, held, sales, buys, try_sells,
           price_before, price_after, step, share, stock_load,
           COALESCE(profit_now,0) AS profit_now,
           COALESCE(fwd_profit_1,0) AS fp1,
           COALESCE(fwd_profit_2,0) AS fp2,
           COALESCE(fwd_profit_3,0) AS fp3,
           nacenka_before, cycle_minutes
    FROM capital_cycles
    WHERE ts>=? AND ts<? AND item_id IN ({','.join('?'*len(items))})
    ORDER BY item_id, ts
    """
    rows = [dict(r) for r in con.execute(q, [t0, t1, *items])]
    return rows


def attach_p10(con, rows, sample_every=1):
    """Attach 10m raw p10 (no ban). Expensive — only for FOCUS rows."""
    cache = {}
    out = []
    for i, r in enumerate(rows):
        if sample_every > 1 and i % sample_every:
            r = dict(r)
            r["p10"] = None
            r["ratio"] = None
            out.append(r)
            continue
        key = (r["item_id"], r["ts"][:16])  # minute bucket
        if key not in cache:
            prices = [
                p[0]
                for p in con.execute(
                    """SELECT price FROM ah_book_lots
                       WHERE item_id=? AND julianday(ts)>julianday(?)-10.0/1440
                         AND julianday(ts)<=julianday(?) AND price>0""",
                    (r["item_id"], r["ts"], r["ts"]),
                )
            ]
            if len(prices) >= 40:
                prices.sort()
                cache[key] = prices[len(prices) // 10]
            else:
                cache[key] = None
        r = dict(r)
        r["p10"] = cache[key]
        r["ratio"] = (r["price_before"] / r["p10"]) if r["p10"] else None
        out.append(r)
    return out


def attach_p10_fast(con, rows):
    """Faster: use rolling median of sell prices as market proxy when book thin/slow."""
    # Preload sell medians by item+day-hour from trade_events for FOCUS
    items = sorted({r["item_id"] for r in rows})
    sells = defaultdict(list)
    for it in items:
        for ts, price in con.execute(
            """SELECT ts, price FROM trade_events
               WHERE item_id=? AND event_type='sell' AND ts>='2026-07-15'""",
            (it,),
        ):
            sells[it].append((ts, price))
    for it in sells:
        sells[it].sort()

    ptr = {it: 0 for it in items}
    win = {it: [] for it in items}  # prices in last 2h

    out = []
    for r in rows:
        it = r["item_id"]
        ts = r["ts"]
        # advance pointer
        while ptr[it] < len(sells[it]) and sells[it][ptr[it]][0] <= ts:
            win[it].append(sells[it][ptr[it]][1])
            ptr[it] += 1
        # trim — keep last ~40 sells as market proxy
        if len(win[it]) > 80:
            win[it] = win[it][-80:]
        r = dict(r)
        if len(win[it]) >= 8:
            s = sorted(win[it])
            med = s[len(s) // 2]
            r["mkt"] = med
            r["ratio"] = r["price_before"] / med if med else None
        else:
            r["mkt"] = None
            r["ratio"] = None
        # also try book p10 only if we already have cached from light sample — skip heavy
        r["p10"] = r["mkt"]  # alias for policies that want "market"
        out.append(r)
    return out


def avg_nacenka(con, items):
    out = {}
    for it in items:
        row = con.execute(
            """SELECT AVG(nacenka) FROM trade_events
               WHERE item_id=? AND event_type='sell' AND nacenka>0""",
            (it,),
        ).fetchone()
        out[it] = int(row[0] or 200_000)
    return out


# ── Phase 1: observational policy comparison ─────────────────────────

def observational_compare(con):
    res = {}
    for pol in COMPARE_POLICIES:
        row = con.execute(
            """SELECT COUNT(*) n,
                      SUM(COALESCE(profit_now,0)) profit,
                      MIN(ts) t0, MAX(ts) t1,
                      SUM(CASE WHEN action LIKE '%price_up%' THEN 1 ELSE 0 END) ups,
                      SUM(CASE WHEN action LIKE '%price_down%' THEN 1 ELSE 0 END) downs,
                      AVG(COALESCE(fwd_profit_1,0)+COALESCE(fwd_profit_2,0)+COALESCE(fwd_profit_3,0)) fwd3,
                      AVG(held*1.0/NULLIF(share,0)) fill,
                      AVG(sales) asales
               FROM capital_cycles WHERE policy=?""",
            (pol,),
        ).fetchone()
        n, profit, t0, t1, ups, downs, fwd3, fill, asales = row
        # hours rough
        try:
            from datetime import datetime
            h = (datetime.fromisoformat(t1.replace("Z", "+00:00")) - datetime.fromisoformat(t0.replace("Z", "+00:00"))).total_seconds() / 3600
        except Exception:
            h = max(n / 6, 1)  # ~10m cycles
        res[pol] = {
            "n": n,
            "profit_m": round((profit or 0) / 1e6, 1),
            "profit_per_hour_m": round((profit or 0) / 1e6 / max(h, 0.1), 2),
            "hours": round(h, 1),
            "up_pct": round(100 * ups / n, 1) if n else 0,
            "down_pct": round(100 * downs / n, 1) if n else 0,
            "fwd3_m": round((fwd3 or 0) / 1e6, 2),
            "avg_fill": round(fill or 0, 3),
            "avg_sales": round(asales or 0, 2),
        }
    return res


# ── Phase 2: hypothesis tests ────────────────────────────────────────

def hyp_inventory_down(rows):
    """DOWN when held low vs high — does future profit differ?"""
    buckets = defaultdict(list)
    for r in rows:
        share = r["share"] or 12
        hb = held_bucket(r["held"], share)
        if "price_down" not in r["action"]:
            continue
        fwd = r["fp1"] + r["fp2"] + r["fp3"]
        buckets[hb].append(fwd)
    out = {}
    for k, v in buckets.items():
        out[k] = {
            "n": len(v),
            "mean_fwd3_m": round(sum(v) / len(v) / 1e6, 2) if v else 0,
            "median_fwd3_m": round(sorted(v)[len(v) // 2] / 1e6, 2) if v else 0,
        }
    return out


def hyp_empty_streak_up(rows):
    """After empty streak of length k, is UP better than HOLD for next 3-cycle profit?"""
    by_item = defaultdict(list)
    for r in rows:
        by_item[r["item_id"]].append(r)

    results = {}
    for k in [2, 3, 4, 6, 8]:
        up_fwd, hold_fwd = [], []
        for it, seq in by_item.items():
            streak = 0
            for i, r in enumerate(seq):
                if r["held"] == 0 and r["sales"] == 0 and r["buys"] == 0:
                    streak += 1
                else:
                    streak = 0
                if streak < k or i + 1 >= len(seq):
                    continue
                nxt = seq[i]  # decision at this cycle
                fwd = nxt["fp1"] + nxt["fp2"] + nxt["fp3"]
                if "price_up" in nxt["action"]:
                    up_fwd.append(fwd)
                elif "price_down" not in nxt["action"]:
                    hold_fwd.append(fwd)
        results[f"streak>={k}"] = {
            "up_n": len(up_fwd),
            "hold_n": len(hold_fwd),
            "up_fwd3_m": round(sum(up_fwd) / len(up_fwd) / 1e6, 2) if up_fwd else None,
            "hold_fwd3_m": round(sum(hold_fwd) / len(hold_fwd) / 1e6, 2) if hold_fwd else None,
            "delta_up_minus_hold_m": (
                round((sum(up_fwd) / len(up_fwd) - sum(hold_fwd) / len(hold_fwd)) / 1e6, 2)
                if up_fwd and hold_fwd
                else None
            ),
        }
    return results


def hyp_empty_streak_with_ratio(rows):
    """Empty streak × underprice (ratio<0.85) vs near/over."""
    by_item = defaultdict(list)
    for r in rows:
        by_item[r["item_id"]].append(r)
    out = defaultdict(list)
    for it, seq in by_item.items():
        streak = 0
        for r in seq:
            if r["held"] == 0 and r["sales"] == 0 and r["buys"] == 0:
                streak += 1
            else:
                streak = 0
            if streak < 3:
                continue
            rb = ratio_bucket(r.get("ratio"))
            out[rb].append(r["fp1"] + r["fp2"] + r["fp3"])
    return {
        k: {
            "n": len(v),
            "mean_fwd3_m": round(sum(v) / len(v) / 1e6, 2) if v else 0,
        }
        for k, v in out.items()
    }


# ── Phase 3: demand model ────────────────────────────────────────────

@dataclass
class DemandModel:
    # key: (item, ratio_bucket, held_bucket, hour_bucket) -> mean sales
    rates: Dict[Tuple, float] = field(default_factory=dict)
    global_by_ratio: Dict[str, float] = field(default_factory=dict)
    nac: Dict[str, int] = field(default_factory=dict)
    default_sales: float = 1.5

    def fit(self, rows, nac):
        self.nac = nac
        buckets = defaultdict(list)
        by_r = defaultdict(list)
        for r in rows:
            rb = ratio_bucket(r.get("ratio"))
            hb = held_bucket(r["held"], r["share"] or 12)
            hour = hour_utc(r["ts"])
            hbkt = "night" if 0 <= hour < 6 else "day"  # UTC ~ MSK-3; 0-6 UTC ≈ 3-9 MSK
            key = (r["item_id"], rb, hb, hbkt)
            buckets[key].append(r["sales"])
            by_r[rb].append(r["sales"])
        for k, v in buckets.items():
            self.rates[k] = sum(v) / len(v)
        for k, v in by_r.items():
            self.global_by_ratio[k] = sum(v) / len(v)
        self.default_sales = sum(r["sales"] for r in rows) / max(len(rows), 1)

    def expected_sales(self, item, price, mkt, held, share, ts) -> float:
        ratio = price / mkt if mkt else None
        rb = ratio_bucket(ratio)
        hb = held_bucket(held, share or 12)
        hour = hour_utc(ts)
        hbkt = "night" if 0 <= hour < 6 else "day"
        key = (item, rb, hb, hbkt)
        if key in self.rates:
            return max(0.0, self.rates[key])
        # backoff
        for alt in [
            (item, rb, hb, "day"),
            (item, rb, "band", hbkt),
            (item, rb, "under", hbkt),
        ]:
            if alt in self.rates:
                return max(0.0, self.rates[alt])
        return max(0.0, self.global_by_ratio.get(rb, self.default_sales))

    def expected_buys(self, sales, held, share, logged_buys=None, empty_idle=False) -> float:
        # Refill when understocked; less when over.
        # Critical: do NOT invent restock on empty_idle — that kills empty-streak signal.
        share = share or 12
        if empty_idle:
            if logged_buys is not None:
                return float(logged_buys)
            return 0.0
        target = int(0.22 * share)
        gap = max(0, target - held)
        if held <= 0:
            base = min(sales + 0.5, share * 0.15)
            if logged_buys is not None:
                return 0.5 * base + 0.5 * logged_buys
            return base
        if held > int(0.35 * share):
            return max(0, sales * 0.4)
        return min(sales + gap * 0.3, share * 0.2)


# ── Policies ─────────────────────────────────────────────────────────

@dataclass
class State:
    price: int
    held: int
    empty_streak: int = 0
    up_cd: int = 0
    down_cd: int = 0
    up_streak: int = 0


Decision = Tuple[str, int]  # action, new_price


def step_band(share, lo=0.18, hi=0.25, soft=0.28, over=0.35, dump=0.50):
    share = max(share, 1)
    return (
        max(1, int(lo * share)),
        max(2, int(hi * share)),
        max(3, int(soft * share)),
        max(4, int(over * share)),
        max(5, int(dump * share)),
    )


PolicyFn = Callable[[State, dict, DemandModel], Decision]


def policy_hold(st, obs, dm) -> Decision:
    return "HOLD", st.price


def policy_classic_inventory(st, obs, dm, lo_frac=0.18, hi_frac=0.25, min_sales_up=2) -> Decision:
    """Old economic core: DOWN only if excess; UP if understock + sales."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share, lo_frac, hi_frac)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    if st.up_cd > 0:
        # still allow DOWN
        pass
    if st.held >= dump:
        return "DOWN", max(step, st.price - 2 * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - 2 * step)
    if st.held > hi and st.down_cd == 0:
        return "DOWN", max(step, st.price - step)
    if st.held < lo and sales >= min_sales_up and sales > buys and st.up_cd == 0 and st.held > 0:
        return "UP", st.price + step
    return "HOLD", st.price


def policy_inv_no_down_under(st, obs, dm, **kw) -> Decision:
    """Strict: never DOWN if held < hi."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    if st.held <= hi:
        # only UP/HOLD
        if st.held < lo and sales >= 2 and sales > buys and st.up_cd == 0 and st.held > 0:
            return "UP", st.price + step
        return "HOLD", st.price
    if st.held >= dump:
        return "DOWN", max(step, st.price - 2 * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - 2 * step)
    return "DOWN", max(step, st.price - step)


def policy_empty_streak_up(st, obs, dm, streak_need=3, gap=0.85, stop=0.90, use_mkt=True) -> Decision:
    """Inventory core + empty streak UP when under market."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    mkt = obs.get("mkt") or obs.get("p10")

    # DOWN only on excess
    if st.held >= dump:
        return "DOWN", max(step, st.price - 2 * step)
    if st.held >= over:
        return "DOWN", max(step, st.price - 2 * step)
    if st.held > hi:
        return "DOWN", max(step, st.price - step)

    # empty streak catchup (streak already encodes sustained empty)
    if (
        st.held == 0
        and st.empty_streak >= streak_need
        and st.up_cd == 0
        and mkt
        and st.price / mkt < gap
        and st.price + step <= mkt * stop
    ):
        return "UP", st.price + step

    # demand UP when understocked
    if st.held < lo and st.held > 0 and sales >= 2 and sales > buys and st.up_cd == 0:
        return "UP", st.price + step
    return "HOLD", st.price


def policy_inv_p10_guard(st, obs, dm, streak_need=3, gap=0.85) -> Decision:
    """Inventory + empty streak + no hard DOWN when already underpriced."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    mkt = obs.get("mkt") or obs.get("p10")
    ratio = st.price / mkt if mkt else None

    # DOWN blocked if underpriced
    if st.held > hi:
        if ratio is not None and ratio < 0.90:
            return "HOLD", st.price  # inventory excess but price already low
        if st.held >= dump:
            return "DOWN", max(step, st.price - 2 * step)
        if st.held >= over:
            return "DOWN", max(step, st.price - 2 * step)
        return "DOWN", max(step, st.price - step)

    if (
        st.held == 0
        and st.empty_streak >= streak_need
        and st.up_cd == 0
        and ratio is not None
        and ratio < gap
        and st.price + step <= mkt
    ):
        return "UP", st.price + step

    if st.held < lo and st.held > 0 and sales >= 2 and sales > buys and st.up_cd == 0:
        # don't UP if already above market
        if ratio is not None and ratio >= 1.05:
            return "HOLD", st.price
        return "UP", st.price + step
    return "HOLD", st.price


def make_policy(name, **params) -> PolicyFn:
    if name == "hold":
        return policy_hold
    if name == "classic_inv":
        return lambda st, obs, dm: policy_classic_inventory(st, obs, dm, **params)
    if name == "inv_no_down_under":
        return lambda st, obs, dm: policy_inv_no_down_under(st, obs, dm, **params)
    if name == "empty_streak":
        return lambda st, obs, dm: policy_empty_streak_up(st, obs, dm, **params)
    if name == "inv_p10_guard":
        return lambda st, obs, dm: policy_inv_p10_guard(st, obs, dm, **params)
    if name == "v9_core":
        return lambda st, obs, dm: policy_v9_core(st, obs, dm, **params)
    if name == "v8af_approx":
        return lambda st, obs, dm: policy_v8af_approx(st, obs, dm, **params)
    raise ValueError(name)


# ── Sequential simulator ─────────────────────────────────────────────

def simulate_item(seq, policy: PolicyFn, dm: DemandModel, up_cd_cycles=2):
    if not seq:
        return {"profit": 0, "sales": 0, "ups": 0, "downs": 0, "cycles": 0, "under_frac": 0, "over_frac": 0}
    st = State(price=seq[0]["price_before"], held=seq[0]["held"])
    profit = 0.0
    total_sales = 0.0
    ups = downs = 0
    under_n = over_n = 0
    nac = dm.nac.get(seq[0]["item_id"], 200_000)

    for obs in seq:
        if st.up_cd > 0:
            st.up_cd -= 1
        if st.down_cd > 0:
            st.down_cd -= 1

        # Decision uses streak accumulated from previous cycles.
        # For activity gates inside policy, blend logged flow with model at current sim price.
        mkt = obs.get("mkt") or obs.get("p10")
        pre_sales = dm.expected_sales(obs["item_id"], st.price, mkt, st.held, obs["share"], obs["ts"])
        empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0
        if abs(st.price - obs["price_before"]) <= (obs["step"] or 100_000):
            gate_sales = 0.7 * obs["sales"] + 0.3 * pre_sales
            gate_buys = dm.expected_buys(
                gate_sales, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle
            )
        else:
            gate_sales = pre_sales if not empty_idle else 0.0
            gate_buys = dm.expected_buys(
                gate_sales, st.held, obs["share"], logged_buys=obs["buys"], empty_idle=empty_idle
            )
        obs_gate = {**obs, "sales": gate_sales, "buys": gate_buys}

        action, new_price = policy(st, obs_gate, dm)

        exp_sales = dm.expected_sales(obs["item_id"], new_price, mkt, st.held, obs["share"], obs["ts"])
        if empty_idle and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
            sales = float(obs["sales"])
            buys = float(obs["buys"])
        elif abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000):
            sales = 0.7 * obs["sales"] + 0.3 * exp_sales
            buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
        else:
            sales = exp_sales
            buys = dm.expected_buys(sales, st.held, obs["share"], logged_buys=obs["buys"])
        sales = max(0.0, sales)

        if action == "UP":
            ups += 1
            st.up_cd = up_cd_cycles
            st.up_streak += 1
        elif action == "DOWN":
            downs += 1
            st.down_cd = 1
            st.up_streak = 0
        else:
            st.up_streak = 0

        st.price = new_price
        st.held = max(0, int(round(st.held - sales + buys)))
        share = obs["share"] or 12
        st.held = min(st.held, int(share * 1.5))

        profit += sales * nac
        total_sales += sales

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

    n = len(seq)
    return {
        "profit": profit,
        "sales": total_sales,
        "ups": ups,
        "downs": downs,
        "cycles": n,
        "under_frac": under_n / n if n else 0,
        "over_frac": over_n / n if n else 0,
        "profit_per_cycle": profit / n if n else 0,
    }


def simulate_panel(rows_by_item, policy, dm, up_cd=2):
    tot = {"profit": 0, "sales": 0, "ups": 0, "downs": 0, "cycles": 0, "under_frac": 0, "over_frac": 0}
    per = {}
    for it, seq in rows_by_item.items():
        r = simulate_item(seq, policy, dm, up_cd)
        per[it] = r
        for k in ("profit", "sales", "ups", "downs", "cycles"):
            tot[k] += r[k]
        tot["under_frac"] += r["under_frac"] * r["cycles"]
        tot["over_frac"] += r["over_frac"] * r["cycles"]
    if tot["cycles"]:
        tot["under_frac"] /= tot["cycles"]
        tot["over_frac"] /= tot["cycles"]
        tot["profit_m"] = tot["profit"] / 1e6
        tot["profit_per_cycle_m"] = tot["profit"] / tot["cycles"] / 1e6
    return tot, per


def split_by_item(rows):
    d = defaultdict(list)
    for r in rows:
        d[r["item_id"]].append(r)
    return d


def time_split(rows, frac=0.6):
    if not rows:
        return [], []
    # global time split
    ts = sorted(set(r["ts"] for r in rows))
    cut = ts[int(len(ts) * frac)]
    train = [r for r in rows if r["ts"] < cut]
    test = [r for r in rows if r["ts"] >= cut]
    return train, test, cut


# ── Mass search ──────────────────────────────────────────────────────

def mass_search(train, test, dm):
    candidates = []
    for min_sales in [1, 2, 3, 4, 5]:
        for lo, hi in [(0.15, 0.25), (0.18, 0.25), (0.18, 0.28), (0.20, 0.30)]:
            candidates.append(("classic_inv", {"min_sales_up": min_sales, "lo_frac": lo, "hi_frac": hi}))
    candidates.append(("inv_no_down_under", {}))
    candidates.append(("hold", {}))
    for streak in [2, 3, 4, 5, 6, 8]:
        for gap in [0.70, 0.75, 0.80, 0.85, 0.90]:
            candidates.append(("empty_streak", {"streak_need": streak, "gap": gap}))
            candidates.append(("inv_p10_guard", {"streak_need": streak, "gap": gap}))
            candidates.append(
                ("inv_p10_guard", {"streak_need": streak, "gap": gap})
            )
    # v9 combo: stricter demand + guard
    for streak in [2, 3, 4]:
        for gap in [0.80, 0.85]:
            for ms in [3, 4]:
                candidates.append(
                    (
                        "v9_core",
                        {"streak_need": streak, "gap": gap, "min_sales_up": ms, "down_block_ratio": 0.90},
                    )
                )

    seen = set()
    uniq = []
    for name, params in candidates:
        key = (name, tuple(sorted(params.items())))
        if key in seen:
            continue
        seen.add(key)
        uniq.append((name, params))
    print(f"  candidates: {len(uniq)}")

    train_by = split_by_item(train)
    test_by = split_by_item(test)
    results = []
    for name, params in uniq:
        pol = make_policy(name, **params)
        tr, _ = simulate_panel(train_by, pol, dm)
        te, _ = simulate_panel(test_by, pol, dm)
        results.append(
            {
                "name": name,
                "params": params,
                "train_profit_m": round(tr.get("profit_m", 0), 1),
                "test_profit_m": round(te.get("profit_m", 0), 1),
                "test_ppc_m": round(te.get("profit_per_cycle_m", 0), 3),
                "test_sales": round(te["sales"], 0),
                "test_ups": te["ups"],
                "test_downs": te["downs"],
                "test_under": round(te["under_frac"], 3),
                "test_over": round(te["over_frac"], 3),
                "train_cycles": tr["cycles"],
                "test_cycles": te["cycles"],
            }
        )
    results.sort(key=lambda x: x["test_profit_m"], reverse=True)
    return results


def policy_v9_core(st, obs, dm, streak_need=2, gap=1.00, min_sales_up=3, down_block_ratio=0.90) -> Decision:
    """Production stock_corridor_v9 (matches pricing_v9.go). gap=1.0 = p10 safety; buy-stop via empty streak."""
    share = obs["share"] or 12
    lo, hi, soft, over, dump = step_band(share, 0.18, 0.25)
    step = obs["step"] or 100_000
    sales, buys = obs["sales"], obs["buys"]
    mkt = obs.get("mkt") or obs.get("p10")
    ratio = st.price / mkt if mkt else None
    night = obs.get("night", False)
    min_sales = 4 if night else min_sales_up

    if st.held > hi:
        if ratio is not None and ratio < down_block_ratio:
            return "HOLD", st.price
        if st.held >= dump:
            return "DOWN", max(step, st.price - 2 * step)
        if st.held >= over:
            return "DOWN", max(step, st.price - 2 * step)
        return "DOWN", max(step, st.price - step)

    if st.held < lo and st.held > 0 and sales >= min_sales and sales > buys and st.up_cd == 0:
        if ratio is not None and ratio >= 1.05:
            return "HOLD", st.price
        return "UP", st.price + step

    if (
        st.held == 0
        and st.empty_streak >= streak_need
        and st.up_cd == 0
        and ratio is not None
        and ratio < gap
        and st.price + step <= mkt
    ):
        return "UP", st.price + step
    return "HOLD", st.price


def policy_v8af_approx(st, obs, dm, **kw) -> Decision:
    """Coarse v8af stand-in for comparison: inventory + no empty catchup, no underprice DOWN veto.
    Not a full port — observational baseline for relative ranking only.
    """
    return policy_classic_inventory(st, obs, dm, min_sales_up=3, lo_frac=0.18, hi_frac=0.25)


def ablation(train, test, dm, base_params):
    """Ablation around v9_core / inv_p10_guard."""
    variants = [
        ("A_inv_only", "classic_inv", {"min_sales_up": 4}),
        ("B_inv_no_down_under", "inv_no_down_under", {}),
        ("C_inv_empty3", "empty_streak", {"streak_need": 3, "gap": 0.85}),
        ("D_inv_p10guard", "inv_p10_guard", {"streak_need": 2, "gap": 0.80}),
        ("E_v9_core", "v9_core", base_params if base_params else {"streak_need": 2, "gap": 1.00, "min_sales_up": 3, "down_block_ratio": 0.90}),
        ("F_hold", "hold", {}),
    ]
    train_by, test_by = split_by_item(train), split_by_item(test)
    out = []
    for label, name, params in variants:
        pol = make_policy(name, **params)
        te, _ = simulate_panel(test_by, pol, dm)
        out.append(
            {
                "label": label,
                "name": name,
                "params": params,
                "test_profit_m": round(te.get("profit_m", 0), 1),
                "test_ppc_m": round(te.get("profit_per_cycle_m", 0), 3),
                "under": round(te["under_frac"], 3),
                "over": round(te["over_frac"], 3),
                "ups": te["ups"],
                "downs": te["downs"],
            }
        )
    return out


def walk_forward(rows, dm, policy_name, params, folds=3):
    ts = sorted(set(r["ts"] for r in rows))
    if len(ts) < 100:
        return []
    fold_size = len(ts) // (folds + 1)
    out = []
    for f in range(folds):
        # train: [0, (f+1)*fold_size), test: [(f+1)*fold_size, (f+2)*fold_size)
        t0 = ts[0]
        t_cut = ts[(f + 1) * fold_size]
        t1 = ts[min((f + 2) * fold_size, len(ts) - 1)]
        train = [r for r in rows if r["ts"] < t_cut]
        test = [r for r in rows if t_cut <= r["ts"] < t1]
        # refit demand on train
        dm_f = DemandModel()
        dm_f.fit(train, dm.nac)
        pol = make_policy(policy_name, **params)
        te, _ = simulate_panel(split_by_item(test), pol, dm_f)
        # baseline classic
        pol_b = make_policy("classic_inv", min_sales_up=2)
        te_b, _ = simulate_panel(split_by_item(test), pol_b, dm_f)
        out.append(
            {
                "fold": f,
                "cut": t_cut,
                "end": t1,
                "test_cycles": te["cycles"],
                "cand_profit_m": round(te.get("profit_m", 0), 1),
                "base_profit_m": round(te_b.get("profit_m", 0), 1),
                "delta_m": round(te.get("profit_m", 0) - te_b.get("profit_m", 0), 1),
            }
        )
    return out


# ── Logged baseline (replay historical actions as "policy") ──────────

def replay_logged_profit(rows):
    return {
        "profit_m": round(sum(r["profit_now"] for r in rows) / 1e6, 1),
        "cycles": len(rows),
        "ppc_m": round(sum(r["profit_now"] for r in rows) / max(len(rows), 1) / 1e6, 3),
        "ups": sum(1 for r in rows if "price_up" in r["action"]),
        "downs": sum(1 for r in rows if "price_down" in r["action"]),
    }


def main():
    t_start = time.time()
    print("=== v9 research start ===")
    con = connect()

    print("\n[1] Observational policy comparison (live logs)")
    obs = observational_compare(con)
    for k, v in sorted(obs.items(), key=lambda x: -x[1]["profit_per_hour_m"]):
        print(f"  {k:28} p/h={v['profit_per_hour_m']:7.2f}M  profit={v['profit_m']:8.1f}M  "
              f"h={v['hours']:6.1f} up%={v['up_pct']:5.1f} fwd3={v['fwd3_m']:5.2f} fill={v['avg_fill']}")

    print("\n[2] Load FOCUS panel + market proxy")
    rows = load_panel(con, FOCUS_ITEMS)
    print(f"  raw cycles: {len(rows)}")
    rows = attach_p10_fast(con, rows)
    with_ratio = sum(1 for r in rows if r.get("ratio") is not None)
    print(f"  with market ratio: {with_ratio}/{len(rows)}")

    nac = avg_nacenka(con, FOCUS_ITEMS)
    print(f"  avg nacenka: {nac}")

    print("\n[3] Hypothesis: inventory DOWN predictive value")
    hyp_inv = hyp_inventory_down(rows)
    print(json.dumps(hyp_inv, ensure_ascii=False, indent=2))

    print("\n[4] Hypothesis: empty streak → UP vs HOLD fwd")
    hyp_empty = hyp_empty_streak_up(rows)
    print(json.dumps(hyp_empty, ensure_ascii=False, indent=2))

    print("\n[5] Empty streak × ratio bucket")
    hyp_er = hyp_empty_streak_with_ratio(rows)
    print(json.dumps(hyp_er, ensure_ascii=False, indent=2))

    print("\n[6] Fit demand + walk-forward mass search")
    train, test, cut = time_split(rows, 0.65)
    print(f"  cut={cut} train={len(train)} test={len(test)}")
    dm = DemandModel()
    dm.fit(train, nac)

    # logged baselines on test
    print("\n[6b] Logged profit on TEST window (observational)")
    print("  all logged:", replay_logged_profit(test))
    for pol in ["stock_corridor_v8", "stock_corridor_v8b", "stock_corridor_v6", "stock_corridor_v4"]:
        sub = [r for r in test if r["policy"] == pol]
        if sub:
            print(f"  {pol}:", replay_logged_profit(sub))

    print("\n[7] Mass candidate search (train fit → test eval)")
    results = mass_search(train, test, dm)
    print("  TOP 15 by test profit:")
    for r in results[:15]:
        print(f"    {r['name']:20} {r['params']} test={r['test_profit_m']}M "
              f"ppc={r['test_ppc_m']} under={r['test_under']} up={r['test_ups']} dn={r['test_downs']}")

    best = results[0]
    print("\n[8] Ablation")
    base_params = {"streak_need": 2, "gap": 1.00, "min_sales_up": 3, "down_block_ratio": 0.90}
    for r in results:
        if r["name"] == "v9_core":
            base_params = r["params"]
            break
    abl = ablation(train, test, dm, base_params)
    print(json.dumps(abl, ensure_ascii=False, indent=2))

    print("\n[9] Walk-forward best + v9_core + classic")
    wf = walk_forward(rows, dm, best["name"], best["params"], folds=3)
    print("best:", json.dumps(wf, ensure_ascii=False, indent=2))
    wf2 = walk_forward(rows, dm, "v9_core", base_params, folds=3)
    print("v9_core:", json.dumps(wf2, ensure_ascii=False, indent=2))
    wf3 = walk_forward(rows, dm, "classic_inv", {"min_sales_up": 4}, folds=3)
    print("classic4:", json.dumps(wf3, ensure_ascii=False, indent=2))
    wf4 = walk_forward(rows, dm, "inv_p10_guard", {"streak_need": 2, "gap": 0.80}, folds=3)
    print("p10guard:", json.dumps(wf4, ensure_ascii=False, indent=2))

    payload = {
        "elapsed_sec": round(time.time() - t_start, 1),
        "observational": obs,
        "hyp_inventory_down": hyp_inv,
        "hyp_empty_streak": hyp_empty,
        "hyp_empty_ratio": hyp_er,
        "cut": cut,
        "mass_top": results[:25],
        "best": best,
        "ablation": abl,
        "walk_forward_best": wf,
        "walk_forward_v9": wf2,
        "walk_forward_classic4": wf3,
        "walk_forward_p10guard": wf4,
        "simulator_limits": [
            "demand from historical buckets (item,ratio,held,day/night)",
            "buys are synthetic refill toward share",
            "market proxy = rolling sell median (not ban-filtered AH p10)",
            "blend with logged sales when price within 1 step of history",
            "does not model FunTime set_min / multi-bot share shifts",
        ],
    }
    with open(OUT, "w") as f:
        json.dump(payload, f, ensure_ascii=False, indent=2)
    print(f"\nWrote {OUT}")
    print("=== DONE ===")


if __name__ == "__main__":
    main()
