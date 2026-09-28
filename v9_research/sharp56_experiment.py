#!/usr/bin/env python3
"""Status board for sword-sharp5 / sharp6 experiment. Read-only on pricing.db."""
from __future__ import annotations

import sqlite3
import statistics
import sys
from datetime import datetime, timezone

DB = sys.argv[1] if len(sys.argv) > 1 else "/root/4narek-new/ml_data/pricing.db"
IDS = ("sword-sharp5-1.21", "sword-sharp6-1.21", "sword7-1.21")
SINCE = sys.argv[2] if len(sys.argv) > 2 else "2026-09-29T00:00:00Z"


def pct(xs: list[float], p: float):
    if not xs:
        return None
    xs = sorted(xs)
    if len(xs) == 1:
        return xs[0]
    k = (len(xs) - 1) * p
    f = int(k)
    c = min(f + 1, len(xs) - 1)
    return xs[f] if f == c else xs[f] * (c - k) + xs[c] * (k - f)


def med(xs):
    return statistics.median(xs) if xs else None


def main() -> None:
    db = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    print(f"DB={DB}")
    print(f"since={SINCE}  now={datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%MZ')}")
    print()

    print("=== BOOK (unique uuid / lot rows) ===")
    for item in IDS:
        row = db.execute(
            """
            SELECT COUNT(*) lots, COUNT(DISTINCT uuid) uuids,
                   MIN(price), MAX(price)
            FROM ah_book_lots WHERE item_id=? AND ts>=? AND price>0
            """,
            (item, SINCE),
        ).fetchone()
        prices = [
            r[0]
            for r in db.execute(
                """
                WITH latest AS (
                  SELECT uuid, price, MAX(ts) AS mts
                  FROM ah_book_lots WHERE item_id=? AND ts>=? AND price>0
                  GROUP BY uuid
                )
                SELECT price FROM latest
                """,
                (item, SINCE),
            )
        ]
        p10 = pct(prices, 0.10)
        p50 = pct(prices, 0.50)
        print(
            f"  {item:24s} lots={row[0]:6d} uuid={row[1]:5d}  "
            f"p10={p10 if p10 else '-':>10} p50={p50 if p50 else '-':>10}  "
            f"min={row[2]} max={row[3]}"
        )

    print("\n=== TRADES ===")
    for item in IDS:
        for et in ("buy", "sell", "try-sell"):
            r = db.execute(
                """
                SELECT COUNT(*), AVG(price), MIN(price), MAX(price)
                FROM trade_events
                WHERE item_id=? AND event_type=? AND ts>=?
                """,
                (item, et, SINCE),
            ).fetchone()
            if not r[0]:
                continue
            print(
                f"  {item:24s} {et:8s} n={r[0]:4d}  "
                f"avg={r[1]:.0f} min={r[2]} max={r[3]}"
            )

    print("\n=== BUY vs book p10 (FIFO light, last 200 sells/item) ===")
    for item in IDS:
        book_h = {}
        for hr, price in db.execute(
            """
            WITH latest AS (
              SELECT substr(ts,1,13) hr, uuid, price, MAX(ts) mts
              FROM ah_book_lots WHERE item_id=? AND ts>=? AND price>0
              GROUP BY hr, uuid
            )
            SELECT hr, price FROM latest
            """,
            (item, SINCE),
        ):
            book_h.setdefault(hr, []).append(price)
        p10h = {hr: pct(ps, 0.10) for hr, ps in book_h.items() if len(ps) >= 8}

        buys = list(
            db.execute(
                """
                SELECT ts, price FROM trade_events
                WHERE item_id=? AND event_type='buy' AND ts>=? ORDER BY ts
                """,
                (item, SINCE),
            )
        )
        sells = list(
            db.execute(
                """
                SELECT ts, price FROM trade_events
                WHERE item_id=? AND event_type='sell' AND ts>=? ORDER BY ts
                """,
                (item, SINCE),
            )
        )
        from collections import deque

        q = deque(buys)
        ratios = []
        for st, sp in sells:
            if not q:
                break
            bt, bp = q.popleft()
            p10 = p10h.get(bt[:13])
            if not p10 or bp <= 0:
                continue
            ratios.append(bp / p10)
        if ratios:
            print(
                f"  {item:24s} pairs={len(ratios):3d}  "
                f"med buy/p10={med(ratios):.3f}  "
                f"p10={pct(ratios,0.1):.3f} p90={pct(ratios,0.9):.3f}"
            )
        else:
            print(f"  {item:24s} no paired buys with book")

    print("\nDone. See SHARP56_EXPERIMENT.md for go/kill rules.")


if __name__ == "__main__":
    main()
