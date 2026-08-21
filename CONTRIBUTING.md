# Contributing to Gable

Thanks for your interest in contributing to Gable, the open commons for lumber
& building-materials (LBM) operations. This guide covers how to build and run
the project, the branch model, the pre-flight gates your change must pass, and
how inbound contributions are licensed.

Please also read our [Code of Conduct](./CODE_OF_CONDUCT.md). For security
issues, do **not** open a public issue — follow [SECURITY.md](./SECURITY.md).

## Build & run

**Prerequisites:** Docker (for Postgres), Go 1.25+, Node 20+, PostgreSQL 16
(via Docker or your own instance).

```bash
# 1. Boot Postgres (docker compose maps it to localhost:5434)
make up

# 2. Apply database migrations
make migrate

# 3. (Optional) seed demo data. DEMO_SEED=1 is REQUIRED — without it the seed
#    writes nothing and exits 0 (it TRUNCATEs transactional tables, so it is
#    gated against accidental runs).
DEMO_SEED=1 make seed

# 4. Run the backend and frontend in two terminals
cd backend && AUTH_MODE=dev go run ./cmd/server   # API on :8080
cd app && npm install && npm run dev              # SPA on :5173
```

`AUTH_MODE=dev` is required to start the backend locally and is **not** a
default — without it (and without a `JWKS_URL`) the server fail-closes and
exits. It disables authentication and authorization completely, so never set
it anywhere reachable. See [SECURITY.md](./SECURITY.md).

Open <http://localhost:5173>. To wipe and rebuild the dev database:

```bash
make reset-db
```

For stack details, code conventions, and gotchas, see [`CLAUDE.md`](./CLAUDE.md)
and the [`docs/`](./docs/) directory (architecture, design system, database
ERD). When conventions and this file disagree about the stack, trust
`CLAUDE.md`.

## Branch model

Development flows through two branches:

| Branch | Purpose |
|---|---|
| `main` | Stable trunk. Fork-ready. Releases are cut from here. |
| `staging` | Integration branch. Contributions land here first. |

**Open your pull request against `staging`.** After review, maintainers
fast-forward `staging → main`. Do not target `main` directly.

## Pull request workflow

1. Fork the repository (or branch directly if you have write access).
2. Branch off `staging`: `git checkout -b feat/short-description staging`.
3. Make your change. Keep commits focused — one logical change per commit.
4. Run the pre-flight gates below **before pushing**.
5. Open the PR against `staging` and fill out the PR template.
6. Address review feedback; a maintainer merges once it's approved and green.

### Pre-flight checklist

These are **exactly** the gates CI enforces — see
[`.github/workflows/ci.yml`](./.github/workflows/ci.yml). Run them locally
before pushing; failing fast locally saves a round trip.

The short version:

```bash
make preflight     # backend + frontend + licensing, everything the merge gate checks
```

The long version, if you'd rather run the pieces yourself:

```bash
# ---- Backend (the `backend` job) -----------------------------------------
cd backend
go vet ./...
go build ./...
go run ./cmd/migrate                                  # needs Postgres
go test -race ./...                                   # needs Postgres

# ---- Frontend (the `frontend` job) ---------------------------------------
cd app
npm ci
npx tsc --noEmit
npm run lint
npm run test:coverage                                 # or: npm run test -- --run
npm run build

# ---- Licensing (the `license` job) ---------------------------------------
cd <repo root>
pipx install reuse==6.2.0                             # once
make license-check

# ---- Container images (the `docker` job) ---------------------------------
docker build -f backend/Dockerfile .
docker build -f app/Dockerfile .
```

Equivalent make targets: `build`, `vet`, `test`, `test-short`, `cover`,
`fe-install`, `fe-typecheck`, `fe-lint`, `fe-test`, `fe-cover`, `fe-build`,
`license-check`, `vuln`. Run `make help` for the full list.

**Not a gate:** `govulncheck` (`make vuln`) runs in its own advisory CI job on
every push and nightly. It reports known vulnerabilities in Go dependencies but
does **not** block your PR — a CVE disclosed overnight is not your bug to fix.
Maintainers triage those findings.

**Also not a gate:** code coverage. CI measures it, uploads it as a build
artifact, and prints a summary — but there is no minimum threshold and no
build failure for lowering it. Please do add tests; just don't expect a number
to police it.

#### Running tests without Postgres

The backend suite talks to a real database for repository and integration
tests. If you haven't booted Postgres, use the short flag — the DB-dependent
tests skip themselves rather than failing:

```bash
cd backend && go test -short ./...    # or: make test-short
```

That is the fastest inner loop for changes that don't touch persistence. It is
**not** a substitute for the full run: CI always runs `go test -race ./...`
against a live Postgres 16, so a change that only passes under `-short` can
still go red. Before pushing anything that touches SQL, migrations, or
repository code, run the real thing:

```bash
make up && make migrate     # boots Postgres on localhost:5434 and migrates
make test
```

Database changes: new columns should follow the repo conventions — UUID primary
keys, `DECIMAL(19,4)` for physical quantities, money-as-cents in application
code, and every quantity paired with a UOM ID. See `CLAUDE.md` for details.

Licensing: every source file carries an SPDX header, and `make license-check`
enforces that the whole tree is accounted for. If you add a new file, copy the
header from a neighbouring file in the same directory — the correct identifier
for each path is in [`LICENSE-MAP.md`](./LICENSE-MAP.md).

## Licensing of contributions (inbound = OpenLBM, via CLA)

Gable is licensed **per component**: each directory is governed by a specific
OpenLBM Standard license (see [`LICENSE-MAP.md`](./LICENSE-MAP.md), the SPDX
headers on each file, and [`REUSE.toml`](./REUSE.toml)).

**Inbound contributions are licensed under the same OpenLBM Standard license
that governs the file(s) you change.** For example, a change under
`backend/internal/` is contributed under `LicenseRef-OpenLBM-Commons-1.0`; a
change under `app/` under `LicenseRef-OpenLBM-Surface-1.0`; a change under
`backend/pkg/apps/` under `LicenseRef-OpenLBM-Connector-1.0`.

Because these are custom licenses (not an off-the-shelf inbound=outbound OSS
license), we use a **Contributor License Agreement (CLA)**. Before your first
contribution is merged, you'll be asked to agree to the CLA, under which you
license each contribution to the project under the applicable component
license. The canonical OpenLBM Standard texts and the CLA live in the OpenLBM
repository:

> **OpenLBM Standard & CLA:** <https://github.com/FutureBuildAIinc/openlbm>

The license texts under [`LICENSES/`](./LICENSES/) are copies of the published
Standard, shipped so this repository is self-contained. The published Standard
is canonical; where a copy here and it ever disagree, the published Standard
governs.

By submitting a pull request, you confirm that:

- The contribution is your original work (or you have the right to submit it).
- You agree to license it under the component license(s) it touches, per the
  CLA.
- You are not knowingly including third-party code under incompatible terms.

## Reporting bugs & requesting features

Use the issue templates:

- **Bug reports** — include your branch/commit, environment, and reproduction.
- **Feature requests** — describe the LBM operations problem you're solving.

Please search existing issues first to avoid duplicates.
