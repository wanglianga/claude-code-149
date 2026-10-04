#!/usr/bin/env bash
# 无 Docker 环境下的本地端到端验证：
# 初始化/复用用户态 PostgreSQL(5433) + 本机 Redis(6379) -> 起服务 -> 跑 scripts/e2e_test.sh
# 适用：Debian/Ubuntu 已安装 postgresql-15 与 redis-server，但当前用户无系统库管理权限的环境。
set -euo pipefail
cd "$(dirname "$0")/.."

PGBIN="${PGBIN:-/usr/lib/postgresql/15/bin}"
PGDATA="${PGDATA:-$HOME/pgdata}"
PGPORT=5433
PGUSER=appuser
PGDB=badminton
export PATH="$PGBIN:$PATH"

# 1) Redis
redis-cli ping >/dev/null 2>&1 || { echo "需要本机 Redis（redis-server）可用"; exit 1; }

# 2) 用户态 PostgreSQL 集群
if ! pg_isready -h 127.0.0.1 -p "$PGPORT" >/dev/null 2>&1; then
  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    initdb -D "$PGDATA" -U "$PGUSER" --auth-local=trust --auth-host=trust -E UTF8
  fi
  pg_ctl -D "$PGDATA" -o "-p $PGPORT -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp" -l "$HOME/pg.log" start
  for _ in $(seq 1 30); do pg_isready -h 127.0.0.1 -p "$PGPORT" >/dev/null 2>&1 && break; sleep 1; done
fi

dropdb -h 127.0.0.1 -p "$PGPORT" -U "$PGUSER" --if-exists "$PGDB"
createdb -h 127.0.0.1 -p "$PGPORT" -U "$PGUSER" "$PGDB"
redis-cli flushall >/dev/null

# 3) 起服务（关闭自动后台任务，由 e2e 脚本显式触发）
go build -mod=vendor -o /tmp/gymsrv-local ./cmd/server
for pid in $(pgrep -f '/tmp/gymsrv-local' || true); do kill "$pid" 2>/dev/null || true; done
ALLOW_TEST_CLOCK=1 DISABLE_BG_JOBS=1 \
  DATABASE_URL="postgres://$PGUSER@127.0.0.1:$PGPORT/$PGDB?sslmode=disable" \
  REDIS_ADDR=127.0.0.1:6379 HTTP_ADDR=:18080 /tmp/gymsrv-local >/tmp/gymsrv-local.log 2>&1 &
APP_PID=$!
trap 'kill $APP_PID 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:18080/api/v1/health >/dev/null 2>&1 && break; sleep 1; done

# 4) 跑验证
BASE=http://127.0.0.1:18080 bash scripts/e2e_test.sh
