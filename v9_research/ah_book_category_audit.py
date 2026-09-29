#!/usr/bin/env python3
"""Утренний аудит книги АХ: зачары лотов vs item_id (не путаем ли категории).

Usage:
  python3 v9_research/ah_book_category_audit.py
  python3 v9_research/ah_book_category_audit.py --hours 12 --db ml_data/pricing.db
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
from collections import Counter, defaultdict
from datetime import datetime, timedelta, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_DB = ROOT / "ml_data" / "pricing.db"

LORE = re.compile(
    r"купить|цена|продавец|истекает|нажмите|чтобы|white|gray|///|➥|☃|java|истeкaeт|ценa",
    re.I,
)
KNOWN = {
    "sharpness", "smite", "bane_of_arthropods", "fire_aspect", "knockback",
    "looting", "unbreaking", "sweeping_edge", "mending", "fortune",
    "efficiency", "silk_touch", "protection", "fire_protection",
    "blast_protection", "projectile_protection", "thorns", "depth_strider",
    "frost_walker", "feather_falling", "respiration", "aqua_affinity",
    "vampirism", "oxidation", "poison", "detection", "skilled",
}
KEY = {
    "sharpness", "smite", "bane_of_arthropods", "looting", "vampirism",
    "poison", "fire_aspect", "knockback", "unbreaking", "skilled",
    "detection", "oxidation",
}
FOCUS = [
    "pochti-megasword-1.21",
    "megasword-1.21",
    "megasword-яд3-1.21",
    "sword7-1.21",
    "sword-sharp5-1.21",
    "sword-sharp6-1.21",
]


def clean(ench_json: str) -> list[tuple[str, int]]:
    try:
        arr = json.loads(ench_json or "[]")
    except Exception:
        return []
    out: list[tuple[str, int]] = []
    for e in arr:
        name = (e.get("name") or "").strip()
        lvl = int(e.get("lvl") or 0)
        short = name.split(":")[-1].lower()
        if LORE.search(name):
            continue
        if name.startswith("minecraft:") or short in KNOWN:
            out.append((short, lvl))
    return sorted(out)


def fingerprint(pairs: list[tuple[str, int]]) -> tuple[tuple[str, int], ...]:
    return tuple((a, b) for a, b in pairs if a in KEY)


def fmt_fp(fp: tuple[tuple[str, int], ...]) -> str:
    return ", ".join(f"{a}{b}" for a, b in fp) or "(пусто)"


def multi_bottom(db: sqlite3.Connection, item: str, since: str, min_lots: int = 3) -> tuple[int | None, int]:
    by: dict[str, dict] = defaultdict(lambda: {"n": 0, "min": 10**18})
    for seller, price in db.execute(
        """SELECT lower(trim(seller)), price FROM ah_book_lots
           WHERE item_id=? AND ts>=? AND price>0 AND trim(coalesce(seller,''))!=''""",
        (item, since),
    ):
        st = by[seller]
        st["n"] += 1
        if price < st["min"]:
            st["min"] = price
    mins = sorted(st["min"] for st in by.values() if st["n"] >= min_lots)
    if not mins:
        return None, 0
    return mins[0], len(mins)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", type=Path, default=DEFAULT_DB)
    ap.add_argument("--hours", type=float, default=12)
    ap.add_argument("--top", type=int, default=6)
    args = ap.parse_args()

    db = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    mx = db.execute("SELECT max(ts) FROM ah_book_lots").fetchone()[0]
    if not mx:
        print("книга пуста")
        return
    t1 = datetime.fromisoformat(mx.replace("Z", "+00:00"))
    since = (t1 - timedelta(hours=args.hours)).astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    print(f"книга max_ts={mx}  окно={args.hours}h  since={since}\n")

    print("=== дно мульти-селлеров (≥3 лота) — якорь дорогих ===")
    for item in FOCUS[:3]:
        bottom, n = multi_bottom(db, item, since)
        if bottom is None:
            print(f"  {item}: нет мульти-селлеров")
        else:
            print(f"  {item}: дно={bottom/1e6:.2f}M  multi={n}")
    print()

    print("=== топ отпечатков зачар по item_id (после фильтра лора) ===")
    for item in FOCUS:
        fps: Counter[tuple] = Counter()
        n = empty = 0
        for (ej,) in db.execute(
            "SELECT enchants_json FROM ah_book_lots WHERE item_id=? AND ts>=?",
            (item, since),
        ):
            n += 1
            c = clean(ej)
            if not c:
                empty += 1
                continue
            fps[fingerprint(c)] += 1
        print(f"\n{item}  lots={n}  без_чистых_зачар={empty}")
        for fp, cnt in fps.most_common(args.top):
            print(f"  {cnt:5d}  {fmt_fp(fp)}")

    # грубый сигнал: poison/detection lvl vs ожидаемый SKU
    print("\n=== грубая проверка poison/detection (если категория врала — увидишь чужой lvl) ===")
    expect = {
        "pochti-megasword-1.21": (1, 1),
        "megasword-1.21": (2, 2),
        "megasword-яд3-1.21": (3, 3),
    }
    for item, (ep, ed) in expect.items():
        odd = 0
        total = 0
        for (ej,) in db.execute(
            "SELECT enchants_json FROM ah_book_lots WHERE item_id=? AND ts>=?",
            (item, since),
        ):
            c = dict(clean(ej))
            if "poison" not in c and "detection" not in c:
                continue
            total += 1
            if c.get("poison") not in (None, ep) or c.get("detection") not in (None, ed):
                odd += 1
        print(f"  {item}: с poison/detection={total}  чужой_lvl={odd}")


if __name__ == "__main__":
    main()
