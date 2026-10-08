# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0007: drafts, links and confirm gated scopes

## Status

Proposed for the Gable v1 refactor (item C5-0, the design stop before
cycle 5's agent surface); accepted when the lead merges it after its review.
Item C5-2 builds from this record. Where C5-2 has to differ from it, this
record is changed first, in its own pull request.

It stands on ADR 0001 (the wire contract: revision and `If-Match`, the
transitions route, document numbers, the error table), ADR 0002 (scoped
machine keys, whose vocabulary it extends and two of whose known limits it
takes up), ADR 0003 (the outbox, whose commit ordered position rule it
reuses for the draft change feed), ADR 0004 (web session custody, which
decides how a browser authenticates a stream) and ADR 0005 (the sales core,
whose orders become the second draft kind and whose AR batch receipts are
named here as a later one). It also stands on the actor seam item R1-14
added (`pkg/actor`, `audit_log.actor_kind`, the `X-Acting-As` and
`X-Agent-Tool` headers).

## Context

The refactor inputs (section 4) name five things an agent driven screen
needs from the platform, each learned in the agentic UI experiment:

1. a first class draft resource that an agent tool and a person's screen
   both bind to, with a revision for optimistic concurrency, instead of UI
   key value state;
2. a change feed on drafts so screens stop polling;
3. agent identity on every write: the user's identity plus an `acting-as:
   agent` marker and the tool name, into the audit log;
4. confirm gated writes as a product concept, `quotes:propose` and
   `quotes:commit`, so "the agent proposes, the person confirms" is
   enforced by the server and not by UI convention;
5. one transactional promotion endpoint per draft kind, so the confirm is
   atomic.

Section 5 adds deep links: a canonical record URL in every frontend, human
readable document numbers in those URLs, a link resolution endpoint so
agents never embed route tables, and auth continuity across frontends.

What exists at the base of this item:

- Quotes are on the recipe (R1-15): `revision`, `If-Match`, 409
  `stale_revision`, 428 `precondition_required`, the transitions route, the
  `Q-` number, `quote.created` through the outbox. The quote's own
  lifecycle has a status named `draft`: an editable quote that already has
  a number, appears in lists and reports, and wrote `quote.created`.
- Machine keys (R1-13) carry exact `<module>:read` and `<module>:write`
  scopes, the module being the first path segment under `/api/v1/`, with a
  census test holding the vocabulary against the routes. `GenerateKey`
  stores whatever scope strings it is handed; nothing checks them at mint.
- The actor seam (R1-14) resolves `user`, `key`, `agent` or `anonymous` per
  request and writes it on every `audit_log` row. The agent marker is self
  asserted and grants nothing.
- The desk routes records by UUID (`/quotes/:id`), the API reads quotes by
  UUID only, the front door at `/` opens apps but no records, and the Tauri
  shell turns `gable://quotes/<id>` into `/quotes/<id>` for any plain
  segments.
- The server's `WriteTimeout` is fixed for every route, which would cut any
  long lived response, and nothing in the server streams today.

This record settles each point as a buildable specification.

## Decision

### 1. Words

- A **draft** is the resource this record defines: a proposed document
  that is not yet an entity. It has no document number, appears in no
  entity list, report or aging, and writes no entity event.
- A **quote in status `draft`** is an entity. The two are different things
  and the wire never mixes them: a draft lives under `/api/v1/drafts/...`,
  a quote under `/api/v1/quotes/...`.
- A **draft kind** is named by the module whose entity it proposes, spelled
  exactly as that module's path segment under `/api/v1/` (`quotes`,
  `orders`). The kind and the scope module are the same string, so the
  scope a draft route needs is read off its URL (section 5).
- **Promotion** turns a draft into its entity, or applies it to the entity
  it edits, in one transaction.
- A **confirm gated module** is a module with a registered draft kind.

### 2. The draft resource

#### 2.1 One generic table

One table, `drafts`, holds every kind. The payload is the module's own
request body, stored as JSONB; the module's code decodes and validates it.

| Column | Type and rule | Wire |
|---|---|---|
| `id` | `UUID PRIMARY KEY DEFAULT uuid_generate_v4()` | `id` |
| `module` | `TEXT NOT NULL`; a registered kind (checked in code, see below) | `module` |
| `branch_id` | `UUID NOT NULL REFERENCES locations(id)` | `branch_id` |
| `subject_id` | `UUID NULL`; set on an edit draft (2.4), the entity it edits | `subject_id` |
| `subject_revision` | `BIGINT NULL`; the entity revision the edit is built on; NOT NULL exactly when `subject_id` is | `subject_revision` |
| `status` | `TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','PROMOTED','DISCARDED'))` | `status`: `open`, `promoted`, `discarded` |
| `revision` | `BIGINT NOT NULL DEFAULT 1` | `revision`, and the `ETag` |
| `payload` | `JSONB NOT NULL`; a JSON object | `payload` |
| `created_by_kind`, `created_by_id`, `created_acting_as`, `created_tool` | the actor quadruple of section 6, at create | `created_by` |
| `updated_by_kind`, `updated_by_id`, `updated_acting_as`, `updated_tool` | the actor of the last write | `updated_by` |
| `promoted_entity_id` | `UUID NULL` | `promoted.entity_id` |
| `promoted_number` | `TEXT NULL`; the entity's document number when it has one | `promoted.number` |
| `promoted_at`, `promoted_by_kind`, `promoted_by_id`, `promoted_acting_as`, `promoted_tool` | set once, at promotion | `promoted.at`, `promoted.by` |
| `discarded_at`, `discarded_by_kind`, `discarded_by_id`, `discarded_acting_as`, `discarded_tool` | set at discard, cleared at reopen | `discarded.at`, `discarded.by` |
| `created_at`, `updated_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | `created_at`, `updated_at` |

Constraints: the `*_kind` columns carry the same CHECK vocabulary as
`audit_log.actor_kind` (`user`, `key`, `agent`, `anonymous`); `PROMOTED`
requires `promoted_entity_id` and `promoted_at`; `DISCARDED` requires
`discarded_at`. The module vocabulary is not a CHECK: it lives in code (the
kind registry) and a test holds it against the routes and the scope
vocabulary, the same reasoning as ADR 0002 section 7.

Indexes: `(module, created_at DESC, id DESC)` for the list's ordering;
`(module, subject_id) WHERE status = 'OPEN' AND subject_id IS NOT NULL` for
"open proposals on this quote"; `(promoted_entity_id)` for "which draft
became this quote".

The entity tables are not changed: provenance runs one way, from the draft
(`promoted_entity_id`) to the entity, so no module grows a column for this.

#### 2.2 The wire shape

```json
{
  "id": "6b1d...",
  "module": "quotes",
  "status": "open",
  "revision": 4,
  "branch_id": "0f3a...",
  "subject_id": null,
  "subject_revision": null,
  "payload": { "customer_id": "...", "lines": [ ... ] },
  "validation": {
    "ready": false,
    "problems": [ { "field": "payload.lines[1].quantity", "message": "is required" } ]
  },
  "created_by": { "kind": "agent", "id": "user-subject", "acting_as": "agent", "tool": "quote-builder" },
  "updated_by": { "kind": "user", "id": "user-subject", "acting_as": null, "tool": null },
  "promoted": null,
  "discarded": null,
  "created_at": "...",
  "updated_at": "..."
}
```

- `promoted` is `null` or `{entity_id, number, at, by}`; `discarded` is
  `null` or `{at, by}`. Optional fields are present as `null` (ADR 0001
  section 12).
- `validation` is computed on every read and write from the payload alone
  (no database), by the kind's parser: `ready` is true when the parser
  would accept the payload as a create (or update) request, and `problems`
  is exactly the `details` that parser's 400 would carry, each `field`
  prefixed with `payload.`. It tells both editors what still blocks a
  promotion without refusing the save. Checks that need the database
  (the customer exists, the product exists, the price) run only at
  promotion (section 4).
- The list item (`DraftSummary`) is the same object without `payload` and
  without `validation.problems` (it keeps `validation.ready`).

#### 2.3 Routes

Each kind registers its own literal routes, so the route census lists
exactly the kinds that exist and an unknown kind is the router's 404. For
the quotes kind:

| Route | What |
|---|---|
| `GET /api/v1/drafts/quotes` | list, ADR 0001 envelope; filters `status`, `subject_id`, `created_by_kind`; ordering scope `drafts.created_at_id_desc` |
| `POST /api/v1/drafts/quotes` | create; body `{payload, subject_id?, subject_revision?}`; 201, `Location`, `ETag` |
| `GET /api/v1/drafts/quotes/{id}` | read; `ETag` |
| `PUT /api/v1/drafts/quotes/{id}` | replace the payload; body `{payload, revision?, subject_revision?}`; `If-Match` or body `revision` required |
| `POST /api/v1/drafts/quotes/{id}/transitions` | `{"to": "discarded" or "open", "revision": n}`, ADR 0001 section 11 |
| `POST /api/v1/drafts/quotes/{id}/promote` | promotion, section 4 |
| `GET /api/v1/drafts/quotes/feed` | the change feed, section 3 |

The list orders on `created_at, id`, never on `updated_at`: a keyset on a
column that moves under concurrent writes would repeat or skip rows.

Edits: `PUT` replaces the whole payload, only while `open`; on a promoted or
discarded draft it is 409 `conflict` with blocker `draft_not_open` (the
recipe's edit rule: an edit is not a transition). Transitions: `open` to
`discarded`, `discarded` to `open`; `promoted` is terminal; anything else is
409 `invalid_state_transition`. Promotion is its own route, not a `to:
promoted` transition, because it needs a different scope (section 5) and
the scope must be readable from the URL.

Payload rules, enforced at create and at every `PUT`:

- a JSON object, decoded strictly into the kind's request type: an unknown
  field or a value of the wrong JSON type is a 400 `validation_failed`
  naming `payload.<path>`. This is the structural check only; a missing
  required field is a `validation.problems` entry, not a refusal, because a
  draft is by nature unfinished;
- the payload never carries `revision`: the draft's own revision is the
  precondition, and a payload `revision` is a 400 naming `payload.revision`;
- the branch is fixed at create (the payload's `branch_id` when present,
  otherwise the request's branch context through
  `ResolveBranchForWrite`); a later payload naming another branch is a 400
  naming `payload.branch_id`;
- the size bound is the server's request bound, as for the entity's own
  create (a quote payload may carry an AI parse's original file).

Who may read and write is section 5.

#### 2.4 Create drafts and edit drafts

A draft with `subject_id` null proposes a new entity; its payload is the
kind's create request. A draft with `subject_id` set proposes an edit to an
existing entity (the inputs' "subject entity"); its payload is the kind's
update request, and `subject_revision` is the entity revision the edit was
built on. At create the server reads the subject's current revision: the
client may assert it with `subject_revision` (a mismatch is 409
`stale_revision` naming the subject in a `subject_stale` blocker), or omit
it and take the current one. A `PUT` may move `subject_revision` forward
(a rebase after the entity moved), never back past what the server has. A
subject the caller cannot see is a 404, the same as reading it.

#### 2.5 Revision concurrency, and how an agent and a person co-edit

The draft follows ADR 0001 section 11 exactly: every read and write carries
the revision in the body and the `ETag`; every `PUT`, transition and
promotion needs `If-Match` or the body `revision`; a missing precondition
is 428; a moved revision is 409 `stale_revision`. The write is one database
act: the row is locked `FOR UPDATE` inside the transaction and the revision
checked there (the recipe's service order).

The revision is per document, not per field. The co-editing protocol:

1. Both parties hold revision 4 (each read it, or the feed told them).
2. The person saves: `PUT` with `If-Match: "4"` succeeds, the draft is at 5,
   and the feed carries `{op: updated, revision: 5, by: {kind: user ...}}`.
3. The agent's save, built on 4, arrives: 409 `stale_revision`. Nothing is
   overwritten.
4. The agent re-reads (revision 5), re-applies its own change to the new
   payload, and saves with `If-Match: "5"`.

A screen with unsaved local edits that receives a feed event for a newer
revision re-reads the draft and replays its own pending field changes on
top before its next save; it shows who made the newer revision (`by`, with
the tool name for an agent). That merge is the client's, field by field; the
server stays a whole document compare and swap.

### 3. The draft change feed

#### 3.1 Where the changes come from

Every draft write (create, `PUT`, transition, promotion) inserts one row
into `draft_events` in the same transaction, as its last draft statement:

| Column | Rule |
|---|---|
| `position` | `BIGINT NOT NULL PRIMARY KEY`, assigned by a `BEFORE INSERT` trigger that takes a transaction scoped advisory lock and then draws from `draft_events_position_seq`, exactly the ADR 0003 section 2 mechanism, with its own lock key distinct from the outbox's |
| `draft_id`, `module`, `branch_id` | the draft's |
| `op` | `created`, `updated`, `discarded`, `reopened`, `promoted` |
| `revision`, `status` | after the write |
| `actor_kind`, `actor_id`, `acting_as`, `tool` | who wrote it (section 6) |
| `promoted_entity_id`, `promoted_number` | on `promoted` only |
| `at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` |

Index `(module, position)`. The commit ordered position gives the feed the
same guarantee ADR 0003 proves for the events feed: a reader paging with
`position > cursor` can never skip a row that commits later.

Why a table of its own and not `events_outbox`: draft saves are frequent
(a screen saves as a person types; an agent saves per tool call), they are
not business facts, and every outbox write serializes on the outbox's one
advisory lock. Putting them there would make money writers queue behind
autosaves and flood `GET /api/v1/events` consumers with keystrokes. A
second table with its own lock keeps the two apart. Only the business fact,
the promotion, goes to the outbox (section 4.4).

#### 3.2 The route and the protocol

`GET /api/v1/drafts/<kind>/feed` answers `Content-Type: text/event-stream`
and streams:

```
id: <cursor>
event: draft
data: {"draft_id":"...","module":"quotes","op":"updated","revision":5,"status":"open","branch_id":"...","by":{"kind":"user","id":"...","acting_as":null,"tool":null},"promoted":null,"at":"..."}

```

- **What it carries:** the row's summary above, never the payload. A
  subscriber whose revision is behind `GET`s the draft. Two reasons: the
  feed stays small whatever a payload weighs, and a payload is only ever
  served by the route that applies the payload's read rules.
- **Filters:** `draft_id` (one draft: the co-editing screen) and
  `subject_id` (the proposals on one entity); none means every draft of the
  kind the caller can see (an inbox of proposals). The branch wall applies
  per row, through the same branch context rule the list uses. Any other
  query parameter is a 400 `unsupported_query_parameter` before the stream
  opens.
- **The cursor:** opaque, minted by `httpx` with ordering scope
  `drafts.feed_position`, carried as each event's SSE `id`. The client
  resumes with the `Last-Event-ID` header (what an SSE client sends on
  reconnect) or the `cursor` query parameter; both present and different is
  a 400 naming `cursor`; a malformed one is a 400 naming whichever carried
  it. No cursor means "from now".
- **Opening without a gap:** the first thing the server writes is `event:
  ready` with `id:` the head cursor. A client opens the stream first, then
  reads the draft (or the list): any write after the stream opened arrives
  on the stream, and the client drops events whose `revision` is not newer
  than what it holds. No snapshot ever has to agree with a stream position.
- **Progress without matches:** each read takes the matching rows and the
  head position in one statement (one snapshot), so the connection's own
  position moves past rows its filters exclude. When it has moved without
  sending an event, the next heartbeat is `event: cursor` with the new
  `id:` and no data, so a reconnecting client does not rescan them.
- **Heartbeat:** a comment line `: keepalive` (or the `cursor` event above)
  at an interval the configuration sets below the proxy's read timeout
  (`DRAFT_FEED_HEARTBEAT`), so idle streams survive intermediaries.
- **Retention:** a worker purge deletes `draft_events` rows older than
  `DRAFT_EVENTS_RETENTION` in batches (the ADR 0003 section 6 shape, without
  subscriber cursors to respect: this feed has no in process drain) and
  records the highest purged position in a one row table
  `draft_events_purged`. A client resuming from a cursor at or below it
  gets `event: reset` first: it must re-read what it shows, because changes
  it never saw have aged out. The draft itself is never purged; only its
  change rows are.

#### 3.3 Authentication on a stream

- **Keys and agents** send `Authorization: Bearer` like any request. The
  machine key core validates and scope checks once, at open (section 5).
- **The desk** cannot use the browser's `EventSource`: it sends no custom
  header, and ADR 0004 keeps the token out of cookies until the cutover. The
  desk reads the stream with `fetch` and a `ReadableStream` reader, sending
  the same Bearer header `@gable/auth` sends everywhere, and parses the SSE
  framing itself (a small helper in a shared web package, with reconnect
  and `Last-Event-ID`). A token in the query string was refused: it lands in
  access logs and proxy logs. When the cutover moves the session to an
  HttpOnly cookie (ADR 0004's target), a plain `EventSource` works with no
  server change.
- **Lifetime:** a stream ends at the JWT's `exp`, or at
  `DRAFT_FEED_MAX_LIFETIME`, whichever is first, with `event: reauth` before
  the close; the client reconnects with a fresh token and its last cursor.
  This bounds how long a revoked key or an expired session keeps reading.

#### 3.4 One wake signal per process, one reader per stream, and back pressure

- A feed hub per serve process holds the head position. It learns of new
  rows two ways: the drafts service nudges it after each local commit
  (immediate for writes on the same replica), and it polls `SELECT
  max(position) FROM draft_events` at `DRAFT_FEED_POLL` (for writes on other
  replicas). One query per process, not per stream.
- The hub only signals "the head moved"; it never buffers rows. Each stream
  reads its own rows past its own position, in batches of at most
  `DRAFT_FEED_BATCH`, through the pool, holding no connection between reads.
  A slow client therefore costs nothing while it is slow: it simply reads
  later from the table, where the rows wait durably. Memory per stream is
  one batch; there is no queue to overflow and nothing to drop.
- Each write to the client sets its own deadline through
  `http.ResponseController.SetWriteDeadline` (the server's fixed
  `WriteTimeout` would otherwise cut every stream), at
  `DRAFT_FEED_WRITE_TIMEOUT`. A client that does not drain its socket within
  it is closed; it reconnects from its last cursor and loses nothing.
- Limits: `DRAFT_FEED_MAX_STREAMS_PER_PRINCIPAL` (over it, 429
  `rate_limited`) and `DRAFT_FEED_MAX_STREAMS` per process (over it, 503
  `unavailable`), both answered before the stream opens, in the error
  envelope.
- The response carries `Cache-Control: no-store` and `X-Accel-Buffering: no`
  so the nginx in front does not buffer it. The serve role's graceful
  shutdown cancels the hub's context (through `RegisterOnShutdown`) so open
  streams end and `Shutdown` is not held by connections that are never idle.
- LISTEN and NOTIFY were considered for the wake signal and deferred: they
  need a dedicated connection outside the pool per process and do not
  survive a transaction mode pooler. The hub hides the wake source, so
  adding NOTIFY later changes the hub only.

### 4. Promotion

#### 4.1 The route

`POST /api/v1/drafts/<kind>/{id}/promote`, body `{"revision": n}` or
`If-Match`, and an `Idempotency-Key` like every mutating request. The body
carries nothing else: what is committed is exactly the revision the
committer read. This is what makes the confirm meaningful: a person who
reviewed revision 7 and confirms it cannot commit revision 8, which an agent
saved after the person looked; that confirm is 409 `stale_revision`.

#### 4.2 One transaction, in this order

1. Lock the draft row `FOR UPDATE`. Not found, another kind's draft, or a
   branch the caller cannot see: 404.
2. Status: `promoted` is 409 `invalid_state_transition` with blocker
   `already_promoted` whose message names the entity's id and number;
   `discarded` is 409 `invalid_state_transition` with blocker
   `draft_discarded`. Status is checked before the revision so a retry of a
   promotion that already happened is told so, whatever revision it sends.
3. Revision: 428 or 409 `stale_revision`, as section 2.5.
4. Parse the payload with the kind's full parser (the module's create or
   update parsing, the same code the entity route runs). A failure is 400
   `validation_failed` with every field, each path prefixed `payload.`.
5. Call the kind's promoter inside the transaction (4.3). It runs the
   module's own create or update: references checked, document priced,
   number minted, rows written, the module's lock order kept. It writes no
   event; it returns the entity's id, number and revision and the event(s)
   the module would have written.
6. Update the draft: `status = 'PROMOTED'`, `revision = revision + 1`, the
   `promoted_*` columns, the `updated_by_*` actor.
7. Write the audit row `draft.promoted` (section 6).
8. Insert the `draft_events` row (`op: promoted`).
9. Write the outbox events, last: the module's own event(s) first
   (`quote.created`, or the edit's event), then `draft.promoted`.

Lock order: the draft row, then the module's own rows in the module's own
order (ADR 0005 section 11 for orders), then the `draft_events` advisory
lock, then the outbox advisory lock. Nothing takes the outbox lock before
the draft events lock, and after each insert only the next insert runs, so
ADR 0003's last statement rule holds for both feeds. A draft `PUT` takes
only the draft row and the draft events lock; an entity route takes only its
module's rows and the outbox lock; neither can close a cycle with a
promotion.

The answer: a create draft is 201 with `Location` the entity's URL
(`/api/v1/quotes/{id}`); an edit draft is 200. The body is the draft
(status `promoted`, its new revision and `ETag`, `promoted` filled with the
entity's id and number), so every draft route returns one type; a client
that wants the entity follows `Location` or `promoted.entity_id`.

#### 4.3 The kind interface

`core/internal/drafts` defines it; each module implements it in its own
package; `serve.go` registers the implementations. `drafts` never imports a
module.

```go
type Kind interface {
    Module() string                 // "quotes": the path segment and the scope module
    Entity() string                 // "quote": the entity name in events and links
    Roles() []string                // the user roles the module's routes admit
    // Structural decode for draft writes: unknown fields and wrong types are
    // the error; missing required fields come back as problems.
    Check(payload json.RawMessage, edit bool) (problems []httpx.FieldError, err error)
    // The subject's current revision, for edit drafts; ErrNotFound when the
    // caller cannot see it.
    SubjectRevision(ctx context.Context, id uuid.UUID) (int64, error)
    // Promote runs inside the caller's transaction and writes no event.
    Promote(ctx context.Context, d Draft) (Promoted, []outbox.Event, error)
}
```

For quotes the module splits `Service.Create` and `Service.Update` into an
in transaction core that returns its event, and the public methods become
that core plus the event write; the entity routes behave byte for byte as
before (the goldens are the test).

#### 4.4 Idempotency and events

- A retry with the same `Idempotency-Key` replays the stored 201 or 200
  (ADR 0001 section 9). A retry without a key meets `already_promoted`, so
  no draft promotes twice either way.
- The outbox gets `draft.promoted`, entity `draft`, data `{module, entity,
  entity_id, number, revision, proposed_by, committed_by}` where each actor
  is the section 6 object: an agent consumer of `GET /api/v1/events` sees
  who proposed and who committed without reading the audit log.
- The module's own event is unchanged (`quote.created` with its usual data),
  so consumers of entity events need not know drafts exist.

#### 4.5 A failed promotion

Every failure rolls the whole transaction back: no entity row, no number
kept (a gapped sequence loses the number, as ADR 0001 section 8 accepts; a
gapless counter rolls back with it), no event, and the draft exactly as it
was, still `open` at the same revision, so the parties fix the payload and
the committer confirms again. Nothing about the failure is written into the
draft: a write that changed the draft without moving its revision would
break the revision contract, and one that moved it would make the
committer's next confirm stale for no edit.

After the rollback, outside it, the drafts service writes one audit row
`draft.promotion_refused` (best effort, the ADR 0002 refusal pattern) with
the HTTP status, the error code and the draft revision, so the trail shows
who tried to commit and why it did not happen. The response carries the
module's own error: a 400 with `payload.`-prefixed fields, or the module's
409 blockers unchanged (`quote_not_draft` for an edit draft whose quote has
been sent, `subject_stale` when the subject moved past `subject_revision`).

### 5. Confirm gated scopes

#### 5.1 The vocabulary

ADR 0002's scope grammar `<module>:<verb>` gains two verbs, so the verbs are
`read`, `write`, `propose`, `commit`. `propose` and `commit` are grantable
only on confirm gated modules (those with a registered kind). Matching stays
exact, with no wildcard and no implication between verbs; the table below is
the whole policy, and a grant reads back as written.

| Route class | Path shape | Admitted scopes for module `m` |
|---|---|---|
| entity read | `GET`, `HEAD` under `/api/v1/m/...` | `m:read` |
| entity write | any other method under `/api/v1/m/...` | `m:write` |
| draft read | `GET` under `/api/v1/drafts/m/...`, the feed included | `m:read`, `m:propose`, `m:commit` |
| draft write | `POST /api/v1/drafts/m`, `PUT .../{id}`, `POST .../{id}/transitions` | `m:propose`, `m:commit` |
| promotion | `POST /api/v1/drafts/m/{id}/promote` | `m:commit` |
| link | `GET /api/v1/links/m/{id}` | `m:read` |
| draft link | `GET /api/v1/links/drafts/m/{id}` | `m:read`, `m:propose`, `m:commit` |

So a `quotes:propose` key creates, edits, discards and reads quote drafts
and nothing else: it is refused promotion (403, the refused scope
`quotes:commit`), every quote entity write (403, `quotes:write`), and even
quote entity reads unless it also holds `quotes:read`. `quotes:commit`
includes the draft writes because a committer that may cause the entity
write may also correct the draft first; a deployment that wants a strict
two party rule grants the agent `propose` and the committing service
`commit` and reads both actors on the promotion's audit row. `m:write`
admits no draft route: an existing write key's reach is unchanged, and a key
meant to work through drafts is granted for that explicitly.

#### 5.2 Mapping a path to its scope

ADR 0002 derives the module from the first segment. Two segments become
delegating: `drafts` and `links`. Under them the module is the next segment
(`/api/v1/drafts/quotes/...` is module `quotes`, class by method and the
last literal segment; `/api/v1/links/drafts/quotes/...` is a draft link of
`quotes`). `ModuleForPath` is replaced by `ScopeTarget(method, path)
(module, class, ok)` and `RequiredScope` by `AdmittedScopes(module, class)`;
the refusal audit row records the first admitted scope as the refused one.
`drafts` and `links` are not modules: they never appear in the vocabulary
and cannot be granted.

The census test extends in both directions: every route under
`/api/v1/drafts/` and `/api/v1/links/` must resolve through `ScopeTarget` to
a vocabulary module; every draft route's module must be a registered kind;
every registered kind must have its seven routes; and every module allowed
`propose` and `commit` must be a registered kind.

#### 5.3 Checked at mint

`GenerateKey` (and `POST /api/v1/admin/keys`) refuses a scope that is not
`<vocabulary module>:<verb>`, or that grants `propose` or `commit` on a
module with no draft kind: 400 `validation_failed` naming `scopes[i]`. A key
could already be minted with a typo that silently granted nothing; with four
verbs the typo space is wider, and the mint is where the operator can still
fix it. No stored key changes: the table holds none outside the grammar.

#### 5.4 People, and agents acting with a person's session

A user's session reaches draft routes through the kind's role guard (the
module's own roles, `admin`, `owner`, `sales` for quotes) composed with the
branch middleware, exactly as the module's entity routes. A person with the
role proposes, edits and promotes.

An agent that acts on a person's behalf with the person's JWT and the
R1-14 marker (`X-Acting-As: agent`) is held to propose authority on every
confirm gated module, server side: on such a request the confirm gate
refuses the promotion route and every entity write route of a gated module
with 403 `forbidden`, message "an agent proposes through drafts; a person
confirms", and writes the audit row `agent.commit_refused` with the method,
the bounded path and the tool. Reads and draft writes pass. The gate sits
in a small package of its own (`pkg/confirmgate`, mounted inside auth so it
sees both the claims and the marker); `pkg/actor` keeps granting nothing.

The honest limit: the marker is self asserted, so an agent holding a
person's token that leaves the marker off is, to the server, that person.
The gate stops an honest agent and every agent framework that sets the
marker by construction; it cannot stop a dishonest one. The enforceable
gate is a key: an agent that must never commit is given a `propose` key,
not a session. The full remedy is a delegated token whose claims say "agent
X acting for user Y" (a token exchange `act` claim issued by the bridge),
which belongs to the cutover's identity work and is listed in Known limits.

#### 5.5 Branch bound keys (ADR 0002's first known limit)

`api_keys` gains `branch_id UUID NULL REFERENCES locations(id)`, set at mint
(`branch_id` in the mint body) and never edited (revoke and mint again). A
null branch is today's behaviour. A bound key:

- with no `X-Branch-Id` is pinned to its branch: the machine key core puts
  that branch into the request's branch context before the module runs, so
  lists, reads, drafts, the feed and links see that branch only, and writes
  resolve to it;
- naming another branch in `X-Branch-Id` is 403 `forbidden`, audited as
  `key.branch_refused`.

One branch per key, not a set: the repositories' branch idiom filters on one
branch or none, and a set would need a second idiom in every module. A
dealer that needs a key for two branches mints two.

ADR 0002's second known limit (finer `admin` and `users` scopes) is not
taken up here. It is not a confirm gate question: no admin act is a draft.
It belongs with the tech admin module's conversion in C5-1, on its own
record.

### 6. Agent identity, in all of it

The actor object on the wire, in draft events, in outbox data and in the
draft row's quadruples is what `pkg/actor.FromContext` resolves for the
request, unchanged:

```json
{ "kind": "user | key | agent | anonymous", "id": "...", "acting_as": "agent" | null, "tool": "quote-builder" | null }
```

For an agent acting with a person's session, `id` is the person's subject:
the inputs' "the user's identity plus the marker and the tool name". For a
keyed agent, `id` is the key's id (a key has no user, ADR 0002).

Every draft act writes one `audit_log` row through `pkg/audit` in the act's
transaction, `entity_type` `draft`, `entity_id` the draft's id, the actor
columns from the request:

| Action | `changes` |
|---|---|
| `draft.created` | `module`, `revision`, `subject_id`, `payload_sha256` |
| `draft.updated` | `module`, `revision`, `payload_sha256` |
| `draft.discarded`, `draft.reopened` | `module`, `revision` |
| `draft.promoted` | `module`, `revision`, `payload_sha256`, `entity_type`, `entity_id`, `number`, `proposers` |
| `draft.promotion_refused` | `module`, `revision`, `status`, `code` (written after the rollback, 4.5) |
| `agent.commit_refused` | `method`, `path` (bounded), `module`; entity the draft or the entity the path names, or `entity_type` the module with the nil UUID on a create |
| `key.branch_refused` | `branch_id`, `method`, `path` (bounded); entity `api_key` and the key's id, as ADR 0002's refusal rows |

`payload_sha256` is the hash of the stored payload's canonical bytes, so an
auditor ties the committed entity to the exact revision that was confirmed;
the promoted draft keeps that payload frozen. `proposers` is the distinct
actors of the draft's `draft.created` and `draft.updated` rows, read from
`audit_log` inside the promotion's transaction. The committer is the row's
own actor. "Who proposed and who committed" is therefore one audit row.

### 7. Document numbers in record URLs

- Every `GET` route whose path has `{id}` for a numbered entity accepts the
  document number in that slot as well as the UUID: `GET
  /api/v1/quotes/Q-000123` answers exactly what `GET /api/v1/quotes/<uuid>`
  does (no redirect). The slot is parsed as a UUID first, then against the
  entity's number pattern (`^Q-[0-9]{6,}$` for quotes, from the entity's
  prefix and pad); a well formed number of another entity, or anything
  else, is a 400 naming `id`; a number that names no visible row is a 404.
- Writes take the UUID only. The idempotency fingerprint includes the path,
  so two spellings of one target would give one write two fingerprints, and
  a retry spelled the other way would run twice.
- The canonical record URL of a numbered entity uses the number:
  `/quotes/Q-000123` in the desk. The desk route `/quotes/:id` accepts
  either; when it was opened by UUID it replaces the address with the
  number form (`history.replaceState`) once the document loads. Entities
  with no number (customers, products) keep the UUID.
- C5-2 does this for quotes, and for orders and invoices if cycle 2 has not
  already (their prefixes are ADR 0005's `SO` and `IN`); each later
  numbered entity does it in its own conversion, a step added to the
  module recipe.

### 8. The link resolver

`GET /api/v1/links/<module>/{id}` and `GET /api/v1/links/drafts/<module>/{id}`,
registered per entity like the draft routes. `{id}` is a UUID or a document
number (section 7). The answer:

```json
{
  "entity": "quote",
  "module": "quotes",
  "id": "4c2e...",
  "number": "Q-000123",
  "links": {
    "desk": "https://gable.example.com/quotes/Q-000123",
    "front_door": "https://gable.example.com/?open=quotes/Q-000123",
    "portal": null,
    "app": "gable://quotes/Q-000123"
  }
}
```

- **Which entities in C5-2:** quotes, orders, invoices, customers
  (`/accounts/{id}` in the desk), products (`/inventory/{id}`), and quote
  drafts (`/quotes/drafts/{id}`); each later module adds its row when it
  converts. The record must exist and be visible to the caller (branch
  walls, roles, scopes), else 404: a resolver that answered for invisible
  records would confirm their existence.
- **The record segment** is the number when the entity has one, otherwise
  the UUID; draft links use the draft's UUID.
- **desk:** the desk route. Absolute when `GABLE_PUBLIC_URL` is configured,
  otherwise a path relative to the deployment's origin (the desk, the door
  and the API share one origin).
- **front_door:** `/?open=<record path>`. The door signs the person in if
  needed and then opens the record, which is the inputs' auth continuity: a
  link handed to someone with no session lands on sign in and then on the
  record. In v1 the door opens it in the desk (the only app). After cycle
  6's split the door maps the record path to the role micro app that serves
  it for this person, and the link does not change. The door validates
  `open` with the same plain segment rule the Tauri shell applies (one
  shared rule, tested in both), so the parameter can never steer the
  browser off the origin.
- **app:** `gable://<record path>`. The shell's existing parser already
  admits `Q-000123` segments and maps the link to the same desk path.
- **portal:** present and `null` until the portal has a record screen for
  the entity. Today the portal has list screens only, served on its own
  session and its own API; when C5-1 or cycle 6 adds a portal record
  screen, its row gains a pattern and the field is the URL a staff member
  would hand the customer. The resolver on `/api/v1` never asserts that a
  portal user may see the record; a portal side resolver
  (`/api/portal/v1/links/...`, limited to the customer's own records) is
  the portal conversion's to add, under this record's shape.
- **One route table:** the Go registry of entity, number pattern and
  per frontend path patterns is the only copy. A generator writes it to
  `core/api/links.json` (a generated, checked in file like the census), a
  desk test asserts every desk pattern in it matches a route in
  `routes.ts`, a door test that every record path opens, and a shell test
  that every `app` sample parses. Agents never ship a route table again.

### 9. Migrations, in order

One numbered migration, `drafts_links_scopes` (the next free number when
C5-2 merges `refactor/v1`), and its down file. Every step idempotent.

1. `drafts`, its CHECKs and its three indexes (2.1).
2. `draft_events_position_seq`, `draft_events` with no position default,
   the `BEFORE INSERT` trigger function taking the draft events advisory
   lock key then drawing the position (the 089 trigger's shape with its own
   key), the `(module, position)` index.
3. `draft_events_purged (id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
   through_position BIGINT NOT NULL DEFAULT 0)` with its one row.
4. `api_keys.branch_id UUID NULL REFERENCES locations(id)`.

No backfill: every table is new and the new column is nullable with today's
meaning. No entity table changes. The down file drops in reverse order.
Applied to an empty and to a seeded database, as the recipe asks; the seed
adds `drafts` and `draft_events` to its transactional reset list.

### 10. The kinds: quotes first, orders second

**Quotes (C5-2).** Module `quotes`, entity `quote`, roles `admin`, `owner`,
`sales`. A create draft's payload is the quote create request
(`quote.Request`, no `revision`); an edit draft's is the update request,
with the update's field refusals (`source`, `parse_map` and the rest, per
the recipe's table) applying at the structural check. Promotion of a create
draft runs the create core: the quote is born in status `draft` with its
`Q-` number and `quote.created`; sending it is a later transition on the
quote, by whoever holds `quotes:write` or a person's session. Promotion of
an edit draft runs the update core with `subject_revision` as the
precondition, so a quote already sent refuses it (`quote_not_draft`) and a
quote edited since refuses it (`subject_stale`). The desk gains
`/quotes/drafts` (open proposals) and `/quotes/drafts/:id` (the quote
builder bound to a draft, following the feed, with a Confirm action that
promotes), each against its visual reference, the quote builder screen, with
the visual review the UI rule requires.

**Orders (after C2-2).** Once cycle 2 lands the order create and draft edit
on the recipe (ADR 0005 sections 5.2 and 5.7), the order kind registers the
same way: module `orders`, roles the order routes' roles, create payload the
order create request, promotion creating the order in status `draft` (the
confirm, with its credit, PO and contact checks, stays an order transition
needing `orders:write` or a session), and edit drafts on draft orders. It is
a kind implementation and its routes, 6 to 10 dev hour equivalents, with no
change to the drafts package.

**Later kinds** name themselves here so their shape is fixed: the AR batch
receipt (ADR 0005 section 1's boundary row), whose promotion posts each row
through the AR core's `RecordPayment` in the one transaction; purchase
orders after cycle 4.

### 11. What C5-2 builds and tests

Size: 46 to 72 dev hour equivalents (the plan's R5-2 band, 42 to 68, plus
the desk draft screens and branch bound keys, which this record places in
it).

| Piece | Builds | Tests |
|---|---|---|
| Migration (9) | the four steps and the down file | applies on empty and seeded databases; down then up is clean |
| Drafts core (2) | `internal/drafts`: repository, service, handler, kind registry; quote kind | wire tests per the recipe set: create shape, `validation`, payload refusals, list and cursor, 428, 409 `stale_revision` with strong and weak `If-Match`, `draft_not_open`, transitions, idempotent create; transaction proofs: three writers at pool size 4 on one revision have one winner; the gated saturation test for create, `PUT`, transition and promotion |
| Feed (3) | `draft_events`, the hub, the SSE handler, the purge in the worker, the web stream helper | a stream receives a `PUT` made on another connection with the right `by`; resume by `Last-Event-ID` and by `cursor` serves each row once; two transactions with the lower position committing last are both served (the ADR 0003 case); a filtered stream advances its cursor; a reader that never reads holds one batch of memory and is closed at its write deadline; `reset` after a purge; the stream ends at token `exp`; shutdown ends open streams |
| Promotion (4) | the route, the quote kind's in transaction create and update cores | one transaction: a failing outbox write leaves no quote, no number reuse, the draft unchanged; two promoters racing one draft: one 201, one 409; a `PUT` racing a promotion: exactly one wins; `already_promoted` on a keyless retry, replay on a keyed one; `payload.` paths on a 400; `subject_stale`; events in order (`quote.created`, `draft.promoted`); the `draft.promotion_refused` row after a failure; quote goldens unchanged |
| Scopes (5) | `ScopeTarget`, `AdmittedScopes`, the census extension, mint validation, the confirm gate, branch bound keys | the class table row by row for a propose, a commit, a read and a write key; mint refusals naming `scopes[i]`; the agent marker refused promotion and entity writes on quotes and admitted draft writes; a bound key pinned and refused another branch, with its audit row |
| Identity (6) | actor columns on drafts and events, the audit actions | each act's audit row with its actor; `proposers` and the committer on the promotion row; outbox data's `proposed_by` and `committed_by` |
| Numbers and links (7, 8) | number reads on quotes (orders and invoices if not done), the desk's canonical URL, the resolver and its registry, `links.json`, the door's `open` handling, the shell test rows | `GET /quotes/Q-000123`; wrong prefix 400; invisible 404; the resolver for each entity; desk, door and shell drift tests against `links.json` |
| Contract | fragments for every new route, `CONTRACT-CHANGES.md` rows (quote reads by number; agent marked writes refused on gated modules; the scope grammar; `branch_id` on keys), census regenerated | `make contract` green |
| Desk (10) | `/quotes/drafts`, `/quotes/drafts/:id` | Playwright: a person edits a draft while an API client acting as an agent edits it, sees the agent's revision arrive, and confirms; visual review |

The cycle 5 exit test lines this item answers:

| Exit test line | Proven by |
|---|---|
| no route outside the contract | every draft, feed, promotion and link route in a fragment; the census and route coverage gates |
| an agent and a person edit one draft and the stale write is refused with 409 | the wire test: a person's session and an agent (once a `quotes:propose` key, once the person's session with `X-Acting-As: agent`) both on revision n; the first save wins, the second is 409 `stale_revision`, the feed delivered the first save to the other party with its actor, both audit rows carry their actors; and the Playwright run above |
| a link resolves to both frontends | `GET /api/v1/links/quotes/Q-000123` answers the desk and the front door links (and the app link); Playwright opens the desk link onto the quote, and opens the door link signed out, signs in, and lands on the quote; the shell test opens the app link's route |
| a `quotes:propose` key cannot commit | the key creates and edits a draft (201, 200), is refused the promotion (403, `key.scope_refused` with `quotes:commit`) and `POST`, `PUT` and transitions on quotes (403, `quotes:write`); a `quotes:commit` key then promotes the same revision (201, a `Q-` quote, `quote.created` then `draft.promoted`, the audit row naming the propose key as proposer and the commit key as committer) |

## Alternatives considered

**One table per kind instead of one generic table.** Typed columns would let
the database check a draft's fields and let reports read drafts with plain
SQL. Rejected: a draft is unfinished by definition, so every typed column
would be nullable and check almost nothing; each kind would repeat the
migration, the revision plumbing, the feed and the promotion; and the
module's own parser is already the authority on its request shape. One
table with the module's parser on top keeps one implementation and one feed.

**Repurposing the quote's own `draft` status as the draft.** The quote
already has an editable status, a revision and a 409. Rejected: a quote in
status `draft` has a number, sits in lists, aging and analytics, and wrote
`quote.created`, so a propose key that could create one would already have
written the entity the confirm gate exists to hold back; and the approach
does not generalize to kinds whose first status is not editable (an AR
batch). The name collision is handled by the URL space and section 1, not
by renaming either.

**Naming the resource `workspace-drafts` or `proposals`.** The inputs say
`workspace-drafts`; `proposals` avoids the collision with the quote status.
Rejected for `drafts`, which the plan, the exit tests and ADR 0005 already
use; the two never share a URL.

**A delegating `/api/v1/drafts/{kind}/{id}` route with a kind parameter.**
One registration for every kind. Rejected for literal routes per kind: the
census then lists exactly which kinds exist, the scope test can map each
route to its module, and an unknown kind is the router's 404 instead of a
handler branch. Nesting under the module (`/api/v1/quotes/drafts/{id}`) was
refused because it collides on the standard mux with `/api/v1/quotes/{id}/...`
patterns (neither pattern is more specific, which panics at registration),
and because one feed route per kind under one prefix is simpler to proxy.

**Field level merging or operational transforms for co-editing.** They let
an agent and a person write different fields at once with no 409. Rejected:
ADR 0001 section 11 fixes whole document revisions for every document, the
exit test asks for the 409, and the merge a screen needs is small enough to
do in the client on a feed event. A JSON merge patch route was also
considered for smaller writes and deferred: it would still 409 on any
concurrent change, so it saves bytes and not conflicts.

**Draft changes on `events_outbox`.** One feed for everything. Rejected in
section 3.1: autosaves would serialize with money writers on the outbox's
one lock and flood the business feed.

**LISTEN and NOTIFY, or an in memory fan out of rows.** Rejected for the
wake signal plus per stream table reads (3.4): NOTIFY needs a connection
outside the pool and breaks behind a transaction pooler; an in memory fan
out needs a buffer per stream that a slow client fills, forcing a choice
between unbounded memory and dropped events. Reading from the table makes
back pressure free.

**WebSockets instead of SSE.** Two way, and a browser can set no headers on
either. Rejected: the feed is one way, SSE is plain HTTP through the
existing nginx and middleware, and resumption by `Last-Event-ID` is built
into the format.

**A token in the stream's query string, or a short lived stream ticket.**
Both make `EventSource` usable today. The query token was refused (it lands
in logs). A ticket route (mint a single use stream ticket, open with it) is
sound but adds a credential type with its own store and expiry for a
problem the cutover's cookie removes; `fetch` with a stream reader needs
neither.

**Promotion as a transition (`to: promoted`).** One route shape for every
status change. Rejected: the scope differs by target, and ADR 0002's rule is
that the scope is read from the URL.

**`write` implying `propose` and `commit`, or `commit` implying nothing.**
Implication would spare an operator a second grant. Rejected for an exact
table: ADR 0002 refused wildcards because a grant must read back as written,
and an implication is a wildcard by another name. `commit` admitting the
draft writes is a row of the table, not an implication, and section 5.1
says why.

**Enforcing the agent gate by the marker alone, with no keys, or by keys
alone, with no marker gate.** The marker alone is self asserted; keys alone
leave the inputs' case (an agent acting with the person's session) gated
only by convention, which is what the inputs reject. Both are built, with
the marker's limit stated.

**A set of branches per key.** Rejected for one branch per key (5.5).

**Storing a promotion failure on the draft.** Rejected in 4.5: it would
either break the revision contract or make the next confirm stale.

**Absolute links only, or relative only.** Absolute needs the deployment to
know its public URL, which a local stack does not; relative is useless to an
agent handing a link to a person. The resolver gives absolute when
`GABLE_PUBLIC_URL` is set and relative otherwise.

**Reads and writes both by number.** Rejected for writes (section 7): one
write must have one fingerprint.

## Consequences

- Agents and people bind to one server side document per proposal, see each
  other's saves as they happen, and cannot overwrite each other silently.
- "The agent proposes, the person confirms" is a server rule: enforced
  exactly for keys, and for agents acting with a person's session wherever
  the agent declares itself.
- A confirm commits exactly the revision the person read, in one
  transaction with the entity, its number, its events and an audit row that
  names every proposer and the committer.
- Every numbered record has one canonical, scannable URL, and agents ask the
  resolver instead of carrying route tables; the door link survives cycle
  6's split unchanged.
- The scope vocabulary has four verbs and two delegating segments; the
  census test, not review, keeps them aligned with the routes and the kinds.
- Keys can be held to one branch.
- Each new confirm gated module is a kind implementation and its routes; the
  drafts package, the feed and the scopes do not change.
- The serve role gains long lived connections: limits, write deadlines and
  shutdown handling are part of it, and the nginx in front must not buffer
  `text/event-stream` responses.

## Known limits

- **The agent marker is self asserted** (5.4). An agent holding a person's
  token that omits the marker is that person to the server. Narrowed by a
  delegated token carrying the agent's identity inside the person's
  (an `act` claim from the bridge), at the cutover's identity work.
- **A revoked key keeps an open stream** until `DRAFT_FEED_MAX_LIFETIME`
  ends it (3.3); its writes are refused at once.
- **Cross replica feed latency is the poll interval** (3.4), until a NOTIFY
  wake source is added behind the hub.
- **Open drafts never expire.** An abandoned proposal stays `open` until
  someone discards it; an expiry sweep is a later decision, made on use.
- **The portal has no record links yet** (8): its field is `null` and its
  own resolver arrives with the portal conversion.
- **Finer `admin` and `users` scopes** (ADR 0002's second limit) stay open,
  assigned to the tech admin conversion's record (5.5).
- **A large draft's retried `PUT` may run twice:** a response over the
  idempotency store's cap is served but not stored (ADR 0001 section 9), so
  its keyed retry executes again and meets 409 `stale_revision`, which is
  the safe outcome but not a replay.
