<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# The module recipe: carrying a module onto the wire contract

Written from item R1-15, which carried the quote module onto
`docs/adr/0001-wire-contract.md` and `docs/adr/0003-events-outbox.md` in
place. Every later module follows these steps, in this order. Quotes is the
worked example: where a step says "see", open the named file and copy its
shape, do not reinvent it.

A module of the weight of quotes (a header with lines, a lifecycle, a list, a
desk screen set, two other seams reading it) is 24 to 40 dev hour equivalents
when the platform packages are used as they are. A module that needs a
platform change (a new helper in `internal/platform/httpx`) lists the change
as its own commit before the module's.

The module's code map, for orientation:

| Concern | Where in quotes |
|---|---|
| Wire types and enum mapping | `core/internal/quote/model.go` |
| Request parsing and validation | `core/internal/quote/input.go` |
| Store | `core/internal/quote/repository.go` |
| Pricing, transitions, events, transactions | `core/internal/quote/service.go` |
| Routes and the platform writers | `core/internal/quote/handler.go` |
| Migration | `core/migrations/090_quotes_wire_contract.sql` (and `down/`) |
| Wire tests, unit tests, transaction proofs | `wire_test.go`, `service_test.go`, `tx_test.go` |
| Contract fragment | `core/api/fragments/quote.yaml` |
| Wiring | `core/internal/app/serve/serve.go` |
| Goldens | `core/internal/characterization/scenarios_sales_test.go` (`quoteGroups`) |

## 0. Before any code

1. Read ADR 0001 (all of it) and ADR 0003 sections 2 and 3. They bind.
2. List every route the module owns (`core/api/ROUTES.txt`, filter by the
   module's package) and every caller of them: the desk
   (`web/apps/desk/src`), the portal, the partner routes, other Go modules
   that import the package, raw SQL elsewhere that reads or writes the
   module's tables (`grep -rn "FROM <table>\|INTO <table>\|UPDATE <table>" core`),
   and the AI load management seam. The integration routes under
   `/api/integration/*` are frozen: their wire stays byte for byte and the
   handler adapts internally to the converted module.
3. Decide what is the module's and what is another module's. Quotes owns
   `/api/v1/quotes`, `/analytics`, `/{id}`, `/{id}/file`, the transitions and
   convert routes. The exposure routes under `/api/v1/quotes/exposure` and
   `/{id}/exposure/...` belong to pricing and stay on their own shapes until
   pricing converts; the quote wire spells the exposure rollup lowercase and
   says so in `CONTRACT-CHANGES.md`.
4. Start your own throwaway Postgres (name and host port from your brief) and
   put its URL on every command. Run the module's goldens green first.

## 1. Platform changes first, as their own commit

If the module needs a helper the platform does not have, add it to
`core/internal/platform/httpx` with tests before the module uses it. R1-15
added `Timestamp`, `DecodeJSON` and `Validator.Int/Quantity/UUID/Timestamp`
(`fields.go`) and `testutil.RequireDBMaxConns`. Do not put a second copy of
any of these in a module.

## 2. Tests first, wire level

Write the module's wire tests before the code. They are black box: a real
Postgres, the real handler on a real mux behind the real idempotency
middleware, requests as JSON and responses read back as JSON. Nothing in them
touches a module type, so each states a wire fact. See `wire_test.go`. The set
every module carries:

- the create shape: number, lowercase status, `_cents`, scaled price, the pair,
  revision and `ETag`, `Location`, timestamps at microsecond precision, optional
  fields present as `null`, no legacy field left on the wire;
- exact money: the extension rounded once, the conversion pair, freight rules;
- field validation: one 400 with every offending field in `details` and the
  full path (`lines[1].quantity`), a body the route cannot consume as
  `bad_request`, an unknown body field refused;
- the list: a filter that filters, an unknown or uppercase filter value refused,
  an unknown parameter refused, the cursor walks every row once, `limit` and
  `cursor` validated, `include=total`, an empty page is `[]` in the bytes;
- the error envelope: status, code and the handler's own message for each
  refusal, `meta.request_id` present;
- revision: 428 without a precondition, 409 `stale_revision`, `If-Match` strong
  and weak, the body revision, header and body disagreeing, the new revision and
  `ETag` on success;
- the lifecycle: each allowed edge, the forbidden ones as 409
  `invalid_state_transition`, each transition's event in order;
- idempotency through the existing middleware: the same create twice with one
  key returns the first response (`Idempotency-Replayed: true`) and makes one
  row and one event; the same key with another body is 422.

Then the service tests with a fake repository (`service_test.go`: pricing,
defaults, the order of calls, the event written last, refusals writing
nothing), and the transaction proofs (`tx_test.go`):

- a failing event write rolls the mutation back, for each kind of write;
- a rolled back create abandons its document number and never reuses it;
- three contenders at pool size 4 (`RequireDBMaxConns(t, 4)`): concurrent
  creates get distinct numbers and one event each, three racers on one revision
  have exactly one winner, mixed writers and a reader finish;
- the gated saturation test (`newGatedTx`): as many contenders as pool
  connections, each held INSIDE its transaction at a gate before it runs a
  statement, for each kind of write. This is the test that fails when code
  inside a transaction uses the pool. Prove it bites once: add a
  `db.Pool.QueryRow` to a repository method used inside the transaction and
  watch it time out, then take it out.

Prove the module's live failures: for each live failure the refactor inputs
name for the module, write a test that fails against the base commit. Check
the base out in a throwaway worktree, write the test with the base request
shapes and the new expected outcome, and show its failure (R1-15 did this for
the missing unit of measure 500, the silent status filter, the offset page and
the dropped error message).

## 3. The migration

One plain numbered SQL file (`ls core/migrations` for the next free number
after merging `refactor/v1`; another item may take yours) and a down file in
`core/migrations/down/`. In order:

1. Fill NULL `created_at` and set it NOT NULL: the list orders on
   `(created_at, id)` and the backfill reads it.
2. `revision BIGINT NOT NULL DEFAULT 1`.
3. The sequence, a `<entity>_next_number()` function and the `number` column:
   backfill existing rows in `(created_at, id)` order, `setval` past the
   maximum, then the DEFAULT, NOT NULL and the unique constraint. The DEFAULT
   is for raw SQL writers elsewhere (the portal's insert, the seed); the Go
   create path mints through its transaction.

   If the table already has a number column (orders, invoices and customers
   do, under their own names and formats), ADR 0001 section 8 applies before
   anything is added: adopt it as the wire's `number` (map the column in the
   repository, keep its existing values and format, put the sequence behind
   its DEFAULT so new rows continue from the maximum) or list the collision in
   `CONTRACT-CHANGES.md` with what the old format becomes. Never add a second
   number column beside an existing one.
4. Widen every unit price column the module exposes as
   `_ten_thousandths` that is still scale 2 (step 2b). Quotes needed none.
5. Add what the wire needs that the table lacks: the conversion pair and
   `price_uom` on lines, a `position` so lines keep their order.
6. The keyset index for the list's ordering.

Apply it to an empty database and to a seeded one, and check the backfill on
rows that exist.

## 4. Model

The wire type is the domain type: structs with `json` tags, money as
`httpx.Cents`, unit prices as `httpx.Price`, quantities as `httpx.Quantity`
(a decimal string on the wire), timestamps as `httpx.Timestamp`, optional
fields as pointers WITHOUT `omitempty` (they serialize as `null`). Enums keep
their storage vocabulary in Go and map at the boundary with `MarshalText`
(lowercase on the wire), plus a `Parse` that accepts only the lowercase
spelling. A list item is its own smaller struct (`QuoteSummary`) that the full
document embeds, so the two cannot drift.

Bind each new wire struct in `core/internal/apicontract/model_shape_test.go`.

## 5. Request parsing

Decode the body with `httpx.DecodeJSON` into a DTO whose fields are strings,
pointers and `json.RawMessage`, never the final types, then `Parse()` it with
an `httpx.Validator`: `Int`, `Quantity`, `UUID`, `Timestamp`, `Required`,
`Enum`, `Check`. Every problem is collected into one 400 with its full field
path. Produce a `Draft` the service prices. See `input.go`.

Rules the quotes parse made that a module reuses:

- the unit of measure defaults from the product (the service fills it from
  the product's own unit, then `price_uom` from the unit); only a line with
  neither a `uom` nor a `product_id` is a 400 naming `lines[i].uom`;
- the conversion is a pair: both `uom_qty` and `price_uom_qty`, or neither;
  `price_uom` defaults to `uom`; a different `price_uom` requires the pair;
- a created document has no `status` in the request.

Map foreign key violations to a 400 naming the request field
(`mapWriteError` in `repository.go`) so a bad reference is never a 500.

## 6. Repository

- Every statement goes through `r.db.GetExecutor(ctx)`. Inside a transaction
  that is the transaction. No `r.db.Pool` use anywhere a transaction may be
  open.
- Read and write scaled values in SQL (`ROUND(col * 100)::bigint`,
  `$1::numeric / 100`), never through float64 and never through text.
- The list is a keyset query: `ORDER BY created_at DESC, id DESC`, the cursor
  predicate `(created_at, id) < ($ts, $id)`, `LIMIT limit+1` so the service
  knows whether another page exists; the filters ride in one shared predicate
  string used by the list and its count.
- `LockQuote` takes `SELECT ... FOR UPDATE` on the row; revision checks happen
  after it, inside the same transaction. A joined select cannot lock an outer
  joined row, so lock first, read second.
- Every write that changes the document moves `revision = revision + 1`.
- The branch wall (`middleware.BranchIDForQuery(ctx)`, a nil result meaning no
  wall) applies to EVERY read and every lock, not only the list: the get, the
  lock, the count, the per-customer list, the file or attachment download and
  any statement a transition runs before it writes. A route that reads a
  document by id without the wall is a cross branch leak (the quote file route
  lacked it until R1-15's fix). Test it: a second branch's request for the
  first branch's id is a 404 on every route.

## 7. Service

Give the service a `TxRunner` and an `EventRecorder` (`WithTxRunner`,
`WithOutbox`), both optional so unit tests need no database. Each mutation is
one `inTx`, in this order:

1. lock the row; read it; check the revision (`httpx.CheckRevision`); check the
   lifecycle; price or validate;
2. write;
3. re-read the document for the response;
4. write the event, as the LAST statement.

Post-commit side effects (auto purchase orders, the exposure snapshot) run
after `inTx` returns, best effort. In process callers (the portal accepting a
quote, the integration seam) call the same transition without a client
revision (`UpdateState`), so the lifecycle and the event are the same for
everyone.

Event types are `<entity>.created` and `<entity>.<target status>`; a reopen is
its own type. The data is a small summary (number, status, from status,
revision, total in cents), never the document.

## 8. Handler

For every route: `httpx.StrictQuery(r, ...allowed)` first (a route with no
parameters passes none), ids parsed to a 400 naming `id`, body through
`DecodeJSON`, list through `ParseListQuery` + `WriteList`, errors through
`httpx.WriteError`, `Content-Type: application/json` on every JSON body. A
document response also calls `WriteRevisionETag`; a create adds `Location`.
The precondition is `If-Match` plus the body revision, handed to the service,
which refuses a write with neither (428).

Register the handler in `serve.go` with the outbox writer and the database as
transaction runner, and register its routes with `scoped(...)`, the role guard
composed with the branch middleware (`serve.go`; quotes: `scoped("admin",
"owner", "sales")`). A machine key reaches the module through its scopes (ADR
0002): `<module>:read` for GET and HEAD and `<module>:write` for every other
method, the module being the first path segment under `/api/v1/`, so a new
route must sit under the module's own segment to inherit the right scope. A
key without the scope is a 403 `forbidden` and the refusal writes an
`audit_log` row attributed to the key. The module's exit test names this: a key
holding only `<module>:read` reads a document (200), is refused a write with
403, and the audit row exists (the `machine_key` golden group pins the pattern;
a wire test of the module's own proves its routes carry the scope).

## 9. Other writers and readers of the module's tables

Grep for them (step 0). For each: raw INSERTs rely on the column DEFAULTs and
should write the creation event in their own transaction (the portal's quote
request does, through an optional `EventRecorder`); other modules that update
the table's content (the exposure escalation rewrites prices) bump the
revision; readers of columns whose NAME and TYPE are unchanged keep working, but a
reader of a column whose MEANING changed does not. The quote lines' pair is the
lesson: `price_uom_qty / uom_qty` changed what `quantity x unit_price` means,
and the exposure scanner, the portal's quote read and the integration seam's
accept-and-convert all kept multiplying the old way until the review found
them. For every column the migration adds or reinterprets, grep every reader
(`grep -rn "<column>\|<table>" core`, including SQL strings) and either apply
the new meaning there with a test (an MBF line priced per 187.5 pieces is the
check) or make it refuse what it cannot carry.

A helper route that feeds an unconverted neighbour (quotes' convert hands the
order payload to the orders route, which takes whole cents per sale unit and no
pair) REFUSES what the neighbour cannot carry rather than rounding it: a quote
with a line whose pair is not 1 to 1, or whose `price_uom` differs from `uom`,
is a 409 `invalid_state_transition` with a `line_not_convertible` blocker
naming `lines[i]`. The payload is built inside the transition's transaction,
before the status changes, so a refusal leaves the document as it was. The
neighbour's own converting module lifts the refusal (here R2-1). The same
rule binds the frozen integration seam that does the same job.

## 10. The contract

1. Edit the module's fragment to describe the new contract exactly: schemas,
   `required` lists matching the Go tags, nullable as `type: [T, "null"]`,
   parameters, headers, the status codes the handler writes. Path parameters
   are declared inline on each operation (the assembler resolves a shared
   parameter only when its component name equals the path name). Error
   responses reference the shared `Wire*` responses and `WireError`.
2. `cd core && go run ./api/tools/merge && go run ./cmd/pending -write &&
   go run ./cmd/census -write`.
3. `cd web/packages/api-client && npm run generate`, fix `test/usage.ts` (the
   happy path uses the new shapes; the `@ts-expect-error` lines keep the wrong
   calls honest).
4. `make contract` from the repository root. Never edit a generated file by
   hand and never hand merge one: regenerate after every merge of
   `refactor/v1`.

## 11. Goldens

Edit the module's scenario to the new bodies and headers (`If-Match` carries
the revision; a scripted document's revision is deterministic), add steps that
pin the new behaviour (the validation 400, the filter, the unsupported
parameter, the missing and stale revision, the refused transition) and a step
that reads the module's events from `/api/v1/events` so the outbox wiring is
pinned by a golden. A list's `next_cursor` normalises to `<cursor>` (it embeds
a time and a random id). Re-record only the module's golden:

```
cd core && DATABASE_URL=<your url> GABLE_TEST_REQUIRE_DB=1 \
  go test ./internal/characterization -update
git status core/internal/characterization/testdata   # only your golden
```

Run the suite twice without `-update` to prove the new golden is stable. The
integration golden must not change. If another module's golden changes with
`-update` and the committed one passes without it, that golden is order
dependent; restore it and say so in the report.

## 12. CONTRACT-CHANGES.md

One row per route or behaviour, in the same pull request: the route, what it
did, what it does. The check that the list is complete: diff the assembled
`core/api/openapi.yaml` against the base commit's, operation by operation, and
make sure every changed operation, parameter, schema property and status code
has a row; add a row for every behaviour change that is not in the contract
(a branch wall added, an event written by another seam, a column made NOT
NULL).

## 13. Callers

- **Desk.** Types come from the generated client
  (`@gable/api-client` `components['schemas']`), so contract drift is a compile
  error. The service parses the error envelope into one error type and the
  pages show its message and field details. Quantities stay strings and money
  stays integers until the display helper; user input is parsed to integers
  with string arithmetic, never `parseFloat`. The page keeps the revision it
  loaded and sends it back; a 409 `stale_revision` reloads with a toast.
- **Portal and partner.** Follow the shape when they share the model; keep
  their own DTOs when they do not.
- **Integration seam.** Unchanged on the wire; adapt inside the handler.
- **The generated SDK** is not regenerated per module (ADR 0001 section 10).

## 14. End to end

Extend the Playwright suite (`web/e2e`) with the module's flow through the
real stack (`web/scripts/e2e-stack.sh`): create, edit with a stale revision,
transition, the status filter and paging. Update any existing spec that read
the old list envelope or the old heading.

## 15. Before the pull request

```
git fetch origin && git merge origin/refactor/v1      # merge commit; regenerate generated files
cd core && go build ./... && go vet ./...
DATABASE_URL=<url> GABLE_TEST_REQUIRE_DB=1 go test -race ./...
cd .. && make contract
bash scripts/check-shape.sh
cd web && npm run typecheck && npm run lint && npm test && npm run build
npx playwright test    # with the stack up, from web/
python3 .github/scripts/reuse_gate.py
```

Then commit per step when green, push, and open the pull request into
`refactor/v1`.

## Decisions the quote template made, for reuse

| Question | Quotes' answer |
|---|---|
| A line without a unit of measure | The server defaults it from the product (`ProductRef.UOMPrimary`) and then `price_uom` from the unit; only a line with neither `uom` nor `product_id` is a 400 naming `lines[i].uom`. The desk still pre-fills the unit so users see it |
| Editing a document the lifecycle has closed | 409 `conflict` with a blocker, not `invalid_state_transition` (an edit is not a transition) |
| Creating in a status other than the first | Not possible: the request has no status |
| A transition by an in process caller | Same rules and event, no revision precondition |
| A list the partner surface serves | Stays a bare array of the new shape until that module converts |
| The unconverted neighbour's vocabulary on the converted wire | Mapped to the wire's casing on the converted module (the quote's `exposure_state`), the neighbour's own routes unchanged |
| The router's own 404 and 405 | Not converted here; they keep net/http's plain text |
| `ETag` and the generated client | The client does not surface response headers; the body carries `revision` for clients that need it, and a convert body carries it too |
| The idempotent replay's `ETag` | A replayed create returns the stored body and status but not the `ETag` header; the body's `revision` is the source (hardening list) |
| A convert-style helper route | Takes the same revision precondition as any transition, builds its payload inside the transition's transaction, and returns the document's revision (body and `ETag`) |
| A helper route whose neighbour cannot carry a line | 409 `invalid_state_transition` with a `line_not_convertible` blocker naming `lines[i]`, until the neighbour converts (convert: a pair that is not 1 to 1, or `price_uom` different from `uom`) |
| A client sending a revision on create | 400 naming `revision`: a create has no revision to precondition on |
| A PUT carrying a field it does not apply | 400 naming the field (`source`, `margin_total_cents`, `original_file`, `original_filename`, `original_content_type`, `parse_map`, `branch_id`), never a silent drop |
| `price_uom` vocabulary | `^[A-Z]{1,6}$`: not limited to the sale unit enum (a price per M or per CWT is real), but a code, never free text |
