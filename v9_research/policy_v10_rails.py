"""v10 rails decide — rev2: sell-through vs stock, not 'hope it pays'.

See V10_RAILS_DESIGN.md. Not wired to production Go.
"""
from __future__ import annotations

from dataclasses import dataclass


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
    lo_frac: float = 0.18
    hi_frac: float = 0.25
    over_frac: float = 0.35
    dump_frac: float = 0.50
    deadband_steps: float = 0.75
    max_steps: int = 2
    # sell-through = sales/held; below → prefer DOWN when excess
    turn_low: float = 0.08
    turn_high: float = 0.25
    # optional model score P(DOWN better than HOLD); None = rules only
    p_down_better: float | None = None
    p_down_tau: float = 0.55
    # optional P(UP better) — unused for gating (↑ on under/empty never model-blocked)
    p_up_better: float | None = None
    p_up_tau: float = 0.55


@dataclass
class V10Out:
    action: str
    new_price: int
    reason: str


def _band(share: int, lo_f, hi_f, over_f, dump_f):
    if share <= 0:
        return 0, 0, 0, 0
    return (
        int(share * lo_f + 0.5),
        int(share * hi_f + 0.5),
        int(share * over_f + 0.5),
        int(share * dump_f + 0.5),
    )


def _pos_target(load: float) -> float:
    if load <= 0.05:
        return 0.90
    if load < 0.18:
        return 0.75
    if load <= 0.25:
        return 0.50
    if load < 0.35:
        return 0.28
    if load < 0.50:
        return 0.12
    return 0.05


def _turn(sales: int, held: int) -> float:
    return sales / max(held, 1)


def _rail_span(inp: V10In, price: int, step: int):
    floor = inp.floor if inp.floor_ok and inp.floor > 0 else max(step, price // 4)
    mid = inp.mid if inp.mid_ok and inp.mid > 0 else price + 10 * step
    if mid <= floor:
        mid = floor + step
    return floor, mid


def _band_pos(price: int, floor: int, mid: int) -> float:
    if mid <= floor:
        return 0.5
    return max(0.0, min(1.0, (price - floor) / (mid - floor)))


def v10_decide(inp: V10In) -> V10Out:
    step = max(1, inp.step)
    price = inp.price

    if inp.floor_ok and inp.floor > 0 and price < inp.floor:
        return V10Out("price_up_floor_jump", inp.floor, "below_floor")
    if inp.mid_ok and inp.mid > 0 and price > inp.mid:
        return V10Out("price_down_mid_jump", inp.mid, "above_mid")

    lo, hi, over, dump = _band(inp.share, inp.lo_frac, inp.hi_frac, inp.over_frac, inp.dump_frac)
    load = (inp.held / inp.share) if inp.share > 0 else 0.0
    turn = _turn(inp.sales, inp.held)
    floor, mid = _rail_span(inp, price, step)
    pos = _band_pos(price, floor, mid)
    target = int(round(floor + _pos_target(load) * (mid - floor)))
    target = max(floor, min(mid, target))
    dead = int(inp.deadband_steps * step)

    if abs(price - target) <= dead:
        return V10Out("hold_deadband", price, "near_target")

    # ----- DOWN: excess + weak sell-through vs stock -----
    if target < price - dead and inp.held > hi:
        want_down = False
        reason = "hold_flow_ok"
        if turn < inp.turn_low:
            want_down = True
            reason = "low_turn"
        elif inp.held >= dump and turn < inp.turn_high:
            want_down = True
            reason = "dump_zone"
        elif inp.held >= over and turn < (inp.turn_low + inp.turn_high) / 2:
            want_down = True
            reason = "over_mid_turn"

        # model override in grey zone
        if inp.p_down_better is not None:
            if inp.p_down_better >= inp.p_down_tau:
                want_down = True
                reason = "model_down"
            elif turn >= inp.turn_low and inp.held < dump:
                want_down = False
                reason = "model_hold"

        if not want_down:
            return V10Out("hold_sellthrough_ok", price, reason)

        delta = min(inp.max_steps * step, price - target)
        if inp.held >= over:
            delta = max(delta, min(2 * step, price - target))
        new_p = max(floor, max(target, price - delta))
        if new_p < price:
            return V10Out("price_down_v10", new_p, reason)
        return V10Out("hold_at_floor", price, "at_floor")

    if target < price - dead and inp.held <= hi:
        return V10Out("hold_no_excess", price, "no_excess")

    # ----- UP: understock — never model-blocked; clear cases always climb -----
    if target > price + dead:
        under = 0 < inp.held < lo
        empty = inp.held == 0
        # veto: high in band, visible but no sales
        if pos >= 0.85 and inp.try_sells >= 3 and inp.sales == 0:
            return V10Out("hold_up_veto_dead_high", price, "dead_near_ceiling")

        if under or empty:
            # clear UP: empty, or any sales while thin, or strong demand, or high turn
            strong = inp.sales >= 3 and inp.sales > inp.buys
            hot_turn = turn >= inp.turn_high  # e.g. 1 sale / held=3
            any_flow = inp.sales >= 1 and under
            tag = (
                "empty"
                if empty
                else (
                    "under_strong"
                    if strong
                    else ("under_hot_turn" if hot_turn or any_flow else "under_weak")
                )
            )

            # p_up_better ignored — Sasha: never block ↑ on under/empty
            delta = min(inp.max_steps * step, target - price)
            if empty and inp.sales == 0 and inp.buys == 0:
                delta = min(step, delta)  # one step when totally idle
            elif tag == "under_weak":
                delta = min(step, delta)  # one step when thin + silent
            new_p = min(mid, min(target, price + delta))
            if new_p > price:
                return V10Out("price_up_v10", new_p, tag)
            return V10Out("hold_at_mid", price, "at_mid")

        return V10Out("hold_no_up", price, "no_understock")

    return V10Out("hold", price, "balanced")


def make_sim_policy(scorers=None, floor_frac=0.75, mid_frac=1.05):
    """Sim adapter: State+obs → (UP|DOWN|HOLD, price). scorers: CFScorers | None."""

    def policy(st, obs, dm):
        share = max(int(obs.get("share") or 12), 1)
        step = max(int(obs.get("step") or 100_000), 1)
        mkt = obs.get("mkt") or obs.get("p10") or st.price
        floor = int(floor_frac * mkt)
        mid = int(mid_frac * mkt)
        sales = int(round(float(obs.get("sales") or 0)))
        buys = int(round(float(obs.get("buys") or 0)))
        try_s = int(obs.get("try_sells") or 0)
        night = bool(obs.get("night"))
        if "night" not in obs and obs.get("ts"):
            try:
                night = 0 <= int(obs["ts"][11:13]) < 6
            except Exception:
                night = False

        p_down = p_up = None
        down_tau = 0.55
        up_tau = 0.55
        if scorers is not None:
            # only score when in potential grey / excess / under — cheap always-score OK
            p_down = scorers.p_down(st.held, obs, price=st.price)
            p_up = scorers.p_up(st.held, obs, price=st.price)
            down_tau = scorers.down_tau
            up_tau = scorers.up_tau

        inp = V10In(
            held=st.held,
            sales=sales,
            buys=buys,
            price=st.price,
            step=step,
            share=share,
            floor=floor,
            floor_ok=True,
            mid=mid,
            mid_ok=True,
            try_sells=try_s,
            night=night,
            p_down_better=p_down,
            p_down_tau=down_tau,
            p_up_better=p_up,
            p_up_tau=up_tau,
        )
        out = v10_decide(inp)
        if "down" in out.action:
            return "DOWN", out.new_price
        if "up" in out.action:
            return "UP", out.new_price
        return "HOLD", out.new_price

    return policy


def _selftest() -> None:
    # rails
    o = v10_decide(V10In(10, 0, 0, 100, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.new_price == 400
    o = v10_decide(V10In(10, 0, 0, 900, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.new_price == 800

    # excess + low turn (sales=1 held=40 → 0.025) → down
    o = v10_decide(V10In(40, 1, 0, 650, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "price_down_v10", o

    # excess + high turn (sales=12 held=40 → 0.3) → hold
    o = v10_decide(V10In(40, 12, 2, 650, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "hold_sellthrough_ok", o

    # under + weak sales → up
    o = v10_decide(V10In(8, 0, 0, 500, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "price_up_v10", o

    # under + strong → up
    o = v10_decide(V10In(8, 4, 1, 500, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "price_up_v10", o

    # 1 sale / held=3 (hot turn, under) → up
    o = v10_decide(V10In(3, 1, 0, 500, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "price_up_v10", o
    assert o.reason in ("under_hot_turn", "under_strong", "under_weak"), o

    # model must NOT block under UP
    o = v10_decide(
        V10In(
            8, 0, 0, 500, 50, 100,
            floor=400, floor_ok=True, mid=800, mid_ok=True,
            p_up_better=0.01, p_up_tau=0.99,
        )
    )
    assert o.action == "price_up_v10", o

    # empty → up
    o = v10_decide(V10In(0, 0, 0, 500, 50, 100, floor=400, floor_ok=True, mid=800, mid_ok=True))
    assert o.action == "price_up_v10", o

    print("policy_v10_rails rev2 selftest OK")


if __name__ == "__main__":
    _selftest()
