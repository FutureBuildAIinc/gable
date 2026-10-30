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
# Appwrite mode (ADR 0012 slice 3): with APPWRITE_SEED_JSON naming the file
# scripts/appwrite/setup.sh wrote, the core starts WITHOUT AUTH_MODE=dev, with
# the Appwrite settings read from that file into its environment (never
# printed; the portal signing secret a non dev core requires is an obvious
# test pattern), for the staff sign in specs (APPWRITE_E2E=1). The bundles must
# then be built without VITE_AUTH_DEV_MODE.
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
  # The large cash payer capture refuses to store a taxpayer id without a
  # vault key, so the stack runs with an obvious test pattern unless the
  # caller gives one.
  if [ -n "${APPWRITE_SEED_JSON:-}" ]; then
    command -v jq >/dev/null || { echo "e2e-stack.sh: jq not found" >&2; exit 69; }
    PROBE_PATH=/
    env -u AUTH_MODE \
      APPWRITE_ENDPOINT="$(jq -r .endpoint "$APPWRITE_SEED_JSON")" \
      APPWRITE_PROJECT_ID="$(jq -r .projectId "$APPWRITE_SEED_JSON")" \
      APPWRITE_TEAM_ID="$(jq -r .teamId "$APPWRITE_SEED_JSON")" \
      APPWRITE_API_KEY="$(jq -r .apiKey "$APPWRITE_SEED_JSON")" \
      GABLE_PUBLIC_URL="http://127.0.0.1:$WEB_PORT" CORS_ORIGINS="http://127.0.0.1:$WEB_PORT" \
      PORT="$CORE_PORT" DATABASE_URL="$DATABASE_URL" RATE_LIMIT_PER_MINUTE=5000 \
      PORTAL_JWT_SECRET="${PORTAL_JWT_SECRET:-e2e-portal-test-pattern-not-a-secret-0123456789}" \
      PAYMENT_VAULT_KEY="${PAYMENT_VAULT_KEY:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}" \
      setsid "$E2E_DIR/core" serve >"$E2E_DIR/core.log" 2>&1 &
  else
    PROBE_PATH=/api/v1/apps
    AUTH_MODE=dev PORT="$CORE_PORT" DATABASE_URL="$DATABASE_URL" RATE_LIMIT_PER_MINUTE=5000 \
      POS_SCALE_SIMULATOR=1 \
      PAYMENT_VAULT_KEY="${PAYMENT_VAULT_KEY:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}" \
      setsid "$E2E_DIR/core" serve >"$E2E_DIR/core.log" 2>&1 &
  fi
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
    -v "$HERE/nginx-headers-frameable.conf:/etc/nginx/gable-headers-frameable.conf:ro" \
    -v "$E2E_DIR/gable-api.conf:/etc/nginx/gable-api.conf:ro" \
    -v "$HERE/apps/front-door/dist:/usr/share/nginx/html/front-door:ro" \
    -v "$HERE/apps/desk/dist:/usr/share/nginx/html/desk:ro" \
    -v "$HERE/apps/work-shell/dist:/usr/share/nginx/html/work-shell:ro" \
    -v "$HERE/apps/field-shell/dist:/usr/share/nginx/html/field-shell:ro" \
    nginx:1.27-alpine >/dev/null
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:$WEB_PORT$PROBE_PATH" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS "http://127.0.0.1:$WEB_PORT$PROBE_PATH" >/dev/null
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
