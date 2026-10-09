<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Invoices and credit memos

An invoice is the bill a yard sends to a customer. It is a header (the
customer, the ship-to, the order, the job, the salesperson) and a list
of priced lines (product, kit, component, charge and text). The invoice
is the document that posts the general ledger, moves the AR subledger,
and is paid through a payment. A credit memo credits an invoice in
part or in full, posting the general ledger the same way an invoice
does, and moving the AR subledger in the opposite direction.

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
(`core/api/ROUTES.txt`) lists each one under the `invoice` module
column.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/invoices` | Cursor list of invoices, newest first; status, customer_id, job_id, ship_to_id, order_id, overdue filter. |
| GET | `/api/v1/invoices/{id}` | One invoice with its lines and revision as ETag. |
| POST | `/api/v1/invoices/{id}/transitions` | Void an invoice. |
| POST | `/api/v1/invoices/{id}/email` | Email invoice to customer (render only; the legacy route kept). |
| GET | `/api/v1/invoices/{id}/payments` | List an invoice's payments (the payment module's). |
| GET | `/api/v1/credit-memos` | Cursor list of credit memos, newest first; status, customer_id filter. |
| POST | `/api/v1/credit-memos` | Create a draft credit memo. |
| GET | `/api/v1/credit-memos/{id}` | One credit memo with its lines and revision as ETag. |
| PUT | `/api/v1/credit-memos/{id}` | Replace a draft credit memo's header and lines, on the client's revision. |
| POST | `/api/v1/credit-memos/{id}/transitions` | Post or void a credit memo. |

The print routes at `/api/v1/documents/print/invoice/{id}` and
`/api/v1/documents/print/pickticket/{id}` are the documents module's
and live in the same fragment for the time being. The email route is
the legacy render, kept on its existing path.

## The main resource

`Invoice` (see `core/api/fragments/invoice.yaml`
`components.schemas.Invoice`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The invoice id. |
| `number` | text | The invoice number, gapless, prefix `IN-`, padded to six. |
| `status` | lowercase enum | `unpaid`, `partial`, `paid`, `void`, `written_off`. The wire name is `status`; the database column is `state`. `overdue` is a derived flag, not a status. |
| `is_overdue` | boolean | Computed: open amount greater than zero and due date past. |
| `branch_id` | UUID | The branch the invoice belongs to. |
| `customer_id` | UUID | The customer the invoice is for. |
| `customer_name` | text | Snapshot of the customer name. |
| `order_id` | UUID, nullable | The order this invoice came from. |
| `job_id` | UUID, nullable | The project (job) this invoice is for. |
| `ship_to_id`, `ship_to` | UUID, object, nullable | The delivery address and its snapshot. |
| `salesperson_id` | UUID, nullable | The book owner. |
| `invoice_date` | date | The day the invoice is dated. |
| `due_date` | date | The day the invoice is due, from the customer's payment terms. |
| `subtotal_cents` | int64 | Sum of `line_total_cents` over non text lines. |
| `taxable_cents` | int64 | Sum of `line_total_cents` over lines with `taxable` true. |
| `tax_cents` | int64 | `round_half_away(taxable_cents x tax_rate)`, once per document. |
| `tax_rate_percent` | decimal string, nullable | The rate used. |
| `tax_exempt` | boolean | Whether the customer is exempt. |
| `tax_source` | lowercase enum | `exempt`, `provider`, `ship_to_rate`, `branch_rate`, `legacy`. |
| `total_cents` | int64 | The customer's total. |
| `amount_paid_cents` | int64 | The sum of payments applied, read only. |
| `amount_open_cents` | int64 | `total_cents - amount_paid_cents - credited_cents`, read only. |
| `credited_cents` | int64 | The sum of credit memos not void, read only. |
| `currency` | ISO 4217 | The customer's effective currency. |
| `lines` | array of `InvoiceLine` | The priced lines, in position order. |
| `revision` | int64 | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

`InvoiceLine` carries the shared sales line shape of ADR 0005 section 2
plus the invoice-only fields:

| Field | Wire form | Note |
|---|---|---|
| `line_type` | lowercase enum | `product`, `kit`, `component`, `charge`, `text`. |
| `parent_line_id` | UUID, nullable | Set on `component` only. |
| `product_id`, `charge_code_id` | UUID, nullable | The product or charge code. |
| `sku`, `description` | text | Snapshot of the product. |
| `quantity`, `uom` | decimal string, text | The sale unit, scale 4. |
| `price_uom`, `uom_qty`, `price_uom_qty` | text, decimal string, decimal string | The price unit and the conversion pair. |
| `unit_price_ten_thousandths` | int64 | The price per `price_uom`. |
| `unit_cost_ten_thousandths` | int64 | The cost per stocking unit, read by margin-aware roles. |
| `cost_cents` | int64 | The cost of the billed quantity, read by margin-aware roles. |
| `line_total_cents` | int64 | The extension, rounded once. |
| `taxable` | boolean | From the product. |
| `revenue_account_code` | text, nullable | Snapshotted at create. |
| `is_special_order`, `vendor_id`, `special_order_unit_cost_ten_thousandths` | boolean, UUID, int64 | A stocked special order line. |
| `order_line_id` | UUID, nullable | The order line this came from. |
| `created_at` | timestamp | RFC 3339 UTC. |

`CreditMemo` carries the same fields and adds `restock` on each line
(boolean, whether the return restocks). The `number` is gapless, prefix
`CM-`, padded to six. The wire shape is `CreditMemo` in the same
fragment.

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
| Invoice | any open status | `void` | `invoice.voided` |
| Credit memo | `draft` | `posted` | `credit_memo.posted` (mints the number) |
| Credit memo | `draft`, `posted` | `void` | `credit_memo.voided` |

A void keeps its number; nothing deletes an invoice or credit memo.
Other invoice statuses (`unpaid`, `partial`, `paid`, `written_off`) are
derived from payments and credit memos, not transitioned through the
wire. The body of a transition is `{"to": "posted", "revision": n}`
with the `If-Match` header carrying the same number, and the
idempotency key on every mutating request.

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
entry: a debit to the customer's AR (account `1200`), a credit to
revenue (`4010` for products, the charge code's account for charges),
and a credit to sales tax payable (`2200`). The cost of the billed
quantity is debited to cost of goods sold (`5010`) and credited to
inventory (`1300`) on the lines that move stock. The AR subledger
(`account.PostTransaction` with `TransactionTypeInvoice`, see
`core/internal/invoice/fulfilment.go`) is updated in the same
transaction. A credit memo posts the same way in the opposite
direction. A void reverses the journal entry with its own
`posted_at`, keeping the row for the audit trail.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)). The
fulfilment call from the order module also writes `invoice.created` as
its second event after `order.fulfilled` (see `core/internal/order/fulfil.go`
`EventInvoiceCreated`).

## Scopes, roles and keys

A machine key reaching the invoice routes needs `invoice:read` for
`GET` and `HEAD`, and `invoice:write` for every other method (ADR
0002). The user guard at the serve layer is the standard sales and
finance wall; the exact guard is composed in
`core/internal/app/serve/serve.go` at
`wall.invoices(mux, invoiceSvc)`. A key without the scope is `403
forbidden`; the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) sections 2, 3, 4.1, 4.2, 6, 8: the shared line shape, totals and tax, gapless numbers, currency, the invoice and credit memo lifecycles, the cost basis.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. From the repository root:

```
make stack-up
make migrate
make seed
make serve
```

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
