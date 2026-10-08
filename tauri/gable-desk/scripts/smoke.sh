#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Smoke test for Gable Desk (CI step "Smoke test"). Starts the built app under
# a virtual display and a throwaway session bus, with the bundled front door
# (GABLE_DESK_URL unset), and requires that the webview finished loading the
# page, reported the front door's document title, and the app exited cleanly.
#
# Usage: scripts/smoke.sh [path-to-binary]
# Needs xvfb-run and dbus-run-session. Never run it against a real desk URL.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="${1:-$here/target/debug/gable-desk}"
[ -x "$bin" ] || { echo "smoke: FAIL: no binary at $bin (run cargo tauri build --debug first)" >&2; exit 1; }

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

# A software-only webview: the runner has no GPU, and the DMABUF renderer
# needs one.
status=0
env -u GABLE_DESK_URL \
  GABLE_DESK_SMOKE=1 \
  WEBKIT_DISABLE_DMABUF_RENDERER=1 \
  WEBKIT_DISABLE_COMPOSITING_MODE=1 \
  timeout -k 5 120 \
  dbus-run-session -- \
  xvfb-run -a -s "-screen 0 1280x800x24" "$bin" >"$log" 2>&1 || status=$?

cat "$log"

[ "$status" -eq 0 ] || { echo "smoke: FAIL: the app exited with status $status (124 means it timed out)" >&2; exit 1; }
grep -q '^gable-desk: page loaded url=' "$log" || { echo "smoke: FAIL: no page load line" >&2; exit 1; }
grep -qF 'gable-desk: document title=GableLBM | Front Door' "$log" || { echo "smoke: FAIL: the front door's title never arrived" >&2; exit 1; }
grep -q '^gable-desk: smoke ok$' "$log" || { echo "smoke: FAIL: no clean exit line" >&2; exit 1; }
echo "smoke: OK"
