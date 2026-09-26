#!/usr/bin/env python3
"""Profit-max book policy: buy gate vs book p10 + book-percentile alternatives."""
from __future__ import annotations

import json
import sqlite3
import statistics
from collections import defaultdict, deque
from datetime import datetime

DB = "/root/4narek-new/ml_data/pricing.db"
SINCE = "2026-09-03T00:00:00Z"
OUT = "/tmp/book2_profit_max.json"


def parse_ts(s: str) -> float:
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


def pct(sorted_vals: list[float], p: float):
    if not sorted_vals:
        return None
    if len(sorted_vals) == 1:
        return sorted_vals[0]
    k = (len(sorted_vals) - 1) * p
    f = int(k)
    c = min(f + 1, len(sorted_vals) - 1)
    if f == c:
        return sorted_vals[f]
    return sorted_vals[f] * (c - k) + sorted_vals[c] * (k - f)


def dist(xs: list[float]) -> dict:
    xs = sorted(xs)
    return {
        k: pct(xs, q)
        for k, q in [
            ("p10", 0.1),
            ("p25", 0.25),
            ("p50", 0.5),
            ("p75", 0.75),
            ("p90", 0.9),
            ("p95", 0.95),
        ]
    }


def main() -> None:
    db = sqlite3.connect(DB)
    db.row_factory = sqlite3.Row

    print("loading book hours...")
    rows = db.execute(
        """
        WITH latest AS (
          SELECT item_id, substr(ts,1,13) AS hr, uuid, price, MAX(ts) AS mts
          FROM ah_book_lots
          WHERE ts >= ? AND price > 0
          GROUP BY item_id, substr(ts,1,13), uuid
        )
        SELECT item_id, hr, price FROM latest
        """,
        (SINCE,),
    ).fetchall()
    print("book lot-hours", len(rows))
    by: dict[tuple, list[int]] = defaultdict(list)
    for r in rows:
        by[(r["item_id"], r["hr"])].append(r["price"])

    book: dict[tuple, dict] = {}
    for key, prices in by.items():
        prices.sort()
        if len(prices) < 12:
            continue
        book[key] = {
            "p5": pct(prices, 0.05),
            "p10": pct(prices, 0.10),
            "p25": pct(prices, 0.25),
            "p50": pct(prices, 0.50),
            "p90": pct(prices, 0.90),
            "n": len(prices),
        }
    print("book hours thick", len(book))

    buys = list(
        db.execute(
            """
            SELECT ts, item_id, category_type, price, nacenka, ref_price
            FROM trade_events WHERE event_type='buy' AND ts>=? ORDER BY ts
            """,
            (SINCE,),
        )
    )
    sells = list(
        db.execute(
            """
            SELECT ts, item_id, category_type, price, nacenka
            FROM trade_events WHERE event_type='sell' AND ts>=? ORDER BY ts
            """,
            (SINCE,),
        )
    )
    print("buys", len(buys), "sells", len(sells))

    buy_q: dict[str, deque] = defaultdict(deque)
    pairs = []
    for b in buys:
        buy_q[b["item_id"]].append(b)
    for s in sells:
        q = buy_q[s["item_id"]]
        if not q:
            continue
        b = q.popleft()
        bt, st = parse_ts(b["ts"]), parse_ts(s["ts"])
        if st < bt:
            continue
        hold_h = (st - bt) / 3600
        bp, sp = b["price"], s["price"]
        if bp <= 0 or sp <= 0:
            continue
        hr = b["ts"][:13]
        bk = book.get((b["item_id"], hr))
        if not bk:
            continue
        p10 = bk["p10"]
        if p10 <= 0:
            continue
        pairs.append(
            {
                "item": b["item_id"],
                "cat": b["category_type"] or "",
                "buy": bp,
                "sell": sp,
                "hold": hold_h,
                "margin": (sp - bp) / bp,
                "profit": sp - bp,
                "buy_r": bp / p10,
                "sell_r": sp / p10,
                "gap_r": (sp - bp) / p10,
                "p5": bk["p5"],
                "p10": p10,
                "p25": bk["p25"],
                "p50": bk["p50"],
                "p90": bk["p90"],
                "nac": b["nacenka"] or 0,
            }
        )
    print("pairs with book", len(pairs))

    swords = [p for p in pairs if p["cat"] == "netherite_sword-1.21"]
    armor = [p for p in pairs if p["cat"] == "netherite_armor-1.21"]
    wins = [p for p in pairs if p["profit"] > 0]

    def summarize(name: str, ps: list) -> dict:
        w = [p for p in ps if p["profit"] > 0]
        return {
            "name": name,
            "n": len(ps),
            "n_win": len(w),
            "buy_r_all": dist([p["buy_r"] for p in ps]),
            "buy_r_win": dist([p["buy_r"] for p in w]) if w else {},
            "sell_r_win": dist([p["sell_r"] for p in w]) if w else {},
            "gap_r_win": dist([p["gap_r"] for p in w]) if w else {},
            "total_profit": sum(p["profit"] for p in ps),
            "frac_buy_le_p5": sum(1 for p in ps if p["buy"] <= p["p5"]) / len(ps) if ps else 0,
            "frac_buy_le_p10": sum(1 for p in ps if p["buy"] <= p["p10"]) / len(ps) if ps else 0,
            "med_buy_vs_p5": statistics.median([p["buy"] / p["p5"] for p in ps if p["p5"] > 0])
            if ps
            else None,
        }

    def score_gate(ps: list, buy_hi: float, sell_c=None, sell_tol=0.08) -> dict:
        kept = []
        for p in ps:
            if p["buy_r"] > buy_hi + 1e-9:
                continue
            if sell_c is not None and abs(p["sell_r"] - sell_c) > sell_tol:
                continue
            kept.append(p)
        if not kept:
            return {
                "n": 0,
                "profit": 0,
                "hold": 0,
                "pph": 0,
                "med_margin": 0,
                "util": 0,
                "win": 0,
            }
        profit = sum(p["profit"] for p in kept)
        hold = sum(p["hold"] for p in kept) + 1e-6
        margins = [p["margin"] for p in kept]
        holds = [p["hold"] for p in kept]
        med_m = statistics.median(margins)
        med_h = statistics.median(holds)
        return {
            "n": len(kept),
            "profit": profit,
            "hold": hold,
            "pph": profit / hold,
            "med_margin": med_m,
            "med_hold": med_h,
            "util": med_m / (med_h + 0.25),
            "win": sum(1 for p in kept if p["profit"] > 0) / len(kept),
            "mean_margin": statistics.mean(margins),
        }

    def scan_buy_hi(ps: list) -> list:
        rows = []
        for buy_hi in [0.50, 0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85, 0.90, 0.95, 1.00, 1.05]:
            s = score_gate(ps, buy_hi, sell_c=None)
            s["buy_hi"] = buy_hi
            missed = [p for p in ps if p["buy_r"] > buy_hi and p["profit"] > 0]
            bad_taken = [p for p in ps if p["buy_r"] <= buy_hi and p["profit"] <= 0]
            s["missed_win_n"] = len(missed)
            s["missed_win_profit"] = sum(p["profit"] for p in missed)
            s["bad_n"] = len(bad_taken)
            s["bad_loss"] = sum(p["profit"] for p in bad_taken)
            s["ppp"] = s["profit"] / s["n"] if s["n"] else 0
            rows.append(s)
        return rows

    def score_book_pct(ps: list, buy_key: str, sell_key=None, sell_tol_frac=0.08) -> dict:
        kept = []
        for p in ps:
            bmax = p[buy_key]
            if bmax is None or p["buy"] > bmax:
                continue
            if sell_key:
                st = p[sell_key]
                if st <= 0:
                    continue
                if abs(p["sell"] - st) / st > sell_tol_frac:
                    continue
            kept.append(p)
        if not kept:
            return {"n": 0, "profit": 0, "pph": 0, "win": 0, "med_margin": 0}
        profit = sum(p["profit"] for p in kept)
        hold = sum(p["hold"] for p in kept) + 1e-6
        return {
            "n": len(kept),
            "profit": profit,
            "pph": profit / hold,
            "med_margin": statistics.median([p["margin"] for p in kept]),
            "win": sum(1 for p in kept if p["profit"] > 0) / len(kept),
        }

    def full_grid(ps: list) -> list:
        rows = []
        for buy_hi in [0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85]:
            for sell_c in [0.90, 0.95, 1.00, 1.05]:
                s = score_gate(ps, buy_hi, sell_c=sell_c, sell_tol=0.07)
                s_all = score_gate(ps, buy_hi, sell_c=None)
                rows.append(
                    {
                        "buy_hi": buy_hi,
                        "sell_c": sell_c,
                        "nac": round(sell_c - buy_hi, 2),
                        "matched_n": s["n"],
                        "matched_profit": s["profit"],
                        "matched_pph": s["pph"],
                        "matched_util": s["util"],
                        "gate_n": s_all["n"],
                        "gate_profit": s_all["profit"],
                        "gate_pph": s_all["pph"],
                        "bad_loss": sum(
                            p["profit"] for p in ps if p["buy_r"] <= buy_hi and p["profit"] <= 0
                        ),
                    }
                )
        rows.sort(key=lambda r: (-r["gate_pph"], -r["gate_profit"]))
        return rows

    book_gates = []
    for bk in ["p5", "p10", "p25"]:
        for sk in [None, "p10", "p50", "p90"]:
            s = score_book_pct(pairs, bk, sk)
            s["buy_book"] = bk
            s["sell_book"] = sk or "any"
            book_gates.append(s)

    book_gates_sword = []
    for bk in ["p5", "p10", "p25"]:
        for sk in [None, "p10", "p50"]:
            s = score_book_pct(swords, bk, sk)
            s["buy_book"] = bk
            s["sell_book"] = sk or "any"
            book_gates_sword.append(s)

    # Static nacenka: how far below implied gate (p10 - nac) did we buy?
    head = []
    for p in pairs:
        if p["nac"] > 0 and p["p10"] > 0:
            implied = (p["p10"] - p["nac"]) / p["p10"]
            head.append(
                {
                    "buy_r": p["buy_r"],
                    "implied_gate": implied,
                    "slack": implied - p["buy_r"],
                    "profit": p["profit"],
                    "nac_r": p["nac"] / p["p10"],
                }
            )

    # Buy below static gate: use nac on buy + assume sell listing ~ actual pair sell
    # (bots buy if ask < sell - nac). Measure buys with buy < sell_pair - nac
    below_gate = []
    for p in pairs:
        if p["nac"] <= 0:
            continue
        gate = p["sell"] - p["nac"]  # proxy using realized sell
        if p["buy"] < gate:
            below_gate.append(p)

    bs = scan_buy_hi(swords)
    ba = scan_buy_hi(pairs)
    mx_p = max(r["profit"] for r in bs) or 1
    mx_pph = max(r["pph"] for r in bs) or 1
    for r in bs:
        r["combo"] = 0.55 * (r["profit"] / mx_p) + 0.45 * (r["pph"] / mx_pph)
    mx_pa = max(r["profit"] for r in ba) or 1
    mx_ppha = max(r["pph"] for r in ba) or 1
    for r in ba:
        r["combo"] = 0.55 * (r["profit"] / mx_pa) + 0.45 * (r["pph"] / mx_ppha)

    # Percentile policy from profitable buys: buyMax = pK of win buy_r, sell = pM of win sell_r
    pct_policies = []
    if swords:
        sw = [p for p in swords if p["profit"] > 0]
        for buy_pct in (0.75, 0.85, 0.90, 0.95):
            for sell_pct in (0.50, 0.75, 0.90):
                br = pct(sorted(p["buy_r"] for p in sw), buy_pct)
                sr = pct(sorted(p["sell_r"] for p in sw), sell_pct)
                if br is None or sr is None or sr <= br:
                    continue
                s_all = score_gate(swords, br, sell_c=None)
                s_m = score_gate(swords, br, sell_c=sr, sell_tol=0.08)
                pct_policies.append(
                    {
                        "buy_pct": buy_pct,
                        "sell_pct": sell_pct,
                        "buy_hi": round(br, 3),
                        "sell_c": round(sr, 3),
                        "nac": round(sr - br, 3),
                        "gate_n": s_all["n"],
                        "gate_profit": s_all["profit"],
                        "gate_pph": s_all["pph"],
                        "matched_n": s_m["n"],
                        "matched_pph": s_m["pph"],
                    }
                )
        pct_policies.sort(key=lambda x: (-x["gate_pph"], -x["gate_profit"]))

    out = {
        "meta": {"pairs": len(pairs), "swords": len(swords), "since": SINCE},
        "all_summary": summarize("all", pairs),
        "sword_summary": summarize("sword", swords),
        "armor_summary": summarize("armor", armor),
        "buy_hi_scan_all": ba,
        "buy_hi_scan_sword": bs,
        "grid_all": full_grid(pairs)[:12],
        "grid_sword": full_grid(swords)[:12],
        "book_gates_all": sorted(book_gates, key=lambda x: -x["pph"])[:10],
        "book_gates_sword": sorted(book_gates_sword, key=lambda x: -x["pph"])[:10],
        "pct_policies_sword": pct_policies[:12],
        "headroom": {
            "n": len(head),
            "slack_p50": pct(sorted(h["slack"] for h in head), 0.5) if head else None,
            "slack_p90": pct(sorted(h["slack"] for h in head), 0.9) if head else None,
            "buy_r_p95_win": pct(sorted(p["buy_r"] for p in wins), 0.95) if wins else None,
            "buy_r_p90_win": pct(sorted(p["buy_r"] for p in wins), 0.9) if wins else None,
            "buy_r_p50_win": pct(sorted(p["buy_r"] for p in wins), 0.5) if wins else None,
            "nac_r_p50": pct(sorted(h["nac_r"] for h in head), 0.5) if head else None,
            "below_gate_proxy_n": len(below_gate),
            "below_gate_buy_r": dist([p["buy_r"] for p in below_gate]) if below_gate else {},
        },
        "sword_best_net": max(bs, key=lambda x: x["profit"]),
        "sword_best_pph": max(bs, key=lambda x: x["pph"]),
        "sword_best_combo": max(bs, key=lambda x: x["combo"]),
        "all_best_combo": max(ba, key=lambda x: x["combo"]),
    }

    with open(OUT, "w") as f:
        json.dump(out, f, indent=2)
    print("WROTE", OUT)
    print("sword_summary", json.dumps(out["sword_summary"], indent=2)[:1500])
    print("headroom", json.dumps(out["headroom"], indent=2))
    print("sword_best_combo", {k: out["sword_best_combo"][k] for k in ("buy_hi", "n", "profit", "pph", "combo", "bad_loss", "missed_win_profit")})
    print("sword_best_net", {k: out["sword_best_net"][k] for k in ("buy_hi", "n", "profit", "pph", "bad_loss")})
    print("sword_best_pph", {k: out["sword_best_pph"][k] for k in ("buy_hi", "n", "profit", "pph", "bad_loss")})
    print("TOP pct_policies", json.dumps(out["pct_policies_sword"][:5], indent=2))
    print("TOP book_gates_sword", json.dumps(out["book_gates_sword"][:5], indent=2))
    print("TOP grid_sword", json.dumps(out["grid_sword"][:5], indent=2))


if __name__ == "__main__":
    main()
