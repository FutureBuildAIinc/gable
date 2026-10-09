<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Millwork

The millwork module is the catalog of options a sales rep can add to
a millwork order: door styles, finishes, hardware sets, glass
options. Each option carries a category, a name, a price adjustment
in integer cents, and an `attributes` JSON value the create carried
(null when it carried none). The catalog is global, not branch
scoped.

The Go code is in `core/internal/millwork/`. The migration that
brought the catalog onto the contract is
`core/migrations/096_crm_projects_millwork_wire_contract.sql`.

## What it does in a yard

A millwork order is a special order: a door, a window, a cabinet
set built to the customer's specifications. The options in the
catalog are the building blocks the sales rep picks from when
configuring the order. The price adjustment is what the option
adds (positive) or removes (negative) from the configured price;
the attributes are the JSON the dealer wants to remember about
the option (a finish color, a hardware set, a glass spec).

## Routes

Every route is in `core/api/fragments/millwork.yaml` and the
registered handles are in `core/internal/millwork/handler.go`. The
route census (`core/api/ROUTES.txt`) lists each one under the
`millwork` module column.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/millwork/options` | Cursor list of options of a category, newest first; category required. |
| POST | `/api/v1/millwork/options` | Create an option, writes `millwork_option.created`. |
| GET | `/api/v1/millwork/options/{id}` | Get one option with its revision as ETag. |

All three routes sit behind the millwork app gate (a 404
`app_disabled` when the millwork app is off) and the `admin`,
`owner` or `sales` guard.

## The main resource

`MillworkOption` (see `core/api/fragments/millwork.yaml`
`components.schemas.MillworkOption`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The option id. |
| `category` | text | 1 to 50 characters; the filter on the list route. |
| `name` | text | 1 to 100 characters. |
| `price_adjustment_cents` | int64 | A positive option adds to the configured price, a negative one discounts it. |
| `attributes` | object, array, string, number, boolean, null | The JSON the create carried; null when it carried none. |
| `revision` | int64 | Starts at 1; returned as ETag on the by-id read. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

### Money and quantity conventions

`price_adjustment_cents` is integer cents under the `_cents`
convention. The list cursor is the keyset cursor of ADR 0001
section 2; the list envelope is `{items, next_cursor, limit}`
with `total` only under `include=total`. The `category` filter
is required and exact match; a missing or repeated `category` is
a 400.

## Lifecycle and transitions

An option has no lifecycle of its own. It is created through
`POST` and read through `GET`; the create writes one
`millwork_option.created` event and one `audit_log` row in the
same transaction. The option is not branch scoped, so the
revision is the only gate the by-id read uses.

## Events the module writes

The constant is in `core/internal/millwork/service.go`
(`EventCreated`). Every create writes the event as the last
statement of its transaction
([ADR 0003](../adr/0003-events-outbox.md)).

## Scopes, roles and keys

A machine key reaching the millwork routes needs `millwork:read`
for `GET` and `HEAD`, and `millwork:write` for `POST` (ADR
0002). The user guard at the serve layer is `admin`, `owner`, or
`sales`. A key without the scope is `403 forbidden`; the audit
row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.

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

Then, with a sales role bearer:

```
curl -X GET 'http://localhost:8080/api/v1/millwork/options?category=DOOR&include=total' \
  -H 'Authorization: Bearer <token>'
```

The wire tests in `core/internal/millwork/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the millwork's scenarios.
