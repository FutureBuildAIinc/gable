<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Projects

A project is the customer's job. A builder or contractor opens a
project for each address they are working on, and the orders,
deliveries and invoices that belong to that job are filed under it.
The portal renders the project's dashboard: a header (name, status,
revision) and three lists (orders, deliveries, invoices), with the
running totals in integer cents.

The Go code is in `core/internal/project/`. The migration that
brought the table onto the contract is
`core/migrations/096_crm_projects_millwork_wire_contract.sql`.

## What it does in a yard

A builder does not order materials one at a time. They open a job
for the address, the order, the deliveries and the invoices roll up
under the project, and the dashboard shows what has been delivered,
what has been billed, and what is still open. The portal renders
this so the builder sees the job from their side.

## Routes

Every route is in `core/api/fragments/project.yaml` and the
registered handles are in `core/internal/project/handler.go`. The
route census (`core/api/ROUTES.txt`) lists each one under the
`internal/project` package (the project routes share the portal
prefix).

| Method | Path | One line |
|---|---|---|
| GET | `/api/portal/v1/projects` | Cursor list of the customer's projects, newest first; status filter. |
| POST | `/api/portal/v1/projects` | Create a job, writes `project.created`. |
| GET | `/api/portal/v1/projects/{id}` | One job with its orders, deliveries and invoices (the dashboard). |
| PUT | `/api/portal/v1/projects/{id}` | Rename a job or change its status, on the client's revision. |

The routes live under the portal prefix and share the portal session
(`portal_token` cookie or bearer JWT). Every read and write is
scoped to the customer the portal chain identifies: another
customer's project is a 404.

## The main resource

`Project` (see `core/api/fragments/project.yaml`
`components.schemas.Project`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The project id. |
| `customer_id` | UUID | The customer the project is for. |
| `name` | text | The job name, free text. |
| `status` | lowercase enum | `active`, `completed`. `inactive` is a storage value the 091 job merge wrote; writes accept `active` and `completed` only. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

The `ProjectDashboard` (returned by `GET /api/portal/v1/projects/{id}`)
is a `ProjectDashboard` object whose keys are the project (a nested
`Project` object under the `project` key) and three list keys,
`orders`, `deliveries` and `invoices` (each an array of `ProjectItem`,
newest first, never null). Each item is a `ProjectItem`: `id`,
`type`, `status`, `total_cents`, `created_at`, `reference`. Item
totals are integer cents (ADR 0001 section 7) and `total_cents` is
null on a delivery, which carries no money.

`ProjectPage` is the list envelope (`core/api/fragments/project.yaml`
`components.schemas.ProjectPage`):

| Field | Wire form | Note |
|---|---|---|
| `items` | array of `Project` | Newest first on `(created_at, id)`. |
| `next_cursor` | string, nullable | The keyset cursor (ADR 0001 section 2); null on the last page. |
| `limit` | integer | The page size in effect. |
| `total` | integer | Present only under `include=total`. |

### Create and update body

`ProjectRequest` is the body of `POST /api/portal/v1/projects` and
`PUT /api/portal/v1/projects/{id}`. No field is required, but create
and update differ.

| Field | Wire form | Note |
|---|---|---|
| `name` | text | 1 to 255 characters (the column is `varchar(255)` in characters). Required on create; absent on update keeps the stored value. |
| `status` | lowercase enum | `active` or `completed`. Create ignores any `status` (the project always starts `active`); update applies the value when set. |
| `revision` | integer | The body form of the precondition on `PUT`; a create carrying one is a 400 naming `revision`. |

Update is partial: absent fields keep their stored values. A request
that names nothing to change (no `name` and no `status`) is a no-op:
the row answers with its current revision and the service writes
no audit row, no event, and the revision does not move
(`core/internal/project/service.go` `Update`).

### Money and quantity conventions

The dashboard carries no quantity, only integer cents under
`total_cents`. Money is integer cents under `_cents`; the project's
own list cursor is the keyset cursor of ADR 0001 section 2.

### Headers and codes

`Idempotency-Key` is accepted on every mutating request (create and
update) for an opt in replay; `ETag` is returned on every record (GET, POST and PUT); the
create response carries a `Location` header. A write without
`If-Match` and without a body `revision` is `428
precondition_required`; a body or header that disagrees with the
server revision is `409 stale_revision`; `If-Match: *` or an
`If-Match` that is a list is `400 bad_request`.

## Lifecycle and transitions

A project has two wire statuses: `active` and `completed`. The
transition is a write through `PUT /api/portal/v1/projects/{id}`
with `{"name": "...", "status": "completed", "revision": n}`.
Anything outside the lowercase vocabulary is `400
validation_failed`. The `inactive` storage value is never set
through the wire; the job merge in migration 091 wrote it for
legacy rows, and the wire reads those rows back as `inactive`.

A completed project can be moved back to `active` through the
same route. The status of the project does not change the status
of its orders, deliveries or invoices; those keep their own
lifecycles and their own revisions.

## Events the module writes

The constants are in `core/internal/project/service.go`
(`EventCreated`, `EventUpdated`). Every mutation writes the event
as the last statement of its transaction
([ADR 0003](../adr/0003-events-outbox.md)), alongside the
`audit_log` row the module writes for every change.

## Scopes, roles and keys

A machine key does not reach the project routes today; the routes
are portal-only and authenticate through the portal session. The
portal's session JWT names the customer and the user, and a
request that names another customer's project id is a 404.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0004-web-session-custody.md`](../adr/0004-web-session-custody.md): the portal session chain.

## How to try it locally

The repository's own seed and the local make targets are the only
way to exercise the module end to end. `make up` builds and starts
the local stack (Postgres, migrate and seed, `core serve`,
`core worker`, the web front door) on http://127.0.0.1:8080 with
`AUTH_MODE=dev`; `make down` removes it. To run the core from source
instead: `make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run -tags devauth ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Then, with a portal session for the seeded customer:

```
curl -X GET 'http://localhost:8080/api/portal/v1/projects?include=total' \
  -H 'Authorization: Bearer <portal session JWT>'
```

The wire tests in `core/internal/project/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the project's scenarios.