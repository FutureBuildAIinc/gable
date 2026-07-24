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

# 3. (Optional) seed demo data
make seed

# 4. Run the backend and frontend in two terminals
cd backend && go run ./cmd/server     # API on :8080
cd app && npm install && npm run dev   # SPA on :5173
```

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
| `master` | Stable trunk. Fork-ready. Releases are cut from here. |
| `staging` | Integration branch. Contributions land here first. |

**Open your pull request against `staging`.** After review, maintainers
fast-forward `staging → master`. Do not target `master` directly.

## Pull request workflow

1. Fork the repository (or branch directly if you have write access).
2. Branch off `staging`: `git checkout -b feat/short-description staging`.
3. Make your change. Keep commits focused — one logical change per commit.
4. Run the pre-flight gates below **before pushing**.
5. Open the PR against `staging` and fill out the PR template.
6. Address review feedback; a maintainer merges once it's approved and green.

### Pre-flight checklist

Run these locally before pushing. CI runs them too, but failing fast locally
saves a round trip.

```bash
# Backend
cd backend
go build ./...
go vet ./...
go test ./...

# Frontend
cd app
npx tsc --noEmit
npm run lint
npm run build
```

Database changes: new columns should follow the repo conventions — UUID primary
keys, `DECIMAL(19,4)` for physical quantities, money-as-cents in application
code, and every quantity paired with a UOM ID. See `CLAUDE.md` for details.

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

The license texts under [`LICENSES/`](./LICENSES/) in this repo are drafts
pending counsel review; where they and the published Standard disagree, the
published Standard governs.

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
