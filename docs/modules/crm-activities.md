<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# CRM activities

An activity is a log entry on a customer: a call, a meeting, an email,
a note. It carries the kind, a description, the contact it concerned,
the user who logged it and the date the activity happened. Activities
are the trail of who talked to whom, when, and about what. They live
on the customer and are reached through the customer's id.

The Go code is in `core/internal/crm/`. The migration that brought
the table onto the contract is
`core/migrations/096_crm_projects_millwork_wire_contract.sql`.

## What it does in a yard

The yard's sales team keeps a paper trail on every active customer.
A rep calls a builder, the call is logged with a one line summary.
A rep visits a job site, the meeting is logged with notes. The trail
is what the next rep reads when they pick up the account, and what
the office reads when a complaint comes in.

## Routes

Every route is in `core/api/fragments/activities.yaml` and the
registered handles are in `core/internal/crm/handler.go`. The route
census (`core/api/ROUTES.txt`) lists each one under the `activities`
module column for the by-id routes and under the `customers` module
column for the by-customer routes.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/customers/{customerId}/activities` | Cursor list of a customer's activities, newest first; activity_type and contact_id filter. |
| POST | `/api/v1/customers/{customerId}/activities` | Log an activity, writes `activity.created`. |
| GET | `/api/v1/activities/{id}` | Get one activity. |
| PUT | `/api/v1/activities/{id}` | Update an activity, on the client's revision. |
| DELETE | `/api/v1/activities/{id}` | Delete an activity, writes `activity.deleted`. |

The by-customer routes carry the `customers` scope segment
(`customers:read` for the list, `customers:write` for the create);
the by-id routes carry the `activities` scope segment
(`activities:read`, `activities:write`).

## The main resource

`Activity` (see `core/api/fragments/activities.yaml`
`components.schemas.Activity`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The activity id. |
| `customer_id` | UUID | The customer the activity is logged against. |
| `contact_id` | UUID, nullable | The contact at the customer the activity concerned. |
| `activity_type` | lowercase enum | `call`, `meeting`, `email`, `note`. |
| `description` | text | 1 to 4000 characters, free text. |
| `logged_by` | UUID, nullable | The user (or machine key) who logged the activity. |
| `activity_date` | timestamp | When the activity happened, RFC 3339 UTC. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

The `ActivityType` enum is the four values above; the database keeps
its uppercase CHECK vocabulary, mapped at the boundary (ADR 0001
section 6).

### Money and quantity conventions

Activities carry no money and no quantity. The list cursor is the
keyset cursor of ADR 0001 section 2; the list envelope is
`{items, next_cursor, limit}` with `total` only under
`include=total`.

## Lifecycle and transitions

An activity has no lifecycle of its own. It is created, updated
and deleted through the routes above. The `revision` field tracks
every write; updates require the client's revision (`If-Match` or
the body's `revision`, neither is `428 precondition_required`, a
stale one is `409 stale_revision`).

## Events the module writes

The constants are in `core/internal/crm/service.go` (`EventCreated`,
`EventUpdated`, `EventDeleted`). Every mutation writes the event as
the last statement of its transaction
([ADR 0003](../adr/0003-events-outbox.md)), alongside the `audit_log`
row the module writes for every change.

## Scopes, roles and keys

A machine key reaching the by-id activities routes needs
`activities:read` for `GET` and `HEAD`, and `activities:write` for
every other method (ADR 0002; the segment is the first path segment
under `/api/v1/`). The by-customer routes need `customers:read` and
`customers:write`. The user guard at the serve layer is the standard
sales wall; the exact guard is composed in
`core/internal/app/serve/wire_branch_wall.go` at `wall.crm`. Every
route carries the branch wall through the activity's customer: a
second branch's request is a 404. A key without the scope is `403
forbidden`; the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.

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
curl -X GET 'http://localhost:8080/api/v1/customers/<id>/activities?include=total' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

The wire tests in `core/internal/crm/wire_test.go` pin the platform
and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for the
activities' scenarios.