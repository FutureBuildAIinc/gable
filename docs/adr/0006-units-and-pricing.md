# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0006: units and pricing

## Status

Proposed for the Gable v1 refactor (item C3-0, the design stop before cycle
3's catalog, units and pricing items); accepted when the lead merges it after
its review. It stands on ADR 0001 (the wire contract, sections 7 and 7a
above all), ADR 0003 (the outbox), ADR 0005 (the sales and money core) and
the module recipe (`docs/refactor/MODULE-RECIPE.md`). Items C3-1 and C3-2
build from this record and the recipe; where they differ from it, this
record is changed first, in its own pull request.

This record supersedes four pieces of ADR 0005, each only from the moment
C3-2 lands: the units row of section 1 ("a stocked line is sold in its
product's stocking unit"), the `uom` rule of section 2.2, the unit of the
allocation invariant of section 5.4 (it now reads on `stock_quantity`, which
equals `quantity` for every line cycle 2 writes), and the
`unit_not_stock_unit` refusal of section 5.8. ADR 0005's Status says so.

## Context

Gable's units of measure are a closed Postgres enum of sixteen codes
(`uom_type`, migration 001) used by two columns, `products.uom_primary` and
`quote_lines.uom`; every other unit column is free text. A product has one
unit. There is no conversion anywhere: the product's price, its contract
price, its pricing rule fixed price and its stock are all silently "per
`uom_primary`". R1-15 gave quote lines ADR 0001's conversion pair, but the
pair is whatever the client sends, checked only for being positive, and
`price_uom` is any string matching `^[A-Z]{1,6}$`. ADR 0005 therefore had to
hold every stocked order line to its product's stocking unit until this
cycle.

The pricing engine (`core/internal/pricing/service.go`) is float64
throughout, rounds a rule or category derived price to cents
(`math.Round(x*100)/100`) and leaves a tier derived price unrounded, has no
notion of branch or unit, and carries two parallel customer pricing concepts:
`customers.tier` (an enum with multipliers hard coded in Go and repeated as
seeded category rules) and `customers.price_level_id` (a dealer named
multiplier). There are no branch prices and no vendor costs beyond a scale 2
purchase order line cost and the EDI catalogue staging table.

Lumber makes this the hard cycle. Commodity lumber is sold by the piece or by
a tally of pieces by length and priced per thousand board feet, and board
feet are rarely a terminating decimal: a 2x4 carries two thirds of a board
foot per linear foot, so a 10 foot piece is 6.666... board feet. Any design
that stores a board foot quantity or a conversion factor at scale 4 rounds
money. ADR 0001 already answered this for a line (the conversion is a pair of
quantities, not a factor); this record carries the same answer into the
catalogue, the unit sets, the tally and the pricing engine, so that nothing
between a price list row and an invoice line rounds except the one extension
ADR 0001 names.

## Decision

### 1. The arithmetic rules, stated once

Every number in this record obeys these five rules. Later sections cite them
by number.

- **R1. Ratios are pairs.** A conversion between two units is a pair of
  positive decimals at scale 4, `a` of one unit is the same goods as `b` of
  the other, never a single factor. The pair is exact for any rational ratio
  whose lowest terms fit the bound below; a factor at any fixed scale is not
  (one third has no finite decimal).
- **R2. Canonical form.** A pair is stored and written in one canonical
  form, so that equal ratios are equal bytes. For the ratio r = a / b:
  1. if r is an exact decimal at scale 4, the pair is (r, 1);
  2. else if 1 / r is, the pair is (1, 1 / r);
  3. else the pair is r in lowest integer terms (p, q);
  4. a pair whose sides exceed 99999999.9999 (the NUMERIC(12,4) bound,
     `httpx.QuantityMax`) in every form above is refused where it is
     written, never at a line.

  Examples: a 2x4x8 line sold by the piece and priced per MBF has the ratio
  187.5 PCS to 1 MBF, an exact decimal, so its pair is (187.5, 1); 1 BF of
  2x4 is 1.5 LF, so the BF row is (1, 1.5); 28 BF = 3 PCS of
  2x4x14 is (28, 3) because neither 28/3 nor 3/28 terminates. A client may
  send any equivalent pair; the server answers with the canonical one, the
  same posture as trailing zeros on a quantity.
- **R3. Exact until the one rounding.** Conversions, board feet, tally
  sums, price comparisons and derived prices are computed in exact rational
  arithmetic (`math/big`), from scale 4 integers, never float64 and never
  through text.
- **R4. The roundings, all of them.** There are exactly three, and no
  other rounding exists in units or pricing:
  1. the extension of a line, once, to cents, half away from zero, by
     `httpx.Extend` (ADR 0001 section 7a). This is the only rounding of
     money;
  2. a derived unit price (a percentage of a price, a markup or margin on a
     cost), once, to scale 4, half away from zero, at the moment the engine
     returns it (section 5.4). A fixed price (a price list row, a contract,
     a rule's fixed price) is never rounded. No price is ever converted from
     one unit to another: a line carries its price in the price's own unit
     and the pair does the conversion inside the extension;
  3. the `board_feet` display field of a tally, to scale 4, half away from
     zero (section 4.3). No arithmetic reads it.
- **R5. Stock moves exactly or not at all.** A quantity in a sale unit
  converts into the stocking unit exactly at scale 4, or the line is
  refused (section 3.4). Stock is never rounded.

### 2. The unit catalogue

#### 2.1 The table

`units` replaces the enum. One row per unit the dealer uses; seeded units are
locked, dealers add their own.

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `code` | TEXT PRIMARY KEY | `^[A-Z]{1,6}$`, immutable; the value every unit column holds | `code` |
| `name` | TEXT NOT NULL | 1 to 80 characters | `name` |
| `dimension` | TEXT NOT NULL CHECK (`COUNT`, `LENGTH`, `AREA`, `VOLUME`, `WEIGHT`, `BOARD_MEASURE`) | immutable once any product unit set or line names the unit | `dimension` (lowercase) |
| `std_unit_qty`, `std_ref_qty` | NUMERIC(12,4) NULL, both or neither, both > 0 | the unit's standard size: `std_unit_qty` of this unit = `std_ref_qty` of its dimension's reference unit (2.2); null for a unit whose size is per product (a box, a bundle) | `std_unit_qty`, `std_ref_qty` (decimal strings) |
| `is_system` | BOOLEAN NOT NULL DEFAULT FALSE | true on seeded rows: their code, dimension and standard size cannot change | `is_system` |
| `is_active` | BOOLEAN NOT NULL DEFAULT TRUE | an inactive unit cannot enter a new unit set row or a new line; existing rows keep it | `is_active` |
| `revision` | BIGINT NOT NULL DEFAULT 1 | ADR 0001 section 11 | `revision` |
| `created_at`, `updated_at` | TIMESTAMPTZ NOT NULL | | same |

No unit is ever deleted: every unit column references `units(code)` with
`ON DELETE RESTRICT`, and the routes offer deactivation only.

#### 2.2 Dimensions and the seed

Each dimension has one reference unit, the unit standard sizes are expressed
in: `COUNT` EA, `LENGTH` LF, `AREA` SF, `VOLUME` CF, `WEIGHT` LBS,
`BOARD_MEASURE` BF. Within one dimension two units that both carry a standard
size convert without any product data (R1: MBF to BF is (1, 1000)); across
dimensions, or for a unit without a standard size, the conversion is the
product's (section 3).

The seed, inserted only where absent:

| Code | Name | Dimension | Standard size |
|---|---|---|---|
| `EA` | Each | count | 1 = 1 EA |
| `PCS` | Pieces | count | 1 = 1 EA |
| `PAIR` | Pair | count | 1 = 2 EA |
| `DOZ` | Dozen | count | 1 = 12 EA |
| `C` | Hundred | count | 1 = 100 EA |
| `M` | Thousand | count | 1 = 1000 EA |
| `SET`, `BOX`, `CTN`, `BAG`, `BUNDLE`, `RL` | Set, Box, Carton, Bag, Bundle, Roll | count | none (per product) |
| `LF` | Linear foot | length | 1 = 1 LF |
| `SF` | Square foot | area | 1 = 1 SF |
| `SQ` | Square (roofing) | area | 1 = 100 SF |
| `CF` | Cubic foot | volume | 1 = 1 CF |
| `CY` | Cubic yard | volume | 1 = 27 CF |
| `GAL` | Gallon (US) | volume | 1728 GAL = 231 CF |
| `LBS` | Pound | weight | 1 = 1 LBS |
| `CWT` | Hundredweight | weight | 1 = 100 LBS |
| `TON` | Short ton | weight | 1 = 2000 LBS |
| `BF` | Board foot | board measure | 1 = 1 BF |
| `MBF` | Thousand board feet | board measure | 1 = 1000 BF |

The sixteen enum codes are all here, so every value stored today stays
valid. `M` and `CWT` are the price units ADR 0001 section 7a names. `GAL`
shows why the standard size is a pair: a gallon is 231 cubic inches and a
cubic foot 1728.

#### 2.3 Routes (C3-2, on the contract from birth)

- `GET /api/v1/units` (list envelope, ordering `units.code`, filters
  `dimension`, `is_active`), `GET /api/v1/units/{code}`.
- `POST /api/v1/units` (201, `Location`), `PUT /api/v1/units/{code}`
  (revision precondition; `name`, `is_active`, and `std_unit_qty` and
  `std_ref_qty` while no product or line names the unit; a field the PUT
  cannot change is a 400 naming it, the recipe's rule).
- Roles: read for every desk role; write for `admin` and `owner`. Machine
  keys: `units` joins the module scope table in
  `core/pkg/middleware/machinekey.go`, so `units:read` and `units:write`
  exist (ADR 0002).
- Events: `unit.created`, `unit.updated`, written last in the transaction.

### 3. Product unit sets

#### 3.1 The tables

`product_units`: one row per unit a product is stocked, sold, bought or
priced in.

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `product_id` | UUID NOT NULL FK `products` ON DELETE CASCADE | | (the parent) |
| `uom` | TEXT NOT NULL FK `units(code)` | | `uom` |
| `unit_qty`, `stock_qty` | NUMERIC(12,4) NOT NULL, both > 0 | `unit_qty` of this unit = `stock_qty` of the product's stocking unit; canonical (R2); (1, 1) on the stocking unit's own row | `unit_qty`, `stock_qty` (decimal strings) |
| `sell`, `purchase`, `price` | BOOLEAN NOT NULL | what the unit may be used for on this product: a line's `uom`, a purchase line's unit, a price's unit | same |
| `created_at` | TIMESTAMPTZ NOT NULL | | |
| | PRIMARY KEY (`product_id`, `uom`) | | |

New `products` columns:

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `uom_primary` | TEXT NOT NULL FK `units` (was `uom_type`) | the stocking unit; its row must exist with the pair (1, 1) | `stock_uom` (renamed on the wire by C3-1, section 8.1) |
| `sale_uom`, `price_uom`, `purchase_uom` | TEXT NOT NULL | the defaults a line, a price and a purchase line take; each a row of the set with the matching flag; composite FK `(id, x)` to `product_units(product_id, uom)`, DEFERRABLE INITIALLY DEFERRED | same |
| `board_thickness_in`, `board_width_in` | NUMERIC(7,4) NULL, both or neither, > 0 | the nominal cross section of a board measure product (a 2x4 is 2 and 4) | same, decimal strings |
| `board_length_ft` | NUMERIC(8,4) NULL, > 0 | the nominal length of a fixed length piece (a 2x4x8 is 8); null for random length | same |
| `random_length` | BOOLEAN NOT NULL DEFAULT FALSE | the product is sold by tally (section 4); requires `board_length_ft` null and an `LF` row | same |

The base price's unit is `products.price_uom` (section 5.2): today's
`base_price` meant "per `uom_primary`", and the backfill says so.

The stocking row invariant (the `uom_primary` row exists and is (1, 1)) is a
deferred constraint trigger on `product_units` and `products`, because the
two rows reference each other inside one transaction.

#### 3.2 Writing a unit set

`GET /api/v1/products/{id}/units` and `PUT /api/v1/products/{id}/units`
(C3-2). The PUT replaces the whole set and the four default columns, with the
product's revision (the product converts in C3-1, so it has one). The body:

```json
{
  "revision": 4,
  "stock_uom": "PCS",
  "sale_uom": "PCS",
  "price_uom": "MBF",
  "purchase_uom": "MBF",
  "units": [
    {"uom": "PCS", "sell": true,  "purchase": false, "price": true},
    {"uom": "LF",  "sell": true,  "purchase": false, "price": false},
    {"uom": "BF",  "sell": false, "purchase": false, "price": true},
    {"uom": "MBF", "sell": false, "purchase": true,  "price": true}
  ]
}
```

A row may omit `unit_qty` and `stock_qty`; the server derives the pair, in
this order, and refuses the row (400 naming `units[k].unit_qty`) when none
applies:

1. the stocking unit's own row is (1, 1);
2. a unit with a standard size in the same dimension as another row whose
   pair is known (sent, or derived earlier in this order) converts through
   the two standard sizes (MBF from BF, EA from PCS, SQ from SF);
3. a `LENGTH` unit on a product with `board_length_ft` converts through the
   piece: 1 PCS = `board_length_ft` LF;
4. a `BOARD_MEASURE` unit on a product with a cross section converts
   through LF (12 LF of the product = thickness x width BF) or, on a fixed
   length product, through the piece (12 PCS = thickness x width x length
   BF), then through rule 2 for MBF.

The 2x4x8 above stores PCS (1, 1), LF (1, 0.125), BF (1, 0.1875) and MBF
(1, 187.5). A 2x4 random length product stocked in LF stores LF (1, 1), BF
(1, 1.5) and MBF (1, 1500).

A row the client sends with a pair is stored canonically. Before it stores,
the write checks every ordered pair of rows in the set resolves within the
bound (R2 step 4), so a line can never meet an unrepresentable pair; the
offending row is a 400 naming `units[k]` with the message "the conversion
between X and Y does not fit the pair's bound".

Refusals beside the field errors (409 `conflict` with a blocker):

- `stock_unit_in_use`: the stocking unit changes while any inventory row of
  the product holds a nonzero `quantity` or `allocated`, or any open order
  line names the product. Changing what stock is counted in is a stock
  conversion, a cycle 4 adjustment act, not an edit.
- `unit_in_use`: a row is removed that a `product_prices` row, a contract, a
  pricing rule fixed price or a vendor cost names (the composite FKs make
  the database refuse it too; the service names it first).

Changing an existing row's pair is allowed and audited (an `audit_log` row
with before and after inside the transaction, R1-14): it changes future
resolutions only. Every line stores its own pair and stock quantity, so no
document already written moves (section 3.3).

The transaction: lock the product row `FOR UPDATE`, check the revision,
replace the rows, update the defaults, `revision = revision + 1`, the audit
row, then the event `product.updated` with `data.parts: ["units"]` last.

#### 3.3 Resolving a line's pair

A line naming a product and carrying `uom` U and `price_uom` P, both rows of
the product's set (`sell` on U, `price` on P), resolves its pair from the two
rows u = (u_unit, u_stock) and p = (p_unit, p_stock):

- 1 U is u_stock / u_unit stocking units; 1 P is p_stock / p_unit;
- x U = y P exactly when x / y = (u_unit x p_stock) / (p_unit x u_stock);
- the line's (`uom_qty`, `price_uom_qty`) is that ratio in canonical form
  (R2).

Worked: 2x4x8 sold by PCS, priced per MBF: (1 x 1) / (1 x 187.5), so x/y =
1/187.5; 1/r = 187.5 is exact, giving (187.5, 1), ADR 0001's own example. A
2x4x14 by the piece per MBF is (750, 7). Fasteners by EA per M are (1000, 1).

The resolved pair is **stored on the line** (`uom_qty`, `price_uom_qty`,
which every sales line already has) at the line's create or edit and never
re-resolved: a later unit set change does not touch a written document, the
same posture as kit explosion (ADR 0005 section 2.6). When a request sends a
pair for a product line it must equal the resolved pair as a ratio, or it is
a 400 naming `lines[i].uom_qty` ("does not match the product's unit set; omit
the pair or send 187.5 and 1"); omitting it is the normal case. A line
without a product (a non stock line) keeps today's rule: any active catalogue
unit, and the pair is the client's, except that two units with standard sizes
in one dimension derive it the same way (rule 2 of 3.2) and a sent pair must
agree.

The line's `uom` defaults to the product's `sale_uom` and its `price_uom` to
the price the engine resolves (section 5) or, on a manually priced line, to
the product's `price_uom`. This replaces R1-15's default of `price_uom` from
`uom`.

#### 3.4 The stock quantity

Every line that names a product (line types `product` and `component`, ADR
0005 section 2.1) stores, beside `quantity` and `uom`:

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `stock_uom` | TEXT NULL FK `units` | the product's `uom_primary` at the line's create | `stock_uom` |
| `stock_quantity` | NUMERIC(12,4) NULL | `quantity` x u_stock / u_unit, exact (R5); same sign as `quantity` | `stock_quantity` (decimal string) |

Both are null on lines without a product. When the product of the line
converts `quantity` into a stocking quantity that is not exact at scale 4,
the line is a 400 `validation_failed` naming `lines[i].quantity`: "does not
convert exactly into the stocking unit LF". This is rare by construction
when the dealer stocks in the finest unit the product is handled in (LF or
PCS for lumber, EA for fasteners), and 3.2's derivations make that the
natural choice; it is the price of never rounding stock.

Inventory, allocation, back orders, fulfilment, restock and cost of goods
sold all read `stock_quantity`, never `quantity`:

- ADR 0005 section 5.4's columns `quantity_allocated`,
  `quantity_backordered` and `quantity_fulfilled` are in the stocking unit,
  and its invariant reads `stock_quantity = quantity_allocated +
  quantity_backordered + quantity_fulfilled + closed remainder`. Every line
  cycle 2 writes has `stock_quantity = quantity`, so the change moves no
  value.
- ADR 0005 section 8.4's cost is `round_half_away(stock_quantity x
  unit_cost)`, `unit_cost` being per stocking unit, as it already is.
- An invoice that bills part of an order line bills `quantity_fulfilled`
  stocking units. The invoice line's `quantity` is that in the order line's
  `uom` (fulfilled x `quantity` / `stock_quantity`) when the result is exact
  at scale 4, and otherwise the invoice line bills in the stocking unit:
  `uom` = `stock_uom`, `quantity` = the stocking quantity, the pair
  re-derived between the stocking unit and the same `price_uom` from the
  order line's own stored quantities (no unit set read), the same unit
  price. The money stays exact either way (R4.1 is the only rounding); a
  full bill always bills in the order line's unit. Worked: 1 MBF of 2x4
  ordered (1500 LF), 700 LF fulfilled: 700 / 1500 MBF does not terminate, so
  the invoice line bills 700 LF at the order's MBF price with the pair
  (1500, 1).

### 4. Tallies and board feet

#### 4.1 What a tally is

A tally is the pieces by length a random length line carries: 10 pieces at
12 feet, 6 at 16 feet. The line's cross section (thickness and width) is the
product's, snapshotted on the line. A tally is allowed only on a line whose
product has `random_length`; its `uom` is then `LF` (sent or defaulted; any
other unit is a 400 naming `lines[i].uom`).

#### 4.2 The tables

`line_tally_rows`, one table for every sales line table:

| Column | Type | Rule |
|---|---|---|
| `id` | UUID PRIMARY KEY | |
| `quote_line_id`, `order_line_id`, `invoice_line_id`, `credit_memo_line_id`, `pos_line_item_id` | UUID NULL, each FK to its line table ON DELETE CASCADE | exactly one is set (CHECK `num_nonnulls(...) = 1`) |
| `position` | INTEGER NOT NULL | order of rows |
| `pieces` | INTEGER NOT NULL CHECK (`pieces` > 0 AND `pieces` <= 1000000) | always positive; a credit's sign is the line's |
| `length_ft` | NUMERIC(12,4) NOT NULL CHECK (`length_ft` > 0) | the piece length in feet |

A partial unique index per line column on (line, `length_ft`): one row per
length. A line carries at most 100 rows. C3-2 part A creates the table with
`quote_line_id` only; part B adds the other four columns and widens the
CHECK (section 8, part B).

Each sales line table gains `board_thickness_in` and `board_width_in`
(NUMERIC(7,4) NULL), set from the product when the line carries a tally on a
board measure product, null otherwise (a random length moulding tally has no
cross section).

One table with one foreign key column per line table was chosen over a
tally header table referenced from the lines (a header cannot cascade from
the line that owns it, so every quote edit, which replaces its lines, would
leave orphans) and over a JSONB column (no constraint could hold the pieces
positive or the lengths unique); see Alternatives.

#### 4.3 The arithmetic

For rows (pieces_i, length_i):

- `linear_feet` = sum of pieces_i x length_i, exact at scale 4 (integers
  times scale 4 decimals);
- the line's `quantity` = `linear_feet`, `uom` = `LF`. A request may send
  `quantity` with the tally; it must equal `linear_feet` or it is a 400
  naming `lines[i].quantity`. A quantity past `QuantityMax` is the usual 400;
- board feet = `linear_feet` x thickness x width / 12, exact (R3). It is
  never stored and never multiplied: the money goes through the line's pair,
  which 3.2 derived from the same thickness and width (12 LF = thickness x
  width BF), so the extension is exact board foot pricing;
- the extension is `httpx.Extend(linear_feet, uom_qty, price_uom_qty,
  unit_price)`, R4.1.

Worked, a 2x4 random length line at 500.00 per MBF (pair (1500, 1)):

| Tally | Linear feet | Board feet (exact) | `board_feet` on the wire | Extension |
|---|---|---|---|---|
| 10 at 8, 5 at 14 | 150 | 100 | `"100"` | 150 x 500.00 / 1500 = 50.00, 5000 cents |
| 10 at 10 | 100 | 200/3 | `"66.6667"` | 33.3333..., 3333 cents |

A 2x6 random length line (pair (1000, 1)), 10 at 12 and 6 at 16 at 525.00
per MBF: 216 linear feet, 216 board feet, 11340 cents.

On the wire every priced line carries `tally`, an object or `null`:

```json
"tally": {
  "thickness_in": "2",
  "width_in": "4",
  "rows": [
    {"pieces": 10, "length_ft": "8"},
    {"pieces": 5, "length_ft": "14"}
  ],
  "linear_feet": "150",
  "board_feet": "100"
}
```

`pieces` is a JSON integer (a count, not a quantity); `length_ft` and the
derived fields are decimal strings (ADR 0001 section 7a). `thickness_in`,
`width_in` and `board_feet` are `null` on a tally without a cross section.
The request carries only `rows`; `thickness_in`, `width_in`, `linear_feet`
and `board_feet` in a request are a 400 naming the field (read only, the
recipe's rule for fields a write does not apply). `board_feet` is R4.3's
display rounding and is read by nothing.

Conversion copies the tally: quote line to order line, order line to invoice
line, invoice line to credit memo line. A tallied line's sale unit and
stocking unit are both `LF`, so a partial bill is always exact in linear
feet; its invoice line carries the rows the fulfilment names (an optional
`tally` on the fulfilment's line, whose linear feet must equal the
fulfilled quantity) or none, and the bill of the line's last quantity
carries whatever rows remain. Rows are copied, never
shared, so editing one document's tally never moves another's.

#### 4.4 Stock by length: deferred to cycle 4

Cycle 3 stocks a random length product as one quantity in `LF` per inventory
row. A tallied line moves `stock_quantity` linear feet; the lengths are kept
on the line's tally rows and not decremented per length.

The reason: stock by length is a dimension of stock identity, and stock
identity (product, location down to bin, lot or serial, bundle or lift for
lumber) is ADR 0008's question, settled by C4-0. A per length inventory key
built here would be redefined there, a second migration of every inventory
row. Nothing in cycle 3's exit test needs it: a random length line prices by
board foot from its tally, and its stock moves exactly in linear feet. Cycle
3 leaves cycle 4 what it needs: every tallied line, on every document, keeps
its rows by length, so ADR 0008 can decrement per length (or per bundle
tally) from data that already exists. A dealer who must count stock by
length today keeps doing what many already do, one fixed length product per
length (2x4x8, 2x4x10), which this record supports fully.

### 5. Pricing

#### 5.1 What a price is

A price is a scale 4 amount per one price unit. Every stored price carries
its unit; the engine returns a price and its unit; a line stores both and
the pair (section 3.3). The units a price may be in are the product's rows
with `price` set.

#### 5.2 The tables

**Base price.** `products.base_price` (NUMERIC(12,4), unchanged) is the base
price per `products.price_uom` (new, 3.1). Keeping the base on the product
row rather than moving it into `product_prices` leaves every raw reader of
`base_price` (the portal, the counter, the AI load management seam, the seed)
reading the same value; section 7.4 lists what each must do about its unit.

**Price levels a dealer creates.** `price_levels` (exists, extended):

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `id` | UUID | | `id` |
| `code` | TEXT NOT NULL UNIQUE | `^[A-Z][A-Z0-9_]{0,15}$` | `code` |
| `name` | TEXT NOT NULL | | `name` |
| `basis` | TEXT NOT NULL CHECK (`BASE`, `AVERAGE_COST`, `REPLACEMENT_COST`) | what a level price is derived from when the product has no explicit price at this level | `basis` (lowercase) |
| `adjust_percent` | NUMERIC(9,4) NOT NULL, -100 < p <= 1000 | added to the basis: -10 is ten percent off the base; 25 on a cost basis is a 25 percent markup | `adjust_percent` (decimal string) |
| `position` | INTEGER NOT NULL | display order | `position` |
| `is_active` | BOOLEAN NOT NULL DEFAULT TRUE | | `is_active` |
| `revision`, `created_at`, `updated_at` | | | same |

`multiplier` is dropped after the backfill (section 8, A3).

**Product prices: explicit level and branch prices.** `product_prices`
(new):

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `id` | UUID | | `id` |
| `product_id` | UUID NOT NULL FK `products` ON DELETE CASCADE | | `product_id` |
| `price_level_id` | UUID NULL FK `price_levels` | null: the branch's base price | `price_level_id` |
| `branch_id` | UUID NULL FK `locations` (a `BRANCH` row) | null: every branch | `branch_id` |
| `price_uom` | TEXT NOT NULL | composite FK to `product_units(product_id, uom)`; the row has `price` | `price_uom` |
| `unit_price` | NUMERIC(12,4) NOT NULL CHECK (>= 0) | never rounded | `unit_price_ten_thousandths` |
| `revision`, `created_at`, `updated_at` | | | same |
| | CHECK (`price_level_id` IS NOT NULL OR `branch_id` IS NOT NULL) | the row with neither is the product's base | |
| | UNIQUE NULLS NOT DISTINCT (`product_id`, `price_level_id`, `branch_id`) | one price per level per branch | |

**Customer price profiles.** `price_profiles` (new): `id`, `code` (as a
level's), `name`, `price_level_id UUID NOT NULL FK price_levels`,
`is_active`, `revision`, timestamps. A profile is the shareable pricing
program a set of customers is on: it names their price level and carries its
own category rules (5.3). `customers.price_profile_id UUID NOT NULL FK
price_profiles`, with DEFAULT `price_profile_default_id()` (a SQL function
returning the `RETAIL` profile, ADR 0005's raw writer pattern for terms).
`customers.tier` and `customers.price_level_id` are dropped after the
backfill.

**Contracts.** `customer_contracts` (exists) gains `price_uom TEXT NOT NULL`
(composite FK to the product's set), `branch_id UUID NULL FK locations`,
`revision`; `customer_id` and `product_id` become NOT NULL; the unique key
becomes NULLS NOT DISTINCT (`customer_id`, `product_id`, `branch_id`).

**Pricing rules.** `pricing_rules` (exists) gains `price_uom TEXT NULL`,
required when `fixed_price` is set on a rule that names a product (a CHECK);
a fixed price on a rule without a product is per each product's
`price_uom`. `min_quantity` and `max_quantity` are in the product's stocking
unit (what they meant when every quantity was). `revision` added.

**Category rules.** `category_pricing_rules` (exists): `target_type` becomes
`ACCOUNT`, `PROFILE` or `LEVEL`, with `price_profile_id` and `price_level_id`
columns replacing `tier`, the CHECK requiring the one that matches. A `FIXED`
category rule's value is per each product's `price_uom`.

#### 5.3 The resolution order

`pricing.Resolve(ctx, Request) (Resolved, error)` is the one entry point
every module prices through. The request: customer, product, branch, the
line's quantity and `uom`, the job (optional), the branch's business date.
Before any rung, the engine computes:

- the **reference price**: the `product_prices` row (level null, the branch)
  if present, else `products.base_price`, each with its own unit;
- the **stocking quantity** of the request (R3, exact), which quantity
  breaks compare against.

The rungs, first match wins, the brief's four price sources in its order
(contract, customer level, branch, base) with today's rules and category
rules kept where today's engine has them:

| Rung | Source | Price and its unit | `price_basis` |
|---|---|---|---|
| 1 | Contract: `customer_contracts` for (customer, product), the branch's row before the all branch row | its fixed price, its `price_uom` | `contract` |
| 2 | Pricing rules: job override, promotional, quantity break, by today's `selectRule` (priority, then the waterfall order, quantity break ties to the lowest price) | a fixed price in its `price_uom`, or a percent of the reference price in the reference's unit, or a markup on cost in the stocking unit | `job_override`, `promotional`, `quantity_break` |
| 3 | Category rules: account, then profile, then level (the profile's), each exact category then ancestor | as today's `ApplyRule`, in the reference's unit (markdown, fixed) or the stocking unit (markup, margin on cost) | `category_account`, `category_profile`, `category_level` |
| 4 | Customer level: the profile's level. Its explicit price at the branch, else its explicit price at every branch, else derived: `BASE` from the reference price; `AVERAGE_COST` from `products.average_unit_cost`; `REPLACEMENT_COST` from the product's primary vendor's current cost (5.5) at its default vendor level, falling back to average cost (named in `details`) | explicit: its own unit; derived: the unit of what it derives from | `level` |
| 5 | Branch base: `product_prices` (level null, the branch) | its unit | `branch` |
| 6 | Base: `products.base_price` | `products.price_uom` | `base` |

Branch prices reach every customer through the reference price: a `BASE`
level derives from the branch's base when the branch has one. A level whose
derivation yields exactly the reference price (a 0 percent `BASE` level, the
seeded `RETAIL`) still answers `level`. A cost basis with no cost to read
(average cost zero or null, and no vendor cost) derives nothing and falls to
rung 5, `details` naming why. Rungs 5 and 6 are otherwise reached only by a
customer whose profile's level is inactive, and by the anonymous price reads
(the portal catalogue without a customer, the counter before a customer is
chosen).

The margin floor that rules and category rules carry today is kept, with its
current meaning (a floor of `reference x (1 - floor / 100)`), and so is the
quantity break tie rule. Wherever two prices in different units are compared
(the floor, the tie, the volume ladder of `VolumeBreaks`), both are converted
to an exact rational price per stocking unit for the comparison only (R3);
the winner keeps its own value and unit.

The engine's answer, `Resolved`: `unit_price` (`httpx.Price`), `price_uom`,
the line's pair for the request's `uom` and that `price_uom` (3.3),
`price_basis`, `details` (the human text today's engine writes), the rule or
row id that won (null for derived and base), and the reference price and its
unit (so an override shows against it).

#### 5.4 No rounding before the extension

A fixed price is returned as stored. A derived price (rung 2 percent or
markup, rung 3 markdown, markup or margin, rung 4 derived) is computed
exactly and rounded once to scale 4 (R4.2): a 10 percent level on a base of
1.3725 is 1.23525, returned as 1.2353. Nothing in the engine rounds to cents;
the first and only cent rounding is the line's `Extend`. Today's engine
rounds rule and category prices to cents and leaves tier prices unrounded,
in float64; C3-1 fixes both (section 10).

#### 5.5 Vendor price levels (the cost side)

`vendor_price_levels` (new): `id`, `vendor_id UUID NOT NULL FK vendors`,
`code` (as a price level's, unique per vendor), `name`, `is_default BOOLEAN
NOT NULL` (a partial unique index: one default per vendor), `is_active`,
`revision`, timestamps. A vendor's levels are its buying programs: list,
truckload, mixed car.

`vendor_product_costs` (new):

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `id` | UUID | | `id` |
| `vendor_id` | UUID NOT NULL FK `vendors` | | `vendor_id` |
| `vendor_price_level_id` | UUID NOT NULL FK `vendor_price_levels` | of the same vendor (checked in the write) | `vendor_price_level_id` |
| `product_id` | UUID NOT NULL FK `products` ON DELETE CASCADE | | `product_id` |
| `branch_id` | UUID NULL FK `locations` | null: every branch | `branch_id` |
| `purchase_uom` | TEXT NOT NULL | composite FK to the product's set; the row has `purchase` | `purchase_uom` |
| `unit_cost` | NUMERIC(12,4) NOT NULL CHECK (>= 0) | never rounded | `unit_cost_ten_thousandths` |
| `vendor_sku` | TEXT NULL | | `vendor_sku` |
| `effective_from` | DATE NOT NULL | the branch's business date | `effective_from` |
| `effective_to` | DATE NULL, >= `effective_from` | | `effective_to` |
| `revision`, `created_at`, `updated_at` | | | same |
| | UNIQUE NULLS NOT DISTINCT (`vendor_id`, `vendor_price_level_id`, `product_id`, `branch_id`, `effective_from`) | | |

`pricing.ResolveCost(ctx, vendor, product, branch, level, on date)`: rows at
the requested level (the vendor's default when none is named), in effect on
the date, the branch's row before the all branch row, the latest
`effective_from` first; when the requested level has none, the default
level's. It answers `unit_cost`, `purchase_uom`, the pair between the
purchase line's unit and `purchase_uom` (3.3), the level code and the row id,
or no cost (a purchase line then takes a manual cost). It never rounds.

Purchasing reads it from cycle 4: C4-1 converts purchase orders and gives
their lines the line shape (unit, price unit, pair, scale 4 cost); the EDI
catalogue staging (`edi_catalog_entries`) and the vendor feed intake of
C4-0 write `vendor_product_costs`. C3-2 builds the tables, the routes and the
resolver, and rung 4's `REPLACEMENT_COST` reads it.

#### 5.6 Routes

C3-1 converts the existing pricing routes (`/api/v1/pricing/calculate`,
`/rules`, `/categories`, `/category-rules` and their bulk and audit routes,
`/matrix`, `/resolve`) onto the recipe. C3-2 adds, all under
`/api/v1/pricing/` so the `pricing` scope covers them:

- `GET`, `POST /pricing/levels`; `GET`, `PUT /pricing/levels/{id}`.
- `GET`, `POST /pricing/profiles`; `GET`, `PUT /pricing/profiles/{id}`.
- `GET /pricing/products/{id}/prices`, `PUT /pricing/products/{id}/prices`
  (the product's whole price list, base excluded, replaced at the product's
  revision; each row carries its own revision for reads).
- `GET`, `POST /pricing/vendor-levels` (filter `vendor_id`); `GET`, `PUT
  /pricing/vendor-levels/{id}`.
- `GET /pricing/vendor-costs` (filters `vendor_id`, `product_id`,
  `branch_id`, `on`), `POST`, `GET`, `PUT /pricing/vendor-costs/{id}`.
- `GET /pricing/cost?vendor_id&product_id&branch_id&vendor_level_id&on&uom`:
  `ResolveCost` on the wire.

`GET /api/v1/price_levels` (the customer module's read today) is retired by
C3-2 in favour of `/pricing/levels`, listed in `CONTRACT-CHANGES.md`, its
desk caller updated. Price writes are `admin`, `owner` and `finance`; reads
add `sales`; `calculate` is also open to every role the counter routes
admit, since the counter prices through it.

`GET /api/v1/pricing/calculate` after C3-2 takes `customer_id`,
`product_id`, `quantity`, `uom` (default the product's `sale_uom`),
`branch_id` (default the caller's branch from the branch middleware, else
none, which skips rungs 1 to 5's branch rows), `job_id`, and answers:

```json
{
  "customer_id": "<uuid>", "product_id": "<uuid>", "branch_id": "<uuid>",
  "quantity": "120", "uom": "PCS",
  "unit_price_ten_thousandths": 5250000, "price_uom": "MBF",
  "uom_qty": "187.5", "price_uom_qty": "1",
  "line_total_cents": 33600,
  "price_basis": "level", "details": "Builder (Level)",
  "reference_price_ten_thousandths": 5750000, "reference_price_uom": "MBF"
}
```

Events: `price_level.created`, `price_level.updated`,
`price_profile.created`, `price_profile.updated`, `product.updated` with
`data.parts: ["prices"]` for a price list write, `vendor_cost.created`,
`vendor_cost.updated`.

### 6. The wire, collected

Every field below follows ADR 0001: quantities, pair sides, unit set pairs,
standard sizes, dimensions in inches and feet, percentages and board feet
are decimal strings; prices are `_ten_thousandths` integers; amounts are
`_cents` integers; unit codes keep their uppercase form (section 6 of ADR
0001: units are a standard vocabulary); `dimension`, `basis` and
`price_basis` are product vocabularies, lowercase.

| Where | Fields added or changed |
|---|---|
| unit | `code`, `name`, `dimension`, `std_unit_qty`, `std_ref_qty` (nullable), `is_system`, `is_active`, `revision`, timestamps |
| product (C3-1) | `stock_uom` replaces `uom_primary`; `base_price_ten_thousandths` replaces `base_price` |
| product (C3-2) | `sale_uom`, `price_uom`, `purchase_uom`, `board_thickness_in`, `board_width_in`, `board_length_ft` (nullable), `random_length`; `units` (the set, on the product read and its own route) |
| unit set row | `uom`, `unit_qty`, `stock_qty`, `sell`, `purchase`, `price` |
| price level | `id`, `code`, `name`, `basis`, `adjust_percent`, `position`, `is_active`, `revision` |
| product price | `id`, `product_id`, `price_level_id`, `branch_id`, `price_uom`, `unit_price_ten_thousandths`, `revision` |
| price profile | `id`, `code`, `name`, `price_level_id`, `is_active`, `revision` |
| customer (C3-2) | `price_profile_id` replaces `tier` and `price_level_id` |
| contract | `price_uom`, `branch_id`, `revision` |
| vendor level, vendor cost | as 5.5 |
| every sales line (quote, order, invoice, credit memo, counter) | `stock_uom`, `stock_quantity` (null without a product), `tally` (null without one); order and counter lines add `price_basis` (null on lines the engine did not price) |
| quote line | `price_uom` must be a catalogue unit and, on a product line, a price unit of the product (replacing the `^[A-Z]{1,6}$` check) |

The new line fields are present on every line, `null` where they do not
apply (ADR 0001 section 12), so a client reads one line shape.

### 7. What changes in each module

#### 7.1 Products and PIM

C3-1: the recipe (wire types, list envelope with keyset `(created_at, id)`,
revision on products, the error envelope, `stock_uom` and the scaled base
price, quantities such as `reorder_point` as decimal strings, `total_quantity`
and `total_allocated` replaced by `on_hand`, `allocated` and `available` in
the stocking unit). PIM's product detail carries the same product summary.
C3-2: the unit set and its route, the board measure columns, the four
default units, the product read's `units`.

#### 7.2 Locations and inventory

C3-1 carries the location and branch routes onto the recipe, and the
inventory read `GET /api/v1/inventory` onto the list envelope with
`include=product` (the product summary embedded) and `available`
(`quantity - allocated`) in the stocking unit, `uom` beside it. Inventory
writes stay for C4-1. C3-2 changes nothing in inventory's storage: stock was
and stays in the stocking unit; its callers pass `stock_quantity`.

#### 7.3 Pricing

C3-1: the engine moves from float64 to `httpx.Price` and exact arithmetic
with the one scale 4 rounding (5.4), its routes onto the recipe, the price
read answering `unit_price_ten_thousandths` with `price_uom` (the product's
`uom_primary` until C3-2), the pair, `line_total_cents` and `price_basis`
(today's sources lowercased). `quantity` and `job_id` that do not parse
become 400s (today they are ignored). ADR 0005's wrapper at the order and
counter boundary (its section 1) is deleted when C3-1 lands, since the
engine now answers scaled prices itself.

C3-2: levels with basis, profiles, branch prices, explicit level prices, the
units on every price, the six rung resolution, vendor levels and costs, and
`price_basis` gaining `category_profile`, `category_level`, `level`,
`branch` and `base` in place of `category_tier`, `tier` and `retail`.

#### 7.4 Quotes

C3-2: `uom` and `price_uom` validated against the catalogue and, on a
product line, the product's set; the pair resolved and stored by the server
(3.3); `stock_uom` and `stock_quantity` computed and stored, so a quote
that could not convert is refused when it is written, not when it converts;
`tally` accepted on random length product lines. The exposure scanner, the
portal quote read and the integration seam already apply the pair (R1-15)
and read nothing new. `quote_lines.uom` moves from the enum to TEXT with the
catalogue FK (A1); its readers that cast `uom::text` keep working.

#### 7.5 Orders, invoices, credit memos and the counter

C3-2 part B, after C2-2, C2-3 and C2-5 have merged:

- every line that names a product resolves its pair and stock quantity from
  the product's set (3.3, 3.4) and may carry a tally (4);
- the order line's `uom` may be any sale unit of the product: ADR 0005's
  stocking unit rule and the convert route's `unit_not_stock_unit` blocker
  are removed;
- a `PRICE_LIST` line prices through `pricing.Resolve` with the document's
  branch and stores `price_basis`, the price and its unit; `QUOTE`,
  `OVERRIDE` and `MANUAL` keep ADR 0005's meaning, and an override's
  `priced_unit_price` is the engine's answer in the engine's unit (an
  override in another unit is refused with a 400 naming
  `lines[i].price_uom`: the override and the engine's answer must be
  comparable in the audit row);
- allocation, fulfilment, restock and COGS read `stock_quantity` (3.4);
- the invoice bills a partial line as 3.4 says, and copies the tally (4.3);
- a sub cent unit price is stored at scale 4 at every step (ADR 0005 widened
  every line's `unit_price`); nothing in this item converts a price.

#### 7.6 Readers of `base_price` and `uom_primary` outside these modules

Recipe step 9: the base price now means "per `products.price_uom`", which
after the backfill equals `uom_primary` for every product, so no value
changes meaning at migration time. It changes the moment a dealer sets a
different price unit. Each raw reader multiplies by the pair or refuses what
it cannot carry, in C3-2 part B: the counter's product lookups
(`pos/repository.go`, which price at `base_price` per `uom_primary`), the
portal catalogue and reorder (`portal/repository.go`; the reorder also
bypasses customer pricing, and C2-2 already moves it onto the order create),
and the seed. The AI load management seam (`integrations/ailm_store_pg.go`)
reads `uom_primary::text` and `base_price` for its frozen contract: both keep
their values, so its golden does not change, and the cast is a no op on TEXT.

### 8. Migrations, in order

Each item lands one numbered migration (the next free number when it merges
`refactor/v1`) and its down file. Every step is idempotent and every backfill
reads only columns earlier steps made NOT NULL. Each is applied to an empty
database and to a seeded one, with the backfill tested on rows that exist.

**C3-1, `catalog_pricing_wire_contract`.**
1. `products.created_at`, `price_levels.created_at`,
   `customer_contracts.created_at`, `pricing_rules` and
   `category_pricing_rules` timestamps: fill, NOT NULL. `revision` on
   `products`, `locations`, `pricing_rules`, `category_pricing_rules`,
   `customer_contracts`.
2. `products.reorder_point`, `reorder_qty` widened from DECIMAL(10,4) to
   NUMERIC(12,4), the bound `Quantity` enforces, so a quantity the wire
   accepts is one the column holds.
3. Keyset indexes for the converted lists: `products (created_at DESC, id
   DESC)`, `locations`, `pricing_rules`, `category_pricing_rules`.

**C3-2 part A, `units_and_pricing`.**
1. **A1, the catalogue.** Create `units`; insert the seed of 2.2 (`ON
   CONFLICT DO NOTHING`). Collect every distinct unit value stored in
   `products.uom_primary`, `quote_lines.uom`, `quote_lines.price_uom`,
   `pos_line_items.uom`, `pos_return_lines.uom` and, when they exist, the
   ADR 0005 line tables' `uom` and `price_uom`, each `upper(trim())`: a value
   matching `^[A-Z]{1,6}$` and not in the catalogue is inserted (dimension
   `COUNT`, no standard size, `is_system` false) and reported by `RAISE
   NOTICE` for the dealer to review; a value that does not match aborts the
   migration with `RAISE EXCEPTION` naming the table and the value (never a
   guessed mapping). Rewrite those columns to the normalised value.
   `products.uom_primary` and `quote_lines.uom` `TYPE TEXT USING
   col::text`; then `DROP TYPE uom_type`. FKs to `units(code)` on every
   column above. `edi_catalog_entries.uom` holds vendors' codes, not the
   dealer's, and gets no FK (C4-0 maps vendor units).
2. **A2, unit sets.** `products`: the board measure columns and
   `random_length` with their CHECKs. Create `product_units`; insert one row
   per product: `uom_primary`, (1, 1), `sell`, `purchase`, `price` all true.
   Add `sale_uom`, `price_uom`, `purchase_uom`, backfilled to `uom_primary`,
   NOT NULL, the deferred composite FKs; the stocking row constraint trigger.
   Then the quote pairs: for each quote line with a product whose
   `price_uom` differs from its `uom`, and one of the two is the product's
   `uom_primary`, the other unit becomes a row of the product's set with the
   pair the line carries, oriented to the stocking unit and made canonical
   (`price` set when it is the price unit, `sell` when it is the sale unit);
   when several lines name one (product, unit) the latest line by
   `(created_at, id)` wins and the migration reports how many disagreed. A
   line whose two units are both not the stocking unit adds nothing and is
   counted in the report; the line keeps its stored pair (history is not
   re-resolved, 3.3).
3. **A3, levels, profiles and prices.**
   - `price_levels`: add `code` (backfilled from `name`, uppercased, runs of
     other characters to `_`, a numeric suffix on collision), `basis`
     (`BASE`), `adjust_percent` = `(multiplier - 1) x 100` (exact: a scale 4
     multiplier is a scale 2 percent), `position` by `(created_at, id)`,
     `is_active`, `revision`; then UNIQUE (`code`), NOT NULLs, drop
     `multiplier`.
   - Tier levels: for each of `SILVER`, `GOLD`, `PLATINUM` that any
     customer's `tier` or any category rule's `tier` names, a level with
     that code if none exists: `BASE`, -10, -15, -20, the multipliers the
     engine hard codes today. `RETAIL` exists as the seeded `Retail` level
     (code `RETAIL`, 0 percent).
   - `price_profiles`; one profile per distinct (`price_level_id`, `tier`)
     combination the customers hold, so every customer's price after the
     migration is the price before it: a customer with a level keeps that
     level; one without a level takes its tier's level; and where a customer
     holds both a level and a non retail tier, its profile also receives a
     `PROFILE` copy of every `TIER` category rule of that tier, because
     today's category step reads the tier before the level is consulted. The
     `RETAIL` profile always exists and backs the column default.
   - `customers.price_profile_id`: backfilled, NOT NULL, DEFAULT
     `price_profile_default_id()`; then drop `customers.tier`,
     `customers.price_level_id` and `DROP TYPE customer_tier`.
   - `category_pricing_rules`: add `price_level_id`, `price_profile_id`;
     `TIER` rows become `LEVEL` rows naming the tier's level; the profile
     copies above; the new CHECK; drop `tier`.
   - `product_prices` (empty: there is no branch or level price today).
   - `customer_contracts`: rows with a null `customer_id` or `product_id`
     cannot match today's lookup (it filters on both); they move to
     `customer_contracts_orphaned` (same columns) and are reported, then both
     columns NOT NULL. `price_uom` backfilled to the product's `uom_primary`,
     NOT NULL; `branch_id`; the new unique key.
   - `pricing_rules.price_uom`: backfilled to the product's `uom_primary`
     where the rule names a product and has a `fixed_price`; the CHECK.
4. **A4, vendor levels.** `vendor_price_levels`, `vendor_product_costs`.
   Backfill one default level `LIST` per existing vendor. No costs are
   backfilled: purchase order line costs are history at scale 2 with no
   unit, and `edi_catalog_entries` is not linked to `vendors`; C4-0's feed
   intake is the first writer.
5. **A5, quote tallies.** `line_tally_rows` with `quote_line_id`;
   `quote_lines.board_thickness_in`, `board_width_in`, `stock_uom`,
   `stock_quantity`. Backfill: for lines with a product whose `uom` is in
   the product's set (after A2), `stock_uom` = `uom_primary` and
   `stock_quantity` = `quantity` converted (exact; a line that does not
   convert exactly keeps both null and is reported, and its next edit
   meets 3.4's refusal).

**C3-2 part B, `sales_line_units`** (after C2-5 has merged):
1. `order_lines`, `invoice_lines`, `credit_memo_lines`, `pos_line_items` and
   the counter return lines: `stock_uom`, `stock_quantity`,
   `board_thickness_in`, `board_width_in`; `order_lines` and
   `pos_line_items` add `price_basis TEXT NULL`. Backfill for lines with a
   product: `stock_uom` = `uom`, `stock_quantity` = `quantity` (exact: ADR
   0005 kept every such line in its stocking unit). CHECK: `stock_uom` and
   `stock_quantity` both set exactly when `product_id` is set.
2. FKs to `units(code)` on every line table's `uom`, `price_uom` and
   `stock_uom` (A1 normalised the values).
3. `line_tally_rows`: add `order_line_id`, `invoice_line_id`,
   `credit_memo_line_id`, `pos_line_item_id` with their FKs and partial
   unique indexes; replace the CHECK with the five column form.

Down files: each reverses its own steps. A1's down recreates `uom_type` and
casts back only when every stored value is one of its sixteen codes, and
otherwise raises an exception naming the first value that is not (a down
never discards a dealer's unit). A3's down restores `tier` and
`price_level_id` from the profiles it made.

### 9. The items

Every item follows the recipe end to end (wire tests first, migration,
model, parse, repository, service, handler, contract fragment, goldens,
`CONTRACT-CHANGES.md`, desk and portal callers, Playwright), proves each live
failure it fixes with a test that fails on its base commit, carries the
transaction proofs (a failing event write rolls the write back; three
contenders at pool size 4; the gated saturation test for every kind of
write), and is money class: two independent reviews.

**C3-1: products, PIM, locations, pricing onto the recipe (40 to 70).**
Builds 7.1 to 7.3's C3-1 parts and its migration. The engine's arithmetic
moves to `httpx.Price` with R3 and R4.2, as its own commit with its unit
tests before any route changes. Tests:
- the engine: a rule discount, a category markdown, a markup and a margin on
  cost each return the exact scale 4 rounding of the exact value (1.3725 at
  10 percent off is 1.23525, returned as 12353), never a cent rounding, with
  a test that fails on the base commit (today's rule path answers a cent
  value); the tier path rounded to scale 4 where today it is
  unrounded; no float64 left in `internal/pricing` outside the escalator and
  exposure code that C3-1 does not convert;
- `GET /pricing/calculate`: the scaled price, `price_uom`, the pair,
  `line_total_cents` by `Extend`, `price_basis` lowercase; a bad `quantity`
  or `job_id` is a 400 naming it (fails on the base: ignored today); the
  strict query guard;
- the product, location, branch and inventory reads: the recipe's wire set
  (envelope, cursor walk, `include=total`, `include=product` on inventory,
  `available` as a decimal string, the branch wall on every read);
- revision on product and location writes: 428, 409 `stale_revision`,
  `If-Match`; the concurrency test for each write that opens a transaction;
- the AI load management golden unchanged byte for byte.

**C3-2: units and pricing parity per this record (92 to 156).** Two pull
requests, because its line changes touch cycle 2's tables: part A (64 to
104) after C3-1 and C2-1 have merged, part B (28 to 52) after C2-5 has
merged.

Part A builds the catalogue (2), unit sets and resolution (3.1 to 3.3, the
`internal/units` package holding the pair arithmetic, canonical form, stock
conversion and tally arithmetic, with no database), quote line units and
tallies (3.4, 4, 7.4), pricing (5), the vendor cost side (5.5), the routes,
and migration part A. Part B builds 7.5 and 7.6 and migration part B.

Tests, part A:
- `units`: canonical form on every case of R2 (terminating ratio,
  terminating inverse, integer terms, out of bound); `Convert` exact or
  reported inexact; the board foot and tally arithmetic, including 200/3;
- the unit set PUT: each derivation rule of 3.2 (the 2x4x8 and the 2x4
  random length sets above read back exactly), a client pair stored
  canonically, a pair out of bound refused naming the row, `stock_unit_in_use`
  and `unit_in_use` refusals, the audit row, the revision and concurrency
  proofs;
- the catalogue routes: create, deactivate, a locked system row's PUT
  refused naming the field, an inactive unit refused on a new set row and a
  new line, the `units` machine key scope (read 200, write 403 with its
  audit row);
- quotes: a product line's pair resolved and stored, a disagreeing pair
  refused, a non sale unit refused, a quantity that does not convert into
  the stocking unit refused, a tally priced and stored, and a quote with a
  tally converting with its rows copied (after part B, the convert test is
  extended through the invoice);
- pricing: each of the six rungs wins in its order with the rung above it
  absent, branch rows beating all branch rows, explicit level prices beating
  derived ones, each basis of a level, `REPLACEMENT_COST` falling back to
  average cost, a price in MBF compared to a floor in PCS correctly, the
  migration's behaviour preservation (a fixture of customers with every
  (level, tier) combination priced on the base commit's engine and after
  the migration, equal to the ten thousandth);
- vendor costs: the resolver's branch, level, default level fallback and
  effective date rules; a cost in MBF for a purchase line in PCS answering
  the right pair.

Tests, part B:
- each line table: the pair and stock quantity resolved and stored; a non
  stocking sale unit on an order line accepted (the cycle 2 refusal gone);
  allocation, back order, fulfilment and restock moving `stock_quantity`;
  COGS on `stock_quantity`; a partial bill in the order's unit and one that
  falls back to the stocking unit, both summing to the right cents;
- `price_basis` stored from the engine; an override in another unit refused;
- the counter and portal readers of `base_price` applying the pair (an
  `MBF` priced product rung at the counter by the piece), recipe step 9.

**The cycle 3 exit test, line by line:**

| Exit test line | Test (item) |
|---|---|
| a conversion round trips on every unit pair defined | `units.TestEveryDefinedPairRoundTrips` (A): for every product in a fixture catalogue covering each derivation and each dimension (2x4x8, 2x4x14, 2x4 random length, 4x8 sheet in PCS and SF, fasteners in EA, BOX and M, rebar in LF and CWT, paint in GAL and CF), and every ordered pair (A, B) of its set: the pair for (A, B) is the swap of the pair for (B, A); a sample of scale 4 quantities converted A to B and back returns itself whenever the forward result is exact, and the exact rationals agree when it is not; plus the standard size pairs of the seed. `product.TestUnitPairWire` (A) runs the same over `GET /products/{id}/units` and a quote line per pair, and the migration test runs it over every product of the seeded database after A2 |
| the resolved factor is stored on each line | `quote.TestLinePairResolvedAndStored` (A) and `order`, `invoice`, `creditmemo`, `pos` `TestLinePairResolvedAndStored` (B): a line sent with `uom` and `price_uom` only stores the resolved `uom_qty`, `price_uom_qty`, `stock_uom` and `stock_quantity` (read from the columns, not the response), and a later unit set change leaves them as they were |
| a sub cent unit price carries from price list to invoice line without loss | `invoice.TestSubCentPriceListToInvoice` (B): a `product_prices` row of 1.3725 per EA at the customer's level, an order line priced `PRICE_LIST` (13725, `price_basis` `level`), fulfilled and invoiced: the invoice line's `unit_price` column is 1.3725, its wire value 13725, its `line_total_cents` `Extend`'s (7 EA, 961); and the same through a quote at 0.00375 per EA expressed as 3.75 per M |
| a random length line prices by board foot | `quote.TestRandomLengthTally` (A) and `invoice.TestRandomLengthTallyBilled` (B): the 2x4 tally of 4.3 at 500.00 per MBF is 150 LF, `board_feet` "100", 5000 cents, and the 10 at 10 tally is 3333 cents with `board_feet` "66.6667"; quoted, converted, fulfilled and invoiced with the rows copied and the cents unchanged |

### 10. Where today's code contradicts this design

| Today | Where | Fixed by |
|---|---|---|
| Units are a closed enum a dealer cannot extend, used by two columns; the other unit columns are free text | migration 001, 003, 027, 079 | C3-2 (A1) |
| A product has one unit and nothing converts; base, contract and rule prices are implicitly per `uom_primary` | migrations 001, 006, 007, 016 | C3-2 (A2, A3) |
| A quote line's pair is whatever the client sends, and `price_uom` is any six letter code | `quote/input.go`, `quote/service.go` (R1-15) | C3-2 (A) |
| A stocked order line must be in its stocking unit | ADR 0005 sections 1, 2.2, 5.8 | C3-2 (B) |
| The engine is float64, rounds rule and category prices to cents, and leaves tier and level prices unrounded | `pricing/service.go` `CalculatePriceWithQty`, `category_service.go` `ApplyRule` | C3-1 |
| The volume ladder's saving is rounded to cents in float | `pricing/volume_breaks.go` | C3-1 |
| A bad `quantity` or `job_id` on the price read is silently ignored | `pricing/handler.go` `HandleCalculatePrice` | C3-1 |
| Two parallel customer pricing concepts: the tier enum with multipliers hard coded in Go and seeded again as category rules, and the price level multiplier | migrations 003, 006, 050; `pricing/service.go` step 5b | C3-2 (A3) |
| No branch prices; the engine has no branch | `pricing` | C3-2 |
| Contracts may have a null customer or product | migration 006 | C3-2 (A3) |
| The price level read lives in the customer module | `customer/handler.go` `GET /api/v1/price_levels` | C3-2 |
| `product.UOM` constants and `quote/input.go`'s `uomCodes` duplicate the enum in Go | `product/model.go`, `quote/input.go` | C3-2 (both read the catalogue) |
| Purchase order line costs are scale 2 with no unit; AP invoice line prices are scale 2 | migrations 011, 028 | C4-1 (with 5.5's resolver) |
| The portal cart's unit price is scale 2 | migration 030 | C5-1 (portal); the cart is a display snapshot, and orders price through the engine from C2-2 |
| The counter and portal price at `base_price` per `uom_primary` by raw SQL | `pos/repository.go`, `portal/repository.go` | C3-2 (B), recipe step 9 |
| The product's geometry is float64 inches for the load planner, separate from its nominal board measure | `product/model.go` (`LengthIn`, `WidthIn`, `HeightIn`) | not a contradiction: actual dimensions stay the load planner's; this record adds nominal ones |

## Alternatives considered

**A conversion factor or a pair, in the unit set.** The brief's framing is a
factor per unit to the stocking unit. A factor at scale 4 cannot hold a
2x4x14's 3/28 piece per board foot, a sheet's 1/32 sheet per square foot, or
a 2x4's 2/3 board foot per linear foot; a wider scale (8, 12) holds more
cases and still not thirds, and the round trip test then fails exactly on
the commonest lumber. Adopted: each row's "factor" is a pair of scale 4
decimal strings, ADR 0001's own answer for a line, so the unit set and the
line speak one arithmetic and every resolution is exact. Most rows read as a
factor anyway, because the canonical form puts a 1 on one side whenever it
can.

**The stocking quantity: refuse, round, or carry a ratio.** Rounding a stock
quantity to scale 4 leaves residues that accumulate in inventory and never
reconcile with the lines. Keeping the stocking quantity as an exact ratio
would put rationals into inventory, which cycle 4 rebuilds. Adopted: refuse
a line that does not convert exactly (R5). The refusal is rare when the
stocking unit is the finest unit the product is handled in, which the
derivations of 3.2 lead to, and it is visible at quote time.

**Allocation columns in the sale unit or the stocking unit.** In the sale
unit, a partial allocation from stock counted in another unit is often
inexact (700 LF of a line sold by the MBF). Adopted: the stocking unit,
where the inventory rows already are; billing converts back exactly or
bills in the stocking unit, and the money is exact either way.

**The tally's quantity: board feet, pieces, or linear feet.** Board feet as
the quantity would be rounded (200/3). Pieces as the quantity with a pair
carrying the average length would make the pair grow with the tally (12000
x pieces against the board foot sum) and burst the bound on a large order.
Adopted: linear feet. It is an exact sum, the pair to MBF depends only on
the cross section, and the board foot price comes out of the existing
`Extend` with no new rounding.

**Mixed cross sections on one tally (random width hardwood).** Rows with
their own thickness and width break the single pair, and the hardwood
grading rules round surface measure per board, a dealer rule this record
cannot pick. Rejected for v1: a tally has the product's cross section; a
random width hardwood line is a later decision with its own rounding rule,
and it changes values, not shapes (the rows gain a width).

**Where the tally lives.** A JSONB column per line table is simplest and
holds no constraint. Five child tables duplicate one shape five times. A
tally header referenced from each line cannot cascade from its owner.
Adopted: one rows table with one foreign key column per line table and a
CHECK that exactly one is set, so every row cascades with its line and one
repository serves every document.

**The unit catalogue's key.** A UUID key with a unique code costs a join on
every read of every line, and the lines already store the code. Adopted: the
code is the key; codes are immutable and units are never deleted, so the
key never moves.

**The code vocabulary.** Widening to digits (`M2`) or longer codes was
considered; the quote contract's `^[A-Z]{1,6}$` already holds every unit in
the seed and every dealer unit anticipated, and keeping it avoids a contract
change on quotes. A dealer who needs more is a later listed change.

**Base price on the product or in the price table.** Moving the base into
`product_prices` gives one table for every price but breaks every raw reader
of `base_price` at once, including the frozen AI load management seam.
Adopted: the base stays on the product with a unit beside it; levels and
branches are the price table's.

**Tier, level and profile.** Keeping the tier enum beside dealer levels
keeps two answers to "what does this customer pay". Folding everything into
per customer settings loses the shared program a dealer runs for many
builders. Adopted: one dealer created level concept, the tiers migrated into
it, and profiles as the shareable program that names a level and carries
category rules; the migration preserves every customer's price, proved by
test.

**The engine's resolution order.** The brief names contract, customer level,
branch, base. Today's rules (job override, promotional, quantity break) and
category rules sit between contract and level; dropping them would remove
working dealer levers, and placing them after the level would let a level
mask a promotion. Adopted: today's order with the new rungs where the brief
puts them.

**Rounding a derived price.** Carrying a derived price as an exact rational
into the line would make the line's `unit_price` unrepresentable at scale 4.
Folding a level's percent into the line as a discount would confuse customer
pricing with ADR 0005's reasoned discounts and their audit rows. Adopted: one
scale 4 rounding of a derived price, stated (R4.2); fixed prices are never
rounded, and no price ever changes unit.

**Cost basis for a cost derived level.** Average cost is what COGS uses
(ADR 0005 section 8.4); many lumber dealers price from replacement cost.
Adopted: both, as a level's basis, the replacement cost read from the
vendor cost side this record adds, falling back to average.

**Stock by length now or in cycle 4.** Settled in 4.4: deferred, because
stock identity is ADR 0008's and nothing in cycle 3 needs it.

## Consequences

- Every conversion in the product is one exact arithmetic, the pair, from
  the catalogue through the unit set to the line, and a client or agent can
  reproduce every extension from the line alone.
- A dealer adds a unit, gives a product the units it is sold, bought and
  priced in, and prices lumber per MBF while selling by the piece or by
  tally, with no rounding anywhere but the one extension.
- Some lines that today's code would accept with a silently rounded stock
  quantity are refused (R5); the message names the stocking unit, and the
  quote is refused before it ever reaches an order.
- The customer wire loses `tier` and `price_level_id` for `price_profile_id`,
  and the price level read moves under pricing: listed contract changes, the
  desk and portal updated in C3-2.
- Stock by length waits for ADR 0008; until then a random length product's
  stock is linear feet, and its lengths live on the lines.
- C3-2 part B cannot start until cycle 2 has landed its line tables; part A
  can, which keeps cycle 3 moving while cycle 2 finishes.
