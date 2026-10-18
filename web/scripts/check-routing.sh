#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Proves web/nginx.conf routes the four real built bundles correctly: `/` is
# the front door, /shell/ is the work shell, /field/ is the field shell, every
# desk route is the desk through the SPA fallback, and the assets load. Runs
# the given web image (default gable-web:routing-check) or, with BUNDLES=local,
# nginx:1.27-alpine over the local dist folders.
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
    -v "$HERE/nginx-headers-frameable.conf:/etc/nginx/gable-headers-frameable.conf:ro" \
    -v "$HERE/apps/front-door/dist:/usr/share/nginx/html/front-door:ro" \
    -v "$HERE/apps/desk/dist:/usr/share/nginx/html/desk:ro" \
    -v "$HERE/apps/work-shell/dist:/usr/share/nginx/html/work-shell:ro" \
    -v "$HERE/apps/field-shell/dist:/usr/share/nginx/html/field-shell:ro" \
    nginx:1.27-alpine >/dev/null
else
  docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" "$IMAGE" >/dev/null
fi

BASE="http://127.0.0.1:$PORT"

# Wait for a real page, not just an open port: the front door must answer 200.
ready=""
for _ in $(seq 1 30); do
  if [ "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/" 2>/dev/null)" = 200 ]; then ready=yes; break; fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "FAIL the web image never answered 200 on $BASE/ within thirty seconds"
  exit 1
fi

# Every page and every header set is read ONCE, into a variable, and the checks
# test the variable. Never pipe curl into `grep -q`: grep exits on its first
# match and closes the pipe, curl's next write then fails (exit 23), and
# pipefail turns that into a failed check, now and then, by timing alone.
fail=0
page() { # path: sets PAGE_CODE and PAGE_BODY from one request
  local out
  out="$(curl -sS -w '\n%{http_code}' "$BASE$1" 2>/dev/null || true)"
  PAGE_CODE="${out##*$'\n'}"
  PAGE_BODY="${out%$'\n'*}"
}
headers() { curl -sSI "$BASE$1" 2>/dev/null || true; }
has() { grep -qi -e "$1" <<<"$2"; } # pattern, text (a here string, no pipe)
check() { # description, condition result
  if [ "$2" = "ok" ]; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi
}

page /;     root_code="$PAGE_CODE"; root_body="$PAGE_BODY"
page /home; home_code="$PAGE_CODE"; home_body="$PAGE_BODY"
door_asset="$(grep -o '/door/[^"]*\.js' <<<"$root_body" | head -1 || true)"
desk_asset="$(grep -o '/assets/[^"]*\.js' <<<"$home_body" | head -1 || true)"

check "/ is the front door (title)" \
  "$(has gable "$root_body" && has '/door/' "$root_body" && echo ok)"
check "/ is not the desk (no /assets/ script)" "$(has '/assets/' "$root_body" || echo ok)"
check "/ status 200" "$([ "$root_code" = 200 ] && echo ok)"
check "/home is the desk" "$(has '/assets/' "$home_body" && echo ok)"
for p in /home /quotes/7 /orders/7 /invoices/7 /inventory/7 /accounts/7 /portal/login /driver /yard /pos /dashboard; do
  page "$p"
  check "$p serves the desk shell (200, desk assets)" \
    "$([ "$PAGE_CODE" = 200 ] && has '/assets/' "$PAGE_BODY" && ! has '/door/' "$PAGE_BODY" && echo ok)"
done
asset_code() { page "$1"; echo "$PAGE_CODE"; }
check "front door asset loads ($door_asset)" "$([ -n "$door_asset" ] && [ "$(asset_code "$door_asset")" = 200 ] && echo ok)"
check "desk asset loads ($desk_asset)" "$([ -n "$desk_asset" ] && [ "$(asset_code "$desk_asset")" = 200 ] && echo ok)"
check "a missing door asset is 404, not the desk" "$([ "$(asset_code /door/nope.js)" = 404 ] && echo ok)"
check "a missing desk asset is 404, not index.html" "$([ "$(asset_code /assets/nope.js)" = 404 ] && echo ok)"
page /api/v1/apps
check "/api/v1/apps is 404 JSON when no API upstream is mounted, not the desk" \
  "$([ "$PAGE_CODE" = 404 ] && ! has '<html' "$PAGE_BODY" && echo ok)"
root_headers="$(headers /)"
csp="$(grep -i '^content-security-policy' <<<"$root_headers" || true)"
check "CSP connect-src is same origin only" \
  "$(has "connect-src 'self';" "$csp" && ! has localhost "$root_headers" && echo ok)"
# The framing split: the door, the shell hosts, the portal (sign in and
# checkout live there) and the static assets stay unframeable; the desk's own
# pages are frameable by this origin only, for the work and field shells.
for p in / /shell/ /field/ /portal /portal/login "$desk_asset" "$door_asset"; do
  check "security headers deny framing on $p" "$(has '^x-frame-options: DENY' "$(headers "$p")" && echo ok)"
done
for p in /home /quotes/7; do
  check "security headers allow same origin framing on $p" "$(has '^x-frame-options: SAMEORIGIN' "$(headers "$p")" && echo ok)"
done

exit "$fail"
