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

Several of the questions this ADR answers (the list envelope, limit
handling, the error body, enum casing, cursor integrity, money) have more
than one defensible answer, and systems in this space have answered each of
them differently. Cycle 1 needs one rule set that a Gable module can be
converted onto once, without a second breaking pass. The decisions, the
options weighed, and the reasons are recorded in Alternatives considered,
near the end of this document.

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
- `include` is a comma separated list of names, and `total` is the first of
  them; later expansions (an inventory rows' product summary, for example)
  join it as converting modules need them, each name listed in the route's
  contract. Every list route accepts exactly `cursor`, `limit`, and
  `include`, so the strict query guard's allowed set is the same three
  names on every list, and a client never meets a list route that forgets
  one.

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
offsets. An ascending ordering resumes with `>`; a descending ordering
mirrors every comparison to `<`; and an ordering that mixes directions
(one column ascending, another descending) has no single row comparison,
so its predicate expands per column, each comparison pointed by its own
direction, ANDed together.

An ordering's columns are NOT NULL and bounded, or the ordering uses a
surrogate key (for example `created_at, id`): a nullable column and a
column holding the empty string have no keyset position to mint. A
converting module whose would-be ordering column is nullable today
(`quotes.created_at` is) fills the NULLs and sets the column NOT NULL in
the migration that adopts the ordering, before any ordering or numbering
backfill reads that column. Text sort keys sort on a bounded projection
(the first N characters), so a long name or description cannot make a
cursor unmintable. The handler parses each decoded key part to its column
type, and a parse failure is a 400 on `cursor`; the package carries the
typed decode helpers for the two column types orderings actually use,
timestamps (RFC 3339 UTC with the Z, at the column's full microsecond
precision: the package's formatter always writes the six fraction
digits, so a minted cutoff is never earlier than the row it came from)
and UUIDs (canonical lowercase hyphenated).

A cursor that is absent means first page. A cursor that is present but
broken is a 400, never quietly treated as the first page: the client must be
told its token is unusable, because silently winding it back to the head of
the list would hand it a second copy of rows it may already have processed.
Malformed means:
not decodable base64url in canonical form, not the canonical JSON form of
exactly the three fields (byte for byte what minting emits: case-variant
field names, duplicated fields, and trailing bytes are all refused), a
version other than 1, a missing or non-matching ordering
scope, an empty keyset, keyset parts that are empty, not valid UTF-8, or
carry control characters, or a decoded size over the package's bound.

The cursor is strictly validated, not signed. An HMAC was considered and
rejected for now: it would couple every core instance to a shared secret
with a rotation story that invalidates in-flight cursors, and it buys little
here because a forged cursor can only seek within the same ordering scope
and the same server-side filters; it is a pagination token, not a
credential. If cursors ever carry more authority than a row position, that
decision is reopened. Row visibility is never derived from the cursor:
branch walls, roles, and filters are re-applied from the request on every
page, which is what makes the unsigned choice safe.

Event consumers persist `GET /events` cursors across deploys, so a format
version bump on that feed keeps decoding the previous version through the
transition; the bump itself is a listed contract change.

`limit` is strict too: an integer between 1 and 200, default 50. A malformed
or out-of-range limit is a 400 naming the field. Quietly clamping a bad
limit to the default was considered and rejected: Gable's posture on the
wire is uniform, and if the server will not honor what the client sent, the
server says so.

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
- `details`, when present, carries one entry per reason. Most entries are
  about a field: `field` is the field's path (for body fields, its JSON
  path; for query parameters, the parameter name; for the cursor,
  `cursor`) and `message` says what is wrong with it. An entry may instead
  be a blocker, a business reason the request failed that belongs to no
  one field (a credit hold, a linked document): it carries `code` and
  `message` with no `field`. `details` is omitted (or empty) when the
  error is about neither.
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
| 405 | `method_not_allowed` | the method is not supported on the route |
| 409 | `stale_revision` | the write was built on a revision the document has moved past (section 11) |
| 409 | `duplicate` | a unique value the request would create already exists; `details` may carry blockers naming it |
| 409 | `idempotency_in_progress` | the same idempotency key is still executing its first request (section 9) |
| 409 | `invalid_state_transition` | the lifecycle transition is not allowed from the current state; `details` may carry blockers |
| 409 | `conflict` | a conflict no finer code above names (kept as the fallback; converting modules use the finer codes) |
| 412 | `precondition_failed` | a precondition header failed for a reason no finer code names |
| 413 | `payload_too_large` | the request body is past the route's size bound |
| 415 | `unsupported_media_type` | the body's content type is not one the route consumes |
| 422 | `idempotency_key_reused` | the key was stored against a different request fingerprint (section 9) |
| 428 | `precondition_required` | the write needs `If-Match` or a body revision and carries neither (section 11) |
| 429 | `rate_limited` | too many requests |
| 500 | `internal_error` | an unexpected server fault |
| 503 | `unavailable` | the service or a dependency it needs is down, retry later |

Errors written by middleware (auth, rate limit, idempotency, the router's
404 and 405, panic recovery) use this envelope too. Each such middleware
converts onto it in the item that owns it (R1-11, R1-13, R1-14), and each
conversion is a row in `docs/refactor/CONTRACT-CHANGES.md`.

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

### 6. Enums and closed vocabularies

The lifecycle field is named `status`, and its values are lowercase
snake_case strings: `draft`, `sent`, `accepted`, `on_hold`, `in_progress`.
Every closed vocabulary the product owns follows the same rule, in and out:
customer tier, transaction type, purchase order source, and the rest are
lowercase snake_case on the wire, and input in any other case is a 400
`validation_failed`. Database CHECK vocabularies stay as they are
(uppercase); the mapping happens at the module boundary. A module whose
column is named `state` (quotes today) exposes it as `status` on the wire.
There is exactly one field name and one casing convention across every
vocabulary, so clients stop carrying per-entity normalizers.

Codes from external standards keep their standard form and are never
rewritten: units of measure (`PCS`, `EA`, `LF`, `BF`, `MBF`), ISO currency
codes, and country and region codes. The product owns its vocabularies; it
does not own the standards.

### 7. Money

Every monetary amount on the wire is an integer in minor units, type int64,
field name carrying the suffix `_cents`: `total_cents`, `amount_cents`,
`balance_cents`. No float ever carries money on the wire: a JSON number with
a decimal point in a money field is a decode error, not a rounding event.
The ledger modules already work in int64 cents internally; this makes the
whole surface agree with them.

Unit prices that need precision below one cent are integers too, at a fixed
scale of 4, field name carrying the suffix `_ten_thousandths`:
`unit_price_ten_thousandths: 13725` is a unit price of 1.3725. Scale 4 is
what the database already keeps wherever sub cent precision exists today:
`quote_lines.unit_price`, `contract_price`, and `products.base_price` are
all `NUMERIC(12,4)`. Other unit price columns still sit at scale 2
(`order_lines.price_each`, `invoice_lines.price_each`, and the POS, AP and
portal cart `unit_price` columns), so today a scale 4 quote price is
rounded to cents the moment it becomes an order or invoice line. That
stops at conversion: a module may not expose a `_ten_thousandths` field
backed by a scale 2 column; the same change that first exposes the field
widens the column to scale 4 (migration note step 2b). Amount columns stay
at scale 2 or move to integer cents. Converting between storage and the
wire goes through the package's `ParseCents` and `ParsePrice` and never
through float64. A finer suffix such as `_micros` (scale 6) was considered
and rejected: the wire would carry values the column silently rounds away
on store, a contract that admits numbers the system cannot keep.

Money and quantity fields are required unless the contract documents the
field optional, and null is not zero: a missing amount and a zero amount
are different facts, and the package's value types refuse JSON null. A
field that is genuinely optional is the pointer type, where null decodes
to nil.

`_cents` is always the amount times 100 in the record's currency, and
`_ten_thousandths` is always the price times 10,000 in the major unit,
whatever the currency turns out to be. Currencies whose minor unit is
finer than two places are out of scope until a cycle decides them. Cycle 1
is single currency per deployment; when multi-currency arrives it is one
`currency` field (ISO 4217) per document, required on every document that
carries money, not one field per amount, in its own listed contract
change.

int64 at scale 4 passes JavaScript's exact number range only above about
900 billion major units. The web client treats these values as ordinary
numbers below that bound, which no dealer document approaches, and any
client that expects to cross it carries them as strings.

Line totals, tax amounts, and every other derived amount stay in cents:
the extension of a line is rounded once, to cents, by the one rule
section 7a fixes, implemented once in the package, never per module, and
its field on the wire is `line_total_cents`, the one name on every
document line that carries an extension.

### 7a. Quantities, units and the extension

Quantities and both sides of a conversion pair travel as JSON strings
holding a plain decimal with at most 4 fraction digits (`"12.5"`,
`"1000"`, `"0.1875"`), parsed with the package's fixed scale helpers,
never as JSON numbers: a float on either side of a line's arithmetic
would make the extension unreproducible for clients and agents. One
canonical spelling is enforced on the wire, the same posture as `limit`:
no leading zeros (`"0012"` is a 400) and no negative zero (`"-0"`);
trailing fraction zeros are the column's own padding and are accepted,
because the same type reads back what the database sends. A magnitude
beyond what the quantity columns hold (NUMERIC(12,4), at most
99999999.9999) is a 400 at the boundary, never a database fault on
store. Signs carry one meaning: a quantity is negative only on a return
or credit line, and a unit price is never negative; the converting
module's validator enforces that through the package's sign helper,
which knows the line's kind. The package's `Quantity` type is the wire
type (parse, database form, and shortest exact string form); the pair's
own precision is what cycle 3's units design works with, and the wire
type is fixed now so that design refines values, not shapes.

Every quantity carries its unit of measure beside it, in a `uom` field.
A unit price is per its price unit: where the price unit can differ from
the line's sale unit, the line carries `price_uom` beside
`unit_price_ten_thousandths`. Commodity lumber is priced per `MBF` and
fasteners per `M` or `CWT`; a price of 3.75 per M is 0.00375 each, which
no per-each scale 4 field could hold, so the conversion belongs to the
line, not to the price.

The conversion is a pair of quantities, not a factor: the line carries
`uom_qty` and `price_uom_qty`, meaning `uom_qty` of its `uom` is the same
goods as `price_uom_qty` of its `price_uom`. Lumber sold by the piece and
priced per `MBF` carries 187.5 and 1 (187.5 PCS = 1 MBF); fasteners sold
by the piece and priced per `M` carry 1000 and 1. When the units agree
the pair is 1 and 1. A single scale 4 factor cannot carry these: 1/187.5
rounds to 0.0053, and a whole thousand board feet at 500.00 per `MBF`
would price about 0.6 percent off; the pair holds both sides exactly.

The extension of a line is the quantity multiplied by the unit price and
by `price_uom_qty`, divided by `uom_qty` (the quantity converted into the
price unit), rounded once, to cents, half away from zero: 187.5 PCS at
500.00 per `MBF` is exactly 50000 cents, and one piece of the same
lumber, 2.6667 cents' worth, rounds to 267 cents. The rounding mode is
named here and implemented once in the package (`Extend`), exact in big
arithmetic until that one rounding; no module prices a line any other
way.

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

An entity may instead declare gapless numbering, minted from a counter row
locked inside the minting transaction: a rolled back create returns the
number to the sequence. Whether invoices and credit memos need this is an
accountant's question, and cycle 2's money design answers it; quotes and
orders take the gapped sequence.

The number is stored as text with a unique index, because record URLs look
documents up by number. A module checks for an existing number column
before minting a new one (`ap_invoices.invoice_number` exists today) and
either adopts it or lists the collision in `docs/refactor/CONTRACT-CHANGES.md`.
The backfill, in one migration: number the existing rows in
`(created_at, id)` order from the sequence's start, then add the
`UNIQUE NOT NULL` constraint, then `setval` past the maximum it reached.
The series key is the entity; multi-company (cycle 5) may widen it to
`(entity, company)`, which is a listed contract change when it happens.

### 9. Idempotency keys

Every POST, PUT, and PATCH that creates or mutates accepts an idempotency
key in the `Idempotency-Key` header: a string of 1 to 255 printable ASCII
characters (0x20 to 0x7E) chosen by the client. Anything else is a 400
`validation_failed` naming `Idempotency-Key`. Requests without a key are
not idempotent by default: agents and integrations that retry must send
one.

The stored scope is the principal and the key. The principal is the
caller each auth chain identifies: the user behind the session on the
desk routes, the portal customer on the portal routes, the integration
tenant on the integration routes. The stored fingerprint adds the
method, the path, the query string sorted by key then value, and a hash
of the raw request body bytes.

The contract: the first request with a key executes and its response is
stored against it; a retry with the same key, the same fingerprint, and
the same body hash returns the stored response rather than executing
again, marked by a replay response header and carrying the stored
status, `Content-Type`, `Location` when the original had one, and body.
Only 2xx and 3xx responses are stored: a 4xx or 5xx releases the key,
so a corrected request, and a retry after a server fault, execute
afresh instead of replaying the failure. The same key reused against a
different fingerprint or body hash is 422 `idempotency_key_reused`; a
second request with the same key while the first is still executing is
409 `idempotency_in_progress`. Storage is durable across restarts, for a
retention window of 24 hours past the stored response.

The canonical header name is `Idempotency-Key`; the existing
`X-Idempotency-Key` spelling keeps working until the middleware item
lands the durable store (R1-11), after which the legacy spelling is
removed, both steps listed as contract changes.

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
the desk and portal (`gable-portal` included once its routes are read) are
updated with each module conversion. The SDK regenerates once, after
cycle 5's last module converts, and is unsupported until then: a
regeneration per module would break it as many times as there are modules.

Before a module converts, its non desk callers are checked, from access
logs and issued keys: if a caller outside this repository hits the
module's routes, the conversion is listed and that caller's owner is
told. The AI load management integration is exempt only so long as it
calls nothing but `/api/integration/*`; whether the AI load management
service calls any other route is an open question, held here until it is
answered from its access logs.

### 11. Revision concurrency and preconditions

Every mutable document carries `revision`, an int64 that starts at 1 on
create and increments on every write. Every read of the document writes
the revision's ETag, and every successful write returns the new revision
in the body and its ETag on the response, so a client always leaves with
the revision it must send back. Updates and transitions require the
client's revision, through an `If-Match` header or a `revision` field in
the body. A mismatch is 409 `stale_revision`; a write carrying neither
is 428 `precondition_required`. Without this rule, two editors of one
document, a person and an agent among them, silently overwrite each
other's work, and the quote update every module copies would seed that
failure across the surface; fixing it in a later cycle would be the
second breaking pass this ADR exists to avoid. Drafts (cycle 5) use the
same mechanism. The package carries the helpers: write the ETag, parse
`If-Match`, and resolve the precondition.

The revision check and the write are one database act, never a check
followed by a write: a second writer between the two would pass the same
check. The write is conditional, `UPDATE ... SET revision = revision + 1
... WHERE id = $1 AND revision = $2`; zero rows updated means re-read
the row to tell a 404 (no such document) from a 409 `stale_revision`
(the document moved past the client's revision), or lock the row with
`SELECT ... FOR UPDATE` inside the transaction and check the revision
there.

`If-Match` carries one revision in quotes, `"3"`. The weak form `W/"3"`
is accepted too: proxies and caches rewrite strong ETags into weak ones
on the way back, and the number is all this contract reads. `*` (any
current version) and a list of tags say things a revision is not, and
are a 400.

A lifecycle transition is one write and takes the same preconditions:
`POST /<entities>/{id}/transitions` with body `{"to": "<target status>",
"revision": n}`, honoring `If-Match` and an idempotency key like every
mutating request (section 9). A transition the document's lifecycle does
not allow from its current state is 409 `invalid_state_transition`
(section 3).

### 12. Field names, timestamps and dates

Field names are snake_case on every route and in every event, the
envelope included: `event_id`, `branch_id`, `org`, `entity`, `data`,
`at`, `type`. Nearly all of today's JSON tags already are; the one
deliberate change is the events envelope, which the refactor inputs
specify in camelCase (`eventId`, `branchId`). This ADR adopts snake_case
on purpose, so the product never ships two casings on one surface, and
the events feed's casing change is recorded as a contract change in the
row of the item that ships that feed.

Timestamps are RFC 3339 UTC with the `Z`. Business dates (delivery, due,
expiry) are `YYYY-MM-DD` in the branch's local calendar. Optional fields
are present with `null`, never omitted: a client reads one shape per
document, and an absent key is not a third state between set and empty.

## Alternatives considered

Each question below is settled once, here, so that no converting module
re-litigates it.

**The list envelope.** The options: an offset envelope carrying `total` on
every page (the refactor inputs' shape); a cursor envelope that never
carries `total`; a cursor envelope with an opt-in `total`. Adopted: the
third. Cursor pagination stays correct under concurrent inserts where
offsets drift, and an unconditional count doubles the work of every list
page; making the count opt-in keeps the inputs' aggregate screens their
totals without taxing every page. The inputs' `offset` and always-on
`total` are the only input requirements not adopted as written, and both
are cost questions rather than shape questions.

**A limit the server will not honor.** The options: clamp the value and
serve the page anyway; refuse the request with a 400 naming the field.
Adopted: refusal, matching the malformed cursor rule. A silent clamp
teaches the client a rule the server never stated, and the client only
discovers it by counting rows.

**The error body.** The options: a bare string body; an envelope of code,
message and details that keeps only a generic status text (what today's
helper writes); an envelope of code, message and details that carries the
handler's message in full. Adopted: the last, with one carve-out for 500s
(section 3). The client reads the real cause ("line 1: uom is required")
instead of "Bad Request", and the specific cause no longer lives only in
the server log.

**Enum casing.** The options: expose the database CHECK vocabulary verbatim
(uppercase); lowercase snake_case on the wire with the mapping done at the
module boundary. Adopted: lowercase on the wire. One casing convention
across every vocabulary lets clients drop their per-entity normalizers, and
the database keeps its existing constraints untouched.

**Cursor integrity.** The options: an opaque versioned string validated
only structurally; a signed token; a structured keyset tuple bound to its
ordering scope and strictly validated, but unsigned. Adopted: the third.
Signing couples every core instance to a shared secret with a rotation
story that invalidates in-flight cursors, and it buys little here: a forged
cursor can only seek within the same ordering scope and the same
server-side filters. It is a pagination token, not a credential.

**Money.** The options: floats with agreed rounding rules; a money object
carrying currency; integer minor units plus a scale 4 integer for sub cent
unit prices. Adopted: the integers. Exactness on the wire costs less than
rounding rules honored by every client and agent alike, and section 7
records the scale decisions.

## Migration note for handlers

Nothing converts in this item. When a module converts (quotes first, as the
template, then module by module):

1. Swap the response writers: the module's list handlers move to
   `httpx.WriteList` with `httpx` cursors; its error paths move to
   `httpx.WriteError` with collected `Validator` field errors; its route
   registrations wrap every handler with the strict query parameter guard.
2. Swap the money fields: floats to `_cents` int64, sub-cent unit prices
   to `_ten_thousandths`, quantities and conversion factors to the
   package's `Quantity`, converting at the database boundary with the
   package's decimal string helpers, never through float64.
2b. Widen to scale 4 every unit price column the module exposes as
   `_ten_thousandths` that is still scale 2, in the same change: a module
   may not expose the field against a scale 2 column (section 7).
3. Add the module's document number sequence migration (a plain numbered
   SQL file creating the sequence, plus a backfill that numbers existing
   rows in `(created_at, id)` order, adds the unique constraint, and moves
   `setval` past the maximum) and mint numbers in the create path.
4. Lowercase and rename lifecycle state to `status` at the handler
   boundary.
5. Update every caller: search the desk (`app/`) and the portal for each
   converted route's callers and update them in the same pull request.
   The desk's `limit` and `offset` sends become cursor paging; pickers
   that relied on unpaged lists page or search. A request the strict
   rules now refuse used to be silently mishandled, and finding every
   sender is part of the conversion, not a follow up.
6. Re-record the module's goldens and list every wire diff in
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
  ride in the same change; the generated SDK regenerates once, after cycle
  5's last module converts, and is unsupported until then.
- The scale-4 unit price forecloses sub-ten-thousandth pricing on the wire;
  if the domain ever needs finer, that is a new suffix and a listed contract
  change, decided then. The same holds for conversion factors finer than
  scale 4: cycle 3's units design works within this wire type or reopens it.
- The revision precondition breaks every blind write: a client that PUTs
  without reading first takes a 428 and one extra round trip. That is the
  point; the desk pays it once per converting module.
- The unsigned cursor accepts that a hostile client can seek within an
  ordered, filtered list it can already read; it cannot widen what it reads,
  which is why signing was deferred.
