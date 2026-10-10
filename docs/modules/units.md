<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Units

The unit catalogue is one row per unit the dealer uses. It replaces
the closed `uom_type` enum the older database carried: a unit is a
code, a dimension, a standard size, and the three flags that say
whether it can be sold, purchased or priced. The product unit set is
one row per unit on a product: `unit_qty` of the row's unit is
`stock_qty` of the product's stocking unit. The pair, the conversion
and the tally are exact rational arithmetic over the scale 4
integers the wire and the columns carry.

This module is the work of C3-2A-units and migrations 099
(`core/migrations/099_units_catalogue_and_sets.sql`). The catalogue
service is in `core/internal/unit/`. The arithmetic that the quote
module and the order fulfilment apply is in `core/internal/units/`.
The unit set wire lives under the product module (`/products/{id}/units`).

## What it does in a yard

A builder asks for "12 LF of 2x4x8". `LF` is the catalogue code for
linear feet, dimension `length`; the reference unit of `length` is
the foot, so `LF` carries `(std_unit_qty = 1, std_ref_qty = 1)`
(migration 099; each other dimension's reference unit is likewise
`(1, 1)`: `EA` and `PCS` for `count`, `SF` for `area`, `CF` for
`volume`, `LBS` for `weight`, `BF` for `board_measure`). `EA` is the catalogue code for
"each", dimension `count`. `BF` is the board foot, dimension
`board_measure`, `(1, 1)`; `MBF` is `1, 1000` board feet. The seed
of ADR 0006 section 2.2 places the 23 standard units in 099
(`099_units_catalogue_and_sets.sql:71-93`), the per product units
(`SET`, `BOX`, `CTN`, `BAG`, `BUNDLE`, `RL`) carry `(NULL, NULL)`
because their size is per product.

The worked examples of ADR 0006 section 3.2: a 2x4x8 stocked in
`PCS` stores `LF (8, 1)`, `BF (1, 0.1875)` and `MBF (1, 187.5)`,
all in canonical form (R2, GCD reduced, scale 4). A 2x4 random
length product stocked in `LF` stores `LF (1, 1)`, `BF (1, 1.5)`
and `MBF (1, 1500)`. A 6x9 paver stocked in `PCS` and sold by `SF`
has `1 SF = 8/3 PCS`, which is not exact at scale 4, so the unit
set PUT answers a warning naming `SF` as the finer stocking unit
that would make every sell row exact (`1 PCS = 0.375 SF`).

A quantity on a line is a plain decimal string at scale 4
(`"12.5"`, `"0.1875"`). A unit conversion is a pair of positive
scale 4 quantities. The arithmetic is exact rational arithmetic
over `int64`: no `float64` and no parsing through text. A sale unit
that does not convert exactly into the stocking unit is refused on
the line, never rounded into stock.

## Routes

Every route below is in `core/api/fragments/units.yaml` and
`core/api/fragments/product.yaml`; the registered handles are in
`core/internal/unit/handler.go` and
`core/internal/product/handler.go`; the route census
(`core/api/ROUTES.txt`) lists each one under the named package.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/units` | Cursor list of units, ordered by `code`; `dimension` and `is_active` filter. |
| POST | `/api/v1/units` | Create a dealer unit; `code` and `name` required. |
| GET | `/api/v1/units/{code}` | One unit with its standard size. |
| PUT | `/api/v1/units/{code}` | Edit a unit's name, dimension, standard size and `is_active`; revision precondition. |
| GET | `/api/v1/products/{id}/units` | One product's unit set: `stock_uom`, `sale_uom`, `price_uom`, `purchase_uom`, the set rows, the board measure facts, the base price, and the warnings list. |
| PUT | `/api/v1/products/{id}/units` | Replace a product's unit set and the four default uom columns; revision precondition; the board measure columns cannot be applied. |

The unit catalogue is dealer wide (no branch wall). The product
unit set is owned by the product module and inherits its product
guard; the catalogue itself has its own read and write guards.

## The catalogue resource

`Unit` (see `core/api/fragments/units.yaml`
`components.schemas.Unit`).

| Field | Wire form | Note |
|---|---|---|
| `code` | text | The code, immutable; every unit column holds it. Pattern `^[A-Z]{1,6}$`. |
| `name` | text | The display name, up to 80 chars. |
| `dimension` | enum | `count`, `length`, `area`, `volume`, `weight`, `board_measure`. |
| `std_unit_qty` | quantity | The left side of the standard pair, a scale 4 decimal string. |
| `std_ref_qty` | quantity | The right side of the standard pair; the dimension's reference unit. |
| `is_system` | boolean | True on the seeded rows; `code`, `dimension` and the standard size never change. |
| `is_active` | boolean | Inactive units cannot enter a new unit set row or a new line; existing rows keep them. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

`UnitCreate` requires `code` and `name`; `dimension` defaults to
`count`; the standard size is `std_unit_qty` and `std_ref_qty`
both, or neither. `is_system` and `revision` are never accepted on
the create body.

`UnitUpdate` is the put at the unit's revision (`If-Match` or
`revision`). A field the PUT cannot change is a `400` naming it:
the immutability rules of `core/internal/unit/service.go` (a system
unit's `code`, `dimension` and standard size never change; a dealer
unit's `dimension` and standard size are immutable once any product
unit set row or any line names the unit).

### Standard sizes and the dimension reference

The reference unit of each dimension is the canonical 1 of the
dimension. The migration seeds the 23 standard units
(`099_units_catalogue_and_sets.sql:71-93`) of ADR 0006 section 2.2:
`EA`, `PCS`, `PAIR`, `DOZ`, `C`, `M`, `SET`, `BOX`, `CTN`, `BAG`,
`BUNDLE`, `RL`, `LF`, `SF`, `SQ`, `CF`, `CY`, `GAL`, `LBS`, `CWT`,
`TON`, `BF`, `MBF`. The reference units (`EA`, `LF`, `SF`, `CF`,
`LBS`, `BF`) and `PCS` are `(1, 1)`; `MBF` is `(1, 1000)` board
feet. The per product units (`SET`, `BOX`, `CTN`, `BAG`, `BUNDLE`,
`RL`) carry `(NULL, NULL)` because their size is per product. The
rest of the count units (`PAIR`, `DOZ`, `C`, `M`) and the weight
units (`CWT`, `TON`) have their canonical pair (`1 = 2`, `1 = 12`,
`1 = 100`, `1 = 1000`, `1 = 100`, `1 = 2000`); the area `SQ` is
`(1, 100)`; volume `CY` is `(1, 27)`; `GAL` is `(576, 77)` of
`CF` (ADR 0006 section 1, R2).

### Unit values already in use

The catalogue table is the place a dealer adds a new unit, not a
guess the migration makes. Migration 099's step A1 collects every
distinct unit value stored in `products.uom_primary`,
`quote_lines.uom` and `quote_lines.price_uom`
(`099_units_catalogue_and_sets.sql:111-118`), normalises with
`upper(trim())`, inserts as a dealer unit (dimension `COUNT`, no
standard size), and reports it when it is a new valid code. A
value that does not match `^[A-Z]{1,6}$` aborts the migration
naming the table and the value; the migration never guesses a
mapping. An operator runs the read-only report first:

```
cd core && go run ./cmd/core migrate -report units
```

(the old entry `go run ./cmd/migrate -report units` runs the same
report; both parse the flag through `migrate.ParseArgs`.) The
report prints, per table and column, the values
that would be inserted as new dealer units with their row counts,
and the values that would abort the migration (the latter exit
code `1`). The report is the same shape B0 (C3-2B) extends to the
counter and document line columns, listed ahead of B0's own output
in `units_report.go`; before B0 the report prints `not present on
this database` for the columns B0 has not converted yet
(`units_report.go`, `unitsReport`).

## The product unit set

`UnitSetDoc` (see `core/api/fragments/product.yaml`
`components.schemas.UnitSetDoc`) is the read of
`GET /products/{id}/units` and the answer of the PUT. It carries:

| Field | Wire form | Note |
|---|---|---|
| `product_id` | UUID | The product the set belongs to. |
| `stock_uom` | code | The stocking unit. |
| `sale_uom`, `price_uom`, `purchase_uom` | code | The three defaults a new line reads. |
| `base_price_ten_thousandths` | integer | The base price per stocking unit at scale 4. |
| `board_thickness_in`, `board_width_in` | decimal string, nullable | The nominal cross section of a board measure product (a 2x4 is 2). |
| `board_length_ft` | decimal string, nullable | The nominal length of a fixed length piece (a 2x4x8 is 8); null for random length. |
| `random_length` | boolean | The product is sold by tally; requires LF stocking. |
| `units` | array of `UnitSetRow` | The set rows. |
| `warnings` | array | Empty on a read, always an array; see the warning rule below. |
| `revision` | integer | Starts at 1; returned as ETag. |

`UnitSetRow` is one row of the set: `unit_qty` of the row's unit
is `stock_qty` of the product's stocking unit, in canonical form.

| Field | Wire form | Note |
|---|---|---|
| `uom` | code | The row's unit. |
| `unit_qty` | quantity | The left side of the pair, scale 4. |
| `stock_qty` | quantity | The right side of the pair, scale 4. |
| `sell` | boolean | The row can be a sale unit. |
| `purchase` | boolean | The row can be a purchase unit. |
| `price` | boolean | The row can be a price unit. A change of the stocking unit (the `price_unit_held` hold of ADR 0006 9.1) is refused while the product has a nonzero `base_price_ten_thousandths`, a customer contract, a pricing rule with a fixed price, or a product price row (`product/unitset.go` `ProductPriceHeld`). |

`UnitSetPut` replaces the whole set and the four default columns at
the product's revision. A row may omit its pair (the derivations of
ADR 0006 section 3.2 fill it); a sent pair is stored canonically
and must agree with every derivation that applies. From C3-2B a
`base_price_ten_thousandths` body field is required whenever the
PUT changes `price_uom` (the stored base is a price per the old
unit); sent with an unchanged `price_uom` it simply sets the base
price (`ADR 0006` section 3.2). A field the PUT cannot apply (the
board measure columns) is a `400` naming it.

### Pair arithmetic and exactness

`Pair` is a conversion between two units as a pair of positive
scale 4 quantities (`core/internal/units/pair.go`): `A` of one unit
is the same goods as `B` of the other. The canonical form of R2
(ADR 0006 section 1, `units/pair.go:108-169`): if both `r = a/b`
and `1/r` are exact decimals at scale 4, the `1` goes on the side
that leaves the other side at least `1`; else if `r` is exact the
pair is `(r, 1)` and if `1/r` is exact the pair is `(1, 1/r)`; else
the lowest integer terms `(p, q)`, or `(p x k, q x k)` for the
largest `k` of `0.1, 0.01, 0.001, 0.0001` that fits the bound; a
ratio that fits the `NUMERIC(12,4)` bound in no form above is
refused where it is written, never at a line (`ErrOutOfBound`).
Worked: 187.5 PCS to 1 MBF is `(187.5, 1)`; 8 LF to 1 PCS is
`(8, 1)`; 1 BF of 2x4 is 1.5 LF so `(1, 1.5)`; 28 BF = 3 PCS of
2x4x14 is `(28, 3)`; GAL to CF is `(576, 77)`. The arithmetic over
both sides is exact rational over `int64` (`math/big.Rat`, never
`float64`, never parsing through text).

A quantity in a sale unit does not convert exactly into the
stocking unit at scale 4 is refused on the line by
`core/internal/units/convert.go`'s `InexactError` (ADR 0006 section
3.4 R5). Stock is never rounded, so the line is refused with the
two neighbouring quantities that do convert exactly named (one
below, one above); a neighbour the quantity bound (99999999.9999,
either sign) cannot hold is left out and the one that can is named
alone. The error names the stocking unit. The warning in
`UnitSetDoc.warnings` is the same idea read only: one entry per sell
row whose unit does not convert exactly into the stocking unit,
naming the finer stocking unit that would make every sell row exact
(an SF sell row on an EA-stocked paver). The warning does not
refuse.

`Quantity` (see `core/internal/platform/httpx/quantity.go` and
`core/api/openapi.yaml` `components.schemas.Quantity`): a JSON
string with at most four fraction digits
(`^-?(0|[1-9][0-9]*)(\\.[0-9]{1,4})?$`), carried internally as the
scale 4 integer. A JSON number never decodes into it. The
`QuantityMax` bound (99999999.9999) is enforced at the parse
boundary, never as a database fault on store.

### Tally on a quote line

A random length product is sold by tally (ADR 0006 section 4). The
table `line_tally_rows` holds one row per length the line carries,
indexed by `(quote_line_id, length_ft)`. Migration 099's step A3
backfills the stock columns on `quote_lines`: a line with a product
whose unit is in the product's set carries `stock_uom =
stocking_unit` and `stock_quantity = quantity converted`, exact at
scale 4. A line that does not convert exactly, or whose unit never
entered the set, keeps both `null` and is reported, and the next
edit meets the section 3.4 refusal.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)):

| Event | Source | Constant |
|---|---|---|
| `unit.created` | `core/internal/unit/service.go:33` | `EventUnitCreated` |
| `unit.updated` | `core/internal/unit/service.go:34` | `EventUnitUpdated` |

The unit set service and the product unit set handler do not write
their own events; the product's write is recorded as
`product.updated`.

## Scopes, roles and keys

A machine key reaching the unit catalogue routes needs `units:read`
for `GET` and `units:write` for every other method (ADR 0002; the
segment is the first path segment under `/api/v1/`). The product
unit set routes are reached under `products:read` and
`products:write`; the segment is `products`, the second path
component is `{id}/units`.

The user guard at the serve layer is composed at
`core/internal/app/serve/serve.go` `unit.NewHandler(unitSvc).RegisterRoutes(...)`:
the read guard is `admin`, `owner`, `sales`, `warehouse`,
`finance`, `cashier`, `purchasing`; the write guard is `admin`,
`owner`. The unit catalogue is dealer wide and so has no branch
wall. The product unit set inherits the product module's wall. A
key without the scope is `403 forbidden`; the audit row carries
the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 7, 7a, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) sections 2, 3, 4, 8: the catalogue, the conversion pair, the unit set, the tally and the report.

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

The pre flight report runs before migration 099:

```
cd core && go run ./cmd/core migrate -report units
```

(the old entry `go run ./cmd/migrate -report units` runs the same
report; both parse the flag through `migrate.ParseArgs` in
`core/internal/app/migrate/migrate.go`). It prints
the new dealer units it would insert (with row counts across the
columns) and the values that would abort the migration (exit code
`1`). Migration 099 inserts the 23 standard units
(`099_units_catalogue_and_sets.sql:70-93`); the seed does not
touch the catalogue, so after `make migrate` the report lists no
new dealer units for them.

Then, with an admin role bearer:

```
curl -X GET 'http://localhost:8080/api/v1/units/EA' \
  -H 'Authorization: Bearer <token>'
```

The transaction proofs in `core/internal/units/pair_test.go`,
`core/internal/units/convert_test.go`,
`core/internal/units/set_test.go`,
`core/internal/units/roundtrip_test.go` and
`core/internal/units/migration099_test.go` pin the pair arithmetic,
the inexact refusal, the dimension derivations, the wire
round-trip and the migration; the migration proof is the canonical
proof of the report.
