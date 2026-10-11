<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Delivery

The delivery module is the yard's fleet, the day's routes, the stops on
each route, and the proof of delivery that closes them out. Vehicles and
drivers are dealer-wide master records; routes and stops are walled by
the branches of their orders, as the Branch wall section sets out. The dispatch board
reads routes and their stops in one payload; the mobile app reads the
driver's run for the day, writes the proof, and closes the stop.

The Go code is in `core/internal/delivery/`. The model and the lifecycle
enums are in `core/internal/delivery/model.go`. The service, the
lifecycle transitions and the event writers are in
`core/internal/delivery/service.go`. The handler and the routes it
serves are in `core/internal/delivery/handler.go`. The request shapes
are in `core/api/fragments/delivery.yaml`; the route census is
`core/api/ROUTES.txt` under `internal/delivery`. The migration that
brought the storage shape onto the wire contract is
`core/migrations/104_delivery_wire_contract.sql`.

## What it does in a yard

A yard uses the delivery module for four things:

- The fleet: vehicles and drivers, with their photos, capacity,
  licence and CDL fields, and the dealer-wide lists.
- The day's routes: one route per run, on a scheduled date, with a
  vehicle, a driver, the joined names, the stop count, and the
  stored duration and distance totals.
- The stops and the proof of delivery: each stop carries an order,
  an address, a sequence number, the geocode, the proof photo, the
  signature, and the status that closes it out.
- The board: the route list with `include=stops` embeds every stop
  under its route in one payload, so the dispatcher's screen reads
  the day's work in a single round trip.

## Routes

Every route the delivery handler registers is in `core/api/ROUTES.txt`
under `internal/delivery` (rows 164 to 188) and in
`core/internal/delivery/handler.go` in `Handler.RegisterRoutes`. The
list below mirrors the path each route serves on the wire and the one
line each operationId gives.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/delivery/vehicles` | Cursor list of vehicles, dealer-wide; `include=total` counts. |
| POST | `/api/v1/delivery/vehicles` | Create a vehicle; `name`, `vehicle_type`, `license_plate` required; no precondition; 201 with `Location` and `ETag`. |
| GET | `/api/v1/delivery/vehicles/{id}` | Get a vehicle, dealer-wide. |
| PUT | `/api/v1/delivery/vehicles/{id}` | Update a vehicle; `If-Match` or body `revision` (428 without, 409 stale). |
| DELETE | `/api/v1/delivery/vehicles/{id}` | Soft delete a vehicle; `If-Match` required; a missing vehicle is a 404. The module does not check whether a route still references it. |
| POST | `/api/v1/delivery/vehicles/{id}/photo` | Upload a vehicle photo (multipart, one file, jpg/jpeg/png/webp, at most 10 MB); no precondition; moves the revision. |
| GET | `/api/v1/delivery/drivers` | Cursor list of drivers, dealer-wide; `include=total` counts. |
| POST | `/api/v1/delivery/drivers` | Create a driver; `name` required; no precondition. |
| GET | `/api/v1/delivery/drivers/{id}` | Get a driver, dealer-wide. |
| PUT | `/api/v1/delivery/drivers/{id}` | Update a driver; `If-Match` or body `revision`; `status` is required on update (default `active` on create). |
| DELETE | `/api/v1/delivery/drivers/{id}` | Soft delete a driver; `If-Match` required; a missing driver is a 404. The module does not check whether a route still references it. |
| POST | `/api/v1/delivery/drivers/{id}/photo` | Upload a driver photo (multipart, one file, jpg/jpeg/png/webp, at most 10 MB); no precondition; moves the revision. |
| GET | `/api/v1/delivery/routes` | Cursor list of routes, behind the branch wall; filters `date`, `driver_id`, `status`; `include=stops` embeds each route's stops (the board read); `include=total` counts; `cursor`, `limit`, `include` on the platform list; unknown parameters are a 400. |
| POST | `/api/v1/delivery/routes` | Create a route; `vehicle_id`, `driver_id`, `scheduled_date` required; route starts `draft`; no precondition; 201 with `Location` and `ETag`. |
| GET | `/api/v1/delivery/routes/{id}` | Get a route with its stops embedded; behind the branch wall. |
| POST | `/api/v1/delivery/routes/{id}/transitions` | Dispatch a route (`to: in_transit`) or complete it (`to: completed`); `If-Match` or body `revision` (428 without, 409 stale). |
| POST | `/api/v1/delivery/routes/{id}/reorder` | Reorder the stops by an explicit `ordered_delivery_ids` array (every stop of the route exactly once); `If-Match` required. |
| POST | `/api/v1/delivery/routes/{id}/optimize` | Reorder the stops and write each stop's estimated arrival; `If-Match` required. With a routing service configured it routes through it. With none configured it falls back to a deterministic mock: stops keep their order (renumbered from 1), stops with no coordinates get mock coordinates, and arrivals are spaced 15 minutes apart. Either way the route's revision moves and `route.updated` is written; a route with no stops, or with no stop that can be geocoded, returns unchanged with no event. |
| GET | `/api/v1/delivery/routes/{id}/deliveries` | Cursor list of the stops of a route, in stop order on `(stop_sequence, id)`; behind the branch wall through the route; `include=total` counts. |
| POST | `/api/v1/delivery/deliveries` | Assign an order to a route (the assign path); body `route_id` and `order_id` required, `stop_sequence` and `delivery_instructions` optional; no precondition; `Location` and `ETag` on the response. |
| GET | `/api/v1/delivery/deliveries/{id}` | Get a stop, behind the branch wall through the stop's order. |
| POST | `/api/v1/delivery/deliveries/{id}/transitions` | Close a stop (`to: delivered`, `failed` or `partial`); `If-Match` or body `revision`; `pod_proof_url` and `pod_signed_by` required for `delivered` and `partial`. |
| POST | `/api/v1/delivery/deliveries/{id}/adjust-qty` | Record driver's on-site quantity adjustments (one call, several lines); no precondition. |
| POST | `/api/v1/delivery/deliveries/{id}/pod-photo` | Attach a proof of delivery photo (`signature`, `site` or `damage`); multipart, jpg/jpeg/png/webp, at most 10 MB; no precondition; moves the revision. |
| GET | `/api/v1/delivery/deliveries/{id}/pod-photos` | List the proof of delivery photos, oldest first on `(uploaded_at, id)`; behind the branch wall through the stop. |

This module has no route cancel, no route update and no route assign
(the assign path is `POST /api/v1/delivery/deliveries`): a route reaches
`in_transit` and `completed` through the transitions route only.
`scheduled` and `cancelled` are valid stored values (seeded and legacy
rows) that the module does not write.

The `internal/delivery` census rows in `core/api/ROUTES.txt` are the
census test's source of comparison; if a handler registers a route
the census does not list, `core/internal/routecensus` fails.

## The main resources

The wire shapes live in `core/api/fragments/delivery.yaml`; the Go
shapes that the handler and service marshal them into live in
`core/internal/delivery/model.go` (`Vehicle`, `Driver`, `Route`,
`Stop`, `PODPhoto`, `QtyAdjustment`, `CapacityWarning`). Every
vehicle, driver, route and stop carries a `revision` int64. Every mutating route that takes
a precondition refuses without one (428 `precondition_required`) and
refuses a stale one (409 `stale_revision`); the photo attaches, the
quantity adjustment, the assign path and the creates take no
precondition but move the revision.

### Vehicles

Vehicles are dealer-wide: their lists and reads do not go behind the
branch wall. The wire shape is `DeliveryVehicle` in
`core/api/fragments/delivery.yaml` and the Go struct is `Vehicle` in
`core/internal/delivery/model.go`.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `name` | string | Required on create. |
| `vehicle_type` | enum | `box_truck`, `flatbed`, `pickup`, `van`, `crane` (lowercase on the wire; the storage vocabulary is uppercase, mapped at the boundary in `model.go`). |
| `license_plate` | string | Required on create, at most 20 characters. |
| `capacity_weight_lbs` | integer, nullable | Optional weight capacity. |
| `vin` | string, nullable | Optional, at most 255 characters. |
| `year` | integer, nullable | Optional, a vehicle year. |
| `make`, `model` | string, nullable | Optional, at most 255 characters. |
| `insurance_expiry` | date, nullable | `YYYY-MM-DD`, business date. |
| `next_service_date` | date, nullable | `YYYY-MM-DD`, business date. |
| `odometer_miles` | integer, nullable | Optional. |
| `notes` | string, nullable | Optional, at most 255 characters. |
| `photo_url` | string, nullable | Set by the photo upload route. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required, written by the service. |

The vehicle's photo is uploaded through
`POST /api/v1/delivery/vehicles/{id}/photo` (multipart, one file,
jpg/jpeg/png/webp, at most 10 MB); the photo attach takes no
precondition, moves the revision, and writes `vehicle.updated` with
data `{part: "photo", revision}`.

### Drivers

Drivers are also dealer-wide. The wire shape is `DeliveryDriver` and
the Go struct is `Driver`.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `name` | string | Required on create. |
| `license_number` | string, nullable | Optional. |
| `status` | enum | `active`, `inactive`, `on_leave` (the closed vocabulary). Optional on create (default `active`), required on update. |
| `phone_number` | string, nullable | Optional. |
| `cdl_class`, `cdl_expiry` | string / date, nullable | Optional CDL fields; `cdl_expiry` is `YYYY-MM-DD`. |
| `hire_date` | date, nullable | `YYYY-MM-DD`, business date. |
| `email` | string, nullable | Optional. |
| `photo_url` | string, nullable | Set by the photo upload route. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The driver's photo is uploaded through
`POST /api/v1/delivery/drivers/{id}/photo` with the same shape as
the vehicle's.

### Routes

Routes are walled through their stops' orders: a route is visible
when none of its stops belongs to a branch the caller cannot see, and
a route with no stops is visible to every branch. The wire shape is `DeliveryRoute` and the Go struct is `Route`.
The list of routes accepts `date` (YYYY-MM-DD), `driver_id`,
`status` and `include=stops` or `include=total`; with `include=stops`,
each route's `stops` array is embedded in the same payload (the
board read). The list is newest scheduled date first on
`(scheduled_date, id)`.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `vehicle_id` | UUID, nullable | A legacy row with no vehicle carries null, never the all-zero UUID. Always present in the response (the null leg is in the type). |
| `driver_id` | UUID, nullable | Same nullability rule. |
| `scheduled_date` | date | Required, `YYYY-MM-DD`, business date. |
| `status` | enum | `draft`, `scheduled`, `in_transit`, `completed`, `cancelled`. |
| `notes` | string, nullable | Optional. |
| `total_duration_mins` | integer, nullable | Stored value; this module reads it and never writes it (it comes from seed or legacy rows). |
| `total_distance_miles` | number, nullable | Stored value; this module reads it and never writes it (it comes from seed or legacy rows). |
| `vehicle_name`, `driver_name` | string | Joined names; empty string when the id is null. |
| `stop_count` | integer | The number of stops on the route. |
| `stops` | array of `Delivery`, nullable | Embedded under `include=stops`; null otherwise. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The create body is `DeliveryRouteCreate` (`vehicle_id`, `driver_id`,
`scheduled_date` required, `notes` optional); a route starts in
status `draft`. The transition body is `DeliveryRouteTransition`
(`to: in_transit` for dispatch, `to: completed` for completion). The
reorder body is `DeliveryRouteReorder` (`ordered_delivery_ids`,
every stop of the route exactly once).

### Stops

Stops are walled through their order's branch. The wire
shape is `Delivery` and the Go struct is `Stop`. A stop that exists
and is geocoded but is not on a route yet carries `route_id: null`. The seed's dispatch day writes such stops;
this module's assign path always puts a stop on a route.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `route_id` | UUID, nullable | Null when the stop is not on a route yet. |
| `order_id` | UUID | Required, the order the stop delivers. |
| `order_number` | string, nullable | The order's document number. |
| `stop_sequence` | integer | The position in the route's order. |
| `status` | enum | `pending`, `out_for_delivery`, `delivered`, `failed`, `partial`. |
| `pod_proof_url` | string, nullable | Set by the stop transition (`delivered` and `partial` require it); the photo attach does not write it. |
| `pod_signed_by` | string, nullable | Set by the stop transition; required for `delivered` and `partial`. |
| `pod_timestamp` | RFC 3339 UTC, nullable | Set by the stop transition. |
| `signature_data_url` | string, nullable | The signature image data URL; optional on the stop transition. |
| `delivery_instructions` | string, nullable | Free-form instructions. |
| `latitude`, `longitude` | number, nullable | The geocode. |
| `estimated_arrival` | RFC 3339 UTC, nullable | Written by the optimize call. |
| `scheduled_start`, `scheduled_end` | RFC 3339 UTC, nullable | Stored values this module reads and does not write. |
| `customer_name`, `address` | string, nullable | Joined customer fields. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The transition body is `DeliveryStopTransition` (`to: delivered`,
`failed` or `partial`, `revision` optional). The quantity-adjustment
body is `adjusted_by` (UUID) and an `adjustments` array of at least
one line; each line has `product_id`, `original_qty` and
`adjusted_qty` (decimal strings, at most 4 fraction digits), a
`reason_code` (`short_ship`, `damaged`, `refused`, `wrong_product` or
`other`, lowercase) and optional `notes`. The rows land in
`delivery_qty_adjustments`; the call takes no precondition, moves
the stop's revision and writes `delivery.adjusted`.

## Lifecycle and transitions

Lifecycles are lowercase on the wire (ADR 0001 section 6); the
storage vocabulary is uppercase and the mapping lives in
`core/internal/delivery/model.go` (`VehicleType`, `DriverStatus`,
`RouteStatus`, `StopStatus` and the `Parse*` / `MarshalText`
helpers). The transition logic is in
`core/internal/delivery/service.go` (`TransitionRoute`,
`TransitionStop`).

### Route status

The route lifecycle is `draft`, `scheduled`, `in_transit`,
`completed`, `cancelled`. A route starts in `draft` on create.
`TransitionRoute` accepts only `in_transit` and `completed`; any
other target is 409 `invalid_state_transition` with the blocker
`invalid_state` ("this module dispatches and completes routes
only").

| Action | Wire call | Allowed from | Refused from |
|---|---|---|---|
| Dispatch | `POST /routes/{id}/transitions {"to": "in_transit"}` | `draft`, `scheduled` | `in_transit`, `completed`, `cancelled` (409 `invalid_state_transition`, blocker `invalid_state`). A route with `vehicle_id` null or `driver_id` null is refused with the blocker `vehicle_id` or `driver_id`. |
| Complete | `POST /routes/{id}/transitions {"to": "completed"}` | any status except `cancelled`, with at least one stop and every stop terminal (`delivered`, `failed` or `partial`). The route does not have to be `in_transit`, and a route that is already `completed` is accepted again (it writes another `route.completed` event). | A route with no stops (409 `invalid_state_transition`, blocker `route_empty`); a route with any non-terminal stop (blocker `stop_not_terminal`); a `cancelled` route that passes those two checks (blocker `invalid_state`). |

The route lock is `repo.LockRoute` in the repository: every precondition
write (updates, deletes, transitions, reorder) takes the row lock,
reads the row, then checks the revision under the lock, then writes
the new status and the event. The completion refusal code path is
`Service.TransitionRoute`; the same function refuses a route
transition to any status other than `in_transit` and `completed`
with the blocker `invalid_state`.

### Stop status

The stop lifecycle is `pending`, `out_for_delivery`, `delivered`,
`failed`, `partial`. A stop starts in `pending`. A stop transition `to` of `pending` or
`out_for_delivery` parses and is refused 409
`invalid_state_transition`. `out_for_delivery`
is a valid status that a stop can be completed from, but this module
does not set it; it comes from seed or legacy rows. A route dispatch
does not change its stops.

| Action | Wire call | Allowed from | Refused from |
|---|---|---|---|
| Complete | `POST /deliveries/{id}/transitions {"to": "delivered", "failed" or "partial"}` | `pending`, `out_for_delivery` | a terminal stop is 409 `invalid_state_transition`. `delivered` and `partial` need `pod_proof_url` and `pod_signed_by`; a missing one is a 400 naming the field. |

The stop transition writes the audit row, the event
(`delivery.delivered`, `delivery.failed` or `delivery.partial`),
and, for a delivered stop, the order's fulfilment request, in one
transaction. The route completion's "every stop is terminal" check
is `repo.CountNonTerminalDeliveriesByRoute` under the route's row
lock (the count holds against the same write state the completion
is about to commit, and covers every stop, however many the route
holds).

### Assign path

The assign path is `POST /api/v1/delivery/deliveries` with `route_id`
and `order_id` (both required), optional `stop_sequence` (1 or more;
the route's next position by default) and optional
`delivery_instructions` (at most 2000 characters). `Service.AssignOrderToRoute`
checks, in this order: the order exists (a missing order is a 400
naming `order_id`); the order is visible behind the branch wall (a
cross branch order is a 404); the order is not a pickup order (a
will-call order is a 409 with the blocker `pickup_order`, ADR 0005
section 5.5); the lumber-index exposure gate is clear; and the
route is visible (a route the caller cannot see is a 404). Inside
the transaction the route is locked, its status and the next free
sequence are read under the lock (a route already `completed` or
`cancelled` is refused with 409 `invalid_state_transition`), the stop
is inserted, the route's revision moves,
and the audit row and `delivery.created` are written, in one
transaction. The answer is 201 with `{delivery, capacity_warning}`,
a `Location` and an `ETag`.

| Failure | Wire result |
|---|---|
| The order does not exist | 400 naming `order_id` |
| The order is in a branch the caller cannot see | 404 |
| The order is a pickup order | 409, blocker `pickup_order` |
| The route does not exist or is in a branch the caller cannot see | 404 |
| The route is `completed` or `cancelled` | 409 `invalid_state_transition` |

## Proof of delivery

Two routes on a stop carry the proof of delivery:

- `POST /api/v1/delivery/deliveries/{id}/pod-photo` attaches a
  proof-of-delivery photo. The body is multipart, one file,
  jpg/jpeg/png/webp, at most 10 MB. The optional `photo_type` is
  `signature`, `site` (the default) or `damage`. The photo attach
  takes no precondition, moves the stop's revision, and writes the
  audit row and the event `delivery.updated` (the data part is
  `pod_photos`) in one transaction. The stored record is
  `DeliveryPodPhoto` (`id`, `delivery_id`, `photo_url`,
  `photo_type`, `uploaded_at`).
- `GET /api/v1/delivery/deliveries/{id}/pod-photos` lists the
  photos in the cursor list envelope, oldest first on
  `(uploaded_at, id)`. The stop is read behind the branch wall
  first; a missing stop is a 404.

The stop transition carries the proof on the wire: `delivered`
and `partial` need `pod_proof_url` and `pod_signed_by` (a missing
field is a 400), and `pod_timestamp` is written by the same
transition. The signature image is `signature_data_url` on the stop.

## Events the module writes

Every event is written through the outbox as the last statement of
the mutation's transaction (ADR 0003). The constants live at the
top of `core/internal/delivery/service.go` (`EventVehicleCreated` to
`EventStopAdjusted`, lines 85 to 102) and the writer calls are the
`Service.log` and `Service.record` calls that bracket each
mutation.

| Event | Writer | Data |
|---|---|---|
| `vehicle.created` | `CreateVehicle` | `{name, revision}` |
| `vehicle.updated` | `UpdateVehicle`; `SetVehiclePhoto` | `{name, revision}`; the photo attach is `{part: "photo", revision}` |
| `vehicle.deleted` | `DeleteVehicle` | `{name, revision}` |
| `driver.created` | `CreateDriver` | `{name, revision}` |
| `driver.updated` | `UpdateDriver`; `SetDriverPhoto` | `{name, revision}`; the photo attach is `{part: "photo", revision}` |
| `driver.deleted` | `DeleteDriver` | `{name, revision}` |
| `route.created` | `CreateRoute` | `{scheduled_date, status: "draft", revision}` |
| `route.updated` | `ReorderStops`, `OptimizeRoute` | `{part: "stops", revision}` |
| `route.in_transit`, `route.completed` | `TransitionRoute` | `{from_status, status, revision}` |
| `delivery.created` | `AssignOrderToRoute` | `{route_id, order_id, status: "pending", revision}` |
| `delivery.updated` | `UploadPODPhoto` | `{part: "pod_photos", revision}` |
| `delivery.delivered`, `delivery.failed`, `delivery.partial` | `TransitionStop` | `{from_status, status, revision}` |
| `delivery.adjusted` | `AdjustDeliveryQuantity` | `{lines, revision}` |

The events feed reads all of them on `core/internal/events/handler.go`
at `GET /api/v1/events`. The feed and the cursor ordering are in
[`docs/modules/events.md`](events.md); the delivery events are listed
in the table above.

## Link to order fulfilment

A delivered stop queues the order's fulfilment request inside the
same transaction as the stop transition. The contract is in
[`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md)
section 5.5: the delivery adapter calls the order module's
fulfilment inserter with the order's id and the delivery's id; the
inserter writes the fulfilment row
(`order_fulfillment_requests.delivery_id` set to the delivery's id)
in one transaction with the stop's status write, so a completed
delivery is never paired with a missing fulfilment request. A
failed fulfilment at delivery completion is never dropped. The worker
in the order module retries the queued request on its own; after 10
failed attempts the request is parked, stays listed at
`GET /api/v1/orders/fulfillment-requests`, and is retried again only
through `POST /api/v1/orders/fulfillment-requests/{delivery_id}/retry`.

A pickup (will-call) order is refused before any write: the assign
service reads the order's delivery type and refuses a pickup with
409, blocker `pickup_order` (ADR 0005 section 5.5). The lumber-index
pre-ship gate (`exposureGate`) runs after the pickup check and before
the route read. An order whose source quote has unresolved index
exposure is stopped by it. On this base the delivery handler does not
map the gate's error to a refusal of its own, so the answer is not a
documented contract and an integrator should treat it as a failed
assign and read the order's exposure at
`GET /api/v1/orders/{id}/exposure-gate`.

## Branch wall, roles and scopes

Vehicles and drivers are dealer-wide: their lists and reads do not
go behind the branch wall. Routes and stops are walled: a stop walls
through its order's branch and a route through its stops' orders;
the wall is composed in `branchWall.delivery`
(`core/internal/app/serve/wire_branch_wall.go`) and mounted in
`core/internal/app/serve/serve.go` as
`wall.delivery(mux, delivery.NewHandler(deliverySvc), "admin",
"owner", "warehouse", "driver")`. A user held to branch A finds
branch B's stop or route a 404 on every route. A route with no stops
carries no branch fact and is visible. Vehicles and drivers have no
branch column and are not walled.

A machine key reaching the delivery routes needs `delivery:read` for
`GET` and `HEAD` and `delivery:write` for every other method (ADR
0002 section 2). The `delivery` segment is declared in
`machineKeyModules` in `core/pkg/middleware/machinekey.go`. A key is
a machine principal with no roles: the role guard on the delivery
mount is skipped for a key, and the branch wall sees no user, so it
applies no branch limit to an unbound key (ADR 0002 sections 4 and 6).
An unbound key with `delivery:read` therefore reads every branch's
routes, stops and proof of delivery photos, and one with
`delivery:write` writes to any branch. A user is held to the roles `admin`, `owner`, `warehouse`
and `driver` and to the branches the user is granted.

A branch bound key (ADR 0007 section 5.5) is pinned to its branch:
the machine key core puts that branch into the request's branch
context before the module runs, so lists, reads and writes on these routes see that
branch only. A key
bound to no branch behaves as today's claims-less caller: it reads
and writes across branches, scoped only by its `delivery:read` or
`delivery:write` scope.

## Migration 104

`core/migrations/104_delivery_wire_contract.sql` carries the module
onto the wire contract. For `vehicles`, `drivers`,
`delivery_routes` and `deliveries` it fills a NULL `created_at`
with `NOW()` and a NULL `updated_at` from `created_at`, then sets
both NOT NULL. It fills a NULL route status with `DRAFT` and a NULL
stop status with `PENDING`, then sets both NOT NULL. It adds
`revision BIGINT NOT NULL DEFAULT 1` to all four tables and four
keyset indexes: vehicles and drivers on `(created_at DESC, id DESC)`,
routes on `(scheduled_date DESC, id DESC)`, and stops on
`(route_id, stop_sequence, id)`.

It also normalises stored statuses to the uppercase storage
spelling after `UPPER(BTRIM(status))`. Routes map the synonyms
`IN_PROGRESS` to `IN_TRANSIT`, `COMPLETE` to `COMPLETED` and
`CANCELED` to `CANCELLED`. Any status still outside the vocabulary
becomes a terminal, non billing value: a route becomes `CANCELLED`
and a stop becomes `FAILED`. Never `DRAFT` or `PENDING`, so a row
of unknown spelling cannot reopen as dispatchable or deliverable,
and cannot re-queue fulfilment or be billed again. Each row it
rewrites raises a `NOTICE` naming the table, the id, the old value
and the new value, so an unattended upgrade of a polluted database
does not fail and each rewrite is reported in the migration output.

## Known limits

The CONTRACT-CHANGES rows that state limits:

- Capacity weights are a soft warning, not a refusal. The assign
  answer carries `capacity_warning` (`vehicle_capacity_lbs`,
  `current_load_lbs`, `order_weight_lbs`, `total_after_lbs`) when
  the order would push the route past the vehicle's
  `capacity_weight_lbs`, and null otherwise; the stop is created
  either way. The four values are JSON numbers, not decimal strings,
  a stated limit of the contract.
- An order may sit on several stops, on one route or several.
  There is no unique key on `deliveries.order_id`, and assigning
  the same order to the same route twice creates a second stop.
  Fulfilment stays safe because the fulfilment queue is keyed by
  delivery id and bills only the allocated, unfulfilled quantity.
  A caller that wants one stop per order must check before it
  assigns.
- A route with no stops carries no branch fact and is visible to
  every branch until it has a stop; vehicles and drivers have no
  branch column and are not walled.

## How to try it locally

The repository's own seed and the local make targets are the only
way to exercise the module end to end. `make up` builds and starts
the local stack (Postgres, migrate and seed, `core serve`, `core
worker`, the web front door) on http://127.0.0.1:8080 with
`AUTH_MODE=dev`; `make down` removes it. To run the core from source
instead: `make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. `DEMO_SEED=1
make seed` truncates the transactional tables first. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header). The web port
comes from `GABLE_WEB_PORT` (8080 by default).

The delivery routes are reached at
`http://127.0.0.1:8080/api/v1/delivery/...` with a machine
key holding `delivery:read` and `delivery:write` (no header in dev mode). The cursor list routes take `?limit=`
and `?cursor=`; the route list takes
`?date=YYYY-MM-DD&driver_id=...&status=...&include=stops` and
`?include=total`. The proof of delivery photo routes take a
multipart `photo` field.

A minimal end to end run:

1. List the vehicles at `GET /api/v1/delivery/vehicles` and the
   drivers at `GET /api/v1/delivery/drivers`; pick one of each.
2. Create a route at `POST /api/v1/delivery/routes` with
   `vehicle_id`, `driver_id` and `scheduled_date` (today's date).
3. Read the route at
   `GET /api/v1/delivery/routes/{id}?include=stops`.
4. Assign an order at `POST /api/v1/delivery/deliveries` with
   `route_id` and `order_id`; the order must be in a branch the
   caller can see and must not be a pickup order.
5. Optimize the route at
   `POST /api/v1/delivery/routes/{id}/optimize`; the call carries
   `If-Match` with the route's revision; with no routing service
   configured it uses the mock described in the routes table.
6. Dispatch the route at
   `POST /api/v1/delivery/routes/{id}/transitions` with
   `{"to": "in_transit"}`; the call carries `If-Match` with the
   current revision.
7. Attach a proof of delivery photo at
   `POST /api/v1/delivery/deliveries/{stop_id}/pod-photo` with
   `photo` and `photo_type=signature`.
8. Close the stop at
   `POST /api/v1/delivery/deliveries/{stop_id}/transitions` with
   `{"to": "delivered", "pod_proof_url": "...", "pod_signed_by":
   "..."}`; the call carries `If-Match` with the current revision.
9. Complete the route at
   `POST /api/v1/delivery/routes/{id}/transitions` with
   `{"to": "completed"}`.

## References

- [`core/internal/delivery/`](../../core/internal/delivery/) the
  handler, the service, the model, the repository, the request
  input shapes.
- [`core/api/fragments/delivery.yaml`](../../core/api/fragments/delivery.yaml)
  the wire contract fragment; the route paths, the operationIds,
  the schema definitions.
- [`core/api/ROUTES.txt`](../../core/api/ROUTES.txt) the route
  census under `internal/delivery`.
- [`core/migrations/104_delivery_wire_contract.sql`](../../core/migrations/104_delivery_wire_contract.sql)
  the migration that brought the storage shape onto the wire.
- [`core/internal/app/serve/wire_branch_wall.go`](../../core/internal/app/serve/wire_branch_wall.go)
  the branch wall and the role guard mounted onto the delivery
  routes.
- [`core/pkg/middleware/machinekey.go`](../../core/pkg/middleware/machinekey.go)
  the `delivery` segment in the module scope map (ADR 0002
  section 2).
- [`../adr/0001-wire-contract.md`](../adr/0001-wire-contract.md)
  the wire contract; section 6 is the enum casing rule that puts
  the lifecycles on the wire lowercase; section 11 is the revision
  precondition; section 12 is the field name and date convention.
- [`../adr/0002-machine-keys.md`](../adr/0002-machine-keys.md)
  section 2 (the segment scope rule: `<module>:read` and
  `<module>:write`, so `delivery:read` / `delivery:write`),
  section 4 (a key has no roles), section 5 (refusals and the
  audit row), section 6 (a key is not held to the branch wall).
- [`../adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md)
  section 5.5 names deliveries and is the contract the delivered
  stop's fulfilment request follows.
- [`../adr/0007-drafts-links-and-confirm-gated-scopes.md`](../adr/0007-drafts-links-and-confirm-gated-scopes.md)
  section 5.5 (a branch bound key is pinned where the branch
  middleware runs).
- [`docs/modules/events.md`](events.md) the events feed and the
  cursor ordering; the delivery events are listed in the events
  table above.