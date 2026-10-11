<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Inventory levels

The inventory module is the read of the stock on hand, the stock
reserved, and the stock available at a location for a product. The
write side is the cycle count adjustment and the inter-location
transfer, both of which write the underlying row directly.

The Go code is in `core/internal/inventory/`. The migration that
brought the read onto the contract is
`core/migrations/098_inventory_read_contract.sql`. The write side
stays on its legacy shape until C4-1.

## What it does in a yard

A yard keeps a quantity of every stocked SKU at one or more
locations: a main yard, a satellite yard, a vendor warehouse, a job
site bin. The levels list is what the sales rep sees when promising
a delivery date, what the buyer sees when placing a purchase order,
and what the office sees when reconciling the count after a cycle
count or a receipt.

## Routes

Every route is in `core/api/fragments/inventory.yaml` and the
registered handles are in `core/internal/inventory/handler.go`. The
route census (`core/api/ROUTES.txt`) lists each one under the
`internal/inventory` package (the route census has a `package`
column, not a module column).

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/inventory` | Cursor list of inventory levels, newest first; product_id and location_id filter. |
| POST | `/api/v1/inventory/adjust` | Adjust inventory (cycle count). |
| POST | `/api/v1/inventory/transfer` | Transfer inventory between locations. |

## The main resource

`InventoryLevel` (see `core/api/fragments/inventory.yaml`
`components.schemas.InventoryLevel`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The level row id. |
| `product_id` | UUID | The product stocked. |
| `location_id` | UUID, nullable | The location; null on a legacy row that never had one (C4-1 migrates legacy rows onto locations). |
| `location_name` | text | The location's path, or the row's deprecated free text when it has none. |
| `quantity` | decimal string | On hand, in the product's stocking unit, scale 4. |
| `allocated` | decimal string | Reserved, in the product's stocking unit, scale 4. |
| `available` | decimal string | `quantity - allocated`, in the product's stocking unit, scale 4. |
| `uom` | text | The product's stocking unit, repeated beside the quantity for clients that do not join the product. |
| `product` | object, optional | The product summary, embedded under `include=product`. |
| `updated_at` | timestamp | RFC 3339 UTC. |

The `product` object embedded under `include=product` is the
`InventoryProductSummary` (see
`core/api/fragments/inventory.yaml`
`components.schemas.InventoryProductSummary`):

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The product's id, the same value as `product_id` on the level. |
| `sku` | text | The product's SKU. |
| `description` | text | The product's description. |
| `stock_uom` | code | The product's stocking unit, the unit the level's quantities are counted in. |

### The list envelope

The list is the keyset envelope of ADR 0001 section 1,
`InventoryLevelPage` (see
`core/api/fragments/inventory.yaml`
`components.schemas.InventoryLevelPage`): `items` (never null),
`next_cursor` (null when the page is the last one), `limit`, and
`total` present only under `include=total`.

### Money and quantity conventions

Quantities are decimal strings with at most four fraction digits. The
unit is the product's stocking unit. No money is exposed on a level
row; the cost of the product is on the product itself.

## Lifecycle and transitions

A level has no lifecycle of its own. The two writes are
`/api/v1/inventory/adjust` and `/api/v1/inventory/transfer`; the
fragments name the legacy shapes they keep until C4-1.

### The write request and ack shapes

The write bodies stay on the legacy `number` shape (a `float64` on the
server) until C4-1; the read is on the `Quantity` contract.

`StockAdjustmentRequest` (see
`core/api/fragments/inventory.yaml`
`components.schemas.StockAdjustmentRequest`), body of
`POST /api/v1/inventory/adjust`:

| Field | Wire form | Note |
|---|---|---|
| `product_id` | UUID | The product the cycle count names. Required by the contract; the decoder does not check it. |
| `location_id` | UUID, nullable | The location the count names; nullable for a legacy row. |
| `quantity` | number | The new on-hand quantity (when `is_delta` is false) or the delta to add or subtract (when `is_delta` is true). Required by the contract; the decoder does not check it. |
| `reason` | text | Accepted and discarded: no movement row is written until C4-1 (ADR 0008). |
| `is_delta` | boolean | If true, add or subtract `quantity`; if false, replace the on-hand with `quantity`. |

`StockMovementRequest` (see
`core/api/fragments/inventory.yaml`
`components.schemas.StockMovementRequest`), body of
`POST /api/v1/inventory/transfer`:

| Field | Wire form | Note |
|---|---|---|
| `product_id` | UUID | The product moved. Required by the contract; the decoder does not check it. |
| `from_location_id` | UUID, nullable | The source location; null is the legacy row with no location, in which case the source branch lookup and the source inventory + unallocated check are skipped (destination is always a required UUID, never a legacy row). |
| `to_location_id` | UUID | The destination location. Required. |
| `quantity` | number | The amount moved; must be positive. Required by the contract; the decoder does not check it. |
| `reason` | text | Accepted and discarded: the wire value is prefixed into the inner adjustment, which discards it; no movement row is written until C4-1 (ADR 0008). |

`InventoryStatusAck` (see
`core/api/fragments/inventory.yaml`
`components.schemas.InventoryStatusAck`), the body of either write's
`200`: `{ "status": "ok" }`.

### Write headers and error answers

Both `POST /api/v1/inventory/adjust` and
`POST /api/v1/inventory/transfer` accept the `Idempotency-Key` and
`X-Branch-Id` request headers (see `core/api/fragments/inventory.yaml`
under each operation's `parameters`; `X-Branch-Id` is the branch wall
header from the `inventory` list, `Idempotency-Key` the dedupe header
shared with every other write). Status codes both routes answer:

| Status | Meaning |
|---|---|
| `200` | Adjusted or transferred; body is `{ "status": "ok" }`. |
| `400` | Malformed JSON body. |
| `401` | Missing or invalid credentials. |
| `403` | Adjust: the body's `location_id` is in a branch the caller may not target. Transfer: the body's `from_location_id` or `to_location_id` is in a branch the caller may not target. |
| `409` | `Idempotency-Key` replay or revision conflict. |
| `413` | Payload too large. |
| `422` | Unprocessable entity. |
| `500` | Adjust: a result that would leave negative stock. Transfer: a cross-branch move, a non positive `quantity`, or insufficient unallocated stock at the source. |

## Events the module writes

The write routes write the `inventory` rows directly. The only two
writes the routes make are `CreateInventory`
(`core/internal/inventory/repository.go:80-94`) and `UpdateInventory`
(`core/internal/inventory/repository.go:96-107`); the calls live in
`core/internal/inventory/service.go` `AdjustStock` (creates on a
missing row, updates on an existing one) and the inner `AdjustStock`
calls inside `MoveStock`. No outbox events and no `audit_log` rows
are written by the inventory module today; the read side does not
write events.

## Scopes, roles and keys

A machine key reaching the inventory routes needs `inventory:read`
for `GET` and `HEAD`, and `inventory:write` for every other method
(ADR 0002; the segment is the first path segment under `/api/v1/`).
The user guard at the serve layer is `admin`, `owner`, `warehouse`;
the exact guard is composed in
`core/internal/app/serve/wire_branch_wall.go` at
`wall.inventory(mux, inventorySvc)`. The list is held to the branch
wall: a context branch (X-Branch-Id) lists its own rows; with no
context branch a bound non-admin user lists the branches granted to
the user, none granted listing none; an administrator without a
header, an unbound key, the single-branch switch and callers with no
branch context at all list every branch's. A legacy row with no
`location_id` has no branch on its joined location and stays visible
to an administrator without a header and to callers with no branch
context only. A key without the scope is `403 forbidden`; the audit
row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 7.2: the levels list on the wire.
- [`docs/adr/0008-inventory-identity-and-vendor-intake.md`](../adr/0008-inventory-identity-and-vendor-intake.md): the inventory identity and the vendor intake (cycle 4, in flight at the time of writing).

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. `make up` builds and starts the
local stack (Postgres, migrate and seed, `core serve`, `core worker`,
the web front door) on http://127.0.0.1:8080 with `AUTH_MODE=dev`;
`make down` removes it. To run the core from source instead:
`make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Then, with a `warehouse` role bearer and the seeded branch:

```
curl -X GET 'http://localhost:8080/api/v1/inventory?include=total&include=product' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

The wire tests in `core/internal/inventory/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the inventory's scenarios.