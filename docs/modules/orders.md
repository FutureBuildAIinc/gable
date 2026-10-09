<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Orders

An order is the sales document a customer commits to before the goods
are pulled and delivered. It is a header (the customer, the ship-to,
the job, the salesperson, the delivery type) and a list of priced lines
(product lines, kit lines, component lines, charge lines and text
lines). The order is the first place inventory is allocated, the first
place a credit hold lands, the first place the back order state is
recorded, and the moment a quote becomes a sale. The fulfilment call
against an order is the money moment: it writes the invoice, posts the
general ledger, and moves the AR subledger.

The Go code is in `core/internal/order/`. The fulfilment code is in
`core/internal/order/fulfil.go`, the allocation code in
`core/internal/order/alloc.go`, the will-call queue in
`core/internal/order/fulfil_queue.go`. The migration that brought the
table onto the contract is
`core/migrations/092_orders_wire_contract.sql`; the migration that
wired fulfilment and allocation is
`core/migrations/094_fulfilment_and_allocation.sql`.

## What it does in a yard

A builder or contractor calls or walks in with a list of materials. The
sales rep opens an order, picks the customer, picks the ship-to, and
lines up the products. The order goes through draft, on hold (if
credit is over the limit), confirmed, back ordered (if stock is short),
fulfilled and closed. The fulfilment is the act that issues the
invoice, the picking ticket, and the delivery or the will-call pickup.
For will-call the order sits at the yard until the customer picks up,
and the queue holds the lines until they are pulled.

## Routes

Every route is in `core/api/fragments/order.yaml` and the registered
handles are in `core/internal/order/handler.go`. The route census
(`core/api/ROUTES.txt`) lists each one under the `order` module
column.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/orders` | Cursor list of orders, newest first; status, customer_id, job_id, ship_to_id, quote_id, delivery_type filter. |
| POST | `/api/v1/orders` | Create a draft order, priced through the platform rule, writes `order.created`. |
| GET | `/api/v1/orders/{id}` | One order with its lines and revision as ETag. |
| PUT | `/api/v1/orders/{id}` | Replace a draft order's header and lines, on the client's revision. |
| POST | `/api/v1/orders/{id}/transitions` | Move an order along its lifecycle, on the client's revision. |
| POST | `/api/v1/orders/{id}/allocate` | Allocate a back ordered order on demand. |
| POST | `/api/v1/orders/{id}/fulfillments` | Fulfil an order: invoice, picking ticket, delivery or will-call. |
| GET | `/api/v1/orders/fulfillment-requests` | List the fulfilment requests of completed deliveries. |
| POST | `/api/v1/orders/fulfillment-requests/{delivery_id}/retry` | Retry a parked fulfilment request. |
| GET | `/api/v1/orders/{id}/exposure-gate` | Check the pre ship exposure gate. |
| POST | `/api/v1/orders/{id}/exposure-override` | Override the exposure gate. |

The exposure routes are the pricing module's; the rest are the order
module's. The exposure-gate read is what the order's fulfilment
consults before it bills.

## The main resource

`Order` (see `core/api/fragments/order.yaml` `components.schemas.Order`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The order id. |
| `number` | text | The order number, prefix `SO-`, padded to six. |
| `status` | lowercase enum | `draft`, `on_hold`, `confirmed`, `backordered`, `fulfilled`, `cancelled`. |
| `branch_id` | UUID | The branch the order belongs to. |
| `customer_id` | UUID | The customer the order is for. |
| `customer_name` | text | Snapshot of the customer name. |
| `quote_id` | UUID, nullable | The quote this order came from, when convert is used. |
| `ship_to_id` | UUID, nullable | The delivery address. |
| `ship_to` | object, nullable | Snapshot of the ship-to at confirm. |
| `job_id` | UUID, nullable | The project (job) this order is for. |
| `customer_po` | text, nullable | The customer's purchase order number. |
| `ordered_by_contact_id` | UUID, nullable | The contact who placed the order. |
| `salesperson_id` | UUID, nullable | The book's owner. |
| `scheduled_delivery_date` | date, nullable | The planned delivery day. |
| `delivery_type` | lowercase enum | `delivery`, `pickup`. |
| `hold_reason` | lowercase enum, nullable | `credit_limit`, `manual`. Set only in `on_hold`. |
| `hold_note` | text, nullable | A free text note for a manual hold. |
| `subtotal_cents` | int64 | Sum of `line_total_cents` over non text lines. |
| `tax_cents` | int64 | The order's tax estimate. |
| `tax_rate_percent` | decimal string, nullable | The rate used for the estimate. |
| `tax_exempt` | boolean | Whether the customer is exempt. |
| `tax_source` | lowercase enum | `exempt`, `provider`, `ship_to_rate`, `branch_rate`, `legacy`. |
| `total_cents` | int64 | The customer's total, in minor units. |
| `total_cost_cents`, `total_margin_cents`, `margin_percent` | int64, int64, decimal string | Read by margin-aware roles. |
| `total_commission_cents` | int64 | Read by sales roles. |
| `confirmed_at` | timestamp, nullable | When the order was first confirmed. |
| `currency` | ISO 4217 | The customer's effective currency. |
| `lines` | array of `OrderLine` | The priced lines, in position order. |
| `invoice_ids` | array of UUID | The invoices that have been issued from this order. |
| `deposit_unapplied_cents` | int64 | The unapplied payments (added by C2-4). |
| `revision` | int64 | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

`OrderLine` carries the shared sales line shape of ADR 0005 section 2
and adds the order-only allocation and back order fields:

| Field | Wire form | Note |
|---|---|---|
| `line_type` | lowercase enum | `product`, `kit`, `component`, `charge`, `text`. |
| `parent_line_id` | UUID, nullable | Set on `component` only. |
| `product_id` | UUID, nullable | Required on `kit` and `component`; optional on `product`; null otherwise. |
| `charge_code_id` | UUID, nullable | Required on `charge` only. |
| `sku`, `description` | text | Snapshot of the product. |
| `quantity`, `uom` | decimal string, text | The sale unit, scale 4. |
| `price_uom`, `uom_qty`, `price_uom_qty` | text, decimal string, decimal string | The price unit and the conversion pair. |
| `unit_price_ten_thousandths` | int64 | The price per `price_uom`, scale 4. |
| `priced_unit_price_ten_thousandths` | int64 | What the pricing engine resolved, read only. |
| `price_source` | lowercase enum | `price_list`, `quote`, `override`, `manual`, `none`. |
| `override_reason` | text, nullable | Required when `price_source` is `override`. |
| `discount_percent` | decimal string, nullable | 0 to 100. |
| `discount_cents` | int64, nullable | Positive. |
| `discount_reason` | text, nullable | Required with either discount. |
| `price_adjusted_by` | text, nullable | The actor id of the last override or discount. |
| `line_total_cents` | int64 | The extension, rounded once. |
| `taxable` | boolean | From the product (or the request for a non stock line). |
| `revenue_account_code` | text, nullable | The charge code's account, snapshotted at create. |
| `is_special_order`, `vendor_id`, `special_order_unit_cost_ten_thousandths` | boolean, UUID, int64 | A stocked special order line. |
| `quote_line_id` | UUID, nullable | The quote line this came from. |
| `quantity_allocated`, `quantity_backordered`, `quantity_fulfilled` | decimal string | Order lines only; section 5.4 invariant. |
| `created_at` | timestamp | RFC 3339 UTC. |

### Money and quantity conventions

Money is integer cents under `_cents`; unit prices are integers at scale
4 under `_ten_thousandths`; quantities are decimal strings with at most
four fraction digits. The extension is the quantity multiplied by the
unit price and by `price_uom_qty`, divided by `uom_qty`, rounded once to
cents half away from zero, through `httpx.Extend`. The same function is
used for kit components and the discount, with the percent multiplied
into the exact product before the one rounding.

## Lifecycle and transitions

The wire vocabulary is lowercase. The transitions are below; the
guard codes in parentheses are the `409` blockers when the transition
is refused (ADR 0005 section 5.2):

| From | To | Effect | Events, in order |
|---|---|---|---|
| `draft` | `confirmed` | Credit check; over the limit lands `on_hold` with `hold_reason` `credit_limit`; otherwise allocate and derive status; snapshot the ship-to; set `confirmed_at`. | `order.hold` then `order.confirmed`, or `order.confirmed` then `order.backordered` when it lands `backordered`. |
| `on_hold` | `confirmed` | Release the hold; skip the credit check; allocate if never allocated. | `order.hold_released`, then `order.confirmed` if never confirmed before, then `order.backordered` when it lands `backordered`. |
| `confirmed`, `backordered` | `on_hold` | Manual hold; allocations kept; `hold_reason` `manual`; `hold_note` required. | `order.hold`. |
| `draft`, `on_hold`, `confirmed`, `backordered` | `draft` | Reopen: release every allocation, zero back orders. | `order.reopened`. |
| `draft`, `on_hold`, `confirmed`, `backordered` | `cancelled` | Reason required; release allocations, zero back orders. | `order.cancelled`. |
| `confirmed`, `backordered` | `fulfilled` | Close short: release allocations and back orders of the unfulfilled remainder. | `order.closed_short`. |

Derived moves raise their own events: `backordered` to `confirmed` after
a release on receipt (`order.backorder_released`), `confirmed` to
`backordered` after an invoice void cannot re-allocate
(`order.backordered`), a fulfilment that leaves quantity open
(`order.partially_fulfilled`), a fulfilment that completes the order
(`order.fulfilled`).

Edits: `PUT /api/v1/orders/{id}` replaces header fields and lines in
`draft` only; any other status is `409 conflict` with blocker
`order_not_draft`. Event `order.updated`.

The events are the constants in `core/internal/order/service.go` and
`core/internal/order/fulfil.go` and `core/internal/order/alloc.go`
(`EventCreated`, `EventUpdated`, `EventConfirmed`, `EventHold`,
`EventHoldReleased`, `EventReopened`, `EventCancelled`,
`EventClosedShort`, `EventPartiallyFulfilled`, `EventFulfilled`,
`EventBackordered`, `EventBackorderReleased`, `EventInvoiceCreated`).

## Allocation, back orders and will-call

When an order is confirmed, the service runs `allocateLines` (see
`core/internal/order/alloc.go`). Each stocked line gets the lesser of
the available stock and the still-needed quantity, in `(product id,
line id)` order. A kit's components are planned together in whole kits
from the available stock read under the inventory row locks. The
invariants on every stocked line are:

```
quantity = quantity_allocated + quantity_backordered + quantity_fulfilled + closed_remainder
```

Non stock product lines, kit lines and charge lines carry
`quantity_fulfilled` only. A cancel, a reopen, and a close short release
what is allocated and zero the back orders. The receipt of a purchase
order line releases the back ordered quantity through the same path
(`order.backorder_released`, then `order.fulfilled` or
`order.partially_fulfilled`).

For will-call (`delivery_type` `pickup`), the fulfilment is the
`fulfilments` call with a `pickup` delivery type. The order sits in the
queue (`core/internal/order/fulfil_queue.go`) until the customer shows
up and the lines are pulled. A delivery uses the same call with
`delivery`; the picking ticket and the delivery route are issued in the
same transaction as the invoice.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)). A fulfilment
that issues an invoice writes `order.fulfilled` and `invoice.created`
in order. A retry of a parked fulfilment request
(`POST /api/v1/orders/fulfillment-requests/{delivery_id}/retry`)
re-runs the same path with the same event ids and the same idempotency.

## Scopes, roles and keys

A machine key reaching the order routes needs `order:read` for `GET`
and `HEAD`, and `order:write` for every other method (ADR 0002). The
user guard at the serve layer is the standard sales and finance wall;
the exact guard is composed in `core/internal/app/serve/serve.go` at
`wall.orders(mux, orderSvc)`. A key without the scope is `403
forbidden`; the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) sections 2, 3, 4.1, 4.2, 5, 6, 7, 8.4: the shared line shape, totals and tax, document numbers, currency, the order state machine, the credit and contact checks, allocation and back orders, fulfilment, the cost basis, the kit definition.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 4.4: the `lines[].tally` field on fulfilment.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. From the repository root:

```
make stack-up
make migrate
make seed
make serve
```

Then, with a sales role bearer and the seeded branch:

```
curl -X POST http://localhost:8080/api/v1/orders \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @order-create.json
```

The wire tests in `core/internal/order/wire_test.go` pin the platform
and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for the
order's scenarios; the transaction proofs in `tx_test.go` pin the
revision and the back order invariants.
