#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# The real stack for the Playwright run: `core serve` (AUTH_MODE=dev) over the
# given Postgres, and web/nginx.conf in nginx serving the built bundles
# (VITE_AUTH_DEV_MODE=true builds) and proxying /api to core.
#
#   DATABASE_URL=<migrated and seeded db> web/scripts/e2e-stack.sh up
#   BASE_URL=http://127.0.0.1:18080 npx playwright test      (from web/)
#   web/scripts/e2e-stack.sh down
#
# State (pid, binary, log, snippet) lives in $E2E_DIR. `down` stops exactly the
# pid and container this script started.
set -euo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
E2E_DIR="${E2E_DIR:-/tmp/gable-e2e}"
CORE_PORT="${CORE_PORT:-8481}"
WEB_PORT="${WEB_PORT:-18080}"
NGINX_NAME="${NGINX_NAME:-gable-e2e-nginx}"

down() {
  if [ -f "$E2E_DIR/core.pid" ]; then
    kill "$(cat "$E2E_DIR/core.pid")" 2>/dev/null || true
    rm -f "$E2E_DIR/core.pid"
  fi
  docker rm -f -v "$NGINX_NAME" >/dev/null 2>&1 || true
}

case "${1:-}" in
up)
  : "${DATABASE_URL:?DATABASE_URL must name a migrated and seeded database}"
  down
  mkdir -p "$E2E_DIR"
  sed "s/__CORE_PORT__/$CORE_PORT/" "$HERE/e2e/gable-api.conf" > "$E2E_DIR/gable-api.conf"

  (cd "$HERE/../core" && go build -o "$E2E_DIR/core" ./cmd/core)
  AUTH_MODE=dev PORT="$CORE_PORT" DATABASE_URL="$DATABASE_URL" RATE_LIMIT_PER_MINUTE=5000 \
    setsid "$E2E_DIR/core" serve >"$E2E_DIR/core.log" 2>&1 &
  echo $! > "$E2E_DIR/core.pid"
  for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$CORE_PORT/health" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS "http://127.0.0.1:$CORE_PORT/health" >/dev/null

  docker run -d --name "$NGINX_NAME" -p "127.0.0.1:$WEB_PORT:8080" \
    --add-host=host.docker.internal:host-gateway \
    -v "$HERE/nginx.conf:/etc/nginx/conf.d/default.conf:ro" \
    -v "$HERE/nginx-headers.conf:/etc/nginx/gable-headers.conf:ro" \
    -v "$E2E_DIR/gable-api.conf:/etc/nginx/gable-api.conf:ro" \
    -v "$HERE/apps/front-door/dist:/usr/share/nginx/html/front-door:ro" \
    -v "$HERE/apps/desk/dist:/usr/share/nginx/html/desk:ro" \
    nginx:1.27-alpine >/dev/null
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:$WEB_PORT/api/v1/apps" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS "http://127.0.0.1:$WEB_PORT/api/v1/apps" >/dev/null
  echo "stack up: http://127.0.0.1:$WEB_PORT (core on $CORE_PORT, pid $(cat "$E2E_DIR/core.pid"))"
  ;;
down)
  down
  echo "stack down"
  ;;
*)
  echo "usage: $0 up|down" >&2
  exit 2
  ;;
esac
