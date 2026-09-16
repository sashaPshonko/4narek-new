#!/usr/bin/env python3
"""
Higher-fidelity pricing simulator (research only).

Upgrades vs search_super / sim_v9 DemandModel:
1. Market = AH book p10 (10m, n≥40) + book depth bucket.
2. Demand fit on (item, ratio, depth, held, tod) using AH ratios.
3. Counterfactual: pure model when |price−logged| > 1 step (no 70% logged blend cheat).
4. sales capped by held (can't sell air).
5. Inventory for policies: on_ah=min(held,share), inv=max(0,held−share).
6. Elasticity prior: observational means (peak near-market) as backoff, not as free lunch under.

Does NOT change production Go.
"""
from __future__ import annotations

import sqlite3
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Any, Dict, List, Optional, Tuple

import sim_v9 as S
from search_v10 import Chrom, decide, chrom_v9

BOOK_T0 = "2026-09-03"

# Observational mean sales by ratio (v10 robust, Jul–Sep panel) — peak at 95–105
_ELAST_MEAN = {
    "lt70": 0.491,
    "70_85": 2.581,
    "85_95": 2.949,
    "95_105": 3.439,
    "105_120": 3.259,
    "gt120": 1.653,
    "na": 2.0,
}
_ELAST_PEAK = _ELAST_MEAN["95_105"]
ELAST_REL = {k: v / _ELAST_PEAK for k, v in _ELAST_MEAN.items()}


def depth_bucket(n: Optional[int]) -> str:
    if n is None or n < 40:
        return "thin"
    if n < 200:
        return "ok"
    return "thick"


def attach_ah_market(con: sqlite3.Connection, rows: List[dict], window_min: int = 10, min_n: int = 40) -> List[dict]:
    """Attach p10 + book_n from ah_book_lots minute bins."""
    if not rows:
        return rows
    items = sorted({r["item_id"] for r in rows})
    t_hi = max(r["ts"] for r in rows)
    bins: Dict[str, Dict[str, List[int]]] = {it: defaultdict(list) for it in items}
    for it in items:
        for ts, price in con.execute(
            """SELECT ts, price FROM ah_book_lots
               WHERE item_id=? AND price>0 AND ts>=? AND ts<=?
               ORDER BY ts""",
            (it, BOOK_T0, t_hi),
        ):
            bins[it][ts[:16]].append(int(price))
    for it in items:
        for ps in bins[it].values():
            ps.sort()

    def stats_at(it: str, ts: str) -> Tuple[Optional[int], int]:
        try:
            end = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        except Exception:
            return None, 0
        prices: List[int] = []
        for dmin in range(window_min):
            t = end - timedelta(minutes=dmin)
            prices.extend(bins[it].get(t.strftime("%Y-%m-%dT%H:%M"), ()))
        n = len(prices)
        if n < min_n:
            return None, n
        prices.sort()
        return prices[n // 10], n

    cache: Dict[Tuple[str, str], Tuple[Optional[int], int]] = {}
    out = []
    for r in rows:
        key = (r["item_id"], r["ts"][:16])
        if key not in cache:
            cache[key] = stats_at(r["item_id"], r["ts"])
        p10, n = cache[key]
        rr = dict(r)
        rr["book_n"] = n
        rr["depth"] = depth_bucket(n)
        if p10:
            rr["p10"] = p10
            rr["mkt"] = p10
            rr["ratio"] = r["price_before"] / p10 if r["price_before"] else None
            rr["mkt_source"] = "ah_p10"
        else:
            rr["p10"] = None
            rr["mkt"] = None
            rr["ratio"] = None
            rr["mkt_source"] = "missing"
        out.append(rr)

    # sell-median fallback for missing book
    missing = [r for r in out if r["mkt"] is None]
    if missing:
        filled = S.attach_p10_fast(con, missing)
        by = {(r["item_id"], r["ts"]): r for r in filled}
        for r in out:
            if r["mkt"] is None:
                fb = by.get((r["item_id"], r["ts"]))
                if fb and fb.get("mkt"):
                    r["mkt"] = fb["mkt"]
                    r["p10"] = fb["mkt"]
                    r["ratio"] = r["price_before"] / r["mkt"] if r["mkt"] else None
                    r["mkt_source"] = "sell_median_fallback"
                    r["depth"] = "thin"
    return out


@dataclass
class BookDemand:
    """Demand with AH ratio + book depth + held + TOD."""

    rates: Dict[Tuple, float] = field(default_factory=dict)
    by_item_ratio: Dict[Tuple, float] = field(default_factory=dict)
    by_ratio_depth: Dict[Tuple, float] = field(default_factory=dict)
    nac: Dict[str, int] = field(default_factory=dict)
    default_sales: float = 1.5

    def fit(self, rows: List[dict], nac: Dict[str, int]) -> None:
        self.nac = nac
        b_full: Dict[Tuple, List[float]] = defaultdict(list)
        b_ir: Dict[Tuple, List[float]] = defaultdict(list)
        b_rd: Dict[Tuple, List[float]] = defaultdict(list)
        for r in rows:
            # Prefer AH ratio; skip sell-median for fit when possible
            if r.get("mkt_source") != "ah_p10" and r.get("ratio") is None:
                continue
            rb = S.ratio_bucket(r.get("ratio"))
            hb = S.held_bucket(r["held"], r["share"] or 12)
            tod = "night" if 0 <= S.hour_utc(r["ts"]) < 6 else "day"
            depth = r.get("depth") or depth_bucket(r.get("book_n"))
            sales = float(r["sales"])
            b_full[(r["item_id"], rb, depth, hb, tod)].append(sales)
            b_ir[(r["item_id"], rb)].append(sales)
            b_rd[(rb, depth)].append(sales)
        self.rates = {k: sum(v) / len(v) for k, v in b_full.items() if len(v) >= 3}
        self.by_item_ratio = {k: sum(v) / len(v) for k, v in b_ir.items() if len(v) >= 5}
        self.by_ratio_depth = {k: sum(v) / len(v) for k, v in b_rd.items() if len(v) >= 8}
        self.default_sales = sum(float(r["sales"]) for r in rows) / max(len(rows), 1)

    def expected_sales(self, item, price, mkt, held, share, ts, depth: str = "ok") -> float:
        if held <= 0:
            return 0.0
        ratio = price / mkt if mkt else None
        rb = S.ratio_bucket(ratio)
        hb = S.held_bucket(held, share or 12)
        tod = "night" if 0 <= S.hour_utc(ts) < 6 else "day"
        key = (item, rb, depth, hb, tod)
        if key in self.rates:
            base = self.rates[key]
        elif (item, rb) in self.by_item_ratio:
            base = self.by_item_ratio[(item, rb)]
        elif (rb, depth) in self.by_ratio_depth:
            base = self.by_ratio_depth[(rb, depth)]
        else:
            # elasticity prior × global default
            base = self.default_sales * ELAST_REL.get(rb, 0.7)
        # Cap by inventory
        return max(0.0, min(float(base), float(held)))

    def expected_buys(self, sales, held, share, logged_buys=None, empty_idle=False) -> float:
        share = share or 12
        if empty_idle:
            return float(logged_buys) if logged_buys is not None else 0.0
        target = int(0.22 * share)
        gap = max(0, target - held)
        if held <= 0:
            base = min(sales + 0.5, share * 0.15)
            if logged_buys is not None:
                return 0.5 * base + 0.5 * float(logged_buys)
            return base
        if held > int(0.35 * share):
            return max(0.0, sales * 0.4)
        return min(sales + gap * 0.3, share * 0.2)


def _blend_weight(sim_price: int, logged_price: int, step: int) -> float:
    """1.0 = pure model; 0.0 = pure logged. Near logged → partial blend."""
    step = max(step or 100_000, 1)
    if abs(sim_price - logged_price) <= step:
        return 0.45  # model weight when near (was 0.30 model / 0.70 logged — too sticky)
    return 1.0  # full counterfactual


def simulate_fidelity(rows_by_item, ch: Chrom, dm: BookDemand) -> Dict[str, Any]:
    day_profit: Dict[str, float] = defaultdict(float)
    tot_profit = 0.0
    ups = downs = holds = cycles = 0
    under_n = over_n = deep_under_n = 0
    per_sku: Dict[str, float] = defaultdict(float)
    book_ok = 0
    pure_cf = 0

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

            step = obs["step"] or 100_000
            empty_idle = st.held == 0 and obs["sales"] == 0 and obs["buys"] == 0

            def sales_at(price: int) -> float:
                w = _blend_weight(price, obs["price_before"], step)
                model = dm.expected_sales(it, price, mkt, st.held, share, obs["ts"], depth=depth)
                if empty_idle and w < 1.0:
                    return float(obs["sales"])  # still empty at logged price
                if empty_idle and w >= 1.0:
                    return 0.0  # empty + moved price: no phantom sales
                if w >= 1.0:
                    return model
                return (1.0 - w) * float(obs["sales"]) + w * model

            # pre-decision ghost sales for policy signals
            gs = sales_at(st.price)
            if _blend_weight(st.price, obs["price_before"], step) >= 1.0:
                pure_cf += 1
            gb = dm.expected_buys(gs, st.held, share, logged_buys=obs["buys"], empty_idle=empty_idle)
            obs_g = {**obs, "sales": gs, "buys": gb}

            action, new_price = decide(st, obs_g, ch)

            sales = sales_at(new_price)
            if _blend_weight(new_price, obs["price_before"], step) >= 1.0:
                pure_cf += 1
            buys = dm.expected_buys(
                sales, st.held, share,
                logged_buys=obs["buys"],
                empty_idle=empty_idle and abs(new_price - obs["price_before"]) <= step,
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

            st.price = new_price
            st.held = max(0, int(round(st.held - sales + buys)))
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
        "n_days": len(days),
        "ups": ups,
        "downs": downs,
        "holds": holds,
        "cycles": cycles,
        "under": under_n / cycles if cycles else 0,
        "deep_under": deep_under_n / cycles if cycles else 0,
        "over": over_n / cycles if cycles else 0,
        "book_ok_frac": book_ok / cycles if cycles else 0,
        "pure_cf_frac": pure_cf / max(cycles * 2, 1),
        "per_sku_m": {k: round(v / 1e6, 1) for k, v in sorted(per_sku.items(), key=lambda x: -x[1])},
        "days": days,
    }


def constrained_ok(m: dict, v9: dict, under_slack: float = 0.05) -> bool:
    if m["n_days"] < 2:
        return False
    if m["under"] > v9["under"] + under_slack:
        return False
    if m["deep_under"] > max(0.12, v9.get("deep_under", 0) + 0.05):
        return False
    return True


def walk_forward_folds(rows: List[dict], n_folds: int = 2) -> List[dict]:
    days = sorted({r["ts"][:10] for r in rows})
    n = len(days)
    val_len, oos_len = 3, max(3, n // (n_folds + 2))
    folds = []
    for i in range(n_folds):
        oos_end = n - i * max(1, oos_len - 1)
        oos_start = oos_end - oos_len
        val_end = oos_start
        val_start = val_end - val_len
        if val_start < 4:
            continue
        train_fit = set(days[:val_start])
        val_days = set(days[val_start:val_end])
        oos_days = set(days[oos_start:oos_end])
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


def make_hypotheses() -> List[Chrom]:
    """Four focused hyps + v9 + mild variants."""
    out: List[Chrom] = []

    v9 = chrom_v9()
    v9.name = "v9"
    out.append(v9)

    # H1: harder underprice DOWN veto
    h1 = chrom_v9()
    h1.name = "H1_under_veto_095"
    h1.down_block_ratio = 0.95
    out.append(h1)

    h1b = chrom_v9()
    h1b.name = "H1b_under_veto_100"
    h1b.down_block_ratio = 1.01  # block DOWN whenever under market
    out.append(h1b)

    # H2: catchup strictly to p10, faster empty trigger
    h2 = chrom_v9()
    h2.name = "H2_catchup_p10_fast"
    h2.catchup_on = True
    h2.catchup_gap = 1.0
    h2.empty_streak = 1
    out.append(h2)

    # H3: never soft-↓ while in band; grant held≤hi
    h3 = chrom_v9()
    h3.name = "H3_strict_grant"
    h3.no_down_if_held_le_hi = True
    h3.soft_down_when_over_mkt = False
    h3.down_block_ratio = 0.95
    out.append(h3)

    # H4: slightly lower inventory target (mild V10 idea without aggressive down)
    h4 = chrom_v9()
    h4.name = "H4_lower_band"
    h4.lo, h4.hi = 0.12, 0.20
    h4.over, h4.dump = 0.28, 0.40
    h4.down_hard_mult = 2.0
    h4.down_block_ratio = 0.90
    out.append(h4)

    # H5: no soft over-market dump + hard veto
    h5 = chrom_v9()
    h5.name = "H5_no_soft_over"
    h5.soft_down_when_over_mkt = False
    h5.down_block_ratio = 0.95
    h5.min_sales_up = 4
    h5.night_sales_up = 5
    out.append(h5)

    return out
