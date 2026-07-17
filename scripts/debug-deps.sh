#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "$ROOT/tmp"

if [[ -f "$ROOT/tmp/debug-port-forwards.pid" ]]; then
  while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$ROOT/tmp/debug-port-forwards.pid"
fi

: > "$ROOT/tmp/debug-port-forwards.pid"
nohup kubectl -n kubesqueeze-system port-forward service/postgres 5432:5432 >"$ROOT/tmp/postgres-port-forward.log" 2>&1 &
echo $! >> "$ROOT/tmp/debug-port-forwards.pid"
nohup kubectl -n kubesqueeze-system port-forward service/prometheus 9090:9090 >"$ROOT/tmp/prometheus-port-forward.log" 2>&1 &
echo $! >> "$ROOT/tmp/debug-port-forwards.pid"

sleep 2
echo "Postgres:   postgres://kubesqueeze:kubesqueeze@127.0.0.1:5432/kubesqueeze?sslmode=disable"
echo "Prometheus: http://127.0.0.1:9090"
echo "PIDs and logs are in $ROOT/tmp"
