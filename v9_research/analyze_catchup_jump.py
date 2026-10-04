#!/usr/bin/env python3
"""
Анализ: empty-catchup +1 step vs прыжок к multi-seller p5.

Не трогает prod. Ответ: испортит ли прыжок относительно текущего v9 step.
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time
from collections import defaultdict
from copy import deepcopy
from datetime import datetime, timedelta
from typing import Any, Callable, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sim_v9 as S
import sim_fidelity as F
from search_v10 import Chrom, decide
from search_free import chrom_v9_live

DB = os.environ.get(
    "PRICING_DB",
    os.path.join(os.path.dirname(__file__), "..", "ml_data", "pricing.db"),
)
OUT = os.path.join(os.path.dirname(__file__), "catchup_jump_results.json")


def pct(xs: List[int], q: float) -> int:
    if not xs:
        return 0
    xs = sorted(xs)
    if len(xs) == 1:
        return xs[0]
    pos = q * (len(xs) - 1)
    i = int(pos)
    f = pos - i
    if i + 1 >= len(xs):
        return xs[-1]
    return int(xs[i] * (1 - f) + xs[i + 1] * f + 0.5)


def attach_multi_p5(con: sqlite3.Connection, rows: List[dict], window_min: int = 30) -> List[dict]:
    if not rows:
        return rows
    t_min = min(r["ts"] for r in rows)
    cur = con.execute(
        """
        SELECT item_id, ts, seller, price FROM ah_book_lots
        WHERE ts >= datetime(?, '-1 days') AND price > 150000
          AND seller IS NOT NULL AND trim(seller) != ''
        ORDER BY item_id, ts
        """,
        (t_min.replace("T", " ").replace("Z", ""),),
    )
    by_item: Dict[str, List[Tuple[str, str, int]]] = defaultdict(list)
    for item_id, ts, seller, price in cur:
        by_item[item_id].append((ts, seller or "", int(price)))

    cache: Dict[Tuple[str, str], Optional[int]] = {}

    def multi_p5_at(item: str, ts: str) -> Optional[int]:
        key = (item, ts[:16])
        if key in cache:
            return cache[key]
        raw = ts.replace("Z", "").replace("T", " ")
        try:
            t_end = datetime.fromisoformat(raw)
        except ValueError:
            cache[key] = None
            return None
        t_start = t_end - timedelta(minutes=window_min)
        start_s, end_s = t_start.strftime("%Y-%m-%d %H:%M:%S"), t_end.strftime("%Y-%m-%d %H:%M:%S")
        mins: Dict[str, int] = {}
        counts: Dict[str, int] = defaultdict(int)
        for lts, seller, price in by_item.get(item, []):
            lt = lts.replace("T", " ").replace("Z", "")[:19]
            if lt < start_s or lt > end_s:
                continue
            counts[seller] += 1
            if seller not in mins or price < mins[seller]:
                mins[seller] = price
        multi = [mins[s] for s, n in counts.items() if n >= 2]
        if not multi:
            cache[key] = None
            return None
        v = pct(multi, 0.05)
        cache[key] = v if v > 0 else None
        return cache[key]

    out = []
    for r in rows:
        rr = dict(r)
        mp5 = multi_p5_at(r["item_id"], r["ts"])
        rr["multi_p5"] = mp5
        rr["catchup_mkt"] = mp5 or rr.get("mkt") or rr.get("p10")
        out.append(rr)
    return out


DecideFn = Callable[[S.State, dict, Chrom], Tuple[str, int]]


def decide_step(st: S.State, obs: dict, ch: Chrom) -> Tuple[str, int]:
    """v9 corridor; catchup +1 к catchup_mkt (multi p5)."""
    # временно подменяем mkt для catchup-ветки через chrom path:
    # используем штатный decide, но подсовываем mkt=catchup_mkt только когда held==0
    obs2 = dict(obs)
    if st.held == 0 and obs.get("catchup_mkt"):
        obs2["mkt"] = obs["catchup_mkt"]
        obs2["p10"] = obs["catchup_mkt"]
    ch2 = deepcopy(ch)
    ch2.up_mult = 1.0
    ch2.style = "corridor"
    ch2.mf_pull = 0.0
    return decide(st, obs2, ch2)


def decide_jump(st: S.State, obs: dict, ch: Chrom, max_steps: Optional[int] = None) -> Tuple[str, int]:
    """Empty catchup: прыжок к catchup_mkt; иначе как step."""
    obs2 = dict(obs)
    catch = obs.get("catchup_mkt")
    step = max(1, int((obs.get("step") or 100_000) * ch.step_mult))
    if st.held == 0 and catch and st.empty_streak >= ch.empty_streak and st.up_cd == 0:
        if st.price < catch:
            tgt = catch
            if max_steps is not None:
                tgt = min(tgt, st.price + max_steps * step)
            tgt = max(tgt, st.price + step)
            # demand/down не трогаем — только empty jump
            # но сначала проверим, не должен ли сработать обычный HOLD из-за block —
            # упрощённо: прыжок только empty
            return "UP", tgt
    return decide_step(st, obs2, ch)


def simulate(rows_by_item, decide_fn: DecideFn, dm: F.BookDemand, ch: Chrom) -> Dict[str, Any]:
    day_profit: Dict[str, float] = defaultdict(float)
    tot = 0.0
    ups = downs = holds = cycles = 0
    under_n = over_n = deep_under_n = book_ok = 0
    catchup_ups = 0
    jump_sum = 0
    jump_n = 0
    per_sku: Dict[str, float] = defaultdict(float)

    for it, seq in rows_by_item.items():
        if not seq:
            continue
        st = S.State(price=seq[0]["price_before"], held=seq[0]["held"])
        nac0 = max(1, int(dm.nac.get(it, 200_000)))
        margin = max(1, int(nac0 * ch.nac_mult))
        for obs in seq:
            if st.up_cd > 0:
                st.up_cd -= 1
            hour = S.hour_utc(obs["ts"])
            obs = dict(obs)
            obs["night"] = 0 <= hour < 6
            share = obs["share"] or 12
            mkt = obs.get("mkt") or obs.get("p10")
            depth = obs.get("depth") or "ok"
            if obs.get("mkt_source") == "ah_p10" or obs.get("multi_p5"):
                book_ok += 1

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
            obs_g = {**obs, "sales": gs, "buys": gb}
            action, new_price = decide_fn(st, obs_g, ch)

            sales = sales_at(new_price)
            buys = dm.expected_buys(
                sales,
                st.held,
                share,
                logged_buys=obs["buys"],
                empty_idle=empty_idle
                and abs(new_price - obs["price_before"]) <= (obs["step"] or 100_000),
            )
            sales = max(0.0, min(sales, float(st.held) if st.held > 0 else 0.0))

            if action == "UP":
                ups += 1
                if st.held == 0:
                    catchup_ups += 1
                    jump_sum += max(0, new_price - st.price)
                    jump_n += 1
                st.up_cd = ch.up_cd
                st.up_streak += 1
            elif action == "DOWN":
                downs += 1
                st.up_streak = 0
            else:
                holds += 1
                st.up_streak = 0

            st.price = max(new_price, 1)
            st.held = max(0, int(round(st.held - sales + buys)))
            st.held = min(st.held, int(share * 1.5))
            p = sales * margin
            tot += p
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

    return {
        "profit_m": tot / 1e6,
        "ups": ups,
        "downs": downs,
        "holds": holds,
        "cycles": cycles,
        "under": under_n / cycles if cycles else 0,
        "deep_under": deep_under_n / cycles if cycles else 0,
        "over": over_n / cycles if cycles else 0,
        "book_ok_frac": book_ok / cycles if cycles else 0,
        "catchup_ups": catchup_ups,
        "avg_jump": (jump_sum / jump_n) if jump_n else 0,
        "per_sku_m": {k: round(v / 1e6, 1) for k, v in sorted(per_sku.items(), key=lambda x: -x[1])},
    }


def gap_stats(rows: List[dict]) -> Dict[str, Any]:
    """Насколько empty далеко от multi p5 (в шагах)."""
    gaps = []
    for r in rows:
        if r.get("held", 0) != 0:
            continue
        if (r.get("sales") or 0) > 0 or (r.get("buys") or 0) > 0:
            continue
        mp5 = r.get("multi_p5")
        if not mp5:
            continue
        price = r.get("price_before") or 0
        step = r.get("step") or 100_000
        if price < mp5 and step > 0:
            gaps.append((mp5 - price) / step)
    if not gaps:
        return {"n": 0}
    gaps.sort()
    return {
        "n": len(gaps),
        "median_steps_below_p5": round(gaps[len(gaps) // 2], 1),
        "p90_steps": round(gaps[int(0.9 * (len(gaps) - 1))], 1),
        "max_steps": round(gaps[-1], 1),
        "share_gap_ge_5": round(sum(1 for g in gaps if g >= 5) / len(gaps), 3),
        "share_gap_ge_10": round(sum(1 for g in gaps if g >= 10) / len(gaps), 3),
    }


def main():
    t0 = time.time()
    print(f"DB={DB}", flush=True)
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row

    rows = S.load_panel(con, S.FOCUS_ITEMS, t0="2026-09-28", t1="2026-10-02")
    print(f"panel={len(rows)}", flush=True)
    if len(rows) < 100:
        print("too thin", flush=True)
        sys.exit(1)

    rows = F.attach_ah_market(con, rows)
    print("multi p5…", flush=True)
    rows = attach_multi_p5(con, rows)
    mp5_n = sum(1 for r in rows if r.get("multi_p5"))
    print(f"multi_p5 coverage={mp5_n}/{len(rows)} ({100*mp5_n/len(rows):.1f}%)", flush=True)
    gaps = gap_stats(rows)
    print("empty gaps vs multiP5:", json.dumps(gaps, ensure_ascii=False), flush=True)

    nac = S.avg_nacenka(con, S.FOCUS_ITEMS)
    base = chrom_v9_live()
    base.catchup_on = True
    base.empty_streak = 2
    base.catchup_gap = 1.0
    base.up_mult = 1.0
    base.down_block_ratio = 0.95
    base.demand_max_ratio = 1.0
    base.up_cd = 2
    base.max_up_streak = 1
    base.style = "corridor"
    base.mf_pull = 0.0

    variants: List[Tuple[str, DecideFn, Chrom]] = [
        ("v9_step_+1", decide_step, base),
        ("v9_jump_multiP5", decide_jump, base),
        ("v9_jump_cap5", lambda st, obs, ch: decide_jump(st, obs, ch, 5), base),
        ("v9_jump_cap3", lambda st, obs, ch: decide_jump(st, obs, ch, 3), base),
    ]
    no_cu = deepcopy(base)
    no_cu.catchup_on = False
    no_cu.empty_streak = 99
    no_cu.name = "v9_no_catchup"
    variants.append(("v9_no_catchup", decide_step, no_cu))

    folds = F.walk_forward_folds(rows, n_folds=2)
    fold_out = []
    for fd in folds:
        dm = F.BookDemand()
        dm.fit(F.filter_days(rows, fd["train_fit"]), nac)
        by = S.split_by_item(F.filter_days(rows, fd["oos"]))
        print(f"fold{fd['fold']} oos={fd['oos_range']}", flush=True)
        fr = {"fold": fd["fold"], "oos": fd["oos_range"], "variants": {}}
        for name, fn, ch in variants:
            m = simulate(by, fn, dm, ch)
            fr["variants"][name] = {
                "profit_m": round(m["profit_m"], 2),
                "under": round(m["under"], 3),
                "over": round(m["over"], 3),
                "catchup_ups": m["catchup_ups"],
                "avg_jump": round(m["avg_jump"], 0),
            }
            print(
                f"  {name}: {m['profit_m']:.2f}M under={m['under']:.3f} "
                f"cu={m['catchup_ups']} jump={m['avg_jump']:.0f}",
                flush=True,
            )
        fold_out.append(fr)

    days = sorted({r["ts"][:10] for r in rows})
    cut = max(1, len(days) // 2)
    late_fit, late_eval = set(days[:cut]), set(days[cut:])
    dm = F.BookDemand()
    dm.fit(F.filter_days(rows, late_fit), nac)
    by = S.split_by_item(F.filter_days(rows, late_eval))
    late = {}
    for name, fn, ch in variants:
        m = simulate(by, fn, dm, ch)
        late[name] = {
            "profit_m": round(m["profit_m"], 2),
            "under": round(m["under"], 3),
            "over": round(m["over"], 3),
            "catchup_ups": m["catchup_ups"],
            "avg_jump": round(m["avg_jump"], 0),
            "per_sku_m": m["per_sku_m"],
        }

    base_p = late["v9_step_+1"]["profit_m"] or 1e-9
    ranking = []
    for name, st in late.items():
        ranking.append(
            {
                "name": name,
                "late_m": st["profit_m"],
                "vs_step_%": round(100 * (st["profit_m"] / base_p - 1), 2),
                "under": st["under"],
                "over": st["over"],
                "catchup_ups": st["catchup_ups"],
                "avg_jump": st["avg_jump"],
            }
        )
    ranking.sort(key=lambda x: -x["late_m"])

    jump = next(r for r in ranking if r["name"] == "v9_jump_multiP5")
    step = next(r for r in ranking if r["name"] == "v9_step_+1")
    cap5 = next(r for r in ranking if r["name"] == "v9_jump_cap5")
    d = jump["vs_step_%"]
    if d <= -5:
        verdict, detail = "hurt", f"полный прыжок {d}% к step — портит"
    elif d >= 5 and jump["under"] <= step["under"] + 0.03:
        verdict, detail = "helps", f"полный прыжок +{d}% — помогает"
    elif abs(d) < 5:
        verdict, detail = "flat", f"полный прыжок ~{d}% — в шуме панели (~3 дня)"
    else:
        verdict, detail = "risky", f"прыжок {d}%, under {jump['under']} vs step {step['under']}"

    # fold consistency
    fold_deltas = []
    for fr in fold_out:
        sp = fr["variants"]["v9_step_+1"]["profit_m"] or 1e-9
        jp = fr["variants"]["v9_jump_multiP5"]["profit_m"]
        fold_deltas.append(round(100 * (jp / sp - 1), 2))

    out = {
        "db": DB,
        "n_cycles": len(rows),
        "days": days,
        "multi_p5_coverage": round(mp5_n / len(rows), 3),
        "empty_gap_vs_multiP5": gaps,
        "folds": fold_out,
        "fold_jump_vs_step_%": fold_deltas,
        "late": late,
        "ranking": ranking,
        "verdict": verdict,
        "detail": detail,
        "cap5_vs_step_%": cap5["vs_step_%"],
        "elapsed_s": round(time.time() - t0, 1),
        "limits": [
            "панель локальная ~3 суток (29.09–01.10) — шум большой",
            "sim demand ≠ FunTime; bookMid UP-cap в скрипте не наложен",
            "прыжок к multi p5, не к p10/середине книги",
        ],
    }
    with open(OUT, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)

    print("\n=== VERDICT ===", flush=True)
    print(verdict, "|", detail, flush=True)
    print("fold Δ% jump vs step:", fold_deltas, flush=True)
    print("ranking:", json.dumps(ranking, ensure_ascii=False, indent=2), flush=True)
    print(f"wrote {OUT} ({out['elapsed_s']}s)", flush=True)


if __name__ == "__main__":
    main()
