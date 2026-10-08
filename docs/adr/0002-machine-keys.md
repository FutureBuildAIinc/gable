# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0002: scoped machine keys

## Status

Accepted for the Gable v1 refactor (item R1-13, security class). It stands on
ADR 0001's error envelope and code table, and on the actor kinds item R1-14
added to the audit log.

## Context

The refactor inputs list coarse auth as a live failure: one shared
`X-Integration-Key` guarded every machine call, while the `api_keys` table
(migration 014, holding a name, an Argon2id hash, a prefix, a scope array and
revocation) sat unused. `GenerateKey` existed, `ListKeys` and `RevokeKey`
existed, `ValidateKey` was defined and never called: a key minted by an
administrator authenticated nothing.

This item makes the key a principal. The decisions below bind the auth layer,
the role guards, the audit trail, and the integration seam.

## Decision

### 1. Credential shape and dispatch

`GenerateKey` mints `sk_live_` followed by base64url; a JWT is three dot
separated segments. The prefix therefore separates the two Bearer credential
kinds exactly, and the auth middleware dispatches on it before the JWT parser
sees the token:

- a JWT goes down the JWT path unchanged, JWKS validation and all;
- a machine-key-shaped token goes to the machine-key core, which calls
  `ValidateKey` (prefix lookup, Argon2id compare in constant time, revocation
  filter, `last_used_at` touch);
- with no core wired, a machine-key Bearer is refused 401 `unauthorized` fail
  closed; it never falls into JWT parsing, whose error would mislead.

An unknown, revoked or malformed key is 401 `unauthorized` (one verdict:
revealing which of the three applies buys an attacker information and buys
the operator nothing). The key store being unreachable is not a credential
verdict: it answers 503 `unavailable`. Every answer is in ADR 0001's error
envelope.

### 2. Scopes: module, read, write

A key's module is the first path segment under `/api/v1/`, verbatim: the
route `/api/v1/quotes/{id}` belongs to the module `quotes`. The scope a call
needs is therefore derivable from the URL alone, no lookup table: GET and
HEAD need `<module>:read`, every other method needs `<module>:write`.

The vocabulary of modules mirrors the route census (`backend/api/ROUTES.txt`)
and a test fails when they disagree in either direction: a route added under
a segment the vocabulary does not declare has no scope policy and cannot
merge, and a vocabulary entry no route sits under is a dead name. The test,
not review diligence, is what keeps "no `/api/v1` route left without a
module" true over time.

Scope matching is exact. There are no wildcard forms (`*`, `quotes:*`): no
existing data needs them (the table is empty at the base, nothing seeds
scopes, and `ValidateKey` was never called, so no key ever worked under any
interpretation), and an exact vocabulary keeps every granted scope auditable
as written. Should a migration ever arrive with wildcard-carrying keys, that
is a listed contract change decided then.

### 3. Where a key is a principal

A machine key is a principal on declared `/api/v1` module routes only.

- The public seams keep their own authentication and never consult a Bearer
  machine key: `/api/integration/*` stays `X-Integration-Key` only (its
  published contract and its goldens untouched, per ADR 0001's in place rule;
  a machine-key-only request there is refused by the seam's own missing-key
  401), `/api/v1/a2a/*` stays JWS, the portal stays on its session JWT.
- A machine key that reaches the auth layer on any other path (a non-public
  path outside `/api/v1`, or a segment under `/api/v1` the vocabulary
  declares neither module nor exclusion) is refused 403 `forbidden` fail
  closed. Refusing, rather than ignoring, keeps a caller from believing a
  wrong-header request succeeded on its merits.

`AUTH_MODE=dev` mounts the machine-key core standalone at the same position
the JWT middleware occupies in production, over the same public path list, so
a key draws the same verdicts in dev as in production. Callers without a
machine key keep the dev behaviour they had: no JWKS, no authentication, the
documented local-development bypass.

### 4. Roles

A key is a machine principal with no roles. `RequireRole` skips its check
when the request authenticated with a key: the scope check in the auth layer
replaces it as the gate, and the guards stacked at route registration
(branch walls among them) see no user and apply no user grants.

- Tech admin and staff routes are ordinary module routes (`admin`, `users`):
  a key holding `admin:read` or `admin:write` reaches them like any user with
  the role would.
- Key management itself, the three routes under `/api/v1/admin/keys`, is
  user-only: a key is refused there whatever scope it holds. A key that
  could mint or revoke keys would be a key that could grant itself
  everything, and the scope check cannot express "no key may hold this"
  because the minted key would simply hold it.
- The POS routes that resolve their cashier from the request identity
  (starting a sale, opening a till, recording a return) are user-only the
  same way: the cashier is a human user the JWT names, so a machine key is
  refused 403 `forbidden` with the message "a cashier must be a user", in
  the wire ADR's envelope, rather than being handed the fabricated stand-in
  cashier the dev-mode fallback mints for keyless demo callers. A
  body-supplied cashier id does not help a key: it names a user the key
  asserts, not one the request authenticated. The refusal is served by the
  route itself, after the scope check admitted the key, and is logged
  server-side rather than audited; the routes that carry no cashier
  identity of their own (catalog, search, item changes, completion, void,
  reports) stay reachable with `pos` scopes.

### 5. Refusals and the audit trail

Every 403 the auth layer serves a valid key writes one audit row through
`pkg/audit`, with the key as the actor (`actor_id` the key's id, and
`user_id` null: a key is never a user, per the R1-14 attribution rule; the
kind is `key`, or `agent` with the same `actor_id` when the agent identity
headers rode along, and `user_id` is null either way):

- `key.scope_refused`, with the refused scope, the method and the path;
- `key.user_required`, on the key management routes;
- `key.path_refused`, off the module routes.

A 401 writes no row: an unknown or revoked key has no attributable id. The
refused request is answered 403 whether or not the audit write lands; a full
audit table must not turn a refusal into a server error. The one 403 a valid
key can be served outside the auth layer is the POS cashier rule in section
4; the route logs it server-side and writes no row.

A machine key is a principal for idempotency too (the R1-11 layers): the
ERP layer keys an `Idempotency-Key` claim on `key:<id>` in every auth mode,
so a keyed caller's retries share its own namespace instead of passing
uncached outside dev or joining the shared dev principal inside it.

### 6. Branch scoping

The branch wall (`user_locations` grants behind `X-Branch-Id`) is a user
concern and does not apply to a key: `api_keys` carries no branch, and a key
pinning `X-Branch-Id` behaves as any claims-less caller does today. Per
branch or per location key limits, if wanted, are a later cycle's decision on
this record.

### 7. No schema change

Migration 090 is not used. `api_keys` already carries the scope array,
`last_used_at` and `revoked_at`; the scope vocabulary lives in code, where
the census test holds it against the routes, and a CHECK constraint would
duplicate that vocabulary in SQL with nothing to keep the two aligned.

## Alternatives considered

**Wildcard scopes.** A `*` or `quotes:*` form would let one key stand for
"everything" or "the whole module". Rejected: no existing data carries them,
and a wildcard cannot be audited as a written grant the way an enumerated
list is.

**A per route scope table.** A mapping from each registered route to its
scope would allow non uniform names. Rejected: the path segment rule gives
callers and reviewers the scope from the URL alone, and the census test holds
the one rule against the whole surface.

**A second middleware beside the JWT one.** Mounting machine-key handling as
its own middleware would leave the JWT file untouched, but splits one
question (what credential is this Bearer token?) across two components that
must agree on ordering and on fail-closed defaults. Dispatch inside the auth
middleware keeps one resolution point.

**Accepting machine keys on `/api/integration/*` with an integration
scope.** The integration seam has its own published contract, its own
authentication and its own goldens; adding a second credential to it widens
a frozen surface. Rejected: the seam stays `X-Integration-Key` only, and its
own scoped-key migration, if ever, is decided on its own record.

## Consequences

- Machine callers get per principal, per module, read versus write authority
  with per key attribution in the audit trail, replacing one shared secret
  for every service.
- A key's reach is exactly its written scopes: an admin granting a key reads
  the grant back from the key row and from the refusal rows when a key
  overreaches.
- The auth layer's error bodies are now ADR 0001's envelope (lowercase codes,
  the specific message kept), which is the conversion ADR 0001 assigns to
  this item; the JWT validation semantics are unchanged.
- Route additions under `/api/v1` must declare their module segment in the
  vocabulary, which is the point: a new route arrives with a scope policy or
  not at all.

## Known limits

Accepted with this record, each named with the later item that narrows it:

- **A key is not branch bound.** `api_keys` carries no branch, so a key
  reads and writes every branch the module routes expose unless the caller
  itself pins `X-Branch-Id` (section 6); a `quotes:read` key can read every
  branch's quotes. Branch bound keys narrow this with the confirm gated
  write scopes of cycle 5, item R5-2.
- **`admin` and `users` are broad scopes.** The module is the only
  granularity, so `admin:write` reaches AI settings, routing settings,
  staff and module grants alike, and `users:write` reaches branch grants
  for human users, wider than "per module" sounds. Finer scope names (for
  example `admin:settings`, `users:grants`) narrow this with the same
  confirm gated write scopes of cycle 5, item R5-2 (`*:propose`,
  `*:commit`).
- **The prefix lookup is a timing oracle for prefix existence.** A request
  whose 12 character prefix matches a stored key but whose body is wrong
  pays one Argon2id compare; an unknown prefix returns before any hash
  work, so response time tells a caller whether a prefix exists. The
  impact is low: the prefix is not a secret (an administrator reads it
  from the key row), the global rate limit bounds probing, and the hash
  compare itself is constant time. A dummy compare on the miss path would
  close it if it ever matters.
