#!/usr/bin/env bash
# Сжатие pricing.db на Go VPS.
# Go должен быть остановлен с панели.
#
# 1) Переписать старые снимки/payloads компактнее + VACUUM:
#      cd ~/4narek-new && ./4narek-info -compact-db
# 2) Только VACUUM (если compact уже отработал в фоне):
#      cd ~/4narek-new && ./4narek-info -vacuum-db
# Или вручную через sqlite3 (Go остановлен):
#   cd ~/4narek-new/ml_data
#   sqlite3 pricing.db "VACUUM INTO 'pricing.vacuum.db'"
#   mv pricing.db pricing.db.bak && mv pricing.vacuum.db pricing.db
set -euo pipefail
cd "$(dirname "$0")/.."
BIN="${BIN:-./4narek-info}"
MODE="${1:-compact}"
if [[ -x "$BIN" ]]; then
  if [[ "$MODE" == "vacuum" ]]; then
    exec "$BIN" -vacuum-db
  fi
  exec "$BIN" -compact-db
fi
if command -v sqlite3 >/dev/null; then
  DB="${DB:-ml_data/pricing.db}"
  TMP="${DB}.vacuum-tmp.db"
  BAK="${DB}.pre-vacuum.bak"
  sqlite3 "$DB" "VACUUM INTO '$(printf '%s' "$TMP" | sed "s/'/''/g")'"
  mv "$DB" "$BAK"
  mv "$TMP" "$DB"
  echo "✅ vacuum ok: $DB (backup $BAK)"
  ls -lh "$DB" "$BAK"
  exit 0
fi
echo "Нет ./4narek-info и sqlite3 — собери бинарь или установи sqlite3"
exit 1
