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

> **A note on the Go module path.** The backend's module is
> `github.com/gablelbm/gable` (see [`backend/go.mod`](./backend/go.mod)), but the
> repository lives at <https://github.com/FutureBuildAIinc/gable>. That is
> deliberate and not a mistake: the module path predates the move to the
> `FutureBuildAIinc` organisation and is load-bearing — every import in the
> backend uses it, and the SDK's CI asserts that `gable-sdk` never imports it.
> Renaming it would be a breaking change for anything that vendors the backend,
> so it stays. **`go get github.com/gablelbm/gable` does not resolve**; clone the
> repository instead. Build from a checkout, as the Quickstart below does.

## Quickstart

**Prerequisites:** Docker, Go 1.25+, Node 20+, PostgreSQL 16.

```bash
# 1. Boot Postgres (docker compose → localhost:5434)
make up

# 2. Apply migrations
make migrate

# 3. (Optional) seed demo data — the "Gable Lumber & Supply" fixture.
#    DEMO_SEED=1 is REQUIRED: without it the seed writes nothing and exits 0,
#    because it TRUNCATEs transactional tables and must never run by accident.
DEMO_SEED=1 make seed

# 4. Run backend and frontend in two terminals
cd backend && AUTH_MODE=dev go run ./cmd/server   # API on :8080
cd app && npm install && npm run dev              # SPA on :5173
```

Open <http://localhost:5173>. To wipe and rebuild the dev database, run
`make reset-db`.

> **Why `AUTH_MODE=dev` is on that command:** it is **not** a default. The
> backend is fail-closed — with `AUTH_MODE` unset it refuses to start, logging
> `CORS_ORIGINS not set and AUTH_MODE != dev` (checked first,
> `cmd/server/main.go:96-99`) and, once that is satisfied,
> `JWKS_URL not set and AUTH_MODE != dev` (`cmd/server/main.go:146-149`). It
> exits rather than starting without authentication. You must opt in
> explicitly, which is the point.
>
> `AUTH_MODE=dev` disables authentication and authorization entirely: no JWT
> is verified and every role check passes, so any request reaching the port has
> full access to the ERP, general ledger, AP/AR and payments. It is safe **only**
> on your own machine against throwaway data, and must **never** be set on a
> reachable or production deployment — see [SECURITY.md](./SECURITY.md).

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
fast-forward to `main`), the pre-flight checklist, and how inbound
contributions are licensed via the CLA.

Please also read our [Code of Conduct](./CODE_OF_CONDUCT.md). To report a
security vulnerability, follow [SECURITY.md](./SECURITY.md) — do not open a
public issue.

### Contributing with Claude

**You don't have to be a programmer to contribute.** This repo ships a Claude
Code agent kit in [`.claude/`](./.claude/) that loads automatically when you
open the project. It knows this codebase — the module layout, the money
conventions, the pre-flight gates, the per-component licence map — so it gives
you repo-specific help rather than generic advice.

If you run a yard, work a counter, dispatch trucks, or write docs, it can turn
what you know into a filed issue, a workflow spec, or a documentation fix
without you writing any code.

| Skill | Shortcut | For |
|---|---|---|
| `report-an-issue` | `/file-issue` | "The delivery screen showed the wrong total" → a properly scoped, correctly routed bug report |
| `describe-a-workflow` | `/describe-workflow` | How will-call, dispatch, or quoting really works at your yard → a buildable spec |
| `improve-docs` | `/fix-doc` | Docs that are wrong, stale, or missing |
| `explain-this-code` | `/newcomer-tour` | Getting oriented in the codebase |
| `check-my-contribution` | `/preflight` | The real pre-flight before you open a PR |
| `add-a-test` | `/write-a-test` | Coverage for a module that has none |
| `licensing-check` | `/license-of` | "Which licence governs this file?" |

Start here: **[`CONTRIBUTING-WITH-CLAUDE.md`](./CONTRIBUTING-WITH-CLAUDE.md)** —
installation, worked examples, and the ground rules. AI-assisted contributions
are welcome; you remain responsible for understanding and testing what you
submit.
