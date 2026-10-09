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
`inventory` module column.

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

### Money and quantity conventions

Quantities are decimal strings with at most four fraction digits. The
unit is the product's stocking unit. No money is exposed on a level
row; the cost of the product is on the product itself.

## Lifecycle and transitions

A level has no lifecycle of its own. The two writes are
`/api/v1/inventory/adjust` and `/api/v1/inventory/transfer`; the
fragments name the legacy shapes they keep until C4-1.

## Events the module writes

The write routes write the product and location rows directly and
emit a `audit_log` row. The outbox event is not part of the inventory
module today; the read side does not write events.

## Scopes, roles and keys

A machine key reaching the inventory routes needs `inventory:read`
for `GET` and `HEAD`, and `inventory:write` for every other method
(ADR 0002). The user guard at the serve layer is the standard
inventory and sales wall; the exact guard is composed in
`core/internal/app/serve/serve.go` at
`wall.inventory(mux, inventorySvc)`. The list is held to the branch
wall: a context branch (X-Branch-Id) lists its own rows; with no
context branch a bound non-admin user lists the branches granted to
the user, none granted listing none; an administrator without a
header, an unbound key, the single-branch switch and callers with no
branch context at all list every branch's. A legacy row with no
location_id has no branch on its joined location and stays visible
to an administrator without a header and to callers with no branch
context only.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) section 7.2: the levels list on the wire.
- [`docs/adr/0008-inventory-identity-and-vendor-intake.md`](../adr/0008-inventory-identity-and-vendor-intake.md): the inventory identity and the vendor intake (cycle 4, in flight at the time of writing).

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. From the repository root:

```
make stack-up
make migrate
make seed
make serve
```

Then, with an inventory role bearer and the seeded branch:

```
curl -X GET 'http://localhost:8080/api/v1/inventory?include=total&include=product' \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>'
```

The wire tests in `core/internal/inventory/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for
the inventory's scenarios.
