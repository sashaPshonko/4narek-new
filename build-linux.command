#!/bin/zsh
# Сборка linux-бинарника на Mac (на VPS gotd OOM при compile).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/4narek-info-linux-amd64 .
ls -lh dist/4narek-info-linux-amd64
echo "Залей: scp dist/4narek-info-linux-amd64 root@212.8.229.76:/path/to/4narek-info"
