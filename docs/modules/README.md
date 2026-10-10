<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Module documentation

One page per module that has been converted onto the wire contract of
ADR 0001 in this repository. A page is a guided tour for a person who
runs the module or extends it: what the module is for in a lumber and
building materials yard, the routes it owns, the main resource's
fields, the lifecycle and its events, the scopes and roles that reach
it, the ADRs that govern it, and a short path to try it locally.

The wire contract, the recipe that modules follow to convert, and the
machine key rules are in:

- [`../adr/0001-wire-contract.md`](../adr/0001-wire-contract.md): the
  list envelope, cursors, the error envelope, enums, money and
  quantity, document numbers, idempotency, revisions, the in-place
  rule.
- [`../adr/0002-machine-keys.md`](../adr/0002-machine-keys.md):
  scoped machine keys, the path segment rule.
- [`../adr/0003-events-outbox.md`](../adr/0003-events-outbox.md):
  the events table, the ordering guarantee, the writer, the read API.
- [`../adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md):
  the shared line shape, totals and tax, currency, the order and
  invoice lifecycles, the AR subledger, the gapless counter.
- [`../adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md):
  units, the conversion pair, the product on the wire, the inventory
  list.
- [`../adr/0008-inventory-identity-and-vendor-intake.md`](../adr/0008-inventory-identity-and-vendor-intake.md):
  the inventory identity and the vendor intake (cycle 4, in flight
  at the time of writing).
- [`../refactor/MODULE-RECIPE.md`](../refactor/MODULE-RECIPE.md): the
  recipe a module follows to land on the contract in place.

The pages:

| Module | Page | What it does | Converted by |
|---|---|---|---|
| Quotes | [quotes.md](quotes.md) | The priced offer the sales rep sends before a sale. | R1-15 (template). |
| Customers | [customers.md](customers.md) | The customer master, ship-tos, contacts, payment terms. | C2-1, C5-1b. |
| Orders | [orders.md](orders.md) | The sales document, allocation, back orders, fulfilment. | C2-2. |
| Invoices and credit memos | [invoices.md](invoices.md) | The bill, the credit memo, the AR subledger, the general ledger. | C2-3. |
| Products | [products.md](products.md) | The catalog, the PIM, the kit definition. | C3-1. |
| Inventory levels | [inventory.md](inventory.md) | The on hand, allocated, available read. | C3-1b. |
| CRM activities | [crm-activities.md](crm-activities.md) | The call, meeting, email, note trail on a customer. | C5-1b. |
| Projects | [projects.md](projects.md) | The customer's job in the portal. | C5-1b. |
| Configurator | [configurator.md](configurator.md) | The rule engine for non-stock SKUs. | C5-1b. |
| Millwork | [millwork.md](millwork.md) | The millwork option catalog. | C5-1b. |
| Payments | [payments.md](payments.md) | Today; the conversion is C2-4 (in flight at the time of writing). | pending C2-4. |

## Conventions every page uses

- The SPDX header block at the top of every page is the same one
  `MODULE-RECIPE.md` carries.
- Money is integer cents under `_cents` field names. Unit prices
  are integers at scale 4 under `_ten_thousandths` field names.
  Quantities and rate percents are decimal strings with at most four
  fraction digits.
- Lifecycles are lowercase on the wire. The database keeps the
  uppercase CHECK vocabulary, mapped at the boundary.
- Events are written through the outbox as the last statement of
  the mutation's transaction. The event constants live in the
  module's `service.go` (or `model.go` when the service is small).
- The scopes a machine key needs are `<segment>:read` for `GET` and
  `HEAD`, and `<segment>:write` for every other method, where
  `<segment>` is the first path segment under `/api/v1/` (for the
  pages here: `invoices`, `quotes`, `orders`, `customers`,
  `products`, `inventory`, `activities`, `ship-tos`, `contacts`,
  `payment-terms`, `price_levels`, `credit-memos`, `configurator`,
  `millwork`, `payments`). The user guard is composed at the serve
  layer in `core/internal/app/serve/serve.go` and
  `core/internal/app/serve/wire_branch_wall.go`; the guard a page
  lists is the one the wiring code applies.
- No calendar dates, no schedule durations, no em or en dashes.
  Examples use the repository's own seed data or invented generic
  values.

## Where the fragments live

Every route on the wire is in `core/api/fragments/<module>.yaml`,
assembled into `core/api/openapi.yaml` by `core/api/tools/merge`. The
generated route census is `core/api/ROUTES.txt`, with the parser in
`core/cmd/census`. The page routes match the registered handles in
`core/internal/<module>/handler.go` and the census row by row.

## Where the migrations live

The migration that brought each module onto the contract is named in
its page. The list of migrations is `core/migrations/`. The down
files are in `core/migrations/down/`. The `core/migrations/NNN_*.sql`
files are the only authoritative source for column types and
constraints; the page cites the migration and the ADR section, and
the page describes the wire rather than restating the SQL.

## What this index does not cover

- The reporting module and the dashboard, both of which read across
  modules and have their own shape.
- The integration seam under `/api/integration/*`, which keeps its
  own published contract (ADR 0001 section 10).
- The agent-to-agent JWS routes under `/api/v1/a2a/*`, which keep
  their own published contract.
- The portal and partner surfaces, which carry their own sessions
  and their own shape. The project module is the one portal-only
  module with a page here; the other portal endpoints (cart, catalog,
  dashboard, deliveries, invites, invoices, orders, quotes, users
  and the rest) sit under the `internal/portal` package in the
  route census.
- The accounts, GL, AP, bank reconciliation, matching, purchase
  order, deposit, document print, EDI, governance, accounting
  and AI surfaces, which are not in the brief's list.
