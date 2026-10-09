<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Customers

A customer is the company or person the dealer sells to. The customer
record carries the account number, the billing address, the tier that
sets the price level, the salesperson, the credit limit, the currency
override, the payment terms and the flag that says whether a purchase
order number is required on an order. The customer also owns ship-tos
(delivery addresses), contacts (people who can place orders) and the
escalation policy that says how a lumber price exposure is escalated.

This module is the wire contract for customer master data, payment
terms and contact authority, plus the read of price levels. The
activities routes under a customer were moved to the CRM activities
module in C5-1b. The Go code is in `core/internal/customer/`. The
migration that brought the table onto the contract is
`core/migrations/091_customers_wire_contract.sql`.

## What it does in a yard

The customer is who the order is for and who gets the invoice. A yard
keeps the customer's tier, the price level that flows from it, the
contacts who can place orders and the ship-tos that deliveries go to.
A customer with a credit limit lands orders over the limit on hold
until a finance person releases them. A customer with `po_required`
forces the sales rep to enter a purchase order number at confirm.

## Routes

Every route is in `core/api/fragments/customer.yaml` and the registered
handles are in `core/internal/customer/handler.go`. The route census
(`core/api/ROUTES.txt`) lists each one under the `customers`,
`ship-tos`, `contacts` and `payment-terms` module columns.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/customers` | Cursor list of customers, newest first; q, tier, is_active, salesperson_id filter. |
| POST | `/api/v1/customers` | Create a customer; writes `customer.created`. |
| GET | `/api/v1/customers/{id}` | One customer with its revision as ETag. |
| PUT | `/api/v1/customers/{id}` | Replace a customer header; the currency override is locked while documents are open. |
| PATCH | `/api/v1/customers/{id}/salesperson` | Assign or clear the salesperson. |
| GET | `/api/v1/customers/{id}/escalation-policy` | Get the lumber index escalation policy. |
| PUT | `/api/v1/customers/{id}/escalation-policy` | Set the lumber index escalation policy. |
| GET | `/api/v1/customers/{id}/ship-tos` | List a customer's ship-tos. |
| POST | `/api/v1/customers/{id}/ship-tos` | Add a ship-to address. |
| GET | `/api/v1/ship-tos/{id}` | Get one ship-to. |
| PUT | `/api/v1/ship-tos/{id}` | Replace a ship-to. |
| GET | `/api/v1/payment-terms` | List the payment terms master. |
| POST | `/api/v1/payment-terms` | Create terms. |
| GET | `/api/v1/payment-terms/{id}` | Get one terms row. |
| PUT | `/api/v1/payment-terms/{id}` | Replace terms. |
| GET | `/api/v1/customers/{customerId}/contacts` | List a customer's contacts. |
| POST | `/api/v1/customers/{customerId}/contacts` | Add a contact. |
| GET | `/api/v1/contacts/{id}` | Get one contact. |
| PUT | `/api/v1/contacts/{id}` | Replace a contact. |
| DELETE | `/api/v1/contacts/{id}` | Delete a contact. |
| GET | `/api/v1/price_levels` | List the price level master. |

`ship-tos`, `contacts`, `payment-terms` and `price_levels` each carry
their own scope segment.

## The main resource

`Customer` (see `core/api/fragments/customer.yaml` `components.schemas.Customer`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The customer id. |
| `account_number` | text | The dealer's account number, unique. |
| `name` | text | The customer's name. |
| `email`, `phone`, `address` | text, nullable | Free text fields; `address` is the billing address, not a delivery address. |
| `tier` | lowercase enum | `retail`, `silver`, `gold`, `platinum`. |
| `is_active` | boolean | Whether the customer is active. |
| `primary_branch_id` | UUID | The home branch. |
| `price_level_id` | UUID, nullable | The price level that drives pricing. |
| `price_level` | object, nullable | Embedded price level summary. |
| `salesperson_id`, `salesperson_name` | UUID, text, nullable | The book owner. |
| `credit_limit_cents` | integer, nullable | The credit limit; null is no limit, zero is a limit of nothing. |
| `balance_cents` | integer | The AR balance, read only: the AR core is the single writer. |
| `currency` | ISO 4217, nullable | The customer's currency override; null means the dealer default. |
| `effective_currency` | ISO 4217 | The currency a new document of this customer takes. |
| `payment_terms_id`, `payment_terms` | UUID, object | The terms master row, embedded. |
| `po_required` | boolean | Whether an order of this customer must carry a PO number. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

A `ShipTo` carries `id`, `customer_id`, `code`, `name`, `line1`,
`line2`, `city`, `region`, `postal_code`, `country`, `phone`,
`delivery_instructions`, `tax_rate_percent`, `is_default`,
`is_active`, `revision`, `created_at`, `updated_at`.

A `Contact` carries `id`, `customer_id`, `first_name`, `last_name`,
`title`, `email`, `phone`, `role`, `is_primary`, `is_active`,
`can_place_orders`, `order_limit_cents`, `revision`, `created_at`,
`updated_at`. The order confirm checks `can_place_orders` and the
`order_limit_cents` against the order's total; an over-limit order by
a contact who cannot place them is refused (blocker
`contact_authority`).

A `PaymentTermsRecord` carries `id`, `code`, `name`, `kind`
(`net_days`, `day_of_month`, `due_on_receipt`), `net_days`,
`day_of_month`, `discount_percent`, `discount_days`, `is_active`,
`revision`, `created_at`, `updated_at`. The `kind` and the conditional
fields together drive the invoice due date.

### Money and quantity conventions

`credit_limit_cents`, `balance_cents` and `order_limit_cents` are
integer cents under `_cents` names. The rate on a ship-to
(`tax_rate_percent`) is a decimal string with at most four fraction
digits. The early payment discount percent is the same shape. The
balance is read only on the wire; the AR core is the writer.

## Lifecycle and transitions

A customer has no lifecycle of its own. It is enabled or disabled by
`is_active` and locked on `currency` change while it has open
documents. The currency override is refused with `409 conflict` and
blocker `open_documents` when the customer has an order in `draft`,
`confirmed` or `on_hold`, an invoice that is `unpaid` or `partial`,
a credit memo in `draft`, `open` or `partial`, or a deposit with an
unapplied amount.

A `ShipTo` and a `Contact` are versioned through `revision` but have no
lifecycle. A `Contact` may be deleted; a `ShipTo` may not be deleted
through the wire today.

## Events the module writes

The events are written in the same transaction as the mutation
([ADR 0003](../adr/0003-events-outbox.md)). The constants are in
`core/internal/customer/service.go` (`EventCreated`, `EventUpdated`).
The data is a small summary; the full document is read through `GET`.

## Scopes, roles and keys

A machine key reaching the customer routes needs `customers:read` for
`GET` and `customers:write` for every other method (ADR 0002; the
segment is the first path segment under `/api/v1/`). Ship-tos need
`ship-tos:read` or `ship-tos:write`; contacts need
`contacts:read` or `contacts:write`; payment terms writes need
`payment-terms:read` or `payment-terms:write`; price levels need
`price_levels:read`. The user guard at the serve layer is
`admin`, `owner`, `sales` for customer reads and writes, with
`admin`, `owner`, `finance` taking the narrower payment terms writes;
the exact guard is composed in
`core/internal/app/serve/wire_branch_wall.go` at `wall.customers`.
A key without the scope is `403 forbidden`; the audit row carries the
refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 4.2: currency, the `open_documents` rule; section 7: the customer contract on the wire.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 6: price levels on the customer.

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

Then, with a sales role bearer and the seeded branch:

```
curl -X POST http://localhost:8080/api/v1/customers \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @customer-create.json
```

The wire tests in `core/internal/customer/wire_test.go` pin the platform
and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for the
scenarios.