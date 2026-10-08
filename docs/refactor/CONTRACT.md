# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# The API contract

Item R1-7, part a: the tooling and the first module fragments. The OpenAPI
3.1 document `core/api/openapi.yaml` describes today's HTTP surface route
by route, exactly as the handlers behave, and is assembled from one
fragment per module. This document explains how to work on it: how a
fragment is written, how the document and the generated TypeScript are
regenerated, and what the drift and coverage gates check.

The transcription rule comes first because everything else follows from
it: **the fragments describe the routes as they are, never as they should
be.** A float money field is written `type: number` with a note; the
lifecycle field the quote module names `state` with UPPERCASE values is
transcribed under that name with those values; a list that can answer
`null` is typed nullable. The wire conventions of ADR 0001
(`docs/adr/0001-wire-contract.md`) land module by module after this, and
each conversion edits its fragment in the same change and lists every wire
diff in `docs/refactor/CONTRACT-CHANGES.md`. A fragment that wishes a shape
into existence is a bug: it desynchronizes the contract from the goldens
and from the code the goldens pin.

## The pieces

| Path | What it is |
|---|---|
| `core/api/fragments/*.yaml` | One fragment per module. Top-level keys are exactly `paths` and `components`. Underscore prefixed files carry shared components only. |
| `core/api/fragments/_shared.yaml` | The shared fragment: security schemes, the limit and offset parameters, the branch and idempotency headers, the standard error envelope, the integration error body, the exposure 409 payload. |
| `core/api/openapi.yaml` | The assembled document. Generated; never edited by hand. |
| `core/api/ROUTES.txt` | The route census (R1-2): every route the sources register. The truth the coverage gate counts against. |
| `core/api/contract-pending.txt` | Routes still without an operation. Generated; shrinks as fragments land, and only shrinks. |
| `core/api/tools/merge` | The assembler (Go, no new toolchain). |
| `core/cmd/pending` | Regenerates the pending list from the census and the assembled document. |
| `core/internal/apicontract` | Loads the assembled document and the census; the coverage test and the later conformance pass live against it. |
| `web/packages/api-client` | Generated TypeScript types and a thin typed client (see below). |

## Writing a fragment

A fragment is a YAML file named for its module (`quote.yaml`,
`integration.yaml`). Its shape:

```yaml
# SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# One or more comment lines saying which module this is and naming the
# quirks of today's wire shapes that the transcription preserves.
paths:
  /api/v1/things:
    get:
      operationId: thingList        # camelCase, unique across the document
      tags: [thing]                 # exactly one, the module's tag
      summary: List things
      parameters:
        - $ref: "#/components/parameters/Limit"
      responses:
        "200":
          description: The page of things.
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ThingPage"
        "401": { $ref: "#/components/responses/Unauthorized" }
components:
  schemas:
    Thing: { ... }
```

The rules the assembler enforces (each is a merge failure):

- Top-level keys are exactly `paths` and `components`. Everything a route
  needs is declared on the operation; path item level keys are refused,
  because a second fragment may own another method of the same path.
- A path's method is owned by exactly one fragment. Two fragments may each
  own methods of one path; one method may not be owned twice.
- Component names (schemas, parameters, responses, security schemes) are
  unique across every fragment. Prefix shared-looking names with the
  module (`IntegrationOrder`, `QuotePage`) so ownership is visible.
- Every operation carries a unique `operationId` and exactly one tag.
- Every templated path parameter (`{id}`, `{customerId}`, `{branch_id}`)
  is declared on the operation, inline or through a shared parameter, with
  the name byte-identical to the registration in the sources.
- Two templated paths may not collide under different parameter names.
- Every local `$ref` resolves inside the assembled document.

Transcription conventions (following the models' JSON tags):

- A Go field without `omitempty` is listed in `required`; with it, not.
- A pointer with `omitempty` serializes to an absent field, so it is
  typed without a null leg; a pointer serialized today (the PIM geometry,
  the nullable integration coordinates) is typed `type: [T, "null"]`.
- UUIDs, times and dates carry `format: uuid`, `format: date-time` and
  `format: date`; int64 cents carry `format: int64`; float money carries
  `type: number` with a note saying it is float dollars today.
- Enum vocabularies are transcribed verbatim (UPPERCASE where the module
  is UPPERCASE today, `Buyer` and `Site Super` where the contact roles
  carry spaces and case).
- Status codes are the ones the handler actually writes, including the
  500s its validation paths surface today. Success bodies that can be
  `null` (append-built repository slices) are typed nullable; the
  integration seam's lists, which the handler guarantees non null, are
  not.

A known deviation: the create handlers (quote, customer and its contact,
order, product, and the location, credit memo and integration creates)
write their JSON body without setting a Content-Type header, so the wire
answers those 201s as `text/plain; charset=utf-8` today, and the goldens
record them wrapped as `{"text": "<json>"}`. The fragments keep declaring
`application/json`, because the body is JSON and `text/plain` is simply a
missing header, not a shape; a separate item now owns fixing the handlers
to send the header, and until it lands the conformance pass must read
those bodies through the golden wrapper rather than trust the declared
content type.

## Regenerating

From the Go module root (`core/`):

```sh
go run ./api/tools/merge            # assemble core/api/openapi.yaml
go run ./cmd/pending -write         # rewrite core/api/contract-pending.txt
```

Then from `web/packages/api-client/`:

```sh
npm run generate                    # regenerate src/schema.d.ts
npm run typecheck                   # tsc over src and the usage check
```

A fragment change is committed together with the regenerated
`openapi.yaml`, `contract-pending.txt` and `schema.d.ts`. When a module
converts onto the ADR 0001 conventions, the same pull request edits the
fragment, regenerates, re-records that module's goldens, and appends the
wire diffs to `docs/refactor/CONTRACT-CHANGES.md`.

## The gates

Four checks guard the contract, all wired into CI. All four also run behind
one command at the repository root, `make contract`: its Go half
(`make contract-go`) runs the first two in CI's backend job and its
TypeScript half (`make contract-ts`) runs the last two in CI's frontend job.

1. **Assembly drift** (`go run ./api/tools/merge -check`): reassembles
   the document in memory and fails when the committed `openapi.yaml`
   differs, printing the first differing lines. An edited fragment
   without a regeneration cannot pass as fresh.
2. **Pending drift** (`go run ./cmd/pending` without `-write`): same
   comparison for `contract-pending.txt`.
3. **Coverage** (`go test ./internal/apicontract`, no database): reads
   `ROUTES.txt`, `openapi.yaml` and `contract-pending.txt` and fails
   when a census route has no operation and is not pending; when a
   pending entry already has an operation (the list only shrinks: strike
   the line when the fragment lands); when a pending entry names no
   census route; and when an operation backs no census route (a
   fragment describing a route the sources do not register, or a
   pattern spelled differently).
4. **TypeScript drift** (`npm run drift` in `web/packages/api-client`):
   regenerates the types in check mode and fails when the committed
   `schema.d.ts` is stale, so a contract change reaches the generated
   client or blocks the merge.

The route census test (R1-2, `go test ./internal/routecensus`) sits under
all of these: a route that appears in the sources without a census update
fails there before the coverage gate ever sees it.

## The conformance pass, later

The goldens (R1-1, `core/internal/characterization`) merged beside this
item; a later part of R1-7 validates every golden's response against its
operation. The seam is already built: `core/internal/apicontract` loads
the assembled document and `Spec.Find(method, concretePath)` resolves a
recorded request to its operation and extracts the path parameters,
exactly as the coverage test already uses it. Find strips any query
string recorded with the request, and when a literal route and a
templated route both match one concrete path it prefers the most specific
pattern the way net/http's ServeMux ranks them: a literal segment beats a
templated one at the first differing position, a longer run of leading
literal segments beats a shorter one, and the pattern text is the
deterministic backstop. The conformance test will Find each golden's
request, then validate the golden's response body against the operation's
declared response schema.

What that test will need, beyond the seam above:

- Response schema access: `Operation` carries only method, path and ID
  today; it needs the response schemas of the operation it resolved.
- A JSON Schema 2020-12 validator in Go. No validator is in `go.mod`, so
  the dependency and its licence need a decision before the part starts.
- Golden decoding rules: the normaliser's tokens (`<id-n>`, `<ts+0d>`,
  `<days>`) must be ignored or mapped so format assertions stay off them;
  the `{"text": ...}` wrapper the recorder writes for non JSON content
  types must be unwrapped (or the body compared by content type), and an
  empty `{"text": ""}` must count as the body of a 204.
- A skip list for goldens whose routes are still pending, which shrinks
  like `contract-pending.txt` as fragments land.
- The status check comes first: the recorded status must be declared on
  the operation, and only then is the body validated against that
  status's schema.

## The generated client

`web/packages/api-client` holds `src/schema.d.ts` (generated by
openapi-typescript 7.13.0, pinned in devDependencies beside typescript
5.9.3, the desk's own line; no runtime dependency at all) and
`src/index.ts`, a thin typed client over the platform `fetch`: paths,
query parameters, path parameters and bodies type from the generated
contract, non 2xx responses raise an `ApiError` carrying the error body
and the `X-Request-ID`, and idempotency keys ride as a per-call option.
`test/usage.ts` is a type-level usage check: the happy paths must
compile, and the wrong calls sit under `@ts-expect-error`, so the typing
cannot rot when the contract changes. The desk wires the client in R1-8;
until then the package ships types and client without a consumer.

## Module status

Fragments done (the pattern the rest copy):

| Module | Fragment | Routes |
|---|---|---|
| quote | `quote.yaml` | 8 |
| customer | `customer.yaml` | 12 |
| order | `order.yaml` | 8 |
| invoice | `invoice.yaml` | 4 |
| payment | `payment.yaml` | 5 |
| product | `product.yaml` | 7 |
| location | `location.yaml` | 18 |
| integration (AI_LM) | `integration.yaml` | 10 |
| shared components | `_shared.yaml` | 0 |

72 of 345 census routes covered. Pending (in `contract-pending.txt`,
later parts R1-7b onward): portal, pricing, delivery, pos, reporting, gl,
purchase_order, pim, techadmin, bankrecon, staff, edi, ap, dashboard,
crm, configurator, tax, project, governance, deposit, matching, vendor,
partner, inventory, document, salesteam, millwork, account, vision,
parsing, the apps registry under `pkg/apps`, the A2A receiver, the health
and metrics endpoints of `cmd/server`, and the `/uploads/` file pattern.

The AI_LM integration routes (`/api/integration/*`) keep their own
published contract shapes: the ADR 0001 in place rule exempts that seam
until its own scoped key migration, and its wire types are mirrored by
the AI load management service's client and machine checked on that side.
