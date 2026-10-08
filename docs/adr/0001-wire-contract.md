# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0001: the wire contract for every route

## Status

Accepted for the Gable v1 refactor (item R1-6). This ADR is the design stop the
cycle plan calls review point 2: the money rule and the wire conventions are
settled here before any module converts. The platform package
`backend/internal/platform/httpx` (later `core/internal/platform/httpx`) is its
implementation; no handler uses it yet.

## Context

Gable's HTTP surface grew module by module, and every module chose its own
wire shapes. The refactor inputs (the agentic UI experiment's requirement
list) document what this costs outside the repository: every client had to
ship a normalizer layer, and the normalizers hid live failures (mixed float
and integer money, `null` for empty lists, silent filter no-ops, a 500 for a
missing unit of measure). Inside the repository the same inconsistencies show
up as five different list envelope spellings across the modules.

The HH reference (read for pattern only; nothing is copied from it) solved
the first half of this for another product: one cursor envelope, cents,
document numbers, contract first with drift gates. Its rules differ from the
inputs in places (it forbids `total` in the envelope, its error body is a
bare string, its enums are uppercase). Cycle 1 needs one rule set that a
Gable module can be converted onto once, without a second breaking pass.

This ADR is that rule set. It governs every JSON route of the product: all
of `/api/v1/*` and `/api/portal/v1/*`. Two seams are outside it and keep
their own published contracts: the AI load management integration routes
under `/api/integration/*`, and the agent-to-agent JWS routes under
`/api/v1/a2a/*`. Health and metrics endpoints are not resource routes and
are exempt.

## Decision

### 1. The list envelope

Every route that returns a collection returns:

```json
{
  "items": [ ... ],
  "next_cursor": "b3M6..." ,
  "limit": 50
}
```

- `items` is always a JSON array, never `null`. An empty page is `[]`. A
  client never branches on null versus array.
- `next_cursor` is a string when another page exists under the current
  filters and ordering, and `null` when the page just returned is the last.
  The client passes it back verbatim as `?cursor=`. It is opaque: clients
  must not parse, build, or infer meaning from it.
- `limit` is the effective page size the server used (the requested limit,
  which is also bounded as below), echoed so a client can size its buffers.
- `total` appears only when the client asks for it: `?include=total`. The
  response then carries `"total": <integer>`, the count of rows matching the
  request's filters. Counting a filtered set is a second query and on large
  tables a costly one, which is why it is opt-in and not part of every page.
  The count is a snapshot of the moment the count query ran; under concurrent
  writes it can disagree with the pages that follow it.

Offset pagination (`offset`, `offset` echoes, bare `data` arrays, `null`
collections) does not survive module conversion: cursor pagination replaces
it on every list. There is no `/api/v2`: see the in place rule.

### 2. Cursors

A cursor is minted from the last row of a page and consumed by the next
request. The encoding is the base64url (unpadded) form of a compact JSON
object:

```json
{"v": 1, "o": "quotes.created_at", "k": ["<last row sort value>", "<last row id>"]}
```

- `v` is the format version. A cursor this package did not mint in a version
  it knows is refused.
- `o` is the ordering scope: the converting module's stable name for that
  list's ordering (the sort columns plus tie-breaker). A cursor resumed
  against a different ordering scope is refused with 400, because silently
  resuming a different sort repeats or drops rows the client already acted
  on.
- `k` is the keyset tuple: the ordering column values of the last row, in
  the ordering's column order, each as a string.

The cursor is keyset friendly by construction: the handler resumes with a
`WHERE (sort columns) > (tuple)` style predicate combined with the same
filters, which is index friendly and stable under concurrent inserts, unlike
offsets.

A cursor that is absent means first page. A cursor that is present and
malformed is a 400, never silently treated as the first page: a client
resuming from a corrupted token must learn that, because restarting from the
top of a list repeats rows it may already have acted on. Malformed means:
not decodable base64url in canonical form, not well-formed JSON of exactly
the three fields, a version other than 1, a missing or non-matching ordering
scope, an empty keyset, keyset parts that are empty or carry control
characters, or a decoded size over the package's bound.

The cursor is strictly validated, not signed. An HMAC was considered and
rejected for now: it would couple every core instance to a shared secret
with a rotation story that invalidates in-flight cursors, and it buys little
here because a forged cursor can only seek within the same ordering scope
and the same server-side filters; it is a pagination token, not a
credential. If cursors ever carry more authority than a row position, that
decision is reopened.

`limit` is strict too: an integer between 1 and 200, default 50. A malformed
or out-of-range limit is a 400 naming the field. This deliberately does not
copy the HH reference, whose clamped fail-quiet limit is recorded there as a
pinned divergence; Gable's posture on the wire is uniform: if the server will
not honor what the client sent, the server says so.

### 3. The error envelope

Every error response, from every route, is:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "line 1: uom is required",
    "details": [
      {"field": "lines[0].uom", "message": "is required"}
    ]
  },
  "meta": {"request_id": "..."}
}
```

- `code` is a stable, lowercase, snake_case machine code from the table
  below. Clients branch on it, never on the message text.
- `message` is the handler's own message, in full. Today's
  `backend/pkg/httputil` `RespondError`
  (`backend/pkg/httputil/response.go`, the `RespondError` function) accepts a
  handler message but writes only `genericMessage(code)` into the body: the
  client sees "Bad Request" while the specific cause ("line 1: uom is
  required") goes only to the server log. That drop is what this rule fixes.
- `details`, when present, carries one entry per offending field: `field` is
  the field's path (for body fields, its JSON path; for query parameters, the
  parameter name; for the cursor, `cursor`), `message` says what is wrong
  with it. `details` is omitted (or empty) when the error is not about
  fields.
- `meta.request_id` is the request id assigned by the request id middleware
  (the same value the `X-Request-ID` response header carries), so a client
  can quote one identifier in a support conversation for any response, good
  or bad.

The code table:

| HTTP | code | meaning |
|---|---|---|
| 400 | `bad_request` | the request cannot be parsed or consumed: malformed JSON body, malformed cursor, unparseable parameter value |
| 400 | `validation_failed` | one or more fields failed validation; `details` names each field |
| 400 | `unsupported_query_parameter` | the route does not accept a query parameter the request carries; `details` names it |
| 401 | `unauthorized` | no or invalid credentials |
| 403 | `forbidden` | credentials lack the scope or role the route requires |
| 404 | `not_found` | the addressed resource does not exist (or is not visible to this caller) |
| 409 | `conflict` | the request conflicts with current state: a stale revision, a duplicate unique value, an idempotency key reuse with a different body |
| 429 | `rate_limited` | too many requests |
| 500 | `internal_error` | an unexpected server fault |

One carve-out keeps the message rule safe: for status 500 the body's
`message` is the fixed string "internal error" and `details` is omitted. A
handler message on a 500 is written by a person debugging an unexpected
fault and can name tables, files, or drivers; it goes to the log with the
request id, not to the client. The handler's message is kept verbatim for
every 4xx, where the message is written for the client by construction.

Plain text error bodies, `{error}` string bodies, and per-module error
shapes do not survive module conversion.

### 4. Field validation

A request body or parameter set that fails validation is a single 400 with
`code` `validation_failed` and every offending field in `details`, collected
in one pass: a client fixing a form gets the whole list, not one error per
round trip. The platform package provides the collector (`Validator`):
handlers assert per field (`Required`, `Enum`, `Range`, or a free `Check`)
and hand the result to the error writer. Hand-rolled per-field early returns
do not survive module conversion.

### 5. Strict query parameters

A route accepts exactly the query parameters its contract declares. A
request carrying an unknown parameter name, or a value the route does not
support for a known name (a status outside the route's status set), is a 400
with `unsupported_query_parameter` (or `validation_failed` for a known name
with a bad value), naming the parameter in `details`. Silent no-op filters do
not survive module conversion: the inputs document a live failure where
`?status=sent` returned drafts because the filter was never implemented and
the parameter was ignored.

### 6. Status enums

The wire field is named `status`, and its values are lowercase snake_case
strings: `draft`, `sent`, `accepted`, `on_hold`, `in_progress`. Database
CHECK vocabularies stay as they are (uppercase); the mapping happens at the
module boundary. A module whose column is named `state` (quotes today)
exposes it as `status` on the wire. There is exactly one field name and one
casing convention for lifecycle state across every transactional entity, so
clients stop carrying per-entity lowercasing normalizers.

### 7. Money

Every monetary amount on the wire is an integer in minor units, type int64,
field name carrying the suffix `_cents`: `total_cents`, `amount_cents`,
`balance_cents`. No float ever carries money on the wire: a JSON number with
a decimal point in a money field is a decode error, not a rounding event.
The ledger modules already work in int64 cents internally; this makes the
whole surface agree with them. Cycle 1 is single currency per deployment;
when multi-currency arrives (cycle 2, per the plan) the money fields gain
their currency companion fields in their own listed contract change.

Unit prices that need precision below one cent are integers too, at a fixed
scale of 4, field name carrying the suffix `_ten_thousandths`:
`unit_price_ten_thousandths: 13725` is a unit price of 1.3725. Scale 4 is
chosen against the database: physical quantities and prices are stored as
`DECIMAL(19,4)` (the project's pinned column convention; the normalizing
migration that made it so is in the history beside `CLAUDE.md`'s convention
table). A scale-4 integer is that column multiplied by ten thousand: every
value the database keeps is representable exactly, and every value the wire
can express persists exactly, in both directions, with no rounding anywhere.
A finer suffix such as `_micros` (scale 6) was considered and rejected: the
wire would then carry values (a price of 0.000005) that the column silently
rounds away on store, a contract that admits numbers the system cannot keep.
Scale 4 is also the domain's precision: unit pricing in lumber quotes to a
hundredth of a cent (per board foot, per each), which is one hundredth of a
cent in minor-unit terms. int64 at scale 4 still reaches nine hundred
trillion major units.

Line totals, tax amounts, and every other derived amount stay in cents:
the extension of a line (quantity at scale 4 times price at scale 4) is
rounded once, to cents, at the point the line is priced, and that one
rounding rule lives in the pricing code, not on the wire.

### 8. Document numbers

Every externally addressable document entity (quote, order, invoice,
purchase order, and the rest as they convert) carries a human-readable
document number minted from a database sequence dedicated to that entity,
formatted `<PREFIX>-<zero padded>`, for example `Q-000123`. Prefixes are one
to four uppercase letters assigned per entity when the module converts
(quotes `Q` first). The default pad width is 6; a sequence that outgrows the
width simply produces longer numbers (`Q-1000000`), never truncation or
wraparound. Numbers are minted by `nextval` through the caller's own
transaction, so a create that rolls back abandons the number: sequences are
not transactional, gaps are expected, and a document number is an identifier
for people, not a counter for audits (the created row's audit trail carries
its own identifiers). Numbers are unique per entity per database, which is
the tenancy unit.

### 9. Idempotency keys

Every POST that creates or mutates accepts an idempotency key in the
`Idempotency-Key` header: an opaque string of 1 to 128 characters chosen by
the client. Scope is the key together with the principal and the route. The
contract: the first request with a key executes and its response (status and
body) is stored against that key; a retry with the same key, same route, and
same body returns the stored response rather than executing again, across
restarts; the same key with a different body is a 409 `conflict`. The
canonical header name is `Idempotency-Key`; the existing `X-Idempotency-Key`
spelling keeps working until the middleware item lands the durable store
(R1-11), after which the legacy spelling is removed, both steps listed as
contract changes. Requests without a key are not idempotent by default:
agents and integrations that retry must send one.

### 10. The in place rule

The v1 routes change in place. There is no `/api/v2` fork in this refactor:
the modules convert one at a time onto these rules, each conversion changing
its routes' wire shapes where they differ, in place. Two disciplines make
that survivable:

- Every wire change of every converting route is listed in
  `docs/refactor/CONTRACT-CHANGES.md` (route, field or behavior, before,
  after) in the same change that makes it, and that module's
  characterisation goldens are re-recorded in the same pull request, so the
  contract document, the drift gates, and the goldens never disagree.
- The AI load management integration routes under `/api/integration/*` and
  their goldens are untouched by these conversions. That seam keeps its own
  published contract and its `X-Integration-Key` authentication until its
  own scoped-key migration later in the plan.

Known outside clients (the desk and portal apps in this repository, the
generated `gable-sdk`, the frozen agentic UI experiment) break knowingly:
the desk and portal are updated with each module conversion, and the SDK
regenerates from the contract after the contract item completes.

### Where HH's rules and the inputs differ, and what this ADR adopts

| Question | Refactor inputs | HH reference | This ADR |
|---|---|---|---|
| List envelope | `{items, total, limit, offset}`, total always | `{items, next_cursor, limit}`, never total, never offset | cursor envelope; `total` opt-in via `?include=total` |
| Bad limit | limits must be honored | clamped, fail-quiet (recorded there as a pinned divergence) | strict 400, matching the malformed cursor rule |
| Error body | one envelope: code, message, details | `{error: string}`, no code, no details | the inputs' shape, in the exact field layout above, fixing the message drop |
| Enum casing | one casing convention (lowercase) | DB CHECK vocabulary verbatim (uppercase) | lowercase wire enums, mapped at the module boundary |
| Cursor integrity | cursor semantics that work | opaque string, version prefix, structural validation | structured keyset tuple, ordering-scope bound, strictly validated, unsigned |
| Money | integer minor units or a money object | integer cents everywhere | integer cents, plus the scale-4 `_ten_thousandths` type the inputs' sub-cent unit pricing needs |

The inputs' `offset` and always-on `total` are the only input requirements
not adopted as written, and both are money-and-cost questions rather than
shape questions: cursor pagination is stable under concurrent inserts where
offsets are not, and an unconditional count doubles the work of every list
page. The opt-in keeps the inputs' aggregate screens their totals.

## Migration note for handlers

Nothing converts in this item. When a module converts (quotes first, as the
template, then module by module):

1. Swap the response writers: the module's list handlers move to
   `httpx.WriteList` with `httpx` cursors; its error paths move to
   `httpx.WriteError` with collected `Validator` field errors; its route
   registrations wrap every handler with the strict query parameter guard.
2. Swap the money fields: floats to `_cents` int64, sub-cent unit prices to
   `_ten_thousandths`, converting at the database boundary with the
   package's decimal string helpers, never through float64.
3. Add the module's document number sequence migration (a plain numbered
   SQL file creating the sequence, plus a backfill that numbers existing
   rows from the sequence's start) and mint numbers in the create path.
4. Lowercase and rename lifecycle state to `status` at the handler
   boundary.
5. Re-record the module's goldens and list every wire diff in
   `docs/refactor/CONTRACT-CHANGES.md` in the same pull request.

Routes under `/api/integration/*` are never touched by these steps.

## Consequences

- One envelope, one error shape, one casing rule, one money rule, one
  parameter posture: a client (or an agent) learns the surface once, and the
  normalizer layers the agentic UI experiment had to write become empty.
- The strictness rules (unknown parameters, bad limits, malformed cursors
  are all 400) surface client bugs early instead of silently mis-serving;
  they will also reject sloppy first-time integrators, which is the trade
  being taken knowingly.
- Every in place conversion breaks that module's existing clients, which is
  why the contract change list, the goldens, and the desk and portal updates
  ride in the same change; the generated SDK regenerates once, after the
  contract item.
- The scale-4 unit price forecloses sub-ten-thousandth pricing on the wire;
  if the domain ever needs finer, that is a new suffix and a listed contract
  change, decided then.
- The unsigned cursor accepts that a hostile client can seek within an
  ordered, filtered list it can already read; it cannot widen what it reads,
  which is why signing was deferred.
