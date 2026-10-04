#!/usr/bin/env bash
# 本地一键重置 + 启动 + e2e 验证（不依赖 Docker 的开发机用法）。
set -euo pipefail

cd "$(dirname "$0")/.."

export DATABASE_URL="${DATABASE_URL:-postgres://postgres@127.0.0.1:5433/courtbook?sslmode=disable}"
export REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6379}"
export HTTP_ADDR="${HTTP_ADDR:-:18080}"
export ENABLE_TEST_CLOCK=true
export WORKER_AUTOSTART=false
export PGHOST=127.0.0.1
export PGPORT=5433
export PGUSER=postgres

echo "==> 停止旧服务进程"
pkill -f courtbook-server 2>/dev/null || true
sleep 1

echo "==> 重置数据库 courtbook"
psql -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='courtbook' AND pid<>pg_backend_pid();" >/dev/null
dropdb --if-exists courtbook
createdb courtbook

echo "==> 构建并启动服务（$HTTP_ADDR）"
go build -o /tmp/courtbook-server ./cmd/server
setsid /tmp/courtbook-server > /tmp/courtbook.log 2>&1 < /dev/null &

echo "==> 等待健康检查通过"
for i in $(seq 1 30); do
  if curl -sf localhost:18080/healthz >/dev/null; then
    echo "    服务已就绪"
    break
  fi
  sleep 1
done

echo "==> 运行 e2e"
python3 scripts/e2e.py "$@"
