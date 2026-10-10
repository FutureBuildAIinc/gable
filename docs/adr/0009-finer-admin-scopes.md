# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0009: finer admin and users scopes

## Status

Accepted for the Gable v1 refactor (item C5-1a, the admin group of cycle 5's
module conversions). It is the record ADR 0002's second known limit names: it
narrows the `admin` and `users` scopes with the tech admin module's
conversion, as that limit assigns to C5-1. It stands on ADR 0002 (the scope
grammar and the exact match rule) and changes nothing about ADR 0007's confirm
gated verbs: no admin act is a draft, so `propose` and `commit` do not appear
here.

## Context

ADR 0002 derives a key's required scope from the URL alone: the module is the
first path segment under `/api/v1/`, reads need `<module>:read`, writes need
`<module>:write`. Its second known limit records what that costs on the admin
surface: `admin:write` reaches the AI settings, the routing settings, the
staff roster and the module kill switches alike, and `users:write` reaches the
branch grants for human users, wider than "per module" sounds. A service key
that only needs to push load plans to the routing engine, or an onboarding
script that only edits the roster, cannot be granted less than the whole admin
surface.

The admin surface at this record holds four distinct areas behind one module
segment: the settings (`/api/v1/admin/settings/ai` and
`/api/v1/admin/settings/routing`, owned by internal/techadmin), the staff
roster with its per staff module grants (`/api/v1/admin/staff...`, owned by
internal/staff), the global module kill switches (`/api/v1/admin/modules...`,
owned by internal/staff), and the key management routes
(`/api/v1/admin/keys...`, user only, a key is refused there whatever it
holds). The pricing module's exposure scan trigger
(`/api/v1/admin/exposure-scan`) also sits under the segment and is not this
record's to move.

## Decision

### 1. The names

| Scope | Route class it admits |
|---|---|
| `admin:settings` | every method on `/api/v1/admin/settings/ai` and `/api/v1/admin/settings/routing` |
| `admin:staff` | every method on `/api/v1/admin/staff`, `/api/v1/admin/staff/{id}`, `/api/v1/admin/staff/{id}/modules` and `/api/v1/admin/staff/{id}/modules/{module_id}` |
| `admin:modules` | every method on `/api/v1/admin/modules` and `/api/v1/admin/modules/{id}` |
| `users:grants` | every non read method on the users module: `POST /api/v1/users/{sub}/branches`, `DELETE /api/v1/users/{sub}/branches/{branch_id}` and `PUT /api/v1/users/{sub}/home-branch` |

`users:read` keeps its name and its meaning (the users module's reads). The
key management routes gain no scope because no key may reach them (ADR 0002
section 4); the exposure scan trigger keeps `admin:read` and `admin:write`, so
those two coarse scopes stay live for the admin segment, admitted only by
routes no finer area declares.

### 2. The derivation rule

The scope stays derivable from the URL alone, ADR 0002's one rule:

- inside the admin module, the SECOND path segment names the area, and a
  declared area needs its area scope for every method, reads included;
- a module whose only writes are grants (the users module today) names its
  write scope for what it grants;
- everything else keeps ADR 0002's plain rule.

`RequiredScopeForPath(method, path)` in `core/pkg/middleware` implements it;
the auth core serves it in place of the plain `RequiredScope`, and the refusal
audit row records the finer scope the key lacked.

### 3. One scope per area, no verb split inside it

An area scope admits every method on its area's routes. An operator who may
save the AI key may see its hint, and one who may grant a module may read the
roster; a read/write split inside a two route settings surface would double
the vocabulary for no separation an operator actually asks for. The verb split
stays the rule at the module level, where every other module lives.

### 4. Exact match, no implication

A key holding `admin:write` does NOT reach `/api/v1/admin/settings/ai`: the
finer names replace the coarse ones on the routes they cover, they do not
layer over them. ADR 0002 refused implication because a grant must read back
as written; an "admin:write implies admin:settings" rule would be a wildcard
by another name. No stored key changes reach: the table holds no seeded keys,
and no golden mints an admin scope, so the replacement lands on an empty
surface. `users:write` is dead from this record (no route requires it); until
C5-2a's mint validation lands, an operator can still mint it and hold a scope
nothing reads, which is the typo space ADR 0007 section 5.3 closes at the
mint.

### 5. What this record leaves alone

- The mint route's grammar check is C5-2a's (ADR 0007 section 5.3). This
  record exposes `ValidScopeGrammar()` beside the auth core so the mint and
  the auth core cannot disagree: every scope a registered route can require,
  finer names included, from one function.
- The confirm gated verbs (`propose`, `commit`) do not apply: no admin act is
  a draft (ADR 0002's known limit says so, ADR 0007 section 5.5 repeats it).
- Branch bound keys are C5-2a's (ADR 0007 section 5.5).
- The integration seam keeps `X-Integration-Key` (ADR 0007 section 5.6).
- `GET /api/v1/branches/{id}/users` lists the users (subs) holding a branch
  but sits under the `branches` segment, so it stays under `branches:read`
  in v1 and moves under the `users` segment (and `users:read`) in a later
  item.

## Alternatives considered

**Verb split inside each area** (`admin:settings:read`,
`admin:settings:write`). Rejected in section 3: it breaks the
`<module>:<name>` grammar with a third part, and the separation it buys is one
no dealer asks for on a two route surface.

**Implication from the coarse scopes** (`admin:write` also admits the areas).
Rejected in section 4: ADR 0002's exact match rule exists so a grant reads
back as written.

**A second segment table in a config file.** The areas are code constants
held against the route census by a test, the same reasoning as ADR 0002
section 7's vocabulary: a CHECK or config copy would drift from the routes.

## Consequences

- A machine key can be granted exactly the admin surface it needs: settings,
  the roster, or the kill switches, one area at a time, and a branch grant
  key no longer reads the whole users listing unless it also holds
  `users:read`.
- The coarse `admin:read` and `admin:write` still reach the exposure scan
  trigger and any future admin route no area declares, so a new admin route
  arrives with a scope policy either way (declare an area or live on the
  coarse name).
- C5-2a's mint validation reads `ValidScopeGrammar()`; a finer name added
  later changes the middleware map and the grammar follows.
