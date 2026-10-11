# Architecture Specification

> Rewritten July 2026 to describe the system **as built**. Forward-looking
> design is explicitly labelled. For the installable-apps direction, see
> [`modularization-blueprint.md`](./modularization-blueprint.md).

## 1. System Principles
- **Modular Monolith:** Single deployment binary; 54 directories under `core/internal/` (47 domain modules plus `app`, `apicontract`, `characterization`, `events`, `platform`, `routecensus`, `testutil`), wired in `core/internal/app/serve/serve.go`.
- **Zero-Trust Modules:** Modules never access another module's database tables directly — cross-module needs go through the other module's service or repository types.
- **Synchronous Interop:** Every inter-module *write* is a synchronous Go call. The one asynchronous path is the transactional outbox, drained by the worker (§4.2). There is no message broker and no NATS client in the codebase.
- **Interface Seams Where They Earn Their Keep:** Most coupling is concrete `*Service` injection; consumer-defined interfaces + adapters in `core/internal/app/serve/serve.go` exist where cycles had to be broken (`quote.AutoPOService`, `delivery.DeliveryNotifierInterface`, `pos.PriceCalculator`, `gl` → `integrations.GLAdapter`).
- **Apps Platform (Phase 0 landed):** Modules are becoming *apps* — declared manifests, a DB-backed registry (`apps` table), per-instance enable/disable, and an Apps admin page. See the blueprint for the full model.

## 2. Technology Stack
- **Backend:** Go 1.26+, stdlib `net/http.ServeMux` (Go 1.22+ pattern routing) — **not** Chi. Middleware lives in `pkg/middleware`.
- **Database:** PostgreSQL 16+ via pgx v5.
    - Extensions in use: `uuid-ossp` (migration 001), `ltree` (migration 049). (`pgvector`/`postgis` are not installed; adopt only when a feature needs them.)
- **Frontend:**
    - **Core:** Lit 3 Web Components + TypeScript 5.9 + Vite 7 — **not** React. Custom SPA router (`web/apps/desk/src/lib/router.ts`).
    - **Styling:** Tailwind CSS 3.4 + custom design tokens (no Shadcn).
    - **Charts:** Chart.js 4. **Maps:** Leaflet.
    - **Components:** Light DOM (`createRenderRoot() { return this; }`) so Tailwind classes apply directly.

## 2a. Hosting

This repository ships **example** Digital Ocean App Platform specs (PaaS,
Dockerfile-based) for self-hosting — not a live deployment. Every hostname,
repository and database name in them is a placeholder (`your-org/gable`,
`*.example.com`, `your-db-cluster`), and both run `AUTH_MODE=production`
against a placeholder `JWKS_URL`. Operational notes live in `.do/README.md`.

| Spec | Tracks branch | Example domain | Logical DB |
|---|---|---|---|
| `.do/app-demo.yaml` | `main` | `demo.example.com` | `gable_demo_db` |
| `.do/app-staging.yaml` | `staging` | `staging.example.com` | `gable_staging_db` |

Both examples reference one DO Managed Postgres 16 cluster (the `gable-db`
app resource) with isolated logical databases, so several environments can
share a cluster. Both apps share the backend Docker image — the same image
runs `main` (API server) as a service and `migrate && seed` as a post-deploy
job.

## 3. Module Boundaries (as built)

Actual packages under `core/internal/` (the older draft of this document
described an idealized `sales`/`finance`/`logistics` grouping that never
existed as packages). Grouped by domain:

| Domain | Packages | Responsibility |
|--------|----------|---------------|
| Catalog & Inventory | `product`, `inventory`, `pim`, `location` | Products, UOM, stock quants/moves, cycle counts, AI product content, locations/branches |
| Sales | `quote`, `order`, `pricing`, `configurator`, `millwork`, `parsing`, `vision` | Quotes, orders, pricing rules/categories/rebates, millwork configurator, material-list document parsing (`POST /api/v1/parsing/upload`), blueprint scanning (`POST /api/v1/vision/scan`) |
| Finance | `invoice`, `payment`, `account`, `gl`, `ap`, `bankrecon`, `tax`, `matching` | Invoicing, payments, AR subledger + customer deposits/prepayments, general ledger, AP, bank reconciliation, tax, 3-way matching |
| Purchasing | `purchase_order`, `vendor`, `edi` | POs (with the auto-reorder scheduler in `purchase_order/scheduler.go`), vendors, EDI X12 (832/846/850 in `edi/` and `integrations/edi/x12/`) |
| Logistics | `delivery` | Dispatch, routing (OpenRouteService), fleet, POD |
| Front-of-house | `pos`, `dashboard`, `reporting`, `document` | POS terminal, dashboards, report builder, generated documents |
| CRM & People | `crm`, `salesteam`, `customer` | Activities, sales teams, customer master |
| External surfaces | `portal`, `partner`, `project`, `integrations` | B2B portal, co-op partner API, portal projects, service-to-service integration |
| Platform (not apps) | `config`, `ai`, `domain`, `notification`, `techadmin`, `governance` | Env config, OpenRouter AI client + KeyStore, shared types, email/SMS stubs, admin settings, RFC governance |

Platform primitives live in `core/pkg/`: `apps` (app registry/gating),
`middleware` (auth/branch/cors/rate-limit/idempotency/…), `database`,
`audit`, `metrics`, `httputil`, `pagination`, `branchctx`.

`internal/testutil` is a test-only helper package (DB/transaction fixtures),
not a domain module.

## 4. Inter-Module Communication

### 4.1. Synchronous calls (everything today)
Direct Go calls within the same process, usually a concrete `*Service`
dependency injected at construction in `core/internal/app/serve/serve.go`;
occasionally an interface defined by the consumer with an adapter in the same
file.

**Example:** Order fulfilment posts an invoice, its GL entry and the AR
subledger debit in one transaction (`order.Fulfil` →
`invoice.CreateFulfilmentInvoice` → `account.PostInvoice`, which writes both
the GL entry and the subledger debit). A payment is a separate path
(`payment.Service` → `account.RecordPayment`).

### 4.2. Asynchronous events: the transactional outbox
`core/pkg/eventbus` defines the event vocabulary (`Event`, `Handler`, the
subject constants and NATS style subject matching, where `*` is one token and
`>` is one or more trailing tokens) and an in-process, in-memory `Bus`. No
production code constructs that bus; it is a tested seam kept for a possible
broker. All production events travel over the transactional outbox.

There is still **no broker**: nothing is added to `go.mod`, `docker-compose.yml`
or the deploy specs, and Gable remains a single Go binary.

The transport is the **transactional outbox** (`core/pkg/outbox`, plus the
read API at `core/internal/events` serving `GET /api/v1/events`): a mutation
writes its domain event into the `events_outbox` table inside its own
transaction (ADR 0003), and the worker role
(`core/internal/app/worker/worker.go`) runs the `outbox.DrainRunner` to
deliver committed rows to subscribers past each subscriber's cursor. Both
subscribers attach through the drain (`drain.Subscribe` in `worker.go`), each
under its durable name, and handlers receive an `eventbus.Event`:

| Subject pattern | Durable name | Handler |
|---|---|---|
| `quote.exposure.>` | `exposure-notifier` | `notification.ExposureNotifier` (salesperson and customer email) |
| `purchase_order.received` | `order-allocation-requests` | `order.Service.HandlePurchaseOrderReceived` |

The price exposure service and scanner write their `quote.exposure.*` events
as outbox rows (`outbox.Event`, through each one's `EventRecorder`). The drain and its retention purge run only in the worker, so
serve replicas never contend for the cursors.

What the outbox drain provides:

| Property | Outbox drain |
|---|---|
| Durability | Rows are committed with the mutation and survive a restart |
| Cross-process delivery | Rows written by any serve replica are delivered by the worker |
| Redelivery | A handler error leaves the cursor before the row and retries it; after `DefaultDrainMaxAttempts` (10) consecutive failures the row is parked in `event_subscriber_parked` and the cursor moves on |
| Ordering | Each subscriber receives rows in position order |
| Delivery | At least once: a crash between a handler's success and the cursor advance redelivers the row, so a handler that must not repeat an effect deduplicates on the event id |

Consequences for design: **every inter-module write stays a synchronous Go
call**, and an event carries a side effect, never the write itself. Every
durable fact in the exposure subsystem lives in Postgres
(`quote_exposure_events`, `price_escalators.current_state`, the `quotes`
rollup columns), and the nightly safety-net scan (`quote.ExposureScheduler`)
recomputes state from Postgres. Do not design features that assume eventual
consistency between modules.

## 5. API Strategy
- **Style:** RESTful JSON.
- **Surfaces:** `/api/v1/*` (ERP, JWT), `/api/portal/v1/*` (B2B portal — portal-session auth; `project` also mounts here), `/api/partner/v1/*` (co-op partner API), `/api/integration/*` (service-to-service via `X-Integration-Key`), `/api/v1/a2a/*` (Brain agent-to-agent JWS).
- **Router:** Go stdlib `http.ServeMux` with method+path patterns; per-module registration via `RegisterRoutes(mux, roleGuard…)`, converging on the gated `apps.Router` as modules convert.
- **Auth:** JWT verified against JWKS (`pkg/middleware.NewAuthMiddleware`); `AUTH_MODE=dev` is a **local-development-only** pass-through that disables auth entirely (see `SECURITY.md`). Role gating via `middleware.RequireRole`.
- **App gating:** converted modules' routes 404 with `{"error":"app_disabled"}` when the app is disabled in the registry.
- **Config:** Environment variables with `godotenv` fallback.

## 6. Frontend Architecture
- **Routing:** custom singleton router (`web/apps/desk/src/lib/router.ts`) + flat route
  table (`web/apps/desk/src/routes.ts`) with lazy `import()` per route. Surfaces:
    - `/erp/*` — ERP desktop (`<gable-app-shell>`) *(ERP pages actually mount at root paths like `/orders`, with `layout: 'erp'`)*
    - `/portal/*` — B2B dealer portal (`<gable-portal-layout>`)
    - `/driver/*` — Mobile driver app (`<gable-driver-layout>`)
    - `/yard/*` — Warehouse/yard app (`<gable-yard-layout>`)
    - `/pos` — Point of sale terminal (no layout)
- **Apps:** converted apps declare routes + nav in `web/apps/desk/src/apps/<key>.ts`;
  the app registry feeds the route table, tag resolution, and generated
  sidebar entries, filtered by enablement from `GET /api/v1/apps`.
- **State:** component-local state + framework-agnostic singleton services (`web/apps/desk/src/services/`); HTTP via `services/fetchClient.ts` only.
- **AI Keys:** Managed via Tech Admin UI → stored in `system_settings` → resolved dynamically by backend `ai.KeyStore`.

## 7. Partner & Governance (present state vs. vision)
- **Built:** `internal/partner` exposes a read-only co-op partner API
  (`/api/partner/v1/dashboard|quotes`); `internal/governance` manages RFCs
  (`/api/v1/governance/rfcs`) with AI assistance (`governance/ai.go`);
  governance UI pages exist under `web/apps/desk/src/pages/governance/`.
- **Planned (not built):** AI-mediated impact analysis of proposed changes,
  backlog orchestration, and a federated catalog sync layer for co-ops to
  push master SKU data to member dealer instances.

## 8. Legacy Interop & Migration Strategy

- **No third-party vendor API specifications ship with this repository, and
  none will.** Import/sync adapters for Epicor BisTrack, ECI Spruce and DMSi
  Agility remain a goal, but their API definitions are those vendors'
  copyrighted and trademarked material — we have no right to redistribute
  them. Build adapters against each vendor's own documentation under your own
  licensed access to their APIs. Do not expect this repo to supply the specs.
- **Built today —** `internal/integrations`:
    - **AI_LM dispatch contract** (`ailm.go`, `handler.go`): ten
      `/api/integration/*` endpoints (five GET, five POST): fleet, drivers,
      a date's confirmed orders, catalog with weight/geometry, quote pricing
      + creation, quote accept-and-convert, staff validation, locations, and
      route write-back, consumed by the separate `gable-ai-lm` service. Wire
      shapes are pinned against golden fixtures by `ailm_contract_test.go`.
    - **GL adapter seam** (`integrations/gl/`): a `GLAdapter` interface with a
      logging mock (`mock.go`) and a QuickBooks Online stub (`qbo.go`).
      `internal/gl` depends on the interface, never on a concrete adapter.
    - **X12 EDI** (`integrations/edi/x12/850.go` for outbound POs; the 832/846
      catalog and trading-partner logic lives in `internal/edi`).
- **Agent-to-agent (separate surface):** `POST /api/v1/a2a/purchase-order`
  accepts JWS-signed inbound POs (`purchase_order.NewA2AReceiver`, wired in
  `core/internal/app/serve/serve.go`). It is not part of `internal/integrations`.
- **Planned (not built):** per-module `adaptors/` mappers and a dedicated sync
  engine for bi-directional phase-in from legacy systems. Design these as
  *apps* once the platform work (blueprint Phases 1–2) is in place.
