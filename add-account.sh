#!/bin/sh
# add-account.sh — 容器内交互式添加 TRAE 账号。
# 用法：docker exec -it <容器名> /app/add-account.sh
set -eu
cd /app 2>/dev/null || true
if [ -f /app/config.json ]; then
    exec /app/tw2api add-account -config /app/config.json
fi
exec /app/tw2api add-account
