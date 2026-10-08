# Characterisation goldens (R1-1)

The characterisation harness is the safety net of the whole refactor: it pins
today's HTTP surface, response by response, so that any behaviour change the
refactor makes is a deliberate, listed change rather than an accident. It was
recorded against commit `8361f23b` and every later change to a response must
either keep its golden byte-identical after normalisation or re-record it with
a reason in `docs/refactor/CONTRACT-CHANGES.md`.

## How the harness works

The harness lives in `backend/internal/characterization` (all in `_test.go`
files; `doc.go` exists so the package builds). One test,
`TestCharacterisationGoldens`, does the following on every run:

1. **Own database.** It reads the Postgres server from `DATABASE_URL` and
   creates a fresh database on it with a random name (`gv1_goldens_<hex>`),
   dropped at the end of the run with `DROP DATABASE ... WITH (FORCE)`. Other
   packages' tests share the `DATABASE_URL` database, so the harness never
   touches it beyond `CREATE`/`DROP` of its own.
2. **Migrate and seed.** It builds `cmd/migrate`, `cmd/seed` and `cmd/server`
   from the working tree, migrates the fresh database, and seeds it with
   `DEMO_SEED=1`. The seed draws its demo data through Go's global `math/rand`
   source, which auto-seeds randomly at startup; the harness runs it with
   `GODEBUG=randautoseed=0` and `TZ=Etc/UTC`, so the same demo data lands on
   every run and every machine.
3. **Real server.** It runs the real `cmd/server` binary as a subprocess on a
   free port with `AUTH_MODE=dev` (the same shape as CI's backend job). The
   whole wiring of `cmd/server/main.go` (middleware order, schedulers,
   adapters) is the behaviour under characterisation, so the harness exercises
   the production entry point rather than reconstructing the handler in
   process, which would fork the wiring and drift. The subprocess runs in its
   own process group, recorded by pid, and is stopped by that group number
   even when the test fails.
4. **Script.** It replays a fixed, ordered script of 133 requests across 41
   groups (one golden file per group). Writes run in a deterministic order, so
   sequence-derived values (order numbers, journal entry numbers) land the
   same way every run. Later steps reference values extracted from earlier
   responses (the order whose invoice is read, the invoice whose payment is
   recorded) through `{placeholders}` resolved at run time.
5. **Compare.** Each response's status code, content type and body is
   normalised and compared to the golden file under
   `backend/internal/characterization/testdata/goldens/<group>.json`, which
   also records the request (method, path, notable headers, body) so the
   golden is self-describing.

### What is normalised, and why

Values that legitimately vary between two runs of the same script on the same
code are replaced with stable placeholders; everything else compares byte for
byte.

| Pattern | Placeholder | Reason |
|---|---|---|
| UUIDs (row ids, request ids, dev-mode POS cashier ids) | `<id-1>`, `<id-2>`, ... in order of first appearance | generated per run; the same value keeps its placeholder across the whole group |
| RFC 3339 timestamps and Postgres text timestamps | `<ts>` | seeded rows are dated relative to the seed clock |
| Calendar dates | `<date>` | same, at day granularity |
| Generated API keys (`sk_live_...`), JWTs | `<api-key>`, `<jwt>` | random by construction |
| `parse_time_ms` | `<ms>` | a timing measurement, not behaviour |

Placeholders are numbered by walking the transcript (paths, request bodies,
headers, response bodies) with map keys in sorted order, so the numbering is
independent of Go's random map iteration.

Not normalised, because they are real behaviour: money, status strings, field
names, null versus empty array, error codes and messages, content types
(including the fourteen responses that serve JSON sniffed as
`text/plain; charset=utf-8` because the handler never sets the header).

### Binary bodies (printed PDFs)

Printed documents are PDFs whose glyph positions depend on the rendered width
of the per-run uuids they contain, so no byte-level hash can be stable. The
golden records the byte length and a SHA-256 over the document's extracted
text (page content streams, Flate-decompressed first, same volatile-value
normalisation applied): it pins what the document says, not where each glyph
sits.

### Order-insensitive steps

`sort_body_arrays: true` appears on steps whose response arrays are sorted
(recursively) before comparison. The only such step today is
`integration.orders`: the dispatch-day fixture gives all its orders one
`created_at`, and the handler's `ORDER BY o.created_at, o.id, ...` tiebreaks
on the random row ids, so the wire order of that array is genuinely unstable
at this base. The step pins the content of every order and line instead; the
flag is recorded in the golden so a reader can see what is and is not pinned.

### Endpoints deliberately not goldened

Endpoints whose answer is a window over the clock are real behaviour but shift
every day relative to the seeded, now-relative demo dates, so a golden of them
would be stale tomorrow rather than wrong when the code changes. Each module
below is covered through its date-free reads and writes instead. Not
goldened: AP aging (`/api/v1/ap/aging`), AR aging and default-window sales
summary (`/api/v1/reports/ar-aging`, `/api/v1/reports/sales-summary` without
explicit dates), dashboard `summary`/`revenue-trend`/`top-customers`/
`order-activity`, GL `trial-balance`/`profit-and-loss`/`balance-sheet` (their
default `as_of` windows), market index history windows, and exposure
`days_open`. When R1-6 gives these endpoints stable contract shapes, the
goldens can be extended with fixed-date variants.

## Running and re-recording

The harness follows the repo's integration test convention: it skips with the
standard reason when `DATABASE_URL` is unset, and runs as part of plain
`go test -race ./...` where it is set (CI runs it in the backend job with no
special wiring).

```sh
# from backend/, against a throwaway Postgres:
DATABASE_URL='postgres://gable_user:gable_password@127.0.0.1:<port>/gable_test?sslmode=disable' \
  go test ./internal/characterization/

# re-record every golden after a deliberate behaviour change (explicit, reviewed act):
DATABASE_URL='postgres://.../gable_test?sslmode=disable' \
  go test ./internal/characterization/ -update
```

A missing or drifted golden fails with a line diff between the recorded and
actual transcripts. Re-recording is only correct together with an entry in
`docs/refactor/CONTRACT-CHANGES.md` naming what changed and why.

### Notes for maintainers

- The rate limiter is per client IP (120 per minute). The script is sequential
  and short, but the harness rotates `X-Forwarded-For` every 30 requests so a
  429 can never leak into a golden as a timing artefact on slow machines. The
  portal login's own strict limit is not reached (two logins).
- The harness sends no `Idempotency-Key` headers, so the idempotency cache
  never interferes.
- The partner surface answers 401 under `AUTH_MODE=dev` (it needs ERP JWT
  claims, and dev mode mounts no auth middleware); that refusal is its golden.
- The AI_LM contract goldens in `internal/integrations` (the mirror-struct
  contract tests) predate this harness and stay exactly as they are; the
  harness adds black-box goldens for the same routes.

## Coverage

One read and one write for every module with routes, plus cheap error paths
(a 404, a validation 400, and in one place the 405 text/plain answer). Groups
run in the order listed; the order matters (see the invoice and apps notes).

| Module | Golden group | Routes covered |
|---|---|---|
| platform health | `health` | GET /healthz/live; GET /health; DELETE /healthz/live (405) |
| product | `product` | GET /api/v1/products/{id}; GET /api/v1/products; POST /api/v1/products; GET bad uuid (400); GET unknown id (404) |
| customer (incl. price levels) | `customer` | POST /api/v1/customers; GET /api/v1/customers/{id}; GET /api/v1/customers; GET /api/v1/price_levels; GET unknown id (404) |
| salesteam | `salesteam` | GET /api/v1/sales-team |
| crm | `crm` | POST /api/v1/customers/{id}/activities; GET /api/v1/customers/{id}/activities; invalid activity_type (400) |
| gl | `gl` | GET /api/v1/gl/accounts; POST /api/v1/gl/journal-entries; POST /api/v1/gl/journal-entries/{id}/post; unbalanced entry (400) |
| quote | `quote` | POST /api/v1/quotes; GET /api/v1/quotes/{id}; GET /api/v1/quotes |
| order | `order` | POST /api/v1/orders; GET /api/v1/orders/{id}; GET /api/v1/orders/{id}/exposure-gate; GET unknown id (404) |
| invoice (via order flow) | `invoice` | POST /api/v1/orders/{id}/confirm and /fulfill (204s; the invoice-creating flow); GET /api/v1/invoices; GET /api/v1/invoices/{id}; POST /api/v1/invoices/{id}/credit-memo; GET /api/v1/credit-memos/{customerId}; GET unknown id (404) |
| payment | `payment` | POST /api/v1/payments; GET /api/v1/invoices/{id}/payments; POST /api/v1/payments/intent (503 with no gateway configured) |
| account | `account` | GET /api/v1/accounts/{id}; GET /api/v1/accounts/{id}/transactions (pins the nullable array) |
| deposit | `deposit` | POST /api/v1/deposits; GET /api/v1/deposits/{id}; zero amount (400) |
| tax | `tax` | GET /api/v1/tax/exemptions/{customerID} (empty, pins []); POST /api/v1/tax/exemptions; POST /api/v1/tax/preview |
| vendor | `vendor` | POST /api/v1/vendors; GET /api/v1/vendors/{id}; GET /api/v1/vendors |
| purchase_order | `purchase_order` | POST /api/v1/purchase-orders; GET /api/v1/purchase-orders/{id}; bad vendor_id (400) |
| ap | `ap` | POST /api/v1/ap/invoices; GET /api/v1/ap/invoices/{id}; GET /api/v1/ap/invoices |
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
| dashboard | `dashboard` | GET /api/v1/dashboard/inventory-alerts (the clock-free read) |
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
| integrations (AI_LM) | `integrations` | no key and wrong key (401 `invalid integration key`); GET /api/integration/locations, /vehicles, /drivers, /products?category=, /orders?date=; POST /api/integration/quotes; POST /api/integration/validate-staff (known and unknown); POST /api/integration/delivery-routes |

Modules without routes of their own (notification, ai, domain, config,
testutil, the x12 and gl integration libraries) are exercised through the
modules that wire them.
