"""v10 rails decide — pure function for sim / unit tests.

See V10_RAILS_DESIGN.md. Not wired to production Go.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Optional, Tuple


@dataclass
class V10In:
    held: int
    sales: int
    buys: int
    price: int
    step: int
    share: int
    floor: int = 0
    floor_ok: bool = False
    mid: int = 0
    mid_ok: bool = False
    try_sells: int = 0
    night: bool = False
    # tunable
    lo_frac: float = 0.18
    hi_frac: float = 0.25
    over_frac: float = 0.35
    dump_frac: float = 0.50
    deadband_steps: float = 0.75
    max_steps: int = 2
    min_sales_up_day: int = 3
    min_sales_up_night: int = 4
    # toxic: weak flow
    toxic_max_sales: int = 1  # sales <= this while excess → toxic if also buys>=sales
    try_toxic_mult: float = 3.0  # try_sells >= mult*max(sales,1) → toxic


@dataclass
class V10Out:
    action: str
    new_price: int
    reason: str


def _band(share: int, lo_f, hi_f, over_f, dump_f):
    if share <= 0:
        return 0, 0, 0, 0
    lo = int(share * lo_f + 0.5)
    hi = int(share * hi_f + 0.5)
    over = int(share * over_f + 0.5)
    dump = int(share * dump_f + 0.5)
    return lo, hi, over, dump


def _pos_target(load: float) -> float:
    """held/share → desired position in [floor,mid] (0=floor, 1=mid)."""
    if load <= 0.05:
        return 0.85
    if load < 0.18:
        return 0.70
    if load <= 0.25:
        return 0.50
    if load < 0.35:
        return 0.30
    if load < 0.50:
        return 0.15
    return 0.05


def _toxic(inp: V10In, hi: int, dump: int) -> bool:
    if inp.held <= hi:
        return False
    if inp.held >= dump:
        return True
    sales, buys = inp.sales, inp.buys
    if sales <= inp.toxic_max_sales and buys >= sales:
        return True
    if sales > 0 and buys > sales * 1.5:
        return True
    if inp.try_sells >= inp.try_toxic_mult * max(sales, 1) and sales <= 2:
        return True
    return False


def _patient_excess(inp: V10In, hi: int) -> bool:
    """Overstock but still flowing — do not force DOWN."""
    if inp.held <= hi:
        return False
    if inp.sales >= 2 and inp.sales >= inp.buys:
        return True
    if inp.sales >= 3:
        return True
    return False


def v10_decide(inp: V10In) -> V10Out:
    step = max(1, inp.step)
    price = inp.price
    if price < inp.price and False:
        pass

    # --- rails first ---
    if inp.floor_ok and inp.floor > 0 and price < inp.floor:
        return V10Out("price_up_floor_jump", inp.floor, "below_floor")
    if inp.mid_ok and inp.mid > 0 and price > inp.mid:
        return V10Out("price_down_mid_jump", inp.mid, "above_mid")

    lo, hi, over, dump = _band(inp.share, inp.lo_frac, inp.hi_frac, inp.over_frac, inp.dump_frac)
    load = (inp.held / inp.share) if inp.share > 0 else 0.0
    pos_t = _pos_target(load)

    floor = inp.floor if inp.floor_ok and inp.floor > 0 else max(step, price // 4)
    mid = inp.mid if inp.mid_ok and inp.mid > 0 else price + 10 * step
    if mid <= floor:
        mid = floor + max(step, 1)

    target = int(round(floor + pos_t * (mid - floor)))
    # clamp target into rails
    target = max(floor, min(mid, target))

    dead = int(inp.deadband_steps * step)
    if abs(price - target) <= dead:
        return V10Out("hold_deadband", price, "near_target")

    # --- DOWN toward target ---
    if target < price - dead:
        toxic = _toxic(inp, hi, dump)
        patient = _patient_excess(inp, hi)
        if patient and not toxic and inp.held < dump:
            return V10Out("hold_patient_excess", price, "patient_excess")
        if not toxic and inp.held <= hi:
            return V10Out("hold_no_down_signal", price, "no_down_permission")
        # allowed: toxic or dump
        delta = min(inp.max_steps * step, price - target)
        # dump zone: prefer hard
        if inp.held >= dump or inp.held >= over:
            delta = max(delta, min(2 * step, price - target))
        new_p = max(target, price - delta)
        new_p = max(floor, new_p)
        if new_p < price:
            return V10Out("price_down_v10", new_p, "toxic_or_dump" if toxic else "to_target")
        return V10Out("hold_at_floor", price, "at_floor")

    # --- UP toward target ---
    if target > price + dead:
        min_s = inp.min_sales_up_night if inp.night else inp.min_sales_up_day
        under = inp.held > 0 and inp.held < lo
        empty = inp.held == 0
        strong = inp.sales >= min_s and inp.sales > inp.buys
        # empty: only climb if we want high target (low load) — rails already fixed below floor
        if under and strong:
            delta = min(inp.max_steps * step, target - price)
            new_p = min(target, price + delta)
            new_p = min(mid, new_p)
            if new_p > price:
                return V10Out("price_up_v10_demand", new_p, "under_demand")
            return V10Out("hold_at_mid", price, "at_mid")
        if empty and strong:
            # rare: empty but sales in window — allow toward target
            delta = min(step, target - price)
            new_p = min(mid, price + delta)
            if new_p > price:
                return V10Out("price_up_v10_empty_flow", new_p, "empty_but_sales")
        return V10Out("hold_no_up_permission", price, "no_up_permission")

    return V10Out("hold", price, "balanced")


def _selftest() -> None:
    # below floor → jump
    o = v10_decide(V10In(10, 0, 0, 100, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.new_price == 400 and "floor" in o.reason
    # above mid → jump
    o = v10_decide(V10In(10, 0, 0, 900, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.new_price == 800 and "mid" in o.reason
    # patient excess: held high, sales ok → hold
    o = v10_decide(
        V10In(40, 4, 1, 600, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True)
    )
    assert o.action == "hold_patient_excess", o
    # toxic excess: held high, sales 0 buys 5 → down
    o = v10_decide(
        V10In(40, 0, 5, 600, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True)
    )
    assert o.action == "price_down_v10", o
    # under + demand → up
    o = v10_decide(
        V10In(10, 4, 1, 500, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True)
    )
    assert o.action == "price_up_v10_demand", o
    print("policy_v10_rails selftest OK")


if __name__ == "__main__":
    _selftest()
