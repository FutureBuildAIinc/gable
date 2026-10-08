# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0005: the sales and money core

## Status

Proposed for the Gable v1 refactor (item C2-0, the design stop before cycle
2's money items); accepted when the lead merges it after its review. It stands on ADR 0001 (the wire contract), ADR 0003 (the
outbox) and the module recipe (`docs/refactor/MODULE-RECIPE.md`, from R1-15).
Items C2-1 to C2-5 build from this record and the recipe; where they differ
from this record, this record is changed first, in its own pull request.

## Context

Cycle 2 carries customers, orders, invoices, credit memos, payments,
deposits, the AR subledger and the counter (POS and till) onto the wire
contract, and folds in the parity the plan lists for them: currency,
COGS and will-call, typed price override and line discount, ship-to
addresses, jobs on the AR ledger, charge and text lines, kits, unapplied
cash, void and write-off, a payment terms master, and back orders.

These modules move money, so the shapes cannot be settled module by module.
Today's code shows what happens when they are: the invoice posting credits
sales tax to revenue, nothing posts cost of goods sold, a payment moves the
AR subledger but not the general ledger, a credit memo moves the subledger
and nothing else, the counter books its revenue after its own transaction
commits, an order line stores its price at scale 2, and a payment cannot
exist without an invoice. The full list, with the item that fixes each, is
section 15.

This record settles every money question first: the tables and columns, the
wire fields, the state machines and their events, the general ledger
posting of every money movement and the transaction it rides in, the AR
subledger rule, aging, currency, numbering, line types, kits, back orders and
will-call, and what each item must build and test.

## Decision

### 1. Boundaries with later cycles

| Concern | Cycle 2 does | Later |
|---|---|---|
| Units of measure | a stocked line is sold in its product's stocking unit (`products.uom_primary`); the price unit may differ through the conversion pair | cycle 3 adds unit conversions and lifts the stocking unit rule |
| Pricing engine | wraps today's engine at the order and counter boundary, converting its result to a scale 4 price once | cycle 3 makes the engine return scaled prices |
| Inventory cost | the cost basis is `products.average_unit_cost` (scale 4), read in the posting transaction | cycle 4 owns cost layers, lots and serials; it replaces the cost source behind one function (`costOf`, section 8.4) |
| Tax | one rate per document, resolved from ship-to or branch, rounded once per document | cycle 5's tax engine (GC-166) replaces the resolver; the per line `taxable` flag and the document fields stay |
| General ledger routes | GL keeps its routes; cycle 2 adds `currency` to journal entries and groups the GL reports by it | cycle 5 converts the GL module |
| Multi company | none; the series key for numbers is the entity | cycle 5 (GC-207) may widen series keys |
| Reporting module | the AR aging and statement move to new routes under `/api/v1/ar/` | cycle 5 retires the reporting module's aging and statement routes |

### 2. One line shape for every sales document

#### 2.1 Line types

Every line of an order, invoice, credit memo and counter sale has a
`line_type`. Storage is UPPERCASE text with a CHECK; the wire is lowercase
(ADR 0001 section 6).

| `line_type` | What it is | Product | Priced fields | Stock | Revenue account | Taxable | COGS |
|---|---|---|---|---|---|---|---|
| `product` | goods; with a product, a stocked item; without one, a non stock item (a special order the dealer does not carry, a quote line with no product) | optional | required | moves when a product is set | `4010` | from `products.taxable`; a non stock line takes the request's flag, default true | quantity in stocking unit x unit cost (8.4) |
| `kit` | a kit sold and priced as a whole | required, `products.is_kit` true | required | never (its components move) | `4010` | from the kit product | none on the kit line |
| `component` | one component of a kit, child of a `kit` line | required | present, unit price 0, extension 0 | moves | none | false | quantity x unit cost |
| `charge` | a fee: freight, fuel, restocking, any other | none | required | never | the charge code's account | from the charge code, overridable per line | none |
| `text` | a note on the document | none | all null | never | none | false | none |

A charge line names a `charge_code` from the charge code master (section
2.5). A text line carries only `description`, `position` and the line
identity fields; its `quantity`, `uom`, `price_uom`, `uom_qty`,
`price_uom_qty`, `unit_price_ten_thousandths` and `line_total_cents` are
`null`, present on the wire (ADR 0001 section 12). Every other line type
carries all of them non null (ADR 0001 section 7a).

#### 2.2 The line columns

Order lines, invoice lines, credit memo lines and counter lines carry the
same columns (names below are the order line's; the others are listed per
table in section 13). Quantities and conversion pair sides are
NUMERIC(12,4); unit prices NUMERIC(12,4); amounts NUMERIC(12,2) read and
written through `ROUND(col * 100)::bigint` and `$n::numeric / 100`, never
float (recipe step 6).

| Column | Type | Rule | Wire field |
|---|---|---|---|
| `id` | UUID | | `id` |
| `position` | INTEGER NOT NULL | order of lines; a kit's components follow it | `position` |
| `line_type` | TEXT NOT NULL CHECK | section 2.1 | `line_type` |
| `parent_line_id` | UUID NULL, FK same table, ON DELETE CASCADE | set on `component` only | `parent_line_id` |
| `product_id` | UUID NULL | required on `kit` and `component`; optional on `product`; null otherwise | `product_id` |
| `charge_code_id` | UUID NULL, FK `charge_codes` | required on `charge` only | `charge_code` (the code text, read); `charge_code_id` |
| `sku` | TEXT NULL | snapshot of the product's SKU | `sku` |
| `description` | TEXT NOT NULL | snapshot; required on every line | `description` |
| `quantity` | NUMERIC(12,4) NULL | null only on `text`; negative only on credit memo lines | `quantity` |
| `uom` | TEXT NULL | the sale unit; on a stocked line it equals the product's `uom_primary` (section 1) | `uom` |
| `price_uom` | TEXT NULL | the price unit; equals `uom` unless the pair says otherwise | `price_uom` |
| `uom_qty`, `price_uom_qty` | NUMERIC(12,4) NULL, both > 0 when set | the conversion pair; 1 and 1 when the units agree | `uom_qty`, `price_uom_qty` |
| `unit_price` | NUMERIC(12,4) NULL, >= 0 | the price charged per price unit, after any override, before any discount | `unit_price_ten_thousandths` |
| `priced_unit_price` | NUMERIC(12,4) NULL | what the pricing engine resolved, read only; null on `text`, `charge`, non stock lines | `priced_unit_price_ten_thousandths` |
| `price_source` | TEXT NOT NULL CHECK (`PRICE_LIST`, `QUOTE`, `OVERRIDE`, `MANUAL`, `NONE`) | 2.3 | `price_source` |
| `override_reason` | TEXT NULL | required when `price_source` is `OVERRIDE` | `override_reason` |
| `discount_percent` | NUMERIC(7,4) NULL, 0 < p <= 100 | 2.3; at most one of the two discounts | `discount_percent` (decimal string, like a quantity) |
| `discount_amount` | NUMERIC(12,2) NULL, > 0 | 2.3 | `discount_cents` |
| `discount_reason` | TEXT NULL | required with either discount | `discount_reason` |
| `price_adjusted_by` | TEXT NULL | the actor id of the last override or discount (the audit row carries the rest) | `price_adjusted_by` |
| `line_total` | NUMERIC(12,2) NULL | the extension (2.4); null only on `text` | `line_total_cents` |
| `taxable` | BOOLEAN NOT NULL | 2.1 | `taxable` |
| `revenue_account_code` | TEXT NULL | charge lines: the code's account, snapshotted at create (2.5); null otherwise (posting uses `4010`) | `revenue_account_code` |
| `is_special_order`, `vendor_id`, `special_order_cost` | existing | `special_order_cost` widens to NUMERIC(12,4), a unit cost | `is_special_order`, `vendor_id`, `special_order_unit_cost_ten_thousandths` |
| `quote_line_id` | UUID NULL, FK `quote_lines` ON DELETE SET NULL | order lines only | `quote_line_id` |
| `quantity_allocated`, `quantity_backordered`, `quantity_fulfilled` | NUMERIC(12,4) NOT NULL DEFAULT 0 | order lines only; section 5.4 | same names |
| `created_at` | TIMESTAMPTZ NOT NULL | | `created_at` |

Invoice and credit memo lines add `order_line_id` (invoice) or
`invoice_line_id` (credit memo), `unit_cost` NUMERIC(12,4) and `cost`
NUMERIC(12,2) (section 8.4), and credit memo lines add `restock` BOOLEAN.
The wire exposes `unit_cost_ten_thousandths` and `cost_cents` on invoice and
credit memo lines to the roles that see margin (`admin`, `owner`,
`finance`, `sales`), the same audience as the quote's
`unit_cost_ten_thousandths`.

#### 2.3 Price source, typed override and line discount

A product or kit line's price comes from one place, recorded in
`price_source`:

- `PRICE_LIST`: the request sends no `unit_price_ten_thousandths`; the
  server prices the line through the pricing engine for the customer and
  quantity, and stores the result as both `priced_unit_price` and
  `unit_price`.
- `QUOTE`: the line came from a quote line through conversion (5.8); the
  quote's unit price is kept exactly.
- `OVERRIDE`: the request sends `unit_price_ten_thousandths` and
  `override_reason` (1 to 500 characters). The server still prices the line
  and keeps the engine's answer in `priced_unit_price`, so the override is
  visible as the difference. A price sent without a reason is a 400
  `validation_failed` naming `lines[i].override_reason`. A price equal to
  the engine's answer is stored as `PRICE_LIST` and the reason is dropped.
- `MANUAL`: charge lines and non stock product lines, whose price is
  whatever the request sends (a charge code's `default_unit_price` fills it
  when the request sends none); no reason is required.
- `NONE`: text lines and kit components.

A discount is one of `discount_percent` (a decimal string with at most 4
fraction digits, greater than 0 and at most 100) or `discount_cents`
(positive), never both, and either requires `discount_reason`. A discount on
a `text` or `component` line is a 400.

Every override and every discount writes one `audit_log` row inside the
document's transaction (action `<entity>.line_price_overridden` or
`<entity>.line_discounted`, the line id, the before and after values, the
reason), the R1-14 rule. The line's `price_adjusted_by` keeps the last actor
for the wire.

#### 2.4 The extension

`line_total` is computed in one place, the platform package, and rounded
once, to cents, half away from zero (ADR 0001 section 7a):

- no discount: `httpx.Extend(quantity, uom_qty, price_uom_qty, unit_price)`;
- percent discount: `httpx.ExtendDiscounted(quantity, uom_qty,
  price_uom_qty, unit_price, discount_percent)`, which multiplies the exact
  product by `(1000000 - p) / 1000000` (p is the percent at scale 4) before
  the one rounding. C2-2 adds this helper to `internal/platform/httpx` as its
  own commit (recipe step 1);
- amount discount: `Extend(...) - discount_cents`, both integers, no second
  rounding. A discount larger than the extension is a 400 naming
  `lines[i].discount_cents`.

When an invoice bills part of an order line (5.6), the extension is
recomputed for the billed quantity through the same function. A percent
discount carries through unchanged. An amount discount is prorated: the
invoice line takes `round_half_away(discount_cents x billed / ordered)`, and
the invoice that bills the line's last quantity takes the remainder, so the
billed discounts sum exactly to the order line's.

#### 2.5 Charge codes

`charge_codes` (new, C2-2): `id UUID`, `code TEXT UNIQUE NOT NULL` (one to
sixteen uppercase letters, digits or underscores), `name TEXT NOT NULL`,
`revenue_account_code TEXT NOT NULL REFERENCES gl_accounts(code)`,
`taxable BOOLEAN NOT NULL`, `default_unit_price NUMERIC(12,4) NULL`,
`is_active BOOLEAN NOT NULL DEFAULT TRUE`, `revision BIGINT NOT NULL DEFAULT
1`, `created_at`, `updated_at`. Seeded: `FREIGHT` (account `4020`, not
taxable), `FUEL` (`4020`, not taxable), `RESTOCK` (`4030`, not taxable),
`ADJUST` (`4010`, not taxable; used by migrated credit memos). The seed only
inserts missing codes; the dealer sets taxability per jurisdiction. A line
snapshots the code's account into its own `revenue_account_code` column at
create, so a later edit of the code never moves posted revenue.

Account `4030 Fees and Charges Revenue` (REVENUE, CREDIT) is seeded with the
charge codes.

#### 2.6 Kits

- `products.is_kit BOOLEAN NOT NULL DEFAULT FALSE`.
- `product_kit_components` (new, C2-2): `kit_product_id UUID NOT NULL FK
  products`, `component_product_id UUID NOT NULL FK products`, `quantity
  NUMERIC(12,4) NOT NULL CHECK (quantity > 0)` in the component's stocking
  unit per one kit, `position INTEGER NOT NULL`, primary key
  `(kit_product_id, component_product_id)`, a CHECK that the two differ. A
  kit cannot contain a kit (one level; the write refuses it with a 400).
- Routes (on the contract from birth): `GET /api/v1/products/{id}/kit-components`
  and `PUT /api/v1/products/{id}/kit-components` (replaces the whole list,
  with the product's revision once products convert; until then
  last write wins, stated in the fragment).
- Explosion: when a line names a product with `is_kit`, the document stores
  a `kit` line (priced, taxable per the kit product) followed by one
  `component` line per component, `quantity = kit quantity x component
  quantity`, `uom` the component's `uom_primary`, unit price 0, extension 0,
  `price_source` `NONE`, `taxable` false. The explosion is done once, at the
  line's create or edit, so a later change of the kit definition does not
  touch existing documents.
- Stock and cost move on the components only. Allocation and fulfilment
  treat a kit as whole units: the number of kits allocatable is the minimum
  over its components of `floor(available / per kit quantity)`; components
  are allocated and fulfilled in whole kit multiples.

#### 2.7 The shared package

C2-2 creates `core/internal/salesdoc`: the line type vocabulary, the line
request parse (`ParseLines`, collecting into one `httpx.Validator`), kit
explosion (`Explode`), the extension and discount (`ExtendLine`), the totals
and tax (`Totals`, section 3) and the posting groups (`RevenueGroups`,
`CostGroups`, section 8). Orders, invoices, credit memos and the counter all
call it; no module computes a line or a total another way.

### 3. Totals and tax

On every document:

- `subtotal_cents` = sum of `line_total_cents` over non text lines;
- `taxable_cents` = sum of `line_total_cents` over lines with `taxable`
  true;
- `tax_cents` = `round_half_away(taxable_cents x tax_rate)`, once per
  document, exact (the rate is NUMERIC(9,6), read as a decimal string);
- `total_cents` = `subtotal_cents + tax_cents`.

The rate is resolved once, when the document is created (an order's rate is
refreshed on every draft edit and at confirm; an invoice's is fixed at
create):

1. the customer has an active `tax_exemptions` row: rate 0, `tax_exempt`
   true;
2. the document is a delivery and its ship-to has a `tax_rate`: that rate;
3. the branch's `locations.default_tax_rate`;
4. none configured: the act is refused, 409 `conflict` with a blocker
   `tax_rate_not_configured` naming the branch. Zero is a valid configured
   rate.

`tax_rate` columns widen to NUMERIC(9,6) (`locations.default_tax_rate`,
`invoices.tax_rate`, the new `orders.tax_rate`, ship-to `tax_rate`):
NUMERIC(7,4) and (5,4) cannot hold a rate such as 0.08875. The wire field is
`tax_rate_percent`, a decimal string with at most 4 fraction digits
(`"8.875"`), and `tax_exempt` beside it.

The order's tax is an estimate; each invoice computes its own on the lines
it bills. A credit memo that returns lines of one invoice uses that
invoice's rate.

### 4. Document numbers and currency

#### 4.1 Numbers

| Entity | Prefix | Series | Gapless | Backfill order |
|---|---|---|---|---|
| order | `SO` | `order_number_seq` | no | `(created_at, id)` |
| invoice | `IN` | counter `invoice` | yes | `(created_at, id)` |
| credit memo | `CM` | counter `credit_memo` | yes | `(created_at, id)` |
| payment | `PAY` | `payment_number_seq` | no | `(created_at, id)`, migrated deposits after payments in their own `(created_at, id)` order |
| counter sale | `POS` | `pos_transaction_number_seq` | no | `(created_at, id)` |
| counter return | `RTN` | `pos_return_number_seq` | no | `(created_at, id)` |

Gapped numbers follow ADR 0001 section 8 and the quote migration exactly
(sequence, `<entity>_next_number()` function as the column DEFAULT, Go
create path minting through `httpx.NextDocumentNumber`).

Invoices and credit memos are gapless. They are the documents a tax
authority or an auditor reads in sequence, and several jurisdictions where
dealers sell require an unbroken invoice series; a gap in them is a question
an accountant must answer, while a gap in order or payment numbers is not.
The mechanism:

- `document_counters (series TEXT PRIMARY KEY, next_value BIGINT NOT NULL)`
  (C2-3).
- `invoice_next_number()` and `credit_memo_next_number()` are SQL functions
  that run `UPDATE document_counters SET next_value = next_value + 1 WHERE
  series = '<series>' RETURNING next_value - 1` and format it. They are the
  columns' DEFAULTs, so raw SQL writers (the seed) number through the same
  counter, and a rolled back insert rolls the increment back with it.
- C2-3 adds `httpx.NextGaplessNumber(ctx, q, series, prefix, width)` for the
  Go create path, which runs the same statement through the caller's
  transaction.
- The counter row stays locked from the mint to the commit, so invoice
  creating transactions serialize from their mint. The mint is therefore
  taken late: after every other row lock of the transaction except the
  customer row and the outbox (section 11), immediately before the invoice
  insert.
- A void keeps its number; nothing deletes an invoice or credit memo.

#### 4.2 Currency

- The dealer default: `system_settings` key `currency.default`, an ISO 4217
  code, seeded `USD` when absent (C2-1). The enabled list: key
  `currency.enabled`, comma separated, seeded with the default. Only codes
  whose minor unit is 0 or 2 places are accepted (ADR 0001 section 7).
- The customer override: `customers.currency CHAR(3) NULL`; null means the
  dealer default; a set value must be in the enabled list. Changing it is
  refused (409 `conflict`, blocker `open_documents`) while the customer has
  any order not fulfilled or cancelled, any invoice or credit memo with an
  open amount, or any payment with an unapplied amount.
- The document: `currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$')`
  on orders, invoices, credit memos, payments, counter sales and journal
  entries. It is the customer's effective currency at create, copied, never
  sent by the client (a `currency` in a create body is a 400 unknown field),
  and never changes. An invoice takes its order's; a credit memo its
  invoice's or its customer's.
- Cross currency: refused in v1. An application whose payment or credit
  memo currency differs from the invoice's is a 409 `conflict` with blocker
  `currency_mismatch`. No exchange rates are stored or applied.
- The ledger: every journal entry carries `currency`; an entry's lines are
  all in it. GL reports (trial balance, profit and loss, balance sheet) and
  every AR report group by currency and never add amounts of two
  currencies. A dealer with one enabled currency sees no change.
- The card gateway is called with the document's currency, not the literal
  `USD` it receives today.
- Until C2-4 groups the GL reports by currency, `currency.enabled` refuses a
  second code (C2-1 adds the check; C2-4 removes it).

### 5. Orders

#### 5.1 The order header

`orders` gains (C2-2 migration; existing columns kept):

| Column | Rule | Wire |
|---|---|---|
| `number TEXT UNIQUE NOT NULL` | 4.1 | `number` |
| `revision BIGINT NOT NULL DEFAULT 1` | ADR 0001 section 11 | `revision` |
| `currency` | 4.2 | `currency` |
| `delivery_type TEXT NOT NULL CHECK (DELIVERY, PICKUP)` | required on create; `pickup` is will-call | `delivery_type` |
| `ship_to_id UUID NULL FK customer_ship_tos` | defaults to the customer's default ship-to on a delivery order | `ship_to_id` |
| `ship_to_snapshot JSONB NULL` | the address at confirm | `ship_to` (object or null) |
| `project_id` (exists) | the job (section 7.1) | `job_id` |
| `customer_po TEXT NULL` | required at confirm when `customers.po_required` | `customer_po` |
| `ordered_by_contact_id UUID NULL FK customer_contacts` | checked at confirm (5.3) | `ordered_by_contact_id` |
| `hold_reason TEXT NULL CHECK (CREDIT_LIMIT, MANUAL)`, `hold_note TEXT NULL` | set only in `ON_HOLD` | `hold_reason`, `hold_note` |
| `subtotal`, `tax_amount` NUMERIC(12,2) NOT NULL DEFAULT 0; `tax_rate` NUMERIC(9,6) NOT NULL DEFAULT 0; `tax_exempt BOOLEAN NOT NULL DEFAULT FALSE` | section 3 | `subtotal_cents`, `tax_cents`, `tax_rate_percent`, `tax_exempt` |
| `total_amount` widens to NUMERIC(12,2) and now includes the tax estimate | | `total_cents` |
| `confirmed_at TIMESTAMPTZ NULL` | first confirm | `confirmed_at` |
| `created_at` NOT NULL | | `created_at` |

Other wire fields: `id`, `branch_id`, `customer_id`, `customer_name`,
`quote_id`, `status`, `salesperson_id`, `scheduled_delivery_date`
(`YYYY-MM-DD`), `deposit_unapplied_cents` (the unapplied amount of payments
taken against this order, read), `updated_at`, `lines`. The list item is
`OrderSummary` (no lines), embedded in `Order` (recipe step 4). The margin
fields of today (`total_cost`, `total_margin`, `margin_percent`,
`total_commission`) stay as `_cents` fields and the percentage as a decimal
string `margin_percent`.

#### 5.2 The state machine

Storage vocabulary: `DRAFT`, `ON_HOLD`, `CONFIRMED`, `BACKORDERED`,
`FULFILLED`, `CANCELLED` (`BACKORDERED` joins the CHECK). Wire: lowercase.

`CONFIRMED`, `BACKORDERED` and `FULFILLED` are derived from the stocked line
quantities after every act that moves them (5.4):

- every non text line fully fulfilled, or closed short: `FULFILLED`;
- otherwise any line with `quantity_backordered > 0`: `BACKORDERED`;
- otherwise `CONFIRMED`.

Transitions go through `POST /api/v1/orders/{id}/transitions` with
`{"to": ..., "revision": n}` plus the fields named below (ADR 0001 section
11). Anything not in this table is 409 `invalid_state_transition`.

| From | `to` | Body extras | Guard (409 blocker codes) | Effect | Events, in order |
|---|---|---|---|---|---|
| `draft` | `confirmed` | none | `price_exposure`, `tax_rate_not_configured`, `po_required`, `contact_authority`, `empty_order` | credit check (5.3): over the limit lands `on_hold` with `hold_reason` `credit_limit`; otherwise allocate (5.4) and derive status; snapshot the ship-to; set `confirmed_at` | `order.hold`; or `order.confirmed`, then `order.backordered` when it lands `backordered` |
| `on_hold` | `confirmed` | none; roles `admin`, `owner`, `finance` | `price_exposure`, `tax_rate_not_configured` | release: skips the credit check; allocates if never allocated; derive status | `order.hold_released`, then `order.confirmed` if never confirmed before, then `order.backordered` when it lands `backordered` |
| `confirmed`, `backordered` | `on_hold` | `hold_note` required | none | keeps allocations; `hold_reason` `manual` | `order.hold` |
| `confirmed`, `backordered`, `on_hold` | `draft` | none | `has_fulfilments` | release every allocation, zero back orders | `order.reopened` |
| `draft`, `on_hold`, `confirmed`, `backordered` | `cancelled` | `reason` required | `has_fulfilments` (use close short) | release allocations, zero back orders | `order.cancelled` |
| `confirmed`, `backordered` | `fulfilled` | `reason` required | `no_fulfilments` | close short: release allocations and back orders of the unfulfilled remainder; each line's remainder is recorded closed | `order.closed_short` |

Derived moves raise their own events: `backordered` to `confirmed` after a
release on receipt (`order.backorder_released`), `confirmed` to
`backordered` after an invoice void cannot re-allocate (`order.backordered`),
a fulfilment that leaves quantity open (`order.partially_fulfilled`), a
fulfilment that completes the order (`order.fulfilled`).

Edits: `PUT /api/v1/orders/{id}` replaces header fields and lines in
`draft` only, with the revision precondition; in any other status it is 409
`conflict` with blocker `order_not_draft` (the recipe's edit rule). A line
keeps its id across edits when the request sends it. Event `order.updated`.
Create: `POST /api/v1/orders`, status `draft`, event `order.created`.

#### 5.3 Credit, PO and contact checks at confirm

- Credit exposure = the customer's `balance_due` (the AR subledger, section
  9) minus the customer's unapplied cash, plus the `total_cents` of the
  customer's other orders in `confirmed`, `backordered` or `on_hold` not yet
  invoiced (their unbilled remainder), plus this order's `total_cents`. Over
  `credit_limit` lands the order on hold in the same transaction, and the
  transition answers 200 with the order in `on_hold`: the hold is a
  committed state change with its event, not an error. A null
  `credit_limit` means no limit.
- `po_required` on the customer and no `customer_po`: 409, blocker
  `po_required`.
- `ordered_by_contact_id` set and that contact has `can_place_orders` false,
  or the order's `total_cents` exceeds the contact's `order_limit`: 409,
  blocker `contact_authority`.

#### 5.4 Allocation, back orders and release on receipt

Order line quantities hold one invariant for every stocked line (`product`
with a product, and `component`) once the order has been confirmed:

`quantity = quantity_allocated + quantity_backordered + quantity_fulfilled + closed remainder`

(the closed remainder is `quantity - allocated - backordered - fulfilled`
after a close short, and zero otherwise). Non stock product lines, kit
lines and charge lines carry `quantity_fulfilled` only.

- Confirm allocates each stocked line `min(available, quantity)` from the
  branch's inventory and records the rest as `quantity_backordered`. The
  whole order no longer fails when one line is short. Kit components
  allocate in whole kits (2.6). Lines are processed in `(product_id, line
  id)` order so two orders never lock the same inventory rows in opposite
  orders.
- Release on receipt: C2-2 makes the purchase order receive write a
  `purchase_order.received` outbox event (the event cycle 4 names; data:
  the purchase order id, branch id and the received product ids). An order
  subscriber on the drain (ADR 0003 section 4) allocates available stock of
  those products to backordered lines, oldest `confirmed_at` first, one
  order per savepoint, and derives each order's status. The handler is
  idempotent by construction: it allocates only what is still backordered
  from what is still available.
- `POST /api/v1/orders/{id}/allocate` runs the same allocation for one
  order on demand (the desk's retry), with the revision precondition;
  event `order.backorder_released` when it clears the back order.
- Special order lines are stocked lines: they land backordered at confirm
  until their purchase order is received, then release like any other.

Inventory calls take quantities as `httpx.Quantity` from C2-2 on: C2-2 adds
`AllocateQty`, `ReleaseQty`, `FulfillQty` and `RestockQty` to the inventory
service, passing decimal strings to SQL. The float64 versions stay for the
callers cycle 4 converts.

#### 5.5 Will-call and delivery

- `delivery_type` `pickup` is will-call: the customer collects at the
  branch. Its fulfilment requires `picked_up_by` (the name the person at the
  counter gave, 1 to 200 characters, stored on the invoice), and it is never
  routed: the delivery module's route and stop creation refuses a pickup
  order with 409 blocker `pickup_order` (C2-2 adds that check where
  deliveries are created).
- `delivery` orders fulfil from the desk when the load leaves, or from
  delivery completion. Delivery completion no longer builds invoices: the
  adapter in `serve.go` calls the order fulfilment for the order's allocated
  unfulfilled quantity (method `delivery`, `delivery_id` set) and does
  nothing when nothing is left to fulfil.
- Tax: a pickup order takes the branch rate, a delivery order its ship-to's
  rate when set (section 3).

#### 5.6 Fulfilment: the money moment

`POST /api/v1/orders/{id}/fulfillments`, body `{"revision": n, "lines":
[{"order_line_id": ..., "quantity": "..."}], "picked_up_by": ...,
"delivery_id": ...}`. `lines` is optional: absent means every allocated
quantity, plus every charge line not yet billed. Allowed in `confirmed` and
`backordered`. Answers 201 with the updated order (its new revision in the
body and the `ETag`) and `Location: /api/v1/invoices/{id}` naming the invoice
created; the order body lists its invoices in `invoice_ids`.

One transaction, in this order (section 11 gives the lock order):

1. lock the order row; check the revision and the status; re check the
   price exposure gate and the credit (an over limit customer is 409
   blocker `credit_limit`, no hold);
2. lock the order's payments with an unapplied amount (deposits);
3. move stock: `FulfillQty` per stocked line, in `(product_id, line id)`
   order; a quantity above the line's allocation is 409 blocker
   `exceeds_allocation`;
4. build the invoice through `salesdoc`: product, kit and component lines for
   the billed quantities (a kit bills whole kits with their components);
   charge lines in full on the first invoice that bills them; every text
   line copied; extensions per 2.4; cost per 8.4; totals and tax per
   section 3;
5. mint the invoice number (gapless), insert the invoice and its lines;
6. post: the invoice entry with its COGS legs (section 8) and the
   subledger debit, through the AR core (section 9);
7. apply the order's unapplied payments to the new invoice, oldest first, up
   to its total (section 9);
8. update the lines' `quantity_allocated` and `quantity_fulfilled`, derive
   the order status, bump the order revision;
9. write the events last: `invoice.created`, `payment.applied` and
   `invoice.partial` or `invoice.paid` when step 7 applied anything, then
   `order.partially_fulfilled` or `order.fulfilled`.

COGS posts in the same transaction as the invoice for every fulfilment,
will-call and delivery alike. An order may now have many invoices; the
"already invoiced" guard of today (`ExistsInvoiceForOrder`) is removed.

#### 5.7 Order routes after C2-2

| Route | Notes |
|---|---|
| `GET /api/v1/orders` | list envelope; filters `status`, `customer_id`, `job_id`, `ship_to_id`, `delivery_type`, `quote_id`; ordering scope `orders.created_at` |
| `POST /api/v1/orders` | create in `draft` |
| `GET /api/v1/orders/{id}` | ETag |
| `PUT /api/v1/orders/{id}` | draft edit |
| `POST /api/v1/orders/{id}/transitions` | 5.2 |
| `POST /api/v1/orders/{id}/fulfillments` | 5.6 |
| `POST /api/v1/orders/{id}/allocate` | 5.4 |
| `GET /api/v1/orders/{id}/exposure-gate`, `POST /api/v1/orders/{id}/exposure-override` | converted onto the envelope; behaviour kept |
| `POST /api/v1/orders/{id}/confirm`, `/fulfill`, `/cancel` | removed; replaced by transitions and fulfilments (listed in CONTRACT-CHANGES) |
| `GET/POST /api/v1/charge-codes`, `GET/PUT /api/v1/charge-codes/{id}` | new |
| `GET/PUT /api/v1/products/{id}/kit-components` | new |

`charge-codes` joins the ADR 0002 module vocabulary.

#### 5.8 From quote to order to invoice, without loss

`POST /api/v1/quotes/{id}/convert` (revision precondition, as today) now
accepts the quote and creates the order in one transaction, answering 201
with the order and `Location: /api/v1/orders/{id}`, instead of returning a
payload for the client to post. The quote row is locked first; a quote that
already has an order not cancelled is 409 blocker `already_converted`.
Events: `quote.accepted`, then `order.created`. This lifts R1-15's refusal of
lines whose pair is not 1 and 1: the order line carries the pair.

| Quote line | Order line | Invoice line |
|---|---|---|
| `product_id` (null) | `product_id`, `line_type` `product` (a non stock line when null; a kit line exploded when the product is a kit) | `product_id`, `line_type` |
| `sku`, `description` | same | same |
| `quantity`, `uom` | same; a stocked product whose `uom` is not its `uom_primary` is 409 blocker `unit_not_stock_unit` until cycle 3 | the billed quantity, same `uom` |
| `price_uom`, `uom_qty`, `price_uom_qty` | same | same |
| `unit_price` (scale 4) | `unit_price` and `priced_unit_price` exactly; `price_source` `QUOTE` | `unit_price` exactly |
| `line_total` | recomputed by `Extend`, equal by construction (same inputs, same function); the conversion test asserts equality | recomputed for the billed quantity; equal to the order line's on a full bill |
| `id` | `quote_line_id` | `order_line_id` |
| quote header `freight_cents` > 0 | one `charge` line with code `FREIGHT`, quantity 1 `EA`, unit price the freight, `price_source` `QUOTE` | billed on the first invoice |
| quote `job_id`, `delivery_type`, `customer_id`, `branch_id` | `project_id`, `delivery_type`, `customer_id`, `branch_id` | carried |

### 6. Invoices and credit memos

#### 6.1 The invoice header

`invoices` gains (C2-2 adds what fulfilment writes; C2-3 the rest):

| Column | Item | Rule | Wire |
|---|---|---|---|
| `currency` | C2-2 | 4.2 | `currency` |
| `delivery_type`, `picked_up_by TEXT NULL`, `delivery_id UUID NULL` | C2-2 | from the fulfilment | `delivery_type`, `picked_up_by`, `delivery_id` |
| `ship_to_id`, `ship_to_snapshot`, `project_id` | C2-2 | from the order | `ship_to_id`, `ship_to`, `job_id` |
| `tax_rate` widens to NUMERIC(9,6); `tax_exempt BOOLEAN NOT NULL DEFAULT FALSE` | C2-2 | section 3 | `tax_rate_percent`, `tax_exempt` |
| `gl_entry_id UUID NULL FK gl_journal_entries` | C2-2 | the invoice entry | `gl_entry_id` |
| `invoice_date DATE NOT NULL` | C2-2 | the branch's local date at create; backfill `created_at` in the branch time zone | `invoice_date` |
| `origin TEXT NOT NULL DEFAULT 'ORDER' CHECK (ORDER, POS)` | C2-2 | | `origin` |
| `number`, `revision` | C2-3 | 4.1 | `number`, `revision` |
| `payment_terms_id UUID NULL FK payment_terms`; `due_date` becomes DATE; `discount_due_date DATE NULL`, `discount_percent NUMERIC(7,4) NULL` | C2-3 | terms (7.2) | `payment_terms_id`, `due_date`, `discount_due_date`, `discount_percent` |
| `voided_at`, `voided_by`, `void_reason` | C2-3 | 6.2 | same names |
| `amount_open NUMERIC(12,2) NOT NULL` | C2-4 | `total - live applications`; owned by the AR core | `open_cents` |
| status CHECK: `UNPAID`, `PARTIAL`, `PAID`, `VOID`, `WRITTEN_OFF` | C2-3 | 6.2 | `status` |

Other wire fields: `id`, `branch_id`, `customer_id`, `customer_name`,
`order_id`, `subtotal_cents`, `tax_cents`, `total_cents`, `is_overdue`
(computed: open and `due_date` before the branch's today), `paid_at`,
`created_at`, `updated_at`, `lines`. `OVERDUE` stops being a stored
status: the C2-3 migration maps it to `UNPAID` or `PARTIAL` from the
payments recorded against the row, and the list filter `overdue=true`
replaces it.

#### 6.2 The invoice state machine

| From | To | Act | Events |
|---|---|---|---|
| none | `unpaid` (or `paid` when the total is 0) | created by a fulfilment, a counter sale, or nothing else in v1 | `invoice.created` (and `invoice.paid` for a zero total) |
| `unpaid`, `partial` | `partial` | an application leaves an open amount | `invoice.partial` |
| `unpaid`, `partial` | `paid` | applications close the open amount, none of them a write off | `invoice.paid` |
| `unpaid`, `partial` | `written_off` | a write off closes the open amount | `invoice.written_off` |
| `partial`, `paid`, `written_off` | `unpaid` or `partial` | an application is reversed | `invoice.reopened` (data carries the new status) |
| `unpaid` with no live application | `void` | `POST /api/v1/invoices/{id}/transitions {"to": "void", "reason": ...}`; roles `admin`, `owner`, `finance` | `invoice.voided` |

Status after an application is derived by the AR core from `amount_open`
and the live applications; it is never sent by a client. `void` is
terminal. Voiding an invoice with live applications is 409 blocker
`has_applications` (reverse them first). Voiding an invoice reverses its
whole entry (section 8), returns its billed stock to on hand, reduces the
order lines' `quantity_fulfilled`, re-runs allocation for those quantities,
and derives the order status, all in one transaction.

#### 6.3 Credit memos

`credit_memos` evolves in place (C2-3). A credit memo is the AR document for
goods returned or a price given back; a counter return produces one.

| Column | Rule | Wire |
|---|---|---|
| `number` (gapless, `CM`), `revision`, `currency`, `branch_id`, `project_id`, `ship_to_id` | | same names, `job_id` for `project_id` |
| `invoice_id` (exists) | the invoice it credits, optional | `invoice_id` |
| `pos_return_id UUID NULL` | set by a counter return | `pos_return_id` |
| `reason_code TEXT NOT NULL CHECK (RETURN, PRICE_ADJUSTMENT, DAMAGE, OTHER)`, `reason` (exists) | | `reason_code`, `reason` |
| `subtotal`, `tax_amount`, `total_amount` NUMERIC(12,2), all <= 0; `tax_rate` NUMERIC(9,6) | lines are negative (ADR 0001 section 7a), so totals are | `subtotal_cents`, `tax_cents`, `total_cents` |
| `amount_open NUMERIC(12,2)` <= 0 | the credit not yet used, owned by the AR core (C2-4) | `open_cents` |
| `gl_entry_id`, `memo_date DATE` | | same names |
| status CHECK: `DRAFT`, `OPEN`, `PARTIAL`, `APPLIED`, `VOID` | below | `status` |
| `amount` (exists) | kept, equal to `-total_amount`, for the raw readers until cycle 5 converts them | not on the wire |

`credit_memo_lines` (new): the line columns of 2.2, `credit_memo_id`,
`invoice_line_id` (optional source line), `restock BOOLEAN NOT NULL DEFAULT
FALSE`, `unit_cost`, `cost`. Quantities and extensions are negative. A line
that names an invoice line cannot credit more than that line billed less
what earlier credit memos returned (409 blocker `exceeds_billed`); its
price, pair and discount come from the invoice line.

| From | To | Act | Events |
|---|---|---|---|
| none | `draft` | `POST /api/v1/credit-memos` | `credit_memo.created` |
| `draft` | `draft` | `PUT /api/v1/credit-memos/{id}` | `credit_memo.updated` |
| `draft` | `open` | transition: posts it (section 8), restocks `restock` lines; roles `admin`, `owner`, `finance` | `credit_memo.posted` |
| `open`, `partial` | `partial`, `applied` | applications or refunds use the credit | `credit_memo.partial`, `credit_memo.applied` |
| `partial`, `applied` | `open`, `partial` | an application is reversed | `credit_memo.reopened` |
| `draft` | `void` | transition | `credit_memo.voided` |
| `open` with no live application or refund | `void` | transition: reverses its entry and its restock | `credit_memo.voided` |

The counter return path creates and posts in one act (`draft` is skipped,
events `credit_memo.created` then `credit_memo.posted`).

Credit memo routes (C2-3 builds create, read, edit, transitions; C2-4
builds applications and refunds): `GET /api/v1/credit-memos` (filters
`customer_id`, `status`, `invoice_id`, `job_id`), `POST
/api/v1/credit-memos`, `GET /api/v1/credit-memos/{id}`, `PUT
/api/v1/credit-memos/{id}`, `POST /api/v1/credit-memos/{id}/transitions`,
`POST /api/v1/credit-memos/{id}/applications`, `POST
/api/v1/credit-memos/{id}/refunds`. `GET /api/v1/credit-memos/{customerId}`
and `POST /api/v1/invoices/{id}/credit-memo` are removed (the first
collides with `{id}`; the second is `POST /credit-memos` with `invoice_id`).

Invoice routes after C2-3: `GET /api/v1/invoices` (filters `status`,
`customer_id`, `job_id`, `ship_to_id`, `order_id`, `overdue`), `GET
/api/v1/invoices/{id}`, `POST /api/v1/invoices/{id}/transitions`. C2-4 adds
`POST /api/v1/invoices/{id}/write-offs` and converts `GET
/api/v1/invoices/{id}/payments` into the invoice's applications. The
document print and email routes keep their paths and read the new columns.

### 7. Customers (C2-1)

#### 7.1 Jobs

Two job tables exist: `customer_jobs` (read by no Go code but the seed;
`quotes.job_id` points at it) and `projects` (live: the portal, orders,
quotes). The job is `projects`. C2-1's migration copies `customer_jobs`
rows into `projects` (ids kept, `status` `Active` or `Inactive` from
`is_active`), sets `quotes.project_id = COALESCE(project_id, job_id)`, drops
`quotes.job_id` and `customer_jobs`, and the quote repository reads
`project_id` for the quote's wire `job_id` (a listed change inside the quote
module). Every document column is `project_id`; every wire field is
`job_id`. Invoices (C2-2) and payments (C2-4) gain `project_id`.

#### 7.2 Payment terms master

`payment_terms` (new): `id`, `code TEXT UNIQUE NOT NULL`, `name`, `kind
TEXT NOT NULL CHECK (NET_DAYS, DAY_OF_MONTH, DUE_ON_RECEIPT)`, `net_days
INTEGER NULL` (NET_DAYS), `day_of_month INTEGER NULL CHECK 1..31`
(DAY_OF_MONTH: due on that day of the month after the invoice month,
clamped to the month's length), `discount_percent NUMERIC(7,4) NULL`,
`discount_days INTEGER NULL` (both or neither), `is_active`, `revision`,
timestamps. Seeded: `NET30`, `NET60`, `NET90` (NET_DAYS), `DUE_ON_RECEIPT`
and `COD` (DUE_ON_RECEIPT). Any other text value found in
`customers.payment_terms` or `invoices.payment_terms` is inserted as
NET_DAYS 30 with its text as code and name, so no row loses its terms.
`customers.payment_terms_id` NOT NULL (backfilled by code, default `NET30`).
An invoice snapshots `due_date`, `discount_due_date` and
`discount_percent` from its customer's terms at create. The text columns
`customers.payment_terms` and `invoices.payment_terms` are dropped by C2-3's
migration, after invoices take terms by id.

Routes: `GET/POST /api/v1/payment-terms`, `GET/PUT
/api/v1/payment-terms/{id}` (deactivate through `is_active`; a term in use
is never deleted).

#### 7.3 Ship-to addresses

`customer_ship_tos` (new): `id`, `customer_id FK ON DELETE RESTRICT`,
`code TEXT NOT NULL` (unique per customer), `name`, `line1`, `line2`,
`city`, `region`, `postal_code`, `country CHAR(2) NULL`, `phone`,
`delivery_instructions`, `tax_rate NUMERIC(9,6) NULL`, `is_default BOOLEAN`
(at most one per customer, a partial unique index), `is_active`,
`revision`, timestamps. Backfill: one default ship-to `MAIN` per customer
whose free text `address` is not empty, `line1` holding that text. Used by
orders (5.1), invoices (6.1), delivery (the stop address, cycle 5 reads it)
and tax (section 3).

Routes: `GET/POST /api/v1/customers/{id}/ship-tos`, `GET/PUT
/api/v1/ship-tos/{id}`. `ship-tos` and `payment-terms` join the ADR 0002
module vocabulary.

#### 7.4 Contacts, PO required, credit limit, currency

- `customer_contacts.can_place_orders BOOLEAN NOT NULL DEFAULT TRUE`,
  `order_limit NUMERIC(12,2) NULL` (`order_limit_cents`), `revision`.
- `customers.po_required BOOLEAN NOT NULL DEFAULT FALSE`.
- `customers.credit_limit`: today 0 means no limit; the backfill turns 0
  into NULL and the wire `credit_limit_cents` is nullable (null: no limit).
- `customers.currency` (4.2); `customers.balance_due` becomes NOT NULL
  DEFAULT 0 and is read only on the wire (`balance_cents`), written only by
  the AR core.
- Customer routes convert in place: `GET /api/v1/customers` (list
  envelope, filters `q`, `tier`, `is_active`, `salesperson_id`), `POST`,
  `GET /{id}`, new `PUT /{id}` (header edit with revision), `PATCH
  /{id}/salesperson` and `GET/PUT /{id}/escalation-policy` (revision
  required), the contacts routes, `GET /api/v1/price_levels`.
- Events: `customer.created`, `customer.updated` (header, ship-to, contact
  or terms change; data names the part changed).

### 8. General ledger postings

#### 8.1 Accounts

Postings name accounts by stable code, as `gl.resolveAccountIDs` does
today:

| Code | Name | Use | Status |
|---|---|---|---|
| `1010` | Cash | every receipt and refund (cash, check, card, ACH) | exists |
| `1020` | Accounts Receivable | invoices, credit memos, applications, write offs | exists |
| `1030` | Inventory | COGS relief and restock | exists |
| `2020` | Sales Tax Payable | tax on invoices and credit memos | exists |
| `2200` | Customer Deposits | unapplied cash, deposits included | exists |
| `4010` | Sales Revenue | product, kit and non stock lines | exists |
| `4020` | Delivery Revenue | `FREIGHT` and `FUEL` charges | exists |
| `4030` | Fees and Charges Revenue | `RESTOCK` | C2-2 seeds |
| `4050` | Sales Discounts | early pay discounts (REVENUE, normal DEBIT) | C2-4 seeds |
| `5010` | Cost of Goods Sold | COGS | exists |
| `5040` | Bad Debt Expense | write offs | C2-4 seeds |
| `5030` | Cash Over/Short | till variance | exists |

`gl_journal_entries.source` CHECK gains `CREDIT_MEMO` and `WRITE_OFF`.
`gl_journal_entries.currency` (C2-2) is set on every entry. The entry date is
the document's business date in the branch's time zone, not the server's
`time.Now()` date. An entry dated into a closed period fails the act with 409
blocker `period_closed` (the 077 trigger raises; the AR core maps it).

#### 8.2 The postings

Every row is one balanced journal entry, status `POSTED`, written inside the
transaction of the act named, through the AR core (section 9) calling
`gl.PostEntry` (C2-2 adds it: a balanced entry through the caller's
executor, refusing an entry with no transaction open). Legs with a zero
amount are left out; an entry whose legs are all zero is not written.

| Movement | Transaction | Source | Debit | Credit |
|---|---|---|---|---|
| Invoice (order fulfilment or counter sale) | the fulfilment or sale | `INVOICE` | `1020` total; `5010` cost | each revenue account its group's line totals (8.3); `2020` tax; `1030` cost |
| Credit memo posted | the credit memo's `draft` to `open` (or the counter return) | `CREDIT_MEMO` | each revenue account its group's line totals; `2020` tax; `1030` restocked cost | `1020` total; `5010` restocked cost |
| Payment received | the payment create | `PAYMENT` | `1010` amount | `1020` the part applied in the same act; `2200` the rest |
| Unapplied cash applied | the application | `PAYMENT` | `2200` amount | `1020` amount |
| Early pay discount taken | the application | `PAYMENT` | `4050` discount | `1020` discount |
| Credit memo applied to an invoice | the application | none: both sides are in `1020` | | |
| Write off | the write off | `WRITE_OFF` | `5040` amount | `1020` amount |
| Application reversed | the reversal | `REVERSAL` (reverses the application's entry) | the original's credits | the original's debits |
| Payment voided | the void | first one `REVERSAL` per live application, then `PAYMENT` | `2200` amount | `1010` amount |
| Unapplied cash refunded | the refund | `PAYMENT` | `2200` refund | `1010` refund |
| Credit memo refunded | the refund | `CREDIT_MEMO` | `1020` refund | `1010` refund |
| Invoice voided | the void | `REVERSAL`, `reverses_entry_id` the invoice entry, dated the void date | the original's credits | the original's debits |
| Credit memo voided (from `open`) | the void | `REVERSAL` of the credit memo entry | | |
| Till over or short | unchanged (`PostTillOverShort`) | `ADJUSTMENT` | | |

Card receipts debit `1010` like cash in v1; a card clearing account is a
chart of accounts follow up, as today's code notes.

#### 8.3 Revenue groups

`salesdoc.RevenueGroups` sums `line_total_cents` per revenue account:
product, kit and non stock lines to `4010`; charge lines to their line's
`revenue_account_code`. Discounts are already inside the line totals:
revenue posts net. Component and text lines post nothing.

#### 8.4 Cost

- Unit cost of a stocked line (`product` with a product, `component`) is
  `products.average_unit_cost` read inside the posting transaction
  (`costOf`, the one function cycle 4 replaces). A special order line with
  `special_order_cost` uses that instead.
- `cost = round_half_away(quantity x unit_cost)` per line, in cents, stored
  on the invoice line with `unit_cost`; the entry's COGS legs are the sum.
- A unit cost of zero or NULL posts no COGS for that line and stores 0; the
  margin read shows it. It is not an error: a missing cost must not stop a
  sale.
- A credit memo line that restocks takes its source invoice line's
  `unit_cost` (the cost that left), or `costOf` when it names no invoice
  line, and its restock legs reverse COGS at that cost.
- Kit lines, charge lines, text lines and non stock lines without a special
  order cost carry no cost.

### 9. Payments and the AR subledger

#### 9.1 The payment

`payments` evolves in place (C2-4):

| Column | Rule | Wire |
|---|---|---|
| `customer_id UUID NOT NULL FK customers` | backfilled from the invoice | `customer_id` |
| `invoice_id` | becomes NULL-able and is no longer written; applications are the truth; dropped after cycle 5 converts its raw readers | not on the wire |
| `number`, `revision`, `currency`, `branch_id` | 4.1, 4.2 | same names |
| `status TEXT NOT NULL DEFAULT 'POSTED' CHECK (POSTED, VOIDED)`, `voided_at`, `voided_by`, `void_reason` | 9.4 | `status` and the void fields |
| `received_on DATE NOT NULL` | the business date; backfill `created_at` in the branch time zone | `received_on` |
| `amount` widens to NUMERIC(12,2) | > 0 | `amount_cents` |
| `amount_unapplied NUMERIC(12,2) NOT NULL` | `amount - live cash applications - refunds`; owned by the AR core | `unapplied_cents` |
| `order_id UUID NULL FK orders` | a deposit against an order | `order_id` |
| `project_id UUID NULL` | | `job_id` |
| `method` CHECK gains `ACH`, `OTHER`; `ACCOUNT` stays for history and is refused on new payments (charging to account is not a payment) | | `method` lowercase |
| `gl_entry_id` | the receipt entry | `gl_entry_id` |
| card columns (exist) | | `card_last4`, `card_brand`, `gateway_tx_id`, `auth_code`; `token_id` never |

A deposit is a payment with `order_id` set and nothing applied: there is no
second deposit document. `customer_deposits` and
`customer_deposit_applications` are migrated into payments and applications
(section 13, C2-4) and renamed `*_legacy`, read by nothing.

#### 9.2 Applications

`ar_applications` (new, C2-4) is the one record of AR being settled:

| Column | Rule |
|---|---|
| `id UUID` | |
| `customer_id UUID NOT NULL`, `currency CHAR(3) NOT NULL` | |
| `kind TEXT NOT NULL CHECK (PAYMENT, CREDIT_MEMO, DISCOUNT, WRITE_OFF)` | |
| `payment_id UUID NULL`, `credit_memo_id UUID NULL` | `PAYMENT` and `DISCOUNT` name a payment; `CREDIT_MEMO` names a credit memo; `WRITE_OFF` names neither (CHECK) |
| `invoice_id UUID NOT NULL` | |
| `amount NUMERIC(12,2) NOT NULL CHECK (amount > 0)` | |
| `reason TEXT NULL` | required for `WRITE_OFF` |
| `applied_on DATE NOT NULL`, `applied_by TEXT` | |
| `gl_entry_id UUID NULL` | none for `CREDIT_MEMO` |
| `reversed_at`, `reversed_by`, `reversal_reason`, `reversal_gl_entry_id` | set once; a reversed application is never deleted |
| `created_at` | |

Rules, enforced by the AR core under row locks:

- an application's payment, credit memo and invoice belong to one customer
  and one currency (409 blockers `customer_mismatch`, `currency_mismatch`);
- it never exceeds the invoice's `amount_open` (409 `exceeds_open_amount`),
  the payment's `amount_unapplied` (409 `exceeds_unapplied`) or the credit
  memo's open credit (409 `exceeds_open_credit`);
- an invoice in `void` takes no application (409 `invoice_void`); a voided
  payment applies nothing;
- `DISCOUNT` is allowed only with a `PAYMENT` application in the same act,
  on or before the invoice's `discount_due_date`, and at most
  `round_half_away(invoice total x discount_percent / 100)` less earlier
  discounts (409 `discount_not_available`);
- `WRITE_OFF` needs roles `admin`, `owner` or `finance`.

Unapplied cash is a payment whose `amount_unapplied` is above zero. It is
created with no applications at all (`POST /api/v1/payments` without
`applications`), sits in `2200`, and applies later, partly, across many
invoices, through `POST /api/v1/payments/{id}/applications`.

#### 9.3 The subledger rule and the single writer

`customer_transactions` records the movements of account `1020` for one
customer, nothing else: one row per movement, `amount` signed (debit
positive), `balance_after` the running `customers.balance_due`.

| Act | Row |
|---|---|
| invoice posted | `+total`, type `INVOICE` |
| credit memo posted | `total` (negative), type `CREDIT_MEMO` |
| payment application, at receipt or later | `-amount`, type `PAYMENT` |
| discount | `-amount`, type `DISCOUNT` |
| write off | `-amount`, type `WRITE_OFF` |
| credit memo applied | none (net zero inside `1020`) |
| application reversed | the opposite of the original row, type `REVERSAL` |
| invoice voided | `-total`, type `REVERSAL` |
| credit memo voided | `-total` (positive), type `REVERSAL` |
| credit memo refunded | `+refund`, type `REFUND` |
| unapplied cash received or refunded | none: it is in `2200`, not `1020` |

`customer_transactions.type` becomes TEXT with a CHECK over `INVOICE`,
`PAYMENT`, `ADJUSTMENT`, `REFUND`, `CREDIT_MEMO`, `DISCOUNT`, `WRITE_OFF`,
`REVERSAL` (converted from the enum in C2-4's migration: a value added to an
enum cannot be used in the transaction that adds it, and the migration
runner runs each file in one). It gains `currency`, `source_kind TEXT` and
keeps `reference_id` as the source document or application id.

Invariants, true after every act and tested (section 14):

- `customers.balance_due` = sum of the customer's `customer_transactions` =
  sum of `amount_open` over the customer's invoices and credit memos, per
  currency;
- the sum of `balance_due` over all customers = the balance of `1020`, per
  currency; the sum of `amount_unapplied` over posted payments = the balance
  of `2200`, per currency;
- an invoice's `amount_open` = `total - live applications`; a credit memo's
  = `total + live applications from it + refunds`; a payment's
  `amount_unapplied` = `amount - live PAYMENT applications - refunds`.

The single writer is the AR core in `core/internal/account` (the package
that already owns the subledger). It is the only code that writes
`customer_transactions`, `customers.balance_due`, `ar_applications`,
`invoices.amount_open` and `invoices.status` after create,
`credit_memos.amount_open` and `credit_memos.status` after `draft`,
`payments.amount_unapplied` and `payments.status`, and the journal entries
of the movements in 8.2. Its functions run only inside the caller's
transaction (they refuse a context with none) and take plain values, so the
package imports `gl` and nothing of invoice, payment or order:

`PostInvoice`, `VoidInvoice`, `PostCreditMemo`, `VoidCreditMemo`,
`RecordPayment` (with optional applications), `Apply`, `Reverse`,
`VoidPayment`, `RefundPayment`, `RefundCreditMemo`, `WriteOff`.

`account.Service.PostTransaction` becomes unexported. A test in
`internal/account` fails when any Go file outside the package contains SQL
writing those tables or columns (the recipe's raw writer grep, made a gate).
`GET /api/v1/ar/reconciliation` (roles `admin`, `owner`, `finance`) reports
every customer whose subledger, document open amounts and ledger disagree,
per currency, so drift in rows written before cycle 2 is visible; nothing
repairs it silently.

#### 9.4 Payment acts and the payment state machine

| Route | From | Effect | Events |
|---|---|---|---|
| `POST /api/v1/payments` | none | records `POSTED`; applies `applications` when sent (each `{invoice_id, amount_cents, discount_cents}`) | `payment.recorded`; `payment.applied` and per invoice `invoice.partial` or `invoice.paid` when applied |
| `POST /api/v1/payments/card` | none | gateway charge outside the transaction (as today), then the same record and apply | same |
| `POST /api/v1/payments/{id}/applications` | `posted` | applies unapplied cash | `payment.applied`, per invoice `invoice.partial` or `invoice.paid` |
| `POST /api/v1/ar/applications/{id}/reverse` | any live application | reverses one application of any kind, `reason` required | `payment.unapplied` or `credit_memo.reopened` or none for a write off, then `invoice.reopened` |
| `POST /api/v1/payments/{id}/transitions {"to": "voided", "reason"}` | `posted` | reverses every live application, then the receipt (8.2); refused for `CARD` (use a refund) with 409 `card_payment`; roles `admin`, `owner`, `finance` | `payment.unapplied` per reversed application, `invoice.reopened` per invoice, `payment.voided` |
| `POST /api/v1/payments/{id}/refunds` | `posted` | refunds from `amount_unapplied` only (409 `exceeds_unapplied`); a card refund goes through the gateway before the transaction, as today | `payment.refunded` |
| `POST /api/v1/credit-memos/{id}/applications` | `open`, `partial` | applies credit | `credit_memo.partial` or `credit_memo.applied`, per invoice `invoice.partial` or `invoice.paid` |
| `POST /api/v1/credit-memos/{id}/refunds` | `open`, `partial` | pays the credit out (cash or card refund) | `credit_memo.refunded`, then the status event |
| `POST /api/v1/invoices/{id}/write-offs` | `unpaid`, `partial` | `{amount_cents, reason}` | `invoice.written_off` when it closes the invoice, else `invoice.partial` |

`payment_refunds` gains `credit_memo_id` (the refund names a payment or a
credit memo, CHECK exactly one), `method`, `gl_entry_id`, and its
`amount` stays NUMERIC(12,2). `POST /api/v1/payments/refund` is removed
(replaced by `/payments/{id}/refunds`). `POST /api/v1/payments/intent` is
converted in place. The deposit routes (`/api/v1/deposits`,
`/{id}`, `/{id}/apply`) are removed: a deposit is `POST /api/v1/payments`
with `order_id`, and its list is `GET /api/v1/payments?order_id=`.

Payment list filters: `customer_id`, `status`, `unapplied` (`true` lists
payments with unapplied cash), `order_id`, `job_id`, `method`.

A payment's revision moves on every act that changes it; an invoice's and a
credit memo's on every application or reversal that touches them, as an in
process write (no client revision, the recipe's in process rule). The
client's precondition is the revision of the document named in the path.

### 10. Aging and statements (C2-4)

- `GET /api/v1/ar/aging?group_by=customer|job|ship_to&as_of=YYYY-MM-DD&basis=due_date|invoice_date&customer_id=`:
  the list envelope, one item per group per currency, ordering scope
  `ar_aging.customer` keyed on `(customer name projection, customer id,
  group id)`. Defaults: `group_by=customer`, `as_of` the branch's today,
  `basis=due_date`.
- An item: `customer_id`, `customer_name`, `job_id`, `job_name`,
  `ship_to_id`, `ship_to_code` (the fields of coarser groupings are null),
  `currency`, `current_cents` (not yet due), `days_1_30_cents`,
  `days_31_60_cents`, `days_61_90_cents`, `over_90_cents`,
  `unapplied_cents` (open credit memos plus unapplied cash, negative),
  `total_cents` (the buckets plus `unapplied_cents`).
- An invoice's open amount as of a date is its total less the applications
  with `applied_on <= as_of` not reversed by then (the reversal's date),
  so aging can be run for a past date. Invoices created after `as_of` and
  void invoices are excluded.
- Credit memos and unapplied payments fall in the row of their own job and
  ship-to (a payment carries a job, never a ship-to; its row has a null
  ship-to).
- `GET /api/v1/ar/aging/summary` with the same parameters answers the
  bucket totals per currency (not a list).
- `GET /api/v1/ar/customers/{id}/statement?from=&to=&job_id=`: opening
  balance, every subledger row in the range (filtered to the job when
  given, through the source document's `project_id`), closing balance, and
  the open documents, per currency.
- `GET /api/v1/accounts/{id}` and `/{id}/transactions` convert in place
  (`balance_cents`, `credit_limit_cents`, `available_credit_cents`,
  `unapplied_cents`; the transactions as a list envelope).
- `ar` joins the ADR 0002 module vocabulary. The reporting module's aging
  and statement routes stay as they are until cycle 5 retires them.

### 11. Transactions and lock order

Every act above is one transaction (recipe step 7): a rolled back act leaves
no row, no ledger line, no audit row and no event. Locks are taken in this
order and never against it, which is what keeps the money paths free of
deadlocks:

1. the document named in the path (order, payment, credit memo, invoice,
   counter sale), `SELECT ... FOR UPDATE`;
2. other existing documents the act touches: the order's deposit payments,
   then invoices and credit memos in id order;
3. inventory rows, in `(product_id, inventory id)` order;
4. the gapless counter row (minting the invoice or credit memo number);
5. the customer row (the AR core's `balance_due` lock);
6. the journal entry inserts (no row locks; the entry number is a
   sequence);
7. the outbox: every event of the act, written last (ADR 0003 section 2).

No code takes a gapless number after the customer row, and no code reads or
writes through the pool while a transaction is open (the recipe's gated
saturation test proves it for every act). Payment, application and void acts
take the invoice rows they touch under step 2, so two payments racing for
one invoice serialize and the second sees the first's `amount_open`.

The card gateway is called before the transaction and its result written
inside it, as today; a database failure after a successful charge is
logged as critical for reconciliation, as today.

### 12. Events

Every event is written through the outbox in the act's transaction, last,
with `entity_type` the entity and data a small summary: `number`,
`customer_id`, `status`, `from_status` on a status change, `revision`,
`currency`, and the act's amount in cents (`total_cents`, `amount_cents`,
`open_cents`, `unapplied_cents` as fits).

| Entity | Types |
|---|---|
| order | `order.created`, `order.updated`, `order.confirmed`, `order.backordered`, `order.backorder_released`, `order.hold`, `order.hold_released`, `order.reopened`, `order.cancelled`, `order.partially_fulfilled`, `order.fulfilled`, `order.closed_short` |
| invoice | `invoice.created`, `invoice.partial`, `invoice.paid`, `invoice.written_off`, `invoice.reopened`, `invoice.voided` |
| credit memo | `credit_memo.created`, `credit_memo.updated`, `credit_memo.posted`, `credit_memo.partial`, `credit_memo.applied`, `credit_memo.reopened`, `credit_memo.refunded`, `credit_memo.voided` |
| payment | `payment.recorded`, `payment.applied`, `payment.unapplied`, `payment.refunded`, `payment.voided` |
| customer | `customer.created`, `customer.updated` |
| counter | `pos_transaction.completed`, `pos_transaction.voided`, `pos_return.completed`, `till.opened`, `till.closed` |
| purchase order | `purchase_order.received` (written by C2-2, consumed by the order subscriber) |
| quote | `quote.accepted` from the convert route (exists) |

The plan's list is covered: `order.confirmed`, `order.cancelled`,
`order.hold`, `order.hold_released`, `invoice.created`, `invoice.paid`,
`invoice.partial`, `payment.recorded`, `customer.updated`. The rest are added
here.

### 13. Migrations, in order

Each item lands one numbered migration and its down file (the number is the
next free one when the item merges `refactor/v1`; another item may take
yours). Every step is idempotent (`IF NOT EXISTS`, guarded inserts) and every
backfill reads only columns earlier steps made NOT NULL. Apply each to an
empty database and to a seeded one, and test the backfill on rows that
exist (recipe step 3).

**C2-1, `customers_wire_contract`.**
1. `customers.created_at`, `customer_contacts.created_at`: fill, NOT NULL.
   `revision` on both.
2. `system_settings`: insert `currency.default` = `USD` and
   `currency.enabled` = the default, when absent. `customers.currency
   CHAR(3) NULL` with the format CHECK.
3. `payment_terms` with the seed and the legacy text values (7.2);
   `customers.payment_terms_id`, backfill by code, default and NOT NULL.
4. `customer_ship_tos`; backfill `MAIN` from `customers.address`.
5. Contacts: `can_place_orders`, `order_limit`. Customers: `po_required`;
   `credit_limit` 0 to NULL and the default dropped; `balance_due` NULL to 0,
   NOT NULL DEFAULT 0.
6. Jobs: copy `customer_jobs` into `projects`; `quotes.project_id =
   COALESCE(project_id, job_id)`; drop `quotes.job_id`; drop
   `customer_jobs`.
7. Keyset index `customers (created_at DESC, id DESC)` and on ship-tos.

**C2-2, `orders_wire_contract`.**
1. `orders.created_at`: fill, NOT NULL; `revision`.
2. `order_number_seq`, `order_next_number()`, `number` backfill, `setval`,
   DEFAULT, NOT NULL, UNIQUE (the quote migration's shape).
3. `orders.currency`: backfill `COALESCE(customer.currency,
   currency.default)`, NOT NULL. `gl_journal_entries.currency`: backfill
   the default, NOT NULL, DEFAULT dropped after (every insert names it).
   The journal `source` CHECK gains `CREDIT_MEMO` and `WRITE_OFF`.
4. `orders.delivery_type`: backfill `DELIVERY` where a `deliveries` row or
   `scheduled_delivery_date` exists, else `PICKUP`; NOT NULL.
5. Order header columns of 5.1; `subtotal = total_amount`, `tax_amount =
   0`, `tax_rate = 0` on existing rows (historic orders carry no tax
   estimate); `total_amount` widened; status CHECK adds `BACKORDERED`.
6. `locations.default_tax_rate` widened to NUMERIC(9,6); `products.is_kit`,
   `products.taxable`; `product_kit_components`; `charge_codes` with the
   seed; account `4030`.
7. `order_lines`: the columns of 2.2. Backfill: `line_type` `PRODUCT`;
   `position` by `(created_at, id)` within the order; `description` and
   `sku` from the product; `uom` and `price_uom` from `uom_primary`; pair 1
   and 1; `price_each` renamed `unit_price` and widened (the value keeps its
   meaning: per sale unit, which is the price unit when the pair is 1 and
   1); `priced_unit_price = unit_price`; `price_source` `QUOTE` when the
   order has a `quote_id`, else `PRICE_LIST`; `line_total =
   ROUND(quantity * unit_price, 2)` (the extension today's order total used);
   `taxable` true; `quantity_allocated = quantity` on `CONFIRMED` orders
   (today's `ON_HOLD` is set before allocating, so it holds none),
   `quantity_fulfilled = quantity` on `FULFILLED`; `quantity` widened to NUMERIC(12,4) and made NULL-able with
   the per type CHECKs; `special_order_cost` widened to NUMERIC(12,4).
8. `invoice_lines`: the same columns, plus `order_line_id`, `unit_cost`,
   `cost`, `revenue_account_code`; backfill as step 7 (`cost` 0,
   `unit_cost` NULL: historic invoices posted no COGS and this does not
   invent it). `invoices`: the C2-2 rows of 6.1; `invoice_date` from
   `created_at` in the branch time zone; `currency` as step 3;
   `delivery_type` from the order or `PICKUP`; `origin` `ORDER` where
   `order_id` is set, else `POS` (counter account charges are the only
   invoices without an order today).
9. `order_lines.revenue_account_code` (charge lines); the keyset index
   `orders (created_at DESC, id DESC)`.

**C2-3, `invoices_wire_contract`.**
1. `invoices.created_at`, `credit_memos.created_at`: fill, NOT NULL;
   `revision` on both.
2. `document_counters`; series `invoice` and `credit_memo`; the two
   numbering functions.
3. `invoices.number`: backfill `IN-` in `(created_at, id)` order, counter
   set past the maximum, DEFAULT, NOT NULL, UNIQUE.
4. Status: `OVERDUE` to `PARTIAL` when payments recorded against the row
   sum above zero, else `UNPAID`; the new CHECK. `due_date` to DATE.
   `payment_terms_id` backfilled from the text, then `discount_due_date`
   and `discount_percent` from the terms; drop `invoices.payment_terms` and
   `customers.payment_terms`. Void columns.
5. `credit_memos`: the columns of 6.3. Backfill: `PENDING` to `DRAFT`;
   `VOID` stays; `APPLIED` to `APPLIED` when `invoice_id` is set (C2-4
   writes its application) and to `OPEN` when not (the old apply already
   lowered the subledger, so an open credit keeps documents and subledger
   agreeing); `total_amount = -amount`; one `charge` line per memo (code
   `ADJUST`, quantity -1 `EA`, unit price the amount, untaxed);
   `currency`, `branch_id` from the invoice or the customer's primary
   branch; `number` `CM-` in `(created_at, id)` order through the counter.
6. `credit_memo_lines`; keyset indexes on both tables.

**C2-4, `payments_and_ar`.**
1. `payments.created_at`: fill, NOT NULL; `customer_id` backfilled from the
   invoice, NOT NULL; `invoice_id` DROP NOT NULL; `revision`, `status`,
   `currency`, `branch_id`, `received_on`, `order_id`, `project_id`,
   `amount_unapplied`, `gl_entry_id`; the method CHECK; number sequence and
   backfill.
2. `ar_applications`. Backfill, per invoice in payment `(created_at, id)`
   order: each payment applies `min(amount, invoice total less earlier
   applications)`; any excess becomes the payment's `amount_unapplied`. Each
   `APPLIED` credit memo with an `invoice_id` applies its amount the same way.
3. Deposits: each `customer_deposits` row becomes a payment (same id,
   method, amount, customer, branch, reference, note, `gl_entry_id`,
   `order_id` null) numbered after the payments; each
   `customer_deposit_applications` row with an `invoice_id` becomes a
   `PAYMENT` application keeping its `gl_entry_id`; rows without one are
   applied to the customer's open invoices oldest due first, and any amount
   left becomes an `OPEN` credit memo (reason `migrated deposit
   application`, no entry: the ledger already moved). A `REFUNDED` deposit
   gets a `payment_refunds` row for its unapplied rest. Then
   `amount_unapplied` is computed; the two tables are renamed `*_legacy`.
4. `invoices.amount_open` and `credit_memos.amount_open` computed from the
   applications; invoice and credit memo statuses re-derived.
5. `customer_transactions.type` from the enum to TEXT with the CHECK;
   `currency`, `source_kind`; the enum type dropped.
6. `payment_refunds` columns; accounts `4050`, `5040`.

No migration writes a journal entry or a subledger row: history moves as
data, and the reconciliation read (9.3) shows where it never agreed.

**C2-5, `pos_wire_contract`.**
1. `pos_transactions`: `created_at` NOT NULL, `revision`, `number` (`POS`),
   `currency`, `invoice_id FK invoices`, totals widened.
2. `pos_line_items`: the line columns of 2.2 (`unit_price` widened to
   NUMERIC(12,4) in place; `uom` stays the sale unit). `pos_tenders`:
   `payment_id FK payments`.
3. `pos_returns`: `number` (`RTN`), `credit_memo_id FK credit_memos`;
   `pos_return_lines` widened like the lines.
4. The walk-in customer: a customers row (`account_number` `WALK-IN`, name
   `Walk-in`) and `system_settings` `pos.walk_in_customer_id`, when absent.

### 14. The items

Every item follows the recipe end to end (wire tests first, migration,
model, parse, repository, service, handler, contract fragment, goldens,
CONTRACT-CHANGES, desk and portal callers, Playwright), proves each live
failure the refactor inputs name for its module with a test that fails on
the base commit (recipe step 2), carries the transaction proofs (a failing
event write rolls the act back; three contenders at pool size 4; the gated
saturation test for every kind of write), and is money class: two
independent reviews. Every new route is on the contract from birth.

**C2-1: customers, contacts, ship-tos, payment terms (24 to 40).**
Builds 7.1 to 7.4 and the C2-1 migration; the currency settings and their
one-code guard. Tests: the ship-to and terms CRUD on the wire; a terms
`DAY_OF_MONTH` due date across a short month; the customer currency change
refused with an open order; credit limit null versus zero on the wire; the
jobs merge (a quote's `job_id` reads the same job after the migration); the
contact authority and PO flags stored and read; `customer.updated` per
part.

**C2-2: orders (60 to 100).** Builds sections 2, 3 and 5, the order half of
6.1, 8.2's invoice row with its COGS, `salesdoc`, `ExtendDiscounted`,
`gl.PostEntry`, the inventory `Qty` functions, the receive event and its
subscriber, the delivery adapter change and the pickup refusal, charge codes
and kit components, and lifts R1-15's convert refusal (5.8). It updates the
raw writers of `order_lines.price_each` (the portal's order create in
`portal/repository.go`, the seed and `seed/dispatch_day.go`). Until C2-4 the
invoice module posts the fulfilment's entry through `gl.PostEntry` and the
subledger through today's `PostTransaction`; C2-4 moves both calls into the
AR core. Tests:
- each line type's extension, tax and COGS treatment, one wire test per row
  of 2.1; a percent and an amount discount; an amount discount prorated
  across two partial invoices summing exactly;
- an override without a reason refused; an override stored with the
  engine's price beside it and one audit row;
- quote to order to invoice: a 187.5 `PCS` = 1 `MBF` line at 500.00 per
  `MBF` converts and bills 50000 cents with the pair intact; quote freight
  becomes a `FREIGHT` charge line;
- a kit explodes, allocates in whole kits, bills whole kits, and posts COGS
  from its components only;
- confirm with short stock lands `backordered`; a purchase order receipt
  releases it through the drain (oldest first); the replay of the receive
  event allocates nothing twice;
- the credit hold lands `on_hold` with `order.hold` and the 200; the release
  needs the role; every forbidden transition is 409;
- **exit line: a will-call order posts COGS to the GL**: a `pickup` order
  fulfilled with `picked_up_by` writes one entry with `1020` debit = total,
  `4010` and `2020` credits, `5010` debit = quantity x average cost and
  `1030` credit, in the fulfilment's transaction (a failing event write
  rolls the entry back with it);
- a delivery completion fulfils the remainder and never creates a second
  invoice for billed quantity;
- tax: ship-to rate on delivery, branch on pickup, exemption, the refusal
  without a configured rate, a 0.08875 rate stored exactly;
- concurrency: two orders confirming against the same two products in
  opposite line order finish without deadlock; three fulfilments of one
  order at pool size 4 bill each allocated unit once.

**C2-3: invoices and credit memos (40 to 64).** Builds the rest of 6.1, 6.2
except the AR acts, 6.3's documents and transitions, gapless numbering
(`httpx.NextGaplessNumber`, the counter functions), the invoice void with its
stock and order effects, the credit memo post with restock and its entry, the
jobs on invoices, and the invoice wire with lines. Its postings take the
same interim path as C2-2's until C2-4, and credit memo statuses past `open`
arrive with C2-4's applications. Tests: a rolled back
invoice create leaves no gap (the next invoice takes the number); three
concurrent fulfilments get consecutive numbers; the seed's raw insert
numbers through the counter; an invoice void reverses its entry, returns
stock and re-derives the order; void refused with live applications;
`OVERDUE` gone from the wire and the `overdue` filter working; a credit memo
cannot return more than was billed; a restocking credit memo reverses COGS at
the original cost; the portal and print readers still read invoices.

**C2-4: payments, deposits and AR (48 to 80).** Builds section 9, section 10,
8.2's payment and AR rows, the AR core as single writer and its gate test,
the deposit merge, the write off and discount, the reconciliation read, the
GL report grouping by currency (and lifts C2-1's one-currency guard), and
moves C2-2's and C2-3's postings into the AR core. Tests:
- **exit line: an unapplied payment exists without an invoice and applies
  later**: a payment with no applications posts `1010` / `2200`, shows
  `unapplied_cents`; later applied across two invoices partly, posting
  `2200` / `1020` and the subledger rows, the invoices `partial` and `paid`;
- **exit line: AR aging splits by job and ship-to**: invoices on two jobs
  and two ship-tos of one customer age into separate rows under each
  `group_by`, unapplied cash on its job's row, and the `as_of` aging of a
  past date ignores later applications;
- payment void reopens its invoices and reverses the ledger; a card payment
  void refused; a refund limited to the unapplied amount;
- write off posts `5040` / `1020` and closes the invoice `written_off`; its
  reversal reopens it;
- cross currency application refused; over application refused;
- the invariants of 9.3 asserted after every act in the service tests;
- concurrency: three payments applying to one invoice at pool size 4 never
  over apply; a payment void racing an application ends consistent;
- the migration: deposits and their applications land as payments and
  applications with the ledger untouched.

**C2-5: POS and till (30 to 50).** Builds the counter on the recipe: lines in
the shared shape (product, charge and text lines at the counter, typed
override and discount with audit, kits), cents tenders, the walk-in customer,
and a completed sale as one transaction: stock out, an invoice (`origin`
`POS`, `pickup`) posted with tax and COGS, each cash, check or card tender a
payment applied to it (the tendered amount less change), an `ACCOUNT` tender
left open on the invoice (refused for the walk-in customer, and subject to the
credit check). The post commit, best effort GL calls are removed. A void of a
completed sale, while its till session is open, is the invoice void and the
payment voids in one transaction; after the session closes it is a return. A
return is a credit memo created and posted in one act with its restock lines,
refunded in cash or card or left as account credit. Tests: a split tender sale
posts one invoice entry and one receipt per tender; the till's expected cash
equals the cash payments less change; a return restocks and reverses COGS;
the offline sync completes each sale through the same path; a sale whose
event write fails leaves no invoice, payment, stock move or entry.

The sizes are dev hour equivalents, 202 to 334 together, against the plan's
194 to 324 for R2-1 and R2-2.

### 15. Where today's code contradicts this design

| Today | Where | Fixed by |
|---|---|---|
| The invoice entry credits the whole total, tax included, to `4010`; nothing posts to `2020` | `gl.SyncInvoice`, `invoice.PostInvoiceToLedger` | C2-2 |
| No code posts cost of goods sold | the whole tree | C2-2 |
| A payment moves the subledger and never the GL; `gl.SyncPayment` has no caller | `payment.Service.ProcessPayment`, `ProcessCardPayment` | C2-4 |
| A credit memo moves the subledger only; `ApplyCreditMemo` does nothing | `invoice/service.go` | C2-3, C2-4 |
| A refund posts to the subledger as type `PAYMENT` with a positive amount and never touches the invoice | `payment.Service.RefundPayment` | C2-4 |
| Order and invoice unit prices are scale 2 (`price_each DECIMAL(10,2)`) and quantities float64 | migrations 004, 005; `order/model.go` | C2-2 |
| The quote convert payload rounds the per each price to cents | `quote.Service.Convert` (R1-15) | C2-2 |
| The credit hold writes `ON_HOLD` outside any transaction and then returns an error | `order.Service.ConfirmOrder` | C2-2 |
| The open balance sums invoice totals, ignoring partial payments; aging does the same | `invoice.SumOpenBalanceCents`, `reporting.GetARAgingReport` | C2-2 (credit check), C2-4 (aging) |
| Allocation is all or nothing; no back orders | `order.Service.ConfirmOrder`, `inventory.Allocate` | C2-2 |
| One invoice per order; delivery completion builds a second invoice path | `ExistsInvoiceForOrder`, `invoiceServiceAdapter` in `serve.go` | C2-2 |
| Tax in float, an 8.25 percent constant fallback, rate columns that cannot hold 0.08875 | `invoice.CreateInvoice`, `DefaultTaxRate`, migrations 018 and 057 | C2-2 |
| `OVERDUE` is a stored status nothing sets | migration 005, `invoice/model.go` | C2-3 |
| The counter books GL after its transaction commits, best effort; no tax leg; a split tender invoice is built on a fake product line; a void reverses no ledger entry | `pos.Service.CompleteTransaction`, `buildAccountInvoice`, `VoidTransaction` | C2-5 |
| A payment cannot exist without an invoice | `payments.invoice_id NOT NULL` | C2-4 |
| Two concurrent payments on one invoice race the status and may over apply; no invoice row lock | `payment.updateInvoiceStatus` | C2-4 |
| `ACCOUNT` accepted as a payment method | `payment/model.go` | C2-4 |
| Card charges hard code `USD` | `payment.Service`, `pos.Service` | C2-4, C2-5 |
| Two job tables; `customer_jobs` is read by nothing | migrations 003, 035 | C2-1 |
| Payment terms are free text; a credit limit of 0 means no limit | migrations 018, 003 | C2-1 |
| Journal entries take the server's date, not the branch's business date | every `gl.Sync*` | C2-2 (invoice), C2-4 (the rest) |
| Deposits are a second prepayment document beside payments | `internal/deposit` | C2-4 |

## Alternatives considered

**Line types.** One line table per kind (charges on the header, notes in a
text column) was rejected: freight on the quote header is already the
special case that every total, tax and posting has to remember. A free
`kind` with nullable everything was rejected for CHECKs per type, so a text
line with a price cannot be stored. A separate `nonstock` type was weighed
and folded into `product` with an optional product: the posting and tax
treatment are identical, only stock differs, and the product id says which.

**Discounts.** A discount folded into the unit price (rounded at scale 4)
loses the record of what was given and rounds twice; a separate discount
line or a contra revenue account (`4090`) keeps gross revenue visible but
doubles the lines and the tax arithmetic. Adopted: the discount on the line,
one rounding, revenue posted net. A dealer who wants gross revenue reporting
gets it from the lines, not the ledger.

**Currency.** Refusing any second currency in v1 contradicts the brief's
customer override. Converting at a rate captured on each document (a
functional currency ledger with realized exchange gain and loss) is the
complete answer and needs a rate source, revaluation and two more accounts,
all of which belong with multi company in cycle 5. Adopted: documents in the
customer's currency, cross currency application refused, the ledger kept per
currency and every report grouped by it. The cost is a balance sheet that
shows one section per currency until conversion exists.

**Unapplied cash: AR credit or liability.** Crediting `1020` in full at
receipt keeps one account and makes the subledger equal AR exactly, but it
puts customer credit balances inside receivables and needs a period end
reclassification. Holding the unapplied part in `2200` and moving it to `1020`
on application is how today's deposit code already books prepayments, keeps
`1020` an honest receivable, and makes a deposit a payment with an order
rather than a second document. Adopted: `2200`. The cost is one entry per
later application.

**Deposits.** Keeping `customer_deposits` as its own document beside
unapplied cash gives two names to one fact (money held for a customer before
an invoice) with two apply paths. Adopted: merged into payments.

**Credit memos: own table or a kind of invoice.** One `invoices` table with a
`kind` shares lines and numbering code, but every raw reader of `invoices`
(the portal, reporting, the dashboard, print) would start counting credit
memos as invoices before those modules convert. Adopted: `credit_memos`
evolves in place with its own lines table of the same columns, sharing Go
code through `salesdoc`.

**Credit memo signs.** Positive amounts with the kind giving direction is
simpler to read but makes every sum over AR documents branch on kind. ADR
0001 section 7a already allows negative quantities on credit lines; adopted:
credit memo lines, totals and open amounts are negative, and a plain sum is
the AR effect.

**Gapless invoices.** A sequence (gaps on rollback) costs nothing at run
time and is what ADR 0001 gives quotes and orders. A counter row costs a
serialization from the mint to the commit. Adopted for invoices and credit
memos because an unbroken series is what tax authorities in several dealer
jurisdictions expect and what an auditor asks about first; the mint is taken
late (section 11) to keep the hold short. Orders, payments and counter
transactions keep sequences.

**Where COGS posts.** At confirm (allocation) is too early: nothing has left
the yard. At delivery completion misses will-call and splits the sale from
its cost across two transactions. Adopted: at fulfilment, in the invoice's
own entry, the one moment stock leaves and revenue is earned.

**Credit hold as an error or a state.** Refusing the confirm with a 409 and
leaving the order in draft loses the fact that a person tried to release it
and makes the hold invisible to the queue that works holds. Adopted: the
confirm commits the hold with its event and answers 200 with the held order.

**Back order release.** Allocating inside the purchase order receive
transaction couples receiving to every open order and takes inventory locks
before order locks, the opposite of confirm's order, inviting deadlock. A
desk only button leaves stock idle until someone presses it. Adopted: the
receive writes an event, an order subscriber allocates in its own
transaction in confirm's lock order, and the desk keeps a retry route.

**Quote conversion.** Keeping the payload and letting the client post it
leaves a window where the quote is accepted and no order exists, and lets a
retry create two orders. Adopted: accept and create in one transaction.

**Where the AR single writer lives.** A new `ar` package beside `account`
would split one ledger across two packages. Adopted: `internal/account`,
which already owns `customer_transactions`, becomes the AR core; the `ar`
route prefix is its new surface.

**The job entity.** Keeping both `customer_jobs` and `projects` leaves every
AR report choosing one. `projects` is the live one; adopted.

**Tax rate fallback.** Keeping the 8.25 percent constant silently charges a
rate nobody configured; falling back to zero silently charges none. Adopted:
refuse until the branch (or ship-to) has a rate, zero included.

**Payment void for card payments.** Allowing a void of a settled card charge
would reverse the books while the money stays with the customer's bank.
Adopted: card money comes back only through a gateway refund.

## Consequences

- One line shape, one extension, one tax rule and one posting table cover
  orders, invoices, credit memos and the counter; a client reads one line
  shape everywhere and the ledger is written from one place.
- The general ledger finally carries sales tax payable, cost of goods sold,
  customer payments, credit memos, write offs and discounts, inside the
  transactions that cause them. History written before cycle 2 is not
  rewritten; the reconciliation read shows where it never agreed.
- Several routes are removed and replaced (the order confirm, fulfil and
  cancel routes; the deposit routes; the payment refund route; two credit
  memo routes): each is a CONTRACT-CHANGES row in its item, and the desk is
  updated in the same pull request.
- Invoice creation serializes on the counter row from its mint to its
  commit. At dealer volume this is invisible; a counter with many registers
  at peak is where it would first show, and the escape is a series per
  branch (a listed contract change), not a sequence.
- A second currency becomes possible only after C2-4, and then without
  conversion; reports show each currency on its own.
- The stocking unit rule refuses some quote conversions that today's code
  would have mispriced or mis-stocked silently, until cycle 3's units land.
- Cycle 4 replaces one function to change the cost basis; cycle 5 replaces
  the tax rate resolver and retires the reporting module's aging.
