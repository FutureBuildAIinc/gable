# Gable

**An open commons for lumber & building-materials (LBM) operations.**

Gable is an open-source operations platform purpose-built for lumber and
building-materials dealers — quoting, orders, inventory, purchasing, delivery
and routing, POS, B2B portal, and accounting — as a modern, self-hostable
alternative to legacy systems like Epicor BisTrack, ECI Spruce, and DMSi
Agility.

It's a **commons**: the code is open, the data model is documented, and the
platform is built to be forked, self-hosted, and extended. Contributions flow
back so every dealer benefits.

## What's here

- **Modular monolith backend** — a single Go binary with ~40 domain modules,
  moving toward an installable-*apps* model so third parties can plug in at a
  stable connector seam.
- **Web app** — a fast, dark-themed operations UI (ERP desktop, B2B portal,
  yard/warehouse, driver mobile, and POS surfaces).
- **Documented schema** — UUID keys, double-entry inventory moves, and a real
  general ledger.

## Stack

| Layer | Technology |
|---|---|
| Backend | Go 1.25 (stdlib `net/http.ServeMux` + pgx v5) |
| Database | PostgreSQL 16 |
| Frontend | Lit 3 web components + TypeScript 5.9 + Vite 7 + Tailwind 3.4 |
| Packaging | Docker |

## Quickstart

**Prerequisites:** Docker, Go 1.25+, Node 20+, PostgreSQL 16.

```bash
# 1. Boot Postgres (docker compose → localhost:5434)
make up

# 2. Apply migrations
make migrate

# 3. (Optional) seed demo data — the "Gable Lumber & Supply" fixture
make seed

# 4. Run backend and frontend in two terminals
cd backend && go run ./cmd/server      # API on :8080
cd app && npm install && npm run dev    # SPA on :5173
```

Open <http://localhost:5173>. To wipe and rebuild the dev database, run
`make reset-db`.

> **Local dev only:** the default `AUTH_MODE=dev` disables authentication for
> convenience on your machine. It must **never** be used on a reachable or
> production deployment — see [SECURITY.md](./SECURITY.md).

## Documentation

| Document | What's in it |
|---|---|
| [`docs/architecture.md`](./docs/architecture.md) | Module boundaries, API surface, hosting model |
| [`docs/modularization-blueprint.md`](./docs/modularization-blueprint.md) | The installable-apps platform: design and phases |
| [`docs/design-system.md`](./docs/design-system.md) | Colors, typography, component patterns |
| [`docs/database-erd.md`](./docs/database-erd.md) | Full schema + entity-relationship diagram |
| [`CLAUDE.md`](./CLAUDE.md) | Stack, conventions, pre-flight checks, and gotchas for contributors |
| [`.do/`](./.do/) | Example Digital Ocean App Platform deploy specs for self-hosting |

## Licensing

Gable is licensed **per component** under the **OpenLBM Standard**: different
directories are governed by different licenses. The authoritative directory →
license mapping is in [`LICENSE-MAP.md`](./LICENSE-MAP.md); the full license
texts are in [`LICENSES/`](./LICENSES/); every source file carries a matching
`SPDX-License-Identifier` header; and [`REUSE.toml`](./REUSE.toml) encodes the
same mapping in machine-readable form.

The canonical, counsel-reviewed OpenLBM Standard is published at:

> **<https://github.com/FutureBuildAIinc/openlbm>**

Third-party dependency licenses and map-data attribution requirements are
summarized in [`NOTICE`](./NOTICE) and
[`THIRD-PARTY-NOTICES.md`](./THIRD-PARTY-NOTICES.md).

## Contributing

We welcome contributions. Start with [`CONTRIBUTING.md`](./CONTRIBUTING.md) for
the build steps, branch model (**PRs target `staging`**, which maintainers
fast-forward to `master`), the pre-flight checklist, and how inbound
contributions are licensed via the CLA.

Please also read our [Code of Conduct](./CODE_OF_CONDUCT.md). To report a
security vulnerability, follow [SECURITY.md](./SECURITY.md) — do not open a
public issue.
