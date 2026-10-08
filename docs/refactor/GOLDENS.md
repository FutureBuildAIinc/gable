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
4. **Script.** It replays a fixed, ordered script of 202 requests across 44
   groups (one golden file per group). Writes run in a deterministic order, so
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

The demo seed also writes its 13 dispatch-day orders with one shared
created_at (the dispatch date's midnight, two days back), and the dashboard's
order-activity read is `ORDER BY created_at DESC LIMIT 10` with no tiebreak.
Which of the 13 appeared in the ten newest, and in what order, therefore
followed the physical row order and the plan's top-N sort, and an unlucky
tuple layout (autovacuum timing in a loaded full run) changed a `total_amount`
in the `clockwindow` transcript. After the clock-window rows the harness gives
each dispatch order its own created_at, a few milliseconds after the shared
midnight so the recorded day offset does not move, in a fixed newest-first
order (`dispatchRecencyOrder` in `fixtures_clock_test.go`) that reproduces the
recorded golden byte for byte, and it fails the run if any of the newest 25
orders ever share a created_at again. The missing tiebreak in the read itself
is product behaviour and is left as found.

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

**Depth deferred to R1-1b.** Every module with routes now has a golden (the
R1-1 exit bar); the remaining 194 of the 345 census routes without one are
listed at the end of this document.

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
- The harness sends no `Idempotency-Key` headers, so the idempotency cache
  never interferes.
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
| portal | `portal` | GET /api/portal/v1/config; POST /api/portal/v1/login; GET /api/portal/v1/catalog; POST /api/portal/v1/cart/items; GET /api/portal/v1/cart |
| project | `project` | POST /api/portal/v1/projects; GET /api/portal/v1/projects/{id} |
| techadmin | `techadmin` | GET /api/v1/admin/keys; POST /api/v1/admin/keys (generated key normalised) |
| staff roster | `staff` | GET /api/v1/admin/staff; POST /api/v1/admin/staff; GET /api/v1/admin/modules; missing fields (400) |
| apps registry | `apps` | GET /api/v1/apps; POST /api/v1/apps/product/disable (409 core); POST /api/v1/apps/governance/disable; POST /api/v1/apps/governance/enable; unknown key (404) |
| integrations (AI_LM) | `integrations` | no key and wrong key (401 `invalid integration key`); GET /api/integration/locations, /vehicles, /drivers, /products?category=, /orders?date=; POST /api/integration/quotes; POST /api/integration/quotes/bulk-price; POST /api/integration/quotes/{id}/accept-and-convert; POST /api/integration/validate-staff (known and unknown); POST /api/integration/delivery-routes |

Modules without routes of their own (notification, ai, domain, config,
testutil, the x12 and gl integration libraries) are exercised through the
modules that wire them.

## Deferred to R1-1b: census routes without a golden

151 of the 345 census routes carry a golden. The remaining 194, by module
(paths abbreviated; every one needs a scenario step plus a recorded golden
before its module's conversion in R1-7):

- portal (29): cart item PUT/DELETE; catalog/{id} and /volume-breaks;
  catalog/categories; checkout; dashboard; deliveries (list, get,
  reschedule GET/POST); invites GET/POST; invoices (list, get); logout;
  orders (list, get, cancel, reorder, set-project); quotes (list, create,
  get, accept, decline); users (list, role, status).
- delivery (21): deliveries (create, get, adjust-qty, pod-photo, pod-photos,
  status); drivers (list, PUT, DELETE, photo); routes (list, create,
  complete, deliveries, dispatch, optimize, reorder); vehicles (get, PUT,
  DELETE, photo).
- pricing (21): admin/exposure-scan; market-indices/{id}/history,
  /refresh, /refresh/preview; pricing/calculate-escalation; categories
  (list, create, PUT); category-rules (list, create, bulk POST/DELETE, PUT,
  DELETE, audit); matrix; rebates program get, claims get/calculate;
  pricing/resolve; reports/exposure.
- pos (15): products/search; returns (list, create, get); sync;
  till close/report/zreport; transactions (list, get, complete, items
  POST/DELETE, void); zreports.
- purchase_order (11): list; recommendations; refresh-reorder-targets;
  reorder-check; reorder-runs; source-summary; freight GET/POST/apply;
  receive; submit.
- reporting (10): builder export/preview; export/{entity}; saved get/PUT/
  DELETE/run; schedules DELETE; customer-statement; daily-till.
- pim (10): products/{id}/detail; collateral GET/DELETE/generate;
  generate descriptions/image/seo; media GET/DELETE/PATCH.
- gl (9): accounts POST/PUT; fiscal-periods (list, close, reopen);
  journal-entries (list, get, reverse, void).
- customer (8): contacts (get, POST, PUT, DELETE); escalation-policy
  GET/PUT; salesperson PATCH.
- bankrecon (7): import; match; unmatch; sessions (list, create, get,
  complete).
- techadmin (7): keys DELETE; settings/ai GET/PUT/DELETE;
  settings/routing GET/PUT/DELETE.
- staff (5): staff get/PUT; modules PUT; staff modules POST/DELETE.
- product (4): reorder-alerts; dimensions/lead-time/margins PATCHes.
- ap (3): approve; payments GET/POST.
- crm (3): activities get/PUT/DELETE.
- location (3): branches/{id}/tree; locations PUT/DELETE.
- matching (3): exceptions; results/{po_id}; run/{po_id}.
- partner (2): quotes list/get (all partner routes answer the dev-mode 401;
  one 401 is pinned, the rest are the same refusal).
- configurator (2): presets; rules.
- deposit (2): list; apply.
- document (2): print/invoice/{id}; invoices/{id}/email.
- governance (2): rfcs list; PUT.
- payment (2): card; refund.
- project (2): list; PUT.
- server (2): /metrics; /uploads/.
- inventory (1): transfer.
- quote (1): /quotes/{id}/file.
- salesteam (1): /sales-team/{id}.
- tax (1): exemptions DELETE.
