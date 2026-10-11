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
The configurator says yes and builds the SKU; pricing is the
order's. The rules are what make the configurator say
yes or no; the presets are the out-of-the-box answers the
sales rep picks from.

## Routes

Every route is in `core/api/fragments/configurator.yaml` and the
registered handles are in `core/internal/configurator/handler.go`.
The route census (`core/api/ROUTES.txt`) lists each one under the
`internal/configurator` module (the `module` column is the registering
package's directory relative to the Go module root).

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/configurator/rules` | List every configurator rule. |
| GET | `/api/v1/configurator/options` | List the allowed values of one attribute, given the current selections. |
| GET | `/api/v1/configurator/presets` | List the active presets. |
| POST | `/api/v1/configurator/validate` | Validate a set of selections against the rules. |
| POST | `/api/v1/configurator/build-sku` | Build a non-stock SKU from selections. |

Every route registers under the millwork app (a 404
`app_disabled` when the millwork app is off) and the `admin`,
`owner` or `sales` guard. The `GET` routes take a strict set of
query parameters; a parameter the route does not declare is a 400
`unsupported_query_parameter` (`core/internal/platform/httpx`).

| Method | Path | Query parameters |
|---|---|---|
| GET | `/api/v1/configurator/rules` | none (every declared name is a 400) |
| GET | `/api/v1/configurator/options` | `attribute_type` (required, single value); `selections` (a comma separated list of `Type=Value` pairs) |
| GET | `/api/v1/configurator/presets` | `product_type` (single value; absent or empty lists every active preset) |
| POST | `/api/v1/configurator/validate` | none (every declared name is a 400) |
| POST | `/api/v1/configurator/build-sku` | none (every declared name is a 400) |

The options route reads `attribute_type` once; a repeated or blank
value is a 400 naming it. The options route reads `selections`
once; a repeated parameter is a 400 naming it, and a pair without
`=` or with an empty key or value is a 400 ("must be a comma
separated list of Type=Value pairs"). The presets route reads
`product_type` once; a repeated parameter is a 400 naming it.

## The main resources

`ConfiguratorRule` (see `core/api/fragments/configurator.yaml`
`components.schemas.ConfiguratorRule`) carries `id`, `attribute_type`,
`attribute_value`, `depends_on_type`, `depends_on_value`,
`is_allowed` (boolean), `error_message` (nullable), plus the
timestamps `created_at` and `updated_at`. A rule is matched by its
dependency type and value; the matrix is ordered by dependency type
and value, then attribute type and value, never null.

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

## Request bodies

The two `POST` routes take a JSON body parsed in
`core/internal/configurator/handler.go` `parseSelectionsBody`. The
body is bounded by the platform request size limit (10 MiB,
`core/internal/app/serve/chain.go`); a larger body is a 413. A body
that is not valid JSON, or is empty, is a 400 that names no field;
an empty `selections` map is a 400 naming `selections`. Every
problem collected by the parse is answered in a single 400.

### Validate (`POST /api/v1/configurator/validate`)

The schema is `ConfiguratorValidateRequest`; the Go type is the
local `parseSelectionsBody` decode.

| Field | Required | Note |
|---|---|---|
| `selections` | yes | A map from attribute type to chosen value; missing or empty is a 400 naming "is required". |
| `product_type` | refused | A present value is a 400 naming "is not a field of this route". |

### Build SKU (`POST /api/v1/configurator/build-sku`)

The schema is `ConfiguratorBuildSKURequest`; the Go type is the
local `parseSelectionsBody` decode with `productType = true`.

| Field | Required | Note |
|---|---|---|
| `product_type` | yes | Absent or blank after trim is a 400 naming "is required". |
| `selections` | yes | A map from attribute type to chosen value; missing or empty is a 400 naming "is required". |

The SKU rule is `NS-<product type code>-<species>-<grade>-<treatment>-<dimensions>`
joined with `-` (`core/internal/configurator/service.go` `BuildSKU`).
The product type code maps `Lumber` to `LBR`, `Door` to `DR`,
`Trim` to `TRM`, `Panel` to `PNL`; any other product type is upper
cased and cut to three characters. The four selection keys are
`Species`, `Grade`, `Treatment`, `Dimensions`, in that order;
their values are upper cased and spaces become hyphens. Selection
keys other than those four are ignored. Selection values that are
empty or `"None"` are skipped (they do not appear in the SKU or
the description). The description joins the same keys with spaces
in the same order, again skipping empty and `"None"` values, and
prefixes the product type.

## Error answers on the write routes

The error answers below are the ones declared in
`core/api/fragments/configurator.yaml` and the codes raised by the
service and the platform layer; the source that raises each is
named in the source column.

| Status | Meaning | Source |
|---|---|---|
| 400 | A missing required field, an unknown field on the route (`product_type` on validate), a cap exceeded, an empty `selections` map, or a `validation_failed` whose details carry one `config_conflict` blocker per violated rule on build-sku. | `configurator.yaml` `BadRequestEither`; `handler.go` `parseSelectionsBody`; `service.go` `BuildSKU` (`httpx.CodeValidationFailed`, `httpx.Blocker("config_conflict", ...)`) |
| 401 | The auth layer answers every route with the ADR 0001 envelope, code `unauthorized`. | `_shared.yaml` `Unauthorized`; `pkg/middleware` auth |
| 403 | The role guard or the machine-key scope check refuses the caller, code `forbidden`. | `_shared.yaml` `Forbidden`; `pkg/middleware` machinekey |
| 404 | The millwork app is disabled and the route's gated router answers `app_disabled`. | `configurator.yaml` `AppDisabled`; apps registry gate |
| 413 | The body exceeded the platform's request size limit. | `_shared.yaml` `PayloadTooLarge` |
| 422 | The idempotency layer rejected the write (`idempotency_key_reused` for a key replayed with a different body fingerprint). | `_shared.yaml` `UnprocessableEntity`; idempotency middleware |
| 500 | A handler or database error. | `configurator.yaml` `InternalErrorEither` |

## Lifecycle and transitions

The configurator has no lifecycle of its own. The rules and the
presets are master data; the module owns no write, no audit row
and no event. Both `POST` routes take the `Idempotency-Key`
header; a replay of a completed request answers with
`Idempotency-Replayed: true` (the `Idempotency-Key` parameter on
`core/api/fragments/_shared.yaml`).

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