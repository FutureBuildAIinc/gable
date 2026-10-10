<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Charge codes

A charge code is the master row for a fee that a sales document can
name on a charge line: freight, fuel surcharge, restocking fee, any
other named fee. A charge code names the revenue general ledger
account its money posts to (a freight fee posts to freight revenue,
a restocking fee to fees and charges revenue, and so on); a charge
line on an order, invoice or credit memo snapshots that account on its own
row at create time, so a later edit of the code never moves posted
revenue ([ADR 0005](../adr/0005-sales-and-money-core.md) section 2.5).

This module is the work of C2-2 (item C2-2a: the charge code master).
The Go code is in `core/internal/chargecode/`. The table is
`charge_codes` (migration 092, `core/migrations/092_orders_wire_contract.sql`,
lines 235 to 268); the order and invoice modules reference it
through a foreign key on their lines (`charge_code_id`). The wire
contract is in `core/api/fragments/chargecode.yaml`.

## What it does in a yard

A yard quotes and bills freight as a line beside the goods. The yard
operator does not want a freight line to behave like a product line
(no stock to draw, no inventory identity to track), and a freight line
later cannot just be told where to post: the money already moved
against the GL account that the line snapshotted at create. The
charge code master is the small lookup that lets every charge line
point at the right revenue account once, and then never move.

The seed of migration 092 places four charge codes
(`092_orders_wire_contract.sql` lines 263 to 266): `FREIGHT` and
`FUEL` (revenue account `4020`), `RESTOCK` (`4030`, fees and
charges revenue), and `ADJUST` (`4010`, used by migrated credit
memos). The seeded rows are inserted only when missing (`ON
CONFLICT (code) DO NOTHING`); a dealer is free to add codes for
in state delivery, will call, environmentally adjusted pricing, or
any other fee, and to set `taxable` per jurisdiction.

The dealer's owner, an admin or the finance team adds a code through
`POST /charge-codes`; the finance team edits the name, the default
price and, when needed, the revenue account (a PUT must send
`revenue_account_code`; only `code` can never change); an in use code is never deleted, it is flipped inactive
through `is_active`. A charge line takes the code's revenue account
into its own column at create; a later edit of the code's account or
name does not change the line.

## Routes

Every route below is in `core/api/fragments/chargecode.yaml`; the
handler is `core/internal/chargecode/handler.go`; the route census
(`core/api/ROUTES.txt`) lists each one under `internal/chargecode`.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/charge-codes` | The whole master as a bare JSON array (no list envelope, no cursor), ordered by code, active rows only unless `include_inactive=true`. |
| POST | `/api/v1/charge-codes` | Create a charge code (`code` and `name` and `revenue_account_code` required); `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/charge-codes/{id}` | One charge code with its revision. |
| PUT | `/api/v1/charge-codes/{id}` | Replace the name, revenue account, taxable flag, default price and `is_active`; `code` is never accepted (a 400); the revision precondition applies (`If-Match` and `revision`); `Idempotency-Key` rides the standard header. |

The rows are dealer wide: `charge_codes` carries no `branch_id` and
no row is filtered by branch. The routes still run behind the branch
middleware, so with branches switched on a non admin caller names a
branch the user holds in `X-Branch-Id`, as on other scoped routes. The
`include_inactive` flag is the only filter; nothing else narrows the
list because the table is small and the dealer typically carries a
handful of charge codes.

## The charge code resource

`ChargeCode` (see `core/api/fragments/chargecode.yaml`
`components.schemas.ChargeCode`, `core/internal/chargecode/model.go`
`Code`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The charge code id. |
| `code` | text | What documents name; one to sixteen capital letters, digits or underscores (`^[A-Z0-9_]{1,16}$`), immutable once written. |
| `name` | text | The display name. |
| `revenue_account_code` | text | The general ledger revenue account the fee posts to; a charge line snapshots this on its row at create, so a later edit never moves posted revenue. |
| `taxable` | boolean | The default; a charge line may override it per line. |
| `default_unit_price_ten_thousandths` | integer, nullable | The default price per unit at scale 4, fills a charge line that sends no price. `httpx.Price` on the Go side. |
| `is_active` | boolean | False means the code cannot appear on a new charge line; existing lines keep the code. A code in use is never deleted. |
| `revision` | integer | Starts at 1, returned as ETag. |
| `created_at` | timestamp | RFC 3339 UTC. |
| `updated_at` | timestamp | RFC 3339 UTC. |

`ChargeCodeRequest` (`components.schemas.ChargeCodeRequest`): the
body of POST and PUT. On create `code`, `name`, and
`revenue_account_code` are required; `taxable` defaults to false,
`is_active` defaults to true; `default_unit_price_ten_thousandths`
is optional; `code` is checked once with the same pattern as on the
response. On update `code` is never accepted (a 400 naming `code`);
`revision` is the body form of the `If-Match` precondition; the
header form `If-Match: "<revision>"` rides alongside, and a PUT with
neither is a 428. `revision` on a create is a 400.

A PUT replaces every mutable field: send all of them, or `is_active`
returns to true, `taxable` to false and the default price to null
(`Parse` and `Update` in `core/internal/chargecode/service.go`). A
price is never negative, and `revenue_account_code` is one to six
digits naming an account in the chart (`accountPattern`, and the
foreign key to `gl_accounts(code)`).

The wire carries the price under `default_unit_price_ten_thousandths`
at scale 4, ADR 0001 section 7a; the Go side stores it as the
`httpx.Price` type and the database carries `default_unit_price
NUMERIC(12,4)`. The reference for the price convention is
`core/internal/platform/httpx/price.go`.

## Lifecycle and transitions

A charge code has one row state the wire cares about: `is_active`.
A code is active on create and stays active until a writer (admin,
owner or finance) flips it through `PUT /charge-codes/{id}` with `is_active: false`.
Inactive codes cannot appear on a new charge line; lines written
before the flip keep the code on their row and post to the revenue
account the line snapshotted.

There is no status enum and no state machine. A PUT replaces the
mutable fields at the revision; a code in use is never deleted
(`core/internal/chargecode/handler.go`, `HandleUpdate`, and
`chargecode.yaml` `components.schemas.ChargeCodeRequest`).

## Events the module writes

The charge code module is read and write only, no events. A change
to a charge code is today read by polling `GET /charge-codes/{id}`
and diffing the revision. The events vocabulary table is in
[`docs/modules/events.md`](events.md).

## How a charge code reaches an order, invoice or credit memo line

An order, invoice or credit memo line that names a charge code names it
with the text `code` (the lookup), and the line takes three fields
from the master at create:

1. `charge_code_id` (UUID) and `charge_code` (text, the lookup code)
   on the line, written from the master row
   (`core/internal/order/service.go` `buildLines`, the `LineCharge`
   case of the line type switch, lines 269 to 315).
2. `revenue_account_code` on the line, snapshotted from
   `code.RevenueAccountCode` at create (the snapshot:
   `core/internal/order/service.go:285-286`); a later edit of the
   master never moves the line's account.
3. The `unit_price_ten_thousandths` on the line, taken from the line
   body's own price when present, else from
   `code.DefaultUnitPrice` (`core/internal/order/service.go:298-309`).
   A code without a default price and a line without a price is a
   400 naming `lines[i].unit_price_ten_thousandths` at order time.

The line's `taxable` flag falls through to the code's `taxable` when
the body omits it (`core/internal/order/service.go:311-315`); a line
can override the default.

The same fields land on an invoice line through the order
fulfilment. `order/fulfil.go` builds the invoice lines from the
order lines and copies every charge field by column, including the
`ChargeCodeID` it pulls from the order line and the
`RevenueAccountCode` the line snapshotted (`fulfil.go:552-554`, the `FulfilmentLine` literal); the
invoice repository inserts the line and its foreign key
(`invoice/repository.go:574` the `INSERT INTO invoice_lines`
statement and the column list). Migration 092 wires the foreign key
on the order side (`ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS
charge_code_id UUID REFERENCES charge_codes(id)` at
`092_orders_wire_contract.sql:286`); the invoice side inherits the
line through the fulfilment's snapshot.

A quote has no charge lines. Its header freight becomes one `FREIGHT`
charge line on the order at conversion (`core/internal/order/service.go`
`buildFromQuote`, the `FreightCents` block; ADR 0005 section 5.8),
priced from the freight, so converting a quote that has freight needs
the `FREIGHT` code to exist.

A credit memo writes a charge line the same way: a free line on a
credit memo names its own `charge_code` (`core/internal/invoice/input.go`,
`CreditLineRequest`), and the credit memo lines carry a
`charge_code_id`. The seeded code `ADJUST` (revenue account `4010`) is
the code the migration backfilled onto each migrated credit memo, one
charge line per memo ([ADR 0005](../adr/0005-sales-and-money-core.md)
section 2.5 and the migration's step 5); it is not a default for new
credit memos.

## Scopes, roles and keys

A machine key reaching the charge code routes needs
`charge-codes:read` for `GET` and `charge-codes:write` for every
other method (ADR 0002; the scope segment is the first path segment
under `/api/v1/`; `pkg/middleware/machinekey.go` the scope table at
the `"charge-codes"` entry).

The user guards are two `scoped(...)` calls, a role guard composed
with the branch middleware (`branchWall.chargeCodes` in
`core/internal/app/serve/wire_branch_wall.go`): the reads take
`admin`, `owner`, `sales`, `finance`; the writes (`POST` and `PUT`)
take `admin`, `owner`, `finance`, so a sales user reads the master and
cannot change it (`chargecode.RegisterRoutes` applies the second guard
to the writes; `TestBranchWall_ChargeCodeWritesTakeTheFinanceGuard`
drives serve's wiring). A machine key is not subject to the role
guard; its scope is the gate (ADR 0002 section 4). A key without the
scope is `403 forbidden`; the audit row carries the refused scope
(ADR 0002 section 5).

The order, quote, invoice and credit memo modules that read or
reference a charge code do so through their own module guards; the
charge code permission is the additional segment check.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 5, 7, 7a, 9, 11, 12: section 1 (this list is the one deliberate bare array, no envelope), the strict query parameters, money (integer cents), unit prices (scale 4), idempotency keys, the revision precondition (`If-Match`, the in place rule), and the field names and timestamps.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2: the segment scope rule (`charge-codes:read`, `charge-codes:write`).
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 2.5 (`Charge codes`): the schema, the seed, the snapshot rule, the account `4030 Fees and Charges Revenue`. The line snapshot on an order, invoice or credit memo line is the section 2.1 line type `charge` and section 2.5's `charge_code` reference.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. `make up` builds and starts the
local stack (Postgres, migrate and seed, `core serve`, `core worker`,
the web front door) on http://127.0.0.1:8080 with `AUTH_MODE=dev`;
`make down` removes it. To run the core from source instead:
`make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Migration 092 seeds the four codes (`FREIGHT`, `FUEL`, `RESTOCK`,
`ADJUST`) when the migrate runs. The demo seed adds credit memo lines
on `ADJUST` and no order with a charge line. The seed TRUNCATEs the
orders, invoices, quotes, payments and ledger tables, so run it only
against a throwaway database. Then, with a finance role bearer and the
seeded branch:

```
curl -X GET 'http://localhost:8080/api/v1/charge-codes' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

To add a charge code:

```
curl -X POST http://localhost:8080/api/v1/charge-codes \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @charge-code-create.json
```

with body `{"code": "DELIVERY", "name": "Delivery fee",
"revenue_account_code": "4020", "taxable": false,
"default_unit_price_ten_thousandths": 2500}`. The new code's
`Location: /api/v1/charge-codes/{id}` and the ETag header return
the freshly created row.

The route guards are tested in `core/internal/chargecode/handler_test.go`
and `core/internal/app/serve/wire_branch_wall_test.go`. The charge
line snapshot is tested in `core/internal/order/wire_test.go`
(`TestOrderCreateChargeAndTextLines`) and
`core/internal/order/fulfil_test.go` (`TestEachLineTypeOnAFulfilment`),
and the quote freight conversion in
`core/internal/quote/convert_branch_test.go`
(`TestConvert_FreightBecomesAFreightLine`).
