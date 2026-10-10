<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Quotes

A quote is the priced offer a sales rep sends to a customer before a sale.
It carries the line items, the prices, the units, the total and the
expiration, and it is the first priced document. The customer accepts
or rejects the offer through the portal or the integration seam; an
accepted quote becomes an order through
`POST /api/v1/quotes/{id}/convert`. The lifecycle spans draft, sent,
accepted, rejected, expired and reopened, every transition writing one
outbox event.

This module is the wire template. The recipe in
[`docs/refactor/MODULE-RECIPE.md`](../refactor/MODULE-RECIPE.md) was
written from it and the wire contract in
[`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) was first
applied here. The Go code lives in `core/internal/quote/`. The migration
that brought the table onto the contract is
`core/migrations/090_quotes_wire_contract.sql`.

## What it does in a yard

A yard's sales team prices material for builders and contractors. The
quote is the document the customer signs off on. The line carries
the unit price and the unit cost; the order line is where overrides
and discounts are recorded with a reason, so margin reporting later
can ask "why did this line not hit list?" and get an answer.

## Routes

Every route is in `core/api/fragments/quote.yaml` and the registered
handles are in `core/internal/quote/handler.go`. The route census
(`core/api/ROUTES.txt`) lists each one under the `internal/quote`
package.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/quotes` | Cursor list of quotes, newest first; status and customer_id filter. |
| POST | `/api/v1/quotes` | Create a draft quote, priced through the platform rule, writes `quote.created`. |
| GET | `/api/v1/quotes/analytics` | Aggregated quote analytics; money in cents. |
| GET | `/api/v1/quotes/{id}` | One quote with its lines and revision as ETag. |
| PUT | `/api/v1/quotes/{id}` | Replace a draft quote's header and lines, on the client's revision. |
| POST | `/api/v1/quotes/{id}/transitions` | Move a quote along its lifecycle, on the client's revision. |
| POST | `/api/v1/quotes/{id}/convert` | Accept and create the order in one transaction. |
| GET | `/api/v1/quotes/{id}/file` | Download the original parsed file. |

The exposure routes at `/api/v1/quotes/exposure` and
`/api/v1/quotes/{id}/exposure/...` belong to the pricing module and
register under `internal/pricing` in the route census, even though
their paths sit under `quotes`.

## The main resource

`Quote` (see `core/api/fragments/quote.yaml` `components.schemas.Quote`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The document id. |
| `number` | text | The document number, minted from a sequence, prefix `Q-`, padded to six (`Q-000123`). |
| `status` | lowercase enum | `draft`, `sent`, `accepted`, `rejected`, `expired`. The wire name is `status`; the database column is `state`. |
| `revision` | integer | Starts at 1, increments on every write. Returned as ETag. |
| `branch_id` | UUID | The branch the quote belongs to. |
| `customer_id` | UUID | The customer the quote is for. |
| `customer_name` | text | Snapshot of the customer name. |
| `job_id` | UUID, nullable | The project (job) this quote is for. |
| `total_cents` | integer | The customer's total, in minor units of the customer's currency. |
| `freight_cents` | integer | The freight amount. |
| `margin_total_cents` | integer | The computed margin across all lines. |
| `delivery_type` | lowercase enum | `pickup`, `delivery`. |
| `vehicle_id` | UUID, nullable | The vehicle that will deliver, when known. |
| `vehicle_name` | text, nullable | The vehicle display name. |
| `source` | text | `manual`, `ai`, or `portal`. |
| `expires_at` | timestamp, nullable | When the quote stops being valid, RFC 3339 UTC. |
| `sent_at` | timestamp, nullable | When the quote was sent. |
| `accepted_at` | timestamp, nullable | When the quote was accepted. |
| `rejected_at` | timestamp, nullable | When the quote was rejected. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC, microsecond precision. |
| `original_filename` | text, nullable | The name of the original parsed file, when AI sourced. |
| `original_content_type` | text, nullable | The MIME type of the original parsed file. |
| `parse_map` | array, nullable | The AI parse mapping data. |
| `exposure_state` | lowercase enum | The pricing module's worst exposure rollup (`ok`, `flagged`, `escalated`, `ack_required`, `acknowledged`, `blocked`, `overridden`). |
| `exposure_cents` | integer | The exposure dollar value at the rollup. |
| `exposure_last_checked_at` | timestamp, nullable | When the exposure scanner last looked at this quote. |
| `lines` | array of `QuoteLine` | The priced lines, in position order. |

A `QuoteLine` carries:

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The line id. |
| `quote_id` | UUID | The parent quote. |
| `product_id` | UUID, nullable | The product, null for a special order line the dealer does not stock. |
| `sku` | text | Snapshot of the product SKU. |
| `description` | text | Snapshot of the description. |
| `customer_note` | text, nullable | The customer's free text note on the line. |
| `quantity` | decimal string | Scale 4. |
| `uom` | uppercase enum | The sale unit (`PCS`, `EA`, `LF`, `SF`, `BF`, `MBF`, `SQ`, `BOX`, `CTN`, `RL`, `GAL`, `LBS`, `BAG`, `BUNDLE`, `PAIR`, `SET`). |
| `price_uom` | text | The price unit. |
| `uom_qty` | decimal string | Scale 4. |
| `price_uom_qty` | decimal string | Scale 4. |
| `unit_price_ten_thousandths` | integer | The price per `price_uom`. |
| `line_total_cents` | integer | The extension, rounded once. |
| `unit_cost_ten_thousandths` | integer | The cost per stocking unit. |
| `created_at` | timestamp | RFC 3339 UTC. |

The conversion pair (`uom_qty`, `price_uom_qty`) is mandatory; when
the units agree, the pair is 1 and 1.

### Money and quantity conventions

Money is integer cents under `_cents` field names; unit prices are
integers at scale 4 under `_ten_thousandths`; quantities are decimal
strings with at most four fraction digits. No decimal point appears in a
money field. Floats never carry money. The full rule is ADR 0001
sections 7 and 7a.

## Lifecycle and transitions

The wire vocabulary is lowercase; the database keeps its uppercase CHECK.
The transitions (`core/internal/quote/service.go` `validateStateTransition`)
are:

| From | To | Event |
|---|---|---|
| `draft` | `sent` | `quote.sent` |
| `draft` | `accepted` | `quote.accepted` |
| `draft` | `rejected` | `quote.rejected` |
| `draft` | `expired` | `quote.expired` |
| `sent` | `accepted` | `quote.accepted` |
| `sent` | `rejected` | `quote.rejected` |
| `sent` | `expired` | `quote.expired` |
| `rejected` | `draft` | `quote.reopened` |
| `expired` | `draft` | `quote.reopened` |

`accepted` is terminal. A transition the lifecycle does not allow is
`409 invalid_state_transition`. The body is `{"to": "sent", "revision":
n}` with the `If-Match` header carrying the same number, and the
idempotency key on every mutating request. Events are defined in
`core/internal/quote/service.go` (`EventCreated`, `EventSent`,
`EventAccepted`, `EventRejected`, `EventExpired`, `EventReopened`).

## Events the module writes

Every event is one outbox row in the mutation's transaction
([ADR 0003](../adr/0003-events-outbox.md)). The summary is a small
object: number, customer id, status, revision, total in cents, and,
on a transition, from status. The event type strings are the
constants in `core/internal/quote/service.go`. Outbox drain consumers
(the exposure notifier among them) read by type.

## Scopes, roles and keys

A machine key reaching the module needs `quotes:read` for `GET` and
`HEAD`, and `quotes:write` for every other method (ADR 0002; the
segment is the first path segment under `/api/v1/`). The user guard
is registered at the serve layer as `admin`, `owner`, `sales` (see
`core/internal/app/serve/wire_branch_wall.go` at the `wall.quotes(mux,
...)` line, the quote module being the wire template the recipe names).
A key without the scope is `403 forbidden`; the route names the path
segment of the scope so the audit row carries it.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12: the list envelope, the cursor, the error envelope, the enum casing, the money and quantity rules, the document number, the idempotency key, the revision precondition, the timestamps.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2: the path segment rule for scope.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5: the table, the ordering guarantee, the writer, the read API.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 5.8: the convert route and the convert route's pair refusal.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) sections 3, 4: the unit set, the pair arithmetic, the inexact refusal (section 3.4) and the tally (section 4.3).

## How to try it locally

The repository's own seed and the local make targets are the only way to
exercise the module end to end; the integration seam under
`/api/integration/*` keeps its own published contract and is not the path
here. `make up` builds and starts the local stack (Postgres, migrate and
seed, `core serve`, `core worker`, the web front door) on
http://127.0.0.1:8080 with `AUTH_MODE=dev`; `make down` removes it. To run
the core from source instead: `make db`, `make migrate`,
`DEMO_SEED=1 make seed`, then `cd core && AUTH_MODE=dev go run ./cmd/server`.
The `make up` and `make db` workflows use different compose projects and
volumes, so the `make db` data is never truncated or removed by `make up`
or `make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Then, with a sales role bearer and the seeded branch, create a draft and
walk it to accepted:

```
curl -X POST http://localhost:8080/api/v1/quotes \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @quote-create.json
```

The goldens under `core/internal/characterization/testdata` pin the wire
shapes for the module's scenarios; the wire tests in
`core/internal/quote/wire_test.go` pin the platform and HTTP behaviour.