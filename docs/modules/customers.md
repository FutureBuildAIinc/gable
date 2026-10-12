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
(`core/api/ROUTES.txt`) lists each one under the module named in the
`module` column (the column carries the registering package
directory).

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

A `PriceLevel` (see `core/api/fragments/customer.yaml`
`components.schemas.PriceLevel`) carries `id`, `name`, `multiplier`
(a price multiplier, a rate and not money), `created_at`,
`updated_at`. The level the customer holds is embedded on the
`Customer` document as `price_level` (and named by `price_level_id`
when set).

The customer's lumber index escalation policy (`EscalationPolicy`,
see `core/api/fragments/customer.yaml`
`components.schemas.EscalationPolicy`) carries `customer_id`,
`policy` (`auto_escalate`, `flag_for_requote`, `require_ack`),
`threshold_percent` (a decimal string with at most four fraction
digits, above 0 and at most 50), `agreement_signed_at` (a
timestamp; required for `auto_escalate`), `agreement_ref`, and the
customer's `revision`. `EscalationPolicyRequest` (the body of
`PUT /escalation-policy`) takes the same fields except
`customer_id` and requires `policy` and `threshold_percent`.

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
a credit memo in `draft`, `open` or `partial`, or a posted payment
with an unapplied amount (a deposit or not;
`customer/repository.go` `OpenDocuments` reads
`payments.status = 'POSTED' AND amount_unapplied > 0`).

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
`contacts:read` or `contacts:write`; payment terms need
`payment-terms:read` or `payment-terms:write`; price levels need
`price_levels:read`. The user guard at the serve layer is
`admin`, `owner` and `sales` for customer reads and writes, with
`admin`, `owner`, `finance` taking the narrower payment terms writes and
`finance` added on exactly the three routes that change a customer's
restricted fields (`PUT /customers/{id}`, `PATCH /customers/{id}/salesperson`
and `PUT /customers/{id}/escalation-policy`; the shared customer rule below
hears its role there) and on the two reads those writes need
(`GET /customers/{id}` and `GET /customers/{id}/escalation-policy`: a caller
that must send the revision as `If-Match` has to be able to read it; reads
only), and on no other customer route; the exact guard is composed in
`core/internal/app/serve/wire_branch_wall.go` at `wall.customers`.
A key without the scope is `403 forbidden`; the audit row carries the
refused scope.

A key bound to one branch (ADR 0007 section 5.5) reads a customer its
branch shares with another, and changes a shared customer's record only on
the branch local fields. The rule is deny by default: any header field not
listed as branch local is restricted, so a field added later is restricted
until it is listed in `branchLocalFields` (`core/internal/customer/shared.go`).
A restricted write is `403 forbidden` in the words "this key may act
only on its own branch: the customer is also held by branch <id>" (the lowest
numbered other branch), with the `key.branch_refused` row, and the record is
untouched. The customer scoped pricing rules, tax exemptions and category
rules refuse a shared customer's bound key write in the same quoted lead in
(`core/pkg/middleware/keybranch.go`), so every shared customer refusal a
bound key meets carries words ADR 0007 section 5.5 quotes; each wall
refusal writes its own `key.branch_refused` row naming the refused call's
method, path and the pin branch, with nothing written to the wall's table. A customer the
key's branch holds alone and an unshared customer
are unchanged for every caller. Who else may change a shared customer's
restricted fields: a user only with the `finance` or `owner` role (a sales
or admin user is refused with the lead in "this role may not change a shared
customer's restricted fields" naming the lowest
numbered branch that holds the customer, and a caller with no claims at all
is refused fail closed), and a dealer wide key only while it carries the
dedicated `customers:restricted` scope; every other unbound key is refused
with the lead in "this key may not change a shared customer's restricted
fields" over the same clause (the lead in names the caller, so the words are
true for whoever reads them). A refusal of a user or an unbound key writes a
`customer.shared_restricted_refused` audit row for the customer naming the
actor, the refused fields, that branch and the method and path. A branch
bound key is refused even with the scope. The scope is in the grant grammar
but no route requires it, so no module scope and no default grant carries
it; only an owner may mint it, and the mint's `key.created` audit row names
it. An allowed key's change writes a `customer.shared_restricted_changed`
audit row naming the key and the before and after value of every field; a
user's change writes none.

| Route | Field | Class | Reason |
|---|---|---|---|
| `PUT /customers/{id}` | `account_number`, `name`, `address` | RESTRICTED | the identity and billing address every branch's documents carry |
| | `email`, `phone` | BRANCH LOCAL | contact channels of whoever deals with the customer |
| | `tier`, `price_level_id` | RESTRICTED | price every branch's sales |
| | `is_active` | RESTRICTED | deactivates the customer for every branch |
| | `credit_limit_cents`, `currency`, `payment_terms_id`, `po_required` | RESTRICTED | credit, currency, terms and the purchase order rule at every branch |
| | `salesperson_id` | RESTRICTED | one salesperson per customer across branches |
| `PATCH /customers/{id}/salesperson` | `salesperson_id` | RESTRICTED | as above |
| `PUT /customers/{id}/escalation-policy` | policy, threshold, agreement | RESTRICTED | governs the repricing of every branch's quotes |
| `POST /customers/{id}/ship-tos` with `is_default: true` | the existing default | shared record | the add clears the shared default ship to, so it is refused like a change; an add naming no default is allowed |

Ship-tos and contacts carry no branch column, so on a shared customer every
existing one is shared: a bound key reads them and may add a new one with
`POST`, and is refused `PUT /ship-tos/{id}`, `PUT /contacts/{id}` and
`DELETE /contacts/{id}` in the bound key's words. A ship-to add that names
`is_default: true` is refused the same way, because naming a default
clears the customer's existing default, which on a shared customer is a
shared ship to every branch's delivery orders default to; an add that
names no default stays allowed. On a customer the key's branch
holds alone, all of it is allowed.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 4.2: currency, the `open_documents` rule; section 7: the customer contract on the wire.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 6: price levels on the customer.
- The customer's unapplied cash and the open-documents rule (order, invoice, credit memo, posted payment with unapplied cash) live in [payments.md](payments.md) (C2-4).

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