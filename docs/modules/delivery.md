<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Delivery

The delivery module is the yard's fleet, the day's routes, the stops on
each route, and the proof of delivery that closes them out. Vehicles and
drivers are dealer-wide master records; routes and stops live behind the
branch wall on the branch they are scheduled for. The desk reads the
board (routes and their stops in one payload) and the mobile app reads
the driver's run for the day, writes the proof, and closes the stop.

The Go code is in `core/internal/delivery/`. The model and lifecycle
enums are in `core/internal/delivery/model.go`. The service and the
lifecycle transitions are in `core/internal/delivery/service.go`. The
handler and the routes it serves are in `core/internal/delivery/handler.go`.
The request shapes are in `core/api/fragments/delivery.yaml` and the
route census in `core/api/ROUTES.txt` under `internal/delivery`. The
migration that brought the storage shape onto the wire contract is
`core/migrations/104_delivery_wire_contract.sql`.

## What it does in a yard

A yard uses the delivery module for four things:

- The fleet: vehicles and drivers, with their photos, capacity,
  licence and CDL fields, and the dealer-wide lists.
- The day's routes: one route per run, on a scheduled date, with a
  vehicle, a driver, the joined names, the stop count, and the
  totals that the optimizer writes back.
- The stops and the proof of delivery: each stop carries an order,
  an address, a sequence number, the geocode, the proof photo, the
  signature, and the status that closes it out.
- The board: the route list with `include=stops` embeds every stop
  under its route in one payload, so the dispatcher's screen reads
  the day's work in a single round trip.

## Routes

Every route the delivery handler registers is in `core/api/ROUTES.txt`
under `internal/delivery` and in `core/internal/delivery/handler.go`
in `Handler.RegisterRoutes`. The list below mirrors the path each
route serves on the wire and the one line that operationId and its
description give.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/delivery/vehicles` | Cursor list of vehicles, dealer-wide (no branch wall). |
| POST | `/api/v1/delivery/vehicles` | Create a vehicle; `name`, `vehicle_type`, `license_plate` required; `capacity_weight_lbs` optional. |
| GET | `/api/v1/delivery/vehicles/{id}` | Get a vehicle, dealer-wide. |
| PUT | `/api/v1/delivery/vehicles/{id}` | Update a vehicle (revision precondition). |
| DELETE | `/api/v1/delivery/vehicles/{id}` | Delete a vehicle (revision precondition; refused if a route references it). |
| POST | `/api/v1/delivery/vehicles/{id}/photo` | Upload a vehicle photo (jpg, jpeg, png or webp, at most 10 MB). |
| GET | `/api/v1/delivery/drivers` | Cursor list of drivers, dealer-wide. |
| POST | `/api/v1/delivery/drivers` | Create a driver; `name` required. |
| GET | `/api/v1/delivery/drivers/{id}` | Get a driver, dealer-wide. |
| PUT | `/api/v1/delivery/drivers/{id}` | Update a driver (revision precondition). |
| DELETE | `/api/v1/delivery/drivers/{id}` | Delete a driver (revision precondition; refused if a route references them). |
| POST | `/api/v1/delivery/drivers/{id}/photo` | Upload a driver photo (jpg, jpeg, png or webp, at most 10 MB). |
| GET | `/api/v1/delivery/routes` | Cursor list of routes, behind the branch wall; `date`, `driver_id`, `status`, `include=stops`. |
| POST | `/api/v1/delivery/routes` | Create a route; `vehicle_id`, `driver_id`, `scheduled_date` required; route starts draft. |
| GET | `/api/v1/delivery/routes/{id}` | Get a route with its stops, behind the branch wall. |
| PUT | `/api/v1/delivery/routes/{id}` | Update a route (revision precondition). |
| POST | `/api/v1/delivery/routes/{id}/assign` | Assign a stop to the route by `order_id` (the assign path). |
| POST | `/api/v1/delivery/routes/{id}/optimize` | Reorder the stops through the configured routing service; writes `route.updated`. |
| POST | `/api/v1/delivery/routes/{id}/reorder` | Reorder the stops by an explicit `ordered_delivery_ids` array. |
| GET | `/api/v1/delivery/routes/{id}/deliveries` | List the stops of a route, cursor list. |
| POST | `/api/v1/delivery/routes/{id}/transitions` | Dispatch a route (`to: in_transit`) or complete it (`to: completed`). |
| POST | `/api/v1/delivery/routes/{id}/cancel` | Cancel a route (the `cancelled` route transition). |
| POST | `/api/v1/delivery/deliveries/{id}/transitions` | Close a stop (`to: delivered`, `failed` or `partial`). |
| POST | `/api/v1/delivery/deliveries/{id}/adjust-qty` | Record driver's on-site quantity adjustments (one per line, reason code). |
| POST | `/api/v1/delivery/deliveries/{id}/pod-photo` | Attach a proof of delivery photo (`signature`, `site`, `damage`). |
| GET | `/api/v1/delivery/deliveries/{id}/pod-photos` | List the proof of delivery photos, oldest first. |

The `internal/delivery` census row in `core/api/ROUTES.txt` is the
census test's source of comparison; if a handler registers a route
the census does not list, `core/internal/routecensus` fails.

## The main resources

The wire shapes live in `core/api/fragments/delivery.yaml`; the Go
shapes that the handler and service marshal them into live in
`core/internal/delivery/model.go` (`Vehicle`, `Driver`, `Route`,
`Stop`, `PODPhoto`, `QtyAdjustment`, `CapacityWarning`). Every
resource carries a `revision` int64; every mutating route takes it
through `If-Match` or a body field, and the handler answers 409
`stale_revision` on a mismatch and 428 `precondition_required` on a
mutating request that carries neither.

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
| `license_plate` | string | Required. |
| `capacity_weight_lbs` | integer, nullable | Optional weight capacity. |
| `vin` | string, nullable | Optional. |
| `year` | integer, nullable | Optional. |
| `make`, `model` | string, nullable | Optional. |
| `insurance_expiry` | date, nullable | `YYYY-MM-DD`, business date. |
| `next_service_date` | date, nullable | `YYYY-MM-DD`, business date. |
| `odometer_miles` | integer, nullable | Optional. |
| `notes` | string, nullable | Optional. |
| `photo_url` | string, nullable | Set by the photo upload route. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required, written by the service. |

The vehicle's photo is uploaded through
`POST /api/v1/delivery/vehicles/{id}/photo` (multipart, one file,
jpg/jpeg/png/webp, at most 10 MB); the photo attach takes no
precondition, moves the revision, and writes `vehicle.updated`.

### Drivers

Drivers are also dealer-wide. The wire shape is `DeliveryDriver` and
the Go struct is `Driver`.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `name` | string | Required on create. |
| `license_number` | string, nullable | Optional. |
| `status` | enum | `active`, `inactive`, `on_leave` (the closed vocabulary). |
| `phone_number` | string, nullable | Optional. |
| `cdl_class`, `cdl_expiry` | string / date, nullable | Optional CDL fields. |
| `hire_date` | date, nullable | `YYYY-MM-DD`, business date. |
| `email` | string, nullable | Optional. |
| `photo_url` | string, nullable | Set by the photo upload route. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The driver's photo is uploaded through
`POST /api/v1/delivery/drivers/{id}/photo` with the same shape as
the vehicle's.

### Routes

Routes are behind the branch wall on the branch they are scheduled
for. The wire shape is `DeliveryRoute` and the Go struct is `Route`.
The list of routes accepts `date` (YYYY-MM-DD), `driver_id`,
`status` and `include=stops`; with `include=stops`, each route's
`stops` array is embedded in the same payload (the board read).

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `vehicle_id` | UUID, nullable | A legacy row with no vehicle carries null, never the all-zero UUID. Always present in the response (the null leg is in the type). |
| `driver_id` | UUID, nullable | Same nullability rule. |
| `scheduled_date` | date | Required, `YYYY-MM-DD`, business date. |
| `status` | enum | `draft`, `scheduled`, `in_transit`, `completed`, `cancelled`. |
| `notes` | string, nullable | Optional. |
| `total_duration_mins` | integer, nullable | The optimizer's total. |
| `total_distance_miles` | number, nullable | The optimizer's total. |
| `vehicle_name`, `driver_name` | string | Joined names, always present. |
| `stop_count` | integer | The number of stops on the route. |
| `stops` | array of `Delivery`, nullable | Embedded under `include=stops`; null otherwise. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The create body is `DeliveryRouteCreate` (`vehicle_id`, `driver_id`,
`scheduled_date` required, `notes` optional); a route starts in
status `draft`. The transition body is `DeliveryRouteTransition`
(`to: in_transit` for dispatch, `to: completed` for completion). The
reorder body is `DeliveryRouteReorder` (`ordered_delivery_ids`).

### Stops

Stops are behind the branch wall through their route. The wire
shape is `Delivery` and the Go struct is `Stop`. A stop that exists
and is geocoded but is not on a route yet carries `route_id: null`;
that is the state the seed's dispatch day and the optimizer's
planner write.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Required, server assigned. |
| `route_id` | UUID, nullable | Null when the stop is not on a route yet. |
| `order_id` | UUID | Required, the order the stop delivers. |
| `order_number` | string, nullable | The order's document number. |
| `stop_sequence` | integer | The position in the route's order. |
| `status` | enum | `pending`, `out_for_delivery`, `delivered`, `failed`, `partial`. |
| `pod_proof_url` | string, nullable | Set by the proof attach. |
| `pod_signed_by` | string, nullable | Set by the stop transition; required for `delivered` and `partial`. |
| `pod_timestamp` | RFC 3339 UTC, nullable | Set by the stop transition. |
| `signature_data_url` | string, nullable | The signature image data URL. |
| `delivery_instructions` | string, nullable | Free-form instructions. |
| `latitude`, `longitude` | number, nullable | The geocode. |
| `estimated_arrival`, `scheduled_start`, `scheduled_end` | RFC 3339 UTC, nullable | The planner and optimizer times. |
| `customer_name`, `address` | string, nullable | Joined customer fields. |
| `revision` | int64 | The precondition token. |
| `created_at`, `updated_at` | RFC 3339 UTC | Required. |

The transition body is `DeliveryStopTransition` (`to: delivered`,
`failed` or `partial`, `revision` optional). The quantity-adjustment
body is one line per call with `product_id`, the quantities as
decimal strings, a lowercase reason code (`short_ship`, `damaged`,
`refused`, `wrong_product`, `other`), and notes; the rows land in
`delivery_qty_adjustments` and the stop's revision moves.

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

| Action | Wire call | Allowed from | Refused from |
|---|---|---|---|
| Dispatch | `POST /routes/{id}/transitions {"to": "in_transit"}` | `draft`, `scheduled` | `in_transit`, `completed`, `cancelled`. The dispatch refuses a route with `vehicle_id` null or `driver_id` null with a `vehicle_id` / `driver_id` blocker; a legacy row with neither vehicle nor driver cannot be dispatched. |
| Complete | `POST /routes/{id}/transitions {"to": "completed"}` | any non-terminal with at least one stop and every stop terminal (`delivered`, `failed` or `partial`) | a route with no stops (a `route_empty` blocker); a route with any non-terminal stop (a `stop_not_terminal` blocker); a `cancelled` route. |
| Cancel | `POST /routes/{id}/cancel` | any non-terminal | a route already terminal. |

The route lock is `Service.LockRoute` in the service: every
mutation under `Service.inTx` takes the row lock first, then checks
the revision under the lock, then writes the new status and the
event. The dispatch and complete refusal code paths are
`Service.TransitionRoute`; the same function refuses a route
transition to any status other than `in_transit` and `completed`
with a `this module dispatches and completes routes only` blocker.

### Stop status

The stop lifecycle is `pending`, `out_for_delivery`, `delivered`,
`failed`, `partial`. A stop starts in `pending`.

| Action | Wire call | Allowed from | Refused from |
|---|---|---|---|
| Complete | `POST /deliveries/{id}/transitions {"to": "delivered", "failed" or "partial"}` | `pending`, `out_for_delivery` | a terminal stop is `409 invalid_state_transition`. `delivered` and `partial` need `pod_proof_url` and `pod_signed_by`; a missing one is a 400 naming the field. |

The stop transition writes the audit row, the event
(`delivery.delivered`, `delivery.failed` or `delivery.partial`),
and, for a delivered stop, the order's fulfilment request, in one
transaction. The route completion's "every stop is terminal" check
is `Service.CountNonTerminalDeliveriesByRoute` under the route's
row lock (the count holds against the same write state the
completion is about to commit, so a route past the prior 200-stop
page bound completes correctly).

### Assign path

The assign path lives on the route. The body is an `order_id`, and
the route takes the lock first, then writes the new stop sequence,
the audit row and the event.

| Failure | Wire result |
|---|---|
| The order does not exist in the same branch as the route | `400` naming `order_id`. The route is read behind the branch wall first. |
| The order exists but is in another branch (cross branch) | `404`. The branch wall stays closed. |

A stop that exists and is geocoded but is not on a route yet
carries `route_id: null`; the optimizer's planner and the seed's
dispatch day are the writers of that state.

## Proof of delivery

Two routes on a stop carry the proof of delivery:

- `POST /api/v1/delivery/deliveries/{id}/pod-photo` attaches a
  proof-of-delivery photo. The body is multipart, one file,
  jpg/jpeg/png/webp, at most 10 MB. The optional `photo_type` is
  `signature`, `site` (the default) or `damage`. The photo attach
  takes no precondition, moves the stop's revision, and writes
  the audit row and the event `delivery.updated` (the data part is
  `pod_photos`) in one transaction. The stored record is
  `DeliveryPodPhoto` (`id`, `delivery_id`, `photo_url`,
  `photo_type`, `uploaded_at`).
- `GET /api/v1/delivery/deliveries/{id}/pod-photos` lists the
  photos in the cursor list envelope, oldest first on
  (`uploaded_at`, `id`). The stop is read behind the branch wall
  first; a missing stop is a 404.

The stop transition carries the proof on the wire: `delivered`
and `partial` need `pod_proof_url` and `pod_signed_by` (a missing
field is a 400), and `pod_timestamp` is written by the same
transition. The signature image is `signature_data_url` on the stop.

## Events the module writes

Every event is written through the outbox as the last statement of
the mutation's transaction (ADR 0003). The constants live at the
top of `core/internal/delivery/service.go` and the writer calls are
the `Service.log` and `Service.record` calls that bracket each
mutation.

| Event | When | Data |
|---|---|---|
| `vehicle.created` | `Service.CreateVehicle` | the new vehicle's fields. |
| `vehicle.updated` | `Service.UpdateVehicle`, the photo attach | the fields that changed; the photo attach's data part is `photo`. |
| `vehicle.deleted` | `Service.DeleteVehicle` | the vehicle's id. |
| `driver.created` | `Service.CreateDriver` | the new driver's fields. |
| `driver.updated` | `Service.UpdateDriver`, the photo attach | the fields that changed; the photo attach's data part is `photo`. |
| `driver.deleted` | `Service.DeleteDriver` | the driver's id. |
| `route.created` | `Service.CreateRoute` | `scheduled_date`, `status: draft`, `revision`. |
| `route.updated` | `Service.UpdateRoute`, the optimize and the reorder, the pod photo attach's parent route | the changed fields; the optimize's data part is `stops`. |
| `route.in_transit` | `Service.TransitionRoute` to `in_transit` | the route's fields. |
| `route.completed` | `Service.TransitionRoute` to `completed` | the route's fields. |
| `route.cancelled` | `Service.CancelRoute` | the route's fields. |
| `delivery.created` | `Service.AssignStop` | the new stop's fields. |
| `delivery.updated` | `Service.UpdateStop`, the proof of delivery photo attach | the fields that changed; the photo attach's data part is `pod_photos`. |
| `delivery.delivered` | `Service.TransitionStop` to `delivered` | the stop's fields. |
| `delivery.failed` | `Service.TransitionStop` to `failed` | the stop's fields. |
| `delivery.partial` | `Service.TransitionStop` to `partial` | the stop's fields. |
| `delivery.adjusted` | `Service.AdjustQty` | the adjustment's fields. |

The events feed reads all of them on `core/internal/events/handler.go`
at `GET /api/v1/events`. The events vocabulary and the cursor
ordering live in `docs/modules/events.md`.

## Link to order fulfilment

A delivered stop queues the order's fulfilment request inside the
same transaction as the stop transition. The contract is in
`docs/adr/0005-sales-and-money-core.md` section 5.5: the delivery
adapter calls the order module's fulfilment inserter with the
order's id and the delivery's id; the inserter writes the
fulfilment row (`order_fulfillment_requests.delivery_id` set to
the delivery's id) in one transaction with the stop's status
write, so a completed delivery is never paired with a missing
fulfilment request. A failed fulfilment at delivery completion is
never dropped: the order module retries it from
`POST /api/v1/orders/fulfillment-requests/{delivery_id}/retry`,
and the inserted row is what surfaces the failure in the order's
fulfilment requests list.

The order's `delivery_type` (`pickup` or `delivery`) is consulted
by `Service.CreateStop` in `core/internal/delivery/service.go`: the
order's branch id is read first (a missing order is the 400 that
the assign path refuses), the order's delivery type is read next,
and a stop on a pickup order is refused with a 422 naming the
field. The lumber-index pre-ship gate (`exposureGate`) is the last
check; a clear-for-order refusal at stop creation is also a 422.

## Branch wall, roles and scopes

Vehicles and drivers are dealer-wide: their lists and reads do not
go behind the branch wall, so any role with the right scope can read
the fleet and the roster across every branch. Routes and stops are
walled: the route is read behind the branch wall first, and a route
the caller cannot see is the 404 the assign path and the stop
transition return when the order is in another branch. The wall
itself is composed in `core/internal/app/serve/wire_branch_wall.go`
in `branchWall.delivery` and the route's `wall.delivery` mount
(`wall.delivery(mux, delivery.NewHandler(deliverySvc), "admin",
"owner", "warehouse", "driver")`).

A machine key reaching the delivery routes needs
`delivery:read` for `GET` and `HEAD` and `delivery:write` for every
other method (ADR 0002 section 2, the segment scope rule). The
`delivery` segment is in `core/pkg/middleware/machinekey.go` in the
`machineKeyModules` map. The role guard the serve wires onto the
delivery routes is the four roles the wall's mount lists:
`admin`, `owner`, `warehouse`, `driver`. A user guard and a
machine key guard are both supported; the role names are the
wire's role vocabulary the key's grant list carries (ADR 0002
section 4).

The role-grant rows the key carries are the rows the key's
`staff_module_grants` table lists; a missing role is a 403 and a
role the caller has is the role the wall's guard accepts. A user
authenticate is a session cookie, and the user's role the auth
middleware reads from the session is the role the same wall's guard
accepts.

## Migration 104

`core/migrations/104_delivery_wire_contract.sql` brought the
storage shape onto the wire contract. The migration renames
columns to match the wire (`scheduled_date`, `vehicle_id`,
`driver_id`, `stop_sequence`, `pod_proof_url`, `pod_signed_by`,
`pod_timestamp`, `signature_data_url`, `delivery_instructions`,
`estimated_arrival`, `scheduled_start`, `scheduled_end`,
`customer_name`), drops the legacy not-null on `vehicle_id` and
`driver_id` (so a legacy row with neither carries null, never the
all-zero UUID), adds `delivery_qty_adjustments` with the
reason-code check, adds the `delivery_pod_photos` table, adds the
`delivery_routes_total_distance_miles` column the optimizer
writes, and adds the `delivery_routes_total_duration_mins`
column. Timestamps were indexed for the cursor reads; the
`scheduled_date` column was indexed for the day's routes board
read.

The migration normalizes legacy lowercase statuses to the
uppercase CHECK vocabulary: `pending` to `PENDING`,
`out_for_delivery` to `OUT_FOR_DELIVERY`, `delivered` to
`DELIVERED`, `failed` to `FAILED`, `partial` to `PARTIAL`,
`draft` to `DRAFT`, `scheduled` to `SCHEDULED`,
`in_transit` to `IN_TRANSIT`, `completed` to `COMPLETED`,
`cancelled` to `CANCELLED`. Unknown statuses are mapped to a
terminal non-billing value with a `NOTICE` per row, so the
migration never fails on a row the contract does not know and the
row is reachable on the wire.

## Known limits

The CONTRACT-CHANGES rows that state limits:

- Capacity weights are a soft warning, not a refusal. A
  `DeliveryCapacityWarning` is returned when an assignment would
  exceed the vehicle's weight capacity: the assignment still
  happens. The fields are `vehicle_capacity_lbs`,
  `current_load_lbs`, `order_weight_lbs`, `total_after_lbs`.
- One order may sit on several stops (an order split across two
  routes, the typical case for a back-ordered yard). The stop's
  `order_id` is the join key, not a unique key on the stop side.

## How to try it locally

The `make serve` target in the repository root boots the API
server with a seed dataset; the `make seed` target reseeds the
warehouse. The delivery routes are reached at
`http://localhost:8080/api/v1/delivery/...` under the same
machine key the rest of the API uses. The cursor list routes take
`?limit=...` and `?cursor=...`; the route list takes
`?date=YYYY-MM-DD&driver_id=...&status=...&include=stops`. The
proof of delivery photo routes take a multipart `photo` field.

A minimal end to end run:

1. List the vehicles at `GET /api/v1/delivery/vehicles` and the
   drivers at `GET /api/v1/delivery/drivers`; pick one of each.
2. Create a route at `POST /api/v1/delivery/routes` with
   `vehicle_id`, `driver_id` and `scheduled_date` (today's date).
3. Read the route at `GET /api/v1/delivery/routes/{id}?include=stops`.
4. Assign an order at `POST /api/v1/delivery/routes/{id}/assign`
   with `order_id`; the order must be in the same branch as the
   route.
5. Optimize the route at
   `POST /api/v1/delivery/routes/{id}/optimize`.
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
10. Complete the route at
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
  the lifecycles on the wire lowercase; section 11 is the
  revision precondition; section 12 is the field name and date
  convention.
- [`../adr/0002-machine-keys.md`](../adr/0002-machine-keys.md)
  section 2 (the segment scope rule: `<module>:read` and
  `<module>:write`, so `delivery:read` / `delivery:write`),
  section 3 (a key as a principal), section 4 (roles), section 5
  (refusals and the audit row), section 6 (branch scoping).
- [`../adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md)
  section 5.5 names deliveries and is the contract the delivered
  stop's fulfilment request follows.
- [`../adr/0007-drafts-links-and-confirm-gated-scopes.md`](../adr/0007-drafts-links-and-confirm-gated-scopes.md)
  section 2.3 (the branch bound key, the branch payload, the
  `CheckPayloadBranch` guard that holds the branch wall the route
  and stop rides).
- [`docs/modules/events.md`](events.md) the events feed, the
  cursor ordering, the module's event vocabulary on
  `refactor/v1`.