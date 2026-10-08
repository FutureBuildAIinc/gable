#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Proves web/nginx.conf routes the two real built bundles correctly: `/` is the
# front door, every desk route is the desk through the SPA fallback, and the
# assets of both load. Runs the given web image (default gable-web:routing-check)
# or, with BUNDLES=local, nginx:1.27-alpine over the local dist folders.
#
#   docker build -f web/Dockerfile -t gable-web:routing-check .
#   web/scripts/check-routing.sh
set -euo pipefail

NAME="${CHECK_CONTAINER:-gable-web-routing-check}"
PORT="${CHECK_PORT:-18080}"
IMAGE="${1:-gable-web:routing-check}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

cleanup() { docker rm -f -v "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

if [ "${BUNDLES:-image}" = "local" ]; then
  docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" \
    -v "$HERE/nginx.conf:/etc/nginx/conf.d/default.conf:ro" \
    -v "$HERE/nginx-headers.conf:/etc/nginx/gable-headers.conf:ro" \
    -v "$HERE/apps/front-door/dist:/usr/share/nginx/html/front-door:ro" \
    -v "$HERE/apps/desk/dist:/usr/share/nginx/html/desk:ro" \
    nginx:1.27-alpine >/dev/null
else
  docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" "$IMAGE" >/dev/null
fi

BASE="http://127.0.0.1:$PORT"
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "$BASE/" 2>/dev/null && break
  sleep 1
done

fail=0
body() { curl -sS "$BASE$1"; }
code() { curl -sS -o /dev/null -w '%{http_code}' "$BASE$1"; }
check() { # description, condition result
  if [ "$2" = "ok" ]; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi
}

door_asset="$(body / | grep -o '/door/[^"]*\.js' | head -1)"
desk_html="$(body /home)"
desk_asset="$(echo "$desk_html" | grep -o '/assets/[^"]*\.js' | head -1)"

check "/ is the front door (title)" "$(body / | grep -qi 'gable' && body / | grep -q '/door/' && echo ok)"
check "/ is not the desk (no /assets/ script)" "$(body / | grep -q '/assets/' || echo ok)"
check "/ status 200" "$([ "$(code /)" = 200 ] && echo ok)"
check "/home is the desk" "$(echo "$desk_html" | grep -q '/assets/' && echo ok)"
for p in /home /quotes/7 /orders/7 /invoices/7 /inventory/7 /accounts/7 /portal/login /driver /yard /pos /dashboard; do
  check "$p serves the desk shell (200, desk assets)" \
    "$([ "$(code $p)" = 200 ] && body $p | grep -q '/assets/' && ! body $p | grep -q '/door/' && echo ok)"
done
check "front door asset loads ($door_asset)" "$([ -n "$door_asset" ] && [ "$(code "$door_asset")" = 200 ] && echo ok)"
check "desk asset loads ($desk_asset)" "$([ -n "$desk_asset" ] && [ "$(code "$desk_asset")" = 200 ] && echo ok)"
check "a missing door asset is 404, not the desk" "$([ "$(code /door/nope.js)" = 404 ] && echo ok)"
check "a missing desk asset is 404, not index.html" "$([ "$(code /assets/nope.js)" = 404 ] && echo ok)"
check "/api/v1/apps is 404 JSON when no API upstream is mounted, not the desk" \
  "$([ "$(code /api/v1/apps)" = 404 ] && ! body /api/v1/apps | grep -qi '<html' && echo ok)"
check "CSP connect-src is same origin only" \
  "$(curl -sSI "$BASE/" | grep -i '^content-security-policy' | grep -q "connect-src 'self';" && ! curl -sSI "$BASE/" | grep -qi 'localhost' && echo ok)"
for p in / /home /quotes/7 "$desk_asset" "$door_asset"; do
  check "security headers on $p" "$(curl -sSI "$BASE$p" | grep -qi '^x-frame-options: DENY' && echo ok)"
done

exit "$fail"
