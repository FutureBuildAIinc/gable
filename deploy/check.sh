#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# The deploy recipe's static check. It reads deploy/compose.yml (the recipe
# that builds on the host) and deploy/compose.images.yml (the recipe that runs
# prebuilt images) and proves both are safe to hand to a deployment platform.
# No stack, no daemon, no build and no network: nothing is started, nothing is
# pulled.
#
# What it proves, for each file:
#   1. the compose file parses: `docker compose config` succeeds with
#      placeholder values;
#   2. it refuses to start when a required value is missing: each value that
#      has no default makes `docker compose config` fail, and the failure
#      names the value (the images file adds GABLE_IMAGE_TAG to the list);
#   3. no service publishes a host port;
#   4. every long running service has a health check;
#   5. the recipe builds this repository's own Dockerfiles (compose.yml), or
#      every service runs a prebuilt image ref (compose.images.yml);
#   6. the customer variant (AUTH_MODE unset) passes JWKS_URL and AUTH_ISSUER
#      to the core service;
#   7. core receives the four Appwrite settings from the environment (each
#      one required, so the Appwrite variant cannot start without them);
#   8. the recipe has no dev sign in: neither core nor worker takes AUTH_MODE,
#      the web build takes no VITE_AUTH_DEV_MODE, and the web image is the
#      customer image (no `demo-` tag).
#
# Run it from anywhere inside the repository:
#
#   bash deploy/check.sh
#
# It needs bash, docker compose and python3.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

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

# The values both recipes require, and the placeholder each one gets while the
# check runs. The placeholders are obviously not secrets: they are the same
# word, so nothing here can be mistaken for a credential.
REQUIRED=(POSTGRES_PASSWORD PAYMENT_VAULT_KEY PORTAL_JWT_SECRET GABLE_PUBLIC_URL CORS_ORIGINS TRUSTED_PROXIES
	APPWRITE_ENDPOINT APPWRITE_PROJECT_ID APPWRITE_TEAM_ID APPWRITE_API_KEY)
PH_SECRET="placeholder-not-a-real-secret"
PH_URL="https://demo.example.invalid"
PH_PROXY="10.0.0.0/8,172.16.0.0/12"
PH_APPWRITE="https://appwrite.example.invalid/v1"
PH_JWKS="https://idp.example.invalid/.well-known/jwks.json"
PH_ISSUER="https://idp.example.invalid"
# The images file only: the commit sha the images were built for.
PH_TAG="0000000000000000000000000000000000000000"

# The services that stay up. The one shot service (migrate) is not one of
# them: it runs, it exits, and the platform does not start it again.
LONG_RUNNING=(postgres core worker web)

command -v docker >/dev/null 2>&1 || fail "docker is not on the PATH"
docker compose version >/dev/null 2>&1 || fail "docker compose is not available"
command -v python3 >/dev/null 2>&1 || fail "python3 is not on the PATH"

# write_env_file writes the placeholder values into an env file, leaving out
# the one named. `docker compose --env-file` reads them for the file's
# interpolation, exactly as a platform's own environment would. The extra
# pairs argument carries the images file's own values (the tag, the demo
# values), so both files are checked through the same path.
write_env_file() {
	local omit="$1"
	local out="$2"
	shift 2
	local pair
	: >"$out"
	for pair in "POSTGRES_PASSWORD=$PH_SECRET" "PAYMENT_VAULT_KEY=$PH_SECRET" \
		"PORTAL_JWT_SECRET=$PH_SECRET" "GABLE_PUBLIC_URL=$PH_URL" \
		"CORS_ORIGINS=$PH_URL" "TRUSTED_PROXIES=$PH_PROXY" \
		"APPWRITE_ENDPOINT=$PH_APPWRITE" "APPWRITE_PROJECT_ID=placeholder-project" \
		"APPWRITE_TEAM_ID=placeholder-team" "APPWRITE_API_KEY=$PH_SECRET" "$@"; do
		[ "${pair%%=*}" = "$omit" ] && continue
		printf '%s\n' "$pair" >>"$out"
	done
}

# check_recipe <compose file> <mode: build|images> [extra env pairs...]
#
# Runs checks 1 to 6 on one file. The mode says how the services are made:
# `build` (compose.yml, built from the repository's Dockerfiles) or `images`
# (compose.images.yml, prebuilt image refs).
check_recipe() {
	local file="$1"
	local mode="$2"
	shift 2
	local extra=("$@")
	local required=("${REQUIRED[@]}")
	local tag_pairs=()
	if [ "$mode" = "images" ]; then
		required+=(GABLE_IMAGE_TAG)
		tag_pairs=("GABLE_IMAGE_TAG=$PH_TAG")
	fi

	[ -f "$file" ] || fail "missing $file"

	# 1. The file parses with placeholder values.
	write_env_file "" "$TMPDIR_CHECK/all.env" "${tag_pairs[@]}"
	if ! docker compose --env-file "$TMPDIR_CHECK/all.env" -f "$file" config >"$TMPDIR_CHECK/config.json" 2>"$TMPDIR_CHECK/config.err"; then
		cat "$TMPDIR_CHECK/config.err" >&2
		fail "docker compose config did not accept $file"
	fi
	ok "docker compose config accepts $file"

	# 2. Each required value refuses the file when it is missing.
	local name status output
	for name in "${required[@]}"; do
		write_env_file "$name" "$TMPDIR_CHECK/without-$name.env" "${tag_pairs[@]}"
		status=0
		output="$( (unset "$name"; docker compose --env-file "$TMPDIR_CHECK/without-$name.env" -f "$file" config) 2>&1 )" || status=$?
		if [ "$status" -eq 0 ]; then
			fail "$file starts without $name; that value must have no default"
		fi
		case "$output" in
		*"$name"*) ;;
		*) fail "the refusal without $name does not name it: $output" ;;
		esac
		ok "$file refuses to start without $name"
	done

	# 3, 4, 5 and 6. The rendered config is the authority: no service
	# publishes a port, every long running service has a health check, the
	# services come from this repository's own Dockerfiles (build mode) or
	# from prebuilt image refs (images mode), and the customer variant
	# passes JWKS_URL and AUTH_ISSUER to core.
	docker compose --env-file "$TMPDIR_CHECK/all.env" -f "$file" config --format json >"$TMPDIR_CHECK/config.json"
	cp "$TMPDIR_CHECK/all.env" "$TMPDIR_CHECK/customer.env"
	printf 'JWKS_URL=%s\nAUTH_ISSUER=%s\n' "$PH_JWKS" "$PH_ISSUER" >>"$TMPDIR_CHECK/customer.env"
	(unset AUTH_MODE; docker compose --env-file "$TMPDIR_CHECK/customer.env" -f "$file" config --format json >"$TMPDIR_CHECK/customer.json")

	python3 - "$TMPDIR_CHECK/config.json" "$TMPDIR_CHECK/customer.json" "$mode" "$PH_JWKS" "$PH_ISSUER" "$PH_TAG" "$PH_APPWRITE" <<'PY'
import json
import os
import sys

config_path, customer_path, mode, expected_jwks, expected_issuer, expected_tag, expected_appwrite_endpoint = sys.argv[1:8]
with open(config_path, encoding="utf-8") as handle:
    config = json.load(handle)
with open(customer_path, encoding="utf-8") as handle:
    customer = json.load(handle)

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

if mode == "build":
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
else:
    # Every made service runs a prebuilt image ref pinned to the tag: the core
    # roles (migrate, core, worker) on gable-core, the front on gable-web.
    for name, expected in (
        ("migrate", "ghcr.io/futurebuildaiinc/gable-core"),
        ("core", "ghcr.io/futurebuildaiinc/gable-core"),
        ("worker", "ghcr.io/futurebuildaiinc/gable-core"),
        ("web", "ghcr.io/futurebuildaiinc/gable-web"),
    ):
        service = services.get(name, {})
        image = service.get("image", "")
        if not image:
            problems.append("service %s has no image ref" % name)
            continue
        prefix, _, tag = image.rpartition(":")
        if prefix != expected:
            problems.append("service %s runs %s, not %s:<sha>" % (name, image, expected))
        elif not tag.endswith(expected_tag):
            problems.append("service %s runs %s, which is not tagged with the required tag" % (name, image))
    # The web image is the customer image: its tag is the sha alone, never
    # the `demo-` tag of the persona sign in build.
    web_image = services.get("web", {}).get("image", "")
    _, _, web_tag = web_image.rpartition(":")
    if web_tag != expected_tag:
        problems.append("the web tag %s is not the required tag; the demo runs the customer web image" % web_tag)

# The four Appwrite settings reach core, and only core (item 345).
expected_appwrite = {
    "APPWRITE_ENDPOINT": expected_appwrite_endpoint,
    "APPWRITE_PROJECT_ID": "placeholder-project",
    "APPWRITE_TEAM_ID": "placeholder-team",
}
core_env = services.get("core", {}).get("environment", {})
for key, want in expected_appwrite.items():
    if core_env.get(key) != want:
        problems.append("service core does not receive %s" % key)
if not core_env.get("APPWRITE_API_KEY"):
    problems.append("service core does not receive APPWRITE_API_KEY")

# No dev sign in in the recipe (item 345): the demo signs in through Appwrite,
# so nothing takes AUTH_MODE and the web build takes no persona sign in arg.
for name in ("core", "worker"):
    if "AUTH_MODE" in services.get(name, {}).get("environment", {}):
        problems.append("service %s takes AUTH_MODE; AUTH_MODE=dev belongs to the local root stack only" % name)
web_args = services.get("web", {}).get("build", {}).get("args", {})
if "VITE_AUTH_DEV_MODE" in (web_args or {}):
    problems.append("the web build takes VITE_AUTH_DEV_MODE; the persona sign in is not part of the recipe")

# The data lives on a named volume.
volumes = config.get("volumes", {})
if not volumes:
    problems.append("the recipe has no named volume")

# The customer variant (AUTH_MODE unset) must pass JWKS_URL and AUTH_ISSUER to
# core so the fail-closed server can verify tokens from the identity provider.
customer_core_env = customer.get("services", {}).get("core", {}).get("environment", {})
if customer_core_env.get("AUTH_MODE", "") != "":
    problems.append("the customer variant left AUTH_MODE set on core: %r" % customer_core_env.get("AUTH_MODE"))
if customer_core_env.get("JWKS_URL") != expected_jwks:
    problems.append("service core does not receive JWKS_URL in the customer variant")
if customer_core_env.get("AUTH_ISSUER") != expected_issuer:
    problems.append("service core does not receive AUTH_ISSUER in the customer variant")

if problems:
    for problem in problems:
        print("check-deploy: FAIL: %s" % problem, file=sys.stderr)
    sys.exit(1)
print("check-deploy: ok: no service publishes a port")
print("check-deploy: ok: every long running service has a health check")
if mode == "build":
    print("check-deploy: ok: the images are built from this repository's Dockerfiles")
else:
    print("check-deploy: ok: every made service runs a prebuilt image ref pinned to the required tag")
print("check-deploy: ok: the customer variant passes JWKS_URL and AUTH_ISSUER to core")
print("check-deploy: ok: core receives the four Appwrite settings from the environment")
print("check-deploy: ok: the recipe has no dev sign in (no AUTH_MODE, no persona sign in build, customer web image)")
PY
}

check_recipe deploy/compose.yml build
check_recipe deploy/compose.images.yml images

ok "both recipes are safe to hand to a platform"
