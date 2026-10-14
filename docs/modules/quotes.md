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
accepted, rejected and expired; rejected and expired reopen to draft, every
transition writing one outbox event.

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
| GET | `/api/v1/quotes` | Cursor list of `QuoteSummary` items, newest first; status and customer_id filter; the response is `QuotePage` (no `lines`, no file fields, no exposure fields); `total` only under `include=total`. |
| POST | `/api/v1/quotes` | Create a draft quote, priced through the platform rule, writes `quote.created`. |
| GET | `/api/v1/quotes/analytics` | Aggregated quote analytics; counts and rates (`total_quotes`, `draft_count`, `sent_count`, `accepted_count`, `rejected_count`, `expired_count`, `conversion_rate`, `ai_sourced_count`, `ai_conversion_rate`, `manual_conversion_rate`, `avg_days_to_close`) and money in cents (`avg_margin_accepted_cents`, `avg_margin_rejected_cents`, `total_quote_value_cents`, `total_accepted_value_cents`); `trend_data` rows carry `total_value_cents` and `accepted_value_cents`. |
| GET | `/api/v1/quotes/{id}` | One quote with its lines and revision as ETag. |
| PUT | `/api/v1/quotes/{id}` | Replace a draft quote's header and lines, on the client's revision. |
| POST | `/api/v1/quotes/{id}/transitions` | Move a quote along its lifecycle, on the client's revision. |
| POST | `/api/v1/quotes/{id}/convert` | Accept the quote (the `quote.accepted` transition) and create the order in one transaction, answering 201 with the order (ETag and Location); the quote's `freight_cents` becomes one `FREIGHT` charge line on the order; a stocked line sold in a unit other than its product's stocking unit is 409 with the `unit_not_stock_unit` blocker until cycle 3; a quote that already has an order not cancelled is 409 with the `already_converted` blocker; events `quote.accepted` then `order.created`. |
| GET | `/api/v1/quotes/{id}/file` | Download the original parsed file. |
| PUT | `/api/v1/quotes/{id}/file` | Attach the AI parse's original file after promotion; `application/octet-stream` body, 5 MiB bound (413 above it); the route takes no query parameter (any is a 400 `unsupported_query_parameter`); the revision precondition is the `If-Match` header, required (428 without); only while the quote is in status `draft`, else 409 with the `quote_not_draft` blocker; the quote is returned with its new ETag and revision plus one; the audit row `quote.file_attached` is written and no outbox event; the route is gated by `quotes:write`. |

The exposure routes at `/api/v1/quotes/exposure` and
`/api/v1/quotes/{id}/exposure/...` belong to the pricing module and
register under `internal/pricing` in the route census, even though
their paths sit under `quotes`. The draft routes at
`/api/v1/drafts/quotes` and `/api/v1/drafts/quotes/{id}[/...]` belong
to the drafts module and register under `internal/quote` (the
promotion route turns a quote draft into a quote in one transaction);
the draft routes are gated by `quotes:propose` and `quotes:commit`
(the propose and commit scopes of ADR 0007 section 5.1: draft read,
draft write and the draft link resolver admit both scopes, while
`promote` is reserved to `quotes:commit`), the entity routes above
keep the `<module>:read` / `<module>:write` pair and the rule "`quotes:write`
is the first path segment, anything but GET and HEAD" applies to the
entity routes only. The draft routes are documented in
[`docs/modules/drafts.md`](drafts.md) and are listed here only so the
route census and this page agree on the package that owns the paths.

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
| `total_cents` | integer | The customer's total, in minor units. |
| `freight_cents` | integer | The freight amount, cleared for a pickup. |
| `margin_total_cents` | integer | The margin total the client sent on create (default 0); create-only, a PUT refuses it, and it is not recomputed from the lines. |
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
| `exposure_cents` | integer | The exposure value at the rollup, in integer cents. |
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
| `uom` | uppercase pattern | The sale unit, regex `^[A-Z]{1,6}$`, a code from the units catalogue (ADR 0006 section 2.1: the seed inserts 23 codes including `EA`, `PCS`, `PAIR`, `DOZ`, `C`, `M`, `SET`, `BOX`, `CTN`, `BAG`, `BUNDLE`, `RL`, `LF`, `SF`, `SQ`, `CF`, `CY`, `GAL`, `LBS`, `CWT`, `TON`, `BF`, `MBF`; dealers add their own). On a product line the value must be one of the product's own sale units (ADR 0006 sections 3.2 and 3.3). |
| `price_uom` | text | The price unit. |
| `uom_qty` | decimal string | Scale 4. |
| `price_uom_qty` | decimal string | Scale 4. |
| `unit_price_ten_thousandths` | integer | The price per `price_uom`. |
| `line_total_cents` | integer | The extension, rounded once. |
| `unit_cost_ten_thousandths` | integer | The cost per stocking unit. |
| `stock_uom` | text, nullable | The product's stocking unit at the line's create (ADR 0006 section 3.4); null on a line without a product. |
| `stock_quantity` | decimal string, nullable | The line's quantity converted into the stocking unit, exact at scale 4 (R5); null on a line without a product. |
| `tally` | object, nullable | The pieces by length a random length line carries (ADR 0006 section 4): `thickness_in`, `width_in`, `rows` (pieces and length_ft), `linear_feet`, `board_feet`. Null on every other line. |
| `created_at` | timestamp | RFC 3339 UTC. |

The conversion pair (`uom_qty`, `price_uom_qty`) is mandatory; when
the units agree, the pair is 1 and 1.

### Create and update body

`QuoteRequest` is the body of `POST /api/v1/quotes` and
`PUT /api/v1/quotes/{id}` (`core/api/fragments/quote.yaml`
`components.schemas.QuoteRequest`). Required: `customer_id`, `lines`.

| Field | Wire form | Note |
|---|---|---|
| `branch_id` | UUID | Create-only. |
| `customer_id` | UUID | The customer the quote is for. |
| `job_id` | UUID, nullable | The project (job), if any. |
| `expires_at` | timestamp, nullable | When the quote stops being valid, RFC 3339 UTC. |
| `delivery_type` | lowercase enum | `pickup` clears `freight_cents` and `vehicle_id`. |
| `freight_cents` | integer | Non negative; cleared when `delivery_type` is `pickup`. |
| `vehicle_id` | UUID, nullable | The vehicle that will deliver; null on a pickup. |
| `source` | enum | `manual` or `ai`; create-only. |
| `margin_total_cents` | integer | The margin total the client sends on create (default 0); create-only, never recomputed. |
| `original_file` | base64 string | The original upload, up to 5 MB; create-only. |
| `original_filename` | text | Create-only. |
| `original_content_type` | text | Create-only. |
| `parse_map` | array, nullable | The AI parse mapping; create-only. |
| `revision` | integer | The PUT precondition beside `If-Match`; a create carrying one is a 400. |
| `lines` | array of `QuoteLineRequest` | The quote's lines; see the line request below. |

A PUT refuses the create-only fields
(`branch_id`, `source`, `margin_total_cents`, `original_file`,
`original_filename`, `original_content_type`, `parse_map`) with a 400
naming each one it carries. A create carrying `revision` is refused
with a 400 naming `revision`.

### Line request

`QuoteLineRequest` is one line of `QuoteRequest` (`core/api/fragments/quote.yaml`
`components.schemas.QuoteLineRequest`). The only required field is
`unit_price_ten_thousandths` (scale 4, min 0). The remaining fields
default as the schema lays out.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The line id, kept on a PUT to address an existing line. |
| `product_id` | UUID, nullable | The product; `sku` and `description` default from it (required when absent). |
| `sku` | text | Defaulted from `product_id`. |
| `description` | text | Defaulted from `product_id`. |
| `customer_note` | text, nullable | A portal user's free text note on the line. |
| `quantity` | decimal string | Scale 4; required unless the line carries a `tally`. |
| `uom` | uppercase pattern | Defaulted to the product's sale unit on a product line; a line with neither `uom` nor `product_id` is a 400 naming `lines[i].uom`. |
| `price_uom` | uppercase pattern | Defaulted to the product's price unit on a product line, to `uom` on the others. |
| `uom_qty`, `price_uom_qty` | decimal string, decimal string | The conversion pair; the server resolves the pair from the product's unit set on a product line (ADR 0006 section 3.3), a sent pair must equal the resolved one as a ratio, and a line without a product carries the client's pair, required when the two units differ unless the standard sizes derive it. |
| `unit_price_ten_thousandths` | integer | Scale 4, min 0; the line's only required field. |
| `tally` | `TallyRequest` object | The pieces by length; rows only. |

`TallyRequest` (`core/api/fragments/quote.yaml`
`components.schemas.TallyRequest`): `rows` is required (maxItems 100);
`thickness_in`, `width_in`, `linear_feet` and `board_feet` are read only,
and one in a request is a 400 naming the field.

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

A user who sends no `X-Branch-Id` while `default_branch_required` is off
is held to the user's own grants (ADR 0007 section 5.7): the by id and by
number reads answer the module's `403 forbidden` naming `id` ("quote is
outside the branches this caller may target") for a quote of a branch the
user does not hold, the lists carry only the granted branches' rows, and a
create whose body names no `branch_id` takes its branch from the user's
grants (exactly one granted branch writes there; several refuse with a
`400 validation_failed` naming `branch_id`; none is refused), never the
deployment default.

A key bound to one branch (ADR 0007 section 5.5) may create or update a
quote only for a customer its branch holds, and only for a job that
customer owns (`POST /quotes`, `PUT /quotes/{id}`: `403 forbidden` naming
`customer_id`, nothing written), which also keeps a quote from being moved
onto another branch's customer. The conversion rechecks the quote's stored
customer under the same wall (`POST /quotes/{id}/convert`: the 403 naming
`customer_id`), so a quote a caller moved onto a foreign customer cannot
become an order at the pin's branch. For every caller, a quote refuses a
`job_id` its customer does not own with the order's words ("is not a job
of this customer").

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12: the list envelope, the cursor, the error envelope, the enum casing, the money and quantity rules, the document number, the idempotency key, the revision precondition, the timestamps.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2: the path segment rule for scope.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5: the table, the ordering guarantee, the writer, the read API.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 5.8: the convert route and the convert route's pair refusal.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 1: the pair arithmetic (R1 to R5). Sections 2, 3, 4 cover the unit set, the inexact refusal (section 3.4) and the tally (section 4.3).

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