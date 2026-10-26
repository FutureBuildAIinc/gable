#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# The deploy recipe's static check. It reads deploy/compose.yml and proves the
# recipe is safe to hand to a deployment platform. No stack, no daemon, no
# build and no network: nothing is started, nothing is pulled.
#
# What it proves:
#   1. the compose file parses: `docker compose config` succeeds with
#      placeholder values;
#   2. it refuses to start when a required value is missing: each of the five
#      values that have no default makes `docker compose config` fail, and
#      the failure names the value;
#   3. no service publishes a host port;
#   4. every long running service has a health check;
#   5. the recipe builds this repository's own Dockerfiles.
#
# Run it from anywhere inside the repository:
#
#   bash deploy/check.sh
#
# It needs bash, docker compose and python3.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

COMPOSE=deploy/compose.yml
TMPDIR_CHECK="$(mktemp -d)"
cleanup() {
	find "$TMPDIR_CHECK" -mindepth 1 -delete 2>/dev/null || true
	rmdir "$TMPDIR_CHECK" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
	echo "check-deploy: FAIL: $*" >&2
	exit 1
}

ok() {
	echo "check-deploy: ok: $*"
}

# The values the recipe requires, and the placeholder each one gets while the
# check runs. The placeholders are obviously not secrets: they are the same
# word, so nothing here can be mistaken for a credential.
REQUIRED=(POSTGRES_PASSWORD PAYMENT_VAULT_KEY PORTAL_JWT_SECRET GABLE_PUBLIC_URL CORS_ORIGINS)
PH_SECRET="placeholder-not-a-real-secret"
PH_URL="https://demo.example.invalid"

# The services that stay up. The one shot service (migrate) is not one of
# them: it runs, it exits, and the platform does not start it again.
LONG_RUNNING=(postgres core worker web)

[ -f "$COMPOSE" ] || fail "missing $COMPOSE"
command -v docker >/dev/null 2>&1 || fail "docker is not on the PATH"
docker compose version >/dev/null 2>&1 || fail "docker compose is not available"
command -v python3 >/dev/null 2>&1 || fail "python3 is not on the PATH"

# write_env_file writes the placeholder values into an env file, leaving out
# the one named. `docker compose --env-file` reads them for the file's
# interpolation, exactly as a platform's own environment would.
write_env_file() {
	local omit="$1"
	local out="$2"
	local pair
	: >"$out"
	for pair in "POSTGRES_PASSWORD=$PH_SECRET" "PAYMENT_VAULT_KEY=$PH_SECRET" \
		"PORTAL_JWT_SECRET=$PH_SECRET" "GABLE_PUBLIC_URL=$PH_URL" "CORS_ORIGINS=$PH_URL"; do
		[ "${pair%%=*}" = "$omit" ] && continue
		printf '%s\n' "$pair" >>"$out"
	done
}

# 1. The file parses with placeholder values.
write_env_file "" "$TMPDIR_CHECK/all.env"
if ! docker compose --env-file "$TMPDIR_CHECK/all.env" -f "$COMPOSE" config >"$TMPDIR_CHECK/config.json" 2>"$TMPDIR_CHECK/config.err"; then
	cat "$TMPDIR_CHECK/config.err" >&2
	fail "docker compose config did not accept $COMPOSE"
fi
ok "docker compose config accepts $COMPOSE"

# 2. Each required value refuses the file when it is missing.
for name in "${REQUIRED[@]}"; do
	write_env_file "$name" "$TMPDIR_CHECK/without-$name.env"
	status=0
	output="$( (unset "$name"; docker compose --env-file "$TMPDIR_CHECK/without-$name.env" -f "$COMPOSE" config) 2>&1 )" || status=$?
	if [ "$status" -eq 0 ]; then
		fail "$COMPOSE starts without $name; that value must have no default"
	fi
	case "$output" in
	*"$name"*) ;;
	*) fail "the refusal without $name does not name it: $output" ;;
	esac
	ok "the recipe refuses to start without $name"
done

# 3 and 4 and 5. The rendered config is the authority: no service publishes a
# port, every long running service has a health check, and the images are
# built from this repository's Dockerfiles.
docker compose --env-file "$TMPDIR_CHECK/all.env" -f "$COMPOSE" config --format json >"$TMPDIR_CHECK/config.json"
python3 - "$TMPDIR_CHECK/config.json" "$COMPOSE" <<'PY'
import json
import os
import sys

config_path, compose_path = sys.argv[1], sys.argv[2]
with open(config_path, encoding="utf-8") as handle:
    config = json.load(handle)

services = config.get("services", {})
problems = []

for name, service in services.items():
    # A published host port would put the database or the API on the host's
    # network, which the recipe forbids: the platform's proxy is the only way
    # in.
    if "ports" in service:
        problems.append("service %s publishes a host port: %s" % (name, service["ports"]))

long_running = ("postgres", "core", "worker", "web")
for name in long_running:
    service = services.get(name)
    if service is None:
        problems.append("the recipe has no %s service" % name)
        continue
    health = service.get("healthcheck")
    if not health or health.get("disable"):
        problems.append("service %s has no health check" % name)

# The one shot service must not be started again by the platform.
migrate = services.get("migrate", {})
if migrate.get("restart") != "no":
    problems.append("the migrate service must not restart itself")

# The images come from this repository's own Dockerfiles.
for name in ("migrate", "core", "worker", "web"):
    build = services.get(name, {}).get("build", {})
    context = build.get("context", "")
    dockerfile = build.get("dockerfile", "")
    if not context or not dockerfile:
        problems.append("service %s has no build" % name)
        continue
    path = os.path.join(context, dockerfile)
    if not os.path.isfile(path):
        problems.append("service %s builds %s, which is not in the repository" % (name, path))

# The data lives on a named volume.
volumes = config.get("volumes", {})
if not volumes:
    problems.append("the recipe has no named volume")

if problems:
    for problem in problems:
        print("check-deploy: FAIL: %s" % problem, file=sys.stderr)
    sys.exit(1)
print("check-deploy: ok: no service publishes a port")
print("check-deploy: ok: every long running service has a health check")
print("check-deploy: ok: the images are built from this repository's Dockerfiles")
PY

ok "the recipe is safe to hand to a platform"
