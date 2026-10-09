<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Configurator

The product configurator is the rule engine that decides what a
non-stock product is, given a set of customer selections. The
configurator's rules say which attributes depend on which other
attributes, what values are allowed under a given selection, and
which presets are available out of the box. The rules are seeded
master data, the engine is read only, and the wire is a small set
of read routes plus one build route that turns selections into a
SKU.

The Go code is in `core/internal/configurator/`. The rules table
comes from `core/migrations/024_configurator_rules.sql`; the routes
needed no contract migration.

## What it does in a yard

A yard sells many non-stock items whose attributes drive whether
the part can be built at all. A builder asks for a 2x6 in
Southern Yellow Pine, pressure treated, no prime, eight foot.
The configurator says yes, prices it, and the order writes a
non-stock line. The rules are what make the configurator say
yes or no; the presets are the out-of-the-box answers the
sales rep picks from.

## Routes

Every route is in `core/api/fragments/configurator.yaml` and the
registered handles are in `core/internal/configurator/handler.go`.
The route census (`core/api/ROUTES.txt`) lists each one under the
`configurator` module column.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/configurator/rules` | List every configurator rule. |
| GET | `/api/v1/configurator/options` | List the allowed values of one attribute, given the current selections. |
| GET | `/api/v1/configurator/presets` | List the active presets. |
| POST | `/api/v1/configurator/validate` | Validate a set of selections against the rules. |
| POST | `/api/v1/configurator/build-sku` | Build a non-stock SKU from selections. |

Every route registers under the millwork app (a 404
`app_disabled` when the millwork app is off) and the `admin`,
`owner` or `sales` guard.

## The main resources

`ConfiguratorRule` (see `core/api/fragments/configurator.yaml`
`components.schemas.ConfiguratorRule`) carries `attribute_type`,
`attribute_value`, `depends_on_type`, `depends_on_value`,
`is_allowed` (boolean), `error_message` (nullable). A rule is
matched by its dependency type and value; the matrix is ordered by
dependency type and value, then attribute type and value, never
null.

`ConfiguratorPreset` carries `id`, `name`, `description` (nullable),
`product_type`, `config` (an object), `is_active`, `created_at`,
`updated_at`.

`ConfiguratorAvailableOption` is the response of the options route: a
list of allowed values for the named attribute under the given
selections, deterministically ordered by value. Each carries
`value`, `allowed` (boolean), `message` (nullable).

### Money and quantity conventions

The configurator carries no money. The validate route returns a 200
with `{valid, conflicts}`; a conflict is `valid: false` with one
entry per violated rule (`attribute_type`, `attribute_value`,
`depends_on_type`, `depends_on_value`, `message`). The build route
validates first, answers 400 `validation_failed` with one
`config_conflict` blocker per violated rule, and otherwise returns
`{sku, description}`.

## Lifecycle and transitions

The configurator has no lifecycle of its own. The rules and the
presets are master data; the module owns no write, no audit row
and no event. The build route is idempotent through the platform
`Idempotency-Key` header.

## Events the module writes

The configurator writes no outbox events. The build and validate
routes do not write `audit_log` rows.

## Scopes, roles and keys

A machine key reaching the configurator routes needs
`configurator:read` for `GET` and `configurator:write` for
`POST /api/v1/configurator/validate` and
`POST /api/v1/configurator/build-sku` (ADR 0002; the segment is
the first path segment under `/api/v1/`). The user guard at the
serve layer is `admin`, `owner`, or `sales`. A key without the
scope is `403 forbidden`; the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 3, 5, 6, 9.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.

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
curl -X GET 'http://localhost:8080/api/v1/configurator/rules' \
  -H 'Authorization: Bearer <token>'
```

The wire tests in `core/internal/configurator/wire_test.go` pin
the platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes
for the configurator's scenarios.