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
`portal` module column (the project routes share the portal prefix).

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
embeds the project and three lists: `orders`, `deliveries` and
`invoices`. Each item is a `ProjectItem`: `id`, `type`, `status`,
`total_cents`, `created_at`, `reference`. The totals are integer
cents, never floats.

### Money and quantity conventions

The dashboard carries no quantity, only integer cents under
`total_cents`. Money is integer cents under `_cents`; the project's
own list cursor is the keyset cursor of ADR 0001 section 2; the
list envelope is `{items, next_cursor, limit}` with `total` only
under `include=total`.

## Lifecycle and transitions

A project has two wire statuses: `active` and `completed`. The
transition is a write through `PUT /api/portal/v1/projects/{id}`
with `{"name": "...", "status": "completed", "revision": n}`.
Anything outside the lowercase vocabulary is `400
validation_failed`. The `inactive` storage value is never set
through the wire; the migration that brought projects onto the
contract mapped legacy rows into `inactive` and the wire allows
reads of those rows.

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
way to exercise the module end to end. From the repository root:

```
make stack-up
make migrate
make seed
make serve
```

Then, with a portal session for the seeded customer:

```
curl -X GET 'http://localhost:8080/api/portal/v1/projects?include=total' \
  -H 'Authorization: Bearer <portal session JWT>'
```

The wire tests in `core/internal/project/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the project's scenarios.