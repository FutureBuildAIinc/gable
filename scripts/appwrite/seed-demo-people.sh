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
# Usage: DEMO_PEOPLE_PASSWORD=... seed-demo-people.sh --config FILE [--core URL]
# Needs: bash, curl, jq.
#
# The demo people's password comes from DEMO_PEOPLE_PASSWORD in the
# environment only (FROM-PRINCIPAL item 329): the script holds no default and
# refuses to run without one of at least 12 characters. It is never printed and
# never sits on a command line: jq reads it from its environment
# ($ENV.DEMO_PEOPLE_PASSWORD) and curl takes each body from a pipe
# (--data-binary @-), so /proc/<pid>/cmdline never shows it.
#
# With --core URL (or GABLE_CORE_URL), once the people exist it also links each
# one to its Gable staff row and yard grants, through the core's own routes and
# never a table write: every person signs in at /api/v1/auth/signin (the same
# password), then the demo owner does what the staff screen does, picking the
# person for the staff row (PUT /api/v1/admin/staff/{id}/sign-in-link, ADR 0012
# slice 2) and granting the person's yards (POST /api/v1/users/{sub}/branches,
# the home yard flagged). The staff rows and yards are the core demo seed's, so
# run the core seed first. A re-run reads what is linked and granted and does
# the same again: no duplicate, no error. Without --core the people are created
# and the link step is left for a run that names the core.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG=""
CORE="${GABLE_CORE_URL:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) CONFIG="${2:?--config needs a value}"; shift 2 ;;
    --core) CORE="${2:?--core needs a value}"; shift 2 ;;
    *) echo "seed-demo-people.sh: unknown argument: $1" >&2; exit 64 ;;
  esac
done
[[ -n "$CONFIG" && -f "$CONFIG" ]] || { echo "seed-demo-people.sh: --config FILE (setup.sh's output) is required" >&2; exit 64; }
: "${DEMO_PEOPLE_PASSWORD:=}"
(( ${#DEMO_PEOPLE_PASSWORD} >= 12 )) || { echo "seed-demo-people.sh: DEMO_PEOPLE_PASSWORD (at least 12 characters) must be set in the environment" >&2; exit 64; }
command -v curl >/dev/null || { echo "seed-demo-people.sh: curl not found" >&2; exit 69; }
command -v jq >/dev/null || { echo "seed-demo-people.sh: jq not found" >&2; exit 69; }
export DEMO_PEOPLE_PASSWORD

# Invented, on a reserved domain.
DOMAIN="demo.gable.example"

ENDPOINT="$(jq -r .endpoint "$CONFIG")"
PROJECT_ID="$(jq -r .projectId "$CONFIG")"
TEAM_ID="$(jq -r .teamId "$CONFIG")"
KEY=("X-Appwrite-Project: $PROJECT_ID" "X-Appwrite-Key: $(jq -r .apiKey "$CONFIG")")

RESP="$(mktemp)"
JARS="$(mktemp -d)"
trap 'rm -rf "$RESP" "$JARS"' EXIT

req() {
  local method="$1" path="$2" body="$3"
  local args=(-sS -X "$method" -H "Content-Type: application/json" -o "$RESP" -w "%{http_code}")
  local h
  for h in "${KEY[@]}"; do args+=(-H "$h"); done
  if [[ -n "$body" ]]; then
    # printf is a builtin, so the body (it can carry the password) is on no
    # command line; curl reads it from the pipe.
    printf '%s' "$body" | curl "${args[@]}" --data-binary @- "$ENDPOINT$path"
  else
    curl "${args[@]}" "$ENDPOINT$path"
  fi
}
# Prints the Appwrite response body; the request body (it carries the password)
# must never be echoed here.
die() { echo "seed-demo-people.sh: $1 (status ${2:-?}): $(cat "$RESP" 2>/dev/null)" >&2; exit 1; }

while IFS=$'\t' read -r uid name role; do
  email="$uid@$DOMAIN"
  code="$(req POST /v1/users "$(jq -nc --arg id "$uid" --arg email "$email" --arg name "$name" \
    '{userId:$id,email:$email,password:$ENV.DEMO_PEOPLE_PASSWORD,name:$name}')")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "user create failed for $uid" "$code"

  code="$(req PATCH "/v1/users/$uid/verification" '{"emailVerification":true}')"
  [[ "$code" == 200 ]] || die "email verification failed for $uid" "$code"

  code="$(req POST "/v1/teams/$TEAM_ID/memberships" "$(jq -nc --arg uid "$uid" --arg role "gable-$role" \
    '{userId:$uid,roles:[$role]}')")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "membership create failed for $uid" "$code"
  echo "seed-demo-people.sh: $uid ($name) is a $role"
done < <(jq -r '.[] | [.id, .name, .role] | @tsv' "$HERE/demo-people.json")

[[ -n "$CORE" ]] || { echo "seed-demo-people.sh: no --core (or GABLE_CORE_URL): the people are created, their staff and yard links are not made"; exit 0; }
CORE="${CORE%/}"

# core_req METHOD PATH BODY JAR: one call to the core with the person's session
# cookie and the CSRF header its cookie routes need. The body (it can carry the
# password) goes in on a pipe, never a command line. Prints the status code.
core_req() {
  local method="$1" path="$2" body="$3" jar="$4"
  local args=(-sS -X "$method" -H "Content-Type: application/json" -H "X-Gable-CSRF: 1" -b "$jar" -c "$jar" -o "$RESP" -w "%{http_code}")
  if [[ -n "$body" ]]; then
    printf '%s' "$body" | curl "${args[@]}" --data-binary @- "$CORE$path"
  else
    curl "${args[@]}" "$CORE$path"
  fi
}

# Everyone signs in once: that is what makes the core know the person (the
# roster link lists the people its sessions have seen) and gives their sub.
declare -A SUB
while IFS=$'\t' read -r uid; do
  code="$(core_req POST /api/v1/auth/signin "$(jq -nc --arg e "$uid@$DOMAIN" '{email:$e,password:$ENV.DEMO_PEOPLE_PASSWORD}')" "$JARS/$uid")"
  [[ "$code" == 200 ]] || die "sign in failed for $uid" "$code"
  SUB[$uid]="$(jq -r .sub "$RESP")"
done < <(jq -r '.[] | .id' "$HERE/demo-people.json")

OWNER="$JARS/demo-owner"
get() { code="$(core_req GET "$1" "" "$OWNER")"; [[ "$code" == 200 ]] || die "read $1 failed" "$code"; }

get "/api/v1/admin/staff?limit=200"
STAFF_JSON="$(cat "$RESP")"
get "/api/v1/branches?limit=200"
BRANCH_JSON="$(cat "$RESP")"

while IFS=$'\t' read -r uid home yards; do
  sub="${SUB[$uid]}"
  staff_id="$(jq -r --arg no "$uid" '[.items[] | select(.staff_no == $no)][0].id // empty' <<<"$STAFF_JSON")"
  [[ -n "$staff_id" ]] || die "no staff row with staff number $uid (run the core demo seed first)" 0

  get "/api/v1/admin/staff/sign-in-people"
  linked="$(jq -r --arg sub "$sub" '[.items[] | select(.sub == $sub)][0].linked_staff_id // empty' < "$RESP")"
  if [[ "$linked" == "$staff_id" ]]; then
    echo "seed-demo-people.sh: $uid is already linked to its staff row"
  else
    code="$(core_req PUT "/api/v1/admin/staff/$staff_id/sign-in-link" "$(jq -nc --arg sub "$sub" '{sub:$sub}')" "$OWNER")"
    [[ "$code" == 204 ]] || die "link of $uid to staff $staff_id failed" "$code"
    echo "seed-demo-people.sh: $uid is linked to its staff row"
  fi

  for y in $yards; do
    branch_id="$(jq -r --arg y "$y" '[.items[] | select(.code == $y)][0].id // empty' <<<"$BRANCH_JSON")"
    [[ -n "$branch_id" ]] || die "no yard with code $y" 0
    is_home=false; [[ "$y" == "$home" ]] && is_home=true
    code="$(core_req POST "/api/v1/users/$sub/branches" "$(jq -nc --arg b "$branch_id" --argjson h "$is_home" '{branch_id:$b,is_home:$h}')" "$OWNER")"
    [[ "$code" == 200 || "$code" == 201 || "$code" == 204 ]] || die "grant of $y to $uid failed" "$code"
  done
  echo "seed-demo-people.sh: $uid has its yards ($yards)"
done < <(jq -r '.[] | [.id, .homeYard, (.yards | join(" "))] | @tsv' "$HERE/demo-people.json")
