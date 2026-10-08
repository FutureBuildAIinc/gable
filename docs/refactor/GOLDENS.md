# Characterisation goldens (R1-1)

The characterisation harness is the safety net of the whole refactor: it pins
today's HTTP surface, response by response, so that any behaviour change the
refactor makes is a deliberate, listed change rather than an accident. It was
recorded against commit `8361f23b` and every later change to a response must
either keep its golden byte-identical after normalisation or re-record it with
a reason in `docs/refactor/CONTRACT-CHANGES.md`.

## How the harness works

The harness lives in `core/internal/characterization` (all in `_test.go`
files; `doc.go` exists so the package builds). `TestMain` owns the throwaway
database; one test, `TestCharacterisationGoldens`, does the following on every
run:

1. **Own database, owned by TestMain.** It reads the Postgres server from
   `DATABASE_URL` and creates a fresh database on it with a random name
   (`gv1_goldens_<hex>`), dropped when the run ends. The create and drop live
   in `TestMain`, not in `t.Cleanup`: a `go test -timeout` panic fires on the
   timer goroutine and skips both, so a timed-out run's database is reclaimed
   by the next run's startup sweep (or by the operator) - teardown covers the
   ordinary failure paths. The sweep drops stale `gv1_goldens_*` databases
   only when no backend is connected to them, which protects a concurrent run
   whose migrate/seed/server pool is already attached; the harness's admin
   connection goes to the `DATABASE_URL` database, not to the golden one, so a
   fresh database is unprotected from its `CREATE` until the built binaries
   connect - a window bounded by the `go build` that precedes the first
   connection, not observed to collide. Other packages' tests share the
   `DATABASE_URL` database; the harness never touches it beyond
   `CREATE`/`DROP` of its own.
2. **Migrate and seed.** It builds `cmd/core` once from the working tree,
   runs `core migrate` against the fresh database, and seeds it with
   `core seed` and `DEMO_SEED=1`. The seed draws its demo data through Go's
   global `math/rand` source, which auto-seeds randomly at startup; the
   harness runs it with `GODEBUG=randautoseed=0` and `TZ=Etc/UTC`, so the
   same draw sequence lands on every run and every machine. (The draw
   sequence is consumed inside map-iteration loops, so which customer owns
   which drawn value still varies per run; see the identity mask below for
   what that rules out.) After the seed finishes, the harness inserts its
   own clock-window fixture rows through SQL on the fresh database (see
   "Clock-window fixture rows" below).
3. **Real server.** It runs the real server - the `serve` role of the one
   `core` binary, `core serve` - as a subprocess on a free port with
   `AUTH_MODE=dev` (the same shape as CI's backend job). The whole wiring of
   `core/internal/app/serve` (middleware order, schedulers,
   adapters) is the behaviour under characterisation, so the harness
   exercises the production entry point rather than reconstructing the
   handler in process, which would fork the wiring and drift. The subprocess
   runs in its own process group, stopped by that group number even when the
   test fails, and carries `Pdeathsig=SIGKILL` on Linux so a killed test
   process takes the server with it.
4. **Script.** It replays a fixed, ordered script of 981 steps across 100
   groups (one golden file per group; 202 steps in the first 44 groups, the
   rest added by R1-1b). Writes run in a deterministic order, so
   sequence-derived values (order numbers, journal entry numbers) land the
   same way every run. Later steps reference values extracted from earlier
   responses (the order whose invoice is read, the invoice whose payment is
   recorded) through `{placeholders}` resolved at run time; `{today±N}`
   resolves to the seed day plus N days.
5. **Compare.** Each response's status code, content type and body is
   normalised and compared to the golden file under
   `core/internal/characterization/testdata/goldens/<group>.json`, which
   also records the request (method, path, notable headers, body) so the
   golden is self-describing. A run that crosses UTC midnight between seed and
   the end of the script fails with a clear message instead of recording or
   comparing a transcript that straddles the day boundary; re-running gives a
   clean run.

### Subprocess environments are built from an allow list

Every subprocess the harness starts (`go build`, `core migrate`, `core seed`,
`core serve`) gets an environment built from a fixed allow list (`PATH`,
`HOME`, `GOCACHE`, `GOMODCACHE`, `GOFLAGS`, `GOPATH`, `TMPDIR`) plus the
harness's explicit values - never from `os.Environ()`. A variable in a
developer's shell (`INTEGRATION_API_KEY`, `RUN_PAYMENTS_*`, `AVALARA_*`,
`CORS_ORIGINS`, ...) configures the server and would silently change the
goldens per machine; `TestSubprocessEnvBlocksOutsideVariables` pins the allow
list.

### Clock-window fixture rows

The demo seed lands every payment and invoice it writes on the seed day, so
the clock windows' edges had no rows on either side: a revenue trend of 6
instead of 7 days, or a yesterday comparison reaching two days back, answered
identically and passed the goldens. After seeding, the harness therefore
inserts fixture rows through its own SQL - mirroring the payment, invoice and
vendor-invoice repositories' INSERT statements (same columns, dollar amounts,
CHECK-constrained method and status values), never product code - at known day
offsets from the seed day, each at UTC midnight so the run's time of day
cannot flip an edge:

- payments at day-1 and day-2: the summary's yesterday window is
  [day-1, day0) - inside and outside its left edge.
- payments at day-6, day-7 and day-8: the revenue trend's 7-day window -
  day-6 is the oldest day fully inside, day-7 sits on the boundary (its
  midnight precedes the request's now-of-day, so it is out, and a widened
  window pulls it in), day-8 is clearly outside.
- AR invoices and AP bills at day-29 and day-31: inside and outside the sales
  summary's 30-day default window, and on either side of the AR aging current
  bucket's 30-day edge (aged by due date).
- AR invoices and AP bills at day-61 and day-91: the AR aging 61-90 bucket's
  edges, past the AP aging 60-day boundary.

The AR invoices belong to a harness-created customer, not a seeded one: a
seeded customer's aging total varies per run (the draw assignment), while the
fixture customer's row is exactly these invoices on every run. Only the rows
the windows read are written; the product's follow-on writes when a payment
happens through the API (invoice status transition, GL postings) are not
simulated, and no goldened query observes their absence.

### What is normalised, and why

Values that legitimately vary between two runs of the same script on the same
code are replaced with stable placeholders; everything else compares byte for
byte.

| Pattern | Placeholder | Reason |
|---|---|---|
| UUIDs (row ids, request ids, dev-mode POS cashier ids) | `<id-1>`, `<id-2>`, ... in order of first appearance | generated per run; the same value keeps its placeholder across the whole group |
| RFC 3339 timestamps and Postgres text timestamps | `<ts+30d>`, `<ts-2d>`, `<ts+0d>` | absolute times are per-run, but their day offset from the seed day is behaviour: a net-30 due date that becomes net-0 changes `<ts+30d>` to `<ts+0d>` and fails. The placeholder is emitted only after the match asserts the timestamp's shape, so a format change cannot hide behind it. The within-day time rides on run timing and stays free |
| Calendar dates | `<day+30>`, `<day-45>`, `<day+0>` | same, at day granularity |
| Generated API keys (`sk_live_...`), JWTs | `<api-key>`, `<jwt>` | random by construction |
| `parse_time_ms` | `<ms>` | a timing measurement, not behaviour |
| `/healthz/ready` `uptime` | `<uptime>` | a duration since process start |
| `/healthz/ready` `pool_total`/`pool_idle`/`pool_in_use` | `<poolstat>` | the pool census at request time (`pool_max` stays pinned: configured, not observed) |
| exposure event `idempotency_key` | `<idem-key>` | a hash over the event's creation instant |
| quote analytics `avg_days_to_close` | `<days>` | at this base it averages only the quotes the script closes milliseconds after creating them; the seeded book has none |
| `insurance_expiry`, `next_service_date` (vehicles) | `<ts>` | fixed calendar dates hardcoded in the demo seed's vehicle fixture; unlike every other seed date they move with the calendar, not the seed clock, so a seed-day offset would drift daily |

Placeholders are numbered by walking the transcript (paths, request bodies,
headers, response bodies) with map keys in sorted order, so the numbering is
independent of Go's random map iteration.

What `<ts+Nd>` does not detect: a change that keeps the same day offset - a
wrong hour of day, or a UTC-offset shift that stays inside the same calendar
day - is invisible, because the within-day time is deliberately free. Day
arithmetic (net terms, window edges) is what the offset pins; time-of-day is
not.

### Customer identity is masked on three per-customer reports

The seed's draw sequence is fixed, but it is consumed inside map-iteration
loops, so which customer owns which drawn amount is random per run. On the
three per-customer reports - AR aging, top customers, order activity - the
amounts, buckets, counts, dates and the sorted row order are stable (they
come from the draws), and only the names on the rows vary. Those steps
therefore mask the customer identity fields (`customer_id`, `customer_name`,
and a customer's `name`) to the class placeholder `<customer>` before
comparison and pin everything else, including row order: rows tied on an
amount differ only in masked identity. The mask is recorded per step in the
golden (`mask_customer_identity`), like `sort_primary_array`, so a reader can
see what is and is not pinned.

Top customers additionally masks `order_count` (recorded as
`mask_order_count`), for a different reason: the script itself writes orders
for one seeded customer - the quote and integration converts - so the count
on a rank owned by that customer carries the script's own orders on top of
the randomly assigned segment's count. The revenue sequence, the row order
and the identities' class stay pinned; that one count provably varies (two
recordings of the same code differ only there, while six fresh seeds show a
fixed multiset of per-customer revenue and count pairs before the script's
writes).

Not normalised, because they are real behaviour: money, status strings, field
names, null versus empty array, error codes and messages, content types
(including the responses that serve JSON sniffed as `text/plain;
charset=utf-8` because the handler never sets the header).

### Binary bodies (printed PDFs)

Printed documents are PDFs whose glyph positions depend on the rendered width
of the per-run uuids they contain, so no byte-level hash can be stable. The
golden records the byte length and a SHA-256 over the document's extracted
text (page content streams, Flate-decompressed first, same volatile-value
normalisation applied): it pins what the document says, not where each glyph
sits.

### Order-insensitive steps

`sort_primary_array: true` appears on steps whose response's primary array
(the body's top-level array, or the `data` array of a paged envelope) is
sorted before comparison. Two steps carry it today, both because the base's
own ordering is unstable: `integration.orders` (the dispatch-day fixture
gives all its orders one `created_at`, and the handler's
`ORDER BY o.created_at, o.id, ...` tiebreaks on the random row ids) and
`ap.aging` (ordered by total with no tiebreak). Both responses are complete,
unpaginated row sets, so sorting hides nothing; arrays nested inside each
element - an order's lines, above all - keep their wire order and stay
pinned. The flag is recorded in the golden so a reader can see what is and is
not pinned.

### Endpoints deliberately not goldened

**The P and L default window.** The clock-window group's profit-and-loss step
uses an explicit 30-day window rather than the default: the default start is
the first of the current month, a calendar boundary that makes the window's
contents differ between a run on the 5th and one on the 25th. The explicit
window exercises the same code path with a stable one.

**Routes with no recorded step.** Four census routes have none; see "Routes
without a recorded step" at the end of this document, each with its reason.

## R1-1b: depth additions

R1-1b added a recorded step for every route in `core/api/ROUTES.txt` but one
(56 new groups, 779 new steps). The new groups all run after the clock group,
so none of their writes can move an earlier golden: the first 44 golden files
are byte-identical to their R1-1 recording. The harness gained the following,
each used only where a route needed it.

- **Order and ownership.** The new groups run in this order after
  `clockwindow`: `machine_key`, `events`, `idempotency`, then the R1-1b module
  groups, then `delivery_delivered` last of all: completing a delivery as
  DELIVERED invoices its order, and an invoice moves the statement, aging and
  ledger reads of any group after it. They create their own fixtures (customers, vehicles, tills, quotes,
  ...) for anything destructive; the few that share a seed or earlier fixture
  only read it. The POS groups share register `REG-01` with the `pos` group
  (the API cannot create a register): `pos_till_close` closes its till and
  reopens another, so the register ends the run with an open till.
- **A step can set up fixture rows** (`setup` on a step): SQL through the
  harness, for state the API cannot create. The `events` group uses it to
  insert a SENT quote whose exposure rollup is ACK_REQUIRED (mirroring the
  pricing package's own acknowledgement fixture), so a real acknowledgement
  can be recorded.
- **A step can be a database probe** (`sql` on a step): a query run in a read
  only transaction that is always rolled back (a write through it fails, and a
  test pins that), whose rows are the recorded response (request method `SQL`,
  content type `application/x-sql-rows`). Two uses. The `machine_key` audit
  row: no route reads `audit_log` (the users listing unions only user
  attributed rows and a key is never a user), so the probe pins the row itself
  (action, `actor_kind` key, the key as `actor_id`, a null `user_id`, the
  refused method, path and scope). The invoices of the `delivery_delivered`
  order: no route lists invoices by order, so the probe pins the count before
  and after a refused completion and the invoice a DELIVERED completion makes.
  The conformance test skips probe steps.
- **A step can mask mock coordinates** (`maskMockGeo`, recorded as
  `mask_mock_geo`): `latitude` and `longitude` on delivery rows. With no routing
  key configured the mock geocoder derives them from two bytes of the order's
  random uuid, so they differ per run, but always inside 0.128 degrees of the
  demo anchor. Only a number inside that band is replaced (by the anchor, so the
  schema still validates); a null coordinate or one outside the band stays in the
  golden, so a delivery that loses its coordinates or gets a wrong one changes
  it. A test pins that reach.
- **A step can mask its whole body** (`maskBody`, recorded as `mask_body`, the
  body stored as `<body>`): only the status and content type stay pinned. Used
  by the three portal reads named under "Routes without a recorded step"; the
  conformance test checks those two against the contract and skips the body.
- **A step can pin response headers** (`captureHeaders`, recorded under the
  response's `headers`): used for `Idempotency-Replayed` in the `idempotency`
  group. No other header is recorded.
- **A step can mask named response fields** (`maskFields`, recorded in the
  golden as `mask_fields`, like the older mask flags): used where one field is
  derived from a per-run id or the calendar and everything else on the step
  stays pinned. Today: `photo_url` on proof of delivery photos (the stored file name
  carries eight random hex characters), `vendor_id` on the purchase order list
  (the seed assigns vendors from rand draws consumed inside map iteration, so
  the rows tie on the sort key and the placeholder numbering would shift), and
  `name`, `start_date` and `end_date` on the fiscal period list (twelve
  monthly periods of the current calendar year: the year and the dates move
  with the calendar, ids, status and order stay pinned), and `length` on the
  xlsx export (a zip whose byte length differs between hosts; the step pins
  status and content type).
- **A group can run against its own server** (`serverEnv` on a group): the
  group gets a second `core serve` process on the same database with extra
  variables, stopped when the group ends. The two category pricing groups
  (`pricing_categories`, `pricing_category_rules`) use it with
  `CATEGORY_PRICING_ENABLED=true`: with the flag off (the main server, and so
  every other golden) the whole route family is unregistered and the mux
  answers 404, which is why no earlier golden could see these routes.
- **One normaliser extension**: `quote_short_id` (the first eight characters of
  a quote uuid, carried in the exposure notification payload the events feed
  serves) normalises to `<short-id>`.

### Cross-cutting groups

| Group | What it pins |
|---|---|
| `machine_key` | The server's machine key wiring under `AUTH_MODE=dev`. A key minted with scope `quotes:read` reads a quote (200); `PUT /quotes/{id}/state` is refused 403 (`machine key lacks required scope quotes:write`, the ADR error envelope); the refusal's audit row is read through the probe step; `GET /admin/keys` is refused 403 (user only); an unknown `sk_live_` key is 401. The raw key never appears: the normaliser replaces it with `<api-key>` in the request header. |
| `events` | `GET /api/v1/events` after a real quote exposure acknowledgement: the fixture quote's exposure, the acknowledge (200), the feed filtered to `quote.exposure.acknowledged` (one item, the cursor, the envelope), an unknown type (400) and an unknown parameter (400). |
| `idempotency` | One `POST /api/v1/vendors` with `Idempotency-Key`: the first answer (201), the replay (same body and vendor id, `Idempotency-Replayed: true`, no second vendor), and the same key with a different body (422 `idempotency_key_reused`). |

## Recorded oddities (not defects of the goldens)

The goldens record the base as it is, including answers a later cycle may
change on purpose. Worth knowing before reading them: many service level
validation failures answer 500 (unknown ids on PUT/DELETE of vehicles,
drivers, contacts, a portal project, bank reconciliation dates, inventory
transfer validation, purchase order receive and submit, the three BI exports
that name columns the schema does not have, governance RFC listing which
scans a NULL column); several missing-id deletes answer 204 or 200; the
portal cart item update, `POST /orders/reorder` and an empty-cart checkout
answer 500 (the cart update writes a column the table does not have); and the
AI backed routes (`pim/generate/*`, freight upload) answer their no-key refusal.
Each is a step you can find by name in its group file; none is a golden bug.
Two quirks sit in `core/api/conformance-known.txt`: a PATCH on a missing
product's dimensions answers 200 with an empty body, and the file route on a
manual quote answers 200 with an empty body where the fragment says 404.

## Running and re-recording

The harness follows the repo's integration test convention: it skips with the
standard reason when `DATABASE_URL` is unset, and runs as part of plain
`go test -race ./...` where it is set (CI runs it in the backend job with no
special wiring). Re-recording with `-update` is refused when `CI` is set:
golden re-recording is a local, reviewed act.

```sh
# from core/, against a throwaway Postgres:
DATABASE_URL='postgres://gable_user:gable_password@127.0.0.1:<port>/gable_test?sslmode=disable' \
  go test ./internal/characterization/

# re-record every golden after a deliberate behaviour change (explicit, reviewed act):
DATABASE_URL='postgres://.../gable_test?sslmode=disable' \
  go test ./internal/characterization/ -update
```

A missing or drifted golden fails with a line diff between the recorded and
actual transcripts. A golden file with no group in the script (a deleted or
renamed scenario) fails `TestGoldenFilesMatchCurrentGroups`. Re-recording is
only correct together with an entry in `docs/refactor/CONTRACT-CHANGES.md`
naming what changed and why.

### Notes for maintainers

- The rate limiter is per client IP (120 per minute). The script is sequential
  and short, but the harness rotates `X-Forwarded-For` every 30 requests so a
  429 can never leak into a golden as a timing artefact on slow machines. The
  server only believes that header from a trusted proxy, so the harness starts
  it with `TRUSTED_PROXIES` set to loopback, the address the harness client
  connects from; without that the rotation would be ignored. The
  portal login's own strict limit is not reached (two logins).
- The harness sends no `Idempotency-Key` header except in the `idempotency`
  group, which pins the replay itself, so the idempotency cache never
  interferes anywhere else.
- The partner surface answers 401 under `AUTH_MODE=dev` (it needs ERP JWT
  claims, and dev mode mounts no auth middleware); that refusal is its golden.
- The A2A purchase-order receiver mounts only when FB Brain is enabled with a
  public key; the harness environment configures neither, so
  `POST /api/v1/a2a/purchase-order` answers the unmuxed 404. That is the
  mount-gated shape at this base, recorded as it is.
- `POST /api/v1/quotes/{id}/exposure/escalate-now` answers 500 at this base
  (its preview computation fails on a quote with no escalator lines);
  recorded as it is. The acknowledge and override writes answer the service's
  409 "already cleared" refusal for a quote whose exposure is clear - the
  honest base behaviour for a clean quote, and a pin on the guard itself.
- The AI_LM contract goldens in `internal/integrations` (the mirror-struct
  contract tests) predate this harness and stay exactly as they are; the
  harness adds black-box goldens for the same routes.

## Coverage

One read and one write for every module with routes, plus the quote state
machine edge by edge, the exposure surface, the clock-window reports and
cheap error paths (404s, validation 400s, one 405 text/plain answer). Groups
run in the order listed; the order matters (the location group must run
before anything writes audit rows, the gl group before the flows that post to
the general ledger, the exposure group after the quote group, the clock group
last).

| Module | Golden group | Routes covered |
|---|---|---|
| location | `location` | GET/POST /api/v1/branches; GET/PUT/DELETE /api/v1/branches/{id}; GET /api/v1/branches/{id}/users; GET/POST /api/v1/locations; GET /api/v1/locations/{id}; GET /api/v1/me/branches; GET /api/v1/users; GET/POST/DELETE /api/v1/users/{sub}/branches; PUT /api/v1/users/{sub}/home-branch; plus the not-a-branch and no-grant error paths |
| platform health | `health` | GET /healthz/live; GET /health; GET /healthz/ready (pool census and uptime normalised); DELETE /healthz/live (405); POST /api/v1/a2a/purchase-order (unmounted 404; see notes) |
| product | `product` | GET /api/v1/products/{id}; GET /api/v1/products; POST /api/v1/products; GET bad uuid (400); GET unknown id (404) |
| customer (incl. price levels) | `customer` | POST /api/v1/customers; GET /api/v1/customers/{id}; GET /api/v1/customers; GET /api/v1/price_levels; GET unknown id (404) |
| salesteam | `salesteam` | GET /api/v1/sales-team |
| crm | `crm` | POST /api/v1/customers/{id}/activities; GET /api/v1/customers/{id}/activities; invalid activity_type (400) |
| gl | `gl` | GET /api/v1/gl/accounts; POST /api/v1/gl/journal-entries; POST /api/v1/gl/journal-entries/{id}/post; unbalanced entry (400) |
| quote | `quote` | POST /api/v1/quotes; GET /api/v1/quotes/{id}; GET /api/v1/quotes; PUT /api/v1/quotes/{id} (DRAFT edit); PUT /api/v1/quotes/{id}/state through every allowed transition (DRAFT to SENT/ACCEPTED/REJECTED/EXPIRED, SENT to ACCEPTED/REJECTED/EXPIRED, REJECTED to DRAFT, EXPIRED to DRAFT) and the refused ACCEPTED to SENT (400); POST /api/v1/quotes/{id}/convert; GET /api/v1/quotes/analytics |
| pricing exposure | `exposure` | GET /api/v1/quotes/{id}/exposure (before and after an event); POST .../request-ack; POST .../acknowledge (409 cleared); POST .../override (409 cleared); POST .../escalate-now (500, see notes); GET /api/v1/quotes/exposure?owner=all (list and summary) |
| order | `order` | POST /api/v1/orders; GET /api/v1/orders/{id}; GET /api/v1/orders?limit=1 (envelope + newest = this script's order); GET /api/v1/orders/{id}/exposure-gate; POST /api/v1/orders/{id}/cancel (204 then 409); POST /api/v1/orders/{id}/exposure-override; GET unknown id (404) |
| invoice (via order flow) | `invoice` | POST /api/v1/orders/{id}/confirm and /fulfill (204s; the invoice-creating flow); GET /api/v1/invoices; GET /api/v1/invoices/{id}; POST /api/v1/invoices/{id}/credit-memo; GET /api/v1/credit-memos/{customerId}; GET unknown id (404) |
| payment | `payment` | POST /api/v1/payments; GET /api/v1/invoices/{id}/payments; POST /api/v1/payments/intent (503 with no gateway configured) |
| account | `account` | GET /api/v1/accounts/{id}; GET /api/v1/accounts/{id}/transactions (pins the nullable array) |
| deposit | `deposit` | POST /api/v1/deposits; GET /api/v1/deposits/{id}; zero amount (400) |
| tax | `tax` | GET /api/v1/tax/exemptions/{customerID} (empty, pins []); POST /api/v1/tax/exemptions; POST /api/v1/tax/preview |
| vendor | `vendor` | POST /api/v1/vendors; GET /api/v1/vendors/{id}; GET /api/v1/vendors |
| purchase_order | `purchase_order` | POST /api/v1/purchase-orders; GET /api/v1/purchase-orders/{id}; bad vendor_id (400) |
| ap | `ap` | POST /api/v1/ap/invoices; GET /api/v1/ap/invoices/{id}; GET /api/v1/ap/invoices |
| clock windows | `clockwindow` | GET /api/v1/ap/aging; GET /api/v1/reports/ar-aging (identity-masked); GET /api/v1/reports/sales-summary (default 30-day window); GET /api/v1/dashboard/summary; GET /api/v1/dashboard/revenue-trend; GET /api/v1/dashboard/top-customers (identity-masked); GET /api/v1/dashboard/order-activity (identity-masked); GET /api/v1/gl/trial-balance; GET /api/v1/gl/profit-and-loss?start={today-30}&end={today}; GET /api/v1/gl/balance-sheet - the now-relative windows pinned through the seed-day date offsets and the harness's fixture rows on their edges |
| edi | `edi` | POST /api/v1/edi/partners; GET /api/v1/edi/partners/{id}; missing name (400) |
| matching | `matching` | GET /api/v1/matching/config; PUT /api/v1/matching/config |
| bankrecon | `bankrecon` | POST /api/v1/bankrecon/accounts; GET /api/v1/bankrecon/accounts |
| pos | `pos` | POST /api/v1/pos/till/open; GET /api/v1/pos/till/current; POST /api/v1/pos/transactions; GET /api/v1/pos/catalog |
| pricing | `pricing` | GET /api/v1/pricing/rules; POST /api/v1/pricing/rules; GET /api/v1/pricing/calculate; calculate without params (400) |
| pricing rebates | `rebate` | POST /api/v1/pricing/rebates/programs; GET /api/v1/pricing/rebates/programs?vendor_id= |
| pricing escalators | `escalator` | GET /api/v1/market-indices; PUT /api/v1/market-indices/{id} |
| delivery | `delivery` | POST /api/v1/delivery/vehicles; GET /api/v1/delivery/vehicles; POST /api/v1/delivery/drivers; GET /api/v1/delivery/drivers/{id} |
| inventory | `inventory` | GET /api/v1/inventory?product_id= (empty, pins null); POST /api/v1/inventory/adjust; missing product_id (400) |
| document | `document` | GET /api/v1/documents/print/pickticket/{orderId} (PDF, text hash); unknown id (404) |
| reporting | `reporting` | POST /api/v1/reporting/save; GET /api/v1/reporting/saved; POST /api/v1/reporting/schedules; GET /api/v1/reporting/schedules; missing fields (400) |
| dashboard | `dashboard` | GET /api/v1/dashboard/inventory-alerts (clock-free read); the clock-window reads live in `clockwindow` |
| pim | `pim` | GET /api/v1/products/{id}/pim/content (synthetic empty); PUT /api/v1/products/{id}/pim/content; GET again |
| parsing | `parsing` | POST /api/v1/parsing/upload (multipart; the no-AI-key synthetic answer) |
| vision | `vision` | POST /api/v1/vision/scan; empty text (400) |
| millwork app | `millwork` | POST /api/v1/millwork/options; GET /api/v1/millwork/options?category= |
| configurator app | `configurator` | GET /api/v1/configurator/options?attribute_type=; POST /api/v1/configurator/validate; POST /api/v1/configurator/build-sku; empty selections (400) |
| governance app | `governance` | POST /api/v1/governance/rfcs; GET /api/v1/governance/rfcs/{id} |
| partner surface | `partner` | GET /api/partner/v1/dashboard (the dev-mode 401) |
| portal | `portal` | GET /api/portal/v1/config; POST /api/portal/v1/login; GET /api/portal/v1/catalog; POST /api/portal/v1/cart/items; GET /api/portal/v1/cart; GET /api/portal/v1/dashboard, /invoices, /deliveries (status and content type only, body masked) |
| project | `project` | POST /api/portal/v1/projects; GET /api/portal/v1/projects/{id} |
| techadmin | `techadmin` | GET /api/v1/admin/keys; POST /api/v1/admin/keys (generated key normalised) |
| staff roster | `staff` | GET /api/v1/admin/staff; POST /api/v1/admin/staff; GET /api/v1/admin/modules; missing fields (400) |
| apps registry | `apps` | GET /api/v1/apps; POST /api/v1/apps/product/disable (409 core); POST /api/v1/apps/governance/disable; POST /api/v1/apps/governance/enable; unknown key (404) |
| integrations (AI_LM) | `integrations` | no key and wrong key (401 `invalid integration key`); GET /api/integration/locations, /vehicles, /drivers, /products?category=, /orders?date=; POST /api/integration/quotes; POST /api/integration/quotes/bulk-price; POST /api/integration/quotes/{id}/accept-and-convert; POST /api/integration/validate-staff (known and unknown); POST /api/integration/delivery-routes |
| customer contacts and policy | `customer_contacts` | GET/POST /api/v1/customers/{customerId}/contacts; GET/PUT/DELETE /api/v1/contacts/{id}; GET/PUT /api/v1/customers/{id}/escalation-policy; PATCH /api/v1/customers/{id}/salesperson, each with error paths |
| salesteam detail | `sales_team_detail` | GET /api/v1/sales-team/{id} |
| crm activity item | `crm_activity_item` | GET/PUT/DELETE /api/v1/activities/{id} |
| location item | `location_item` | GET /api/v1/branches/{id}/tree; PUT/DELETE /api/v1/locations/{id} |
| product item | `product_item` | GET /api/v1/products/reorder-alerts; PATCH /api/v1/products/{id}/dimensions, /lead-time, /margins |
| tax exemption item | `tax_exemption_item` | DELETE /api/v1/tax/exemptions/{id} |
| deposits | `deposit_apply` | GET /api/v1/deposits; POST /api/v1/deposits/{id}/apply |
| payment gateway | `payment_gateway` | POST /api/v1/payments/card (402, no gateway); POST /api/v1/payments/refund (500, no gateway) |
| documents | `document_invoice` | GET /api/v1/documents/print/invoice/{id} (PDF text hash); POST /api/v1/invoices/{id}/email (202, async) |
| quote file | `quote_file` | GET /api/v1/quotes/{id}/file |
| inventory transfer | `inventory_transfer` | POST /api/v1/inventory/transfer |
| ap payments | `ap_payments` | POST /api/v1/ap/invoices/{id}/approve (401/400 only: dev mode has no claims, so no approver); GET/POST /api/v1/ap/payments |
| gl | `gl_accounts_entries`, `gl_fiscal_periods` | POST /api/v1/gl/accounts; PUT /api/v1/gl/accounts/{id}; GET /api/v1/gl/journal-entries and /{id}; POST .../{id}/reverse and /void; GET /api/v1/gl/fiscal-periods; POST .../{id}/close and /reopen |
| matching | `matching_runs` | GET /api/v1/matching/exceptions; GET /api/v1/matching/results/{po_id}; POST /api/v1/matching/run/{po_id} |
| bankrecon | `bankrecon_sessions` | POST /api/v1/bankrecon/import, /match, /unmatch; GET/POST /api/v1/bankrecon/sessions; GET /api/v1/bankrecon/sessions/{id}; POST .../{id}/complete |
| edi | `edi_partner_catalog` | GET /api/v1/edi/partners; PUT/DELETE /api/v1/edi/partners/{id}; GET /api/v1/edi/partners/{id}/catalog; POST .../import-catalog (an X12 832 segment stream carried in a JSON string; raw and CSV uploads are not buildable by the harness) |
| delivery | `delivery_fleet`, `delivery_routes`, `delivery_deliveries`, `delivery_pod_photo`, `delivery_route_lifecycle`, `delivery_delivered` | vehicles and drivers get/put/delete/photo; routes list/create/optimize/reorder/dispatch/complete/deliveries; deliveries create/get/adjust-qty/pod-photo(s)/status (PARTIAL and FAILED are recorded on the shared order; DELIVERED has its own group, `delivery_delivered`, on an order of its own: a missing proof of delivery is refused with no invoice, then a DELIVERED completion with proof reads back the delivery and the invoice it made) |
| pos | `pos_transactions`, `pos_sync`, `pos_returns`, `pos_till_close` | products/search; transactions list/get/items POST and DELETE/complete/void; returns list/create/get; sync; till close/report/zreport; zreports |
| purchase_order | `purchase_order_flow`, `purchase_order_list` | list; recommendations; refresh-reorder-targets; reorder-check; reorder-runs; source-summary; freight GET/POST (the upload refuses with no AI key, so apply is recorded only for an unknown charge); receive; submit |
| pricing (flag off main) | `pricing_escalation`, `market_index_refresh`, `exposure_admin`, `rebate_programs` | POST /api/v1/pricing/calculate-escalation; GET /api/v1/market-indices/{id}/history; POST .../refresh and /refresh/preview; POST /api/v1/admin/exposure-scan; GET /api/v1/reports/exposure; GET /api/v1/pricing/rebates/programs/{id}, /claims; POST .../claims/calculate |
| pricing categories (flag on) | `pricing_categories`, `pricing_category_rules` | categories GET/POST/PUT; category-rules GET/POST/PUT/DELETE, bulk POST/DELETE, audit; matrix; resolve (own server, see above) |
| reporting | `reporting_builder`, `reporting_saved`, `reporting_reports` | builder preview/export; export/{entity} (all 500 at this base); saved get/put/delete/run; schedules DELETE; customer-statement; daily-till |
| pim | `pim_media` | products/{id}/detail; collateral GET/DELETE; media GET/DELETE/primary PATCH; generate/* (500, no AI key) |
| techadmin | `techadmin_keys`, `techadmin_settings` | DELETE /api/v1/admin/keys/{id}; settings/ai and settings/routing GET/PUT/DELETE (invented placeholder values only) |
| staff detail | `staff_detail` | GET/PUT /api/v1/admin/staff/{id}; PUT /api/v1/admin/modules/{id}; POST/DELETE /api/v1/admin/staff/{id}/modules |
| governance | `governance_rfcs` | GET /api/v1/governance/rfcs (500); PUT /api/v1/governance/rfcs/{id} |
| configurator | `configurator_reads` | GET /api/v1/configurator/presets and /rules |
| server | `server_routes` | `/uploads/` (the file server's plain 404) |
| partner | `partner_quotes` | GET /api/partner/v1/quotes and /quotes/{id} (the dev-mode 401) |
| portal | `portal_catalog`, `portal_orders`, `portal_billing`, `portal_quotes`, `portal_team`, `portal_logout` | catalog/{id}, /volume-breaks, /categories; cart item PUT (500) and DELETE; checkout; orders list (project filtered, since, 304), get, cancel, reorder (500), project; invoices/{id}; deliveries/{id}, reschedule GET/POST; quotes list/create/get/accept/decline; users list/role/status; invites GET/POST; logout |
| project | `portal_projects` | GET /api/portal/v1/projects; PUT /api/portal/v1/projects/{id} |
| machine keys | `machine_key` | see Cross-cutting groups |
| events | `events` | GET /api/v1/events |
| idempotency | `idempotency` | POST /api/v1/vendors replayed |

Modules without routes of their own (notification, ai, domain, config,
testutil, the x12 and gl integration libraries) are exercised through the
modules that wire them.

## Routes without a recorded step

Every route in `core/api/ROUTES.txt` has a recorded step except one, for a
stated reason:

- `GET /metrics`: the body is the Prometheus exposition of Go runtime and
  process series (`go_build_info`, memory statistics, goroutines, start time)
  that differ on every run; the normaliser has no rule for them and R1-1b adds
  none.

Three portal reads have a step that pins less than the rest:
`GET /api/portal/v1/dashboard`, `GET /api/portal/v1/invoices` and
`GET /api/portal/v1/deliveries` return the demo customer's seeded book, whose
composition (amounts, which orders, invoices and deliveries exist) is drawn
from the seed inside map-iteration loops and so differs per run. Their steps
(`portal.dashboard`, `portal.invoices`, `portal.deliveries`, in the `portal`
group) pin the status and content type and record the body as the placeholder
`<body>` (`mask_body`). The conformance test checks the status and content type
against the contract and skips the body of such a step. The shape of those
bodies is covered through their sibling reads: `GET /orders` (project
filtered), `GET /invoices/{id}` and `GET /deliveries/{id}` on fixtures the
script creates.

A route a later cycle changes needs its golden re-recorded together with an
entry in `docs/refactor/CONTRACT-CHANGES.md`, as before.
