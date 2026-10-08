# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0003: the transactional outbox and the events read API

## Status

Accepted for the Gable v1 refactor (item R1-12). Implements the delivery
requirement of the refactor inputs (a transactional outbox table plus a
delivery worker, at least once, per subscriber cursor, with a replay window)
on the wire rules of ADR 0001: the events feed is a list route, so it returns
the list envelope, refuses what it will not honor with a 400, and names its
fields in snake_case, the event envelope included.

## Context

Gable's only event seam today is `pkg/eventbus`, an in-process, in-memory
fan-out whose own package doc lists what it does not guarantee: durability,
cross-process delivery, redelivery, replay. The lumber price exposure emails
ride it, which means a restart between the mutation and the drain of the
subscriber queue loses the notification, and nothing outside the process can
see an event at all. The refactor inputs list this as a delivery requirement
learned in the agentic UI experiment: consumers polled the database because
no durable event feed existed, and the experiment's own HTTP emitter was fire
and forget.

The fix is the standard one: the mutation's transaction writes the event into
an `events_outbox` table alongside the mutation itself, and a delivery worker
republishes committed events to consumers, advancing a per subscriber cursor
only after the event has been handed over. An HTTP read API serves the same
table to outside consumers with cursor pagination, so agents poll one feed
instead of guessing at tables.

## Decision

### 1. The table

`events_outbox` (migration 089) carries one row per event, written inside the
mutation's transaction:

- `position BIGINT NOT NULL`, the ordering key, assigned by a `BEFORE
  INSERT` trigger that first takes the transaction scoped advisory lock and
  then draws from `events_outbox_position_seq`. There is no column default:
  the position cannot be drawn before the lock, and any insert (a backfill,
  a SQL function) keeps the ordering. Its guarantee is the subject of
  section 2.
- `event_id UUID NOT NULL UNIQUE`, the logical identity consumers dedup on.
- `type TEXT NOT NULL`, the dot-delimited event type (`quote.exposure.flagged`,
  later `quote.created` and friends). The type is also the in-process bus
  subject, so the drain maps rows to subscribers with the bus's own wildcard
  matching and no second vocabulary exists.
- `org TEXT NOT NULL` and `branch_id UUID NULL`, the tenancy scope the inputs
  require every event to carry. Gable today is one database per dealer with
  no org identity in the schema, so the writer stamps the deployment's org
  slug (`default` until a deployment level org identity exists) and fills
  `branch_id` from the request's branch context when the caller did not set
  one; a NULL branch_id means what it means in branchctx, the event is not
  pinned to one branch.
- `entity_type TEXT NOT NULL`, `entity_id UUID NOT NULL`, the entity the
  event is about, exposed as the envelope's `entity {kind, id}`.
- `data JSONB NOT NULL`, the small summary payload.
- `at TIMESTAMPTZ NOT NULL`, when the event happened.

`event_subscriber_cursors (subscriber TEXT PRIMARY KEY, position BIGINT NOT
NULL, updated_at TIMESTAMPTZ NOT NULL)` holds one cursor per named consumer
of the drain.

### 2. The ordering guarantee, and why positions are assigned in commit order

A sequence assigns `position` at insert, but transactions commit in a
different order than they insert. A reader that pages with `position > cursor`
and advances its cursor to the highest position it saw can therefore skip an
event: transaction A takes position 95 and is still running; transaction B
takes position 100 and commits first; the reader serves 100, mints its cursor
from it, and when A commits, position 95 is below the cursor and is never
served. The brief's exit test names exactly this case: two transactions, the
lower position committing last, must never be skipped.

Two read rules were candidates.

**A horizon from the oldest in-flight transaction (`pg_snapshot_xmin` with an
`xid8` column on each row).** The reader takes a snapshot, computes
`pg_snapshot_xmin`, and reads only rows whose inserting transaction is
certainly finished (`xid < xmin`, and visible, therefore committed). With a
cursor on position alone this is necessary but not sufficient, because xid
order is begin order, not position order, and the failure does not need
exotic timing:

- T0 begins and writes, taking xid 11.
- T1 begins and writes, taking xid 12.
- T1 inserts its event: position 95.
- T0 inserts its event: position 100, then commits.
- T1 is still running, so the next reader's xmin is 12.
- T0's row (xid 11 < 12) passes the filter with position 100; the reader
  advances past 95; T1 commits and its event is skipped forever.

Ordering and paginating on the `(xid8, position)` pair closes that hole:
every transaction with an xid below the snapshot's xmin has finished, so a
reader that serves only rows with `xid < xmin` in `(xid8, position)` order
and mints its cursor from the last served pair can never skip a row,
because any row that becomes visible later carries an xid at or above that
xmin and therefore sorts after everything already served. The rule is
correct. Its costs are real but different in kind: the feed lags behind the
oldest in-flight writer (committed rows whose writers began after that
xmin wait for it, a delay, never a loss), and the cursor becomes a two key
cursor, a wire change for every consumer. It is the second escape below,
not the rule shipped.

**Commit-ordered positions: an advisory lock at insert.** `outbox.Write`
takes `pg_advisory_xact_lock(k)` on a fixed key before the row is inserted,
so before the sequence hands the row its position, and the lock is released
only at transaction end (commit or rollback). The consequence: when a
transaction assigns itself a position, every transaction that assigned a
lower position has already finished. So:

- among committed rows, position order is commit order;
- every row a reader cannot yet see (its transaction still running, or not
  yet inserted) carries a position greater than every committed row's
  position, because its writer cannot even draw a position until every
  lower-positioned writer has finished.

A reader's snapshot sees exactly the transactions that committed before it
was taken, which is a prefix of commit order, therefore a prefix of position
order. Paging with `position > cursor` and minting the cursor from the last
row of the page can never advance past a row that commits later: any such
row has a higher position than everything the reader can see. Rolled back
writers burn their position (a gap), which keyset pagination steps over
harmlessly. This is the rule implemented. `FOR UPDATE SKIP LOCKED` is not
needed for correctness of the read (a plain snapshot read of a prefix is
already correct); the drain uses it only to keep two drain instances from
contending on one subscriber's cursor row.

The cost is stated plainly: transactions that write events serialize on the
event insert, and the serialization extends to the end of the first writer's
transaction. A transaction that writes an event and then lingers blocks every
other event write for as long as it lingers. Gable's event-writing
transactions today are short (the exposure scanner wraps each line's writes
in one small transaction; the service methods are a handful of statements),
and no code path holds a transaction across user think time.

Two rules follow for every event writer:

- **The event write is the transaction's last statement.** A writer that
  takes the advisory lock and then updates other rows can deadlock against
  a second writer holding the lock and updating the same rows in the other
  order (Postgres detects the cycle after `deadlock_timeout` and aborts one
  side, burning a position and failing the caller); after the lock, only
  the insert runs, so the cycle cannot form. Bulk transactions (a catalog
  import, a batch adjustment, an EDI load) therefore write per-row events
  at their end, or one summary event, never per-row events as they go:
  a bulk writer holding the lock through its whole import stalls every
  event write behind it.
- **The lock and the position are enforced in the database**, not only in
  `Write`: migration 089 gives `events_outbox` no position default and a
  `BEFORE INSERT` trigger that takes the advisory lock and assigns the
  position, so an insert that bypasses `Write` (a backfill, a SQL function)
  keeps the ordering instead of silently breaking it.

If event volume ever makes the single lock a bottleneck, the escapes are
named, in order of increasing change: a `DEFERRABLE INITIALLY DEFERRED`
constraint trigger that takes the lock and assigns the position at commit
(no wire change, and it removes the insert-time hold), the xid8 horizon
with its two key cursor (a cursor version bump on the read API), and
logical decoding or a broker (a commit-ordered log outside the row store,
an operations change). Each is a later, listed contract change; none is
needed at Gable's scale.

One rule for every reader, in one place: the drain (`pkg/outbox` drain
runner) and the HTTP read API (`GET /api/v1/events`) both page with
`position > cursor` in position order, and both mint their cursors only from
positions they actually read. Neither ever advances a cursor (its own, or a
client's, by serving a page) on the strength of a position it did not serve.

### 3. The writer

`outbox.Write(ctx, ev)` inserts through the caller's executor, resolved by
the database seam `GetExecutor(ctx)`, exactly as `pkg/audit` does: inside a
transaction the row joins that transaction and commits or rolls back with
the mutation it describes; outside one the write wraps itself in its own
short `RunInTx` so the advisory lock is still transaction scoped (a session
scoped advisory lock on a pooled connection would outlive the write and
poison the pool). Inside a transaction the caller propagates the error, so a
failed event write fails the mutation: an event is a fact about the
mutation, and a mutation whose event cannot be recorded is not permitted to
pretend it happened silently.

### 4. The drain runner

`pkg/outbox.DrainRunner` is a poller with `Start` and `Stop`, started from
the server for now (item R1-4 gives the `worker` role its own jobs, and
moving it there changes only the wiring). Each registered subscriber, a
(durable name, subject pattern, handler) triple mirroring its `pkg/eventbus`
subscription shape, is drained per tick in one short transaction:

1. `Start` creates every registered subscriber's cursor row exactly once
   (`INSERT ... ON CONFLICT DO NOTHING`) before the first pass. A newly
   registered subscriber starts at the feed's current maximum position: the
   outbox is a replay window, not a ledger, so a new consumer receives what
   commits after it registered; one that wants the history opts into replay
   and starts at 0.
2. the pass locks the subscriber's cursor row `FOR UPDATE SKIP LOCKED`. No
   row under that lock means another drain instance (a rolling deploy, a
   second replica, the worker beside serve) holds this subscriber: the tick
   skips it. Because the rows are created at `Start`, "no row" can never be
   misread as "new subscriber, drain from position 0", which would replay
   the whole outbox to a subscriber that already consumed it.
3. the pass reads one window of rows past the cursor in position order and
   delivers each row whose type matches the pattern to the subscriber's
   handler synchronously, in order, carrying the outbox `event_id` as the
   event id on the bus envelope (`eventbus.NewEventWithID`), so a replay of
   a row presents itself as the same logical event downstream.
4. the cursor advances past a row only when its handler returned nil
   (non-matching rows advance it too), and the first error stops that
   subscriber's pass: the cursor stays before the failed row and the
   attempt is counted on the cursor row. The next ticks retry it; after 10
   consecutive failures of the same row the row is parked
   (`event_subscriber_parked`, with its event id and type for an operator
   to inspect and replay by hand), logged, and the cursor moves on, so one
   poison event cannot stall a subscriber forever. One window runs per
   tick, so a backlog larger than the batch drains across ticks instead of
   in one unbounded pass.

Delivery is at least once: a crash between a handler returning nil and the
pass committing replays the window rather than losing it, and subscribers
that must not act twice dedup on the `event_id` the mutation minted. The
exposure notifier dedups in process on that id; its dedup is deliberately
not durable in v1, so a restart during a replay can send a duplicate email.
That is accepted for v1 (an operator-facing annoyance, not a correctness
failure); a durable dedup table is the escape if it ever bites.

The handler runs inside the pass's transaction with that transaction's
context, so its reads and writes go through the transaction (the database
seam resolves it) and the transaction never reaches for a second pool
connection. The cost is stated plainly: the cursor row stays locked for the
window's deliveries, so handlers must not block for long. `Stop` never
cancels a pass mid-transaction (an aborted pass would repeat its
deliveries after a restart); it finishes the in-flight window on a detached
context and the loop checks the stop signal between passes.

The in-process bus no longer sits on the delivery path: nothing publishes
exposure events to it any more, so the drain calls the registered handlers
directly. The bus's envelope (`eventbus.Event`) and wildcard subject rules
remain the shared vocabulary between the drain and its subscribers, so a
future broker-backed delivery swaps the drain's internals, not every
consumer. The exposure notifier itself is unchanged apart from its
registration; the scanner and service that used to publish to the bus
directly now write the outbox row inside their mutation's transaction
instead, so the lumber price exposure emails send from committed rows
rather than from whatever the process happened to keep in memory.

### 5. The read API

`GET /api/v1/events?cursor=&types=&limit=` is a list route on the ADR 0001
rules, served by `internal/events` through `core/internal/platform/httpx`:

- the list envelope `{items, next_cursor, limit}` (`total` only under
  `?include=total`), `items` never null;
- each item is the event envelope in snake_case per ADR 0001 section 12:
  `event_id`, `type`, `org`, `branch_id`, `entity {kind, id}`, `data`, `at`,
  with `at` an RFC 3339 UTC timestamp at microsecond precision;
- the cursor is the package's keyset cursor over the ordering scope
  `events.position`: opaque, versioned, scope checked, malformed is a 400
  naming `cursor`;
- `limit` is 1 to 200, default 50, malformed or out of range is a 400;
- `types` filters by exact type, comma separated, repeatable; each name must
  be a dot-delimited lowercase type or the request is a 400
  `validation_failed` naming `types`; a filter that matches nothing serves an
  empty page, it is not an error;
- any other query parameter is a 400 `unsupported_query_parameter` naming
  it, the strict parameter posture: a silent no-op filter on this feed would
  hide events from exactly the agents that poll it;
- errors use the one error envelope from the package.

Events are not branch-scoped by the reader: the feed is an administration
and integration surface over the whole database (the tenancy unit), like the
rest of the admin reads. It is role gated `admin, owner`, the narrowest gate
the admin reads use (the tech admin and GL surfaces), stated here and in
`docs/refactor/CONTRACT-CHANGES.md`.

Retention: the outbox is a replay window, not a ledger; the exposure ledger
and each module's own tables remain the record. A retention job (purging
rows past the oldest cursor by an operator-set window) belongs to the
`worker` role's job set in R1-4, not to this item; nothing here deletes
rows.

## Alternatives considered

**The reader-side xid8 horizon** is recorded in section 2 with the
interleaving that defeats its position-cursor form and the `(xid8,
position)` pair that fixes it. It is a real escape, not a false lead: it
was refused for v1 because its feed lags behind the oldest in-flight
writer and its cursor is a two key cursor (a wire change for every
consumer), not because it is unimplementable. It is the second named
escape above.

**A deferred constraint trigger assigning the position at commit.** A
`DEFERRABLE INITIALLY DEFERRED` constraint trigger on `events_outbox` can
take the advisory lock and draw the position when the transaction commits,
rather than at insert: writers no longer hold the lock while the rest of
their transaction runs, which also removes the deadlock the last-statement
rule guards against, and position order stays commit order because the
lock is still taken before the position and released only at commit. It
keeps the same table and the same wire. It was deferred with the other
escapes because the trigger moves a correctness-critical step into a
constraint whose firing order against other deferred triggers is easy to
get wrong, and the measured insert cost of the plain lock is far below
Gable's volume; it is the first named escape above, and the honest record
is that it dominates the shipped rule on throughput and loses on
explicitness.

**A second, commit-ordered position assigned after commit** (a NULL
position filled in by a later pass) merely moves the problem: the filling
pass runs in a transaction too, and a reader must again decide whether a
NULL-position row will someday sort before a filled one.

**Logical decoding or a broker** gives a true commit log without the insert
lock, and is the right answer at a scale Gable has not reached; it changes
operations (a replication slot per consumer, or a broker to run) and is
deferred, with the lock's cost documented above.

**Doing nothing** (the in-process bus as is) leaves the exposure emails
restart-fragile and gives outside consumers no feed at all, which is the
live failure the inputs document.

## Consequences

- An event and its mutation are one transactional fact: rollback leaves no
  event, commit leaves exactly one, and no restart between the two can lose
  or orphan either.
- Event-writing transactions serialize their commits on one advisory lock;
  the exposure scanner's per-line transactions make this invisible at
  Gable's scale, and the bound is the duration of the longest event-writing
  transaction.
- Outside consumers get a stable, cursor-paginated feed with the platform's
  strictness rules, and agents stop polling domain tables.
- The in-process bus keeps its role (fan-out, queues, panic isolation) and
  loses its role as the durability seam; its package doc's guarantees are
  now true of the bus alone, not of event delivery end to end.
