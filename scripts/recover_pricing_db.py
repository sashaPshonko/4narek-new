#!/usr/bin/env python3
"""Спасти читаемые строки из битого pricing.db → pricing.clean.db

На VPS (Go остановлен):
  cd ~/4narek-new/ml_data
  python3 ../scripts/recover_pricing_db.py
  # или: python3 recover_pricing_db.py  (если скопировал сюда)
"""

from __future__ import annotations

import os
import sqlite3
import sys
from pathlib import Path

SRC = Path(sys.argv[1] if len(sys.argv) > 1 else "pricing.db")
DST = Path(sys.argv[2] if len(sys.argv) > 2 else "pricing.clean.db")

TABLES = [
    "trade_events",
    "ml_decisions",
    "ml_shadow",
    "capital_cycles",
    "stock_snapshots",
    "server_price_events",
]


def connect_ro(path: Path) -> sqlite3.Connection:
    uri = f"file:{path.resolve()}?mode=ro"
    con = sqlite3.connect(uri, uri=True)
    con.execute("PRAGMA query_only=ON")
    return con


def main() -> None:
    if not SRC.exists():
        print(f"нет файла {SRC}", file=sys.stderr)
        sys.exit(1)

    if DST.exists():
        DST.unlink()

    src = connect_ro(SRC)
    dst = sqlite3.connect(DST)
    dst.execute("PRAGMA journal_mode=WAL")
    dst.execute("PRAGMA synchronous=NORMAL")

    # схема
    try:
        rows = src.execute(
            "SELECT sql FROM sqlite_master WHERE type IN ('table','index') "
            "AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY type DESC, name"
        ).fetchall()
    except sqlite3.Error as e:
        print(f"sqlite_master не читается: {e}", file=sys.stderr)
        rows = []

    for (sql,) in rows:
        try:
            dst.execute(sql)
        except sqlite3.Error as e:
            print(f"schema skip: {e}\n  {sql[:120]}...")

    dst.commit()

    # какие таблицы реально есть в src
    try:
        present = {
            r[0]
            for r in src.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
    except sqlite3.Error:
        present = set(TABLES)

    for table in TABLES:
        if table not in present:
            print(f"=== {table}: нет в src ===")
            continue

        # убедимся что таблица есть в dst
        has = dst.execute(
            "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (table,)
        ).fetchone()
        if not has:
            print(f"=== {table}: нет схемы — пробуем CREATE AS пустой через 0 rows ===")
            try:
                cols = src.execute(f"PRAGMA table_info({table})").fetchall()
                if not cols:
                    print(f"  PRAGMA table_info fail")
                    continue
                coldefs = ", ".join(f'"{c[1]}" {c[2] or "TEXT"}' for c in cols)
                dst.execute(f'CREATE TABLE IF NOT EXISTS "{table}" ({coldefs})')
                dst.commit()
            except sqlite3.Error as e:
                print(f"  create fail: {e}")
                continue

        cols = [c[1] for c in dst.execute(f"PRAGMA table_info({table})").fetchall()]
        if not cols:
            print(f"=== {table}: нет колонок ===")
            continue
        col_list = ", ".join(f'"{c}"' for c in cols)
        placeholders = ", ".join("?" * len(cols))
        insert_sql = f'INSERT OR IGNORE INTO "{table}" ({col_list}) VALUES ({placeholders})'

        copied = 0
        skipped = 0

        # 1) полный SELECT — если повезёт
        try:
            cur = src.execute(f'SELECT {col_list} FROM "{table}"')
            batch = []
            while True:
                try:
                    row = cur.fetchone()
                except sqlite3.Error as e:
                    print(f"  fetch break after {copied}: {e}")
                    break
                if row is None:
                    break
                batch.append(row)
                if len(batch) >= 200:
                    dst.executemany(insert_sql, batch)
                    copied += len(batch)
                    batch.clear()
            if batch:
                dst.executemany(insert_sql, batch)
                copied += len(batch)
            dst.commit()
            print(f"=== {table}: full scan ok, copied={copied} ===")
            continue
        except sqlite3.Error as e:
            print(f"=== {table}: full scan fail ({e}), идём по id ===")

        # 2) по id
        try:
            max_id = src.execute(f'SELECT MAX(id) FROM "{table}"').fetchone()[0]
        except sqlite3.Error as e:
            print(f"  MAX(id) fail: {e}")
            max_id = None

        if max_id is None:
            # без id — пробуем LIMIT/OFFSET кусками пока не упрёмся
            offset = 0
            while True:
                try:
                    chunk = src.execute(
                        f'SELECT {col_list} FROM "{table}" LIMIT 50 OFFSET {offset}'
                    ).fetchall()
                except sqlite3.Error as e:
                    print(f"  offset {offset} fail: {e}")
                    break
                if not chunk:
                    break
                dst.executemany(insert_sql, chunk)
                copied += len(chunk)
                offset += len(chunk)
                if offset % 500 == 0:
                    print(f"  … {copied}")
                    dst.commit()
            dst.commit()
            print(f"=== {table}: offset copy={copied} ===")
            continue

        print(f"  MAX(id)={max_id}")
        for i in range(1, int(max_id) + 1):
            try:
                row = src.execute(
                    f'SELECT {col_list} FROM "{table}" WHERE id=?', (i,)
                ).fetchone()
            except sqlite3.Error:
                skipped += 1
                continue
            if row is None:
                continue
            try:
                dst.execute(insert_sql, row)
                copied += 1
            except sqlite3.Error:
                skipped += 1
            if i % 500 == 0:
                dst.commit()
                print(f"  … id={i} copied={copied} skipped={skipped}")
        dst.commit()
        print(f"=== {table}: by-id copied={copied} skipped={skipped} ===")

    # индексы на всякий
    for sql in [
        "CREATE INDEX IF NOT EXISTS idx_capital_cycles_ts ON capital_cycles(ts)",
        "CREATE INDEX IF NOT EXISTS idx_capital_cycles_item ON capital_cycles(item_id)",
        "CREATE INDEX IF NOT EXISTS idx_stock_snapshots_ts ON stock_snapshots(ts)",
        "CREATE INDEX IF NOT EXISTS idx_stock_snapshots_item ON stock_snapshots(item_id)",
        "CREATE INDEX IF NOT EXISTS idx_trade_events_ts ON trade_events(ts)",
        "CREATE INDEX IF NOT EXISTS idx_ml_decisions_ts ON ml_decisions(logged_ts)",
    ]:
        try:
            dst.execute(sql)
        except sqlite3.Error:
            pass
    dst.commit()

    print("\n--- clean ---")
    try:
        print("quick_check:", dst.execute("PRAGMA quick_check").fetchone()[0])
    except sqlite3.Error as e:
        print("quick_check err:", e)
    for t in TABLES:
        try:
            n = dst.execute(f'SELECT COUNT(*) FROM "{t}"').fetchone()[0]
            print(f"  {t}: {n}")
        except sqlite3.Error:
            print(f"  {t}: (нет)")

    src.close()
    dst.close()
    print(f"\nготово: {DST.resolve()}")
    print("Дальше:")
    print("  mv pricing.db pricing.db.oldmalformed")
    print("  mv pricing.clean.db pricing.db")
    print("  rm -f pricing.db-wal pricing.db-shm")


if __name__ == "__main__":
    main()
