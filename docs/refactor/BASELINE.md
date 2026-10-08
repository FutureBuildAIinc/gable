<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Refactor baseline (item R1-0)

This file records the result of running every command from the `CI` workflow
(`.github/workflows/ci.yml`) locally at the refactoring base commit, before
any refactor item touched the tree. Its purpose: any later item whose CI run
goes red on a command that is green below owns that red itself and cannot
blame the baseline; conversely, a command recorded red or broken below is a
known starting condition, not a regression introduced by a refactor item.

## Base

- Base commit: `8361f23b40c3daf08a64ed4f4c43f518807468e4` (the commit
  `refactor/v1` was cut from; the runs below happened on branch
  `refactor/r1-0-baseline` at that commit with a clean working tree).

## Tool versions used

| Tool | Local version | CI pin |
|---|---|---|
| Go | 1.27.1 | 1.25 (`GO_VERSION` in ci.yml) |
| Node | 20.20.2 | 20 (`NODE_VERSION` in ci.yml) |
| npm | 10.8.2 | ships with the Node version |
| Docker | 29.1.3 | GitHub runner's Docker |
| Python | 3.12.3 | GitHub runner's Python |
| Postgres | `postgres:16-alpine` (image id `sha256:e684c11a6c7c`) | `postgres:16-alpine` service |
| reuse | 6.2.0 | 6.2.0 (`REUSE_VERSION`) |
| govulncheck | 1.1.4 (CI pin), plus a second pass on 1.8.0 | 1.1.4 (`GOVULNCHECK_VERSION`) |

Local Go is newer than the CI pin. The one visible consequence is the
govulncheck result below; everything else builds, vets and tests identically.

## Environment

- Every command ran with `CI=true`.
- Postgres ran in a throwaway `postgres:16-alpine` container reachable at
  `127.0.0.1:55410`, initialised with the same user and database names the
  CI service uses (`gable_user` / `gable_test`). `DATABASE_URL` was passed on
  the command line of every command that needed it; no environment default
  was relied on.

## Results

### Backend job

| CI step | Command | Result |
|---|---|---|
| Vet | `go vet ./...` | pass |
| Build | `go build ./...` | pass |
| Migrate test database | `go run ./cmd/migrate` | pass; all 86 migrations applied |
| Test (with coverage) | `go test -race -coverprofile=coverage.out -covermode=atomic ./...` | pass |

### Seed (not a CI step; recorded because later items depend on it)

| Command | Result |
|---|---|
| `DEMO_SEED=1 go run ./cmd/seed` | pass; seeding completed against the throwaway database |

### Frontend job

| CI step | Command | Result |
|---|---|---|
| Install dependencies | `npm ci` | pass |
| Type check | `npx tsc --noEmit` | pass |
| Lint | `npm run lint` | pass |
| Test (with coverage) | `npm run test:coverage` | pass; `app/package.json` has the `test:coverage` script, so this is the branch the CI test step takes |
| Build | `npm run build` | pass |

### License job

| CI step | Command | Result |
|---|---|---|
| reuse lint (report) | `reuse lint` | exit 1 on the "unused licenses" finding only (`GPL-3.0-or-later`, `AGPL-3.0-or-later`, `LicenseRef-OpenLBM-Community-Source-1.0`, `LicenseRef-OpenLBM-Trademark`). Expected: CI runs this step report-only, and the gate below exists to tolerate exactly this finding |
| REUSE gate | `python3 .github/scripts/reuse_gate.py` | pass; reuse 6.2.0, 751 files scanned, all with copyright and licensing info |

### Docker job

| CI step | Command | Result |
|---|---|---|
| Build backend image | `docker build -f backend/Dockerfile .` | pass |
| Build frontend image | `docker build -f app/Dockerfile .` | pass |

### Vulnerabilities job (advisory; not a merge gate)

| Command | Result |
|---|---|
| `govulncheck v1.1.4 ./...` | tool crash: `panic: unexpected expr: *ast.KeyValueExpr`. The CI-pinned scanner does not parse under local Go 1.27.1 (CI runs it under Go 1.25). A scanner and toolchain incompatibility, not a repository finding |
| `govulncheck v1.8.0 ./...` (newer scanner, informational) | exit 3: one called vulnerability, `GO-2026-6629` in `golang.org/x/text` v0.39.0 (fixed in v0.41.0); one imported and four required vulnerabilities are not called by this module's code. Advisory in CI either way |

## Counts at the base

| What | Count |
|---|---|
| Go packages on `./...` | 59 |
| Go packages containing test files | 50 |
| Go test files | 90 |
| Go test functions (`Test*`) | 877 |
| Frontend test files | 25 |
