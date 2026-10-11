<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Tech admin

The tech admin surface is the dealer administrator's seat for the keys and
secrets that let other systems talk to this one. It mints the machine API
keys a service uses to call the rest of the platform; it stores the AI and
routing provider keys that internal/ai and internal/delivery read; and
it lists and toggles the apps the operator has installed (the catalog the
Apps page renders). It also exposes one operator-owned trigger, the price
exposure safety net scan, that lives at the pricing module's own service
and is mounted under the admin segment because the operator invokes it
from the admin nav.

The Go code is in `core/internal/techadmin/` (keys, AI and routing
settings), `core/pkg/apps/` (the apps catalog and its mount) and
`core/internal/pricing/` (the exposure scan handler,
`exposure_handler.go`). The migration
that brought the surfaces onto the contract is
`core/migrations/095_admin_wire_contract.sql` (the admin revision anchors
and the api_keys timestamps); the apps table the platform boot syncs is
created by `core/migrations/074_*.sql`.

## What it does in a yard

An operator opens Tech Admin from the desk. They mint a machine key for
the integration partner, copy the raw key out of the create response, and
hand the partner the value once; the partner stores it, and every later
call authenticates by the salted Argon2 hash the platform keeps. They
open AI Settings, paste the OpenRouter key, save, and the AI client
reads the override once its short cache expires (the override wins over the
environment default). They open Routing Settings, paste the
OpenRouteService key, and the dispatcher reads it through the routing
engine. They open Apps, scan the catalog of installed apps, and disable
the millwork app.

## Routes

Every route is in `core/api/fragments/admin.yaml` and
`core/api/fragments/apps.yaml`. The registered handles are in
`core/internal/techadmin/handler.go`, `core/internal/staff/handler.go`,
`core/internal/staff/routes.go` (the staff roster and the module flags)
and `core/pkg/apps/handler.go` (the apps catalog). The route census
(`core/api/ROUTES.txt`) lists each one under the `internal/techadmin`,
`internal/staff` or `pkg/apps` package (the route census has a `package`
column, not a module column).

The exposure scan route at `POST /api/v1/admin/exposure-scan` is the
pricing module's `HandleAdminScan`; the route guard admits
`admin`, `owner` and `sales`, and the handler then refuses all but
`admin` and `owner` with a 403, judging by the first role of the
token (the role claim, else the first of the roles array). In dev auth
mode the caller is treated as owner.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/admin/keys` | Cursor list of machine keys, newest first; revoked keys included. |
| POST | `/api/v1/admin/keys` | Mint a machine key, returns the raw key once. |
| DELETE | `/api/v1/admin/keys/{id}` | Revoke a key (idempotent 204 on an already revoked id). |
| GET | `/api/v1/admin/settings/ai` | Read the AI settings document with its revision. |
| PUT | `/api/v1/admin/settings/ai` | Save the OpenRouter key and optional base URL override. |
| DELETE | `/api/v1/admin/settings/ai` | Clear the AI admin override, revert to env defaults. |
| GET | `/api/v1/admin/settings/routing` | Read the routing settings document with its revision. |
| PUT | `/api/v1/admin/settings/routing` | Save the OpenRouteService key. |
| DELETE | `/api/v1/admin/settings/routing` | Clear the routing admin override, revert to env defaults. |
| GET | `/api/v1/apps` | Read the catalog with live enablement state. |
| POST | `/api/v1/apps/{key}/enable` | Enable an app, 409 with blockers if a dependency is disabled. |
| POST | `/api/v1/apps/{key}/disable` | Disable an app, 409 `app_core` for a core app, 409 with dependents otherwise. |
| POST | `/api/v1/admin/exposure-scan` | Run the price exposure safety net scan synchronously. |

The staff roster routes (`/api/v1/admin/staff`, `/api/v1/admin/staff/{id}`,
`/api/v1/admin/staff/{id}/modules`, `/api/v1/admin/staff/{id}/modules/{module_id}`)
and the module flag routes (`/api/v1/admin/modules`,
`/api/v1/admin/modules/{id}`) sit under the admin segment and share the
guard, but they belong to the staff module's page. The key management
routes (`/api/v1/admin/keys/...`) are user only: a machine key is refused
there whatever scopes it holds (ADR 0002 section 4).

## The main resources

The tech admin surface carries four documents and one settings resource.
The keys document and the settings documents are in
`core/api/fragments/admin.yaml`; the apps catalog item is in
`core/api/fragments/apps.yaml`.

`ApiKey` (`components.schemas.ApiKey`) is the row the keys list returns
and the row embedded in a create response.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The api_keys row's id. |
| `name` | text | The operator-set label; not unique. |
| `prefix` | text | The first 12 characters of the raw key; the only identifier the platform serves. |
| `scopes` | array of text | The granted scopes verbatim (ADR 0002: a grant reads back as written); never null. |
| `created_at` | timestamp | RFC 3339 UTC. |
| `last_used_at` | timestamp, nullable | The last successful authentication; null until first use. |
| `revoked_at` | timestamp, nullable | The revocation time; null while the key is live. |

The `key_hash` field is `json:"-"` on the Go type
(`techadmin.APIKey`, `core/internal/techadmin/model.go`) and never
leaves the package. The raw key never appears in `ApiKey`: it lives in
the `api_key` field of the `AdminCreateKeyResponse` envelope and only
there, at mint.

`AdminCreateKeyRequest` carries `name` (required) and `scopes`
(optional, array of text). `AdminCreateKeyResponse` carries `api_key`
(the raw key, `sk_live_` plus 43 URL safe base64 characters) and `key`
(the saved `ApiKey` row).

`AISettings` (`components.schemas.AISettings`) is the AI settings
document: a singleton with a revision anchor in `admin_revisions`.

| Field | Wire form | Note |
|---|---|---|
| `configured` | boolean | True when a key is in effect (admin override or env). |
| `source` | text enum | `admin`, `env`, or `none`. |
| `key_hint` | text, nullable | The first 10 and last 4 characters joined by three dots, or four asterisks for a key of 12 characters or fewer; null when no key is configured. |
| `base_url` | text, nullable | The admin override of the OpenRouter base URL; present only when the override is stored, null otherwise. |
| `revision` | integer | The admin_revisions row for `admin.settings.ai`. |

`AdminSaveAISettingsRequest` carries `api_key` (required),
`base_url` (pointer: absent leaves the override, empty clears it, a URL
sets it; http and non host URLs are refused) and `revision` (the body
form of the `If-Match` precondition; 428 without, 409 stale).

`RoutingSettings` (`components.schemas.RoutingSettings`) is the routing
settings document: the AI settings' shape without the base URL. Its
revision anchor is its own (`admin.settings.routing`); the save takes
the same precondition and answers the new revision in body and ETag.

`AppsManifestStatus` (`components.schemas.AppsManifestStatus`) is the
catalog entry the apps list returns.

| Field | Wire form | Note |
|---|---|---|
| `key` | text | The app's manifest key. |
| `name` | text | The display name. |
| `summary` | text | The customer-facing summary the Apps admin page shows. |
| `category` | text | The catalog category the apps page groups by. |
| `core` | boolean | True for a core app that the platform disallows disabling. |
| `depends_on` | array of text | App keys that must be enabled for this app to be enabled; core dependencies are always treated as enabled. |
| `enabled` | boolean | The current live enablement state. |
| `orphaned` | boolean, optional | Present, and true, only for registry rows with no compiled in manifest; omitted otherwise. |

The apps list envelope is `{apps: [...]}`; it is not the list envelope
of ADR 0001 section 1 (no cursor, no `next_cursor`, no `limit`, no
`include=total`). The enable and disable answers return the same
envelope after the change.

### Money and quantity conventions

Tech admin carries no money and no quantity. The keys list is the
keyset cursor of ADR 0001 section 2, ordered `created_at` then `id`
descending (`api_keys.created_at_id_desc` in
`core/internal/techadmin/handler.go`). The list envelope is the
`{items, next_cursor, limit}` shape with `total` only under
`include=total`. The apps list returns the full catalog.

## Lifecycle and transitions

A machine key has no lifecycle of its own: it is created at mint, lives
until revocation, and is kept after revocation with `revoked_at` set.
Revoking an unknown id is `404 not_found`; revoking an already revoked
key is an idempotent `204 no_content` that writes nothing; a revoke
that flips the row writes its audit row and the `key.revoked` event in
one transaction (`core/internal/techadmin/service.go`).

The AI and routing settings are singleton documents. A save bumps the
revision only when it changes a row, takes `If-Match` or the body
`revision` (`428 precondition_required` without, `409 stale_revision`),
and writes its audit row and the `admin_settings.saved` event in one
transaction. A delete takes the same precondition, answers `204`, and
writes `admin_settings.deleted`. The revision anchor row
(`admin_revisions`) survives the setting's deletion, so the revision
never moves backwards.

An app's enable and disable are idempotent: enabling an already enabled
app succeeds again, disabling an already disabled app succeeds again.
Enabling an app with a disabled non core dependency is `409
app_dependency_conflict` with the blockers; disabling a core app is
`409 app_core`; disabling an app another enabled app depends on is
`409 app_dependency_conflict` with the dependent keys as blockers. The
effect (`enabled` flag in the `apps` row) flips in the database before
the handler answers the catalog; the gating router reads the flag on
the next request and `404`s (or `app_disabled`) when the app is off.

## Events the module writes

The constants are in `core/internal/techadmin/service.go` (key events),
`core/internal/governance/service.go` holds the governance events (see
[governance.md](governance.md)) and `core/pkg/apps/registry.go` writes the
`enabled` flag, not an event. Every mutation writes its event as the last
statement of its transaction ([ADR 0003](../adr/0003-events-outbox.md)),
alongside the `audit_log` row the service writes. The event names the
tech admin writes are `key.created`, `key.revoked`,
`admin_settings.saved` (for both AI and routing settings) and
`admin_settings.deleted` (for both); the staff module writes
`staff.created`, `staff.updated`, `staff.module_granted`,
`staff.module_revoked`, `module.enabled` and `module.disabled` (see the
staff page [staff.md](staff.md) for the staff detail).

The apps module writes no outbox event today; the `enabled` flag is the
only durable change and it lives in the `apps` row. The exposure scan
trigger is an admin nav shortcut for the pricing module's
`HandleAdminScan`, which reads through the pricing service and writes
no event.

## Scopes, roles and keys

A machine key reaching the AI settings routes needs `admin:settings`;
the staff roster and per staff module grant routes need `admin:staff`;
the module flag routes need `admin:modules`; the apps catalog and
toggle routes stay on the plain `apps:read` and `apps:write` scopes
(`core/pkg/middleware/machinekey.go` and ADR 0009; the area scopes are
declared in `adminAreaScopes` and the auth core serves them through
`RequiredScopeForPath`). The key management routes gain no scope
because a machine key is refused there whatever it holds (ADR 0002
section 4); the exposure scan trigger needs `admin:write`
because no finer scope declares it.

The user guard at the serve layer for every tech admin route (key
management, AI and routing settings, the staff roster and the module
flags) is `admin`, `owner` (the exact guard is composed in
`core/internal/app/serve/serve.go` at the `techAdminHandler.RegisterRoutes`
line for techadmin, at the `staffHandler.RegisterRoutes` line for staff).
The apps catalog is reachable by every authenticated role (the SPA
needs it to build its navigation); the apps toggle routes require
`admin` or `owner`. A key without the scope is `403 forbidden`; the
audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2: the scope grammar, the path segment rule; section 4: the user-only routes a machine key is refused on.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0009-finer-admin-scopes.md`](../adr/0009-finer-admin-scopes.md) section 1: the area scope table; section 2: the derivation rule; section 4: exact match, no implication.

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

Then, with an admin or owner role bearer and the seeded branch:

```
curl -X POST http://localhost:8080/api/v1/admin/keys \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Content-Type: application/json' \
  -d '{"name": "integration-partner", "scopes": ["apps:read"]}'
```

The wire tests in `core/internal/techadmin/wire_test.go` pin the
platform and HTTP behaviour; the apps registry tests in
`core/pkg/apps/registry_test.go` pin the toggle rules;
the staff wire tests in `core/internal/staff/wire_test.go` pin the
roster and grant behaviour.