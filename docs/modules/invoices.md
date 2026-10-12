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
(`core/api/ROUTES.txt`) lists each one under the `internal/invoice`
module (the route census has a `module` column whose value is the
registering package's directory relative to the Go module root).
Two of the routes below are registered under other modules: the
email route is `internal/document` and the credit memo refund route
is `internal/payment`. The print routes (below) are `internal/document`.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/invoices` | Cursor list of invoices, newest first; status, customer_id, job_id, ship_to_id, order_id, overdue filter. |
| GET | `/api/v1/invoices/{id}` | One invoice with its lines and revision as ETag. |
| POST | `/api/v1/invoices/{id}/transitions` | Void an invoice. |
| POST | `/api/v1/invoices/{id}/write-offs` | Write off part or all of an open invoice as bad debt (`internal/invoice`); roles `admin`, `owner`, `finance`; body `{amount_cents, reason}` (1 to 500 chars) and revision; posts DR `5040` / CR `1020` and a `WRITE_OFF` subledger row; a write off that takes the open amount to zero closes the invoice as `written_off`, else it stays `partial`. |
| POST | `/api/v1/invoices/{id}/email` | Email invoice to customer (queued, answers 202, notification is log only; owned by `internal/document`). |
| GET | `/api/v1/invoices/{id}/payments` | List an invoice's applications (payments, credit memos, early pay discounts and write offs, oldest first, reversals included). |
| GET | `/api/v1/credit-memos` | Cursor list of credit memos, newest first; status, customer_id, invoice_id, job_id filter. |
| POST | `/api/v1/credit-memos` | Create a draft credit memo. |
| GET | `/api/v1/credit-memos/{id}` | One credit memo with its lines and revision as ETag. |
| PUT | `/api/v1/credit-memos/{id}` | Replace a draft credit memo's header and lines, on the client's revision. |
| POST | `/api/v1/credit-memos/{id}/applications` | Apply a credit memo's open credit to invoices of the same customer and currency (`internal/invoice`); body `{applications: [{invoice_id, amount_cents}], revision}`; no ledger entry (both sides are in `1020`) and no subledger row. |
| POST | `/api/v1/credit-memos/{id}/refunds` | Pay a credit memo's open credit out (`internal/payment`); roles `admin`, `owner`, `finance`; body `{amount_cents, reason, method: card\|check\|ach\|other, payment_id?}`; a card refund names the card payment it goes back to and goes through the gateway before the transaction; posts DR `1020` / CR `1010` and a `REFUND` subledger row. |
| POST | `/api/v1/credit-memos/{id}/transitions` | Post or void a credit memo. |

The print routes at `/api/v1/documents/print/invoice/{id}` and
`/api/v1/documents/print/pickticket/{id}` live in
`core/api/fragments/documents.yaml` and are owned by the documents
module. The email route is owned by `internal/document` (queued,
202, log only).

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
its own `id`, `number`, `branch_id`, `customer_id`, `customer_name`,
`invoice_id`, `pos_return_id`, `job_id`, `ship_to_id`, `status`,
`revision`, `currency`, `subtotal_cents`, `tax_cents`,
`tax_rate_percent`, `total_cents`, `open_cents`, `gl_entry_id`,
`memo_date`, `voided_at`, `voided_by`, `void_reason`, `created_at`,
`updated_at`, plus `reason_code` (`return`, `price_adjustment`,
`damage`, `other`) and `reason`; each line adds `invoice_line_id`
and `restock`. The `number` is gapless, prefix `CM-`, padded to
six, and is null while the memo is a draft (a voided draft never had
one).

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
| Invoice | `unpaid` with no payments and no applied memos and no live memos and not a counter sale | `void` | `invoice.voided` (reason required) |
| Invoice | `unpaid`, `partial` | `written_off` | `invoice.written_off` (reason required) when the write off takes the open amount to zero, or when an application takes the open amount to zero on an invoice with a live write off; `invoice.partial` when it leaves the invoice still open |
| Invoice | `unpaid`, `partial` | `paid` | `invoice.paid` when an application (payment or credit memo) takes the open amount to zero and the invoice has no live write off (with one, it closes as `written_off` and writes `invoice.written_off`); `invoice.partial` when it leaves the invoice still open |
| Invoice | `partial`, `paid`, `written_off` | `unpaid` or `partial` | `invoice.reopened` when an application is reversed |
| Credit memo | `draft` | `open` | `credit_memo.posted` (mints the number) |
| Credit memo | `draft` | `void` | `credit_memo.voided` (reason required) |
| Credit memo | `open` | `void` | `credit_memo.voided` (reason required) |
| Credit memo | `open`, `partial` | `partial` | `credit_memo.partial` when an application or refund leaves credit open; a refund writes `credit_memo.refunded` first, then the status event |
| Credit memo | `open`, `partial` | `applied` | `credit_memo.applied` when an application takes the credit to zero; a refund that closes the memo writes `credit_memo.refunded` first, then the status event |

`invoice.voided` is refused with `409 has_applications` when the
invoice has any payment or any applied credit memo, with
`409 has_credit_memos` when any live (not voided) credit memo names
it, and with `409 counter_sale` when the invoice belongs to a counter
sale (`core/internal/invoice/service.go` `voidInvoice` block). The
statuses past `open` on a credit memo (`partial`, `applied`) are not
reachable from a client; they arrive with applications and refunds.
A void keeps its number; nothing deletes an invoice or credit memo.
The body of a transition is `{"to": "<status>", "revision": n,
"reason": "..."}` for voids (the `reason` is required and is 1 to
500 characters), and `{"to": "<status>", "revision": n}` for the
credit memo's `draft` to `open` post; the `If-Match` header carries
the same number, and the idempotency key is on every mutating
request.

`invoice.written_off` (ADR 0005 section 9.4) is refused with
`409 exceeds_open_amount` when the write off is larger than the
invoice's open amount, with `409 invalid_state_transition` when the
invoice is not `unpaid` or `partial`, and with `409 period_closed`
when the branch's accounting period is closed; the service enforces
the finance role (`requireFinance` at
`core/internal/invoice/service_ar.go` line 89).
`credit_memo.applied` and `credit_memo.partial` (the credit memo's
application call) are refused with `409 exceeds_open_credit` when the
lines together ask for more than the memo's open credit, and with
`409 invalid_state_transition` when the memo is not `open` or `partial`,
`409 exceeds_open_amount` when an invoice is asked for more than its
open amount, `409 invoice_void` when an invoice is void,
`409 customer_mismatch` when an invoice belongs to another customer,
and `409 currency_mismatch` when the invoice's currency differs.
`credit_memo.refunded` (the credit memo's refund call,
`core/internal/payment/service_card.go` `RefundCredit`) is refused with
`409 invalid_state_transition` when the memo is not `open` or `partial`,
with `409 exceeds_open_credit` when the refund is larger than the open
credit, and with `409 customer_mismatch` or `409 currency_mismatch` on a
card refund when the card payment and the credit memo disagree; the
service enforces the finance role (`who.finance` at
`core/internal/payment/service_card.go` line 192).

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
`EventCreditUpdated`, `EventCreditPosted`, `EventCreditVoided`) and
in `core/internal/account/model.go` for the writes the AR core
performs (`EventInvoicePartial`, `EventInvoicePaid`,
`EventInvoiceWrittenOff`, `EventInvoiceReopened`,
`EventCreditPartial`, `EventCreditApplied`, `EventCreditReopened`;
`credit_memo.refunded` is the one the payment module writes itself
at `core/internal/payment/service_card.go` line 263). The events
arrive last in the transaction, after the AR core's effects
(`recordEffects` at `core/internal/invoice/service_cm.go` line 136).

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

A key bound to one branch (ADR 0007 section 5.5) may create or update a
credit memo (`POST /credit-memos`, `PUT /credit-memos/{id}`) only for a
customer its branch holds: `403 forbidden` naming `customer_id`, nothing
written.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) sections 2, 3, 4.1, 4.2, 6, 8: the shared line shape, totals and tax, gapless numbers, currency, the invoice and credit memo lifecycles, the cost basis.
- The AR subledger, the aging, the statement and the reconciliation, the deposit application and the credit memo refund live in [payments.md](payments.md) (C2-4).

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