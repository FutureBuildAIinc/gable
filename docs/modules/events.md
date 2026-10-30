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
(retention). The Go code is in `core/internal/events/`, the table and
drain are in `core/pkg/outbox/` (the writer) and migration 089
(`core/migrations/089_events_outbox.sql`). The shared wire contract is
in `core/api/fragments/events.yaml`. Every other module writes here
through the same `outbox.Write` seam inside its transaction.

## What it does in a yard

A yard operator wants to know every change that mattered since a
moment in time: a customer was updated, a quote was sent, an order
was confirmed, an invoice was paid, a refund was recorded. The feed carries
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
91, where the trigger function (`events_outbox_assign_position`) is
created at lines 80 to 86 and the trigger itself at lines 88 to 91;
the `event_subscriber_cursors` table that follows at lines 93 to 99
is separate). That makes `position` order equal to commit order among writers
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

## Retention

The outbox is a replay window, not a ledger. The worker role purges
rows whose `at` is older than `OUTBOX_RETENTION_DAYS` (default 14;
zero or negative turns the purge off), on an hourly ticker, and never
a row a registered subscriber has not yet reached or a parked row
([ADR 0003](../adr/0003-events-outbox.md) section 6). A feed consumer
that falls further behind than the retention age finds the oldest
events gone and resumes from the oldest retained one. Rebuild a report
from the module tables, not from the feed alone.

## Filters and envelope

The query parameters are exactly `cursor`, `limit`, `types` and
`include`. Any other name is a 400 `unsupported_query_parameter`; a
repeated `include` is a 400 (`internal/events/handler.go`, the
`include` parsing at the `q["include"]` branch), and so is an
`include` name other than `total`, or a repeated `limit` or `cursor`
(`httpx.ParseListQuery`). `limit` is plain digits: no sign, space or
leading zero.

| Parameter | Wire form | Default | Note |
|---|---|---|---|
| `cursor` | opaque string | absent means first page | Minted by `httpx.MintCursor` with the cursor scope `events.position`; a malformed cursor is a 400 naming `cursor`. |
| `limit` | integer 1 to 200 | 50 | Malformed or out of range is a 400 naming `limit`. |
| `types` | repeatable, comma separated, exact matches only | absent reads every type | `types` may repeat as a parameter and carry commas; the same event name more than once, an empty value, or a name outside the dot delimited lowercase shape (the same rule as `outbox.ValidType`) is a 400 naming `types`; a filter matching nothing is an empty page. The read API takes no wildcard syntax. |
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
section 5): `admin` and `owner`. See
`core/internal/app/serve/serve.go` (`eventsHandler.RegisterRoutes`),
the only place the events route is wired.

## Scopes, roles and keys

A machine key reaching `GET /api/v1/events` needs the segment scope
`events:read` (the first path segment under `/api/v1/` is the scope
segment; `pkg/middleware/machinekey.go`, the scope table at the
`"events"` entry, paired with the read/write split the serve layer
applies: `events:read` for `GET`; no route under `events` is a
write, so `events:write` gates nothing today). A key without the
scope is `403 forbidden`; the audit row carries the refused scope
([ADR 0002](../adr/0002-machine-keys.md) section 5).

The user guard at the serve layer is `admin` and `owner`
(`core/internal/app/serve/serve.go` `eventsHandler.RegisterRoutes`,
`middleware.RequireRole("admin", "owner")`). A machine key is not
subject to the role gate; its `events:read` scope is the gate
([ADR 0002](../adr/0002-machine-keys.md) section 4).

## Event vocabulary on the wire today

The vocabulary below is the set of event types the core writers
record as `Type:` on the outbox. Every entry was found in code by
walking each module's `service.go` and `model.go` and listing the
constants it writes through the outbox (the `outbox.Event{Type: ...}`
and `outbox.Write` call sites). The table reports the constant name,
its module's source line, and the writer site; CONTRACT-CHANGES
(the R1-12 row of the contract changes table) names additional events the refactor
items will ship on later conversions, but those conversions have not
landed yet and their writers are not in the tree.

| Event type | Module | Constant and source | Writer (file and function) |
|---|---|---|---|
| `customer.created` | customer | `EventCreated` in `core/internal/customer/service.go` | `core/internal/customer/service.go`, `Service.Create` |
| `customer.updated` | customer | `EventUpdated` in `core/internal/customer/service.go` | `core/internal/customer/service.go`, `Service.Update`; `Service.SetSalesperson`; `Service.SetEscalationPolicy`; also a balance write at `core/internal/account/effects.go`, `Effects.Events` (`EventCustomerUpdated` in `core/internal/account/model.go`) |
| `activity.created` | crm | `EventCreated` in `core/internal/crm/service.go` | `core/internal/crm/service.go`, `Service.Create` (the call inside `Create` goes through the `record` helper) |
| `activity.updated` | crm | `EventUpdated` in `core/internal/crm/service.go` | `core/internal/crm/service.go`, `Service.Update` (through the `record` helper) |
| `activity.deleted` | crm | `EventDeleted` in `core/internal/crm/service.go` | `core/internal/crm/service.go`, `Service.Delete` (through the `record` helper) |
| `project.created` | project | `EventCreated` in `core/internal/project/service.go` | `core/internal/project/service.go`, `Service.Create` |
| `project.updated` | project | `EventUpdated` in `core/internal/project/service.go` | `core/internal/project/service.go`, `Service.Update` |
| `quote.created` | quote | `EventCreated` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.Create` (the `writeEvent` call inside `Create`, which emits `s.buildEvent(out, EventCreated, "")`); also the portal writer in `core/internal/portal/quote.go`, `PostgresRepository.CreatePortalQuote` |
| `quote.sent` | quote | `EventSent` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.transition` |
| `quote.accepted` | quote | `EventAccepted` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.convert` |
| `quote.rejected` | quote | `EventRejected` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.transition` |
| `quote.expired` | quote | `EventExpired` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.transition` |
| `quote.reopened` | quote | `EventReopened` in `core/internal/quote/service.go` | `core/internal/quote/service.go`, `Service.transition` |
| `quote.exposure.flagged` | pricing (exposure) | `SubjectExposureFlagged` in `core/pkg/eventbus/eventbus.go` | `core/internal/pricing/exposure_service.go`, `ExposureService.recordStatus` |
| `quote.exposure.escalated` | pricing (exposure) | `SubjectExposureEscalated` in `core/pkg/eventbus/eventbus.go` | `core/internal/pricing/exposure_service.go`, `ExposureService.recordStatus` |
| `quote.exposure.ack_required` | pricing (exposure) | `SubjectExposureAckRequired` in `core/pkg/eventbus/eventbus.go` | `core/internal/pricing/exposure_service.go`, `ExposureService.recordStatus` |
| `quote.exposure.acknowledged` | pricing (exposure) | `SubjectExposureAcknowledged` in `core/pkg/eventbus/eventbus.go` | `core/internal/pricing/exposure_service.go`, `ExposureService.recordStatus` |
| `quote.exposure.cleared` | pricing (exposure) | `SubjectExposureCleared` in `core/pkg/eventbus/eventbus.go` | `core/internal/pricing/exposure_service.go`, `ExposureService.recordStatus` |
| `order.created` | order | `EventCreated` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.createCore`; also `Service.CreateFromQuote` (both routes use the `Service.record` helper) |
| `order.updated` | order | `EventUpdated` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.updateCore` |
| `order.confirmed` | order | `EventConfirmed` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition`; also `Service.confirm` |
| `order.hold` | order | `EventHold` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition`; also `Service.confirm` |
| `order.hold_released` | order | `EventHoldReleased` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition` |
| `order.backordered` | order | `EventBackordered` in `core/internal/order/alloc.go` | `core/internal/order/service.go`, `Service.applyTransition`; `Service.confirm`; also `core/internal/order/invoice_void.go`, `Service.ReturnBilled` |
| `order.backorder_released` | order | `EventBackorderReleased` in `core/internal/order/alloc.go` | `core/internal/order/alloc.go`, `Service.allocateAndDerive` |
| `order.reopened` | order | `EventReopened` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition`; also `core/internal/order/invoice_void.go`, `Service.ReturnBilled` |
| `order.cancelled` | order | `EventCancelled` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition` |
| `order.closed_short` | order | `EventClosedShort` in `core/internal/order/service.go` | `core/internal/order/service.go`, `Service.applyTransition` |
| `order.partially_fulfilled` | order | `EventPartiallyFulfilled` in `core/internal/order/fulfil.go` | `core/internal/order/fulfil.go`, `Service.fulfil` |
| `order.fulfilled` | order | `EventFulfilled` in `core/internal/order/fulfil.go` | `core/internal/order/fulfil.go`, `Service.fulfil` |
| `order.fulfillment_parked` | order | `EventFulfillmentParked` in `core/internal/order/fulfil_queue.go` | `core/internal/order/fulfil_queue.go`, `Service.ServeFulfilmentRequest` |
| `invoice.created` | invoice, order | `EventInvoiceCreated` in `core/internal/order/fulfil.go` | `core/internal/order/fulfil.go`, `Service.recordInvoice` |
| `invoice.paid` | order | inline string `"invoice.paid"` | `core/internal/order/fulfil.go`, `Service.recordInvoice` (zero total, payment inside fulfilment) |
| `invoice.partial` | account | `EventInvoicePartial` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `invoice.paid` | account | `EventInvoicePaid` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `invoice.voided` | invoice | `EventInvoiceVoided` in `core/internal/invoice/model.go` | `core/internal/invoice/service.go`, `Service.VoidInvoice` |
| `invoice.written_off` | account | `EventInvoiceWrittenOff` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `invoice.reopened` | account | `EventInvoiceReopened` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `credit_memo.created` | invoice | `EventCreditCreated` in `core/internal/invoice/model.go` | `core/internal/invoice/service_cm.go`, `Service.CreateCreditMemo` |
| `credit_memo.updated` | invoice | `EventCreditUpdated` in `core/internal/invoice/model.go` | `core/internal/invoice/service_cm.go`, `Service.UpdateCreditMemo` |
| `credit_memo.posted` | invoice | `EventCreditPosted` in `core/internal/invoice/model.go` | `core/internal/invoice/service_cm.go`, `Service.postCredit` |
| `credit_memo.voided` | invoice | `EventCreditVoided` in `core/internal/invoice/model.go` | `core/internal/invoice/service_cm.go`, `Service.voidCredit` |
| `credit_memo.applied` | account | `EventCreditApplied` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `credit_memo.partial` | account | `EventCreditPartial` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `credit_memo.reopened` | account | `EventCreditReopened` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `credit_memo.refunded` | payment | inline string `"credit_memo.refunded"` | `core/internal/payment/service_card.go`, `Service.RefundCredit` |
| `payment.recorded` | payment | `EventRecorded` in `core/internal/payment/service.go` | `core/internal/payment/service.go`, the package level `recordedEvent` helper |
| `payment.applied` | account | `EventPaymentApplied` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `payment.unapplied` | account | `EventPaymentUnapplied` in `core/internal/account/model.go` | `core/internal/account/effects.go`, `Effects.Events` |
| `payment.refunded` | payment | `EventRefunded` in `core/internal/payment/service.go` | `core/internal/payment/service.go`, `Service.Refund` |
| `payment.voided` | payment | `EventVoided` in `core/internal/payment/service.go` | `core/internal/payment/service.go`, `Service.Void` |
| `purchase_order.received` | purchase_order | `EventReceived` in `core/internal/purchase_order/service.go` | `core/internal/purchase_order/service.go`, `Service.recordReceived` |
| `product.created` | product | `EventProductCreated` in `core/internal/product/service.go` | `core/internal/product/service.go`, `Service.CreateProduct` |
| `product.updated` | product | `EventProductUpdated` in `core/internal/product/service.go` | `core/internal/product/service.go`, `Service.UpdateAverageCost` |
| `location.created` | location | `EventLocationCreated` in `core/internal/location/service.go` | `core/internal/location/service.go`, `Service.CreateLocation` |
| `location.updated` | location | `EventLocationUpdated` in `core/internal/location/service.go` | `core/internal/location/service.go`, `Service.UpdateLocation` |
| `location.archived` | location | `EventLocationArchived` in `core/internal/location/service.go` | `core/internal/location/service.go`, `Service.DeleteLocation` |
| `pricing_rule.created` | pricing | `EventRuleCreated` in `core/internal/pricing/service.go` | `core/internal/pricing/service.go`, `Service.CreateRule` |
| `category.created` | pricing (category) | `EventCategoryCreated` in `core/internal/pricing/category_service.go` | `core/internal/pricing/category_service.go`, `CategoryPricingService.CreateCategory` |
| `category.updated` | pricing (category) | `EventCategoryUpdated` in `core/internal/pricing/category_service.go` | `core/internal/pricing/category_service.go`, `CategoryPricingService.UpdateCategory` (through `recordCategory`) |
| `category_rule.created` | pricing (category) | `EventCategoryRuleCreated` in `core/internal/pricing/category_service.go` | `core/internal/pricing/category_service.go`, `CategoryPricingService.CreateCategoryRule`; also `CategoryPricingService.BulkUpsertRules` |
| `category_rule.updated` | pricing (category) | `EventCategoryRuleUpdated` in `core/internal/pricing/category_service.go` | `core/internal/pricing/category_service.go`, `CategoryPricingService.UpdateCategoryRule`; also `CategoryPricingService.BulkUpsertRules` |
| `category_rule.deleted` | pricing (category) | `EventCategoryRuleDeleted` in `core/internal/pricing/category_service.go` | `core/internal/pricing/category_service.go`, `CategoryPricingService.DeleteCategoryRule` |
| `staff.created` | staff | inline string `"staff.created"` | `core/internal/staff/service.go`, `Service.CreateStaff` |
| `staff.updated` | staff | inline string `"staff.updated"` | `core/internal/staff/service.go`, `Service.UpdateStaff` |
| `staff.module_granted` | staff | inline string `"staff.module_granted"` | `core/internal/staff/service.go`, `Service.GrantModule` |
| `staff.module_revoked` | staff | inline string `"staff.module_revoked"` | `core/internal/staff/service.go`, `Service.RevokeModule` |
| `module.enabled` | staff | inline string `"module.enabled"` | `core/internal/staff/service.go`, `Service.SetModuleEnabled` |
| `module.disabled` | staff | inline string `"module.disabled"` | `core/internal/staff/service.go`, `Service.SetModuleEnabled` |
| `key.created` | techadmin | inline string `"key.created"` | `core/internal/techadmin/service.go`, `Service.GenerateKey` |
| `key.revoked` | techadmin | inline string `"key.revoked"` | `core/internal/techadmin/service.go`, `Service.RevokeKey` |
| `admin_settings.saved` | techadmin | inline string `"admin_settings.saved"` | `core/internal/techadmin/service.go`, `Service.SaveAISettings`; also `Service.SaveRoutingSettings` |
| `admin_settings.deleted` | techadmin | inline string `"admin_settings.deleted"` | `core/internal/techadmin/service.go`, `Service.DeleteAISettings`; also `Service.DeleteRoutingSettings` |
| `rfc.created` | governance | `EventCreated` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.DraftRFC` |
| `rfc.updated` | governance | `EventUpdated` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.UpdateRFC` |
| `rfc.review` | governance | `EventReview` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.Transition` |
| `rfc.approved` | governance | `EventApproved` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.Transition` |
| `rfc.rejected` | governance | `EventRejected` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.Transition` |
| `rfc.reopened` | governance | `EventReopened` in `core/internal/governance/service.go` | `core/internal/governance/service.go`, `Service.Transition` |
| `millwork_option.created` | millwork | `EventCreated` in `core/internal/millwork/service.go` | `core/internal/millwork/service.go`, `Service.record` |
| `unit.created` | unit | `EventUnitCreated` in `core/internal/unit/service.go` | `core/internal/unit/service.go`, `Service.record` (invoked from `Service.Create`) |
| `unit.updated` | unit | `EventUnitUpdated` in `core/internal/unit/service.go` | `core/internal/unit/service.go`, `Service.record` (invoked from `Service.Update`) |

The empty rows of CONTRACT-CHANGES that the tree does not yet ship: no
`charge_code.created` or `charge_code.updated` event is written by the
charge code module (`chargecode/service.go`), so an audit of changes to a
code is today read by polling `GET /charge-codes/{id}` and diffing the
revision; the events feed is not the place to find one. An inventory
event the refactor later writes will be added to this table when its
writer lands.

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
`cd core && AUTH_MODE=dev go run -tags devauth ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape). The seed TRUNCATEs
the orders, invoices, quotes, payments and ledger tables, so run it
only against a throwaway database.

The demo seed writes its rows directly and records no events, so a
fresh stack starts with an empty feed (`items` empty, `next_cursor`
present). Make a change through the API (create a customer, confirm an
order) and the matching events appear. Then, with an admin role bearer:

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
parameter, bad limit, an unknown `include` name, the `types` filter
and its shape refusal); the
golden group `events` in
`core/internal/characterization/testdata/goldens/events.json` pins the
page shape and the always present `next_cursor` for the
characterization fixture state.
