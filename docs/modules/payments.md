<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Payments

A payment is a record of money received against an invoice. The
payment carries the invoice it pays, the amount in cents, the
method, and a reference (check number, last four of the card). A
card payment goes through a gateway; a non-card payment is recorded
directly. A refund is a separate `Refund` record against the
original payment.

This page is the current state of the payment module as it is today.
The full money story is the work of C2-4 (the sales and money core's
payments conversion), which converts the module onto the contract
in place: the events outbox is added; today the module posts a
paid-invoice notice to FB Brain over HTTP and writes no event,
the revision gate is added, the gapless numbering of payments is
settled through the gapless counter, and the unapplied cash term
is added to the customer. Deposits already live in `core/internal/deposit` and
the `customer_deposits` table (created by migration 079); C2-4 is
not the work that brings them. After C2-4, this page is the
migration guide for what moved; today it is the description of what
is here.

The Go code is in `core/internal/payment/`. The payment module has no
contract migration yet.

## What it does in a yard

A customer pays an invoice by cash, check, or card (an on-account
payment against the customer's AR balance is the `ACCOUNT` method).
The payment is the record of the receipt; the payment names the
invoice it pays. A refund is a separate `Refund` row against the
original payment with its own positive amount.

## Routes

Every route is in `core/api/fragments/payment.yaml` and the
registered handles are in `core/internal/payment/handler.go`. The
route census (`core/api/ROUTES.txt`) lists each one under the
`payments` module column. The invoice's payment list is reached
through `/api/v1/invoices/{id}/payments`, owned by this module,
though its path sits under `invoices`.

| Method | Path | One line |
|---|---|---|
| POST | `/api/v1/payments` | Record a non-card payment against an invoice. |
| POST | `/api/v1/payments/intent` | Get the gateway public key for tokenization (creates nothing). |
| POST | `/api/v1/payments/card` | Charge a tokenized card. |
| POST | `/api/v1/payments/refund` | Refund a card payment in part or full; creates a `Refund` row. |
| GET | `/api/v1/invoices/{id}/payments` | List an invoice's payments (the payment module's read). |

The `intent` and `card` routes are the gateway seam and keep their
own shapes until C2-4; the `intent` response echoes the amount
under a cents name while the request carries it under a bare name
(see `core/api/fragments/payment.yaml` `paymentIntent` description,
which records this directly). A card charge whose gateway call
succeeded but persistence failed answers a generic 402 today; C2-4
turns this into the error envelope.

## The main resource

`Payment` (see `core/api/fragments/payment.yaml`
`components.schemas.Payment`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The payment id. |
| `invoice_id` | UUID | The invoice the payment is against. |
| `amount` | integer | The amount in cents, always positive. |
| `method` | uppercase enum | `CASH`, `CHECK`, `CARD`, `ACCOUNT`. |
| `reference` | text | The check number, the last four of the card, or the on-account reference. |
| `notes` | text | A free text note. |
| `created_at` | timestamp | When the payment was recorded, RFC 3339 UTC. |
| `gateway_tx_id` | text | The gateway transaction id, populated for card payments through Run Payments. |
| `gateway_status` | text | The gateway's status text, populated for card payments. |
| `card_last4` | text | The card last four, populated for card payments. |
| `card_brand` | text | The card brand, populated for card payments. |
| `auth_code` | text | The card auth code, populated for card payments. |

The `PaymentCreateRequest` carries `invoice_id`, `amount`, `method`,
`reference`, `notes`. The `idempotency_key` rides in the
`Idempotency-Key` header (ADR 0001 section 9).

`Refund` (see `core/api/fragments/payment.yaml`
`components.schemas.Refund`) carries `id`, `payment_id`, `amount`
(integer cents, positive), `reason`, `gateway_refund_id`,
`status` (`PENDING`, `COMPLETE`, `FAILED`), `created_at`. The
refund is a positive amount against the original payment;
`RefundPayment` refuses an amount not positive
(`core/internal/payment/service.go`) and refuses an amount above the
original.

### Money and quantity conventions

`amount` is integer cents (no `_cents` suffix on the payment
resource, by the fragment; the request's amount rides the same
name). The `PaymentIntentResponse` echoes the amount under
`amount_cents` (see the fragment note). No quantity is exposed.

## Lifecycle and transitions

A payment has no lifecycle of its own. It is created, and a refund
is a separate `Refund` row against the original with its own
positive amount, status and gateway refund id. The AR subledger is
moved in the same transaction
(`account.PostTransaction(ctx, inv.CustomerID,
account.TransactionTypePayment, -amountCents, &p.ID, "Payment
"+ref)`, see `core/internal/payment/service.go`).

## Events the module writes

The payment module today writes no outbox events; the AR subledger
and the gateway are the systems of record. C2-4 writes
`payment.recorded` and `payment.refunded`, among the payment events
of ADR 0005 section 12, through the outbox.

## Scopes, roles and keys

A machine key reaching the payment routes needs `payments:read` for
`GET` and `payments:write` for every other method (ADR 0002; the
segment is the first path segment under `/api/v1/`). The user guard
at the serve layer is `admin`, `owner`, `sales`, `finance`,
`cashier`; the exact guard is composed in
`core/internal/app/serve/serve.go` at the payment handler's
`RegisterRoutes` line. A key without the scope is `403 forbidden`.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 7, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 4.1, 4.2, 9, 11: the payment number, currency, the AR subledger, the card gateway, the customer's unapplied cash.

## What C2-4 will change

- The wire becomes the list envelope of ADR 0001 section 1; the
  fragment's bare array shape is replaced with the `items`,
  `next_cursor`, `limit` envelope, and `total` arrives under
  `include=total`.
- The error envelope is the one envelope of ADR 0001 section 3;
  the legacy 402 on a card persistence failure is rewritten to the
  right code with the handler's message kept.
- The `intent` route's request field is renamed to `amount_cents`
  to match the rest of the surface.
- The payment number is minted from the `PAY` series, gapped; the
  `payment_number_seq` is defined in ADR 0005 section 4.1 and is
  not yet created.
- The payment's unapplied amount (`amount_unapplied`, ADR 0005
  section 9) is added; the AR subledger rule in section 9.3 is the
  gate.
- The Brain notice stays as it is, or moves behind the outbox as
  C2-4 decides.
- A payment's revision is added and the writes that update it
  take the `If-Match` precondition.

## How to try it locally

The repository's own seed and the local make targets are the only
way to exercise the module end to end. `make up` builds and starts
the local stack (Postgres, migrate and seed, `core serve`,
`core worker`, the web front door) on http://127.0.0.1:8080 with
`AUTH_MODE=dev`; `make down` removes it. To run the core from source
instead: `make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Then, with a finance role bearer:

```
curl -X GET 'http://localhost:8080/api/v1/invoices/<id>/payments' \
  -H 'Authorization: Bearer <token>'
```

The tests in `core/internal/payment/` pin the current behaviour.
The goldens under `core/internal/characterization/testdata` will
be re-recorded when C2-4 lands.