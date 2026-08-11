---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: explain-this-code
description: Orient a newcomer in the Gable codebase — the modular-monolith layout, the 41 domain modules under backend/internal, the backend/pkg/apps connector seam, and the Lit web-component frontend — and teach them how to find things themselves. Use when the user says "where do I start", "how does this codebase work", "give me a tour", "I'm new here", "explain the architecture", "where does the order code live", "how does the frontend work", "what is backend/pkg/apps", "how do I find the code for the delivery screen", "/newcomer-tour", or asks what any module or file does.
---

# explain-this-code — teach the map, not just the answer

Two jobs, in this order:

1. **Answer the question they asked**, concretely, with real file paths and line numbers.
2. **Teach the lookup that got you there**, so next time they don't need you.

Every explanation ends with "here's how I found that." If you only ever answer, you've built
a dependency instead of a contributor.

---

## The 90-second map

Give this once, at the start, tailored to what they care about.

```
gable/
  backend/          Go 1.25 — one binary, ~41 domain modules (the "modular monolith")
    cmd/server/     main.go: wires every module repo→service→handler→routes
    cmd/migrate/    applies backend/migrations/*.sql in order
    cmd/seed/       the demo dataset ("Gable Lumber & Supply", Kelowna BC)
    internal/       the 41 modules. THIS is where business logic lives.
    pkg/            shared plumbing (database, middleware, audit, httputil, metrics…)
    pkg/apps/       the connector seam — the stable surface third parties plug into
    migrations/     plain numbered SQL: 001_…, 002_…
  app/              Lit 3 + TypeScript + Vite + Tailwind
    src/routes.ts   THE route table. Start here for any "where is this screen" question.
    src/pages/      one file per screen
    src/components/ reusable gable-* web components
    src/services/   one service per backend module (OrderService.ts, InvoiceService.ts…)
    src/lib/        router, icons, toast, utils (formatCents lives here)
    src/apps/       route + manifest declarations for converted installable apps
  docs/             architecture, design system, database ERD, modularization blueprint
  .do/              example Digital Ocean deploy specs
  LICENSES/         the OpenLBM Standard license texts
```

**One sentence for the shape:** it's a single Go process that serves a REST API at
`/api/v1/*` (plus `/api/portal/v1`, `/api/integration`, `/api/v1/a2a`) to a single-page Lit
app — no microservices, no message bus. Cross-module calls are plain synchronous Go
interface calls. `docker-compose.yml` and `docs/architecture.md` mention NATS events; those
are **future design, not wired**.

---

## Answering "where is X?"

### A screen, a page, a button

Start at the route table:

```bash
grep -n "delivery" app/src/routes.ts
```

Every route is `{ path, load: () => import('./pages/...'), layout }`. The `load` path is the
file. The `layout` tells you which shell it renders inside.

The five surfaces, and what they're for:

| Prefix | Layout shell | Who uses it |
|---|---|---|
| `/erp/*` | `<gable-app-shell>` | Desktop ERP — counter, office, management |
| `/portal/*` | `<gable-portal-layout>` | B2B customer portal (contractors) |
| `/driver/*` | `<gable-driver-layout>` | Delivery drivers, mobile |
| `/yard/*` | `<gable-yard-layout>` | Warehouse / yard, handheld |
| `/pos` | *none* | Front-counter POS terminal, full screen |

If a route isn't in `routes.ts`, it belongs to a **converted app** and is declared in
`app/src/apps/<key>.ts`, spread in via `appRoutes()` from `app/src/apps/registry.ts`.
`millwork` and `governance` are converted so far.

### A backend behaviour

```bash
ls backend/internal/                          # the 41 modules
grep -rn "func (s \*Service)" backend/internal/order/service.go | head -20
```

Every module has the same shape — this is the single most useful thing to know:

| File | What's in it |
|---|---|
| `model.go` | The structs, statuses, and constants. **Read this first.** |
| `repository.go` | All SQL, via pgx. Nothing else touches the DB. |
| `service.go` | Business logic. Rules, validation, transactions. |
| `handler.go` | HTTP handlers **and** `RegisterRoutes(mux, mw)`. There is no `routes.go`. |

So: "what statuses can an order be in?" → `backend/internal/order/model.go`. "What happens
when an order is fulfilled?" → `backend/internal/order/service.go`, function `FulfillOrder`.
"What URL is that?" → `backend/internal/order/handler.go`, `RegisterRoutes`.

### How a request actually flows

```
browser page (app/src/pages/orders/OrderDetail.ts)
  → app/src/services/OrderService.ts
    → app/src/services/fetchClient.ts   (adds auth + base URL; never bare fetch)
      → HTTP /api/v1/orders/{id}
        → backend/cmd/server/main.go    (mux + middleware; the auth whitelist lives here)
          → backend/internal/order/handler.go
            → service.go   (rules)
              → repository.go  (SQL)
                → PostgreSQL
```

Trace it in both directions — that round trip explains most of the codebase.

### The wiring

`backend/cmd/server/main.go` is one long initializer. It is the answer to "how does anything
get connected?" and to "is my new endpoint live?". Grep it:

```bash
grep -n "RegisterRoutes" backend/cmd/server/main.go | head -40
grep -n "publicPaths\|whitelist" backend/cmd/server/main.go
```

An endpoint that isn't in a `RegisterRoutes` call there does not exist at runtime.

---

## The connector seam: `backend/pkg/apps/`

This is the part of the architecture worth understanding early, because it's the project's
whole strategy and it's also a **license boundary**.

Gable is turning its modules into installable **apps**: a module declares a manifest, the
`apps` table (migration 074) records which are installed, and an operator enables/disables
each per instance at **Tech Admin → Apps** (`/admin/apps`).

```bash
ls backend/pkg/apps/                 # apps.go, handler.go, registry.go, registry_test.go
head -30 backend/pkg/apps/apps.go    # the package doc explains the model
```

Two things to internalise:

1. **It's the seam third parties build against.** The point is that someone outside the
   project can ship an app without forking the core.
2. **It's licensed differently.** `backend/pkg/apps/` is `LicenseRef-OpenLBM-Connector-1.0`
   (permissive, no copyleft) carved out of the `backend/pkg/` Commons default — so plugging in
   doesn't drag copyleft across the boundary. Most specific path wins. See
   [`LICENSE-MAP.md`](../../../LICENSE-MAP.md) and the **`licensing-check`** skill.

Conversion recipe and phases: [`docs/modularization-blueprint.md`](../../../docs/modularization-blueprint.md)
§5 and §6. Converted so far: `millwork`, `governance`.

---

## Frontend specifics that will confuse you otherwise

- **Light DOM, not shadow DOM.** Components do `createRenderRoot() { return this; }` so
  Tailwind classes actually apply. If you expect shadow-DOM encapsulation, you won't find it.
- **All custom elements are prefixed `gable-`.**
- **Routing is a hand-rolled singleton** in `app/src/lib/router.ts` (popstate/pushState).
  Navigate with `router.navigate(path)`. Route params arrive as
  `@property({ attribute: 'route-id' })`.
- **State:** `@state()` for internal, `@property()` for external. Cross-component state lives
  in framework-agnostic singleton services under `app/src/services/`.
- **Never hardcode a colour.** Tokens are in `app/tailwind.config.js`. JetBrains Mono for all
  numbers, SKUs, prices, and dimensions; Inter for body text.
- **Money:** `formatCents()` in `app/src/lib/utils.ts` for ERP surfaces. Portal surfaces get
  dollars from the API and format directly. Mixing them renders `$738.87` as `$73,887.00`.

---

## Backend specifics that will confuse you otherwise

- **Router is stdlib `net/http.ServeMux`** (Go 1.22+ pattern matching), not Chi or gorilla.
- **Default Postgres port is 5434**, matching `docker-compose.yml` — not 5432.
- **Money conventions are not uniform.** ERP orders/invoices and the `account` module use
  `int64` cents; portal, quotes, and DailyTill use `float64` dollars — on some of the same
  columns. `CLAUDE.md` § "Money convention is not uniform across modules" has the table. Read
  it before touching anything financial.
- **AR balance is derived, not read.** Compute from open invoices
  (`invoice.OpenInvoiceStatuses`), not from `customers.balance_due` — that column is a
  secondary subledger figure and can drift.
- **`AUTH_MODE=dev` disables auth entirely** and treats every request as the seeded admin.
  That's how demo and staging run. It must never reach production; see `SECURITY.md`.
- **AI and routing keys are runtime-settable** from Tech Admin (DB-first via `system_settings`,
  env fallback). Features degrade gracefully when unset — don't add hard failures.

---

## When they ask about a specific module

Do this, then explain in their language:

```bash
M=order        # or invoice, delivery, inventory, pricing, pos, purchase_order, ...
ls backend/internal/$M/
head -60 backend/internal/$M/model.go
grep -n "func (s \*Service)" backend/internal/$M/service.go
grep -n "mux.HandleFunc\|RequireRole" backend/internal/$M/handler.go
grep -rn "$M\." backend/cmd/server/main.go | head
ls backend/internal/$M/*_test.go 2>/dev/null || echo "no tests yet"
```

Structure the answer as: **what this module owns** → **the main types and statuses** → **the
operations** → **who's allowed to call them** → **what it talks to** → **whether it's tested**.

If it has no tests, say so and mention the **`add-a-test`** skill — about half the modules
still have none, and adding one is a real, welcome contribution.

---

## Deeper reading, in the order that actually helps

1. [`CLAUDE.md`](../../../CLAUDE.md) — conventions, gotchas, and the current backlog. The
   highest-value file in the repo.
2. [`docs/architecture.md`](../../../docs/architecture.md) — module boundaries as built,
   API strategy, hosting. Forward-looking sections are explicitly labelled.
3. [`docs/modularization-blueprint.md`](../../../docs/modularization-blueprint.md) — the
   installable-apps direction and the per-module conversion recipe.
4. [`docs/database-erd.md`](../../../docs/database-erd.md) — the schema.
5. [`docs/design-system.md`](../../../docs/design-system.md) — colours, type, component
   patterns.

---

## Ground rules

- **Show real paths and line numbers.** `backend/internal/order/service.go:412`, not "the
  order service".
- **Read the file before you describe it.** Do not explain from the module name.
- **Say when something is aspirational.** NATS events, the full apps conversion, and parts of
  `docs/architecture.md` describe future design. Mark it.
- **Match their level.** A yard operator asking "where does the delivery total come from?"
  wants the data path in English, not a Go type signature.
- **End with the lookup.** "I found that by grepping `routes.ts` for the URL" is the part that
  compounds.
