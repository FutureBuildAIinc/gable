# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

.PHONY: help up down logs ps pg-shell migrate seed reset-db \
        build vet test test-short cover \
        fe-install fe-typecheck fe-lint fe-test fe-cover fe-build \
        license-check vuln \
        preflight preflight-backend preflight-frontend

# Tool versions. Keep these in sync with env: in .github/workflows/ci.yml so
# `make preflight` reproduces CI rather than approximating it.
GOVULNCHECK_VERSION ?= v1.1.4
REUSE_VERSION       ?= 6.2.0

help:
	@echo "Gable make targets"
	@echo ""
	@echo "  Infra"
	@echo "    up down logs ps pg-shell     docker compose lifecycle"
	@echo "    migrate seed reset-db        database lifecycle"
	@echo "                                 (seed needs DEMO_SEED=1 — see the Makefile)"
	@echo ""
	@echo "  Pre-flight (mirrors .github/workflows/ci.yml)"
	@echo "    preflight                    everything CI gates on"
	@echo "    preflight-backend            build + vet + test"
	@echo "    preflight-frontend           typecheck + lint + test + build"
	@echo "    license-check                REUSE compliance gate"
	@echo ""
	@echo "  Individual gates"
	@echo "    build vet test test-short cover"
	@echo "    fe-install fe-typecheck fe-lint fe-test fe-cover fe-build"
	@echo ""
	@echo "  Advisory (not a merge gate)"
	@echo "    vuln                         govulncheck $(GOVULNCHECK_VERSION)"

# ---------------------------------------------------------------------------
# Infra (Docker)
# ---------------------------------------------------------------------------
up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f

ps:
	docker compose ps

pg-shell:
	docker exec -it gable_postgres psql -U gable_user -d gable_db

# ---------------------------------------------------------------------------
# Backend lifecycle
# ---------------------------------------------------------------------------
# Apply SQL migrations in order. Honors DATABASE_URL if set; otherwise the
# migrator falls back to its built-in dev default (localhost:5434).
migrate:
	cd backend && go run ./cmd/migrate

# Populate the database with Kelowna / Gable Lumber & Supply demo data.
#
# REQUIRES `DEMO_SEED=1`. Without it the command connects to nothing, writes
# nothing, and exits 0 with an explanatory log line — which is the correct
# default for a self-hosted install, and a surprise for a newcomer following
# the Quickstart. Run it as:
#
#     DEMO_SEED=1 make seed
#
# The gate is not hygiene: the seed TRUNCATEs the transactional tables (orders,
# invoices, quotes, deliveries, payments, the general ledger, POs …) with
# RESTART IDENTITY CASCADE before it writes, so an accidental run against a
# database you care about destroys data. Reference data (products, customers,
# vendors, locations, chart of accounts) upserts on natural keys and is safe to
# re-run. See the gate and its reasoning in backend/cmd/seed/main.go.
seed:
	cd backend && go run ./cmd/seed

# Nuke + repave the dev database, then migrate and seed. Requires the
# `gable_postgres` container from docker compose to be running.
reset-db:
	docker exec -i gable_postgres psql -U gable_user -d postgres -c "DROP DATABASE IF EXISTS gable_db;"
	docker exec -i gable_postgres psql -U gable_user -d postgres -c "CREATE DATABASE gable_db OWNER gable_user;"
	$(MAKE) migrate
	$(MAKE) seed

# ---------------------------------------------------------------------------
# Backend gates (the `backend` job in ci.yml)
# ---------------------------------------------------------------------------
build:
	cd backend && go build ./...

vet:
	cd backend && go vet ./...

# The full suite, exactly as CI runs it. Needs Postgres — `make up && make
# migrate` first, or point DATABASE_URL at your own instance.
test:
	cd backend && go test -race ./...

# The no-Postgres path. Tests that need a live database skip themselves under
# -short, so this is what to run when you have not booted docker compose.
test-short:
	cd backend && go test -short ./...

# Coverage, same invocation as CI. Reporting only — there is no threshold.
cover:
	cd backend && go test -race -coverprofile=coverage.out -covermode=atomic ./...
	cd backend && go tool cover -func=coverage.out | tail -n 1
	@echo "HTML report: cd backend && go tool cover -html=coverage.out"

# ---------------------------------------------------------------------------
# Frontend gates (the `frontend` job in ci.yml)
# ---------------------------------------------------------------------------
fe-install:
	cd app && npm ci

fe-typecheck:
	cd app && npx tsc --noEmit

fe-lint:
	cd app && npm run lint

fe-test:
	cd app && npm run test -- --run

fe-cover:
	cd app && npm run test:coverage

fe-build:
	cd app && npm run build

# ---------------------------------------------------------------------------
# Licensing gate (the `license` job in ci.yml)
# ---------------------------------------------------------------------------
# Requires the `reuse` tool: `pipx install reuse==$(REUSE_VERSION)`.
# Runs the full report for humans, then the gate that CI actually enforces.
license-check:
	reuse lint || true
	python3 .github/scripts/reuse_gate.py

# ---------------------------------------------------------------------------
# Advisory — NOT a merge gate (the `vulnerabilities` job in ci.yml)
# ---------------------------------------------------------------------------
vuln:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	cd backend && govulncheck ./...

# ---------------------------------------------------------------------------
# Aggregates
# ---------------------------------------------------------------------------
preflight-backend: build vet test

preflight-frontend: fe-typecheck fe-lint fe-test fe-build

# Everything the merge gate checks. Run this before pushing.
preflight: preflight-backend preflight-frontend license-check
	@echo ""
	@echo "Pre-flight passed. Note: govulncheck (make vuln) is advisory and is"
	@echo "not part of the merge gate — see .github/workflows/ci.yml."
