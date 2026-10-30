#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# ADR 0012 slice 1: seed the local Appwrite proof stack (scripts/appwrite/
# docker-compose.yaml) through the Appwrite HTTP API and write the resulting
# ids and the freshly generated server key as JSON. Idempotent: every create
# tolerates "already exists", and the API key is deleted and recreated so a
# rerun against a kept stack still yields a usable key. The key exists only
# for the run and lands in the caller-chosen --out file; never commit it.
#
# Usage: setup.sh [--endpoint http://127.0.0.1:8580] --out FILE
# Needs: bash, curl, jq.

set -euo pipefail

ENDPOINT="http://127.0.0.1:8580"
OUT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --endpoint) ENDPOINT="${2:?--endpoint needs a value}"; shift 2 ;;
    --out) OUT="${2:?--out needs a value}"; shift 2 ;;
    *) echo "setup.sh: unknown argument: $1" >&2; exit 64 ;;
  esac
done
[[ -n "$OUT" ]] || { echo "setup.sh: --out FILE is required" >&2; exit 64; }
command -v curl >/dev/null || { echo "setup.sh: curl not found" >&2; exit 69; }
command -v jq >/dev/null || { echo "setup.sh: jq not found" >&2; exit 69; }
command -v openssl >/dev/null || { echo "setup.sh: openssl not found" >&2; exit 69; }

# The shared password and the server key id come from the environment
# (APPWRITE_PROOF_CONSOLE_PASSWORD, APPWRITE_PROOF_KEY_ID); when unset each is
# generated for this run. Neither is ever printed; both land only in the --out
# file (mode 600). The key id stays a valid Appwrite id: at most 36 of a-z A-Z
# 0-9 . _ -, not starting with a special character. Against a kept stack, pass
# the same password on a rerun, because the users already exist with it.
PASSWORD="${APPWRITE_PROOF_CONSOLE_PASSWORD:-$(openssl rand -hex 16)}"
# Invented, on a reserved domain.
CONSOLE_EMAIL="console-admin@s9proof.example"
PROJECT_ID="s9proof"
ORG_TEAM_ID="s9proof-org"
STAFF_TEAM_ID="s9proof-staff"
KEY_ID="${APPWRITE_PROOF_KEY_ID:-s9proof-$(openssl rand -hex 8)}"
ROLES=(owner admin sales finance purchasing warehouse cashier driver)
NON_MEMBER_ID="s9proof-nomember"

JAR="$(mktemp)"
RESP="$(mktemp)"
trap 'rm -f "$JAR" "$RESP"' EXIT

# req METHOD PATH [JSON_BODY] [EXTRA_HEADER ...]; echoes the HTTP status,
# body lands in $RESP. The console cookie jar rides on every call; server-key
# calls simply add the key header.
req() {
  local method="$1" path="$2" body="$3"
  shift 3
  local args=(-sS -X "$method" -H "Content-Type: application/json"
    -b "$JAR" -c "$JAR" -o "$RESP" -w "%{http_code}")
  local h
  for h in "$@"; do args+=(-H "$h"); done
  [[ -n "$body" ]] && args+=(-d "$body")
  curl "${args[@]}" "$ENDPOINT$path"
}

# The Appwrite response body is printed here; the request body (it can carry the
# password) must never be echoed.
die() { echo "setup.sh: $1 (status ${2:-?}): $(cat "$RESP" 2>/dev/null)" >&2; exit 1; }

# Wait for the stack.
for _ in $(seq 1 60); do
  if curl -sS -o /dev/null "$ENDPOINT/v1/health/version" 2>/dev/null; then break; fi
  sleep 2
done
curl -sS -o /dev/null "$ENDPOINT/v1/health/version" || die "stack not healthy at $ENDPOINT"

CONSOLE=("X-Appwrite-Project: console")
# Console-authorized calls against the project (keys, platforms, auth method
# switches) need the console session cookie plus admin mode.
PROJECT=("X-Appwrite-Project: $PROJECT_ID" "X-Appwrite-Mode: admin")

# 1. Console account (tolerate exists), then log in for the console cookie.
# 501 user_console_count_exceeded is the "a console root already exists" answer
# on a stack whose whitelist-root switch is on.
# A fresh stack answers the public version route before its database install
# has finished, so the first database backed call can answer 5xx; retry it.
for _ in $(seq 1 30); do
  code="$(req POST /v1/account "$(jq -nc --arg id "s9proof-root" --arg email "$CONSOLE_EMAIL" --arg pw "$PASSWORD" \
    '{userId:$id,email:$email,password:$pw,name:"S9 proof console admin"}')" "${CONSOLE[@]}")"
  [[ "$code" -lt 500 ]] && break
  sleep 3
done
[[ "$code" == 201 || "$code" == 409 || "$code" == 501 ]] || die "console account create failed" "$code"

code="$(req POST /v1/account/sessions/email "$(jq -nc --arg email "$CONSOLE_EMAIL" --arg pw "$PASSWORD" \
  '{email:$email,password:$pw}')" "${CONSOLE[@]}")"
[[ "$code" == 201 ]] || die "console login failed" "$code"

# 2. Organization team and project (console project, console cookie). A rerun
# on a kept stack finds them already there; on a single-org self-hosted
# instance a blind recreate answers 403, so check first.
code="$(req GET "/v1/teams/$ORG_TEAM_ID" "" "${CONSOLE[@]}")"
if [[ "$code" == 404 ]]; then
  code="$(req POST /v1/teams "$(jq -nc --arg id "$ORG_TEAM_ID" '{teamId:$id,name:"Gable proof org"}')" "${CONSOLE[@]}")"
  [[ "$code" == 201 ]] || die "org team create failed" "$code"
fi
[[ "$code" == 200 || "$code" == 201 ]] || die "org team lookup failed" "$code"

code="$(req GET "/v1/projects/$PROJECT_ID" "" "${CONSOLE[@]}")"
if [[ "$code" == 404 ]]; then
  code="$(req POST /v1/projects "$(jq -nc --arg id "$PROJECT_ID" --arg team "$ORG_TEAM_ID" \
    '{projectId:$id,name:"Gable proof",teamId:$team}')" "${CONSOLE[@]}")"
  [[ "$code" == 201 ]] || die "project create failed" "$code"
fi
[[ "$code" == 200 || "$code" == 201 ]] || die "project lookup failed" "$code"

# 3. Server key with exactly the slice 1 scopes; on a rerun the old key is
# deleted first because its secret is unrecoverable.
SCOPES='["sessions.write","users.read","users.write","teams.read","teams.write"]'
code="$(req POST /v1/project/keys "$(jq -nc --arg id "$KEY_ID" --argjson scopes "$SCOPES" \
  '{keyId:$id,name:"s9proof key",scopes:$scopes}')" "${PROJECT[@]}")"
if [[ "$code" == 409 ]]; then
  code="$(req DELETE "/v1/project/keys/$KEY_ID" "" "${PROJECT[@]}")"
  [[ "$code" == 204 ]] || die "key delete failed" "$code"
  code="$(req POST /v1/project/keys "$(jq -nc --arg id "$KEY_ID" --argjson scopes "$SCOPES" \
    '{keyId:$id,name:"s9proof key",scopes:$scopes}')" "${PROJECT[@]}")"
fi
[[ "$code" == 201 ]] || die "key create failed" "$code"
API_KEY="$(jq -r '.secret' "$RESP")"
[[ -n "$API_KEY" && "$API_KEY" != null ]] || die "key response had no secret"
KEY=("X-Appwrite-Project: $PROJECT_ID" "X-Appwrite-Key: $API_KEY")

# 4. Web platforms: the hostnames Gable's callback may legitimately land on.
i=0
for host in 127.0.0.1 localhost; do
  i=$((i + 1))
  code="$(req POST /v1/project/platforms/web "$(jq -nc --arg id "s9proof-loopback-$i" --arg host "$host" \
    '{platformId:$id,name:"loopback",hostname:$host}')" "${PROJECT[@]}")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "platform create failed for $host" "$code"
done

# 5. Client email+password auth OFF: only key calls may mint email sessions.
code="$(req PATCH /v1/project/auth-methods/email-password '{"enabled":false}' "${PROJECT[@]}")"
[[ "$code" == 200 ]] || die "auth method switch failed" "$code"

# 5b. One OAuth2 provider with placeholder credentials, so the proof can start
# a real createOAuth2Token and see Appwrite check the success URL against the
# platform list. No sign in ever completes against it; the values are invented.
code="$(req PATCH /v1/project/oauth2/google '{"enabled":true,"clientId":"s9proof-client-id","clientSecret":"s9proof-client-placeholder"}' "${PROJECT[@]}")"
[[ "$code" == 200 ]] || die "oauth2 provider enable failed" "$code"

# 5c. An OIDC provider pointing at the proof's test only issuer, which the
# integration test serves from the host (core/internal/appwrite, redirects
# proof). The values are invented; nothing real is ever contacted.
code="$(req PATCH /v1/project/oauth2/oidc "$(jq -nc --arg wk "http://host.docker.internal:8582/.well-known/openid-configuration" \
  '{enabled:true,clientId:"s9proof-oidc-client",clientSecret:"s9proof-oidc-placeholder",wellKnownURL:$wk}')" "${PROJECT[@]}")"
[[ "$code" == 200 ]] || die "oidc provider enable failed" "$code"

# 6. Staff team (server key; an API-created team has no owner membership).
code="$(req GET "/v1/teams/$STAFF_TEAM_ID" "" "${KEY[@]}")"
if [[ "$code" == 404 ]]; then
  code="$(req POST /v1/teams "$(jq -nc --arg id "$STAFF_TEAM_ID" '{teamId:$id,name:"Staff"}')" "${KEY[@]}")"
  [[ "$code" == 201 ]] || die "staff team create failed" "$code"
fi
[[ "$code" == 200 || "$code" == 201 ]] || die "staff team lookup failed" "$code"

# 7. One verified user per Gable role, each a confirmed member with its
# gable-* role; plus one user with no membership at all.
for role in "${ROLES[@]}"; do
  uid="s9proof-$role"
  email="$role@s9proof.example"
  code="$(req POST /v1/users "$(jq -nc --arg id "$uid" --arg email "$email" --arg pw "$PASSWORD" --arg name "S9 proof $role" \
    '{userId:$id,email:$email,password:$pw,name:$name}')" "${KEY[@]}")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "user create failed for $role" "$code"

  code="$(req PATCH "/v1/users/$uid/verification" '{"emailVerification":true}' "${KEY[@]}")"
  [[ "$code" == 200 ]] || die "email verification failed for $role" "$code"

  code="$(req POST "/v1/teams/$STAFF_TEAM_ID/memberships" "$(jq -nc --arg uid "$uid" --arg role "gable-$role" \
    '{userId:$uid,roles:[$role]}')" "${KEY[@]}")"
  [[ "$code" == 201 || "$code" == 409 ]] || die "membership create failed for $role" "$code"
done

code="$(req POST /v1/users "$(jq -nc --arg id "$NON_MEMBER_ID" --arg email "nomember@s9proof.example" --arg pw "$PASSWORD" \
  '{userId:$id,email:$email,password:$pw,name:"S9 proof non member"}')" "${KEY[@]}")"
[[ "$code" == 201 || "$code" == 409 ]] || die "non-member create failed" "$code"
code="$(req PATCH "/v1/users/$NON_MEMBER_ID/verification" '{"emailVerification":true}' "${KEY[@]}")"
[[ "$code" == 200 ]] || die "non-member verification failed" "$code"

# 8. The proof output. The key was generated against this stack this run.
users_json="{}"
for role in "${ROLES[@]}"; do
  users_json="$(jq -nc --argjson users "$users_json" --arg role "$role" --arg id "s9proof-$role" --arg email "$role@s9proof.example" \
    '$users + {($role): {id: $id, email: $email}}')"
done
umask 077
jq -nc \
  --arg endpoint "$ENDPOINT" \
  --arg projectId "$PROJECT_ID" \
  --arg teamId "$STAFF_TEAM_ID" \
  --arg apiKey "$API_KEY" \
  --arg password "$PASSWORD" \
  --arg keyId "$KEY_ID" \
  --argjson users "$users_json" \
  --arg nonMemberId "$NON_MEMBER_ID" \
  --arg nonMemberEmail "nomember@s9proof.example" \
  '{endpoint:$endpoint,projectId:$projectId,teamId:$teamId,apiKey:$apiKey,password:$password,keyId:$keyId,users:$users,nonMember:{id:$nonMemberId,email:$nonMemberEmail}}' \
  > "$OUT"
chmod 600 "$OUT"
echo "setup.sh: wrote $OUT"
