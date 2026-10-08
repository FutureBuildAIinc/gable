# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0008: inventory identity and vendor intake

## Status

Proposed for the Gable v1 refactor (item C4-0, the design stop before cycle
4's build items). It becomes accepted when the lead merges it after review.
It stands on ADR 0001 (the wire contract), ADR 0002 (machine keys), ADR
0003 (the outbox), ADR 0005 (the sales and money core), ADR 0006 (units and
pricing) and ADR 0007 (drafts, links and confirm gated scopes), all
accepted, and the module recipe (`docs/refactor/MODULE-RECIPE.md`). Items
C4-1 and C4-2 build from this record and the recipe. Where an item has to
differ from this record, the record is changed first, in its own pull
request. This pull request also amends ADR 0005 section 8.4 and its Status
note (sections 3.4 and 3.5 here are the reason): the average's share lock,
the stocked special order line at `costOf`, and the relief of a non stock
or direct ship line from its linked receipt lines' posted values. C2-2b
(PR 43, in flight) builds ADR 0005 8.4 ahead of this record's items, and
its relief of those lines must follow the amended bullet, the posted
values rule included: its `SpecialOrderUnitCost` today recomputes the
first linked received purchase line by `(created_at, id)`, which is
undefined as a cost when several purchase lines fill one order line and
leaves cent residues in `1030`. The lead carries this line to PR 43.

ADR 0005 is a hard dependency. This record extends its lock order (section
11), its `purchase_order.received` event, its allocation request queue, and
its cost function `costOf`.

**The run order against cycle 2's chain and ADR 0006's items.** Cycle 2
owns customer, order, invoice, payment, deposit, account, GL postings, POS
and till, and its items land in a chain: C2-1 and C2-2a are merged; C2-2b
(allocation and fulfilment) is being built now; C2-3, C2-4 and C2-5 follow.
C2-2b adds the `Qty` functions to `internal/inventory` (in
`inventory/qty.go` on its branch), the `purchase_order.received` event and
the allocation request queue; C2-4 is
the last cycle 2 item that edits `internal/gl`; none of them is on
`refactor/v1` yet. This record's items say "C2-2 adds" in the present tense
of the plan, not of today's code. No cycle 4 item edits a file a running
cycle 2 item or a running ADR 0006 item owns:

| Item | Starts after | Must not edit |
|---|---|---|
| C4-1a (inventory, purchase orders, vendors, EDI partners; matching routes as they are) | C2-2b, C3-1 and C3-1b have merged (C3-1's migration 093 widens `inventory.allocated`, and C3-1b is C3-1's inventory read, held behind C2-2b); the purchase line shape of section 1 after C3-2A-units, else held to the stocking unit as below | the cycle 2 modules while their item is open (`internal/invoice`, `credit_memo`, `payment`, `deposit`, `account`, `gl`, `pos`, and `internal/order`, `salesdoc`, `internal/inventory` until C2-2b has merged), `internal/pricing`, and the unit set service while C3-2A-units is open (the stock conversion paragraph below settles the read this record adds to it) |
| C4-1b (the AP fixes of section 7.4) | C2-4, the last cycle 2 item that edits `internal/gl` | as C4-1a (no cycle 2 item edits `internal/gl` after C2-4) |
| C4-2 A | C2-5 and C3-2B have merged (both C3-2B and A change the order, invoice and counter stock calls, and A's signatures carry C3-2B's `stock_quantity` and fulfilment tally) | nothing in cycle 2 is open; `internal/units` and the unit set service while C3-2A-units is open |
| C4-2 B | A | as A |
| C4-2 C | A, and C2-2b's allocation worker, which C changes (`RESERVED` first) | as A, plus `internal/quote` while C5-2 is open |
| C4-2 D | C4-1a | as C4-1a |
| C4-2 E | C4-1b | as C4-1b |
| C4-2 F | C4-1a and C3-2A-pricing (`vendor_product_costs`, `ResolveCost`) | as C4-1a; `internal/pricing` is open only to C3-2A-pricing, which has merged before F starts, and F may extend pricing's service with the feed's system caller write path but adds no table |

D and F depend only on C4-1a (and, for F, C3-2A-pricing) and may run beside
A to C. C4-2 C's deletion of the quote's auto purchase order path
(section 5.1) edits `internal/quote`, which C2-2 has already merged and
C5-2 has not opened.

## Context

Cycle 4 brings six modules onto the wire contract: inventory, purchase
orders, AP, matching, EDI and vendors. It also folds in the parity items the
plan lists for them: bins and coded adjustments (GC-188), bundle and lift
identity (GC-197), serials and lots (GC-199), special order allocation and
direct ship (GC-176), purchase order approval limits (GC-185), vendor credit
memos (GC-186), and vendor feed intake (GC-209).

Today's code has no notion of stock identity beyond a row:

- `inventory` (migration 001) holds one quantity and one `allocated` figure
  per row. It has no unique key, so one product can have two rows at the
  same location. `location_id` is nullable, and the counter sells from the
  row whose location is null.
- No movement is recorded anywhere. An adjustment's reason is accepted and
  then thrown away (`inventory/service.go` `AdjustStock`).
- No path locks a row before reading it. Allocation records which rows hold
  stock but not which order holds it, so `RevertFulfillment` puts stock back
  on whatever row happens to be first.
- Lots, serials, bundles and tallies do not exist anywhere in `core/`.

Purchasing has the same kind of gap:

- A purchase order goes from draft to sent with no approval step, and
  `SubmitPO` sends from any status, `RECEIVED` included.
- A receipt reads the purchase order without a lock. It accepts any
  quantity, including more than was ordered. It posts nothing to the
  general ledger and writes no event. Its average cost recompute reads
  `products.total_quantity`, which is not a column but the
  `SUM(inventory.quantity)` of the product read by `GetProduct`
  (`product/repository.go`): because the receipt's `AdjustStock` runs
  first in the same transaction, the received quantity is counted twice in
  `Q`, and the cost update's error is dropped
  (`purchase_order/service.go`).
- Vendor statistics are averaged as `(old + new) / 2`, which weights the
  latest sample at one half however many came before, and lead time is
  `time.Since(po.CreatedAt)` at both submit and receive.
- AP approval posts an entry with only the payable leg, because the bill's
  lines are never loaded, and it swallows the error when that save fails.
  `PayVendor` also never checks that the invoice it applies to is
  approved.
- Matching pairs an invoice's lines with the purchase order's lines by
  their position in the list.
- Neither vendor credit memos nor returns to the vendor exist.

Vendor intake is thin:

- The only intake path is a catalogue import route. It writes row by row
  with no transaction and has no run record and no replay protection.
- The only outbound document is a demonstration 850. Its control numbers
  are always 1, its item codes are `UNKNOWN`, and it is written to a local
  directory.

This record settles stock identity first, because every other question
depends on it: what a stock row is, how a move, an allocation and a count
name it, and how its value reaches the ledger. It then settles the
purchasing and intake questions on top of that.

## Decision

### 1. Boundaries

| Concern | This record | Owned elsewhere |
|---|---|---|
| Units | Every stock quantity is held in the product's stocking unit, as NUMERIC(12,4). A purchase line carries ADR 0006's line shape: `uom`, `price_uom`, `uom_qty`, `price_uom_qty`, `stock_uom` and `stock_quantity`, with `unit_cost` NUMERIC(12,4) per `price_uom` and `line_total` by `httpx.Extend`. A receipt converts the received purchase quantity into the stocking unit exactly or refuses it (section 4). | ADR 0006 sections 3.3 and 3.4 own the pair resolution and the stock unit rule, which applies at receipt. Purchase lines are not one of the readers ADR 0006 9.1 protects: from C3-2A-units any unit of the product's set with `purchase` set is allowed; before it, the same columns carry the stocking unit and the pair 1 and 1, and the service holds a purchase line to the stocking unit (409 blocker `unit_not_stock_unit`), as ADR 0005 did for sales. During that hold `REPLACEMENT_COST` reads only stocking unit vendor costs (ADR 0006 9.1). The stock conversion paragraph below the table settles what `stock_unit_in_use` defers here. |
| Stock by length (random length lumber) | Settled here as tally rows on a stock row (section 2.4), in ADR 0006's shape: a tallied row's quantity is linear feet, the sum of pieces x `length_ft`, and board feet are display only. A product sold by tally is a product with `products.random_length`. | ADR 0006 section 4 owns the tally table, the wire shape and the board foot function. No fixed cross section is assumed: a random length moulding has neither thickness nor width, and its board feet are null. |
| Cost basis | Moving weighted average per product, kept in `products.average_unit_cost` and maintained correctly by receipts (section 3.5). `costOf` keeps its signature. | ADR 0005 named cycle 4 as the owner of cost layers. This record decides against layers for v1 (Alternatives). |
| Receipts in the ledger | Receipts post to `1030` against a received-not-invoiced accrual (section 3.4). This closes the gap ADR 0005 section 1 left open. | |
| Vendor costs from feeds | A price feed writes ADR 0006's `vendor_product_costs` through pricing's service (section 8.4); this record builds no cost table of its own. | ADR 0006 section 5.5 owns the table, `ResolveCost` and the vendor price levels. Purchase lines default their unit cost through `ResolveCost`. |
| Drafts and confirm gated scopes | Purchase orders keep their own `draft` status. Adjustments and transfers post on create. Counts have their own lifecycle. | ADR 0007 owns drafts as a resource and the `:propose` and `:commit` verbs. Neither `vendor-feeds` nor `purchase-orders` has a draft kind, so ADR 0007 5.3 refuses `propose` and `commit` on them at mint and their keys keep `read` and `write` exactly as today. Purchase orders become a draft kind only after cycle 4 (ADR 0007 section 10). Approval limits apply to people whatever scope a key holds: no key ever approves, and no agent marked session either (section 6). |
| Inter-branch transfers | Refused, as today. A move stays inside one branch. | A later item: a transfer order with in-transit stock. |
| Vendor ASN, acknowledgement and invoice intake (856, 855, 810) | Not built. Feed runs are shaped so that each becomes another feed kind. | A later item. |
| AP payment runs, 1099 | Not built. | GC-179, following the refactor. |
| Vendor stock availability reads | Stored by the stock feed only. | GC-212, following the refactor. |

**The stock conversion act.** ADR 0006 3.2's `stock_unit_in_use` defers "a
stock conversion, a cycle 4 adjustment act" to this record. This record
defines none in v1, and says so as a known limit: `stock_unit_in_use`
refuses a change of the stocking unit while any stock row of the product
holds a nonzero quantity or allocation, or any open order line or open
purchase line names the product. This record adds open purchase lines to
ADR 0006's check as one read in the unit set service, which ADR 0006 3.2
gives to C3-2A-units: the read lands with whichever of C4-1a and
C3-2A-units merges second, and neither edits the unit set service while
the other has it open (the run table forbids C4-1a; ADR 0006 9.1's table
forbids nothing of C3-2A-units, so if C3-2A-units merges second its brief
carries the read, and until the read lands the check sees open order
lines only, never a false pass on a purchase line).
A dealer who must re-unit a stocked product waits for the conversion act.
The workaround a dealer has today stays open and exact: adjust the stock
out to zero (an OUT reason of the dealer's choosing), let the zero stock
and closed lines satisfy `stock_unit_in_use`, change the stocking unit,
then adjust the stock back in with a unit cost (`FOUND` with a cost, or
`OPENING`): the moves and the average stay exact through that path, the
out at the old average and the in at the given cost. A full act would
convert every row, tally, allocation and open line and
recompute the average, each exactly or refused, under the locks of section
9; that is a later item on this record's successor, because v1 has no
dealer requirement that names it.

### 2. Stock identity

#### 2.1 The dimensions

A unit of stock is identified by these dimensions:

| Dimension | Where it lives | When it is required |
|---|---|---|
| Product | `inventory.product_id` | always |
| Location, down to bin | `inventory.location_id`, any stockable node of the location tree | always (null is gone after C4-2) |
| Branch | `inventory.branch_id`, derived from the location by trigger | always |
| Lot | `inventory.lot_id`, referencing `stock_lots` of kind `LOT` | when `products.tracking = 'LOT'` |
| Serial | `inventory.lot_id`, referencing `stock_lots` of kind `SERIAL`; `inventory.is_serial` true | when `products.tracking = 'SERIAL'` |
| Bundle or lift | `inventory.bundle_id`, referencing `stock_bundles` | optional, for products with `products.bundled` true |
| Lengths | `inventory_tally` rows of the stock row | when the product is sold by tally (ADR 0006) |

A stock row is one combination of product, location, lot and bundle. The
identity is unique: `UNIQUE NULLS NOT DISTINCT (product_id, location_id,
lot_id, bundle_id)` (Postgres 15 or later; the stack runs 16). A bundle has
exactly one stock row: `UNIQUE (bundle_id) WHERE bundle_id IS NOT NULL`. So a
bundle always sits in one place, and moving it re-points that one row.

**Why serials share the lot table.** A serial is a lot of one. Holding both
in `stock_lots`, with a `kind` column, gives one foreign key on the stock row
and one traceability read. The rules that make a serial a serial are three
constraints on `inventory`:

- `CHECK (NOT is_serial OR quantity <= 1)`;
- `UNIQUE (lot_id) WHERE is_serial AND quantity > 0`, so a serial is on hand
  in at most one place;
- `is_serial` is set when the row is inserted, from the product's tracking.

#### 2.2 Locations

`locations` keeps its tree (migration 002) and its branch denormalization
(057, 058). C4-2 adds the following.

- **A closed type vocabulary.** `type` gets a CHECK on `BRANCH`, `ZONE`,
  `AISLE`, `RACK`, `SHELF`, `BIN`, `YARD` and `STAGING`. The migration first
  uppercases existing values and maps any unknown value to `BIN`, reporting
  the count.
- **`stockable BOOLEAN NOT NULL`.** It is false on `BRANCH` and true
  elsewhere. Stock may sit on any stockable node, not only on leaf bins,
  because a yard without bins still holds stock in a zone. A trigger on
  `inventory` refuses a row whose location is not stockable.
- **Three system locations per branch, all of type `STAGING`, created by
  the migration and by branch creation:**
  - `RECEIVING`: the default receiving location;
  - `RETURNS`: where customer returns land;
  - `UNASSIGNED`: where the migration places legacy rows that had no
    location.
  The branch row names them in `receiving_location_id`,
  `returns_location_id` and `unassigned_location_id`. A system location can
  be renamed but cannot be deactivated.
- **`path` is computed by the server** from the codes of the ancestors,
  joined by `/`. Clients no longer supply it.

A bin is identified by its id on the wire. Its `path` (for example
`MAIN/YARD-A/R4/B12`) is a read-only display field. The location routes
stay in the location module, converted by C3-1.

#### 2.3 Lots and serials

`products.tracking TEXT NOT NULL DEFAULT 'NONE' CHECK (NONE, LOT, SERIAL)`
controls tracking:

- A product's tracking can change only while it has no stock and no open
  allocation. Otherwise the change is a 409 blocker `product_has_stock`.
- A `SERIAL` product's stocking unit must be a count unit. Otherwise the
  change is a 409 blocker `serial_needs_count_unit`. ADR 0006 owns the unit
  dimension the check reads.

`stock_lots` has these columns:

- `id`;
- `product_id`;
- `kind CHECK (LOT, SERIAL)`;
- `code TEXT NOT NULL`, 1 to 64 characters, `UNIQUE (product_id, kind,
  code)`;
- `vendor_id NULL`;
- `received_on DATE NULL`;
- `expires_on DATE NULL`;
- `receipt_line_id NULL`, the first receipt that created it;
- `unit_cost NUMERIC(12,4) NULL`, the receipt's cost. It is kept for later
  specific identification and is not the cost basis;
- `created_at`.

Rows of `stock_lots` are never deleted. A lot or serial is created by the
first act that brings it into stock: a receipt, an adjustment in, or a count
that finds it. On the wire a lot is named by its code (`lot_code`, `serial`).
The server resolves or creates the row. An act that would create a serial
already on hand somewhere is a 409 blocker `serial_on_hand`.

#### 2.4 Bundles, lifts and tallies

`products.bundled BOOLEAN NOT NULL DEFAULT FALSE` marks a product whose stock
may be held in bundles. It may also be held loose.

`stock_bundles` has these columns:

- `id`;
- `product_id`;
- `kind CHECK (BUNDLE, LIFT)`, a display distinction only, with the same
  behaviour;
- `tag TEXT NOT NULL UNIQUE`, the dealer's tag;
- `vendor_tag TEXT NULL`;
- `lot_id NULL`;
- `status CHECK (INTACT, BROKEN, CONSUMED)`;
- `receipt_line_id NULL`;
- `created_at`.

The bundle's status follows from what happens to its stock:

- A bundle is `INTACT` until a first move takes part of it. It is then
  `BROKEN`.
- It is `CONSUMED` when its row reaches zero.
- An unbundle act (a transfer with `unbundle: true`) moves a bundle's
  quantity onto the loose row of the same product, location and lot, and
  marks the bundle `CONSUMED`.
- Tags are never reused.

`inventory_tally (inventory_id, length_ft NUMERIC(12,4) CHECK (length_ft >
0), pieces INTEGER NOT NULL CHECK (pieces > 0 AND pieces <= 1000000),
PRIMARY KEY (inventory_id, length_ft))` holds stock by length for a product
with `products.random_length`, in ADR 0006 section 4's shape: a tallied
row's quantity is linear feet, the sum of pieces x `length_ft`, exact at
scale 4. No thickness or width enters it, and board feet are display only
(ADR 0006 R4.3): a random length moulding tallies as freely as a random
length board. Its rules:

- The invariant is that a random length row's `quantity` equals its tally
  rows' linear feet plus `untallied_lf`, the row's untallied remainder
  (2.5): linear feet the row holds with no lengths named. After the
  migration every such row is all remainder; a tallied receipt adds
  lengths beside the remainder instead of being refused (the unique
  identity key allows only one row per identity, so tallied and untallied
  stock cannot be two rows of one identity, and the remainder column lets
  one row carry both); an untallied sale draws the remainder down to zero
  before it touches the lengths. The inventory service is the only writer
  of the quantity, the tally and the remainder, and it recomputes
  `quantity` from them on every write. It never accepts a quantity that
  disagrees with the tally plus the remainder: that is a 400 naming the
  tally.
- An act line that moves a tallied row (a receipt, adjustment, transfer,
  vendor return or count line) carries the pieces by length it moves as
  rows of ADR 0006's `line_tally_rows`: cycle 4's migration extends that
  table with one foreign key column per act line table
  (`purchase_receipt_line_id`, `stock_adjustment_line_id`,
  `stock_transfer_line_id`, `vendor_return_line_id`,
  `stock_count_line_id`) and widens its `num_nonnulls` CHECK. ADR 0006
  chose that table over JSONB for its constraints, and this record does
  not fork it. The stock row's tally decrements or increments length by
  length from those rows.
- `stock_moves.tally` stays JSONB as a snapshot only: nothing reads it for
  arithmetic, and the rows in `line_tally_rows` are the record.
- A sale or move that names a length the row does not hold enough of is a
  409 blocker `tally_unavailable` naming the length.

Three cases ADR 0006 leaves to stock:

- **A sale without a tally, and the cut rule.** ADR 0006 allows a random
  length sale line with no tally, and a partial fulfilment with none. Such
  a sale draws linear feet, not pieces: within each row it draws on, the
  untallied remainder serves first, then whole pieces longest first, and
  when the length still to draw is shorter than the next whole piece,
  that piece is cut. The move takes the drawn linear feet, its snapshot
  (`stock_moves.tally`) shows the pieces taken whole plus the cut piece
  at the length actually sold, and the row keeps the remnant, the piece's
  length less the length sold, as a tally row of one piece (merged into
  an existing row of that length when one exists). The invariant always
  holds and there is no `tally_required` refusal on stock: a yard cuts to
  length. Worked: a row of 3 at 16 and 4 at 14 (104 LF) with no
  remainder, and an untallied sale of 100 LF, take 16, 16, 16, 14, 14,
  14 (90 LF) and cut 10 from the next 14; 100 LF leave and a remnant of
  1 at 4 stays. A client that cares which lengths leave names them.
- **Allocation versus lengths.** An allocation holds linear feet, not
  lengths, so a fulfilment can meet `tally_unavailable` after allocation
  succeeded. Lengths are taken at fulfilment: a fulfilment whose tally
  (ADR 0006 4.4) cannot be taken from the allocated rows is a 409
  `tally_unavailable`, the allocation stands, and the desk reassigns it
  (2.7) to a row that holds the lengths.
- **Legacy rows.** A random length row with no tally rows is an untallied
  linear feet row, which is what every existing row becomes at migration:
  `untallied_lf` is backfilled to the row's quantity. A tallied act line
  into a row that holds a nonzero remainder lands beside it, the lengths
  becoming tally rows and the remainder staying as it is; a dealer who
  wants a clean tallied row receives into a bundle (its own identity,
  above) or another bin, or counts the row's lengths first. The pick
  policy of 2.7 serves the remainder before the lengths, within one row
  and across the branch's rows, so the length detail on tallied rows
  survives until the amorphous pool is empty.

#### 2.5 The stock row

After C4-2, `inventory` has these columns:

| Column | Rule |
|---|---|
| `id` UUID | |
| `product_id` UUID NOT NULL | |
| `location_id` UUID NOT NULL, FK `locations` | stockable (trigger) |
| `branch_id` UUID NOT NULL | set by trigger from the location, like 058 |
| `lot_id` UUID NULL, FK `stock_lots` | required when the product tracks lots or serials; the trigger checks it against the product's tracking |
| `bundle_id` UUID NULL, FK `stock_bundles` | |
| `is_serial` BOOLEAN NOT NULL | 2.1 |
| `quantity` NUMERIC(12,4) NOT NULL CHECK `>= 0` | stocking unit |
| `untallied_lf` NUMERIC(12,4) NOT NULL DEFAULT 0 CHECK `>= 0` | read only on a random length product's rows (0 and ignored elsewhere): the row's untallied remainder, linear feet with no lengths named (2.4); part of the quantity, never beside it |
| `allocated` NUMERIC(12,4) NOT NULL CHECK `>= 0 AND <= quantity` | already widened to (12,4) by C3-1's migration; the CHECK is added by C4-2 A `NOT VALID` and validated only when the migration finds no violating row (it reports them otherwise) |
| `location` TEXT NULL | legacy text, no longer read or written; dropped by a later cycle |
| `created_at`, `updated_at` | |

On the wire a stock row is `StockRow`:

- `id`, `product` (the product summary C3-1 embeds), `branch_id`,
  `location_id`, `location_path`;
- `lot_code`, `serial`, `expires_on`;
- `bundle_tag`, `bundle_status`;
- `quantity`, `allocated`, `available` (decimal strings), and
  `untallied_lf` (decimal string; read only on a random length product's
  rows, 2.4);
- `tally` (null, or ADR 0006 4.3's object: `rows` of `{pieces, length_ft}`,
  with the read only `linear_feet`, `thickness_in`, `width_in` and
  `board_feet`);
- `updated_at`.

`GET /api/v1/inventory` lists stock rows with these filters: `product_id`,
`branch_id`, `location_id` (with `include=descendants` for a subtree),
`lot_code`, `serial`, `bundle_tag`, and `in_stock` (default true). The
ordering scope is `inventory (product_id, id)`.

Rows at zero are kept, so that moves can name them, and are hidden from the
default list.

#### 2.6 The move ledger

Every change to `inventory.quantity` writes one `stock_moves` row in the
same statement batch. No exception is allowed.

| Column | Rule |
|---|---|
| `id` UUID | |
| `act_id` UUID NOT NULL | groups the moves of one act (a transfer's out and in share it) |
| `act_kind` TEXT NOT NULL CHECK | `OPENING`, `RECEIPT`, `SALE`, `SALE_RETURN`, `ADJUSTMENT`, `TRANSFER`, `VENDOR_RETURN` (a count posts through `ADJUSTMENT`) |
| `document_type`, `document_id` | the line that caused it: `purchase_receipt_line`, `invoice_line`, `credit_memo_line`, `pos_line_item`, `pos_return_line`, `stock_adjustment_line`, `stock_transfer_line`, `vendor_return_line` |
| `inventory_id` UUID NOT NULL, and snapshots `product_id`, `branch_id`, `location_id`, `lot_id`, `bundle_id` | the identity as it was at the move |
| `quantity` NUMERIC(12,4) NOT NULL CHECK `<> 0` | signed, stocking unit |
| `tally` JSONB NULL | pieces by length moved, signed like the quantity |
| `unit_cost` NUMERIC(12,4) NULL, `value` NUMERIC(12,2) NULL | the cost the move was valued at (3.5) |
| `reason_code` TEXT NULL | adjustments only |
| `gl_entry_id` UUID NULL | the entry that valued it, when one did |
| `actor_id`, `actor_kind` | the R1-14 attribution |
| `created_at` TIMESTAMPTZ NOT NULL DEFAULT `clock_timestamp()` | |

The invariant is that every stock row's `quantity` equals the sum of its
moves' quantities. The C4-2 migration writes one `OPENING` move per existing
row with a nonzero quantity, so the invariant holds from the start. It is
enforced in two ways:

- **The single writer gate.** As in ADR 0005 section 9.3, a test fails if
  any package other than `internal/inventory` writes `inventory`,
  `stock_moves`, `stock_allocations`, `inventory_tally`, `stock_lots` or
  `stock_bundles`. The seed package is allowlisted, and C4-2 makes the seed
  write its stock through the inventory service.
- **A reconciliation read.** `GET /api/v1/inventory/reconciliation` (roles
  `admin`, `owner`) lists every row where the quantity disagrees with the
  sum of its moves, `allocated` disagrees with the sum of its
  allocations, or, on a random length product, the quantity disagrees
  with its tally rows' linear feet plus its remainder, so drift shows
  instead of hiding.

`GET /api/v1/inventory/moves` lists the ledger. Its filters are
`product_id`, `branch_id`, `location_id`, `lot_code`, `serial`,
`bundle_tag`, `act_kind`, `document_type` and `document_id`, and its
ordering scope is `stock_moves (created_at, id)`.

Traceability (which invoices took lot L, where serial S went) is this list
filtered by lot or serial.

#### 2.7 Allocations carry identity

`stock_allocations` records which stock row holds which order line's
allocation:

| Column | Rule |
|---|---|
| `id` UUID | |
| `order_line_id` UUID NOT NULL, FK `order_lines` ON DELETE RESTRICT | |
| `inventory_id` UUID NOT NULL, FK `inventory` | |
| `state` TEXT NOT NULL CHECK (`ALLOCATED`, `RESERVED`) | `RESERVED`: held for a special order line by its receipt, not yet in the line's `quantity_allocated` (section 5.2) |
| `receipt_line_id` UUID NULL | set on `RESERVED` |
| `quantity` NUMERIC(12,4) NOT NULL CHECK `> 0` | |
| `created_at` | |
| `UNIQUE (order_line_id, inventory_id, state)` | |

Two invariants hold, and both are tested and covered by the reconciliation
read:

- for every stock row, `allocated` equals the sum of its allocation rows
  (both states);
- for every order line, `quantity_allocated` (ADR 0005 section 5.4) equals
  the sum of its `ALLOCATED` rows.

ADR 0005's `AllocateQty`, `ReleaseQty`, `FulfillQty` and `RestockQty` change
in two ways:

- **Signature.** Each takes the order line (or counter line) id it acts for.
  C4-2 changes the callers in the order, invoice and POS modules, after C2-2
  and C2-5 have merged.
- **Behaviour.** Each acts through allocation rows:
  - allocate inserts or grows allocation rows;
  - release removes them, putting each quantity back on the row that held
    it;
  - fulfil consumes from exactly the rows that hold the line's allocation,
    in `(product_id, inventory id)` order;
  - restock returns to the identity the sale took from (its lot or serial)
    at the branch's `RETURNS` location.

**The allocation pick policy.** When an act allocates without naming
identity, it chooses rows in this order:

1. this line's `RESERVED` rows (5.2);
2. then rows in the order's branch with available quantity, in this order:
   - on a random length product, rows with no tally rows before rows with
     them, and within one row the `untallied_lf` remainder before its
     lengths (2.4's legacy case: the amorphous linear feet pool serves
     first, so the lengths on tallied rows survive);
   - lot `expires_on` ascending, nulls last (first expiring, first out);
   - then lot `received_on` ascending (first in, first out);
   - then loose stock before broken bundles, and broken bundles before
     intact ones (this keeps intact bundles whole for bundle sales);
   - then location `path`, then id.

To keep ADR 0005's lock order, the act locks every candidate row of the
product in the branch in `(product_id, inventory id)` order, then picks in
memory by the policy. A serial line allocates one row per serial.

**Reassigning.** `POST /api/v1/inventory/allocations/{id}/reassignments`,
with body `{"quantity", "to": {"location_id", "lot_code", "serial",
"bundle_tag"}}`, moves an allocation to a named identity in the same branch.
This is how the desk picks a particular bundle or serial. It changes no order
line total, so it takes no order lock. It locks the two stock rows in
`(product_id, inventory id)` order.

`GET /api/v1/inventory/allocations` lists allocations, filtered by
`order_line_id`, `order_id`, `inventory_id` and `state`.

**The counter.** A counter sale (ADR 0005, C2-5) is not allocated. It
fulfils from the branch's rows by the same pick policy. C4-2 adds optional
`lot_code`, `serial` and `bundle_tag` to counter lines, each a CONTRACT-CHANGES
row, and two refusals:

- a `SERIAL` product's counter line must name its serial, or it is a 400
  naming `lines[i].serial`, because the clerk hands over one particular
  unit;
- a lot product with more than one lot on hand at the branch takes its lot
  by the policy unless the line names one.

The serial rule stays in package A and is not deferred: tracking defaults
to `NONE`, so the refusal fires only for a product a dealer has turned
serial tracking on, and until then no counter line, no client and no
golden changes. The field is optional and additive. Without the rule, the
serial invariant of 2.1 (a serial is on hand in at most one place) breaks
at the first counter sale of a serial product.

**What the wire shows.** Invoice lines and credit memo lines expose a
read-only `stock` array, `[{location_path, lot_code, serial, bundle_tag,
quantity}]`, read from the line's moves.

#### 2.8 Transfers (moves between bins)

A transfer is posted with `POST /api/v1/inventory/transfers`:

- body `{"note", "lines": [{"product_id", "from": {"location_id",
  "lot_code", "serial", "bundle_tag"}, "to_location_id", "quantity",
  "tally", "with_allocations", "unbundle"}]}`;
- it answers 201 with the transfer (`number` `TR-`, gapped sequence) and
  writes `inventory.moved`.

It is stored in `stock_transfers` (`id`, `number`, `branch_id`, `note`,
`created_by`, `created_at`) and `stock_transfer_lines`.

The rules:

- The source and the destination must be in the same branch. Otherwise it is
  a 409 blocker `cross_branch`.
- A bundle named by tag moves whole when `quantity` is absent, and its row is
  re-pointed. When `quantity` is given, the moved part becomes loose at the
  destination and the bundle is `BROKEN`.
- Without `with_allocations`, only available quantity moves. Asking for more
  is a 409 blocker `exceeds_available`. This keeps today's rule.
- With `with_allocations`, allocated quantity moves too, and its allocation
  rows move onto the destination row for the same order lines. This is how a
  picked order is staged in a staging bay.
- A transfer posts no journal entry: both sides are `1030` at the same cost.

The old `POST /api/v1/inventory/transfer` and `POST /api/v1/inventory/adjust`
routes are removed. They are replaced by transfers, adjustments (3.3) and
counts (2.9), each with a CONTRACT-CHANGES row and the desk updated in the
same pull request.

#### 2.9 Counts

`stock_counts` holds a count:

- `id`, `number` (`CNT-`), `revision`, `branch_id`;
- `location_id`, the root of the counted subtree;
- `product_ids UUID[] NULL`, an optional narrowing;
- `blind BOOLEAN`;
- `status CHECK (DRAFT, IN_PROGRESS, POSTED, CANCELLED)`;
- `adjustment_id NULL`;
- `created_by`, `posted_by`, `created_at`, `started_at`, `posted_at`.

`stock_count_lines` holds its lines:

- `id`, `count_id`;
- `inventory_id NULL` (null for stock found that was not in the snapshot);
- the identity: `product_id`, `location_id`, `lot_code`, `serial`,
  `bundle_tag`;
- `expected_quantity`, `expected_tally` (the snapshot);
- `expected_at_count_quantity NULL`, `expected_at_count_tally NULL` (the
  row as it was when the line was counted, below);
- `counted_quantity NULL`, `counted_tally`, `counted_by`, `counted_at`.

| From | To | Act | Effect | Event |
|---|---|---|---|---|
| none | `draft` | `POST /api/v1/inventory/counts` | | `stock_count.created` |
| `draft` | `in_progress` | transition | snapshot: one line per stock row in scope with its quantity and tally as expected | `stock_count.started` |
| `in_progress` | `in_progress` | `PUT /api/v1/inventory/counts/{id}/lines` (revision) | record counted quantities; add lines for found stock | `stock_count.updated` |
| `in_progress` | `posted` | transition | one adjustment, reason `CYCLE_COUNT`, with one line per counted line whose `counted - expected_at_count` is nonzero, applied to the current quantity; uncounted lines change nothing | `stock_count.posted`, then `inventory.adjusted` |
| `draft`, `in_progress` | `cancelled` | transition | none | `stock_count.cancelled` |

Stock keeps moving during a count. The posted variance is `counted -
expected_at_count`: when `PUT /api/v1/inventory/counts/{id}/lines` records
a line's counted quantity, the same transaction also stores
`expected_at_count`, the stock row's quantity and tally read under that
row's lock (section 9 step 6), so the expectation moves with the shelf
between the snapshot and the count. A sale before the line is counted is
therefore in both `expected_at_count` and the current quantity exactly
once, and the post leaves the shelf quantity: the snapshot said 10, 2 were
sold, the counter finds 8, the variance is 8 - 8 = 0 and 8 stay. A sale
after the line is counted is caught by the variance itself, as any later
move is. A found line, whose `inventory_id` was null at the snapshot, reads
its `expected_at_count` the same way: from the stock row its identity now
names, if one exists by the time it is counted (a receipt during the count
may have created it), under that row's lock; a found line whose identity
still names no row counts from 0. The snapshot's `expected_quantity` stays
for display and blind
counts, and no post reads it. A blind count hides `expected_quantity` on
the wire from roles other than `admin` and `owner`.

Two refusals at posting, each a 409 naming the line:

- a variance that would drive the current quantity below zero (more sold
  since the line was counted than was counted) is blocker `count_below_zero`;
- a variance that would drive it below its allocation is blocker
  `count_below_allocated`.

The counter recounts, or the desk reassigns the allocation, and posts again.

### 3. Coded adjustments, cost and the ledger

#### 3.1 Reasons

`adjustment_reasons` has these columns:

- `code TEXT PRIMARY KEY` with CHECK `^[A-Z][A-Z0-9_]{1,31}$`;
- `name`;
- `direction CHECK (IN, OUT, BOTH)`;
- `gl_account_code TEXT NOT NULL REFERENCES gl_accounts(code)`;
- `requires_note BOOLEAN`;
- `system BOOLEAN`;
- `active BOOLEAN`;
- `revision`, `created_at`, `updated_at`.

Codes are stable:

- A code is never renamed, never deleted and never reused.
- A reason is retired by setting it inactive.
- Its account may change, and the change applies to later adjustments only,
  because each adjustment line snapshots its account code.
- `system` reasons keep their code and direction fixed, and cannot be
  deactivated.

Dealers add their own reasons through these routes (roles `admin`, `owner`):

- `GET /api/v1/inventory/adjustment-reasons`;
- `POST /api/v1/inventory/adjustment-reasons`;
- `PUT /api/v1/inventory/adjustment-reasons/{code}`.

The seed:

| Code | Direction | Account | System | Use |
|---|---|---|---|---|
| `DAMAGE` | OUT | `5070` | no | damaged in the yard |
| `SCRAP` | OUT | `5070` | no | written off as unsellable |
| `SHRINK` | OUT | `5060` | no | unexplained loss |
| `FOUND` | IN | `5060` | no | stock found |
| `INTERNAL_USE` | OUT | `5020` | no | used by the dealer |
| `CYCLE_COUNT` | BOTH | `5060` | yes | posted by counts only; refused on a manual adjustment |
| `OPENING` | IN | `3010` | yes | opening balances at go-live (FB Move loads); requires a unit cost |

#### 3.2 Accounts

These accounts are added to ADR 0005 section 8.1's table. Each is seeded by
the migration of the C4-2 package that first posts to it.

| Code | Name | Type, normal balance | Use |
|---|---|---|---|
| `1050` | Vendor Returns Receivable | ASSET, DEBIT | goods shipped back to a vendor and not yet credited |
| `2040` | Received Not Invoiced | LIABILITY, CREDIT | the accrual a receipt posts and an approved vendor invoice relieves |
| `2045` | Freight Accrued | LIABILITY, CREDIT | freight applied to a purchase order: the freight part of a receipt that had freight applied before it, and the whole of a freight charge applied after the receipt (3.5); the carrier's approved invoice line names the `po_freight_charges` row and relieves exactly this accrual |
| `5050` | Purchase Price Variance | EXPENSE (COGS), DEBIT | invoice cost against receipt cost; vendor allowances; the sold share of post receipt freight |
| `5060` | Inventory Shrinkage | EXPENSE (COGS), DEBIT | shrink, found stock, count variances |
| `5070` | Inventory Damage and Scrap | EXPENSE (COGS), DEBIT | damage and scrap |

`5020` (Operating Expenses) and `3010` (Owner Equity) already exist.

`gl_journal_entries.source` gains `RECEIPT`, `FREIGHT` and `VENDOR_RETURN`.
`VENDOR_CREDIT` is added for vendor credit memos. `ADJUSTMENT`,
`VENDOR_INVOICE` and `REVERSAL` exist.

#### 3.3 The adjustment

The act is `POST /api/v1/inventory/adjustments`, with this body:

```
{"reason_code": "DAMAGE", "note": "...",
 "lines": [{"product_id": "...", "location_id": "...", "lot_code": null,
            "serial": null, "bundle_tag": null, "quantity": "-3",
            "tally": null, "unit_cost_ten_thousandths": null}]}
```

It answers 201 with the adjustment and `Location`. The route is
idempotent through the existing middleware.

It is stored in `stock_adjustments`:

- `id`, `number` (`ADJ-`, gapped sequence), `branch_id`, `reason_code`,
  `note`;
- `source CHECK (MANUAL, COUNT)`, `count_id NULL`;
- `gl_entry_id NULL`, `created_by`, `created_at`.

Its lines are stored in `stock_adjustment_lines`:

- `id`, `adjustment_id`, `position`, `product_id`, `inventory_id`;
- the identity snapshot;
- `quantity` (signed delta), `tally`;
- `unit_cost`, `value`, `gl_account_code`.

An adjustment is posted when created. It has no draft, because drafts
belong to ADR 0007, and it is never edited. A mistake is corrected by a
second adjustment.

Validation, each refusal collected into one 400 with full paths:

- `quantity` is nonzero, and its sign agrees with the reason's direction;
- `CYCLE_COUNT` is refused on this route;
- `note` is required when the reason requires one;
- every line is in one branch;
- a lot or serial product names `lot_code` or `serial`;
- a serial line moves exactly 1 or -1;
- a tallied product names its tally;
- `unit_cost_ten_thousandths` is allowed only on `IN` lines and is required
  for `OPENING`.

There are two state refusals, each a 409 naming the line:

- an OUT line beyond the row's available quantity is blocker
  `below_allocated` when the shortfall is only in allocated stock;
- it is blocker `insufficient_stock` when there is not enough stock at all.

In one transaction, in section 9's order:

1. lock (creating where absent) every stock row named, in `(product_id,
   inventory id)` order;
2. for IN lines that carry a unit cost, lock the product rows in
   `product_id` order and update the average (3.5);
3. write the quantity, tally and move per line, and mark stock levels dirty
   (10.2);
4. insert the adjustment and its lines;
5. post the entry (3.4);
6. write `inventory.adjusted` last.

#### 3.4 The postings

Every row below is one balanced entry, status `POSTED`, written in the act's
transaction through `gl.PostEntry` (added by C2-2). Legs with zero amount
are left out, and an entry whose legs are all zero is not written. A period
that is closed fails the act with 409 blocker `period_closed`, as in ADR
0005.

| Movement | Act | Source | Debit | Credit |
|---|---|---|---|---|
| Stock receipt | receipt (4) | `RECEIPT` | `1030` received value (goods and any freight applied before the receipt, 3.5) | `2040` the goods value; `2045` the freight part, when freight was applied before the receipt |
| Non stock or direct ship receipt | receipt | `RECEIPT` | `1030` received value | `2040` the same (relieved to `5010` when the order line is billed, from its linked receipt lines' posted values pro rata to the billed quantity, the last bill taking the remainder: ADR 0005 section 8.4 as amended by this pull request, the same rule the vendor invoice row below applies to `2040`) |
| Freight applied after the receipt | freight charge applied (3.5) | `FREIGHT` | `1030` the on hand share, folded into the average (3.5); `5050` the sold share | `2045` the freight |
| Adjustment out | adjustment | `ADJUSTMENT` | each line's reason account, its value | `1030` the total |
| Adjustment in | adjustment | `ADJUSTMENT` | `1030` the total | each line's reason account, its value |
| Count | count post, through its adjustment | `ADJUSTMENT` | as above, per sign | |
| Vendor invoice approved, for purchase lines | AP approve (7.4) | `VENDOR_INVOICE` | `2040` relieved from the receipt lines' posted values pro rata to the matched quantity, the last relief taking the remainder (never a scale 4 recompute, which leaves cent residues in `2040`), on values net of any pre receipt freight allocation; `5050` invoiced amount less that (a negative variance credits `5050`); for lines without a purchase line, each line's account, and a carrier's freight line that names its `po_freight_charges` row debits `2045` exactly that charge, relieving the accrual | `2010` total |
| Vendor return shipped | vendor return ship (7.1) | `VENDOR_RETURN` | `2040` for the returned part of its receipt lines not yet invoiced (the vendor will simply invoice less, so the accrual clears now); `1050` the expected credit (quantity x the unit cost on the return line) for the invoiced part | `1030` quantity x average cost; the difference to `5050` |
| Vendor credit memo posted | credit memo post (7.2) | `VENDOR_CREDIT` | `2010` total | return lines: `1050` the returned value, difference to `5050`; allowance lines: `5050`; charge lines: their account |
| Transfer, allocation, reassignment | | none | | |

Customer sale and return postings stay as ADR 0005 section 8.2 sets them.
They value at `costOf`, and their moves carry that unit cost. A stocked
special order line values at `costOf` like any stocked line, because its
receipt entered stock and moved the average (ADR 0005 section 8.4 as
amended by this pull request); the linked purchase cost relieves `1030`
only on non stock and direct ship lines, whose receipts carry no average,
and even there the relief is never a recompute of a purchase line's cost:
such a line's billing relieves `1030` from its linked receipt lines'
posted values, pro rata to the billed quantity, the last bill taking the
remainder, because several purchase lines at different costs can fill one
order line (a single linked cost is undefined among them) and a scale 4
recompute of any one of them leaves cent residues in `1030`. C2-2b (PR
43) builds ADR 0005 8.4 ahead of this record's items; its
`SpecialOrderUnitCost` today takes the first linked received purchase
line by `(created_at, id)` with `LIMIT 1`, which is undefined as a cost
when several purchase lines fill the order line, and the lead carries
this rule to PR 43 so its relief follows the amendment.

#### 3.5 Cost

`costOf(product)` keeps ADR 0005's signature and meaning: the product's
`average_unit_cost` read in the posting transaction. Cycle 4 changes who
maintains it and how:

- **A receipt line** of a stocked product, with `q` stocking units received
  and value `v` (the receipt line's own extension, below), computes the
  following in its transaction, under the product row's `FOR UPDATE` lock
  (section 9 step 6b):
  - `Q` = the sum of `inventory.quantity` over all of the product's rows,
    read after the lock is taken;
  - `avg' = (Q x avg + v) / (Q + q)`, computed exactly from the value and
    the stocking quantity and rounded once, half away from zero, to scale
    4: the cost side's one rounding, beside ADR 0005's extension;
  - `avg' = v / q` when `Q <= 0` or `avg` is null.
- **The receipt line's value** is `httpx.Extend(quantity, uom_qty,
  price_uom_qty, unit_cost)` in cents, on the purchase line's stored pair
  and price unit (section 1). No factor is stored or divided by: the
  purchase line carries ADR 0006's line shape, and the received purchase
  quantity converts into the stocking unit exactly or the receipt is a 400
  naming `lines[i].quantity` with the nearest exact quantities (ADR 0006
  3.4), whose `stock_unit_changed` rule also applies at receipt.
- **Freight.** Freight applied to the purchase order before the receipt
  adds to the receipt's unit cost, as today's freight allocation does: it
  is inside the receipt's value and the average, and the receipt's entry
  credits `2045` for the freight part and `2040` for the goods part only,
  so the goods invoice's relief of `2040` (3.4) uses the receipt lines'
  posted values net of their freight allocations. Freight applied after
  the receipt capitalizes the on hand share: freight
  `F` on a receipt of `q` units puts `F x min(Q, q) / q` into `1030` and
  the average, where `Q` is the product's on hand at that moment, folded
  in under the same product lock by the same formula as a value with no
  quantity (`avg' = (Q x avg + that share) / Q`, rounded once to scale 4);
  the sold share, `F x max(q - Q, 0) / q`, goes to `5050`; the whole `F`
  credits `2045`, the freight accrual (3.2), and the carrier's approved
  invoice line names the `po_freight_charges` row and debits `2045` for
  exactly the charge, so the accrual clears and neither `2040` nor `5050`
  absorbs freight twice. Freight
  therefore stays in inventory for lumber, where it is material, without
  rewriting an average that later sales already used. This is a change
  from today; it has a CONTRACT-CHANGES row and costs 2 to 4 dev hour
  equivalents in package C.
- **IN adjustments with a unit cost** (`OPENING`, and `FOUND` when one is
  given) update the average the same way. Other IN lines and every OUT line
  move at the current average and leave it unchanged.
- **Vendor returns** leave at the average and leave it unchanged.
- `products.total_quantity` is no longer read for cost.

**The average under concurrency.** Every act that values a move at the
average (a fulfilment or counter sale through `costOf`, an OUT adjustment,
a count post, a vendor return, a restock with no source line) reads the
product row `FOR SHARE` at section 9 step 6b, after its inventory locks;
receipts and IN adjustments with a cost keep `FOR UPDATE` there and read
`Q` after taking it. Share locks do not conflict with each other, so sales
never queue behind sales; only receipts serialize against them. A sale
racing a receipt values every relieved unit at one average, the old or the
new, never a torn mix, and the race adds no difference beyond each move's
own rounding: the average is rounded once to scale 4 and every move to
cents, so the balance of `1030` and on hand x average can differ by that
rounding however the locks fall, and by nothing more. Package A proves it
with `inventory.TestAverageLockNoResidue`: a receipt and a sale racing at
pool size 4, on figures that terminate (10 on hand at 5.00, a receipt of
10 at 7.00), after which `1030` equals the sum over products of on hand x
average exactly; on figures that do not terminate the comparison is
within one cent per move.

### 4. Receipts

A receipt is a document, so that a receipt can be matched, returned against
and traced.

`purchase_receipts` has these columns:

- `id`, `number` (`RCV-`, gapped sequence), `purchase_order_id`,
  `branch_id`;
- `kind CHECK (STOCK, DIRECT_SHIP)`;
- `vendor_reference` (packing slip or ASN number), `received_on DATE`,
  `note`;
- `gl_entry_id`, `created_by`, `created_at`.

`purchase_receipt_lines` has these columns:

- `id`, `receipt_id`, `purchase_order_line_id`, `product_id NULL`;
- `purchase_quantity` (in the purchase line's `uom`), with the purchase
  line's shape snapshotted beside it: `uom`, `price_uom`, `uom_qty`,
  `price_uom_qty`, `stock_uom`, and `quantity`, the stocking unit converted
  exactly as below;
- `unit_cost` NUMERIC(12,4), per `price_uom`, the purchase line's, and
  `value`, the line's own `httpx.Extend` in cents (3.5);
- `location_id NULL` (null on direct ship and non stock lines);
- `lot_code`, `expires_on`;
- `bundles JSONB` (`[{tag, vendor_tag, quantity, tally}]`), `serials
  TEXT[]`; a tallied line's tally is rows in `line_tally_rows` (2.4), not
  JSONB.

The act is `POST /api/v1/purchase-orders/{id}/receipts`, with this body:

```
{"revision": n, "received_on": "YYYY-MM-DD", "vendor_reference": "...",
 "lines": [{"purchase_order_line_id": "...", "quantity": "12",
            "location_id": null, "lot_code": null, "expires_on": null,
            "serials": null, "bundles": null, "tally": null}]}
```

`quantity` is in the purchase unit, and `location_id` defaults to the
branch's `RECEIVING`. The act answers 201 with the purchase order (its new
revision in the body and the `ETag`) and `Location:
/api/v1/purchase-orders/{id}/receipts/{receipt_id}`. It replaces `POST
/api/v1/purchase-orders/{id}/receive`, with a CONTRACT-CHANGES row.

Validation:

- the purchase order is `approved`, `sent` or `partial`;
- each line's total received, this receipt included, is at most `ordered x
  (1 + purchasing.over_receipt_percent / 100)`, where the setting defaults to
  0. Otherwise it is a 409 blocker `over_receipt` naming the line;
- `quantity > 0`;
- the received purchase quantity converts into the stocking unit exactly
  (ADR 0006 R5); otherwise it is a 400 naming `lines[i].quantity` with the
  nearest exact quantities, and a changed stocking unit answers 409
  `stock_unit_changed` (ADR 0006 3.4);
- a lot product names `lot_code`;
- a serial product names exactly `quantity` serials, none already on hand;
- a bundle line's bundle quantities sum to the line's quantity;
- a tallied line names its tally, whose quantity equals the line's.

One transaction, in section 9's order:

1. lock the purchase order row; check the revision and the status;
2. create any missing stock rows, then lock every stock row the receipt
   touches in `(product_id, inventory id)` order;
3. lock the product rows of stocked lines in `product_id` order; update each
   average (3.5);
4. write quantities, tallies, lots, bundles and moves (`RECEIPT`); mark
   stock levels dirty;
5. for each line linked to a live special order line, insert its `RESERVED`
   allocation (5.2);
6. insert the receipt and its lines; update `qty_received`; derive the
   purchase order status (`partial` or `received`); bump its revision;
7. post the `RECEIPT` entry;
8. write the events last: `purchase_order.received`, then one
   `special_order.received` per linked order line.

The vendor performance fields stop being updated inside purchasing acts.
Today they are averaged as `(old + new) / 2`, which weights the latest
sample at one half however many came before. Lead time (from `sent_at` to
first receipt) and fill rate are computed from receipts by the reorder
refresh job and cached on `vendors`.

### 5. Special orders and direct ship

#### 5.1 The link

A special order line (`order_lines.is_special_order`, ADR 0005) is filled by
one or more purchase lines that name it. The link is
`purchase_order_lines.linked_so_line_id`, which exists already.

The rules:

- A purchase line fills at most one order line.
- An order line may be filled by several purchase lines (split vendors,
  split receipts).
- Linking checks that the order line is a special order line on an order not
  `cancelled` or closed. It also checks that the stocking quantity on live
  linked purchase lines (not `cancelled`, not closed) is at most the order
  line's `quantity - quantity_fulfilled`. Otherwise it is a 409 blocker
  `exceeds_order_line`.

Links are created in two ways:

- **By the desk.** `POST /api/v1/purchase-orders` (or `PUT` on a draft)
  with a line carrying `order_line_id`. The act locks the order row before
  the purchase order (section 9), because the link is checked against the
  order line.
- **Automatically.** When the setting `purchasing.special_order_auto_po` is
  true (the default):
  - the order module's existing `order.confirmed` event gets a drain
    subscriber, `special-order-po`;
  - like ADR 0005's allocation subscriber, it only inserts a
    `special_order_po_requests (order_line_id PRIMARY KEY, position
    BIGSERIAL UNIQUE, attempts, last_error, parked_at)` row per special
    order line without a live link, `ON CONFLICT DO NOTHING`;
  - a worker serves the queue in its own transactions, in section 9's order
    (request, order row, purchase order). It adds a linked line to the
    vendor's open `draft` purchase order for the order's branch, or creates
    one with `source` `special_order`, and writes `purchase_order.created`
    or `purchase_order.updated` last;
  - failures count `attempts` and park after 10, like ADR 0005 section 5.5;
  - parked requests are listed at `GET
    /api/v1/purchase-orders/special-order-requests`, filtered by `parked`,
    and retried by `POST
    /api/v1/purchase-orders/special-order-requests/{order_line_id}/retry`.

**Today's automatic purchase order is already broken, and C4-2 C removes
it.** `triggerAutoPO` (`quote/service.go`) fires for any quote line with a
unit cost and a product, not only special order lines, and passes the
quote line id as `linked_so_line_id`, which `REFERENCES order_lines(id)`
(migration 011): `CreatePO` commits the purchase order first, `AddPOLine`
then fails the foreign key, and the error is only logged
(`purchase_order/service.go`). What is left is an empty `SPECIAL_ORDER`
draft with no vendor; the `purchase_order_list` golden holds two of them
from the seed's run. C4-2 C deletes `triggerAutoPO` and the quote's
`AutoPOService` (an edit to `internal/quote`, which C2-2 has merged and
cycle 5 has not opened), so the order confirm subscriber above is the only
creator, and migration C cancels every empty `SPECIAL_ORDER` draft (sets
it `CANCELLED`, never deletes it), with a notice of the count. Tests: no
purchase order line is created from a quote, and none for a priced line
that is not a special order line.

#### 5.2 Allocation on receipt

ADR 0005 makes special order lines land backordered at confirm and release
through the allocation request queue. Through the generic queue alone,
however, a received special order could be allocated to an older backorder
for the same product. This record adds a reservation:

- **In the receipt transaction**, a receipt line linked to a live order line
  inserts a `RESERVED` allocation on the stock row it received into. Its
  quantity is the lesser of the received stocking quantity and the order
  line's `quantity_backordered`, and it raises `inventory.allocated` by the
  same amount. Reserved stock is not available to anyone else. The receipt
  takes no order lock: it reads the order line without one, and the worker
  rechecks under the lock. Two receipts on two purchase lines linked to one
  order line can therefore each read the same `quantity_backordered` and
  over reserve: the receipt never fails for this, the sum of the line's
  `RESERVED` rows may exceed its backorder until the worker serves it, and
  the worker releases the excess with 5.2's rule. Package C tests it: two
  concurrent receipts against one order line at pool size 4 end with the
  reservation no greater than the backorder and both stocks on the rows
  they were received into.
- **In the worker.** The receipt's `purchase_order.received` event queues
  the order through ADR 0005's subscriber, because the order is backordered
  on that product. When the allocation worker serves the request, it
  allocates each backordered line from its own `RESERVED` rows first,
  turning them `ALLOCATED`, before generic available stock.
- **Excess reservation.** If the line was meanwhile filled another way (the
  desk allocated from stock, the order was cancelled or closed short), the
  worker releases the rest of the reservation, so the stock becomes
  available. It then inserts `order_allocation_requests` rows for other
  orders backordered on that product, `ON CONFLICT DO NOTHING`. This is an
  insert only, with no lock on existing request rows, so it stays inside
  ADR 0005's order.
- **Cancel and close short** (ADR 0005 transitions) release `RESERVED` rows
  with the `ALLOCATED` ones, and insert `order_allocation_requests` rows
  for other orders backordered on the released products, `ON CONFLICT DO
  NOTHING`, insert only, exactly as the excess release does, so the freed
  stock is served to the queue rather than left to the next receipt.
- **A received line whose order line is already dead** inserts no
  reservation. The stock lands available. `purchase_order.received` names
  the line in `unlinked_order_line_ids`, so the desk sees it.

**Customer notification is an event.** `special_order.received` is written
in the receipt transaction, with entity `order` and data `{order_id,
order_number, order_line_id, customer_id, purchase_order_id, receipt_id,
quantity, kind: "stock" | "non_stock" | "direct_ship"}`. A notification
subscriber in the notification module (C4-2 builds the subscriber; the
message templates and channels stay the notification module's) sends the
customer message according to the customer's contact preferences. It dedups
on `event_id`, as ADR 0003 requires.

**Non stock special order lines** (a line with no product, which ADR 0005
allows) never enter stock. Their receipt records the receipt line, posts
`1030` against `2040`, and writes `special_order.received` with kind
`non_stock`. The line is then billable for the received quantity, and each
bill relieves `1030` from the linked receipt lines' posted values, pro
rata to the billed quantity, the last bill taking the remainder (3.4),
so `1030` nets to zero exactly across receipts and bills at different
costs. C4-2 adds
to ADR 0005's fulfilment, for any order line with a live linked purchase
line, a 409 blocker `special_order_not_received` when billing more than the
quantity received on its linked lines. Without that rule, a line billed
before receipt would post no cost, and the later receipt would leave its
value in `1030`.

#### 5.3 Direct ship

A direct ship purchase order sends goods from the vendor straight to the
customer.

The purchase order gains:

- `ship_mode CHECK (STOCK, DIRECT_SHIP) DEFAULT 'STOCK'`;
- `direct_ship_order_id UUID NULL FK orders`, required when the mode is
  `DIRECT_SHIP`;
- `ship_to_snapshot JSONB NULL`, copied from the order at approval.

Every line of a direct ship purchase order links to a line of that order.

On the order line (an ADR 0005 table, changed by C4-2 after C2-2):

- it gains `direct_ship BOOLEAN NOT NULL DEFAULT FALSE`, set when the line is
  linked to a direct ship purchase line;
- the linking act runs under the order lock and releases any allocation and
  back order the line held, because a direct ship line is never allocated or
  backordered;
- like a non stock line, it carries `quantity_fulfilled` only, and ADR 0005
  section 5.4's stocked line invariant excludes it.

A direct ship receipt is the vendor's confirmation that it shipped. It is
recorded by the same receipt route, with no `location_id`, and later by an
ASN feed. It moves no stock. It posts `1030` against `2040` at cost and
writes `purchase_order.received` (with `ship_mode`) and
`special_order.received` (kind `direct_ship`).

The order line is then billable for the received quantity, under the same
`special_order_not_received` rule. The desk bills it through ADR 0005's
fulfilment route, and its billing relieves `1030` from its linked receipt
lines' posted values, pro rata to the billed quantity, the last bill
taking the remainder (3.4; ADR 0005 section 8.4 as amended: a direct ship
line never enters stock, so its relief is a non stock line's rule).

**Automatic billing is not built in v1; the desk gets a list instead.** A
direct ship receipt could queue the order's fulfilment the way delivery
completion does (ADR 0005 section 5.5), but that queue is keyed on
`delivery_id` and cycle 2 owns it. When automatic billing is built later,
it gets its own queue keyed on `receipt_id`, in cycle 4's own files, and
re-keys nothing of ADR 0005's. Until then, so that no received direct ship
is forgotten, package C adds `GET /api/v1/purchase-orders/direct-ship-lines`
(list envelope, filters `branch_id`, `order_id`, `unbilled` default true):
one item per direct ship order line with the quantity received on its
linked purchase lines and the quantity still unbilled, read from the
receipt lines and the line's `quantity_fulfilled`, a read only join with
the branch wall applied. Each item carries the order's record link (ADR
0007 section 8's resolver shape, `links` on the item), so the desk opens
the order from the list without composing the path itself.

### 6. Purchase order approval by amount limits

#### 6.1 Limits

`purchasing_limits` has these columns:

- `id UUID`, `user_id TEXT UNIQUE NOT NULL` (the JWT subject, the key the
  rest of the user surface uses);
- `limit_cents BIGINT NOT NULL CHECK (limit_cents >= 0)`;
- `currency CHAR(3) NOT NULL`;
- `revision`, `updated_by`, `updated_at`, `created_at`.

A user with no row has a limit of zero. Limits are company wide; per branch
limits are a later decision.

Limits are set by roles `admin` and `owner` only, through a new module
segment, `purchasing-limits`:

- `GET /api/v1/purchasing-limits`;
- `GET /api/v1/purchasing-limits/{user_id}`;
- `PUT /api/v1/purchasing-limits/{user_id}`. Creating a row needs no
  precondition; changing one needs its revision.

Only an `owner` may set their own limit. A machine key is refused on every
`purchasing-limits` route with 403 `forbidden`, message "purchasing limits
are set by users", whatever scope it holds, on the same reasoning as ADR
0002's key management routes: a key that could set limits could approve
through a user it controls. A session request whose actor resolves to kind
`agent` (ADR 0007 5.4: any `X-Acting-As` value on a non key request) is
refused the same 403 with the audit row `agent.limits_refused`, because
ADR 0007's confirm gate fires only on confirm gated modules and
`purchasing-limits` is not one: without this rule an agent holding a
person's session could set the limit it is later approved under. Package D
tests both refusals with their audit rows.

Each change writes an `audit_log` row `purchasing_limit.set` holding the
before and after values and the actor, and an event
`purchasing_limit.updated`.

#### 6.2 States and transitions

Storage values are `DRAFT`, `PENDING_APPROVAL`, `APPROVED`, `SENT`,
`PARTIAL`, `RECEIVED`, `CLOSED` and `CANCELLED`, lowercase on the wire.
Transitions go through `POST /api/v1/purchase-orders/{id}/transitions` with
`{"to", "revision", "reason"}`. Edits (`PUT`) are allowed in `draft` only;
otherwise they are a 409 blocker `purchase_order_not_draft`.

The approved amount is `total_cents`, the sum of the line extensions (ADR
0001 section 7a) in the purchase order's currency. Freight charges applied
to the purchase order before submission are part of `total_cents`: the
buyer's spend authority covers the whole commitment. Freight applied after
submission is outside it and needs no re-approval; it posts by 3.5's
freight rule (the on hand share into `1030` and the average, the sold
share to `5050`, the whole freight credited to the `2045` accrual that
the carrier's invoice relieves, 3.4), a stated rule, not a gap. A
purchase order whose
currency differs from the approver's limit currency is a 409 blocker
`currency_mismatch`.

| From | `to` | Who | Guard | Effect | Events |
|---|---|---|---|---|---|
| `draft` | `approved` | user or key | `empty_purchase_order`, `vendor_required`, `unit_not_stock_unit` | submit. A user whose limit covers the total lands `approved` (an `AUTO_APPROVED` row). Anyone else (a user under the total, or any key) lands `pending_approval` and the answer is still 200 | `purchase_order.submitted`, then `purchase_order.approved` or `purchase_order.approval_requested` |
| `pending_approval` | `approved` | a user with role `admin`, `owner` or `purchasing` and limit at least the total, other than the submitter | `limit_exceeded` | approve | `purchase_order.approved` |
| `pending_approval` | `draft` | an approver who could approve it (`reason` required), or the submitter | none | reject or withdraw | `purchase_order.rejected` or `purchase_order.reopened` |
| `approved` | `sent` | user or key | `vendor_item_unmapped` (EDI only) | render and queue the outbound document (8.6), or record a manual send when `transmit` is `none` | `purchase_order.sent` |
| `approved`, `sent` with no receipt | `draft` | user | `has_receipts` | reopen; clears the approval; the next send is marked as a replacement | `purchase_order.reopened` |
| `draft`, `pending_approval`, `approved`, `sent` with no receipt | `cancelled` | user | `has_receipts` | the purchase lines' links end | `purchase_order.cancelled` |
| `partial` | `closed` | user (`reason` required) | none | close short; the unreceived remainder is closed; linked order lines stay backordered for the desk | `purchase_order.closed_short` |

A key is refused the approve edge with 403 `forbidden`, message "an approver
must be a user". The refusal is served by the route, like ADR 0002's cashier
rule. A session request whose actor resolves to kind `agent` is refused the
same way, 403 with the audit row `agent.approval_refused`: ADR 0007 5.4's
confirm gate covers only confirm gated modules, and `purchase-orders` is
not one until after cycle 4 (ADR 0007 section 10), so without this rule an
agent holding a person's session could approve its own purchase order.
Package D tests it.

The reorder job, the A2A receiver and agents create `draft` purchase orders.
Their submissions always wait for a person.

#### 6.3 The audit

`purchase_order_approvals` is append only. A trigger refuses `UPDATE` and
`DELETE`. Its columns:

- `id`, `purchase_order_id`;
- `act CHECK (SUBMITTED, APPROVED, AUTO_APPROVED, REJECTED, WITHDRAWN,
  REOPENED, MIGRATED)`;
- `actor_id`, `actor_kind`;
- `total_cents`, `currency`;
- `limit_cents NULL`, the actor's limit at the act, read in the same
  transaction;
- `reason`, `created_at`.

Every act writes one row and one `audit_log` row through `pkg/audit` in the
act's transaction. The purchase order wire carries `approved_by`,
`approved_at` and `approved_total_cents`. `GET
/api/v1/purchase-orders/{id}/approvals` lists the history.

### 7. Vendor returns, vendor credit memos and matching

#### 7.1 Returns to the vendor

`vendor_returns` has these columns:

- `id`, `number` (`RTV-`), `revision`, `vendor_id`, `branch_id`,
  `purchase_order_id NULL`;
- `status CHECK (DRAFT, SHIPPED, CREDITED, CANCELLED)`;
- `vendor_rma`, `reason_code CHECK (DAMAGED, WRONG_ITEM, OVERSHIP, DEFECTIVE,
  OTHER)`, `note`;
- `gl_entry_id`, `shipped_at`, `created_by`, `created_at`.

`vendor_return_lines` has these columns:

- `id`, `return_id`, `position`, `product_id`;
- `purchase_receipt_line_id NULL`;
- the identity: `location_id`, `lot_code`, `serial`, `bundle_tag`;
- `quantity` (positive, stocking unit), `tally`;
- `unit_cost`: the receipt line's unit cost when one is named, else the
  average at ship;
- `value`.

| From | To | Act | Effect | Events |
|---|---|---|---|---|
| none | `draft` | `POST /api/v1/vendor-returns` | | `vendor_return.created` |
| `draft` | `draft` | `PUT /api/v1/vendor-returns/{id}` | | `vendor_return.updated` |
| `draft` | `shipped` | transition | stock leaves from the named identities (`VENDOR_RETURN` moves; available stock only, otherwise a 409 blocker `exceeds_available`); posts 3.4's vendor return entry | `vendor_return.shipped` |
| `shipped` | `credited` | derived, when posted credit memo return lines cover every line's quantity | | `vendor_return.credited` |
| `draft` | `cancelled` | transition | | `vendor_return.cancelled` |

#### 7.2 Vendor credit memos

`vendor_credit_memos` mirrors ADR 0005 section 6.3's AR credit memo on the
AP side:

- `id`, `number` (`VCM-`, gapped; minted at post);
- `vendor_credit_number`, the vendor's own number, with `UNIQUE (vendor_id,
  vendor_credit_number)`;
- `revision`, `vendor_id`, `branch_id`, `currency`;
- `vendor_return_id NULL`, `vendor_invoice_id NULL`;
- `memo_date`;
- `subtotal`, `tax_amount`, `total_amount`, all `<= 0`;
- `amount_open <= 0`;
- `status CHECK (DRAFT, OPEN, PARTIAL, APPLIED, VOID)`;
- `gl_entry_id`, `created_at`.

`vendor_credit_memo_lines` has these columns:

- `id`, `credit_memo_id`, `position`;
- `kind CHECK (RETURN, ALLOWANCE, CHARGE)`;
- `vendor_return_line_id NULL`, required on `RETURN`;
- `vendor_invoice_line_id NULL` and `purchase_order_line_id NULL`, one of
  which is required on `ALLOWANCE`;
- `gl_account_code NULL`, required on `CHARGE`;
- `description`;
- `quantity` (negative), `unit_price` (`>= 0`), `line_total` (negative).

Every credit line is negative, as ADR 0001 section 7a and ADR 0005 do for
AR. A return line cannot credit more than its return line shipped less what
earlier credit memos credited (409 blocker `exceeds_returned`).

The routes are `GET` and `POST /api/v1/ap/credit-memos`, `GET` and `PUT
/api/v1/ap/credit-memos/{id}`, and `POST
/api/v1/ap/credit-memos/{id}/transitions`. The transitions are `draft` to
`open` (post: mint the number, post 3.4's entry, run matching) and `draft`
or `open` with no application to `void` (a reversal). Applications use
`POST /api/v1/ap/credit-memos/{id}/applications`, with body `{invoice_id,
amount_cents}`; each application lowers both documents' `amount_open`.

The events are `vendor_credit_memo.created`, `.posted`, `.applied`,
`.partial` and `.voided`.

AP gains `ap_applications`, which is backfilled from
`ap_payment_applications`. Its columns:

- `id`, `vendor_invoice_id`;
- `payment_id NULL`, `credit_memo_id NULL`, exactly one of them set;
- `amount`, `applied_on`, `created_at`.

`vendor_invoices` gains `amount_open`.

#### 7.3 Matching

Matching becomes line keyed and document keyed.

- **Line keys.** `vendor_invoice_lines` gains `purchase_order_line_id NULL`
  (FK), `product_id NULL`, `position`, and a `unit_price` widened to
  NUMERIC(12,4). The pairing by list position is removed. A line without a
  purchase line is a non purchase line and is matched by nothing.
- **Results.** `ap_match_results` replaces `po_match_results`, whose rows
  migrate. Its columns are `id`, `document_kind CHECK (VENDOR_INVOICE,
  VENDOR_CREDIT_MEMO)`, `document_id`, `purchase_order_id NULL`,
  `vendor_return_id NULL`, `status CHECK (MATCHED, PARTIAL, EXCEPTION)`,
  `matched_at`, `matched_by`, `notes`. Line details move into
  `ap_match_lines`, keyed by purchase line or vendor return line, with the
  quantities and prices below.
- **A vendor invoice's purchase lines.** For each purchase line it names,
  matching compares:
  - net received `R` = receipts less shipped vendor returns on the line;
  - net invoiced `I` = invoice lines less credit memo `RETURN` lines on the
    line, over every posted document on the purchase line;
  - net unit price `P` = (invoiced extensions less `ALLOWANCE` credits on
    the line) / invoiced quantity.

  Quantity matches when `|R - I|` is within `qty_tolerance_pct`. Price
  matches when `|P - PO unit cost|` is within `price_tolerance_pct`, or the
  extended difference is within `dollar_tolerance`. The config table stays
  as it is.
- **A vendor credit memo's `RETURN` lines.** Each line's credited quantity
  is compared with its return line's shipped quantity, and its credited unit
  price with the return line's unit cost, under the same tolerances. Its
  `ALLOWANCE` lines re-run the match of the invoice or purchase line they
  name. That is how a credit memo given for an overcharge turns that
  invoice's `EXCEPTION` into `MATCHED`.
- **Signs.** Matching compares net positive figures built from the signed
  lines, so a credit's negative quantity reduces `I` and never appears as a
  variance on its own.
- **When it runs.** Matching runs inside the transaction of the act that
  posts a vendor document (credit memo post, invoice create) and on demand
  through `POST /api/v1/matching/runs` with body `{document_kind,
  document_id}`.

  Auto approve on `MATCHED` stays, and it now approves through AP's
  transition in the same transaction. The system approver is attributed as
  actor kind `system`, not as a fake user id.

  The routes `GET /api/v1/matching/results` (filters `document_kind`,
  `document_id`, `purchase_order_id`, `status`) and `GET
  /api/v1/matching/results/{id}` replace `/matching/results/{po_id}`,
  `/matching/run/{po_id}` and `/matching/exceptions`. The exceptions route
  becomes `status=exception`.

  The event is `match.completed`, with data `{document_kind, document_id,
  status, purchase_order_id}`.

#### 7.4 AP posting fixes that C4-1b makes

These are required by 3.4 and are today's live failures:

- approve loads the lines;
- the entry posts through `gl.PostEntry` in the approve transaction, and a
  failure fails the act instead of being logged and swallowed;
- `VOIDED` is reachable by a transition that reverses the entry;
- `vendor_invoices` gains `branch_id`, `currency`, `revision`, a status
  CHECK, `po_id` as a real FK (an orphan is set null with a notice), and
  `UNIQUE (vendor_id, invoice_number)`. Existing duplicates are suffixed
  `#2`, `#3` in `(created_at, id)` order, with a notice;
- a payment applies only to invoices in `approved` or `partial`, and locks
  them in id order (section 9).

On the wire the vendor's number is `vendor_invoice_number`. The wire
`number` is Gable's own (`AP-`, gapped sequence). ADR 0001 section 8's
collision rule is met by listing the rename in CONTRACT-CHANGES: the
vendor's number is not unique across vendors, so it cannot serve as the
document number.

### 8. Vendor feed intake and the send outbox

#### 8.1 Feeds

A feed is one vendor's one kind of file. It is stored in `vendor_feeds`:

- `id`, `code TEXT UNIQUE`, `revision`, `vendor_id`;
- `kind CHECK (CATALOG, PRICE, STOCK)`;
- `format CHECK (CSV, X12_832, X12_846, JSON)`;
- `mapping JSONB`, the CSV column map and the unit code map;
- `machine_key_id UUID NULL UNIQUE REFERENCES api_keys(id)`;
- `active`, `created_at`, `updated_at`.

The routes (roles `admin`, `owner`, `purchasing`) are `GET` and `POST
/api/v1/vendor-feeds`, and `GET` and `PUT /api/v1/vendor-feeds/{id}`.
`vendor-feeds` joins ADR 0002's module vocabulary.

**One scoped machine key per feed.** The key is minted through ADR 0002's
user only key route with the scopes `vendor-feeds:write` and
`vendor-feeds:read`. It is bound to the feed by `PUT
/api/v1/vendor-feeds/{id}` with `machine_key_id`. Binding is user only: a
key is refused 403.

`UNIQUE (machine_key_id)` makes the binding one to one. Every
`vendor-feeds` route checks it when the caller is a key:

- a key may read and post runs only on the feed it is bound to;
- on any other feed it is refused 403 `forbidden`, message "this key is not
  bound to this feed", with an `audit_log` row `key.feed_refused` that
  holds the key as actor and the bounded path, as in ADR 0002 section 5;
- `GET /api/v1/vendor-feeds` returns only the key's own feed.

**A bound key's reach, closed.** The `vendor-feeds:write` scope admits
every non GET route under the segment, so the binding rule must close what
the scope opens. A key may do exactly three things: `GET` its own feed,
`POST /vendor-feeds/{id}/runs` on it, and read its runs and their rows.
It is refused 403 `forbidden`, with an `audit_log` row, on `POST
/api/v1/vendor-feeds` (creating a feed, and so a binding) and on every
`PUT /vendor-feeds/{id}`, on its own feed included: a key that could edit
its feed's mapping, `vendor_id` or `active` flag could rewrite what its
own files post, or point its feed at another vendor and post costs
against it. The retry and repost routes are users only. Package F tests
each refusal with its audit row.

A user with the role may post a run by hand from the desk.

#### 8.2 Runs and digest idempotency

`vendor_feed_runs` has these columns:

- `id`, `number` (`FR-`), `feed_id`;
- `digest BYTEA NOT NULL`, the SHA-256 of the request body's bytes exactly
  as received;
- `byte_size`, `filename`, `content_type`;
- `status CHECK (RECEIVED, PROCESSING, POSTED, PARTIAL, FAILED)`;
- `rows_total`, `rows_posted`, `rows_unchanged`, `rows_rejected`;
- `processed_through INTEGER NOT NULL DEFAULT 0`, the last row number
  committed;
- `attempts`, `last_error`, `parked_at`;
- `posted_by` (actor id and kind);
- `received_at`, `started_at`, `finished_at`;
- `UNIQUE (feed_id, digest)`.

`vendor_feed_files (run_id PRIMARY KEY, body BYTEA NOT NULL)` holds the
body. `vendor_feed_run_rows (run_id, row_number, status CHECK (POSTED,
UNCHANGED, REJECTED), entity_type, entity_id, error_code, message, excerpt,
PRIMARY KEY (run_id, row_number))` holds the outcome of each row. The
excerpt is bounded to 512 bytes and cut on a rune boundary, as in ADR 0002.

The post is `POST /api/v1/vendor-feeds/{id}/runs`. The raw file is the body,
with content type `text/csv`, `application/edi-x12` or `application/json`.
`filename` is a query parameter. The body limit is the setting
`vendor_feeds.max_bytes`, and the global request limit exempts this route
only. In one short transaction the route:

1. computes the digest;
2. runs `INSERT ... ON CONFLICT (feed_id, digest) DO NOTHING RETURNING id`;
3. on insert, stores the body, writes `vendor_feed.run_received` last, and
   answers 201 with the run and `Location`;
4. on conflict, reads the existing run and answers 200 with it and the
   header `Feed-Run-Replayed: true`. Nothing is stored and nothing is
   written.

A replayed file therefore posts once however often it arrives, whichever
key or user sends it and whenever. The unique index makes concurrent
replays converge: the second insert waits for the first to commit and then
conflicts.

The `Idempotency-Key` middleware still applies to the route. The digest is
the durable guarantee; the key is the short lived one.

**Why the digest is taken over the raw bytes.** A digest over a normalized
form (rows sorted, whitespace trimmed) would also catch a re-export of the
same content, but it would need a normalization rule per format, each one a
place for two different files to collide. Raw bytes are predictable. A
re-export with different bytes becomes a new run, and its rows post as
`UNCHANGED` because every row write is an upsert keyed by the vendor's own
identifiers (8.4). The cost of that is a run record, not a double posting.

**A, B, A.** A vendor that sends file A, then B, then A again to revert
cannot post the third: its digest is already taken. Effective dates cover
most price reverts, but the case is real, so the replay answer names the
earlier run it matches, and a user (roles `admin`, `owner`, `purchasing`)
may post it again: `POST /api/v1/vendor-feeds/{id}/runs/{run_id}/repost`,
body `{"reason": "..."}`, refused unless the run is finished (`POSTED` or
`PARTIAL`) and its body still stored (409 blocker `body_purged` once the
file retention purge has taken it). It creates a new run from the stored
body, processes it from row 0, records the reason on the new run, and
writes the audit row `vendor_feed.reposted`. No key may call it.

**The idempotency layer on a body this large.** The `Idempotency-Key`
middleware buffers the request body for its fingerprint inside the
request's own size limit and has no smaller cap of its own; its only own
cap, 1 MiB, is on the stored response, and an answer above it is served in
full and simply not stored, which is safe here because the digest is the
durable guarantee and a run's answer is small. Package F tests a run
posted at the `vendor_feeds.max_bytes` limit with an `Idempotency-Key`:
the first post answers 201 and the keyed retry replays it.

#### 8.3 Processing and partial failure

A worker job, `vendor-feed-runs` in `internal/app/worker`, serves the runs
as a queue:

1. It claims the oldest `RECEIVED` or `PROCESSING` run that is not parked,
   with `FOR UPDATE SKIP LOCKED`, and sets it `PROCESSING`.
2. It parses the body. A file that cannot be parsed at all (an unknown
   header, a broken X12 envelope) sets the run `FAILED` with `last_error`
   and posts nothing. The run writes `vendor_feed.run_failed`.
3. It processes rows from `processed_through + 1` in chunks of the setting
   `vendor_feeds.chunk_rows` (default 500). Each chunk is one transaction:
   lock the run row, apply each row, insert its `vendor_feed_run_rows`
   outcome, advance `processed_through` and the counters, commit. The chunk
   writes no event.
4. **Row failures.** A row that fails validation (an unknown unit, a
   malformed cost, a missing vendor SKU) is recorded as `REJECTED` with its
   code and message, inside a savepoint so the chunk goes on. Rows are
   independent facts, so one bad price does not hold back the good ones.
5. **Chunk failures.** A chunk that fails as a whole (a database error)
   rolls back. The worker increments `attempts` and records `last_error` in
   a separate short transaction. The next pass resumes from
   `processed_through`, which only ever moved with committed chunks, so no
   row posts twice. After 10 failed attempts the run is parked.
6. **The end.** A final transaction sets `POSTED` (no rejected rows) or
   `PARTIAL` (some rejected) and `finished_at`, and writes one summary event
   last: `vendor_feed.run_posted`, with data `{feed_id, run_number, kind,
   status, rows_posted, rows_unchanged, rows_rejected}`. This follows ADR
   0003 section 2's rule that bulk work writes one summary event and never
   per row events as it goes.

The routes:

- `GET /api/v1/vendor-feeds/{id}/runs` (filter `status`);
- `GET /api/v1/vendor-feeds/{id}/runs/{run_id}`;
- `GET /api/v1/vendor-feeds/{id}/runs/{run_id}/rows` (filter `status`);
- `POST /api/v1/vendor-feeds/{id}/runs/{run_id}/retry`, users only, allowed
  for `FAILED` or parked runs. It clears the parking and the attempts and
  re-queues the run from `processed_through`. A `PARTIAL` run is not
  retried: the vendor sends a corrected file, which has a new digest and
  becomes a new run;
- `POST /api/v1/vendor-feeds/{id}/runs/{run_id}/repost` (8.2's A, B, A
  case), users only.

The worker purges file bodies (not runs, rows or digests) of finished runs
older than the setting `vendor_feeds.file_retention_days`. A purged run can
still be read, and its digest still refuses a replay.

#### 8.4 What each kind posts

None of the three kinds ever creates a product or changes the dealer's own
stock. The catalogue is the dealer's, and the dealer's stock moves only
through section 2.

- **`CATALOG` (CSV, X12 832).** Upserts `vendor_items`, keyed by
  `UNIQUE (vendor_id, vendor_sku)`, with these columns:
  - `id`, `vendor_id`, `vendor_sku`, `product_id NULL`, `upc NULL`;
  - `description`, `purchase_uom`, `pack_quantity`, `min_order_quantity`,
    `lead_time_days NULL`;
  - `discontinued BOOLEAN`, `updated_by_run_id`, `created_at`,
    `updated_at`.

  A row maps to a product through an existing `product_id`, or else through
  a unique UPC match, or else stays unmapped. An unmapped row is still
  posted, not rejected. The desk maps it later with `PUT
  /api/v1/vendors/{id}/items/{item_id}`; the list is `GET
  /api/v1/vendors/{id}/items`. A row equal to the stored values is
  `UNCHANGED`.

  `edi_catalog_entries` migrates into `vendor_items` for partners linked to
  a vendor (`edi_trading_partners.vendor_id`, new). The
  `/edi/partners/{id}/import-catalog` route becomes an adapter that creates
  a run on the partner vendor's catalogue feed. It answers with the run, a
  CONTRACT-CHANGES row, and refuses with 409 blocker `partner_has_no_feed`
  when the partner has no linked vendor or no catalogue feed.
- **`PRICE` (CSV, X12 832 pricing).** Writes ADR 0006 5.5's
  `vendor_product_costs` through pricing's service: pricing is the single
  writer of that table, and the feed worker calls it as a system caller
  (`branchctx.WithSystem`, null `branch_id`, every branch), which ADR
  0006 5.6's `CheckEveryBranch` admits; a feed carries no branch split in
  v1. The row it writes:
  - `product_id` from the vendor item's mapping: a row whose vendor item
    is not mapped to a product is `REJECTED` with `vendor_item_unmapped`
    (the recovery is the desk mapping the item and then a user reposting
    the run through 8.2's `repost` act, which a `PARTIAL` run allows
    while its body is stored; the vendor's next file is not the recovery,
    because the same bytes are answered as a replay and post nothing),
    not staged;
  - `purchase_uom` from the feed's unit map, which must resolve to a
    product unit with `purchase` set in the product's set; otherwise the
    row is `REJECTED` with `unit_not_purchase_unit`;
  - `unit_cost` NUMERIC(12,4), never rounded;
  - `effective_from`, and `effective_to NULL`, from the feed's
    `effective_on` and `expires_on`;
  - `vendor_price_level_id`, named by the feed, defaulting to the vendor's
    default level.

  `vendor_product_costs` gains no column for the feed (ADR 0006 5.5's
  table is unchanged): which run wrote a cost row is the run's own
  record, `vendor_feed_run_rows` with `entity_type` `vendor_cost` and
  the row's `entity_id`.

  Quantity breaks (`min_quantity`) have no column in ADR 0006 and are
  dropped for v1; if a dealer needs them, ADR 0006 is amended first in
  its own pull request. A row for a vendor SKU that is not in
  `vendor_items` is rejected (`unknown_vendor_sku`). ADR 0006 5.6's per
  row `vendor_cost.created` and `vendor_cost.updated` events are replaced
  on the feed path only by the run's summary event (ADR 0003 section 2);
  nothing else that writes a vendor cost changes. Purchase lines default
  their unit cost through `pricing.ResolveCost`, and receipts never read
  the table.
- **`STOCK` (CSV, X12 846).** Upserts `vendor_item_availability`:
  - `vendor_item_id`, `warehouse_code`;
  - `quantity_available`, `as_of`, `updated_by_run_id`;
  - `PRIMARY KEY (vendor_item_id, warehouse_code)`.

  This is the vendor's stock, read later by GC-212. It never touches
  `inventory`.

#### 8.5 Partners and feeds

EDI trading partners keep their routes (converted by C4-1a) and gain
`vendor_id NULL`.

A partner's transport settings may name a secret only by reference: a
setting key resolved from the environment or the secret store at send time.
`transport_config` refuses on write any field named like a credential
(`password`, `key`, `secret`, `token`), as a 400 naming it.

#### 8.6 The send outbox for outbound documents

`vendor_document_outbox` has these columns:

- `id`, `vendor_id`, `partner_id NULL`;
- `document_kind CHECK (PURCHASE_ORDER, VENDOR_RETURN)`, `document_id`,
  `document_number`;
- `format CHECK (X12_850, X12_180, PDF, JSON)`;
- `transport CHECK (FILE, EMAIL, SFTP, AS2, HTTP)`;
- `payload BYTEA NOT NULL`, rendered at enqueue;
- `digest`;
- `interchange_control_number BIGINT NULL`;
- `replaces_id NULL`;
- `status CHECK (PENDING, SENDING, SENT, FAILED, PARKED)`;
- `attempts`, `last_error`;
- `created_at`, `sent_at`, `acknowledged_at NULL`.

The rules:

- **Enqueue in the act's transaction.** The `approved` to `sent` transition
  renders the document and inserts it, in that transaction. A purchase
  order is `sent` exactly when its document is queued, and the payload is
  the approved revision, not whatever the purchase order holds when a
  worker gets to it. The same applies when a vendor return ships with
  transmission.
- **Control numbers.** For X12, `interchange_control_number` and the group
  and transaction set numbers come from `edi_control_counters (partner_id,
  kind CHECK (ISA, GS, ST), next_value, PRIMARY KEY (partner_id, kind))`,
  locked and incremented in the same transaction (section 9 step 8). They
  are never 1 for every document again. ISA13 is exactly nine digits, so
  the ISA counter wraps from 999999999 back to 1 and never emits 0; the
  GS and ST counters wrap the same way at their own field widths. A wrap
  changes nothing but the number: the outbox row keeps the number it
  minted, and a vendor deduplicating on it has seen the earlier one long
  before the counter comes round.
- **Partner data.** The 850 is rendered from the partner row (its ISA and GS
  identifiers) and from `vendor_items.vendor_sku` per line. A line without a
  mapped vendor item is a 409 blocker `vendor_item_unmapped` naming the
  line. The purchase order's `number` is the BEG reference.
- **Delivery.** A worker job, `vendor-document-sender`, claims `PENDING` or
  `FAILED` rows with `FOR UPDATE SKIP LOCKED`, marks them `SENDING`, and
  commits. It then sends outside any transaction, and records `SENT` or
  `FAILED` (with attempts) in a short transaction. It parks a row after 10
  failures and writes `vendor_document.parked`. A sent row writes
  `vendor_document.sent`.

  Delivery is at least once: a crash between sending and recording resends.
  A vendor deduplicates on the interchange control number, which is the
  reason the number is minted at enqueue and kept on the row.
- **The routes** sit under their own segment, which joins the vocabulary:
  `GET /api/v1/vendor-documents` (filters `vendor_id`, `status`,
  `document_kind`, `document_id`), `GET /api/v1/vendor-documents/{id}`, and
  `POST /api/v1/vendor-documents/{id}/retry` (users only).

C4-2 builds the `FILE` transport (today's output directory, now one file
per row, named by document number and control number) and the transport
interface. `EMAIL`, `SFTP`, `AS2` and `HTTP` are later items, because each
needs credentials or a connector. A vendor whose partner names an unbuilt
transport is a 409 blocker `transport_not_available` at send. The desk can
still send with `transmit: "none"` and record a manual send.

The events outbox of ADR 0003 is not used for this. Its rows are summaries,
it is a replay window purged by age, and its delivery is per subscriber
cursor. An outbound document needs a stored payload, per row state, per row
retries, an operator retry and an audit trail that outlives the retention
age.

### 9. Lock order

ADR 0005 section 11's order holds. Cycle 4 inserts its kinds into it as
follows (the new steps are marked):

0. queue request rows: `order_allocation_requests`,
   `order_fulfillment_requests`, **`special_order_po_requests`**, **the
   claimed `vendor_feed_runs` row**, **the claimed `vendor_document_outbox`
   row**. Each is taken only by its own worker, before anything else;
1. the order row or the counter sale row. ADR 0005's step 1a, the
   customer credit advisory lock that C2-2b adds right after the order
   row, keeps its number and its place: no act that holds a purchasing or
   stock document lock (1b below) takes it, because the acts that take it
   (a confirm, a hold release, a fulfilment) lock no purchase order,
   vendor return or stock count, and this record adds no act that breaks
   that;
   - **1b. purchasing and stock documents, in id order: purchase orders,
     vendor returns, stock counts.** (This step was labelled 1a before
     ADR 0005's step 1a existed; it is renamed so the two records' labels
     do not collide, and no act holds both.) An act locks an order before
     a purchase order (special order linking, direct ship linking), and never
     the reverse: receipts and approvals never lock orders;
   - **1c. `purchasing_limits` is read and never locked.** The limit in
     force is the one committed when the approval reads it;
2. payments; 3. credit memos; 4. invoices; 5. `ar_applications`. These are
   unchanged;
   - **5a. AP documents in id order (vendor invoices, vendor credit memos,
     AP payments), then `ap_applications`.** No act locks both an AR and an
     AP document;
6. inventory rows, in `(product_id, inventory id)` order. Allocation rows,
   tally rows and bundle rows of a stock row are changed only under its
   lock. An act creates any missing stock rows (`INSERT ... ON CONFLICT DO
   NOTHING`) before it locks, so creation never interleaves with locking;
   - **6b. product rows, in `product_id` order.** Two locks live here.
     `FOR UPDATE`, on an average cost update, is taken only by receipts, IN
     adjustments with a cost, the post receipt freight fold (3.5) and the
     C4-2 migration, each of which reads `Q` after taking it. `FOR SHARE`,
     on an average cost read, is taken by every act that values a move at
     the average (a fulfilment or counter sale through `costOf`, an OUT
     adjustment, a count post, a vendor return, a restock with no source
     line), after its inventory locks (3.5). Share locks never conflict
     with each other, so sales do not queue behind sales; a receipt's
     `FOR UPDATE` serializes against them and against the other average
     movers only. Product edits lock a product row and take no inventory
     lock, so they cannot meet a receipt in the opposite order;
   - **6c. `stock_level_dirty` inserts (`ON CONFLICT DO NOTHING`).** These
     take no lock on existing rows;
7. the customer row;
8. the gapless counter row, **and `edi_control_counters`**;
9. journal entry inserts;
10. the outbox, last.

The vendor feed worker's chunk transactions take the run row (0) and then
only `vendor_items` and their cost and availability rows, in `(vendor_id,
vendor_sku)` order. One other act locks a cost row: pricing's vendor cost
update route (ADR 0006 5.6), one row at a time, never beside a feed row
or another cost row, so no cycle forms between them.

Every act that opens a transaction carries the recipe's tests: three
contenders at pool size 4, and the gated saturation test.

### 10. Events

#### 10.1 The list

Every event is written through the outbox, last, in the act's transaction,
with a small summary as data.

| Entity | Types | Written by |
|---|---|---|
| stock adjustment | `inventory.adjusted` | adjustments and count posts; data `{number, branch_id, reason_code, source, line_count, value_cents}` |
| stock transfer | `inventory.moved` | transfers; data `{number, branch_id, lines: [{product_id, from_location_id, to_location_id, lot_code, bundle_tag, quantity}]}` capped at 50 lines with `truncated` |
| product | `stock.low`, `stock.recovered` | the stock level job (10.2); data `{product_id, branch_id, available, reorder_point, unit}` |
| reorder recommendation | `reorder.recommended` | the reorder job (10.3) |
| purchase order | `purchase_order.created`, `purchase_order.updated`, `purchase_order.submitted`, `purchase_order.approval_requested`, `purchase_order.approved`, `purchase_order.rejected`, `purchase_order.reopened`, `purchase_order.sent`, `purchase_order.cancelled`, `purchase_order.closed_short` | purchasing acts; data `{number, vendor_id, branch_id, status, from_status, revision, total_cents, currency}` |
| purchase order | `purchase_order.received` | receipts; ADR 0005's data (`purchase_order_id`, `branch_id`, `product_ids`) plus `{receipt_id, receipt_number, ship_mode, linked_order_line_ids, unlinked_order_line_ids, value_cents}` |
| order | `special_order.received` | receipts (5.2) |
| purchasing limit | `purchasing_limit.updated` | limits (6.1) |
| stock count | `stock_count.created`, `.started`, `.updated`, `.posted`, `.cancelled` | counts |
| vendor return | `vendor_return.created`, `.updated`, `.shipped`, `.credited`, `.cancelled` | returns |
| vendor invoice | `vendor_invoice.created`, `.approved`, `.partial`, `.paid`, `.voided` | AP (C4-1b) |
| vendor credit memo | `vendor_credit_memo.created`, `.posted`, `.applied`, `.partial`, `.voided` | AP (C4-2) |
| match | `match.completed` | matching |
| vendor feed run | `vendor_feed.run_received`, `vendor_feed.run_posted`, `vendor_feed.run_failed` | feeds |
| vendor document | `vendor_document.sent`, `vendor_document.parked` | the sender |
| vendor | `vendor.created`, `vendor.updated` | vendors (C4-1a) |

The plan's six names are covered: `inventory.adjusted`, `inventory.moved`,
`stock.low`, `reorder.recommended`, `purchase_order.created` and
`purchase_order.received`. The rest are added here.

#### 10.2 `stock.low`, edge triggered

`stock_levels` holds stock levels per product and branch:

- `product_id`, `branch_id`;
- `reorder_point NUMERIC(12,4) NULL`, `reorder_quantity NULL`;
- `low_since TIMESTAMPTZ NULL`;
- `revision`, `updated_at`;
- `PRIMARY KEY (product_id, branch_id)`.

C4-1a backfills it from `products.reorder_point` and `reorder_qty` for every
branch that holds the product. From then on, the refresh job writes per
branch targets. The routes are `GET /api/v1/inventory/levels` (filters
`branch_id`, `product_id`, `low`) and `PUT /api/v1/inventory/levels/{id}`.

The money paths never write events about stock levels. Instead:

- the inventory service inserts `stock_level_dirty (product_id, branch_id,
  PRIMARY KEY)` with `ON CONFLICT DO NOTHING` in any act that changes a
  quantity or an allocation (lock step 6c);
- a worker job, `stock-levels`, claims dirty rows with `FOR UPDATE SKIP
  LOCKED`, one product and branch per transaction, and computes `available`
  (the sum of `quantity - allocated` over the branch's rows);
- crossing to `available <= reorder_point` with `low_since` null sets it and
  writes `stock.low`;
- crossing back above clears it and writes `stock.recovered`;
- it deletes the dirty row.

A product that stays low writes no further events. `GET
/api/v1/dashboard/inventory-alerts` reads `stock_levels.low_since`, a change
the dashboard module takes in cycle 5.

#### 10.3 `reorder.recommended` and its payload

The reorder job (`RefreshReorderTargets` and `CreateReorders`, scheduled by
today's settings) computes per product and branch:

| Field | Meaning |
|---|---|
| `on_hand`, `allocated`, `available` | sums over the branch's stock rows |
| `on_order` | stocking quantity ordered less received on purchase lines in `approved`, `sent` or `partial` for the branch |
| `backordered` | the sum of `quantity_backordered` on the branch's open order lines |
| `velocity` | stocking units issued per day over the lookback, from `SALE` moves less `SALE_RETURN` moves (actual issues, not ordered quantities as today) |
| `lookback_days` | the setting |
| `lead_time_days` | the vendor item's when fed, else the vendor's measured lead time from receipts, else the setting `reorder.default_lead_time_days` |
| `lead_time_source` | `vendor_item`, `measured` or `default` |
| `reorder_point`, `reorder_quantity` | from `stock_levels` |
| `suggested_quantity` | `max(reorder_quantity, reorder_point + backordered - (available + on_order))`, rounded up to the vendor item's pack quantity |
| `vendor_id`, `vendor_item_id`, `unit` | |

A recommendation is due when `available + on_order - backordered <=
reorder_point`.

Each due recommendation is stored in `reorder_recommendations`:

- `id`, `run_id`, `product_id`, `branch_id`, `vendor_id`;
- the fields above;
- `status CHECK (OPEN, ORDERED, DISMISSED)`;
- `purchase_order_line_id NULL`, `created_at`.

The run writes one `reorder.recommended` per recommendation, carrying the
whole payload as data. These are written at the end of the run's
transaction, one per branch, after all its inserts: a bulk writer writes its
events last (ADR 0003 section 2).

`GET /api/v1/purchase-orders/recommendations` serves the stored
recommendations in the list envelope (filters `branch_id`, `vendor_id`,
`status`). `CreateReorders` turns open recommendations into draft purchase
lines and marks them `ORDERED`.

### 11. Routes

These are the routes after C4-2. Every one of them follows ADR 0001, has its
fragment edited, and has CONTRACT-CHANGES rows for the differences.

| Module segment | Routes |
|---|---|
| `inventory` | `GET /inventory`; `GET /inventory/moves`; `GET /inventory/reconciliation`; `GET`, `POST /inventory/adjustments`, `GET /inventory/adjustments/{id}`; `GET`, `POST /inventory/adjustment-reasons`, `PUT /inventory/adjustment-reasons/{code}`; `GET`, `POST /inventory/transfers`, `GET /inventory/transfers/{id}`; `GET`, `POST /inventory/counts`, `GET /inventory/counts/{id}`, `PUT /inventory/counts/{id}/lines`, `POST /inventory/counts/{id}/transitions`; `GET /inventory/lots`, `GET /inventory/lots/{id}`; `GET /inventory/bundles`, `GET /inventory/bundles/{id}`; `GET /inventory/allocations`, `POST /inventory/allocations/{id}/reassignments`; `GET /inventory/levels`, `PUT /inventory/levels/{id}` |
| `purchase-orders` | `GET`, `POST /purchase-orders`; `GET`, `PUT /purchase-orders/{id}`; `POST /purchase-orders/{id}/transitions`; `GET`, `POST /purchase-orders/{id}/receipts`, `GET /purchase-orders/{id}/receipts/{receipt_id}`; `GET /purchase-orders/{id}/approvals`; the freight routes, converted; `GET /purchase-orders/recommendations`; the reorder routes, converted; `GET /purchase-orders/special-order-requests`, `POST /purchase-orders/special-order-requests/{order_line_id}/retry`; `GET /purchase-orders/direct-ship-lines` (5.3) |
| `purchasing-limits` (new) | `GET /purchasing-limits`, `GET`, `PUT /purchasing-limits/{user_id}`; users only |
| `vendor-returns` (new) | `GET`, `POST /vendor-returns`; `GET`, `PUT /vendor-returns/{id}`; `POST /vendor-returns/{id}/transitions` |
| `ap` | the invoice routes, converted, with `POST /ap/invoices/{id}/transitions` replacing `/approve`; `GET`, `POST /ap/credit-memos`, `GET`, `PUT /ap/credit-memos/{id}`, `POST /ap/credit-memos/{id}/transitions`, `POST /ap/credit-memos/{id}/applications`; payments and aging, converted |
| `matching` | `GET`, `PUT /matching/config`; `POST /matching/runs`; `GET /matching/results`, `GET /matching/results/{id}` |
| `vendors` | `GET`, `POST /vendors`; `GET`, `PUT /vendors/{id}`; `GET /vendors/{id}/items`, `PUT /vendors/{id}/items/{item_id}` |
| `vendor-feeds` (new) | 8.1 to 8.3 |
| `vendor-documents` (new) | 8.6 |
| `edi` | the partner routes, converted; import-catalog as an adapter (8.4) |

All paths are under `/api/v1`. The A2A receiver stays on its JWS seam (ADR
0002 section 3). C4-1a fixes its check-then-act duplicate:

- the log row is inserted first, `ON CONFLICT (idempotency_key) DO
  NOTHING`, in the same transaction as the purchase order it creates;
- a conflict answers today's 409 body;
- the seam's wire stays byte for byte.

Roles:

- inventory: `admin`, `owner`, `warehouse` (reads also `sales`);
- purchasing: `admin`, `owner`, `purchasing`;
- AP and matching: `admin`, `owner`, `finance`;
- feeds and vendor documents: `admin`, `owner`, `purchasing`.

**The branch wall, per route (on the guards PR 37 and PR 39 landed).**
Every route is behind `scoped(...)` and the branch middleware, and the
wall is three guards the platform already carries plus two same branch
rules this record adds:

- **Body branches.** `middleware.BranchGuard.CheckPayloadBranch`
  (`core/pkg/middleware/branch_payload.go`) on the payload `branch_id` of
  purchase orders, counts, vendor returns, vendor credit memos and vendor
  invoices.
- **Body locations.** `CheckPayloadLocation` on every body `location_id`:
  adjustment lines, a transfer's `from` and `to_location_id`, the count
  root, receipt lines, a reassignment's `to`, and vendor return lines.
- **Path ids.** The path id record check that `checkPOBranch`
  (`purchase_order/handler.go`) applies, on every by id route whose record
  has a branch: purchase orders, receipts, adjustments, transfers, counts,
  vendor returns, credit memos, vendor invoices (which gain `branch_id`),
  allocations and reassignments (the stock row's branch), stock levels,
  approvals, matching results, and the special order retry (the order's
  branch). A record of a branch the caller may not target is a 403 before
  the act.
- **Lists.** The list wall (`middleware.BranchIDForQuery`, nil meaning no
  wall): a bound caller sees the context branch, and an administrator or
  an unbound caller with no context branch sees every branch. The lane
  STATUS's queued item (5) is named as C4-1a's test: with
  `default_branch_required` false and no header, today's purchase order
  list shows every branch, and the converted list applies the wall.
- **Two same branch rules the wall does not give.**
  - A receipt's `location_id` must be in the purchase order's branch,
    else 409 `cross_branch` naming the line: today `HandleReceivePO`
    checks only the caller's grants on the locations it names, so an
    administrator or an unbound key can receive branch A's purchase
    order into branch B.
  - A linked purchase line's purchase order must be in the linked order
    line's order's branch, else 409 `cross_branch` naming the line:
    otherwise the `RESERVED` row sits in another branch, outside the pick
    policy of 2.7 and outside the release the worker does.

### 12. Migrations, in order

Each item lands numbered migrations with their down files. Every step is
idempotent and every backfill reads only columns that earlier steps made
NOT NULL. Each migration is applied to an empty database and to a seeded
one, and the backfill is tested on rows that exist. The numbering: 092 is
the last number merged to `refactor/v1`; 093 is C3-1's
`catalog_pricing_wire_contract` (PR 41) and 094 is C2-2b's allocation
migration (PR 43); the cycle 2 and cycle 3 items still to merge take the
numbers after those as they merge, and this record reserves none. Cycle 4
numbers from the next free at merge: C4-1a plans 097 and C4-1b 098, and
the C4-2 packages take the next free numbers upward in their merge
order, planned A 099, B 100, C 101, D 102, E 103, F 104 (D and F may
merge beside A to C, so their numbers follow the merge, not the plan; a
package whose planned number is taken takes the next free one).

**C4-1a, `inventory_purchasing_wire_contract` (097).**

1. `purchase_orders`:
   - fill `created_at` and set it NOT NULL; add `revision`;
   - add `number` (`PO-`, sequence, backfill in `(created_at, id)` order,
     DEFAULT, UNIQUE);
   - add `currency` (backfill the default), `sent_at`;
   - add a status CHECK on today's values (`DRAFT`, `SENT`, `PARTIAL`,
     `RECEIVED`, `CANCELLED`).
2. `purchase_order_lines`:
   - add `position` (by `(created_at, id)` within the purchase order;
     `created_at` exists, migration 011);
   - widen `quantity` and `qty_received` to NUMERIC(12,4);
   - rename `cost` to `unit_cost` and widen it to NUMERIC(12,4), per
     `price_uom`;
   - add the line shape of section 1: `uom`, `price_uom`, `uom_qty`,
     `price_uom_qty`, `stock_uom` and `stock_quantity`, backfilled to the
     product's stocking unit, the pair 1 and 1 and `stock_quantity =
     quantity` (TEXT columns until ADR 0006's catalogue has landed; the
     `units(code)` foreign keys are added by the first cycle 4 migration
     that lands after C3-2A-units);
   - add `line_total` NUMERIC(12,2), computed with ADR 0001's extension.
3. `inventory.created_at` (the `allocated` widening to NUMERIC(12,4) is
   already done: C3-1's migration 093).
4. `vendors`: `created_at` NOT NULL, `revision`.
5. `stock_levels`, backfilled from `products` for each branch holding the
   product; `stock_level_dirty`; `reorder_recommendations`;
   `reorder_runs` gains `branch_id NULL`.
6. Keyset indexes on purchase orders and vendors.

**C4-1b, `ap_wire_contract` (098).**

1. `vendor_invoices`:
   - `created_at` NOT NULL, `revision`;
   - `number` (`AP-`);
   - `branch_id`, backfilled from the purchase order's branch or the
     default branch;
   - `currency`;
   - a status CHECK;
   - `po_id` as a real FK (orphans set to null, with a notice);
   - `UNIQUE (vendor_id, invoice_number)` after suffixing duplicates;
   - `amount_open`.
2. `vendor_invoice_lines`: `position`, `purchase_order_line_id NULL`,
   `product_id NULL`, `unit_price` widened to NUMERIC(12,4). Existing lines
   are not linked: today's lines have no reliable key to their purchase
   line, and the old positional pairing is exactly the defect.
3. Keyset indexes on vendor invoices.

**C4-2, in package order (section 13). Each package is one migration file.**

*A, `stock_identity`, merging in two steps (13.2): A1 is steps 1 to 5,
A2 is step 6, each with its own migration file and numbers following the
merge order.*

1. `locations`: uppercase `type`, map unknowns to `BIN` (notice), add the
   CHECK; add `stockable`; compute `path`; create the three system
   locations per branch and name them on the branch row.
2. `inventory` rows with no location: a pre flight report, `core migrate
   -report stock` (ADR 0006 A1's shape, read only), lists every such row
   with its product and quantity and the count per product, so the
   operator of a multi branch dealer can re-point a branch's counter
   stock before upgrading rather than find it moved to the default
   branch; the migration itself points what is left at the default
   branch's `UNASSIGNED` (notice with the count). Then make
   `location_id` NOT NULL; add `branch_id` and its trigger; add the
   stockable trigger; recreate `v_inventory_with_branch` without the
   join, as `SELECT * FROM inventory`: `i.*` now carries `branch_id`
   itself, and a later `CREATE OR REPLACE` with the join's alias would
   fail on the duplicate column.
3. Merge duplicate `(product_id, location_id)` rows: sum `quantity` and
   `allocated` into the oldest row, delete the others, and report the count.
   Nothing references inventory ids yet, so the merge is safe.
4. `products.tracking` (default `NONE`), `products.bundled`; `stock_lots`,
   `stock_bundles`; `inventory.lot_id`, `bundle_id`, `is_serial`;
   `inventory_tally`; `inventory.untallied_lf`, backfilled to `quantity`
   on a random length product's rows and 0 elsewhere (every existing
   random length row becomes an untallied remainder row, 2.4);
   `line_tally_rows` gains its five act line foreign
   key columns (2.4) and widens the `num_nonnulls` CHECK; the unique
   indexes and checks of 2.1 and 2.5 (the allocation CHECK `NOT VALID`,
   validated when clean).
5. `stock_moves`; one `OPENING` move per row with a nonzero quantity, at
   the product's current average, with no journal entry (history is not
   invented, as in ADR 0005).
6. `stock_allocations`. Backfill per product and branch: order lines with
   `quantity_allocated > 0` in `(orders.confirmed_at, line id)` order take
   the rows with `allocated > 0`, largest first. A line that cannot be
   placed has its unplaced part moved to `quantity_backordered`, so the
   allocation worker refills it. Each row's `allocated` is then set to the
   sum of its allocation rows. The migration reports the counts.

*B, `stock_adjustments_and_counts`.*
1. Accounts `5060`, `5070`; journal source values.
2. `adjustment_reasons` with the seed.
3. `stock_adjustments`, `stock_adjustment_lines`, `stock_transfers`,
   `stock_transfer_lines`, `stock_counts`, `stock_count_lines`, each with
   its number sequence.

*C, `receipts_and_special_orders`.*
1. Accounts `2040`, `2045`, `5050`; the journal sources `RECEIPT` and
   `FREIGHT`.
2. `purchase_receipts`, `purchase_receipt_lines`.
3. Backfill one receipt per purchase order with `qty_received > 0`,
   marked `note = 'migrated'` (the marker its down reads below). It has
   one line per received line, no location, no stock move and no
   entry, because the stock already entered through the legacy path. This
   gives matching and returns a receipt to name.
4. `stock_allocations.state` and `receipt_line_id`;
   `special_order_po_requests`; `purchase_orders.ship_mode`,
   `direct_ship_order_id`, `ship_to_snapshot`; `order_lines.direct_ship`;
   cancel every empty `SPECIAL_ORDER` draft purchase order (5.1), with a
   notice of the count.

*D, `purchase_approval`.*
1. `purchasing_limits`, `purchase_order_approvals` with the append only
   trigger.
2. The status CHECK gains `PENDING_APPROVAL`, `APPROVED` and `CLOSED`.
   Every purchase order in `SENT`, `PARTIAL` or `RECEIVED` gets one
   `MIGRATED` approval row with a null actor, recording that it was approved
   before limits existed. Drafts stay drafts.

*E, `vendor_returns_and_credits`.*
1. Account `1050`; the journal sources `VENDOR_RETURN` and `VENDOR_CREDIT`.
2. `vendor_returns`, `vendor_return_lines`, `vendor_credit_memos`,
   `vendor_credit_memo_lines`.
3. `ap_applications`, backfilled from `ap_payment_applications`;
   `vendor_invoices.amount_open` recomputed.
4. `ap_match_results` and `ap_match_lines`. `po_match_results` rows
   migrate as `VENDOR_INVOICE` results, each marked `notes = 'migrated'`
   (the marker its down reads below). Their line details are not carried
   over, because they were paired by position; the next run recomputes
   them. The old tables are renamed `*_legacy`.

*F, `vendor_feeds`.*
1. `vendor_items`, `vendor_item_availability`. No cost table is created:
   `vendor_product_costs` is ADR 0006's, built by C3-2A-pricing's
   migration and written through pricing's service (8.4).
2. `edi_trading_partners.vendor_id`, backfilled where exactly one vendor's
   `name` equals the partner's `name`, else left null (notice).
   `edi_catalog_entries` rows of linked partners are copied into
   `vendor_items`, with `product_id` from `internal_product_id`. The table
   stays as it is for the unlinked ones.
3. `vendor_feeds`, `vendor_feed_runs`, `vendor_feed_files`,
   `vendor_feed_run_rows`.
4. `vendor_document_outbox`, `edi_control_counters` (starting at 2 for any
   partner, because 1 has been used by every document so far).

**Down files.** Each package's down reverses its own steps and refuses,
naming the first row it cannot map back, wherever data written in the new
shape has no place in the old one (a down never discards a dealer's data).
A down also removes what its own up wrote as backfill, so it runs on a
real database and not only an empty one, and where a backfill cannot be
undone exactly the down states which rows it keeps. A's down deletes the
`OPENING` moves its up wrote (the `OPENING` kind is the migration's
alone, so every other move is later data) and refuses while any other
stock move exists; it refuses while any lot, serial, bundle or tally row
exists (A creates those tables empty); the duplicate merge is kept, not
split, each merged row staying one row with its summed `quantity` and
`allocated`, valid old shape data, and the down reports the count A
reported. A2's down deletes every allocation row and drops the table,
keeping the `allocated` and `quantity_allocated` figures the up derived
(valid old shape values; the parts the up moved to `quantity_backordered`
stay backordered, where the old shape's allocation serves them). B's down
refuses while any adjustment reason is in use by an adjustment or count
that is not cancelled. C's down deletes the receipts its up wrote
(`note = 'migrated'`), refusing while any other receipt exists, while any
later row (a vendor return line, a match line) references one of them, or
while any special order request or `RESERVED` allocation exists; the
empty `SPECIAL_ORDER` drafts its up cancelled stay cancelled (valid old
shape data). D's down refuses while any purchase order is
`PENDING_APPROVAL`, `APPROVED` or `CLOSED`. E's down writes every
`ap_applications` row whose `payment_id` is set back into
`ap_payment_applications` (keyed by application id, so the up's own
backfill rows write back as what they are) and un-migrates the match
results its up marked (`notes = 'migrated'`) into `po_match_results`,
renaming the `*_legacy` tables back; it refuses while any result or match
line the up did not migrate exists, or any vendor return, vendor credit
memo or credit memo application exists (none of those has an old place).
F's down refuses while any feed run, run row or outbox row exists. Each
down is run on the seeded database after its up, as a test of the item
that owns the migration, so a down that refuses against its own backfill
cannot pass unnoticed.

### 13. The items

#### 13.1 C4-1: six modules onto the recipe (35 to 60 dev hour equivalents)

The split follows the run order of the Status: C4-1a (22 to 38) after
C2-2b, C4-1b (13 to 22, the AP fixes) after C2-4.

**C4-1a builds:**

- inventory, purchase orders, matching's routes as they are, EDI partners
  and vendors converted per the recipe;
- migration 097;
- the existing routes on the envelope, with quantities as decimal strings,
  money in `_cents` and unit costs in `_ten_thousandths`;
- purchase order numbers and revisions;
- the purchase line shape of section 1, held to the stocking unit when
  C3-2A-units has not merged, and its cost defaulted through
  `pricing.ResolveCost` once C3-2A-pricing has;
- the receive act made correct in place (until package C replaces its
  route with receipts): the purchase order locked, its status checked under
  the lock, the over receipt rule, stock rows locked in order, and the
  average under the product lock, its `Q` read once and the cost update's
  error not dropped;
- the A2A duplicate fix;
- `stock_levels` and the `stock-levels` job;
- the reorder payload of 10.3;
- these events: `inventory.adjusted` (the old adjust route, until package B
  replaces it), `inventory.moved`, `stock.low`, `stock.recovered`,
  `reorder.recommended`, `purchase_order.created`, `.sent`, `.received`
  and `vendor.*`;
- the census and vocabulary entries.

It tests, beyond the recipe's set:

- each live failure as a test that fails at the base:
  - over receipt accepted;
  - a second purchase order from one A2A key under concurrency;
  - recommendations that ignore on order;
- three concurrent receipts of one line that together exceed it, where
  exactly one passes the over receipt check;
- the list wall, the STATUS queued item (5): the purchase order list
  showing every branch when `default_branch_required` is false now applies
  the wall (section 11);
- `stock.low` written once on crossing and not again while low;
- `reorder.recommended` data carrying `on_order`, `velocity` and
  `lead_time_days` with their sources;
- a key holding only `purchase-orders:read` refused a write with an audit
  row.

**C4-1b builds:**

- the AP fixes of section 7.4: approve loads the lines, the entry posts
  through `gl.PostEntry` in the approve transaction and a failure fails
  the act, `VOIDED` is reachable by a transition that reverses the entry,
  and a payment applies only to `approved` or `partial` invoices, locked
  in id order;
- migration 098;
- the vendor invoice wire (Gable's `number` `AP-`, the vendor's as
  `vendor_invoice_number`) and `vendor_invoice.*` events.

It tests, beyond the recipe's set:

- each live failure as a test that fails at the base:
  - an AP entry with only the payable leg;
  - an AP posting error swallowed;
  - a payment applied to an unapproved invoice.

#### 13.2 C4-2: the parity, in six packages (148 to 240 dev hour equivalents)

Against the plan's 146 to 236: the delta of 2 to 4 at each end is the
direct ship desk list and the post receipt freight fold of 3.5, both added
in review round 1. Each package is its own pull request into
`refactor/v1`, in the run order of the Status: A, then B, then C, with D
after C4-1a, E after C4-1b, and F after C4-1a and C3-2A-pricing; D and F
may run beside A to C. Each has its own migration file and its own tests.

Package A carries the riskiest change of the cycle, every stock caller, so
it merges in two steps, each its own pull request, its own migration file
and its own review: **A1** (16 to 24) the identity columns, the move
ledger, the single writer gate and the reconciliation read (migration A,
steps 1 to 5); **A2** (22 to 34) the allocation rows, the pick policy, the
reassignments, the counter line identity and the callers in order, invoice
and POS (migration A, step 6). Together they are A's 38 to 58.

| Package | Builds | Size |
|---|---|---|
| A, identity (A1 then A2) | 2.1 to 2.8: identity columns, lots, serials, bundles, tallies, the move ledger, the single writer gate, the reconciliation read, allocation rows and the pick policy, reassignments, transfers, counter line identity; the inventory service signatures and their callers in order, invoice and POS | 38 to 58 |
| B, adjustments and counts | 2.9, 3.1 to 3.3, the adjustment and count postings | 20 to 34 |
| C, receipts and special orders | 3.4 and 3.5 for receipts, section 4, section 5, the notification subscriber, the direct ship list, the deletion of the quote's auto purchase order path | 28 to 44 |
| D, approval | section 6 | 14 to 24 |
| E, returns, credit memos, matching | section 7 | 24 to 40 |
| F, feeds and the send outbox | section 8 | 24 to 40 |

The exit test lines of cycle 4, each mapped to the package that proves it
and to its tests:

| Exit line | Package | Tests |
|---|---|---|
| **A feed run posts once however often it is replayed** | F | `vendorfeed.TestRunPostsOnceOnReplay`: the same body posted three times, sequentially, answers 201 then 200, 200 with `Feed-Run-Replayed: true`, one run, one stored body, one `vendor_feed.run_received`, and after processing one `vendor_feed.run_posted` and each cost row written once; the same body posted by three concurrent contenders at pool size 4 gives exactly one 201 and one run; the same body posted by the bound key and by a user is one run; a worker crash simulated after chunk 2 of 3 resumes from `processed_through` and the posted row count equals the file's; a file with two bad rows ends `partial`, the good rows posted, the bad rows `rejected` with codes; an unparseable file ends `failed` with nothing posted; a re-export with different bytes and the same rows is a new run whose rows are all `unchanged`; a key bound to feed X posting to feed Y is 403 with `key.feed_refused` |
| **A PO above a buyer's limit waits for approval** | D | `purchase_order.TestApprovalAboveLimitWaits`: a buyer with a limit of 1,000.00 submits 1,500.00: 200, status `pending_approval`, `submitted` then `approval_requested`, one `SUBMITTED` row with `limit_cents` 100000; the buyer's own approve is 409 `limit_exceeded`; a key's approve is 403; an agent marked session's approve is 403 with `agent.approval_refused`; a manager with a limit of 5,000.00 approves: `approved`, an `APPROVED` row and the audit row; a submission at 800.00 lands `approved` with `AUTO_APPROVED`; a draft edit after a reopen needs approval again; a receipt against `pending_approval` is 409; three concurrent approvers at pool size 4 give one approval; setting a limit by key or by an agent marked session is 403 with the audit row; the limit change writes its audit row; the approvals table refuses `UPDATE` |
| **Stock moves by bin and lot** | A (with B and C) | `inventory.TestStockMovesByBinAndLot`: a lot product received into bin B1 with lot L1 and into B2 with L2 shows two rows; a transfer of 5 of L1 from B1 to B3 moves exactly that identity (B1 L1 down 5, B3 L1 up 5, two moves under one act id, `inventory.moved`); allocation takes L1 before L2 by expiry, and the fulfilment's moves and the invoice line's `stock` name L1 and B3; a serial sold at the counter must be named, and a second receipt of the same serial is 409 `serial_on_hand`; a bundle transferred whole re-points its row, and a part transfer breaks it; a tallied bundle sold by tally decrements its lengths, and a length it lacks is 409 `tally_unavailable`; an untallied sale of 100 LF against a row of 3 at 16 and 4 at 14 takes the remainder and whole pieces longest first, cuts the last piece, and leaves the remnant as a new length row with the invariant `quantity = tally linear feet + untallied_lf` intact (2.4); a tallied receipt into a row holding an untallied remainder lands beside it; a transfer with `with_allocations` carries the allocation; for every row the quantity equals the sum of moves and the reconciliation read is empty after every test; the single writer gate fails when a test file outside the package writes `inventory` |
| **A vendor credit memo matches** | E | `ap.TestVendorCreditMemoMatches`: 10 received at 4.00, 2 returned on a vendor return (`shipped`, `1050` debit 8.00, `1030` credit at average), a credit memo with one `RETURN` line of -2 at 4.00 posts and its match result is `matched`, and the return becomes `credited`; a credit memo crediting 3 against a return of 2 is 409 `exceeds_returned`; an invoice at 4.40 against a purchase line at 4.00 beyond tolerance is `exception`, and a credit memo `ALLOWANCE` of -10 at 0.40 on that invoice line re-runs the match to `matched`; the credit memo applied to an open invoice lowers both `amount_open`s; the credit memo entry balances (`2010` debit, `1050` credit; the `5050` leg is zero at a credit of 4.00 against a return cost of 4.00, and 3.4 omits zero legs); and the P2-7 case: receive 10, return 2 before invoicing, invoice 8, and both `1050` and `2040` end at zero |

The other tests, by package:

- **B.**
  - An adjustment of `DAMAGE` -3 posts one entry, `5070` debit and `1030`
    credit at 3 x average, in its transaction, and a failing event write
    rolls the entry back.
  - A direction mismatch is a 400 naming `lines[0].quantity`.
  - `CYCLE_COUNT` on the route is a 400.
  - A deactivated reason is refused.
  - `inventory.TestCountSaleBeforeLineCounted`: a snapshot of 10, a sale
    of 2, then a count of 8 leaves 8 on hand (the variance is 8 - 8 = 0,
    not 8 - 10 = -2).
  - `inventory.TestCountSaleAfterLineCounted`: a snapshot of 10, a count
    of 10, then a sale of 2 leaves 8 (the sale is later than the count and
    the post does not touch it).
  - `count_below_allocated` blocks the post.
- **C.**
  - A receipt posts `1030` against `2040` at the receipt line's own
    `Extend` on the purchase line's pair and price unit, and moves the
    average by the formula.
  - A purchase quantity that does not convert exactly into the stocking
    unit is a 400 naming `lines[i].quantity` with the nearest exact
    quantities.
  - `order.TestSpecialOrderCostAtAverage`, the worked example of 3.4: 10
    on hand at 5.00, a special order receipt of 10 at 9.00 (average
    7.00), the order line fulfilled: the sale relieves 70.00 at `costOf`
    and `1030` ends at 70.00, on hand x average.
  - A special order received while an older backorder exists for the same
    product goes to the special order, not the older one, and only the
    remainder is available.
  - `special_order.received` is written once per linked line, and a replay
    of the receipt event allocates nothing twice.
  - A cancelled order's reservation is released and queues the next
    backorder.
  - Two concurrent receipts on two purchase lines linked to one order
    line may over reserve; the worker releases the excess and the
    reservation ends no greater than the backorder (5.2).
  - A non stock special order line is refused billing before receipt
    (`special_order_not_received`) and costs from its receipt after.
  - A direct ship receipt moves no stock and posts `1030` against `2040`;
    two receipts at different costs followed by two partial bills leave
    `1030` at zero exactly, the relief pro rata from the linked receipt
    lines' posted values with the last bill taking the remainder (3.4);
    `GET /purchase-orders/direct-ship-lines` shows the received unbilled
    line, carries the order's record link, and empties once it is billed.
  - Freight: a receipt with freight applied before it credits `2045` for
    the freight part and `2040` for the goods, a freight charge applied
    after the receipt debits `1030` (and the average) and `5050` by 3.5's
    split and credits `2045`, the goods invoice relieves `2040` pro rata
    on values net of the freight, and the carrier's approved invoice line
    naming the charge debits `2045` exactly; after both invoices `2040`
    and `2045` end at zero (3.4).
  - No purchase order line is created from a quote, and none for a priced
    line that is not a special order line (5.1).
  - Receipt against special order linking under concurrency ends without
    deadlock.
- **D.** As in the exit row.
- **F.** The 850 for two purchase orders carries distinct control numbers
  and the vendor SKUs, and a partner that has sent 999999999 documents
  wraps to 1 without a duplicate. A purchase order with an unmapped line
  is 409 `vendor_item_unmapped`. A sender failure retries and parks after
  10. A sent purchase order's payload is the approved revision even when
  it is reopened after. A bound key is refused `POST /vendor-feeds` and
  `PUT /vendor-feeds/{id}`, on its own feed included, each 403 with its
  audit row (8.1). A run posted at the `vendor_feeds.max_bytes` limit with
  an `Idempotency-Key` replays its stored answer (8.2). The A, B, A repost
  act (8.2) creates a second run from the stored body with its reason and
  audit row, and is refused for a key and after the body purge.

### 14. Where today's code contradicts this design

| Today | Where | Fixed by |
|---|---|---|
| No unique stock identity; duplicate rows per product and location | migration 001 | C4-2 A |
| Rows with a null location; the counter sells from them | `pos/service.go`, `inventory` | C4-2 A |
| An adjustment's reason is accepted and dropped; nothing records a move | `inventory/service.go` `AdjustStock` | C4-1a (reason on the event), C4-2 A and B |
| Read then write with no row lock on every stock change | `inventory/service.go` | C4-1a |
| Allocation not tied to an order; a revert restocks the first row | `inventory/service.go` `Allocate`, `RevertFulfillment` | C4-2 A |
| A receipt reads its purchase order unlocked, accepts any quantity, posts nothing | `purchase_order/service.go` `ReceivePO` | C4-1a (lock, over receipt), C4-2 C (document, posting) |
| Average cost recomputed from a `total_quantity` that already includes the receipt (the receipt counts twice in `Q`), the cost update's error dropped | `ReceivePO` (`AdjustStock` runs first; `_ =` on `UpdateAverageCost`) | C4-1a |
| Vendor statistics as `(old + new) / 2`, lead time `time.Since(CreatedAt)` at both submit and receive | `SubmitPO`, `ReceivePO` | C4-2 C |
| `SubmitPO` checks no status (it sends a `RECEIVED` order) and there is no approval | `SubmitPO` | C4-1a (status), C4-2 D |
| The automatic special order purchase order fires for any priced quote line with a product, passes the quote line id as `linked_so_line_id` (which `REFERENCES order_lines(id)`), commits an empty draft purchase order and drops the line's failure; the golden holds two empty `SPECIAL_ORDER` drafts | `triggerAutoPO` and `AutoPOService` (`quote/service.go`), `linked_so_line_id` (migration 011), `CreateFromSOLine` (`purchase_order/service.go`) | C4-2 C deletes all three; migration C cancels the empty drafts |
| The 850's control numbers always 1, item codes `UNKNOWN`, a hard coded profile | `integrations/edi/x12/850.go`, `edi/service.go` | C4-2 F |
| Catalogue import row by row, no transaction, no run, no replay guard | `edi/edi_repository.go` | C4-2 F |
| AP approve posts only the payable leg; posting errors swallowed | `ap/service.go`, `gl/service.go` `SyncVendorInvoice` | C4-1b |
| AP payments apply to unapproved invoices without locks | `ap/service.go` `PayVendor` | C4-1b |
| Matching pairs lines by position, one invoice per purchase order | `matching/service.go` | C4-2 E (C4-1a converts the routes as they are) |
| A2A creates the purchase order before its idempotency row | `purchase_order/a2a_receiver.go` | C4-1a |
| Recommendations ignore on order and assume cost as 60 percent of price | `purchase_order/recommendations.go` | C4-1a |
| `allocated` (10,4) and `quantity` (12,4) at different widths | migrations 001, 004 | C3-1's migration 093 (PR 41) |

## Alternatives considered

**Stock identity as a single row with JSON attributes.** One row per
product and location, with lots, serials and bundles in a JSON column, would
avoid new tables. It was rejected: uniqueness, serial on hand once, and
"which invoices took lot L" all become JSON queries with no index or
constraint, and allocation could not name what it holds.

**Bundles as containers in the location tree** (license plate style, a
bundle as a location node). Moving a bundle would be re-parenting one
node. It was rejected because bundles are broken, sold whole and carry lots
and tallies: they behave like identity, not like a place. A container model
would also put customer allocated stock "inside" locations that vanish when
consumed. One stock row per bundle gives the same whole bundle move by
re-pointing that row.

**Serials as their own table.** It is cleaner in name, but it doubles the
foreign keys on the stock row and the traceability reads. A serial is a lot
of one, and three constraints say so.

**Stock by length as separate products per length** (a 2x6x8, a 2x6x10,
and so on, which is how the seed models linear stock). This is simple and
already works for priced lengths. It was rejected for random length lumber,
where a bundle holds many lengths and is sold by tally. A tally on the stock
row keeps one product with many lengths, and it uses the same shape as the
sale line ADR 0006 defines. Dealers who stock fixed lengths as separate
products keep doing so; nothing here forces a tally on them.

**Cost layers (first in, first out) or specific identification for lots
and serials,** which ADR 0005 anticipated cycle 4 might build. Layers would
make every outbound move consume layers, and every return and adjustment
choose one: a layer table, a consumption table, and a cost that depends on
history order. The dealers this serves cost by moving average in common
practice, the GL already relieves at average under ADR 0005, and the
lot's receipt cost is recorded (`stock_lots.unit_cost`). Specific
identification for serials can be added later behind `costOf` without a
schema change to moves. The cost is that a serial item's margin is the
average's, not its own.

**A per product valuation row maintained by every move,** instead of the
product row locks of 3.5. It would give an exact running `Q` without
reading the stock rows, but it would put a hot row on every sale. A plain
unlocked read of the product row is not safe: a sale that reads the
average while a receipt moves it values at the old average against the new
on hand, and `1030` keeps the difference for ever (the first draft of this
record claimed otherwise, and review round 1 corrected it with a worked
example). So 3.5 and section 9 step 6b give every valuing act a `FOR
SHARE` lock on the product row and every average mover a `FOR UPDATE`
one; share locks do not queue sales behind sales, so the hot row costs
only what a receipt already cost.

**Allocating special orders inside the receipt transaction,** by locking
the order and updating its lines. The receipt would then take order locks
after the purchase order lock and inventory, the opposite of confirm's
order, which ADR 0005 rejected for back order release for the same reason.
The `RESERVED` allocation holds the stock without the order lock, and the
existing queue converts it under the right lock order.

**Direct ship as a receipt into a virtual location and an immediate
issue.** This would keep the "everything moves through stock" rule
literally, but every report and every reconciliation would have to exclude
a location that never holds anything. Direct ship lines are treated as non
stock lines instead, which ADR 0005 already costs from the linked purchase
line.

**Approval by role** (purchasing may approve, buyers may not). It was
rejected because the plan item is per user amounts, and dealers set them
per person. Role still gates who may approve at all.

**Per branch limits.** These were deferred: no dealer requirement names
them, and the table can gain `branch_id` later with a listed change.

**Letting a key approve within a limit given to the key.** It was rejected
because an approval is a human control. A key that could approve would make
the limit a property of whoever holds the key.

**Re-approval on edit instead of edit only in draft.** Allowing edits after
approval, and re-approving when the total rises, is friendlier but needs
rules for what counts as a rise (cost, quantity, new lines, currency). Edit
only in draft, with a reopen that clears approval, has one rule.

**Vendor credit memos as negative vendor invoices in one table.** That is
fewer tables, but the status vocabulary, numbering and application rules
differ, and ADR 0005 already chose a separate credit memo document for AR.
The same shape on both sides keeps one mental model.

**Matching on demand only.** It was rejected because auto approve needs the
result in the act that posts the document, and a stale result leaves an
invoice approvable that should not be.

**Feed idempotency by `Idempotency-Key` only.** Its retention is short, and
it is keyed per principal: a vendor re-sending a file after the key's
retention, or through a second sender, posts twice. The digest has no
expiry and no principal.

**A digest over normalized content.** Rejected as section 8.2 explains:
format specific rules that can make two different files collide.

**One transaction per feed run.** All or nothing is simpler to reason
about, but one bad row out of tens of thousands would hold back every good
price, and a large transaction would hold the outbox lock if it wrote
events (ADR 0003 section 2). Chunks with a committed cursor give exactly
once posting per row and visible partial results.

**Processing the run inside the HTTP request.** It is simpler for small
files, but a large file would hold a request and a connection for its whole
run, and a client timeout would leave the caller unsure whether it posted.
The 201 means "received, and will post once". The run is polled, and
`vendor_feed.run_posted` reports the end.

**A feed key scoped per feed in the scope string** (`vendor-feeds:acme:write`).
It breaks ADR 0002's rule that the scope comes from the path alone, and its
exact vocabulary would have to list every feed. A route checked binding,
like ADR 0002's cashier rule, keeps the vocabulary fixed and the binding
auditable on the feed row.

**Sending outbound documents from an events outbox subscriber.** Rejected
as section 8.6 explains: summary payloads, purge by age, and no per
document state.

**Freezing a location during a count.** This gives an exact count with no
variance arithmetic, but it stops sales from that bin for the whole count.
The snapshot rule counts while trading, at the cost of refusing the rare
post where the arithmetic would go below zero.

**Emitting `stock.low` from the act that lowers the stock.** That is
immediate, but it would add a reorder point read and an event to every
fulfilment and counter sale, which are cycle 2's money transactions, and it
would write events from inside a stock helper rather than last in the act.
The dirty marker and the job keep the money paths unchanged, at the cost of
one job pass of lag.

## Consequences

- Every unit of stock has an identity that a move, an allocation, a count
  and an invoice line can name. "Where is it", "who holds it" and "where
  did it go" become reads of one ledger.
- `1030` finally moves on receipts, adjustments, counts and vendor returns,
  in the transactions that cause them, and `2040` shows goods received but
  not yet billed. The gap ADR 0005 left (COGS relief driving `1030` below
  receipts' value) closes for new activity. History is not rewritten.
- The inventory service becomes the single writer of stock tables. The
  order, invoice and POS modules change their inventory calls once, in C4-2
  package A, after cycle 2 has merged.
- Purchasing gains a human approval step bounded by per person limits,
  with an append only history. Keys and jobs can prepare purchase orders
  but never approve them.
- Vendor files post exactly once by content, with partial results visible
  per row. Outbound documents are queued with the act that approves them
  and carry real control numbers.
- New module segments join ADR 0002's vocabulary: `purchasing-limits`,
  `vendor-returns`, `vendor-feeds` and `vendor-documents`.
- Several routes are removed and replaced (inventory adjust and transfer,
  purchase order receive and submit, AP approve, the matching run, results
  and exceptions routes), each with a CONTRACT-CHANGES row and the desk
  updated in the same pull request.

## The review round 1 decisions

The three questions this record left open are decided, as the reviewer
recommended:

1. **Direct ship billing stays manual in v1** (5.3): no automatic billing,
   a desk list of received and unbilled direct ship lines instead, and a
   later automatic path keyed on `receipt_id` in cycle 4's own files, not
   a re-keying of ADR 0005's fulfilment queue.
2. **The counter's required serial stays in package A** (2.7): effective
   only when a dealer turns serial tracking on, additive on the wire.
3. **Freight applied after receipt capitalizes the on hand share** (3.5):
   `F x min(Q, q) / q` into `1030` and the average under the product lock,
   the sold share to `5050`, 2 to 4 dev hour equivalents in package C.
