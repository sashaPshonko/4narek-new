#!/usr/bin/env bash
# Легкая копия pricing.db для переезда на другой сервер: всё, кроме книги АХ (ah_book_lots).
# Книга — кэш на 3 суток, Go сам пересоздаёт таблицу и наполняет её из сканов.
# Работает с живой базой (VACUUM INTO читает снапшот). Использование: scripts/slim-db.sh [src] [dst]
set -euo pipefail
SRC="${1:-ml_data/pricing.db}"
DST="${2:-ml_data/pricing.slim.db}"
[ -f "$SRC" ] || { echo "нет $SRC"; exit 1; }
rm -f "$DST"
sqlite3 "$SRC" "VACUUM INTO '$DST'"
sqlite3 "$DST" "DROP TABLE IF EXISTS ah_book_lots; DROP TABLE IF EXISTS ah_book_lots_old; VACUUM;"
echo "готово: $DST ($(du -h "$DST" | cut -f1)); было $(du -h "$SRC" | cut -f1)"
sqlite3 "$DST" "select name from sqlite_master where type='table'" | tr '\n' ' '; echo
