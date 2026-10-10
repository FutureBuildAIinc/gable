<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Payments, deposits and AR

A payment is a record of money received from a customer. The payment
does not name an invoice: applications (the AR side, `ArApplication`)
settle the payment's amount against one or more invoices. The same
`ArApplication` row is also the unit a credit memo writes when it
credits an invoice. Unapplied cash, deposits and refunds all ride on
the same document.

This module is the work of C2-4 (the sales and money core's payment
conversion). The Go code is in `core/internal/payment/`. The AR
subledger, the aging, the reconciliation, the statements and the
write-offs live in `core/internal/account/` and are reached through
the payment routes that namespace as `/api/v1/payments` and
`/api/v1/ar/*`. The deposit flow uses no separate deposit routes; a
deposit is a payment with `order_id` set, applied by the order's
fulfillment when it bills.

## What it does in a yard

A customer pays cash, check, ACH or card against an open invoice; the
cashier or the portal records a payment against the customer. The
whole amount posts to the deposit liability `2200` and lands on the
customer as unapplied cash. If the body also names `applications`,
the same transaction applies each line: each application takes its
cash against an invoice (or takes its early-pay discount on or before
the invoice's discount date), the subledger row is written
(`payment.applied`, `invoice.partial` or `invoice.paid`,
`customer.updated` for every customer whose balance moved) and the
unapplied amount drops. A card payment runs through Run Payments: the
frontend tokenises the card, gets the gateway public key with
`POST /payments/intent`, then charges with `POST /payments/card`;
the gateway captures immediately, so the charge is the payment.

A refund of a card payment goes back to the card (the gateway
refund id is recorded, the state machine is the gateway's; we
mirror the result). A refund of cash, check, ACH or other is recorded
as the customer getting the cash back: the request takes the
reason, the GL leg is `DR 2200 / CR 1010` (cash out of the drawer),
and the customer's unapplied cash decreases by the amount. A refund
of a credit memo's open credit is the same idea with the credit memo
as the source (`POST /credit-memos/{id}/refunds`).

A deposit is the same record with `order_id` set. The order holds
the unapplied deposits in `deposit_unapplied_cents`. The fulfilment
that bills the order applies the deposits to the invoice it just
created, oldest first, in the same transaction as the invoice and
the AR subledger (see `core/internal/order/fulfil.go` step 7 and
`account.Service.ApplyDeposits`).

## Routes

Every route below is in `core/api/fragments/payment.yaml`,
`core/api/fragments/accounts.yaml` and
`core/api/fragments/invoice.yaml`, the registered handles are in
`core/internal/payment/handler.go`,
`core/internal/account/handler.go` and
`core/internal/invoice/handler.go`, and the route census
(`core/api/ROUTES.txt`) lists each one under the package named in
the `package` column.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/payments` | Cursor list of payments; customer, status, unapplied, order, job, method filter; `include=total` adds a total. |
| POST | `/api/v1/payments` | Record a posted payment for a customer; the whole amount is unapplied cash; `applications` apply it in the same call. |
| POST | `/api/v1/payments/intent` | Get the gateway public key for tokenization; takes `amount_cents`, creates nothing. |
| POST | `/api/v1/payments/card` | Charge a tokenised card; the gateway captures immediately. |
| GET | `/api/v1/payments/{id}` | One payment with its applications and refunds (the full document). |
| POST | `/api/v1/payments/{id}/applications` | Apply the payment's unapplied cash to one or more invoices; revision precondition. |
| POST | `/api/v1/payments/{id}/transitions` | Void a posted payment; `to` is `voided`; reason required; revision precondition. |
| POST | `/api/v1/payments/{id}/refunds` | Refund a payment's unapplied cash; reason required; revision precondition. |
| POST | `/api/v1/credit-memos/{id}/refunds` | Pay a credit memo's open credit out; reason and method required; the precondition is the credit memo's revision. |
| GET | `/api/v1/invoices/{id}/payments` | List an invoice's applications (the AR side). |
| GET | `/api/v1/accounts/{id}` | One customer's account summary (balance, credit limit, unapplied cash). |
| GET | `/api/v1/accounts/{id}/transactions` | The customer's AR transactions, by date. |
| GET | `/api/v1/ar/customers/{id}/statement` | A customer's statement over a date range; opening, lines, closing, open documents. |
| GET | `/api/v1/ar/aging` | Aging of every open document by customer and job. |
| GET | `/api/v1/ar/aging/summary` | Aging totals by currency. |
| GET | `/api/v1/ar/reconciliation` | The receivable and customer deposit ledgers, drift per customer and per currency. |
| POST | `/api/v1/ar/applications/{id}/reverse` | Reverse one AR application; reason required. |
| GET | `/api/v1/credit-memos` | List credit memos. |
| POST | `/api/v1/credit-memos` | Create a draft credit memo. |
| GET | `/api/v1/credit-memos/{id}` | One credit memo with its lines. |
| PUT | `/api/v1/credit-memos/{id}` | Replace a draft credit memo. |
| POST | `/api/v1/credit-memos/{id}/applications` | Apply a credit memo's open credit to invoices. |
| POST | `/api/v1/credit-memos/{id}/transitions` | Move a credit memo along its lifecycle. |

The `credit-memos` routes are owned by the invoice module
(`core/internal/invoice/handler.go`); the credit memo's refund is
owned by the payment module because the refund touches the gateway
(card) or the drawer (cash).

A refusal on `POST /payments` answers `409 conflict` with one of the
blockers the fragment names in the route description:
`exceeds_unapplied`, `exceeds_open_amount`, `invoice_void`,
`customer_mismatch`, `currency_mismatch`,
`discount_not_available`, `period_closed`.

## The main resource

`Payment` (see `core/api/fragments/payment.yaml`
`components.schemas.Payment`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The payment id. |
| `number` | text | Human readable, prefix `PAY-`, padded to six. |
| `customer_id` | UUID | The customer the payment is for. |
| `customer_name` | text | Snapshot of the customer name. |
| `branch_id` | UUID | The branch the payment was recorded at. |
| `status` | lowercase enum | `posted`, `voided`. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `currency` | ISO 4217 | The customer's currency at record time. |
| `method` | lowercase enum | `cash`, `card`, `check`, `ach`, `other`. `account` is the legacy name and is refused on a new payment. |
| `amount_cents` | integer | The payment in minor units, always positive. |
| `unapplied_cents` | integer | `amount_cents` less live applications and refunds; held in `2200`. |
| `received_on` | date | The business date. |
| `reference` | text, nullable | The check number, the last four of the card, or the on-account reference. |
| `notes` | text, nullable | Free text note (max 2000 chars). |
| `order_id` | UUID, nullable | Set on a deposit against an order. |
| `job_id` | UUID, nullable | The job the payment is tagged to. |
| `gl_entry_id` | UUID, nullable | The entry in the journal for `1010`/`2200`. |
| `card_last4`, `card_brand`, `gateway_tx_id`, `auth_code` | text, nullable | Populated for card payments. |
| `voided_at`, `voided_by`, `void_reason` | timestamp, text, text | Set only when `status` is `voided`. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |
| `applications` | array of `ArApplication` | The applications of this payment (always present, may be empty). |
| `refunds` | array of `Refund` | The refunds of this payment (always present, may be empty). |

`PaymentSummary` is the same head, returned as a list item, without
`applications` or `refunds`.

`PaymentCreateRequest` (see the fragment) carries the customer's
`customer_id`, `amount_cents`, and `method`, plus optional
`branch_id` (defaults to the caller's branch, else the customer's
primary), `reference`, `notes`, `received_on` (defaults to the
branch's business date), `order_id` (a deposit), `job_id`, and an
`applications` array. `Idempotency-Key` rides the standard header
(ADR 0001 section 9).

`PaymentApplicationRequest` carries `invoice_id` and `amount_cents`,
plus optional `discount_cents` (a discount beside this cash; written
as a `DISCOUNT` application, `DR 4050 / CR 1020`). The application
amount cannot exceed the invoice's open amount
(`exceeds_open_amount`) and the payment's unapplied
(`exceeds_unapplied`).

`PaymentTransitionRequest` is `to: voided` with `reason`, on the
revision. `PaymentRefundRequest` is `amount_cents` and `reason`, on
the revision. `CreditMemoRefundRequest` adds `method`
(`card|check|ach|other`) and an optional `payment_id` (the card
payment a card refund goes back to; only for `method: card`); the
precondition is the credit memo's revision.

### Money and quantity conventions

`amount_cents`, `unapplied_cents` and discount cents are integer
minor units (ADR 0001 section 7). The payment body echoes the
amount under `amount_cents`; the gateway intent response echoes
under `amount_cents` too (the legacy shape's `amount` is gone). No
quantity is exposed on a payment: a deposit's order is named under
`order_id`, not by a quantity.

## Lifecycle and transitions

The wire vocabulary is lowercase; the database keeps its UPPERCASE
CHECK.

`Payment.status` has two values:

| Status | Wire | Note |
|---|---|---|
| `POSTED` | `posted` | Recorded. The only status a payment can be in on create. |
| `VOIDED` | `voided` | Terminal. |

The transition is `POST /payments/{id}/transitions` with `to:
voided` and `reason`. A posted payment carries its applications and
refunds; a void unposts the GL and rolls the unapplied cash back
through the same leg pair as the create, then it stays in `voided`.
A card payment can be voided while the gateway still allows a void;
a refund that already reached the gateway carries the gateway
refund id and is recorded as a separate `Refund` row.

`Refund.status` is the gateway's state machine, kept in UPPERCASE
by the database CHECK:

| Status | Note |
|---|---|
| `PENDING` | The refund has been initiated; the gateway has not acknowledged. |
| `COMPLETE` | The gateway has confirmed the money left the merchant account. |
| `FAILED` | The gateway refused the refund. |

`PaymentMethod` is UPPERCASE in storage and lowercase on the wire:

| Storage | Wire | Note |
|---|---|---|
| `CASH` | `cash` | A non-card payment recorded against the customer. |
| `CARD` | `card` | Tokenised card through Run Payments; recorded through `POST /payments/card`. |
| `CHECK` | `check` | A non-card payment. |
| `ACH` | `ach` | A non-card payment. |
| `OTHER` | `other` | A non-card payment. |
| `ACCOUNT` | `account` | Legacy only; refused on a new payment. |

A credit memo's lifecycle is owned by the invoice module
(`CreditStatus`: `DRAFT`, `OPEN`, `PARTIAL`, `APPLIED`, `VOID`); see
`docs/modules/invoices.md`.

## AR applications, the unit of settlement

Every payment, refund and credit memo application is one row of
`ArApplication` (see `core/api/fragments/accounts.yaml`
`components.schemas.ArApplication`). The same row is the AR truth
of "this much of that document settled that much of this invoice".

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The application id. |
| `customer_id` | UUID | The customer that owns both sides. |
| `currency` | ISO 4217 | The currency both sides agree on. |
| `kind` | enum | `payment`, `credit_memo`, `discount`, `write_off`. |
| `payment_id` | UUID, nullable | Set when `kind` is `payment` or `discount`. |
| `credit_memo_id` | UUID, nullable | Set when `kind` is `credit_memo`. |
| `invoice_id` | UUID | The invoice being settled. |
| `amount_cents` | integer | Settled; positive. |
| `reason` | text, nullable | Required for a write off. |
| `applied_on` | date | The business date. |
| `applied_by` | text, nullable | The actor. |
| `act_id` | UUID | The grouping of the applications one request made. |
| `gl_entry_id` | UUID, nullable | The journal leg id. |
| `reversed_at`, `reversed_by`, `reversal_reason`, `reversal_gl_entry_id`, `reversed_on` | timestamp, text, text, UUID, date | Set only on a reversal. |
| `created_at` | timestamp | RFC 3339 UTC. |

A reversed application is never deleted: the row stays with its
reversal fields filled and the next read is the reversal as well.
The reverse route is `POST /ar/applications/{id}/reverse` with a
`reason` body and returns the application envelope.

The AR transaction list (`GET /accounts/{id}/transactions`,
`AccountTransaction`) is the date-ordered view of those settlements.
`AccountTransaction.type` is UPPERCASE in storage and lowercase on
the wire: `INVOICE`, `PAYMENT`, `ADJUSTMENT`, `REFUND`,
`CREDIT_MEMO`, `DISCOUNT`, `WRITE_OFF`, `REVERSAL`.

## Aging, statements and reconciliation

The aging buckets a customer's open documents:
`current_cents`, `days_1_30_cents`, `days_31_60_cents`,
`days_61_90_cents`, `over_90_cents`, `unapplied_cents`,
`total_cents` (see `ArAgingItem`). The bucket is chosen by
`due_date` (the default) or `invoice_date`; the call passes
`basis=invoice_date` to flip the choice. The aging summary rolls
the totals by currency (`ArAgingSummary`, `ArAgingTotal`).

The statement (`ArStatement`) names the customer, the date range
(`from`, `to`), an optional `job_id` filter and the currencies. Each
currency carries the opening balance, the dated lines, the closing
balance, and the open documents as of the range end. The same range
end is what the reconciliation cites when it compares the AR
subledger to the customer deposit ledger. The reconciliation report
is a `ArReconciliation` with a `customers` array (per customer
drift: `balance_cents`, `subledger_cents`, `documents_cents`) and a
`currencies` array (per currency: `receivable_ledger_cents`,
`balance_sum_cents`, `deposits_ledger_cents`, `unapplied_sum_cents`,
the two `*_in_sync` flags). The shape is read only.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)). The full
list:

| Event | Source | Constant |
|---|---|---|
| `payment.recorded` | `core/internal/payment/service.go:273` | `EventRecorded` |
| `payment.applied` | `core/internal/account/model.go:52` | `EventPaymentApplied` |
| `payment.unapplied` | `core/internal/account/model.go:53` | `EventPaymentUnapplied` |
| `payment.refunded` | `core/internal/payment/service.go:534` | `EventRefunded` |
| `payment.voided` | `core/internal/payment/service.go:456` | `EventVoided` |
| `invoice.partial` | `core/internal/account/effects.go` | `EventInvoicePartial` |
| `invoice.paid` | `core/internal/account/effects.go` | `EventInvoicePaid` |
| `invoice.written_off` | `core/internal/account/model.go:56` | `EventInvoiceWrittenOff` |
| `invoice.reopened` | `core/internal/account/model.go:57` | `EventInvoiceReopened` |
| `customer.updated` | `core/internal/account/effects.go` | `EventCustomerUpdated` |
| `credit_memo.applied` | `core/internal/invoice/service_ar.go` | `audit.Action` |
| `credit_memo.posted` | `core/internal/invoice/model.go:256` | `EventCreditPosted` |
| `credit_memo.voided` | `core/internal/invoice/model.go:257` | `EventCreditVoided` |

A `POST /payments` with `applications` writes `payment.recorded`,
then `payment.applied` and `invoice.partial` (or `invoice.paid`),
then `customer.updated` for every customer whose balance moved.
The order is the order of legs, not the order the body named.
A card charge writes `payment.recorded` first; the gateway reverses
into `payment.voided` or into a `Refund` row, never both.

## Scopes, roles and keys

A machine key reaching the payment routes needs `payments:read` for
`GET` and `payments:write` for every other method (ADR 0002; the
segment is the first path segment under `/api/v1/`). The account /
AR keys are `ar:read` and `ar:write`; the AR reverse
(`/ar/applications/{id}/reverse`), the reconciliation
(`/ar/reconciliation`) and the credit memo writes
(`/credit-memos/.../applications`,
`/credit-memos/.../transitions`) are `finance` only at the user
guard.

The user guard at the serve layer is composed of one or two
`scoped(...)` calls per handler (see
`core/internal/app/serve/wire_branch_wall.go`): the payment handler
takes the wider guard `admin`, `owner`, `sales`, `finance`,
`cashier` (`wall.payments`); the account / AR handler takes a read
guard `admin`, `owner`, `sales`, `finance` and a write guard
`admin`, `owner`, `finance` (`wall.accounts`). A key without the
scope is `403 forbidden`; the audit row carries the refused scope.

The branch wall applies: a payment is read and written under the
branch the request carries through `X-Branch-Id` (the header of ADR
0001 section 12). The unit catalogue (ADR 0006 section 2) is dealer
wide; no branch wall applies there.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 7, 7a, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 4.1, 4.2, 9, 10, 11: the payment number, currency, the AR subledger and applications, the customer's unapplied cash, the card gateway.

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
curl -X POST http://localhost:8080/api/v1/payments \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @payment-create.json
```

The transaction proofs in `core/internal/payment/tx_test.go` and
the card reversals in
`core/internal/payment/service_card_reversal_test.go` pin the
current behaviour; the AR proofs in
`core/internal/account/ar_test.go` pin the subledger, the aging and
the reconciliation; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the payment, the application, the AR reads and the credit memo
flows.
