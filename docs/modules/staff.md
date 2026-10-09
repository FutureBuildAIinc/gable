<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Staff roster and module grants

The staff roster is the dealer roster the integrations surface
authenticates against: each member is an email, a name, a staff number,
a free-text role, an active flag, the module ids granted to them and
the row's revision. The roster is the write side of two tables: the
`staff` table the `/api/integration/validate-staff` endpoint reads, and
the `module_grants` table that endpoint gates on. The integration
surface derives entitlement as `staff.active AND a module_grants row
(staff_id, 'ai_lm') AND system_settings['modules.ai_lm.enabled'] ==
'true'`.

The module flags are the kill switches under `/api/v1/admin/modules`:
each row in `system_settings` named `modules.<id>.enabled` is toggled
by the admin and gates the integrations surface for every staff
member at once. The staff module owns the write side of those flags
and their revision anchors in `admin_revisions`.

The Go code is in `core/internal/staff/`. The migration that brought
the tables onto the contract is
`core/migrations/080_ailm_integration_contract.sql` (the staff and
module_grants tables) and `core/migrations/095_admin_wire_contract.sql`
(the staff revision column and the admin_revisions anchors).

## What it does in a yard

The dispatcher, the yard operator and the office staff each have a
row. A row's `active` flag is flipped when someone leaves or goes on
leave; a row's `modules` list is the per-staff set of integration
modules that person is allowed to use (today, `ai_lm`). The
`modules.ai_lm.enabled` global flag is the kill switch the operator
pulls when the AI goes wrong; turning it off revokes `ai_lm` for every
staff member without deleting any grant, so turning it back on
restores the roster.

## Routes

Every route is in `core/api/fragments/admin.yaml` and the registered
handles are in `core/internal/staff/handler.go` and
`core/internal/staff/routes.go`. The route census
(`core/api/ROUTES.txt`) lists each one under the `internal/staff`
package (the route census has a `package` column, not a module
column).

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/admin/staff` | Cursor list of the staff roster, newest first; active filter. |
| POST | `/api/v1/admin/staff` | Create a staff member. |
| GET | `/api/v1/admin/staff/{id}` | Read one staff member. |
| PUT | `/api/v1/admin/staff/{id}` | Update a staff member on the client's revision. |
| POST | `/api/v1/admin/staff/{id}/modules` | Grant a module to a staff member. |
| DELETE | `/api/v1/admin/staff/{id}/modules/{module_id}` | Revoke a module from a staff member. |
| GET | `/api/v1/admin/modules` | List the integration module catalog with each flag's live state. |
| PUT | `/api/v1/admin/modules/{id}` | Toggle a module's global enable flag. |

The key management, AI and routing settings routes
(`/api/v1/admin/keys/...`, `/api/v1/admin/settings/...`) sit under the
admin segment and share the guard, but they belong to the tech admin
module's page (see [tech-admin.md](tech-admin.md)). The exposure scan
trigger at `/api/v1/admin/exposure-scan` belongs to the pricing
module.

## The main resources

The staff module carries two documents: the roster member and the
module catalog entry. The schemas are in
`core/api/fragments/admin.yaml`.

`StaffMember` (`components.schemas.StaffMember`) is the row the staff
list returns and the row embedded in a create response, get, update or
grant/revoke answer.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The staff row id. |
| `email` | text | The unique email; the integrations surface authenticates against it. |
| `full_name` | text | The full name. |
| `staff_no` | text, nullable | The dealer's staff number; unique when set. |
| `role` | text | A free text label (e.g. `dispatcher`, `admin`); not an authorisation claim. |
| `active` | boolean | The active flag the validate-staff endpoint reads. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |
| `modules` | array of text | The raw grant set; never null and not filtered by the global enable flag. |

The two "modules" fields are intentionally asymmetric: `modules` here
is the raw grant set so an admin checkbox shows what was granted even
while the module is globally off; the integrations surface reports
`granted AND globally-enabled`, because that is what the caller is
actually allowed to use right now. `StaffModule` is the per flag row
the modules list returns (see the tech admin page
[tech-admin.md](tech-admin.md) for its field set: `id`, `name`,
`enabled`, `revision`).

`AdminCreateStaffRequest` carries `email` (required), `full_name`
(required), `staff_no` (optional, null by default), `role` (defaults
to `staff`), `active` (defaults to true). A duplicate email or staff
number is a `409 conflict` naming the field. `AdminUpdateStaffRequest`
makes every editable field a pointer: nil leaves it alone, so a `PUT`
that only flips `active` must not blank out the email it did not
send; a `staff_no` of null clears the number. `AdminGrantModuleRequest`
carries `module_id` (required; must name a module in the catalog,
`ai_lm` today) and `revision` (optional body precondition).

### Money and quantity conventions

Staff carries no money and no quantity. The list cursor is the
keyset cursor of ADR 0001 section 2, ordered `created_at` then `id`
descending (`staff.created_at_id_desc` in
`core/internal/staff/handler.go`). The list envelope is the
`{items, next_cursor, limit}` shape with `total` only under
`include=total`. The `active` filter accepts `true` or `false`; any
other value is a `400 validation_failed` naming `active`.

## Lifecycle and transitions

A staff member has no lifecycle of its own. The `revision` field
tracks every write; the member's `active` flag and grants are
versioned through the same anchor. A grant adds one module id to the
member's `modules` set and moves the revision; an idempotent re-grant
of an already granted module id changes nothing and writes nothing
(`core/internal/staff/service.go`, the grant branch). A revoke
removes one module id and moves the revision only when the grant was
actually removed. An unknown `module_id` on a grant is a `400
validation_failed` naming the field.

The module flag is a kill switch. A toggle bumps the revision only
when it changes the flag, takes `If-Match` or the body `revision`
against the flag's own anchor (`admin.modules.<id>` in
`admin_revisions`), and writes its audit row and the
`module.flag_changed` event in one transaction. Turning a module off
revokes it for every staff member at once WITHOUT deleting any grant;
turning it back on restores the roster.

## Events the module writes

The constants are in `core/internal/staff/service.go` (the audit and
outbox writes). Every mutation writes its event as the last
statement of its transaction
([ADR 0003](../adr/0003-events-outbox.md)), alongside the `audit_log`
row the service writes. The event names the staff module writes are
`staff.created`, `staff.updated`, `staff.module_granted`,
`staff.module_revoked` and `module.flag_changed`.

## Scopes, roles and keys

A machine key reaching the staff roster and per staff module grant
routes needs `admin:staff`; the module flag routes need
`admin:modules` (ADR 0009; the area scopes are declared in
`adminAreaScopes` in `core/pkg/middleware/machinekey.go` and the auth
core serves them through `RequiredScopeForPath`). The user guard at
the serve layer for every staff route (roster, grants, module flags)
is `admin`, `owner` (the exact guard is composed in
`core/internal/app/serve/serve.go` at the `staffHandler.RegisterRoutes`
line; the `RegisterRoutes` body of `core/internal/staff/routes.go`
applies the same guard to every route it mounts, and a caller that
passes no guard is a privilege escalation path the package comment
calls out). A key without the scope is `403 forbidden`; the audit row
carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0009-finer-admin-scopes.md`](../adr/0009-finer-admin-scopes.md) section 1: the area scope table; section 2: the derivation rule.

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
curl -X POST http://localhost:8080/api/v1/admin/staff \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Content-Type: application/json' \
  -d '{
        "email": "newcomer@example.com",
        "full_name": "New Comer",
        "role": "dispatcher"
      }'
```

The wire tests in `core/internal/staff/wire_test.go` pin the platform
and HTTP behaviour; the transaction proofs in
`core/internal/staff/tx_test.go` pin the grant and revoke rules and
the kill switch invariant.