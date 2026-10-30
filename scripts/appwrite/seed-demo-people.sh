#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# ADR 0012 slice 7: seed the demo people (the nine invented staff of the front
# door's persona cards, listed in demo-people.json) as verified users of the
# local Appwrite and confirmed members of its staff team, each with its
# gable-<role> team role, so the walks sign in for real. Run it after
# setup.sh, which writes the config file this reads (endpoint, project, team,
# server key). Idempotent: a user or membership that exists is left alone.
#
# Usage: DEMO_PEOPLE_PASSWORD=... seed-demo-people.sh --config FILE
# Needs: bash, curl, jq.
#
# The demo people's password comes from DEMO_PEOPLE_PASSWORD in the
# environment only (FROM-PRINCIPAL item 329): the script holds no default and
# refuses to run without one of at least 12 characters. It is never printed.
#
# What it does not do: it does not link a user to its Gable staff row or to the
# yard grants in user_locations. Those key on the Appwrite user id and wait on
# ADR 0012 slice 2 (the core sessions and the roster link).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) CONFIG="${2:?--config needs a value}"; shift 2 ;;
    *) echo "seed-demo-people.sh: unknown argument: $1" >&2; exit 64 ;;
  esac
done
[[ -n "$CONFIG" && -f "$CONFIG" ]] || { echo "seed-demo-people.sh: --config FILE (setup.sh's output) is required" >&2; exit 64; }
PASSWORD="${DEMO_PEOPLE_PASSWORD:-}"
(( ${#PASSWORD} >= 12 )) || { echo "seed-demo-people.sh: DEMO_PEOPLE_PASSWORD (at least 12 characters) must be set in the environment" >&2; exit 64; }
command -v curl >/dev/null || { echo "seed-demo-people.sh: curl not found" >&2; exit 69; }
command -v jq >/dev/null || { echo "seed-demo-people.sh: jq not found" >&2; exit 69; }

# Invented, on a reserved domain.
DOMAIN="demo.gable.example"

ENDPOINT="$(jq -r .endpoint "$CONFIG")"
PROJECT_ID="$(jq -r .projectId "$CONFIG")"
TEAM_ID="$(jq -r .teamId "$CONFIG")"
KEY=("X-Appwrite-Project: $PROJECT_ID" "X-Appwrite-Key: $(jq -r .apiKey "$CONFIG")")

RESP="$(mktemp)"
trap 'rm -f "$RESP"' EXIT

req() {
  local method="$1" path="$2" body="$3"
  local args=(-sS -X "$method" -H "Content-Type: application/json" -o "$RESP" -w "%{http_code}")
  local h
  for h in "${KEY[@]}"; do args+=(-H "$h"); done
  [[ -n "$body" ]] && args+=(-d "$body")
  curl "${args[@]}" "$ENDPOINT$path"
}
die() { echo "seed-demo-people.sh: $1 (status ${2:-?}): $(cat "$RESP" 2>/dev/null)" >&2; exit 1; }

while IFS=$'\t' read -r uid name role; do
  email="$uid@$DOMAIN"
  code="$(req POST /v1/users "$(jq -nc --arg id "$uid" --arg email "$email" --arg pw "$PASSWORD" --arg name "$name" \
    '{userId:$id,email:$email,password:$pw,name:$name}')")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "user create failed for $uid" "$code"

  code="$(req PATCH "/v1/users/$uid/verification" '{"emailVerification":true}')"
  [[ "$code" == 200 ]] || die "email verification failed for $uid" "$code"

  code="$(req POST "/v1/teams/$TEAM_ID/memberships" "$(jq -nc --arg uid "$uid" --arg role "gable-$role" \
    '{userId:$uid,roles:[$role]}')")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "membership create failed for $uid" "$code"
  echo "seed-demo-people.sh: $uid ($name) is a $role"
done < <(jq -r '.[] | [.id, .name, .role] | @tsv' "$HERE/demo-people.json")
