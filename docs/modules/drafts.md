<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Drafts, links and the confirm gated scopes

A draft is a proposed document that is not yet an entity, shared
between an agent tool and a person's screen. A person reviews it and
promotes it: a successful promotion is the one transaction that turns
the draft into the named entity, with the same revision the committer
read. The links surface turns a record id (a UUID or, when the
record has one, its document number) into every frontend's URL for
the record, so an agent never embeds the desk's route table. The
confirm gate keeps an agent acting with a person's session from
committing: the gate refuses the promotion route and every entity
write route of a confirm gated module for such a request, server
side, and writes the `agent.commit_refused` audit row. Reads and
draft writes pass.

This page is the work of C5-2a (item C5-2a, the drafts, links and
confirm gated scopes; merged as PR 66, ADR 0007). The Go code is in
`core/internal/drafts/`, `core/internal/links/`,
`core/pkg/confirmgate/`, and the machine-key core in
`core/pkg/middleware/machinekey.go`. The table is `drafts` and the
change feed's `draft_events`, the branch bound key column is
`api_keys.branch_id`, all in migration 103
(`core/migrations/103_drafts_links_scopes.sql`). The wire contracts
are in `core/api/fragments/drafts_quotes.yaml`,
`core/api/fragments/drafts_orders.yaml`, and
`core/api/fragments/links.yaml`.

## What it does in a yard

An agent (an external tool acting on a person's behalf, a chat
assistant, an automation) proposes a quote or an order by writing
a draft. The agent saves as it works (the draft row carries the
revision), the person reads the same draft in the desk and
promotes it when the payload is right. The payload is the
module's own request body as JSON: the quote create body or the
order create body, not a file, not a revision of its own. A
draft that is not yet ready to promote still saves: `validation`
is computed on every read and write from the payload alone, by
the kind's parser, so the editor knows what still blocks a
promotion without refusing the save. The draft's branch is
fixed at create, and the same draft is visible to every caller
the kind admits: drafts are shared, not owned.

A promotion is the one transactional act that turns a draft into
its entity (`drafts.Promote`, `core/internal/drafts/service.go`).
The lock, the status check, the revision check, the kind's
promoter with the draft's branch as the branch context, the draft
row, the audit row, the draft event, and the outbox events last,
all in one transaction (`Promote` and its `inTx` body). A failure
rolls the whole transaction back; the draft stays open at the same
revision, and the module's own error is carried out (the order
`order_not_draft` on a subject that left draft, `subject_stale`
added when the subject moved, the quote's `payload.`-prefixed
fields on a 400). After the rollback the drafts service writes one
`draft.promotion_refused` row, best effort, and the refusal's
audit row is the receipt a person has for what was tried.

The links surface is the one resolver every frontend reads
through: an agent that needs a URL to a record asks the server
for the record's links, and the resolver answers with the desk
record path (absolute when `GABLE_PUBLIC_URL` is set, origin
relative otherwise), the front door's `?open=` form, `gable://`
for the shell, a `null` portal until the portal has record
screens, and the agent slot from `GABLE_AGENT_URL_TEMPLATE`
(`null` when unset). A record the caller cannot see is the same
404 a missing one gets, so the resolver never leaks a record's
existence by differing between unseen and missing. The `{id}`
slot is a UUID or, for the entities that carry one, the
document number (`Q-000123`, `SO-000123`, `IN-000123`); the
links' record segment is the number when the entity has one and
the UUID otherwise.

The confirm gate and the propose and commit scopes are the
honest enforcement of the "agents propose, people commit" rule
for confirm gated modules (`quotes`, `orders`). An agent acting
with a person's session may still hold the person's JWT, so
the gate is the server side check: an agent marked session
(an `X-Acting-As` header with any non empty value) may list
drafts, read drafts, save drafts, transition drafts, and resolve
links; it may not promote a draft, may not write the entity, and
may not attach the file the parse flow puts on a quote. The
keyed form of the same rule is a key with `propose` but not
`commit`: a key holding `quotes:propose` may save and read drafts
and may not promote; a key holding `quotes:commit` may do
everything `propose` may do and may also promote. Neither verb
admits the entity routes: reading a quote needs `quotes:read`,
and writing one (including the file attach) needs `quotes:write`.
A key that must promote and also edit quotes holds `quotes:commit`
and `quotes:write`. The gate sees the key id the machine-key
core set, so a keyed request carrying an agent marker still
reaches the handler (`confirmgate.isAgentSession` in
`core/pkg/confirmgate/confirmgate.go`, the keyed return).

A machine key may be branch bound (`branch_id` at mint; ADR 0007
section 5.5). A key minted with `branch_id` is pinned to its
branch: its lists, by id reads, drafts, the feed and links see
that branch only; a request naming another branch in
`X-Branch-Id` is a 403 `forbidden` audited as
`key.branch_refused`. The row carries the bound branch id
(`branch_id`), the request method, and the request path
(`AuditKeyBranchRefusal` in `core/pkg/audit/audit.go`); the
refused `X-Branch-Id` value is not stored. A non UUID
`X-Branch-Id` value fails `uuid.Parse` and is refused with the
same 403 and the same `key.branch_refused` row (the bound
branch id is recorded, not the bad value). An empty
`X-Branch-Id` is served under the pin: an empty header names
no branch, so the branch check is skipped. The pin is set before the branch switch
is read (`pkg/middleware/machinekey.go`, the `principal.BranchID
!= nil` branch in `handle`), and the routes that mount no branch
middleware (the user-only prefixes `/api/v1/admin/keys` and
`/api/v1/me`) are JWT user routes, so a machine key cannot
reach them, bound or not. The routes that mount no branch middleware are held to the
pin by the key branch wall (`core/pkg/middleware/keybranch.go`):
a bound key naming another branch in a body `branch_id` or a
path id, or a row of another branch (`PUT` and `DELETE
/api/v1/locations/{id}`), is a 403 `forbidden` in the wire
envelope naming the field, audited as `key.branch_refused`
like the header rule; its own branch passes, and a body is
read whole, so a body that is not exactly one complete JSON
value (trailing data after the first value, or unparsable) is
refused rather than passed for the handler to read only in
part. The directory
create (`POST /api/v1/branches`) refuses a bound key outright,
and `PUT` and `DELETE /api/v1/branches/{id}` refuse it another
branch. A bound key holding `users:grants` grants, revokes and
moves home only its own branch. The routes that act across
every branch refuse a bound key outright: the exposure scan
and the two exposure lists, the market index refresh, the
events feed (`GET /api/v1/events`), every reporting route,
the dealer wide GL, AP and bank reconciliation reads, the
sales team reads and the known users list (`GET /api/v1/users`).
The routes that write a named customer's data are confined to
the pin through that customer's `customer_branches`: the tax
exemption writes and the customer priced rules, plain and
category, single and bulk. The branch directory reads
(`GET /api/v1/branches`, `GET /api/v1/branches/{id}`) stay
reference data (the PR 39 decision). An unbound key behaves
as before.

## Routes

Every route below is in `core/api/fragments/drafts_quotes.yaml`,
`core/api/fragments/drafts_orders.yaml`, or
`core/api/fragments/links.yaml`; the registered handlers are in
`core/internal/drafts/handler.go` and `core/internal/links/links.go`;
the route census (`core/api/ROUTES.txt`) lists each one under
`internal/quote`, `internal/order`, or `internal/links`. The
`PUT /api/v1/quotes/{id}/file` route is `core/internal/quote/handler.go`
`HandleAttachFile`, also in the census.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/drafts/quotes` | Cursor list of quote drafts, newest first on `created_at` then `id` (never `updated_at`); filters `status` (open, promoted, discarded), `subject_id`, `created_by_kind`; `include=total` adds a total; drafts are shared, not owned; behind the branch wall. |
| POST | `/api/v1/drafts/quotes` | Create a quote draft; a create draft (`subject_id` null) carries the quote create request as its payload, an edit draft names the quote it edits and may assert `subject_revision`; `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/drafts/quotes/feed` | The quote drafts change feed as `text/event-stream`; events `ready` (carries the head cursor), `draft` (carries the write's summary with its by actor), `cursor` (advances a filtered stream past rows its filters exclude), `reset` (the resuming client aged out behind the purge), and `reauth` (the stream is closing at the token's exp, the lifetime bound, or a revoked key); `Last-Event-ID` wins over the `cursor` parameter. |
| GET | `/api/v1/drafts/quotes/{id}` | One quote draft with its payload and its computed validation; the ETag is the revision; a draft outside the caller's branch wall is a 404. |
| PUT | `/api/v1/drafts/quotes/{id}` | The whole payload compare and swap; `If-Match` or body `revision` is required (428 without), a moved revision is 409 `stale_revision`, any status but `open` is 409 with the `draft_not_open` blocker; `subject_revision` may move forward, never past the subject's current revision; `Idempotency-Key` rides the standard header. |
| POST | `/api/v1/drafts/quotes/{id}/transitions` | Discard or reopen a quote draft (`to` is `discarded` or `open`) on the revision precondition; `promoted` is terminal (its retry is told `already_promoted`); `Idempotency-Key` rides the standard header. |
| POST | `/api/v1/drafts/quotes/{id}/promote` | The confirm: the revision the committer read becomes a quote in one transaction, born in status `draft` with its `Q-` number and `quote.created`, beside `draft.promoted`; 428 without a precondition, 409 `stale_revision` on a moved one, 409 with the `already_promoted` blocker on a keyless retry, 409 with the `draft_discarded` blocker on a discarded draft; `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/drafts/orders` | Cursor list of order drafts, newest first on `created_at` then `id`; same filters as the quote kind. |
| POST | `/api/v1/drafts/orders` | Create an order draft; create or edit shape, same rules as the quote kind; `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/drafts/orders/feed` | The order drafts change feed; same five events as the quote feed; the `cursor` parameter or `Last-Event-ID` resumes. |
| GET | `/api/v1/drafts/orders/{id}` | One order draft with its payload and validation; ETag is the revision. |
| PUT | `/api/v1/drafts/orders/{id}` | Replace the payload, same compare and swap as the quote kind. |
| POST | `/api/v1/drafts/orders/{id}/transitions` | Discard or reopen an order draft on the revision precondition. |
| POST | `/api/v1/drafts/orders/{id}/promote` | The confirm: the revision becomes an order born in status `draft` with its `SO-` number (`order.created`, then `draft.promoted`; the confirm with its credit, PO and contact checks stays an order transition), or an edit draft's payload becomes the draft order's update (`order.updated`, then `draft.promoted`); 409 `order_not_draft` on a subject that left draft, 409 with the `subject_stale` blocker added when the subject moved; `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/links/quotes/{id}` | Resolve a quote to every frontend's record URL; the `{id}` slot is a UUID or a quote number such as `Q-000123`. |
| GET | `/api/v1/links/orders/{id}` | Resolve an order to every frontend's record URL; the `{id}` slot is a UUID or an order number such as `SO-000123`. |
| GET | `/api/v1/links/invoices/{id}` | Resolve an invoice to every frontend's record URL; the `{id}` slot is a UUID or an invoice number such as `IN-000123`. |
| GET | `/api/v1/links/customers/{id}` | Resolve a customer to every frontend's record URL; the `{id}` slot is a UUID (customers carry no document number, so `number` is null and the segment is the UUID). |
| GET | `/api/v1/links/products/{id}` | Resolve a product to every frontend's record URL; the `{id}` slot is a UUID and `number` is null. |
| GET | `/api/v1/links/drafts/quotes/{id}` | Resolve a quote draft to every frontend's record URL; the `{id}` slot is the draft's UUID (a draft has no number); the link needs the confirm verbs, not the module read scope, because proposals are unfinished work an existing read key must not start reading. |
| GET | `/api/v1/links/drafts/orders/{id}` | Resolve an order draft to every frontend's record URL; the `{id}` slot is the draft's UUID. |
| GET | `/api/v1/quotes/{id}/file` | Download a quote's original uploaded file by quote id; the wire form of the id slot is a UUID or the quote's `Q-` number, matching the link and the entity route. |
| PUT | `/api/v1/quotes/{id}/file` | Attach a quote's original file on the revision precondition, only while the quote is in status `draft`; the 5 MiB bound the create applies; being an entity write on a gated module, an agent marked session is refused it by the confirm gate. The id slot is a UUID only (`pathID` in `core/internal/quote/handler.go`), unlike the read route which accepts a UUID or the quote's `Q-` number (`pathRecord`). |

The same seven shapes (the list, the create, the read, the
replace, the transitions, the promote and the feed) sit over the
order kind, with the same field names, the same `Idempotency-Key`
header, the same `If-Match` or body `revision` precondition, and
the same wire envelope; the only differences are the kind
vocabulary (`orders`, the SO prefix) and the order-specific 409
verdicts on a promotion. The wire form of the id slot is
shared across `/api/v1/quotes/{id}`, `/api/v1/orders/{id}`,
`/api/v1/invoices/{id}`, and the matching `links` resolvers
(`core/internal/quote/handler.go` `HandleGetQuotePath`, the
order and invoice handlers, the link resolver in
`core/internal/links/links.go`): a UUID or the document number,
and the link's record segment is the number when the entity
has one.

## The draft resource

`Draft` (see `core/api/fragments/drafts_quotes.yaml`
`components.schemas.QuotesDraftDocument` and the matching
`OrdersDraftDocument`, `core/internal/drafts/model.go`
`Document`). One wire type from every draft route, promotion
included. The summary form (the list) is the same object without
`payload` and without `validation.problems`, keeping
`validation.ready`.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The draft id. |
| `module` | enum | `quotes` or `orders`; the kind's path segment, what the kind registry holds. |
| `status` | enum | `open`, `promoted`, `discarded` (lowercase on the wire; the database keeps the uppercase CHECK vocabulary, mapped at the boundary in `kind.go` `StatusWire`). |
| `revision` | integer | Starts at 1; returned as `ETag`; the precondition on every `PUT` and `POST /transitions` and `POST /promote`. |
| `branch_id` | UUID | The draft's branch, fixed at create. |
| `subject_id` | UUID, nullable | The quote or order the draft edits; null on a create draft. |
| `subject_revision` | integer, nullable | The subject's revision the draft was built on; asserted at create, moved forward by a bounded rebase on PUT. |
| `payload` | object | The module's own request body as JSON; the module's code decodes and validates it. |
| `validation` | object | Computed on every read and write from the payload alone, by the kind's parser; `ready` is true when the parser would accept the payload as a create (or update) request, and `problems` is exactly that 400's details with `payload.`-prefixed fields. |
| `created_by`, `updated_by` | object | What `pkg/actor` resolved for the request: the user's subject for an agent acting with a person's session, the key's id for a keyed writer, the `acting_as` and `tool` carry the agent marker and tool name. |
| `promoted` | object, nullable | The promotion's outcome: `entity_id`, `number` (the document number when the entity has one), `at`, `by`. Set once. |
| `discarded` | object, nullable | The discard's outcome: `at`, `by`. Cleared on reopen. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC, microsecond precision (ADR 0001 section 12). |

`QuotesDraftCreateRequest` and `OrdersDraftCreateRequest`: the
body of the create. `payload` is required, the module's own
create body or, with `subject_id`, update body; at most 256 KiB
of JSON. `subject_id` and `subject_revision` are the optional
edit shape; `subject_revision` is asserted at create (a mismatch
is 409 `subject_stale`).

`QuotesDraftReplaceRequest` and `OrdersDraftReplaceRequest`:
the body of the PUT. `payload` is required, `revision` is the
body form of the precondition, beside `If-Match`;
`subject_revision` is the bounded rebase, never past the
subject's current revision.

`LinkAnswer` (`core/api/fragments/links.yaml`
`components.schemas.LinkAnswer`, `core/internal/links/links.go`
`Answer`): the record and its links. `entity` (the entity
name, `quote`, `order`, `invoice`, `customer`, `product`,
`draft`), `module`, `id`, `number` (null when the entity has
none), and the five link slots: `desk` (the desk record path,
drawn from the `Table` in `core/internal/links/links.go`:
`accounts/{segment}` for customers, `inventory/{segment}`
for products, `quotes/drafts/{segment}` for quote drafts,
`orders/drafts/{segment}` for order drafts;
absolute when `GABLE_PUBLIC_URL` is configured,
otherwise a path relative to the deployment's origin),
`front_door` (the
front door's `/?open=<record path>` form, which signs a person
in and then opens the record), `portal` (null until the portal
has a record screen for the entity), `app` (the `gable://`
deep link the Tauri shell parses), and `agent` (the agent UI's
record URL from `GABLE_AGENT_URL_TEMPLATE`, with `{module}`,
`{entity}`, `{id}`, `{number}` placeholders; null when the
template is unset, every deployment today).

## Lifecycle and transitions

The draft lifecycle has three states: `open` (the agent and
the person are working on it), `discarded` (the work is
abandoned but reversible), and `promoted` (the draft became
its entity, terminal). The transitions the wire API supports
(`POST /api/v1/drafts/{quotes|orders}/{id}/transitions`):

- `open` to `discarded`: the discard transition; audit row
  `draft.discarded`, draft event `op = "discarded"`,
  `status = "DISCARDED"`.
- `discarded` to `open`: the reopen transition; audit row
  `draft.reopened`, draft event `op = "reopened"`,
  `status = "OPEN"`.
- `open` to `promoted`: the `POST /api/v1/drafts/{m}/{id}/promote`
  route, the one transaction that turns the draft into its
  entity; audit row `draft.promoted`, draft event
  `op = "promoted"`, `status = "PROMOTED"`, with the
  `promoted_entity_id` and `promoted_number` on the event.

A retry of an already promoted promotion is told
`already_promoted` (`StatusPromoted` branch in `Promote`); an
attempt to promote a discarded draft is told
`draft_discarded` (`StatusDiscarded` branch). A promotion
that fails the module's own check rolls the whole transaction
back; the drafts service writes one
`draft.promotion_refused` row, best effort, with the module,
the revision, the draft's `status` (drawn from the draft the
service re reads after the rollback), and the `code` the
module returned (`auditRefusedPromotion` in
`core/internal/drafts/service.go`).
The PUT on a non open draft is 409 with the `draft_not_open`
blocker; an edit is not a transition. The Promote route
also returns 409 with the `subject_stale` blocker on a stale
subject, the `already_promoted` blocker on a keyless retry,
and the `draft_discarded` blocker on a promotion of a
discarded draft.

A draft is created (`draft.created` audit row,
`op = "created"` event), saved (`draft.updated` audit row,
`op = "updated"` event), transitioned (`draft.discarded` or
`draft.reopened` audit row, matching event), and promoted
(`draft.promoted` audit row, `op = "promoted"` event). In
step 9 of `Promote` (`core/internal/drafts/service.go`) the
service writes the outbox events the kind's `Promote`
returned (`quote.created` and `quote.updated`,
`order.created` and `order.updated`), then writes one
`draft.promoted` event of its own (`module`, `entity`,
`entity_id`, `number`, `revision`, `proposed_by`,
`committed_by`), all inside the promotion's transaction.
The `created_by` and `updated_by` actor
quadruples ride every read; the `promoted` and `discarded`
blocks ride the `Document` wire form, the feed item, and the
audit row.

## Events the module writes

The drafts module writes one outbox event of its own:
`draft.promoted`. In step 9 of `Promote`
(`core/internal/drafts/service.go`) the service writes the
outbox events the kind's `Promote` returned (`quote.created`
and `quote.updated` for the quote kind, `order.created`
and `order.updated` for the order kind), then writes the
`draft.promoted` event of its own; the outbox takes them
in order.
The drafts module writes a feed of its own: one row per
write, in `draft_events`, with a commit ordered position
from `draft_events_position_seq` under a transaction scoped
advisory lock (`pg_advisory_xact_lock(1685218678)`, the
ASCII bytes `drev`, distinct from the outbox's `outb`). The
feed's `op` vocabulary (`CHECK (op IN ('created',
'updated', 'discarded', 'reopened', 'promoted'))` on
`draft_events`, `core/migrations/103_drafts_links_scopes.sql`)
maps one to one to the audit row's action.

The audit actions the drafts service writes (the writer calls
in `core/internal/drafts/service.go`):

| Audit action | Written at |
|---|---|
| `draft.created` | `Create` (`core/internal/drafts/service.go`, the `audit.Log` call inside `Create`'s transaction) |
| `draft.updated` | `Replace` (the `audit.Log` call inside `Replace`'s transaction) |
| `draft.discarded` / `draft.reopened` | `Transition` (the `audit.Log` call inside `Transition`'s transaction; the action follows `to`) |
| `draft.promoted` | `Promote` (the `audit.Log` call inside `Promote`'s transaction, after the kind's promoter returns) |
| `draft.promotion_refused` | `auditRefusedPromotion` (after a rolled back promotion, best effort) |

The change feed is the `text/event-stream` the `GET
/api/v1/drafts/{m}/feed` route serves, never payloads
(`core/internal/drafts/feed.go` `stream`, the
`feedItem` shape in `core/internal/drafts/model.go`):

- `event: ready` opens the stream with the head cursor, so a
  client opens first then reads; the SSE id of the event is
  the cursor for the next read.
- `event: draft` carries each write's summary with its by
  actor, the draft id, the module, the op, the revision, the
  status, the branch id and the subject id; the promoted
  block is filled on a promotion.
- `event: cursor` advances a filtered stream past rows its
  filters exclude, so a reconnecting client does not rescan
  the excluded rows.
- `event: reset` is the resume verdict: a cursor at or below
  the highest purged position has missed changes that aged
  out and must re-read what it shows.
- `event: reauth` closes the stream at the token's `exp`, the
  lifetime bound, or a revoked key; the close goes out before
  the stream ends.

The cursor is a feed minted id, the commit ordered position
(`FeedCursorScope = "drafts.feed_position"` in
`core/internal/drafts/feed.go`, the `parseFeedCursor` and
`mintFeedCursor` functions, `httpx.DecodeCursor` /
`httpx.MintCursor`). A client resumes through `Last-Event-ID`
(which wins when both are present) or the `cursor` parameter;
`draft_id` and `subject_id` narrow the stream to a single
draft or subject, and any other parameter is a 400 before
the stream opens. The
heartbeat (`FeedSettings.Heartbeat`, the default 15 seconds)
sends `:keepalive` comments and re-checks the key, and the
key recheck also runs at the loop top on a `lastCheck` timer
and before every re-read of a full page; a revoked key's
stream closes within one heartbeat of the revocation. A
keyed stream rechecks its key at every heartbeat by id, one
row read, no hash work (`KeyActive` in
`core/internal/techadmin/service.go`).

The stream limits (`feed.go` `FeedSettings`, the
`MaxStreamsPerPrincipal` and `MaxStreams` fields, default 8
per principal and 500 total) are answered before the stream
opens, in the error envelope, with status 503 when this
process holds its total limit and status 429 when the
principal holds its per principal limit. The principal is
the key id for a keyed stream (`key:<id>`), the user subject
for a session (`user:<sub>`), or `anonymous` otherwise
(`feedPrincipal` in `core/internal/drafts/feed.go`). The
retention purge (`Purge`, in `core/internal/drafts/purge.go`)
records the highest purged position in `draft_events_purged`,
so a client resuming from a cursor at or below it can be
told `event: reset` instead of silently missing changes.

## Scopes, roles and keys

The scope grammar of a machine key on these routes is four
verbs, applied per module (`core/pkg/middleware/machinekey.go`,
`scopeReadSuffix = ":read"`, `scopeWriteSuffix = ":write"`,
`scopeProposeSuffix = ":propose"`, `scopeCommitSuffix = ":commit"`,
the `ScopeClass` enum and its `AdmittedScopes` switch):

- `<module>:read` admits `GET` and `HEAD` on every route of
  the module's non drafts and non links namespace. The
  entity link route (`GET /api/v1/links/{module}/{id}`,
  `ScopeLink` in `ScopeTarget`'s `links` arm) is also
  admitted by `read` (`AdmittedScopes` returns
  `module + scopeReadSuffix` for `ScopeLink`). The drafts
  and links namespace of a confirm gated module names
  `ScopeDraftRead` (the list, the read, the feed) and
  `ScopeDraftLink` (the resolve for a draft) as draft
  classes; both are admitted by `<module>:propose` and
  `<module>:commit`, never by `read`.
- `<module>:write` admits every other method on the module's
  non drafts and non links namespace. The drafts and links
  namespace does not admit by `write`. `ScopeTarget` resolves
  them by whole method and shape; any shape the policy table
  does not name (a wrong method, a wrong segment count, a dot
  segment or a doubled slash) is `ok = false` and the auth
  core answers 403 `key.path_refused`. `ScopeEntityWrite` is
  the class of every non read method outside those two
  namespaces; it is admitted by the module's `write` scope
  (or its finer name from `writeScopeOverrides`), and it is
  the class the confirm gate refuses for an agent marked
  session on a confirm gated module.
- `<module>:propose` admits the draft list, the draft read,
  the feed, the create, the PUT, the transitions, and the
  link resolution for a draft
  (`ScopeDraftRead`, `ScopeDraftWrite`, `ScopeDraftLink` in
  `AdmittedScopes`).
- `<module>:commit` admits everything `<module>:propose`
  admits, plus the `POST /api/v1/drafts/{m}/{id}/promote`
  route (`ScopePromotion`, the `ScopePromotion` arm of
  `AdmittedScopes`); a key with `propose` but not `commit`
  cannot promote, which is the keyed half of the "agents
  propose, people commit" rule.

The scope module is read off the URL by `ScopeTarget`
(`core/pkg/middleware/machinekey.go`, the `switch segments[0]`
in `ScopeTarget`): for the drafts namespace, the module is
the second segment (`quotes` under `/api/v1/drafts/quotes`,
`orders` under `/api/v1/drafts/orders`); for the links
namespace, the module is the second segment for an entity
link and the third segment for a draft link; for every other
path, the module is the first segment, with the admin areas'
finer names (`admin/settings`, `admin/staff`,
`admin/modules`, ADR 0009). The grammar
(`ValidScopeGrammar`) is the union of the module read and
write scopes (with the finer names in place of the coarse
ones they replace: `users:grants` for the `users` module's
write, `admin:settings` / `admin:staff` / `admin:modules`
for the three admin areas), and the `propose` and `commit`
verbs of the confirm gated modules. The mint's
`ValidateGrantScopes` (`core/internal/techadmin/service.go`)
holds a grant against this grammar; the migration 103
report carries its own SQL copy of the grammar, held to
`ValidScopeGrammar` by `TestMigration103_ReportMatchesTheGoGrammar`;
a key whose scopes are all in grammar and include a
`propose` or `commit` scope gets the
`gains reach through a propose or commit scope` notice (the
new verbs are a wider reach than `read` and `write`).

The confirm gated module set is `quotes` and `orders`
(`confirmGatedModules` in `core/pkg/middleware/machinekey.go`,
`IsConfirmGated`); the census test
`TestDraftRoutesResolveThroughScopeTarget` in
`core/pkg/middleware/machinekey_census_test.go` resolves
every census route under `/api/v1/drafts/` and
`/api/v1/links/` through `ScopeTarget`, requires each draft
route's module to be in `ConfirmGatedModules()`, and
requires the seven draft routes for each module in that set. The auth core and the confirm gate read
the same set; a key with `quotes:read` may read a quote
(`GET /api/v1/quotes/{id}`) but may not list the
quote drafts or resolve a quote draft link (the drafts and
links admit by `propose` and `commit`); a key with
`quotes:propose` may list, read, save and transition quote
drafts and may resolve a quote draft link, but may not
promote, may not write the entity, and may not attach a
file. A key with `quotes:commit` may do everything the
`propose` key may do, plus the promotion. The promotion
route is `ScopePromotion` and is admitted by `quotes:commit`
alone (`AdmittedScopes` returns just `quotes:commit` for
`ScopePromotion`). The entity routes are not reached through
`propose` or `commit`: the entity reads need `quotes:read`,
the entity writes and the file attach need `quotes:write`.

The audit rows the machine-key auth core writes
(`core/pkg/middleware/machinekey.go`, the
`AuditActionKeyScopeRefused` and `AuditActionKeyPathRefused`
consts, the `auditRefusal` calls inside `handle`):

| Refusal action | When |
|---|---|
| `key.scope_refused` | The key is valid but holds none of the scopes `AdmittedScopes` lists for the route's class, or neither `propose` nor `commit` for a draft route, or `propose` but not `commit` for the promotion. The `scope` is empty for a dirty spelling under a real module (a dot segment or doubled slash). Otherwise the row's `scope` is the first admitted scope (the one the policy table records as refused). |
| `key.user_required` | The route is in the user only prefix list (the prefixes `underUserOnlyPrefix` tests in `core/pkg/middleware/machinekey.go` `machineKeyUserOnlyRoutes`: `/api/v1/admin/keys` and `/api/v1/me`). A request whose path falls under one is a 403 with the `key.user_required` audit row; a machine key cannot reach any of these routes under any scope, and no scope check would admit them. |
| `key.path_refused` | The path is not a `/api/v1` module route the key system knows, or not a shape the drafts and links policy table names (a later draft route cannot quietly fall into the draft write class: `ScopeTarget` fails closed). |
| `key.branch_refused` | A branch bound key named another branch in `X-Branch-Id` (the `principal.BranchID != nil` branch in `handle`; `AuditKeyBranchRefusal` in `core/pkg/audit/audit.go`). The row carries the bound branch id, the request method, and the request path; the refused `X-Branch-Id` value is not stored. A non UUID `X-Branch-Id` value (`uuid.Parse` fails) is refused the same way. An empty `X-Branch-Id` is served under the pin: an empty header names no branch, so the branch check is skipped. |

Before the refusal table the auth core runs the credential
check (`core/pkg/middleware/machinekey.go`, the `handle`
function's first `errors.Is(err, ErrInvalidMachineKey)`
branch). An unknown, malformed, or revoked key is
`ErrInvalidMachineKey`: the response is 401 with code
`unauthorized` and message `invalid machine key`, and no
audit row is written (the key id is unknown or stale, so an
attribution row would name nothing). A validation
infrastructure fault (the database cannot serve the key
lookup) is 503 with code `unavailable` and message
`machine key validation is unavailable`, again with no
audit row.

A branch bound key is minted with `branch_id` in the body
of `POST /api/v1/admin/keys` (`core/internal/techadmin/service.go`
`GenerateKey` and `core/internal/techadmin/input.go`
`CreateKeyRequest`): the `branch_id` is verified to be a
branch (`BranchExists` on the repository) and stored on
`api_keys.branch_id` (migration 103's step 4,
`core/migrations/103_drafts_links_scopes.sql`); the column
is `NULL` for unbound keys and never edited after mint
(`GenerateKey`'s comment, "it is stored at mint and never
edited"). The auth core's `handle` reads `principal.BranchID`
and refuses any other branch in `X-Branch-Id` with status
403 and the `key.branch_refused` audit row. The row carries
the bound branch id, the request method, and the request
path; the refused `X-Branch-Id` value is not stored. A
non UUID `X-Branch-Id` value fails `uuid.Parse` and is
refused with the same 403 and the same `key.branch_refused`
row. An empty `X-Branch-Id` is served under the pin (an
empty header names no branch, so the branch check is
skipped). The request's branch context is the pin for every
other request, so a bound key's lists, by id reads, drafts,
the feed and links see that branch only and a payload
`branch_id` is held to it by the same `branchctx` rule the
unbound case uses. The
routes that mount no branch middleware (the user-only
prefixes `/api/v1/admin/keys` and `/api/v1/me`) are JWT user
routes, so a machine key cannot reach them, bound or not.
The branch directory and user grant routes
(`/api/v1/branches` and its children except `/tree`,
`/api/v1/users` and its children) carry no branch wall:
the pin does not narrow them, writes included. A bound key
holding `users:grants` is limited by that scope only, and
can grant a user any branch. The branch CRUD verbs
(`GET`, `POST`, `PUT`, `DELETE` on `/api/v1/branches` and
its by id children except `/tree`) are limited by the key's scope only (the role guard passes a scope checked machine key), so a branch bound key's pin does not confine
those writes either; a bound key holding `branches:write`
can write any branch. The `PUT /api/v1/locations/{id}` and
`DELETE /api/v1/locations/{id}` routes are limited by the key's scope only (the role guard passes a scope checked machine key), so a bound key holding `locations:write`
can write any location. An operator should not mint a
branch bound key with `users:grants`, `branches:write` or
`locations:write` until this gap is fixed (the pin narrows only the routes that mount the branch middleware; on the rest a key reaches whatever its scopes admit). An unbound key behaves
as before.

The confirm gate sits inside auth and outside idempotency
in the serve chain (`core/internal/app/serve/chain.go`,
`buildChain`, the `RequestLogger -> HTTPMetrics ->
RequestID -> Recovery -> [RateLimit] -> CORS -> Actor ->
[Auth | MachineKeyAuth] -> ConfirmGate -> MaxRequestSize
-> [Idempotency] -> CacheControl -> Mux` order). The gate
fires on `ScopePromotion` and `ScopeEntityWrite` for an
agent marked session on a confirm gated module
(`confirmgate.gated` and `confirmgate.isAgentSession` in
`core/pkg/confirmgate/confirmgate.go`); the route classes
the gate sees come from `ScopeTarget` so the same set the
auth core scopes against is the set the gate refuses.
The gate's audit row is `agent.commit_refused`
(`confirmgate.refuse`), the entity is the draft on the
promotion route or the entity the path names on an entity
write (the module with the nil UUID on a create); the
method and the bounded path ride the changes map, the
tool rides it when the actor's `Tool` is set. The gate is
always mounted: a nil sink means "refuse exactly the same
and write no row" (`confirmgate.Middleware`'s nil handling,
the test `TestChainC5_2a_NilSinkKeepsTheGate` in
`core/internal/app/serve/chain_c5_2a_test.go`), never
"no gate", so a chain built without a sink cannot fail
open on the gated writes.

A session request is any request not authenticated by a
machine key, so `AUTH_MODE=dev` with a marker is gated too
(`confirmgate.isAgentSession`, the
`middleware.KeyIDFromContext` check). The marker is any
non empty `X-Acting-As` value: `pkg/actor` records any
marker as an agent, so the gate must not key on the value
`"agent"` alone, or `X-Acting-As: x` would be recorded as
an agent and escape it. Keyed requests are governed by
their scopes alone, with or without a marker: the marker
is recorded and changes nothing (`confirmgate.isAgentSession`,
the `middleware.KeyIDFromContext` short circuit). A key
without `commit` is a "propose key" the gate does not need
to see; the `ScopePromotion` arm of `AdmittedScopes` makes
it a 403 `forbidden` at the auth layer, audited as
`key.scope_refused` with `scope = "quotes:commit"` (or
`orders:commit`), before the gate runs.

## Migration 103 and its down

Migration 103 (`core/migrations/103_drafts_links_scopes.sql`)
is the work in five idempotent steps:

1. `drafts`, one generic table for every draft kind. The
   payload is the module's own request body as JSONB; the
   module's code decodes and validates it. The `*_kind`
   columns carry the `audit_log.actor_kind` vocabulary
   (`user`, `key`, `agent`, `anonymous`); the status shape
   constraints are the database's side of the lifecycle
   (`PROMOTED` requires its provenance columns, `DISCARDED`
   its time, an edit draft requires the subject revision it
   was built on). The indexes are `idx_drafts_module_created`
   (the list's keyset), `idx_drafts_open_subject` (the
   open proposals on one subject), and
   `idx_drafts_promoted_entity` (the resolved entity lookup).
2. `draft_events`, the draft change feed's rows, with a
   commit ordered position drawn from its own sequence
   (`draft_events_position_seq`) under its own transaction
   scoped advisory lock (lock key `1685218678`, the ASCII
   bytes `drev`, distinct from the outbox's `outb`); the
   `draft_events_assign_position` `BEFORE INSERT` trigger
   takes the lock and draws. Draft saves are frequent and
   are not business facts, so they never share the
   outbox's one lock.
3. `draft_events_purged`, one row recording the highest
   purged position, so a client resuming from a cursor at
   or below it can be told `event: reset` instead of
   silently missing changes.
4. `api_keys.branch_id`, the branch bound keys column:
   `NULL` is today's behaviour (unbound), a branch pins the
   key. `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS
   branch_id UUID REFERENCES locations(id)`.
5. The read only report, a `RAISE NOTICE` for every
   unrevoked key whose scopes fall outside the section
   5.1 grammar (`api key % (prefix %) carries a scope
   outside the grant grammar: %; it keeps its stored reach;
   revoke and re-mint it`) and another for every key
   holding a `propose` or `commit` scope (`api key %
   (prefix %) gains reach through a propose or commit
   scope: %; review whether a propose key (cannot commit)
   fits the operator role`). The grammar the report
   enumerates is hard coded in the migration's `DO $$`
   block (`core/migrations/103_drafts_links_scopes.sql`,
   the `IF s IN (...)`, `ELSIF s ~ ...`, `ELSE bad_reach`
   chain): the module list is a literal set of names
   mirroring `ValidScopeGrammar`'s vocabulary, and a later
   module joining the vocabulary must change the SQL to
   match. The two `RAISE NOTICE`s live behind the same
   `IF bad_reach ... ELSIF gaining_reach ... END IF`, so a
   key that holds BOTH an off grammar scope AND a
   `propose` or `commit` scope gets only the off grammar
   notice; the `gains reach` branch is hidden by the first
   `IF` when `bad_reach` is true. The report changes
   nothing: the operator revokes and re mints such a key,
   which keeps exactly the reach it had, because the scope
   check matches exact strings (`scopeHeld` in
   `core/pkg/middleware/machinekey.go`).

The migration's report test
(`TestMigration103_AppliesOnASeededDatabaseAndReportsOffGrammarKeys`
in `core/internal/drafts/migration_test.go`) holds the
report against a seeded set of off grammar keys; the
`TestMigration103_ReportMatchesTheGoGrammar` test holds
the SQL grammar against `ValidScopeGrammar`. The
`TestMigration103_AppliesOnEmptyDatabaseAndIsIdempotent`
test runs up on an empty database and runs up again to
prove idempotence. The
`TestMigration103_DownThenUpLosesNoRow` test runs down and
up on a seeded database and checks no row is lost (the
drafts, draft events and api_keys rows the migration
touched).

The down
(`core/migrations/down/103_drafts_links_scopes_down.sql`)
is in reverse order. Every artifact is this migration's own
(no table existed before it, no backfill ran), so the drops
lose no row the base ever owned: drafts and their change
rows are products of this feature alone, and
`api_keys.branch_id` was `NULL` on every pre existing key.
One confinement exception: a key minted bound to a branch
after 103 keeps its row and its scopes when `branch_id` is
dropped, and the base has no notion of a pin, so it would
reach every branch its scopes allow. The down `DO $$`
block names every such key in a `RAISE NOTICE`, then
`UPDATE api_keys SET revoked_at = now() WHERE branch_id IS
NOT NULL AND revoked_at IS NULL` revokes each, so a roll
back never widens a key's reach. `ALTER TABLE api_keys
DROP COLUMN IF EXISTS branch_id` then drops the column,
and the down drops the index, the trigger, the function,
the table and the sequence in order.

## Known limits

The feed cursor is the global position
(`draft_events.position`, drawn from
`draft_events_position_seq` under the `drev` advisory lock),
not a per branch or per key position. The branch wall is on
the feed too (`feed.go`'s `filter.BranchID =
middleware.BranchIDForQuery(r.Context())` and the
`GrantsSub` follow, applied in `ReadEvents`'s SQL), so a
bound key's stream never sees another branch's draft event.
The `ready` event and the `cursor` events carry the global
position, so gaps in the positions a bound key sees show
that other branches wrote drafts, with no draft id or
content. A consumer that needs gapless positions per
branch must not read the gaps as lost events.

A revoke of a key that the feed's recheck has not yet seen
is read on for at most one heartbeat: the recheck runs at
the loop top on a `lastCheck` timer, before every re read
of a full page, and at the heartbeat (`feed.go`'s
`recheckKey` closure and the three call sites). A drain of
a full backlog never reaches the heartbeat case, so the
loop top and the re read cover the drain. The recheck is
fail closed on a database error: a forced `reauth` prevents
a revoked key from reading on for the lifetime close
(`feed.go`'s `recheckKey`, the `slog.Warn`).

The confirm gate's marker is `X-Acting-As`, an `actor`
seam that records the trim of the header as an agent: a
non empty trim sets `actor.Kind == actor.KindAgent` and
lowercases the value before storing (`pkg/actor/actor.go`,
the `TrimSpace` then `validateHeaderValue` then `ToLower`
chain inside `Middleware`). A value past 128 bytes OR
with a byte outside printable ASCII (0x20 through 0x7E)
is refused with a header rejection, so a whitespace only
marker is an empty trim and the actor is not marked as
agent (the marker must be a real string, not a header
present only for its presence). The gate keys on
`actor.FromContext(ctx).Kind == actor.KindAgent` with the
keyed short circuit (`confirmgate.isAgentSession`). An agent holding a person's
token that leaves the marker off is, to the server, that
person: the gate stops an honest agent and every agent
framework that sets the marker by construction; the
enforceable gate is a key, and an agent that must never
commit is given a `propose` key, not a session
(`confirmgate.go` package comment, the "honest limit").

The machine key refusal audit rows bound their stored path
and scope at `maxRefusalPathBytes` (512 bytes) and
`maxRefusalScopeBytes`; a refusal that names a path or
scope past the cap carries a `path_truncated` or
`scope_truncated` flag on the row's changes
(`AuditKeyRefusal` in `core/pkg/audit/audit.go`, the
`cutRunes` and `cutBeforePartialMarker` helpers). The
confirm gate's stored path is bounded at the same 512
bytes (`maxRefusalPathBytes` in
`core/pkg/confirmgate/confirmgate.go`, `boundedPath`).

The down of migration 103 only rolls back the artifacts
the up created: drafts, draft events, the api_keys
branch_id column, the index, the trigger, the function and
the sequence. The down drops `drafts`, `draft_events` and
`draft_events_purged` with their rows (they belong to this
feature alone). The `api_keys` rows stay, with the
`branch_id` column dropped; a key that was bound to a
branch is revoked first, so a roll back never leaves a
formerly pinned key live with wider reach. The outbox
events the drafts service wrote in the same transaction
(`quote.created`, `quote.updated`, `order.created`,
`order.updated`, and `draft.promoted`) stay in
`events_outbox`. A roll back of 103 does not retract an
entity a draft promoted, and does not retract the
`draft.promoted` event the same promotion wrote. The down
then up test (`TestMigration103_DownThenUpLosesNoRow` in
`core/internal/drafts/migration_test.go`) only counts
`locations` and `api_keys` rows; it does not check the
`drafts` or `draft_events` row counts, because the down
drops those tables and the row loss is by design.

## ADRs that govern this module

- [`docs/adr/0007-drafts-links-and-confirm-gated-scopes.md`](../adr/0007-drafts-links-and-confirm-gated-scopes.md): the design. Sections 1 and 2 set the resource and the lifecycle; section 3 is the change feed (cursor, heartbeat, the key recheck, the hub, the stream limits, the retention); section 4 is promotion and the kind registry (the `Kind` interface and the promoter); section 5 is the scope grammar (read, write, propose, commit; the finer names; the policy table; the `ScopeTarget` resolution; the user only routes; the branch bound key); section 6 is the actor quadruple; section 7 is document numbers in record URLs; section 8 is the links surface; section 9 is the migration order; section 10 is the quote and order kinds and the file attach.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md): the machine key itself (the segment scope rule; a key as a principal; roles; refusals and the audit row; ADR 0007 section 5.5 supersedes its first known limit with the branch bound key).
- [`docs/adr/0009-finer-admin-scopes.md`](../adr/0009-finer-admin-scopes.md): the `users` write scope narrowed to `users:grants`, the admin areas' finer scopes (`admin:settings`, `admin:staff`, `admin:modules`). `ValidScopeGrammar` lists them in place of the coarse ones they replace.
- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md): sections 1 and 2 (the list envelope and its cursor), section 5 (strict query parameters; `include=total` on the list), section 6 (enums lowercase on the wire; `QuotesDraftStatus` and `OrdersDraftStatus` are such enums, the database keeps the uppercase CHECK vocabulary), section 9 (idempotency keys on the creates, the PUT, the transitions and the promote), section 11 (revision and `If-Match`; the in place rule on the PUT and the transitions; the promote's revision precondition), section 12 (timestamps RFC 3339 UTC, every optional field present as null; `promoted` and `discarded` are null until set; `subject_id` and `subject_revision` are null on a create draft).
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md): the events table, the ordering guarantee, the writer, the read API. The drafts module writes no outbox events of its own; the kind's promoter returns the entity's outbox events and the outbox takes them in order (the `Promote` function in `core/internal/drafts/service.go`).

## How to try it locally

The repository's own seed and the local make targets are
the only way to exercise the module end to end. `make up`
builds and starts the local stack (Postgres, migrate and
seed, `core serve`, `core worker`, the web front door) on
http://127.0.0.1:8080 with `AUTH_MODE=dev`; `make down`
removes it. To run the core from source instead:
`make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make
up` and `make db` workflows use different compose projects
and volumes, so the `make db` data is never truncated or
removed by `make up` or `make down` (`AUTH_MODE=dev` needs
no `Authorization` header; the examples below show the
production header shape).

Migration 103 is the migration that creates the
`drafts` and `draft_events` tables, the
`api_keys.branch_id` column, and the retention marker
table. The migration's report runs at `migrate` time: a
seeded key with a scope outside the grammar logs a NOTICE
naming the key, the prefix, and the scopes; a seeded key
with a `propose` or `commit` scope logs a NOTICE naming
the key, the prefix and the scopes, with the
`gains reach through a propose or commit scope` message.
A key with both an off grammar scope and a `propose` or
`commit` scope gets the first notice only. A fresh `make
up` runs `migrate` first, then `seed`, so the NOTICE shows
up in the `migrate` step's log.

Take a branch id from `GET /api/v1/branches`. To create a
quote draft (a create draft, with the quote create request
as its payload, no `subject_id`), with an `Idempotency-Key`
for the create:

```
curl -X POST http://localhost:8080/api/v1/drafts/quotes \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d @quote-draft-create.json
```

with a body of `{"payload": {"customer_id": "<seeded
customer uuid>", "lines": [{"product_id": "<seeded
product uuid>", "quantity": "1"}]}}` (the exact body
shape is the quote module's create body; the drafts layer
stores the payload verbatim and the quote kind's parser
validates it on every read and write). The response
carries the open draft with `Location: /api/v1/drafts/
quotes/{id}` and `ETag` of the draft's revision. To list
the drafts, with a `status` filter:

```
curl -X GET 'http://localhost:8080/api/v1/drafts/quotes?status=open' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

To read the draft (the wire form, with the payload and
the computed validation):

```
curl -X GET http://localhost:8080/api/v1/drafts/quotes/<draft uuid> \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

To promote the draft (the confirm, the one transaction
that turns it into a quote, with the same revision the
committer read; `If-Match` is the body `revision` form):

```
curl -X POST http://localhost:8080/api/v1/drafts/quotes/<draft uuid>/promote \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 2' \
  -H 'Content-Type: application/json' \
  -d '{"revision": 1}'
```

The response is 201 on a create draft's promotion, 200 on
an edit draft's promotion, with `Location: /api/v1/quotes/
{id}` on a create; the body is the promoted draft with
`promoted` filled. To resolve a quote by document number:

```
curl -X GET http://localhost:8080/api/v1/links/quotes/Q-000123 \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

with the response the `LinkAnswer` body
(`core/api/fragments/links.yaml` `components.schemas.LinkAnswer`,
`core/internal/links/links.go` `Answer`): the `entity`,
`module`, `id`, `number` and the five link slots.

To mint a branch bound key (an admin bearer, with the
`branch_id` that names the seeded branch):

```
curl -X POST http://localhost:8080/api/v1/admin/keys \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 3' \
  -H 'Content-Type: application/json' \
  -d '{"name": "proposer", "scopes": ["quotes:propose", "orders:propose"], "branch_id": "<seeded branch uuid>"}'
```

The response carries the raw key once and the row.
A request from this key naming another branch in
`X-Branch-Id` is 403 `forbidden` and the audit log carries
a `key.branch_refused` row; a request from this key with
`X-Branch-Id` unset or naming the bound branch serves
the bound branch only.

The module's tests live in
`core/internal/drafts/migration_test.go` (the migration
report and the down then up test, `TestMigration103_*`),
`core/internal/drafts/feed_keycheck_internal_test.go` for
the key recheck,
`core/internal/drafts/feed_c5_2a_p2_1_test.go`
(`TestFeedStreamLimits`) for the limits,
`core/internal/drafts/feed_c5_2a_test.go` for the flusher
and full batch,
`core/internal/drafts/access_test.go` (the branch wall
on the draft routes, the X-Branch-Id handling), and
`core/internal/drafts/promotion_test.go` (the promotion
transaction and the refused audit row). The confirm
gate's tests live in
`core/pkg/confirmgate/confirmgate_test.go`
(`TestGateRefusesAgentConfirmsOnAMarkedSession`,
`TestGatePassesReadsDraftWritesAndUnmarkedSessions`,
`TestGateBoundedPath`); the gate's chain position tests
live in `core/internal/app/serve/chain_c5_2a_test.go`
(`TestChainC5_2a_GateInsideAuth`,
`TestChainC5_2a_NilSinkKeepsTheGate`). The machine key
scope tests live in
`core/pkg/middleware/machinekey_scopes_test.go`
(`TestScopeTargetClassTable`,
`TestScopeTargetFailsClosed`); the refusal tests live in
`core/pkg/middleware/machinekey_test.go`
(`TestMachineKeyWithoutScopeRefused`,
`TestMachineKeyWithoutScopeRefusalAudited`,
`TestMachineKeyOnKeyManagementRefused`,
`TestMachineKeyOnMeRoutesRefused`,
`TestMachineKeyOutsideModuleRoutesRefused`,
`TestMachineKeyDirtyPathAuditAction`,
`TestRealKeyRefusalWritesAuditRow`).
