<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Invoices and credit memos

An invoice is the bill a yard sends to a customer. It is a header (the
customer, the ship-to, the order, the job) and a list of priced lines
(product, kit, component, charge and text). The invoice is the document
that posts the general ledger, moves the AR subledger, and is paid
through a payment. A credit memo credits an invoice in part or in full,
posting the general ledger the same way an invoice does and moving the
AR subledger in the opposite direction.

The Go code for invoices is in `core/internal/invoice/`. The credit
memos are in `core/internal/invoice/service_cm.go` and
`core/internal/invoice/credit_build.go`. The migration that brought
the tables onto the contract is
`core/migrations/097_invoices_wire_contract.sql`.

## What it does in a yard

A yard issues an invoice when the goods leave the yard, whether the
delivery is a truck going to a job site or a customer picking up at the
counter. The invoice carries the cost and the price on each line so
the margin is read by the office. The credit memo is the document the
yard writes when goods are returned, a price is corrected, or an
invoice is wrong: it credits tax at the invoice's rate and never
credits more tax than the invoice charged.

## Routes

Every route is in `core/api/fragments/invoice.yaml` and the registered
handles are in `core/internal/invoice/handler.go`. The route census
(`core/api/ROUTES.txt`) lists each one under the `invoices` and
`credit-memos` module columns.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/invoices` | Cursor list of invoices, newest first; status, customer_id, job_id, ship_to_id, order_id, overdue filter. |
| GET | `/api/v1/invoices/{id}` | One invoice with its lines and revision as ETag. |
| POST | `/api/v1/invoices/{id}/transitions` | Void an invoice. |
| POST | `/api/v1/invoices/{id}/email` | Email invoice to customer (render only; the legacy route kept). |
| GET | `/api/v1/invoices/{id}/payments` | List an invoice's payments (the payment module's read). |
| GET | `/api/v1/credit-memos` | Cursor list of credit memos, newest first; status, customer_id, invoice_id, job_id filter. |
| POST | `/api/v1/credit-memos` | Create a draft credit memo. |
| GET | `/api/v1/credit-memos/{id}` | One credit memo with its lines and revision as ETag. |
| PUT | `/api/v1/credit-memos/{id}` | Replace a draft credit memo's header and lines, on the client's revision. |
| POST | `/api/v1/credit-memos/{id}/transitions` | Post or void a credit memo. |

The print routes at `/api/v1/documents/print/invoice/{id}` and
`/api/v1/documents/print/pickticket/{id}` live in
`core/api/fragments/documents.yaml` and are owned by the documents
module. The email route is the legacy render, kept on its existing
path.

## The main resource

`Invoice` (see `core/api/fragments/invoice.yaml`
`components.schemas.Invoice`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The invoice id. |
| `number` | text | The invoice number, gapless, prefix `IN-`, padded to six. |
| `status` | lowercase enum | `unpaid`, `partial`, `paid`, `void`, `written_off`. The wire name is `status`; the database column is `state`. |
| `is_overdue` | boolean | True when `status` is `unpaid` or `partial` and `due_date` is before today in the branch's time zone (`core/internal/invoice/repository.go` `overdueExpr`). |
| `branch_id` | UUID | The branch the invoice belongs to. |
| `customer_id` | UUID | The customer the invoice is for. |
| `customer_name` | text | Snapshot of the customer name. |
| `order_id` | UUID, nullable | The order this invoice came from. |
| `job_id` | UUID, nullable | The project (job) this invoice is for. |
| `ship_to_id`, `ship_to` | UUID, object, nullable | The delivery address and its snapshot. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `currency` | ISO 4217 | The customer's effective currency. |
| `origin` | text | The invoice origin (e.g. `order`, `pos`). |
| `delivery_type` | lowercase enum | `delivery`, `pickup`. |
| `picked_up_by` | text, nullable | Set on a pickup invoice. |
| `delivery_id` | UUID, nullable | The delivery this invoice is for. |
| `invoice_date` | date | The day the invoice is dated. |
| `due_date` | date, nullable | The day the invoice is due, from the customer's payment terms. |
| `payment_terms_id` | UUID | The payment terms applied. |
| `discount_due_date` | date, nullable | The early payment discount window's end. |
| `discount_percent` | decimal string, nullable | The early payment discount percent. |
| `subtotal_cents` | integer | Sum of `line_total_cents` over non text lines. |
| `tax_cents` | integer | The invoice's tax, computed once at the rate of the day. |
| `tax_rate_percent` | decimal string, nullable | The rate used. |
| `tax_exempt` | boolean | Whether the customer is exempt. |
| `tax_source` | lowercase enum | `exempt`, `provider`, `ship_to_rate`, `branch_rate`, `legacy`. |
| `total_cents` | integer | The customer's total. |
| `open_cents` | integer | The amount still open, read only. |
| `paid_at` | timestamp, nullable | When the invoice was fully paid, nullable. |
| `gl_entry_id` | UUID, nullable | The general ledger entry, set on post. |
| `voided_at` | timestamp, nullable | When the invoice was voided. |
| `voided_by` | text, nullable | The actor that voided it. |
| `void_reason` | text, nullable | The reason the void carried. |
| `lines` | array of `InvoiceLine` | The priced lines, in position order. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

`InvoiceLine` carries the shared sales line shape of ADR 0005 section 2
plus the invoice-only fields:

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The line id. |
| `position` | integer | The line's place on the document; a kit's components follow it. |
| `line_type` | lowercase enum | `product`, `kit`, `component`, `charge`, `text`. |
| `parent_line_id` | UUID, nullable | Set on `component` only. |
| `product_id`, `charge_code_id` | UUID, nullable | The product or charge code. |
| `charge_code` | text, nullable | Snapshotted at create. |
| `sku`, `description` | text | Snapshot of the product. |
| `quantity`, `uom` | decimal string, text | The sale unit, scale 4. |
| `price_uom`, `uom_qty`, `price_uom_qty` | text, decimal string, decimal string | The price unit and the conversion pair. |
| `unit_price_ten_thousandths` | integer, nullable | The price per `price_uom`. |
| `priced_unit_price_ten_thousandths` | integer, nullable | What the pricing engine resolved, read only. |
| `price_source` | lowercase enum | `price_list`, `quote`, `override`, `manual`, `none`. |
| `override_reason` | text, nullable | Required when `price_source` is `override`. |
| `discount_percent` | decimal string, nullable | 0 to 100. |
| `discount_cents` | integer, nullable | Positive. |
| `discount_reason` | text, nullable | Required with either discount. |
| `price_adjusted_by` | text, nullable | The actor id of the last override or discount. |
| `line_total_cents` | integer, nullable | The extension, rounded once. |
| `taxable` | boolean | From the product (or the request for a non stock line). |
| `revenue_account_code` | text, nullable | The charge code's account, snapshotted at create. |
| `is_special_order`, `vendor_id`, `special_order_unit_cost_ten_thousandths` | boolean, UUID, integer | A stocked special order line. |
| `order_line_id` | UUID, nullable | The order line this came from. |
| `unit_cost_ten_thousandths` | integer, nullable | The cost per stocking unit, read by margin-aware roles. |
| `cost_cents` | integer | The cost of the billed quantity, read by margin-aware roles. |
| `created_at` | timestamp | RFC 3339 UTC. |

`CreditMemo` has its own field set (see
`core/api/fragments/invoice.yaml` `components.schemas.CreditMemo`):
the sales header fields without the invoice's order, date, terms,
delivery and payment fields, plus `invoice_id`, `pos_return_id`,
`open_cents`, `memo_date`, `reason_code` (`return`,
`price_adjustment`, `damage`, `other`) and `reason`; each line adds
`invoice_line_id` and `restock`. The `number` is gapless, prefix
`CM-`, padded to six, and is null while the memo is a draft (a voided
draft never had one).

### Money and quantity conventions

Money is integer cents under `_cents`; unit prices are integers at
scale 4 under `_ten_thousandths`; quantities are decimal strings with
at most four fraction digits. The extension of a line is the quantity
multiplied by the unit price and by `price_uom_qty`, divided by
`uom_qty`, rounded once to cents half away from zero. A percent
discount is multiplied into the exact product before the one rounding.
An amount discount subtracts from the rounded extension, with no second
rounding; a discount larger than the extension is a 400.

## Lifecycle and transitions

The wire vocabulary is lowercase. The transitions are:

| Entity | From | To | Event |
|---|---|---|---|
| Invoice | `unpaid` with no payments and no applied memos and no live memos and not a counter sale | `void` | `invoice.voided` |
| Credit memo | `draft` | `open` | `credit_memo.posted` (mints the number) |
| Credit memo | `draft` | `void` | `credit_memo.voided` |
| Credit memo | `open` | `void` | `credit_memo.voided` (reason required) |

`invoice.voided` is refused with `409 has_applications` when the
invoice has any payment or any applied credit memo, with
`409 has_credit_memos` when any live (not voided) credit memo names
it, and with `409 counter_sale` when the invoice belongs to a counter
sale (`core/internal/invoice/service.go` `voidInvoice` block). The
statuses past `open` on a credit memo (`partial`, `applied`) are not
reachable from a client; they arrive with applications and refunds.
A void keeps its number; nothing deletes an invoice or credit memo.
The body of a transition is `{"to": "open", "revision": n}` with the
`If-Match` header carrying the same number, and the idempotency key
on every mutating request.

A credit memo names an invoice, and the credit memo credits tax at
that invoice's rate, `round_half_away(taxable_cents x rate)`, but never
more than the invoice's `tax_cents` less the tax earlier credit memos
(not void) already credited against it. The credit memo that returns
the last of the invoice's taxable amount takes exactly that remainder,
as the discount proration does. So partial credit memos never credit
more tax than the invoice charged. With a provider, the same cap
applies to the provider's answer.

The events are the constants in `core/internal/invoice/model.go`
(`EventInvoiceCreated`, `EventInvoiceVoided`, `EventCreditCreated`,
`EventCreditUpdated`, `EventCreditPosted`, `EventCreditVoided`).

## Posting and the subledger

When an invoice is posted, the general ledger receives one journal
entry. On the invoice (`core/internal/gl/postentry.go`,
`fulfilments` description in `core/api/fragments/order.yaml`), the
entry debits the customer's AR (`AccountCodeAR = "1020"`), credits
each line's revenue account (the product's `4010` for products, the
charge code's account for charges), credits sales tax payable
(`AccountCodeSalesTax = "2020"`), debits cost of goods sold
(`AccountCodeCOGS = "5010"`) and credits inventory
(`AccountCodeInventory = "1030"`) on the lines that move stock. Account
`2200` is Customer Deposits (`AccountCodeCustomerDeposit`), not sales
tax payable. Delivery revenue reaches `AccountCodeDeliveryRev = "4020"`
through the seeded FREIGHT and FUEL charge codes, not through the
invoice posting itself. The AR subledger (`account.PostTransaction`
with `TransactionTypeInvoice`, see
`core/internal/invoice/fulfilment.go`) is updated in the same
transaction. A credit memo posts the same way in the opposite
direction. A void reverses the journal entry with the void date
(`PostReversal`'s `EntryDate`, the branch's local date at the void),
keeping the row for the audit trail.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)). The
fulfilment call from the order module writes `invoice.created` first,
then the order's `order.fulfilled` or `order.partially_fulfilled`
(`core/internal/order/fulfil.go`).

## Scopes, roles and keys

A machine key reaching the invoice routes needs `invoices:read` for
`GET` and `HEAD`, and `invoices:write` for every other method (ADR
0002; the segment is the first path segment under `/api/v1/`). The
credit memo routes need `credit-memos:read` and `credit-memos:write`.
`/api/v1/invoices/{id}/payments` is an `invoices:read` call on the
invoices segment. The user guard at the serve layer is
`admin`, `owner`, `sales`, `finance`; the exact guard is composed in
`core/internal/app/serve/wire_branch_wall.go` at `wall.invoices`. A
key without the scope is `403 forbidden`; the audit row carries the
refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) sections 2, 3, 4.1, 4.2, 6, 8: the shared line shape, totals and tax, gapless numbers, currency, the invoice and credit memo lifecycles, the cost basis.

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

Then, with a finance role bearer and the seeded branch:

```
curl -X GET 'http://localhost:8080/api/v1/invoices?status=unpaid&include=total' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

The wire tests in `core/internal/invoice/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for the
invoice's scenarios; the transaction proofs in `tx_test.go` pin the
revision, the credit memo cap, and the void race.