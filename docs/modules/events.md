<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Events feed

`GET /api/v1/events` is the read API over the transactional outbox
([ADR 0003](../adr/0003-events-outbox.md)). A consumer uses it to tail
the dealer's events in commit order: a Lumber Price Exposure email
notifier that wants to ship its own message, a third party integration
that has to mirror orders and invoices in another system, a back office
job that rebuilds a report from the event stream. The same table the
feed reads is the table the drain reads to deliver the in-process bus
notifications ([ADR 0003](../adr/0003-events-outbox.md) section 4), so
the outside consumer sees exactly what the inside subscribers see.

This module is the work of R1-12 (event feed) and R1-12b
(lifecycle). The Go code is in `core/internal/events/`, the table and
drain are in `core/pkg/outbox/` (the writer) and migration 089
(`core/migrations/089_events_outbox.sql`). The shared wire contract is
in `core/api/fragments/events.yaml`. Every other module writes here
through the same `outbox.Write` seam inside its transaction.

## What it does in a yard

A yard operator wants to know every change that mattered since a
moment in time: a customer was put on hold, a quote was sent, an
order was confirmed, an invoice was paid, a refund cleared,
a milestone on a customer went over the credit limit. The feed carries
all of them in one paged stream the operator or an integration can
consume at its own pace.

A consumer reads the first page, keeps the `next_cursor`, and asks
again for the next page some moments later (a polling integration or a
cron job). The cursor is the last served `position` of the table (the
ordering scope `events.position`), so a page resumed from a cursor
never skips an event that committed after the page was served
([ADR 0003](../adr/0003-events-outbox.md) section 2). The feed names
its `next_cursor` even when the page is empty; that is the one
exception to ADR 0001's null at end rule, recorded in
[ADR 0003](../adr/0003-events-outbox.md) section 5: a poller adopts the
tail cursor without re-reading its previous page.

A consumer that wants a single module's events names them under the
`types` filter and paged smaller. The drain, the in-process subscriber
that delivers notifications, has its own cursor and the same table
([ADR 0003](../adr/0003-events-outbox.md) section 4); operators inspect
the parked rows (`event_subscriber_parked`) when a subscriber handler
keeps failing.

## Route

The route below is in `core/api/fragments/events.yaml`; the handler is
in `core/internal/events/handler.go`; the route census
(`core/api/ROUTES.txt`) lists it under `internal/events`.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/events` | One cursor paged page of events in commit order; filter `types`, optional `include=total`. |

No other route lives on the events feed (it is read only). Every other
module's mutating routes are where events get written into the
outbox.

## The cursor and its ordering guarantee

The cursor scope is `events.position` (`core/internal/events/handler.go`,
`cursorScope` constant). The `position` column is the primary key of
`events_outbox`, drawn from the `events_outbox_position_seq` sequence
inside a Postgres advisory transaction lock (`outbox` lock key
1869462626, the ASCII bytes `outb`, `core/migrations/089_events_outbox.sql`
lines 13 to 25; enforced by the BEFORE INSERT trigger at lines 75 to
99). That makes `position` order equal to commit order among writers
on the same database: a writer cannot draw a position until every
lower positioned writer's transaction has finished, so a reader paged
on `position > cursor` cannot skip a row that commits late. A rolled
back writer burns its position; gaps are expected and harmless.

`ListEvents` (`core/pkg/outbox/outbox.go`, the function the handler
calls) reads rows with `position > $1` in `position` order, with an
optional `type = ANY($2)` filter and a `LIMIT`. The handler asks for
one page at most 200 at once; an out of range limit is a 400.

`next_cursor` is always present on a successful page, including an
empty one. The feed's writer mints the cursor with the last served
position (or with the request's cursor echoed back when the page was
empty), so a poller can adopt the tail cursor and resume without
re-reading. The `limit` of the envelope is the requested page size,
not the number of items returned.

## Filters and envelope

The query parameters are exactly `cursor`, `limit`, `types` and
`include`. Any other name is a 400 `unsupported_query_parameter`; a
repeated `include` is a 400 (`internal/events/handler.go`, the
`include` parsing at the `q["include"]` branch).

| Parameter | Wire form | Default | Note |
|---|---|---|---|
| `cursor` | opaque string | absent means first page | Minted by `httpx.MintCursor` with the cursor scope `events.position`; a malformed cursor is a 400 naming `cursor`. |
| `limit` | integer 1 to 200 | 50 | Malformed or out of range is a 400 naming `limit`. |
| `types` | repeatable, comma separated, exact matches only | absent reads every type | Names must be dot-delimited lowercase (the same rule as `outbox.ValidType`); a name outside the shape or a repetition is a 400 naming `types`; a filter matching nothing is an empty page. The read API takes no wildcard syntax. |
| `include` | string | absent | Only `total` is known; `total` adds the count of matching events to the envelope. |

The list envelope is the ADR 0001 shape (`core/api/fragments/events.yaml`
`components.schemas.EventPage`):

| Field | Wire form | Note |
|---|---|---|
| `items` | array of `Event` | The page. |
| `next_cursor` | string | Always present, an explicit exception to ADR 0001's null at end rule ([ADR 0003](../adr/0003-events-outbox.md) section 5). |
| `limit` | integer | The requested page size. |
| `total` | integer, present only under `include=total` | The count of matching events. |

Each item is the snake_case event envelope named in
[ADR 0001](../adr/0001-wire-contract.md) section 12
(`core/internal/events/handler.go` `Item`, the wire schema in
`core/api/fragments/events.yaml` `components.schemas.Event` and `EventEntity`):

| Field | Wire form | Note |
|---|---|---|
| `event_id` | UUID | The logical id consumers dedup on; also the bus subject suffix. |
| `type` | dot delimited lowercase | The event type. |
| `org` | text | The deployment's org slug; `default` while one database per dealer is the rule. |
| `branch_id` | UUID, nullable | The event's branch scope, filled from the request's branch context when the caller did not set one. |
| `entity` | `{kind, id}` | The entity the event is about. |
| `data` | JSON object | The small summary payload the writer records; shape depends on `type` (see the vocabulary table below). |
| `at` | RFC 3339 UTC | Timestamp at microsecond precision. |

The feed is not branch scoped. The route takes no `X-Branch-Id`
header; the role guard passes anyone in the admin gate. The
`branch_id` of an event is the branch its writer pinned, recorded in
the row, and is information a consumer filters in its own store if it
needs the gate.

## Branch scope on the feed

`GET /api/v1/events` is not branch scoped; the events of every branch
the deployment serves are in one feed. An outside consumer that wants
the events of one branch filters on `branch_id` in the returned items
in its own store: the events were recorded against the writer's
branch, and the `branch_id` field is the truthful one. The role guard
is the admin reads' narrowest gate ([ADR 0003](../adr/0003-events-outbox.md)
section 3): `admin` and `owner`. See
`core/internal/app/serve/serve.go` (`eventsHandler.RegisterRoutes`),
the only place the events route is wired.

## Scopes, roles and keys

A machine key reaching `GET /api/v1/events` needs the segment scope
`events:read` (the first path segment under `/api/v1/` is the scope
segment; `pkg/middleware/machinekey.go`, the scope table at the
`"events"` entry, paired with the read/write split the serve layer
applies: `events:read` for `GET`, `events:write` is reserved for
future write scopes). A key without the scope is `403 forbidden`;
the audit row carries the refused scope ([ADR 0002](../adr/0002-machine-keys.md)
section 2).

The user guard at the serve layer is `admin` and `owner`
(`core/internal/app/serve/serve.go` `eventsHandler.RegisterRoutes`,
`middleware.RequireRole("admin", "owner")`). A machine key holding the
`events:read` scope passes the same role gate that the `admin` and
`owner` roles pass.

## Event vocabulary on the wire today

The vocabulary below is the set of event types the core writers
record as `Type:` on the outbox. Every entry was found in code by
walking each module's `service.go` and `model.go` and listing the
constants it writes through the outbox (the `outbox.Event{Type: ...}`
and `outbox.Write` call sites). The table reports the constant name,
its module's source line, and the writer site; CONTRACT-CHANGES
(section "Events (R1-12)") names additional events the refactor
items will ship on later conversions, but those conversions have not
landed yet and their writers are not in the tree.

| Event type | Module | Constant and source | Writer (file and function) |
|---|---|---|---|
| `customer.created` | customer | `EventCreated` (`core/internal/customer/service.go:58`) | `core/internal/customer/service.go:263` (`record` of `Service`) |
| `customer.updated` | customer | `EventUpdated` (`core/internal/customer/service.go:59`) | `core/internal/customer/service.go:364`, `462`, `516`; also a balance write at `core/internal/account/effects.go:161` (`EventCustomerUpdated` from `core/internal/account/model.go:61`) |
| `activity.created` | crm | `EventCreated` (`core/internal/crm/service.go:40`) | `core/internal/crm/service.go` `record` (the module's row builder, see the audit row at line 125) |
| `activity.updated` | crm | `EventUpdated` (`core/internal/crm/service.go:41`) | `core/internal/crm/service.go` `record` |
| `activity.deleted` | crm | `EventDeleted` (`core/internal/crm/service.go:42`) | `core/internal/crm/service.go` `record` |
| `project.created` | project | `EventCreated` (`core/internal/project/service.go:40`) | `core/internal/project/service.go:124` (`record` of `Service`) |
| `project.updated` | project | `EventUpdated` (`core/internal/project/service.go:41`) | `core/internal/project/service.go:183` (`record` of `Service`) |
| `quote.created` | quote | `EventCreated` (`core/internal/quote/service.go:64`) | `core/internal/quote/service.go:482`; also the portal at `core/internal/portal/quote.go:432` |
| `quote.sent` | quote | `EventSent` (`core/internal/quote/service.go:65`) | `core/internal/quote/service.go:638` (`transitionEvents[to]` map) |
| `quote.accepted` | quote | `EventAccepted` (`core/internal/quote/service.go:66`) | `core/internal/quote/service.go:744` |
| `quote.rejected` | quote | `EventRejected` (`core/internal/quote/service.go:67`) | `core/internal/quote/service.go:638` |
| `quote.expired` | quote | `EventExpired` (`core/internal/quote/service.go:68`) | `core/internal/quote/service.go:638` |
| `quote.reopened` | quote | `EventReopened` (`core/internal/quote/service.go:69`) | `core/internal/quote/service.go:638` |
| `quote.exposure.flagged` | pricing (exposure) | `SubjectExposureFlagged` (`core/pkg/eventbus/eventbus.go:75`) | `core/internal/pricing/exposure_service.go:431` (through `SubjectForEvent`, `exposure_model.go:55`) |
| `quote.exposure.escalated` | pricing (exposure) | `SubjectExposureEscalated` (`core/pkg/eventbus/eventbus.go:76`) | `core/internal/pricing/exposure_service.go:431` |
| `quote.exposure.ack_required` | pricing (exposure) | `SubjectExposureAckRequired` (`core/pkg/eventbus/eventbus.go:77`) | `core/internal/pricing/exposure_service.go:431` |
| `quote.exposure.acknowledged` | pricing (exposure) | `SubjectExposureAcknowledged` (`core/pkg/eventbus/eventbus.go:78`) | `core/internal/pricing/exposure_service.go:431` |
| `quote.exposure.cleared` | pricing (exposure) | `SubjectExposureCleared` (`core/pkg/eventbus/eventbus.go:79`) | `core/internal/pricing/exposure_service.go:431` |
| `order.created` | order | `EventCreated` (`core/internal/order/service.go:70`) | `core/internal/order/service.go:748`, `1579` (the record helper at `service.go:1411`) |
| `order.updated` | order | `EventUpdated` (`core/internal/order/service.go:71`) | `core/internal/order/service.go:828` |
| `order.confirmed` | order | `EventConfirmed` (`core/internal/order/service.go:72`) | `core/internal/order/service.go:1105`, `1326` |
| `order.hold` | order | `EventHold` (`core/internal/order/service.go:73`) | `core/internal/order/service.go:1132` |
| `order.hold_released` | order | `EventHoldReleased` (`core/internal/order/service.go:74`) | `core/internal/order/service.go:1099` |
| `order.backordered` | order | `EventBackordered` (`core/internal/order/alloc.go:33`) | `core/internal/order/service.go:1110`, `1330`; `core/internal/order/invoice_void.go:112` |
| `order.backorder_released` | order | `EventBackorderReleased` (`core/internal/order/alloc.go:34`) | `core/internal/order/alloc.go:353` |
| `order.reopened` | order | `EventReopened` (`core/internal/order/service.go:75`) | `core/internal/order/service.go:1151`; `core/internal/order/invoice_void.go:120` |
| `order.cancelled` | order | `EventCancelled` (`core/internal/order/service.go:76`) | `core/internal/order/service.go:1179` |
| `order.closed_short` | order | `EventClosedShort` (`core/internal/order/service.go:77`) | `core/internal/order/service.go:1210` |
| `order.partially_fulfilled` | order | `EventPartiallyFulfilled` (`core/internal/order/fulfil.go:27`) | `core/internal/order/fulfil.go:647` |
| `order.fulfilled` | order | `EventFulfilled` (`core/internal/order/fulfil.go:28`) | `core/internal/order/fulfil.go:647` |
| `order.fulfillment_parked` | order | `EventFulfillmentParked` (`core/internal/order/fulfil_queue.go:16`) | `core/internal/order/fulfil_queue.go` (queue parked rows) |
| `invoice.created` | invoice, order | `EventInvoiceCreated` (`core/internal/order/fulfil.go:29`) | `core/internal/order/fulfil.go:873` (`recordInvoice`, the fulfilment's invoice creation) |
| `invoice.paid` | order | inline string `"invoice.paid"` | `core/internal/order/fulfil.go:888` (zero total, payment inside fulfilment) |
| `invoice.partial` | account | `EventInvoicePartial` (`core/internal/account/model.go:54`) | `core/internal/account/effects.go:145` |
| `invoice.paid` | account | `EventInvoicePaid` (`core/internal/account/model.go:55`) | `core/internal/account/effects.go:150` |
| `invoice.voided` | invoice | `EventInvoiceVoided` (`core/internal/invoice/model.go:252`) | `core/internal/invoice/service.go:429` |
| `invoice.written_off` | account | `EventInvoiceWrittenOff` (`core/internal/account/model.go:56`) | `core/internal/account/effects.go:152` |
| `invoice.reopened` | account | `EventInvoiceReopened` (`core/internal/account/model.go:57`) | `core/internal/account/effects.go:148` |
| `credit_memo.created` | invoice | `EventCreditCreated` (`core/internal/invoice/model.go:254`) | `core/internal/invoice/service_cm.go:245` |
| `credit_memo.updated` | invoice | `EventCreditUpdated` (`core/internal/invoice/model.go:255`) | `core/internal/invoice/service_cm.go:323` |
| `credit_memo.posted` | invoice | `EventCreditPosted` (`core/internal/invoice/model.go:256`) | `core/internal/invoice/service_cm.go:459` |
| `credit_memo.voided` | invoice | `EventCreditVoided` (`core/internal/invoice/model.go:257`) | `core/internal/invoice/service_cm.go:591` |
| `credit_memo.applied` | account | `EventCreditApplied` (`core/internal/account/model.go:59`) | `core/internal/account/effects.go:136` |
| `credit_memo.partial` | account | `EventCreditPartial` (`core/internal/account/model.go:58`) | `core/internal/account/effects.go:131` |
| `credit_memo.reopened` | account | `EventCreditReopened` (`core/internal/account/model.go:60`) | `core/internal/account/effects.go:134` |
| `credit_memo.refunded` | payment | inline string `"credit_memo.refunded"` | `core/internal/payment/service_card.go:263` |
| `payment.recorded` | payment | `EventRecorded` (`core/internal/payment/service.go:273`) | `core/internal/payment/service.go:267` (`recordedEvent` of `Service`) |
| `payment.applied` | account | `EventPaymentApplied` (`core/internal/account/model.go:52`) | `core/internal/account/effects.go:115` |
| `payment.unapplied` | account | `EventPaymentUnapplied` (`core/internal/account/model.go:53`) | `core/internal/account/effects.go:117` |
| `payment.refunded` | payment | `EventRefunded` (`core/internal/payment/service.go:274`) | `core/internal/payment/service.go:534` |
| `payment.voided` | payment | `EventVoided` (`core/internal/payment/service.go:275`) | `core/internal/payment/service.go:456` |
| `purchase_order.received` | purchase_order | `EventReceived` (`core/internal/purchase_order/service.go:44`) | `core/internal/purchase_order/service.go:1034` |
| `product.created` | product | `EventProductCreated` (`core/internal/product/service.go:50`) | `core/internal/product/service.go:212` (`recordEvent` of `Service`) |
| `product.updated` | product | `EventProductUpdated` (`core/internal/product/service.go:51`) | `core/internal/product/service.go:281` |
| `location.created` | location | `EventLocationCreated` (`core/internal/location/service.go:40`) | `core/internal/location/service.go:154` |
| `location.updated` | location | `EventLocationUpdated` (`core/internal/location/service.go:41`) | `core/internal/location/service.go:188` |
| `location.archived` | location | `EventLocationArchived` (`core/internal/location/service.go:42`) | `core/internal/location/service.go:205` |
| `pricing_rule.created` | pricing | `EventRuleCreated` (`core/internal/pricing/service.go:21`) | `core/internal/pricing/service.go` `recordRuleEvent` |
| `category.created` | pricing (category) | `EventCategoryCreated` (`core/internal/pricing/category_service.go:43`) | `core/internal/pricing/category_service.go:211` (`recordCategory`) |
| `category.updated` | pricing (category) | `EventCategoryUpdated` (`core/internal/pricing/category_service.go:44`) | `core/internal/pricing/category_service.go` `recordCategory` (the updated path) |
| `category_rule.created` | pricing (category) | `EventCategoryRuleCreated` (`core/internal/pricing/category_service.go:45`) | `core/internal/pricing/category_service.go:302`, `499` |
| `category_rule.updated` | pricing (category) | `EventCategoryRuleUpdated` (`core/internal/pricing/category_service.go:46`) | `core/internal/pricing/category_service.go:336`, `501` |
| `category_rule.deleted` | pricing (category) | `EventCategoryRuleDeleted` (`core/internal/pricing/category_service.go:47`) | `core/internal/pricing/category_service.go:400` |
| `staff.created` | staff | inline string `"staff.created"` | `core/internal/staff/service.go:184` (`record`) |
| `staff.updated` | staff | inline string `"staff.updated"` | `core/internal/staff/service.go:233` |
| `staff.module_granted` | staff | inline string `"staff.module_granted"` | `core/internal/staff/service.go:311` |
| `staff.module_revoked` | staff | inline string `"staff.module_revoked"` | `core/internal/staff/service.go:360` |
| `module.enabled` | staff | inline string `"module.enabled"` | `core/internal/staff/service.go:455` |
| `module.disabled` | staff | inline string `"module.disabled"` | `core/internal/staff/service.go:453` |
| `key.created` | techadmin | inline string `"key.created"` | `core/internal/techadmin/service.go:173` |
| `key.revoked` | techadmin | inline string `"key.revoked"` | `core/internal/techadmin/service.go:281` |
| `admin_settings.saved` | techadmin | inline string `"admin_settings.saved"` | `core/internal/techadmin/service.go:378`, `485` |
| `admin_settings.deleted` | techadmin | inline string `"admin_settings.deleted"` | `core/internal/techadmin/service.go:418`, `522` |
| `rfc.created` | governance | `EventCreated` (`core/internal/governance/service.go:48`) | `core/internal/governance/service.go` `record` |
| `rfc.updated` | governance | `EventUpdated` (`core/internal/governance/service.go:49`) | `core/internal/governance/service.go` `record` |
| `rfc.review` | governance | `EventReview` (`core/internal/governance/service.go:50`) | `core/internal/governance/service.go` `record` |
| `rfc.approved` | governance | `EventApproved` (`core/internal/governance/service.go:51`) | `core/internal/governance/service.go` `record` |
| `rfc.rejected` | governance | `EventRejected` (`core/internal/governance/service.go:52`) | `core/internal/governance/service.go` `record` |
| `rfc.reopened` | governance | `EventReopened` (`core/internal/governance/service.go:53`) | `core/internal/governance/service.go` `record` |

The empty rows of CONTRACT-CHANGES that the tree does not yet ship: no
`charge_code.created` or `charge_code.updated` event is written by the
charge code module (`chargecode/service.go`), so an audit of changes to a
code is today read by polling `GET /charge-codes/{id}` and diffing the
revision; the events feed is not the place to find one. An inventory or
unit module event the refactor later writes will be added to this table
when its writer lands.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 5, 12: the list envelope, the cursor, the error envelope, the strict query parameters and the field names; section 12 adopts snake_case on the events envelope and records the camelCase to snake_case change on the feed that first ships it.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2: the segment scope rule (`events:read` for `GET`).
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md): the table and the `events_outbox_position_seq` advisory lock; the ordering guarantee from the BEFORE INSERT trigger; the writer seam, the drain, the parked row; section 5 records the always present `next_cursor` exception.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the feed end to end. `make up` builds and starts the
local stack (Postgres, migrate and seed, `core serve`, `core worker`,
the web front door) on http://127.0.0.1:8080 with `AUTH_MODE=dev`;
`make down` removes it. To run the core from source instead:
`make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

The seed posts a quote, accepts it, fulfils the order, creates an
invoice, records a partial payment and applies it; the resulting
events are the first page of the feed in the demo state. Then, with
an admin role bearer:

```
curl -X GET 'http://127.0.0.1:8080/api/v1/events?limit=20' \
  -H 'Authorization: Bearer <token>'
```

To poll the tail after a known cursor (the cursor string is the
opaque `next_cursor` from the previous page):

```
curl -X GET 'http://127.0.0.1:8080/api/v1/events?cursor=<next_cursor>&limit=20' \
  -H 'Authorization: Bearer <token>'
```

To read only the order lifecycle:

```
curl -X GET 'http://127.0.0.1:8080/api/v1/events?types=order.created,order.confirmed,order.fulfilled' \
  -H 'Authorization: Bearer <token>'
```

To add the total count of matching events to the envelope:

```
curl -X GET 'http://127.0.0.1:8080/api/v1/events?include=total' \
  -H 'Authorization: Bearer <token>'
```

The handler proof in `core/internal/events/handler_test.go` pins the
happy path and the strict query errors (malformed cursor, unknown
parameter, bad limit, repeated `include`, malformed `types`); the
golden group `events` in
`core/internal/characterization/testdata/goldens/events.json` pins the
page shape and the always present `next_cursor` for the demo state.
