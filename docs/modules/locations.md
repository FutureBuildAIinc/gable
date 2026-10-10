<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Locations and branches

A location is one node in a yard's tree. A branch is a top level
location (type `branch`, no parent): every other node is a sub
location under a branch, named by a denormalized `branch_id` a
trigger keeps in step. A node's type tells what level it sits at:
`branch`, `zone`, `aisle`, `rack`, `shelf`, `bin` or `yard`. A dealer
keeps one `branch` row per site it runs, then a sub location tree
under each (zones in the warehouse, racks and shelves inside a
zone, bins on a shelf, an outdoor yard for bulky stock beside the
warehouse). The location master is the data the desk's Branch
Switcher reads, the inventory transfer writes and the stock totals
filter on.

This module is the work of C3-1 (item C3-1, the location row onto
the wire contract) and the earlier location work that built the
table, the branch wall and the user branch grants. The Go code is
in `core/internal/location/`. The table is `locations` (the schema
lives across the earlier migrations; the wire conversion is
migration 093 `core/migrations/093_catalog_pricing_wire_contract.sql`,
which fills `created_at`, adds `revision BIGINT NOT NULL DEFAULT 1`
on the five catalog tables including `locations`, and creates the
keyset index `idx_locations_created_at_id`). The trigger that keeps
`branch_id` denormalized is migration 058
`core/migrations/058_locations_branch_denorm_trigger.sql`. The
branch the rest of the system reads is the row migration 059
creates when no branch exists
(`core/migrations/059_default_branch_setting.sql`, a `BRANCH` row
with `code = 'MAIN'` and the description "Default branch created by
migration 059"). The wire contract is in
`core/api/fragments/location.yaml`.

## What it does in a yard

A dealer runs one or more branches. A branch is a site the dealer
runs: a main yard, a satellite, a showroom. Every physical node a yard operator
points at (a yard, a warehouse zone, an aisle, a rack, a shelf, a
bin) is a row under the branch. A yard's inventory module stores
each stocked unit on a row's `bin` (the level at the bottom of the
tree); a transfer moves stock between two locations of the same branch; a
move whose endpoints sit in different branches is refused
(`inventory/service.go` `MoveStock`). Stock is conventionally held
on a `bin`, but the move does not check the type.

The user branch grants (`user_locations`) say which branches a
person may target. A yard operator holds grants on every branch in
their yard; a regional manager holds grants on every branch in
their region; an outside sales rep holds a grant on the single
branch they cover. The branch middleware settles a request's branch
context from the `X-Branch-Id` header, the JWT and the grants, but
only while the setting `multi_branch_enabled` in `system_settings` is
`true`. Migration 059 installs it as `false`: until an operator sets
it, every caller except a branch bound key is treated as an
administrator, no `X-Branch-Id` is checked, and none of the wall
statements below apply to them (a change takes
up to 30 seconds to reach a running server, the middleware's
`killSwitchTTL`). With it on, the location handler holds each by id
read, the list and each create to that context, so a caller granted
on branch A cannot read a location or tree of branch B, or create a
sub location under it. `PUT` and `DELETE /api/v1/locations/{id}` and
the branch writes are not branch held; they are admin or owner
routes.

A BRANCH is its own administrative entity: created with no parent
and a required `name`, addressed through the `/api/v1/branches`
routes, and behind a narrower role guard than the sub location create
(the writes are admin or owner only). The location handler refuses to create
a `branch` through the location route except when the caller is an
admin or owner; a machine key has no claims and is refused, even
when it would otherwise pass (the rule keeps a key from minting a
branch through the wider route, `callerMayCreateBranch` in the
handler).

The `locations` table is one row per node, the column `type` says
what level the row sits at, `path` carries the row's display path,
`parent_id` names the parent (null on a `branch`), and `branch_id`
is kept denormalized by a database trigger. A `branch` self-
references (`branch_id = id`); any other row takes its parent's
`branch_id`. The denormalization is what makes the wall cheap: a
scoped read is one column check, no recursion.

## Routes

Every route below is in `core/api/fragments/location.yaml`, the
registered handles are in `core/internal/location/handler.go`, and
the route census (`core/api/ROUTES.txt`) lists each one under
`internal/location`.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/locations` | Cursor list of locations, newest first; behind the branch wall (a bound caller lists its branches' rows); `include=total` adds a total; archived rows (`active=false`) are included, so filter on `active` client side. |
| POST | `/api/v1/locations` | Create a sub location (zone, aisle, rack, shelf, bin, yard) under a parent; the create is held to the caller's branches and a body `parent_id` to the payload branch rule; `type: branch` is a 403 unless the caller is an admin or owner; `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/locations/{id}` | One location with its revision; behind the branch wall (a location of a branch the caller may not target is a 403). |
| PUT | `/api/v1/locations/{id}` | Replace the mutable slice; type and parent are fixed at create, so a body that names them is a 400; `If-Match` (strong or weak) or body `revision` precondition; `Idempotency-Key` rides the standard header. |
| DELETE | `/api/v1/locations/{id}` | Archive (`active=false`) at the revision; `If-Match` or body `revision`; answers 204, and 204 again on an already archived row. |
| GET | `/api/v1/branches` | Cursor list of branches, active rows by default; `include_inactive=true` adds inactive rows; unwalled reference data (PR 39 decision, the C3-1 row for `GET /api/v1/branches/{id}`). |
| POST | `/api/v1/branches` | Create a branch (no parent, `name` required, `type` forced to `branch`); `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/branches/{id}` | One branch (a non branch row is a 404); unwalled reference data. |
| PUT | `/api/v1/branches/{id}` | Replace the mutable slice; `type` and `parent_id` are a 400; the revision precondition applies. |
| DELETE | `/api/v1/branches/{id}` | Archive the branch (active=false) at the revision; a non branch id is a 404; answers 204. |
| GET | `/api/v1/branches/{id}/tree` | The branch row plus every descendant, ordered by path; behind the branch wall (a tree of a branch the caller may not target is a 403); one page; archived descendants are included. |
| GET | `/api/v1/branches/{id}/users` | The users granted to a branch; admin or owner. |
| GET | `/api/v1/me/branches` | The caller's grants with the home flag (a bare array, `[]` when none; inactive branches are left out; no paging); the role guard below, and user only for keys. In dev mode with no claims it returns every active branch with the first marked home. |
| GET | `/api/v1/users` | The union of distinct user subjects from `user_locations` and `audit_log` (feeds the admin user picker); admin or owner. |
| GET | `/api/v1/users/{sub}/branches` | One user's grants; admin or owner. |
| POST | `/api/v1/users/{sub}/branches` | Grant a branch to a user (sets `is_home`; an existing grant is updated, and `is_home` true clears the user's other home flag); admin or owner; answers 204; `Idempotency-Key` rides the standard header. |
| DELETE | `/api/v1/users/{sub}/branches/{branch_id}` | Revoke a user's grant; admin or owner; answers 204; a missing grant is a 404. |
| PUT | `/api/v1/users/{sub}/home-branch` | Set a user's home branch (must already hold a grant); admin or owner; answers 204; `Idempotency-Key` rides the standard header. |

The four routes a body or path id with parameters creates, updates,
deletes or reads against are behind the branch middleware and the
branch guard (`WithBranchWall` in the handler, `branchWall.locations`
in `core/internal/app/serve/wire_branch_wall.go`): `POST
/api/v1/locations`, `GET /api/v1/locations/{id}`, `GET
/api/v1/locations` (the list is filtered to the caller's
branches), and `GET /api/v1/branches/{id}/tree`. The list is filtered
through the branch scope `listScope` resolves (`handler.listScope`):
with a context branch that branch's rows, with no context branch a
bound non admin user's granted branches (none granted, none listed);
an administrator without a header, an unbound key and the single
branch switch see every location. With `default_branch_required` at
its installed `true`, a non admin request with no `X-Branch-Id` is a
400, so the granted branches scope applies only when that setting is
`false`. `GET /api/v1/branches` and `GET
/api/v1/branches/{id}` are unwalled reference data, so an
administrator working in one branch reads another branch's record
(the C3-1 row for `GET /api/v1/branches/{id}`, PR 39 decision; the
desk's Branch Users page does).

The grant routes at the foot (`/users/...`) and the branch's users
list (`/branches/{id}/users`) are admin or owner only, behind the
`adminGuards` the serve wiring passes
(`middleware.RequireRole("admin", "owner")` in `serve.go`).
`/me/branches` takes the role guard below, and is also in the user
only prefix list at `pkg/middleware/machinekey.go`
(`machineKeyUserOnlyRoutes`, the "a machine key has no user"
refusal).

## The location resource

`Location` (see `core/api/fragments/location.yaml`
`components.schemas.Location`, `core/internal/location/model.go`
`Location`). The resource is one shape for every level (branch,
zone, aisle, rack, shelf, bin, yard); the branch only metadata is
present as `null` on a sub location row.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The location id. |
| `parent_id` | UUID, nullable | The parent location; null on a `branch`. |
| `path` | text | The display path; on a branch row the service defaults it to `name` when the body omitted it (`CreateLocation`, the branch branch). |
| `type` | lowercase enum | `branch`, `zone`, `aisle`, `rack`, `shelf`, `bin`, `yard` (stored UPPERCASE, lowercase on the wire; ADR 0001 section 6). |
| `code` | text | Required; one short code the operator reads. |
| `description` | text, nullable | Free text description. |
| `name` | text, nullable | Required on a branch (the only branch only field); a non branch row leaves it null. |
| `address`, `city`, `state`, `zip`, `phone` | text, nullable | Branch only; a non branch row leaves them null. |
| `tax_jurisdiction_code` | text, nullable | Branch only; the tax authority the branch's sales tax posts to. |
| `default_tax_rate` | number, nullable | Branch only; the rate the desk and POS fall back to when no tax provider is configured (serve logs `AVALARA_ACCOUNT_ID not set` when the field is in use). |
| `timezone` | text, nullable | Branch only; the branch's IANA timezone. |
| `active` | boolean | `false` after a DELETE (the location is archived, never removed). |
| `branch_id` | UUID, nullable | Kept denormalized by trigger 058: a `branch` self references (`branch_id = id`), every other row takes its parent's `branch_id`. |
| `revision` | integer | Starts at 1; returned as `ETag`; the precondition on every PUT and DELETE. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC, microsecond precision. |

`LocationCreateRequest` (`components.schemas.LocationCreateRequest`,
`createLocationRequest` in the handler): the body of POST. `path`,
`type`, and `code` are required by the schema (the server enforces
`code` and `type`; send `path`); `parent_id`, `description`, and
the branch only fields are nullable. The active flag is told apart
from an explicit false by being a `*bool` (`null` when absent, a
JSON `false` when set); absent means `true`. `type` is parsed
strictly (a lowercase name; the legacy UPPERCASE is a 400). A branch
creation overrides `type` to `branch` and clears `parent_id` (the
branch route does this; the location route's payload rule applies
to `parent_id`).

`LocationUpdate` (`components.schemas.LocationUpdate`,
`locationUpdateRequest` in the handler): the mutable slice of a
location, in the same wire shape as the create. `type` and
`parent_id` are fixed at create, so a body that names either is a
400 (the same body `revision` carries the precondition); an
undeclared field is a 400; an omitted `active` keeps the stored
value and an empty `code` keeps the stored code, but a PUT clears
every nullable field the body leaves out.

`BranchSummary` (`components.schemas.BranchSummary`): the lightweight
projection `/me/branches` and `/users/{sub}/branches` return.
`id`, `code`, `name`, `active` are required; `is_home` is populated
on `/me/branches` and `/users/{sub}/branches` (true for the home
grant); `timezone` is populated when the branch carries one. `is_home`
and `timezone` are omitted, not null, when false or empty.

`UserLocation` (`components.schemas.UserLocation`, `model.UserLocation`):
a grant row. `user_sub`, `branch_id`, `is_home`, `granted_at` are
required; `granted_by` is the actor the grant recorded (the JWT
subject, omitted when empty); `granted_at` is RFC 3339 with
nanoseconds, not the microsecond timestamp of `Location`. The grant
lists (`/me/branches`, `/users`, `/users/{sub}/branches`,
`/branches/{id}/users`) are bare JSON arrays, `[]` when empty, with
no envelope and no paging.

The `LocationPage` envelope (`components.schemas.LocationPage`,
`items` array of `Location`, `next_cursor` string or null, `limit`
integer, optional `total` integer under `include=total`) is shared
with `/api/v1/branches` and `/api/v1/branches/{id}/tree` (the tree
is one page; `next_cursor` null).

## Lifecycle and transitions

The location module is the directory and the branch wall, not a
state machine. The flag the wire cares about is `active`: a row is
active on create (`active=true` by default; an explicit false is
honored, so an importer can carry a decommissioned yard through),
stays active until a writer (admin or owner) flips it through
`PUT /api/v1/locations/{id}` (omitted keeps the stored value), and
lands `active=false` after a DELETE (the location is archived, not
removed; `location.archived` is the event the delete writes).

There is no status enum and no state machine. A PUT replaces the
mutable slice at the revision (a stale revision is a 409
`stale_revision`, neither header nor body is a 428
`precondition_required`); a DELETE archives at the revision and
answers 204. A PUT or DELETE whose path id is not a branch on the
branch routes is a 404 `not_found` (the `requireBranch` helper
answers for `UpdateBranch` and `DeleteBranch`; `GetBranch` checks
the row's type itself).

The user branch grants carry their own lifecycle: a grant is one
row in `user_locations` (a user sub, a branch id, a home flag,
`granted_at`, an optional `granted_by`). A grant may be added
(`POST /api/v1/users/{sub}/branches`, the `branch_id` and
`is_home` in the body, with the target verified to be a branch),
removed (`DELETE /api/v1/users/{sub}/branches/{branch_id}`), and
its home flag flipped (`PUT /api/v1/users/{sub}/home-branch`, the
target must already be a grant; a missing grant is a 404
`NOT_FOUND` in the legacy error envelope, as these grant routes are
not on the wire error contract yet).
There is no event the grant writes: the grants are directory data
the branch middleware reads on every request, and a refresh of
that data on the next call is the contract.

## Events the module writes

Every mutation writes its outbox event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)), with an
`audit_log` row in the same transaction the event's record writes
under:

| Event | Constant | Written at |
|---|---|---|
| `location.created` | `EventLocationCreated` (`core/internal/location/service.go:40`) | `core/internal/location/service.go:154` (the `record` call inside `CreateLocation`'s transaction) |
| `location.updated` | `EventLocationUpdated` (`core/internal/location/service.go:41`) | `core/internal/location/service.go:188` (the `record` call inside `UpdateLocation`'s transaction) |
| `location.archived` | `EventLocationArchived` (`core/internal/location/service.go:42`) | `core/internal/location/service.go:205` (the `record` call inside `DeleteLocation`'s transaction, with `old.Active = false`) |

The event's data is `{code, type, revision}` with `type` lowercase
(`service.record`); the audit entry's `changes` map is
`{code, type, revision, active}` (`service.go`). A failed audit write
fails the mutation (`WithAudit`, the `AuditLogger` interface). The
three event constants also serve as the audit `Action` strings.

The C3-1 events feed row of `CONTRACT-CHANGES.md`
adds `location.created`, `location.updated` and `location.archived`
to the `events` feed. The user branch grant routes do not write
their own events: the grants are directory data the middleware
reads on the next request.

## How the branch wall applies

The branch wall on this module follows the recipe: every route that
reads a single location by path id, that creates a sub location, or
that lists locations or a branch's tree runs behind the branch
middleware. `GET /api/v1/locations/{id}` and the create's body
`parent_id` go through `CheckPayloadLocation` (the location's
`branch_id`, or itself when it is a branch, must be within the
caller's branches); `GET /api/v1/branches/{id}/tree` calls
`CheckPayloadBranch` on the path id itself. The unwalled reads are
the directory reads: `GET /api/v1/branches` and `GET
/api/v1/branches/{id}` (the C3-1 row for `GET /api/v1/branches/{id}`,
PR 39 decision); the SEC-branch rows of CONTRACT-CHANGES record the
by id and tree wall.

The wall is live only when `multi_branch_enabled` is `true`
(migration 059 installs `false`). With it on and
`default_branch_required` left at its installed `true`, a non admin
request with no `X-Branch-Id` is a 400; the granted branches list
scope applies only when `default_branch_required` is `false`. An
administrator who sends `X-Branch-Id` is held to that branch (the
SEC-branch row of CONTRACT-CHANGES).

The serve wiring is `branchWall.locations` in
`core/internal/app/serve/wire_branch_wall.go:60`, which calls
`location.Handler.WithBranchWall(w.guard, w.mw)` with the same
`BranchGuard` and `BranchMiddleware` every other module uses, then
`RegisterRoutes(mux, middleware.RequireRole("admin", "owner",
"warehouse", "sales"))` for the reads (the role guard). The admin
guards are passed separately in `serve.go`
(`middleware.RequireRole("admin", "owner")` for the writes). The
handler comments (`handler.go` `WithBranchWall` and the comments
on `CreateLocation`, `GetLocation`, and `GetBranchTree`) and the
`wire_branch_wall_test.go` driving tests hold the wiring to the
test: `TestBranchWall_ServeWiring` (the list and the location
create block), `TestBranchWall_PathIDRecords` (the by id read and
the tree refusals) and `TestBranchWall_SwitchOffAdmitsBoundCaller`
(the switch off behaviour).

The payload branch rule (`middleware.BranchGuard.CheckPayloadLocation`
and `CheckPayloadBranch`, ADR 0007 section 2.3): a location named
in a request body stands for a branch through its `branch_id` (or
itself when it is a `branch`); a location whose `branch_id` is not
within the caller's branches is a 403 with the verdict
`ErrPayloadBranchRefused`. A location that does not exist or has
no `branch_id` passes the guard (`CheckPayloadLocation`, the no
rows branch) and the service answers for it. `CheckPayloadBranch`
(`branch_payload.go`) has three cases: with a context branch set,
the branch must equal it; with no context branch, an administrator
or a request with no user may name any branch; otherwise the branch
must be among the user's grants.

## Scopes, roles and keys

A machine key reaching the location and branch routes needs the
scope of the first path segment under `/api/v1/`: `locations:read`
or `locations:write` for `/api/v1/locations...`; `branches:read`
or `branches:write` for `/api/v1/branches...`;
`users:read` for `GET /api/v1/users` and `GET
/api/v1/users/{sub}/branches`. ADR 0009 overrides the `users` write
scope to `users:grants`
(`writeScopeOverrides["users"]`, the "only writes are grant,
revoke and home branch" rule), so `POST`, `DELETE`, and `PUT`
under `/api/v1/users/{sub}/branches...` and `PUT
/api/v1/users/{sub}/home-branch` need `users:grants`. The scope
vocabulary is `pkg/middleware/machinekey.go` `machineKeyModules`
(the `branches`, `locations`, and `users` entries). The
`/api/v1/branches/{id}/users` route takes the module's read scope
(`branches:read`) because the path's first segment is `branches`: a
machine key is not subject to the role guard, so a key holding
`branches:read` reaches that list, which a user reaches only as
admin or owner, and a key holding `users:read` reaches `GET
/api/v1/users`.

Roles come from two guards. The role guard `admin`, `owner`,
`warehouse`, `sales` (`branchWall.locations`) covers `GET` and
`POST /api/v1/locations`, `GET /api/v1/locations/{id}`, `GET
/api/v1/branches`, `GET /api/v1/branches/{id}`, `GET
/api/v1/branches/{id}/tree` and `GET /api/v1/me/branches`; so a
warehouse or sales user can create bins, shelves and the other sub
locations, and a user with another role (finance, cashier,
purchasing) cannot read `/me/branches`. The admin guard `admin`,
`owner` (`serve.go`, `adminGuards`) covers `PUT` and `DELETE
/api/v1/locations/{id}`, `POST`, `PUT` and `DELETE
/api/v1/branches...`, `GET /api/v1/branches/{id}/users`, `GET
/api/v1/users`, and the grant routes. `POST /api/v1/locations` with
`type: branch` additionally needs `admin` or `owner`
(`callerMayCreateBranch`). A machine key is not subject to the role
guard; its scope is the gate (ADR 0002 section 4). A key without
the scope is 403 `forbidden`; the audit row carries the refused
scope (ADR 0002 section 5).

`/api/v1/me/branches` is in the user only prefix list
(`machineKeyUserOnlyRoutes` at `pkg/middleware/machinekey.go:283`,
`/api/v1/me`, the "a machine key has no user" refusal), so a
machine key never reaches the caller's grants even when the key
holds every scope. The `users` write scope is `users:grants`; a key
with only `users:read` cannot grant, revoke, or set home.

A machine key may be branch bound (`branch_id` at mint, `POST
/api/v1/admin/keys`; ADR 0007 section 5.5): the key is pinned to its
branch whether or not `multi_branch_enabled` is on (the pin is set
before the switch is read, `BranchMiddleware` in
`core/pkg/middleware/branch.go`). Its lists, by id reads and tree
see that branch only, a request naming another branch in
`X-Branch-Id` is a 403 `forbidden` audited as `key.branch_refused`,
and a body `parent_id` or path id of another branch is a 403. The
routes that mount no branch middleware are held to the pin by the
key branch wall (`core/pkg/middleware/keybranch.go`, mounted through
`WithKeyBranchWall`): a bound key writing another branch's location
(`PUT` and `DELETE /api/v1/locations/{id}`), another branch itself
(`PUT` and `DELETE /api/v1/branches/{id}`) or another branch in a
grant body or path is a 403 `forbidden` naming the field, audited as
`key.branch_refused`; its own branch passes; the directory create
(`POST /api/v1/branches`) refuses a bound key outright;
`GET /api/v1/branches/{id}/users` refuses it another branch and
`GET /api/v1/users/{sub}/branches` answers only its branch for it.
The branch directory reads (`GET /api/v1/branches`, `GET
/api/v1/branches/{id}`) stay reference data, unwalled by the PR 39
decision, a stated limit. An unbound key behaves as before.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 5, 6, 9, 11, 12: section 1 (the list envelope, `cursor`, `limit`, `include=total`), section 2 (the keyset position), section 5 (strict query parameters; `include_inactive` on `/branches` is the boolean the section requires), section 6 (enums lowercase on the wire; `location.type` is one such enum, with the legacy UPPERCASE a 400), section 9 (idempotency keys on the creates), section 11 (revision and `If-Match`, the in place rule on the updates and the deletes), and section 12 (timestamps RFC 3339 UTC, every optional field present as null).
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2 (the segment scope rule: `<module>:read` and `<module>:write`, so `locations:read` / `locations:write`, `branches:read` / `branches:write`, `users:read`), section 3 (a key as a principal), section 4 (roles), section 5 (refusals and the audit row), section 6 (branch scoping; the branch bound key of ADR 0007 section 5.5 now supersedes its first known limit).
- [`docs/adr/0009-finer-admin-scopes.md`](../adr/0009-finer-admin-scopes.md): the `users` write scope narrowed to `users:grants`.
- [`docs/adr/0007-drafts-links-and-confirm-gated-scopes.md`](../adr/0007-drafts-links-and-confirm-gated-scopes.md) section 2.3: the payload branch rule, and section 5.5: the branch bound key (`CheckPayloadBranch`, `CheckPayloadLocation`, the verdict `ErrPayloadBranchRefused`). The location handler holds every body `parent_id` and every path id with parameters this ADR's rule.

## How to try it locally

The repository's own seed and the local make targets are the only
way to exercise the module end to end. `make up` builds and starts
the local stack (Postgres, migrate and seed, `core serve`,
`core worker`, the web front door) on http://127.0.0.1:8080 with
`AUTH_MODE=dev`; `make down` removes it. To run the core from
source instead: `make db`, `make migrate`, `DEMO_SEED=1 make seed`,
then `cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up`
and `make db` workflows use different compose projects and volumes,
so the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Migration 059 creates a default branch (`code = 'MAIN'`, name
`Main Branch`) when none exists and writes its id under
`system_settings.default_branch_id`. The demo seed then deletes that
`MAIN` row and loads its own branches (`KEL-MAIN`, `WK`, `LK`) with a
few zones under them, so after `make up` or `DEMO_SEED=1 make seed`
take a branch id from `GET /api/v1/branches`. Migration 059 also
writes `multi_branch_enabled = 'false'` and `default_branch_required
= 'true'`; to see the wall, set `multi_branch_enabled` to `true` in
`system_settings` on the throwaway database and wait up to 30
seconds. Migration 093 fills
`locations.created_at` (backfilling from `updated_at` where the
column was null), adds `revision BIGINT NOT NULL DEFAULT 1`, and
creates the keyset index `idx_locations_created_at_id`. The seed
truncates the transactional tables (orders, invoices, quotes,
deliveries, payments, purchase orders, the ledger, POS, projects,
CRM activities, contacts, rebates, saved reports, EDI partners and drafts, and the
locations tree when it finds duplicate branches), so run it only
against a throwaway database. In `AUTH_MODE=dev` there are no
claims, so the per user grants never apply; only an `X-Branch-Id`
context narrows a list once the switch is on. To see the grants,
run against a real token issuer.
Then, with an admin role bearer and the seeded branch:

```
curl -X GET 'http://localhost:8080/api/v1/locations' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

To list the branches (unwalled reference data; no `X-Branch-Id`
required):

```
curl -X GET 'http://localhost:8080/api/v1/branches' \
  -H 'Authorization: Bearer <token>'
```

To create a sub location (a `bin` under a zone of a seeded branch;
the seed has no yard or bin rows), with an `Idempotency-Key` for the
create:

```
curl -X POST http://localhost:8080/api/v1/locations \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @location-create.json
```

with body `{"parent_id": "<zone uuid>", "type": "bin", "code":
"BIN-01", "path": "KEL-A/BIN-01", "active": true}` (the seeded zone
`KEL-A` sits under `KEL-MAIN`). The new
row's `Location: /api/v1/locations/{id}` and the ETag header return
the freshly created row.

To grant the seeded branch to a user (admin or owner; answers 204):

```
curl -X POST 'http://localhost:8080/api/v1/users/<sub>/branches' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d '{"branch_id": "<seeded branch uuid>", "is_home": true}'
```

The branch wall, the payload branch rule, and the writes' revision
precondition are tested in
`core/internal/app/serve/wire_branch_wall_test.go`
(`TestBranchWall_ServeWiring` and the location create block,
`TestBranchWall_PathIDRecords`,
`TestBranchWall_SwitchOffAdmitsBoundCaller`).
The create, update and archive transactions are tested in
`core/internal/location/wire_test.go` (`TestBranchListEnvelopeAndRevision`,
`TestLocationsListAndTree`, `TestLocationCreateAndValidation`,
`TestLocationRevisionAndArchive`, `TestLocationListCursorWalk`)
and the event and audit writer in
`core/internal/location/audit_events_test.go`
(`TestLocationWrites_RecordAuditAndEvent`,
`TestLocationWrites_FailedAuditRollsTheWriteBack`,
`TestLocationWrites_FailedEventRollsThemBack`,
`TestLocationWrites_Pool4ThreeContenders`). The hierarchy rules
(branches are root level, sub locations need a parent, `name` is
required on a branch) are tested in
`core/internal/location/service_test.go`
(`TestCreateLocation_HierarchyRules`,
`TestCreateBranch_ForcesTypeAndClearsParent`,
`TestGetBranch_NonBranchIs404`,
`TestCreateBranch_ActiveDefaultAndExplicitFalse`). The
characterization goldens at
`core/internal/characterization/testdata/goldens/location.json` and
`.../location_item.json` pin the wire shapes for the branch and
location lists, the branch and location creates, the update (with
and without a revision, and a stale revision), the invalid and
unknown field creates, the bad cursor and unsupported parameter
cases, the branch get of a non branch, `me.branches`, the
`users.list_empty`, `users.grant`, `users.branches`,
`users.home_branch`, `users.list_after_grant`,
`users.grant.not_a_branch`, `users.home_branch.no_grant`,
`branch.users`, `users.revoke`, the delete (with and without a
revision), and `branch.list_after_delete`.