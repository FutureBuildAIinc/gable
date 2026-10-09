# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0010: multi company within one database

## Status

Proposed for the Gable v1 refactor (item C5-3's design stop; the build items
are section 11). It becomes accepted when the lead merges it after review.

It stands on ADR 0001 (the wire contract), ADR 0002 (machine keys), ADR 0003
(the outbox), ADR 0005 (the sales and money core), ADR 0006 (units and
pricing), ADR 0007 (drafts, links and confirm gated scopes) and ADR 0008
(inventory identity and vendor intake). ADR 0009 (finer admin and users
scopes) is in flight on `refactor/c5-1a-admin` (PR 49); this record takes
the next free number after it, which is why a design record carries 0010,
and its `companies` scope names (section 6) depend on ADR 0009's grammar
keeping ADR 0002's plain rule for modules outside the admin segment,
which ADR 0009's own section 2 states.

No merged record is superseded. Two listed contract changes extend merged
records by their own rules: the gapless counter key of ADR 0005 section 4.1
becomes per company, and the number patterns of ADR 0007 section 7 carry the
company code. ADR 0005 section 4.2 (currency) is unchanged by this record, as
ADR 0005's Status requires: an accepted record is changed only by a
superseding one, and nothing here needs to change it.

This record replaces the first draft of ADR 0010 in full, on the round 1
review's instruction. What that draft got right is kept: a company is the
legal entity that owns the books; a branch belongs to exactly one company;
parties, products and units are shared; a branch move is refused in v1; the
inter company sale to an outside customer is refused.

## Context

Today's tenancy, with file and line:

- One database per dealer. ADR 0003 section 1 stamps every event with the
  deployment's org slug because "Gable today is one database per dealer with
  no org identity in the schema"; the org is not a row anywhere.
- Branches are `locations` rows: type `BRANCH` with a null parent
  (`core/internal/location/model.go:20`, the type list at `:17-27`). Every
  non branch row (zone, aisle, rack, shelf, bin, yard) carries a denormalized
  `branch_id` kept by a database trigger (`model.go:47`;
  `core/migrations/057_branches_on_locations.sql:21`,
  `core/migrations/058_locations_branch_denorm_trigger.sql`). A branch
  carries the branch level tax facts: `tax_jurisdiction_code` and
  `default_tax_rate` (`model.go:43,44`), and a timezone (`model.go:45`).
- Grants are rows of `user_locations`, keyed by the JWT subject; there is no
  users table (`core/migrations/061_user_locations.sql:5,10-17`). The branch
  middleware reads `X-Branch-Id`, checks it against those grants, honours the
  `multi_branch_enabled` kill switch and `default_branch_required`
  (`core/pkg/middleware/branch.go:105-110,168,281`), and `ResolveBranchForWrite`
  stamps writes with the context branch or the default branch
  (`branch.go:68-97`). PR 37 ("Security: a payload branch must be within the
  caller's branch grants") added the payload guard
  `BranchGuard.CheckPayloadBranch` (`core/pkg/middleware/branch_payload.go:51`),
  PR 39 ("Security: the branch wall on path ids (PO receive, location reads)")
  extended the same guard to path ids
  (`core/internal/purchase_order/handler.go:34,35,67,233`, wired in
  `core/internal/app/serve/wire_branch_wall.go:36-37`), and PR 44 ("Security:
  lists without a branch header are held to the caller's grants") added the
  list rule (`branch.go:44-66`).
- The books have no branch at all: `gl_accounts`, `gl_fiscal_periods`,
  `gl_journal_entries` and `gl_journal_lines`
  (`core/migrations/025_general_ledger.sql:8,28,42,60`), `payments`
  (`008_payments_and_till.sql:9`), `credit_memos`
  (`018_financial_features.sql:14`), `vendor_invoices` and `ap_payments`
  (`028_accounts_payable.sql:7,36`), `bank_accounts`,
  `reconciliation_sessions` and `bank_transactions`
  (`029_matching_and_bankrecon.sql:65,75,95`). `git grep branch_id
  core/migrations` finds branch columns only on the sales, stock and counter
  documents (`062_orders_branch.sql:7`, `063_quotes_branch.sql:6`,
  `064_invoices_branch.sql:7`, `065_po_branch.sql:7,21`,
  `066_pos_branch.sql:13,23`, `067_customers_branch.sql:9,22`,
  `075_till_sessions_pos_payments.sql:21`, `078_till_z_reports.sql:15`,
  `079_pos_returns_customer_deposits.sql:42,79`, and
  `089_events_outbox.sql:64`), several of them nullable (`075:21`,
  `078:15`, `079:42`, `079:79`, `089:64`).
- One chart of accounts for the database: `gl_accounts.code` is `UNIQUE`
  (`025:10`), the standard chart is seeded once (`025:76`), and every posting
  resolves accounts by stable code through `gl.resolveAccountIDs`, which
  loads the whole chart (`core/internal/gl/service.go:49-71`; the posting
  family `SyncInvoice` to `SyncVendorPayment` at `service.go:426-769` and
  `PostEntry` at `core/internal/gl/postentry.go:67,92`).
- One fiscal calendar: `gl_fiscal_periods` (`025:28`) with the non overlap
  exclusion and the closed period trigger of
  `077_gl_reversal_period_hardening.sql` (`:29-33,43-60`).
- One tax company for the provider: `AVALARA_COMPANY_CODE` is one process
  setting (`core/internal/config/config.go:36,150`), stamped on every
  provider call (`core/internal/tax/avalara.go:107`) and held by the tax
  service at construction (`core/internal/tax/service.go:36,43,111`; wired at
  `core/internal/app/serve/serve.go:473-483`).
- One dealer default currency: `system_settings` key `currency.default`,
  seeded `USD` (`core/migrations/091_customers_wire_contract.sql:36`), inside
  ADR 0005 section 4.2's chain (customer override, else this default).
- GL, AP and bank routes take no branch or company at all today
  (`core/internal/gl/handler.go:39-61`, `core/internal/ap/handler.go:38-48`,
  `core/internal/bankrecon/handler.go:37-50`).

The requirement is the plan's GC-207: a dealer may run several legal entities
under one operation. Nothing in the repository says a dealer today really
does; this record assumes it can, and keeps the single company deployment
byte for byte in behaviour wherever it can. Two scoping statements follow
from that: the migration of section 10 puts all of today's books into one
company, and splitting an existing ledger that already mixes two legal
entities into two companies is out of scope for v1 (history would have to be
restated per company; no migration may invent it).

## Decision

### 1. What a company is, and the invariant

A company is the legal entity that owns its books: its chart of accounts,
its journal, its AR and AP subledgers, its gapless document series, its tax
registration with the provider, and its bank accounts. It does not hold cash
of its own; cash sits in a bank account, and each bank account belongs to
exactly one company.

The table `company`:

- `id UUID PRIMARY KEY`
- `code TEXT NOT NULL UNIQUE` (short, uppercase, chosen by the dealer; it
  appears in the gapless prefixes of companies created after the migration
  and is frozen once the company has issued a gapless number, section 4)
- `name TEXT NOT NULL`
- `functional_currency CHAR(3) NOT NULL` (section 5)
- `fiscal_year_start_month SMALLINT NOT NULL DEFAULT 1` (drives the period
  rows seeded when a company is created, section 3)
- `tax_company_code TEXT NULL` (the provider's company code, section 7; null
  falls back to the process setting)
- `created_at`, `updated_at TIMESTAMPTZ NOT NULL`

No timezone column: branches already carry one (`model.go:45`) and ADR 0005
section 8.1 dates each entry in the branch's time zone, which stays the rule.
No country code and no reporting currency: both have no reader in v1 (no FX,
section 9).

The invariant: every branch is a `locations` row of type `BRANCH`, and every
branch belongs to exactly one company. `locations` gains `company_id UUID
NOT NULL REFERENCES company(id)` on every row (branch rows directly; every
other row takes its branch's company, which is its company by construction,
because a non branch row never leaves its branch's subtree,
`058_locations_branch_denorm_trigger.sql`). The company of a branch is set at create by its route (section 6) and
never moves in v1: an update that would change a location
row's company is refused (section 9).

### 2. The table, by module

Every table gets exactly one named rule, stated against the schema as it
stands when the build runs (after C2-5 and C4-2), not as it stands today;
where a column the rule leans on is not present yet, the row names the
cycle that adds it. Six rules, a closed vocabulary the census test of
section 10 reads:

- **R1, follows its parent.** The row carries a NOT NULL parent that already
  names a company (a `branch_id` column set NOT NULL; a bank account for
  the bank tables). A `BEFORE INSERT OR UPDATE` trigger sets `company_id`
  from that parent and refuses a row whose parent moved to another
  company. Application code never writes `company_id` here. A trigger, not
  a generated column: a Postgres generated column cannot read another
  table.
- **R2, set by the single writer and checked.** The books carry no branch
  (`025`, `028`, `029`, above), so no trigger can reach them. The owning
  service sets `company_id` explicitly inside the act's transaction, from
  the document's company, with the foreign key to `company(id)` and a
  database check that the row's company equals its source document's
  company (a composite foreign key where the shapes allow it, section 3; a
  checked read where they do not). A row whose source document is in
  another company is never written.
- **R3, shared.** No `company_id` column at all.
- **child.** No column: a named parent row is the boundary, and every read
  that needs a company joins that parent. Line tables are children of
  their header; a stock row's children name the stock row. This keeps the
  column count down and the company invariant in one place per parent.
- **legacy.** Renamed `*_legacy` by a later cycle and read by nothing: no
  column and no rule while the rows wait for their drop.
- **key.** `api_keys` alone: the one table that gains a NULLABLE
  `company_id` (section 6), so it is none of the other five.

Child line tables (`order_lines`, `invoice_lines`, `credit_memo_lines`,
`pos_line_items`, `pos_tenders`, `vendor_invoice_lines`, `gl_journal_lines`,
a transfer's, adjustment's or count's lines, draft payloads) are children
of their headers.

| Module | Table | Rule | Source or note |
|---|---|---|---|
| locations | `locations` | R1 | `company_id` on every row; branch rows are the anchor, others take their branch's (`057`, `058`) |
| sales | `orders` | R1 | `062:7` (`branch_id` NOT NULL from `062:13`) |
| sales | `quotes` | R1 | `063:6` |
| sales | `invoices` | R1 | `064:7` |
| sales | `payments` | R1 | `branch_id` is added by C2-4 (ADR 0005 9.1 at `0005:1067`, migration step at `0005:1484`, backfilled from the invoice or the receipt branch); the build sets it NOT NULL, then the standard trigger |
| sales | `credit_memos` | R1 | `branch_id` is added by C2-3 (ADR 0005 6.3 at `0005:821`, backfilled from the invoice or the customer's primary branch at `0005:1474`, nullable on arrival); the build sets it NOT NULL, then the standard trigger |
| sales (C2-4, ADR 0005 9.2) | `ar_applications` | R2 | child rows of a payment or credit memo and an invoice; the AR core checks both sides name one company and refuses the act when they do not, the R2 check beside the two R1 parents |
| sales (C2-4, ADR 0005 9.3) | `customer_transactions` | R2 | the AR subledger row; from its invoice, credit memo or payment; the balance invariant of ADR 0005 9.3 holds per company |
| sales (C2-4) | `payment_refunds` | R2 | from its payment or credit memo |
| sales (legacy) | `customer_deposits`, `customer_deposit_applications` | legacy | renamed `*_legacy` by C2-4 (ADR 0005 9.1); no reader, waiting for the drop |
| counter | `pos_registers`, `pos_transactions` | R1 | `066:13,23` |
| counter | `till_sessions`, `till_z_reports` | R2 | `branch_id` nullable today (`075:21`, `078:15`); the writer sets the company from the register's branch at open and close; backfilled by section 10 |
| counter | `pos_returns` | R2 | `079:42`; from the sale it returns |
| purchasing | `purchase_orders`, `po_receipts` | R1 | `065:7,21` |
| purchasing | `po_freight_charges` | child | of the purchase order it belongs to |
| AP | `vendor_invoices` | R2 | no branch (`028:7`); from the purchase order it buys against, else the request's company |
| AP | `ap_payments`, `ap_payment_applications` | R2 | `028:36`; from the invoices they pay, checked one company per act |
| AP (C4, ADR 0008 7.2) | `vendor_credit_memos` | R2 | from the vendor return or invoice it credits |
| GL | `gl_accounts` | R2 | section 3 |
| GL | `gl_journal_entries` | R2 | section 3; `PostEntry` sets it |
| GL | `gl_fiscal_periods` | R2 | section 3; per company calendar |
| bank | `bank_accounts` | R2 | created naming its company; its `gl_account_id` must be of the same company (`029:70`, section 3) |
| bank | `reconciliation_sessions`, `bank_transactions` | R1 | parent is the bank account (`029:75,95`); trigger from `bank_accounts.company_id` |
| parties | `customers`, `customer_ship_tos`, `customer_contacts`, `vendors` | R3 | shared masters; per company facts below |
| parties | `customer_branches` | R3 | `067:22`; a shared "trades at" fact |
| catalog | `products`, `product_kit_components`, `product_categories`, PIM tables | R3 | shared |
| catalog | `charge_codes` | R3 | `092:235`; the code FK question is settled in section 3 |
| pricing (ADR 0006) | `product_units`, `price_levels`, `pricing_rules`, `category_pricing_rules`, `vendor_price_levels`, `vendor_product_costs` | R3 | shared masters (ADR 0006 sections 3 and 5); a row that names a branch (ADR 0006's branch specific prices) follows that branch's company, read through the join, no column |
| stock (ADR 0008 2.5) | `inventory` | child | no column, the one deliberate departure from "NOT NULL branch means R1": the boundary is its location, which is R1, and `branch_id` is NOT NULL on the row from C4-2 (ADR 0008 2.5), so the company is one indexed join away (`locations.company_id`, as `002_add_locations.sql:27-30` places stock at a location). A column and trigger on the hottest table would tax every stock write for a fact no reader needs stored (stock reads scope by branch, and value per company joins locations anyway); the census file carries this departure with this reason |
| stock (ADR 0008 2.3, 2.4) | `stock_lots`, `stock_bundles` | R3 | product keyed identity masters with no location (`UNIQUE (product_id, kind, code)`, a unique tag; ADR 0008 2.3 and 2.4); a lot's or bundle's stock sits in `inventory` rows, which carry the company through their location |
| stock (ADR 0008 2.4) | `inventory_tally` | child | of its stock row (`inventory_id`, ADR 0008 2.4) |
| stock (ADR 0008 2.6, 2.8, 3.3, 2.9) | `stock_moves`, `stock_transfers`, `stock_adjustments`, `stock_counts` | R1 | each carries `branch_id` (ADR 0008 2.6, 2.8, 3.3, 2.9) |
| stock (ADR 0008 2.7) | `stock_allocations` | child | keyed by its stock row and its order line (ADR 0008 2.7 at `0008:463`); the stock row carries the company through its location |
| stock (ADR 0008) | `adjustment_reasons` | R3 | shared master naming an account code (ADR 0008 3.1); resolved at posting, section 3 |
| feeds (ADR 0008 8.1 to 8.4) | `vendor_feeds`, `vendor_feed_runs`, `vendor_feed_files`, `vendor_feed_run_rows` | R3 | no feed kind writes a company owned table: none creates a product or changes the dealer's stock (ADR 0008 8.4); they write the shared vendor catalog, cost and availability tables, and the run is the audit of that |
| feeds (ADR 0008 8.6) | `vendor_document_outbox` | child | the boundary is the document it sends: `document_kind` and `document_id` name the purchase order or vendor return, each of which carries its company |
| delivery | `deliveries`, routes, stops | child | of the orders and trucks they serve; a delivery's company is its order's |
| users | `user_locations` | R3 | the grant table stays exactly as it is (`061:10-17`); a user's companies are derived: a user reaches a company exactly when a granted branch is of it. No `company_id` column and no new index |
| users | `module_grants` | R3 | shared |
| keys | `api_keys` | key | gains a nullable `company_id`, section 6; the one nullable case in the schema |
| drafts (ADR 0007) | `drafts` | R1 | the branch is fixed at create (ADR 0007 2.3), so the draft follows it when C5-2a builds the table |
| events | `events_outbox` | R3 | no company column in v1; consumers derive it from `branch_id` (ADR 0003 section 1), which every branch carrying act stamps; a null `branch_id` event (`089:64`) names no company, and a consumer that needs one reads the entity. Stated as a known limit |
| settings | `system_settings` | R3 | one row set per database stays. What varies per legal entity moves to the company row: `functional_currency` (section 5) and `tax_company_code` (section 7). Settings that name one branch (`default_branch_id`, `brain_inbound_branch_id`, `pos.walk_in_customer_id`) or gate the process (`multi_branch_enabled`, `default_branch_required`, `currency.enabled`) stay database level, each listed here so the ruling is on the record |

Shared parties carry facts a company would own. The v1 ruling: the master is
shared and its terms apply to every company alike, stated as a known limit;
per company terms are a later item (a `customer_company_terms` child row
keyed `(customer_id, company_id)` carrying credit limit, payment terms,
credit hold, currency override and PO required; the same shape for vendor
terms and tax reporting). The facts in question, all on the shared row
today: `customers.credit_limit`, `customers.payment_terms_id`,
`customers.currency`, `customers.po_required` (ADR 0005 sections 5.3 and
7.4), `customers.primary_branch_id` and `customer_branches` (`067:9,22`);
`vendors.payment_terms` (`020_create_vendors.sql:16`). Balances stay per
company by construction, because every document carries its company:
`customers.balance_due` remains the running sum the AR core keeps (ADR 0005
9.3), one number in the customer's one effective currency across companies
(section 5), and every AR read that names a company filters documents by it.

### 3. The GL per company

- **Chart.** `gl_accounts` gains `company_id UUID NOT NULL`; `UNIQUE (code)`
  (`025:10`) becomes `UNIQUE (company_id, code)`. The standard chart seeded
  once (`025:76`) becomes the seed company's chart. A company created later
  gets a copy of a named template company's whole live chart in its
  creation transaction (the seed company by default), not a replay of the
  025 INSERT list: 025 seeds only the original chart, later records add
  codes to it (`4030` by C2-2, `4050` and `5040` by C2-4, ADR 0008 3.2's
  accounts by the C4 packages), and dealers add accounts their charge codes
  name, all of which the INSERT list would miss, so `resolveAccountIDs`
  would fail a fresh company on the first restocking fee, write off or
  coded adjustment. The copy carries dealer accounts with the standard
  ones, so `resolveAccountIDs` never fails on a fresh company.
  `gl_accounts.parent_id` (`025:14`) must stay inside one company, checked
  in the account write.
- **Foreign keys that name accounts.** `gl_journal_lines.account_id`
  (`025:63`) and `bank_accounts.gl_account_id` (`029:70`) become composite
  foreign keys: `UNIQUE (company_id, id)` is added to `gl_accounts`, and the
  two reference `(company_id, account_id)` and `(company_id,
  gl_account_id)`, so the database itself refuses a line or a bank account
  pointing into another company's chart. `charge_codes.revenue_account_code`
  (`092:239`) and, when it lands, `adjustment_reasons.gl_account_code`
  (ADR 0008 3.1) lose their database FK to `gl_accounts(code)`: a shared
  table cannot hold a foreign key into every company's chart. Both are
  stable code references, resolved at posting through the document's
  company's chart, exactly as ADR 0005 section 8.1 resolves posting codes
  and section 2.5 snapshots the code on the line. With the database FK
  gone, the write paths hold the reference instead: a charge code write
  and an adjustment reason write check that the named code exists in every
  company's chart (a shared row must resolve in every company), and
  deactivating a `gl_accounts` row a charge code or an adjustment reason
  names is refused with 409 blocker `account_in_use`.
- **The resolver.** `resolveAccountIDs(ctx, codes...)` (`service.go:53`)
  becomes `resolveAccountIDs(ctx, companyID, codes...)`: it loads one
  company's chart and fails the act when a code is missing there, as it
  fails today. `PostingInput` gains `CompanyID` (`postentry.go:51-60`), and
  every posting path names its company: the `Sync*` family
  (`service.go:426-769`) from its document, and the AR core's calls (ADR
  0005 8.2) from the document the entry belongs to. An entry's legs are all
  of one company by construction, because the resolver takes one company id
  per entry.
- **Entries.** `gl_journal_entries.company_id NOT NULL`, set by `PostEntry`,
  never by raw SQL (the AR core gate of ADR 0005 9.3 covers the writers).
  `entry_number` stays one `SERIAL` across the database (`025:43`): it is an
  internal ordering key with no legal reader, cross database uniqueness
  holds, and per company numbering of entries would buy nothing. Stated.
- **Fiscal periods.** `gl_fiscal_periods.company_id NOT NULL`; the seed
  calendar rows become the seed company's. The 077 constraints become per
  company: the non overlap exclusion over `(company_id, daterange)` and the
  closed period trigger checking periods of `NEW.company_id`
  (`077:29-33,43-60`). A company's periods are seeded from its
  `fiscal_year_start_month` when it is created. Closing a period closes it
  for that company only, which is the accounting question a calendar answers.
- **Postings unchanged.** Every entry keeps exactly ADR 0005 section 8.2's
  shape and ADR 0008 section 3.4's shape; only the company plumbing changes.
  A single entry never spans two companies: it could not balance per
  company, and the per company trial balance would be wrong (section 9).

### 4. Numbering

ADR 0005 section 4.1 gives invoices and credit memos gapless numbers from
`document_counters (series TEXT PRIMARY KEY, next_value)` with one series
`invoice` and one `credit_memo` (built by C2-3, in review as PR 46), and the
other documents gapped numbers from Postgres sequences; its named escape,
if a dealer ever reaches the mint's ceiling, is a series per branch with the
branch code in the prefix.

The gapless counter key becomes per company, keyed by the company's id:
series `invoice:<company uuid>` and `credit_memo:<company uuid>`. The key
is the id, never the code, because the code is dealer editable and a
rename after the first number would either restart the series at 1 under a
new key or orphan the old one, breaking the legal entity's unbroken
series; `company.code` is frozen once the company has issued a gapless
number, an update that would change it refused with 409 blocker
`numbers_issued` (section 1). The prefix carries the company code of
companies created after the migration, `IN-<code>-000001` and
`CM-<code>-000001`. The reason gaplessness exists is the tax authority,
and the tax authority is per legal entity (ADR 0005 section 4.1); two
companies must not both issue `IN-000001`. This is the widening ADR 0005
section 1's boundary row reserved for cycle 5 ("Multi company: none; the
series key for numbers is the entity; cycle 5 may widen series keys").

`number` stays `UNIQUE NOT NULL` across the database. ADR 0007 section 7
serves record URLs by number only under that constraint, and its parser
reads the entity's number pattern; the invoice and credit memo patterns
widen to carry the company code (`^IN-[A-Z0-9]+-[0-9]{6,}$` shaped), which
is a listed contract change against ADR 0007 section 7's pattern table and
the openapi fragments. Both spellings of a company's numbers parse, so
existing rows keep working.

Backfill ruling: the seed company keeps everything it has. Its series rows
keep the names C2-3 builds (`invoice`, `credit_memo`), its counter
positions are untouched (`next_value` moves only as numbers issue), and
its numbers keep the bare forms `IN-000001` and `CM-000001` before and
after the item lands, so no existing deployment's next number changes
form and an auditor reading the series sees no format change mid period.
Nothing is renamed and no counter restarts. A company created after the
migration starts its own series at 1 under its own key and code bearing
prefix; no collision is possible, because the code bearing form is a
different string.

Gapped sequences (orders `SO`, payments `PAY`, counter sales `POS`,
counter returns `RTN`, and ADR 0008's transfers, adjustments and counts)
stay shared across companies: gaps are allowed there by ADR 0005 section
4.1's own table, cross database uniqueness holds, and interleaved internal
numbers across companies cost nothing. Stated, so the next record does not
re litigate it. The mint stays where ADR 0005 section 4.1 and section 11
put it (lock order step 8); only the series key and the format change.

### 5. Currency

ADR 0005 section 4.2 is unchanged, every link of it: a document's currency
is its customer's effective currency at create, the chain being
`customers.currency` when set, else `system_settings` `currency.default`
(`091:36`), copied, never sent (a `currency` in a create body is a 400
unknown field), never changing; cross currency applications are refused;
every journal entry carries its currency; v1 refuses a document whose
effective currency is not in `currency.enabled`, exactly as today. This
record inserts nothing into that chain, so ADR 0005 9.3's footing holds:
one customer, one effective currency, therefore one `balance_due` number.

`company.functional_currency` is not a default and never feeds the chain.
It is the currency of that company's own books, the one the company
reports in, used as a check on documents, not as their source: a document
of a company may be in a currency other than the company's functional
currency (ADR 0005 4.2 already allows exactly this through the customer
override, and its GL reports group by currency), and no amount of it is
ever added to one of another currency. A customer with no override who
trades with two companies is billed in the dealer default in both, one
effective currency, because the chain has one source per customer. The
seed company's `functional_currency` is the setting's value at migration
(section 10), so a single company deployment changes nothing.

ADR 0005 9.3's invariants, restated for companies. `customers.balance_due`
stays the customer's single total in its one currency across companies.
`customer_transactions.company_id` (R2, section 2) makes the subledger
testable per company: the sum of a customer's `customer_transactions` in
one company equals that company's share of the customer's open documents,
per currency, and the sum of `customer_transactions` per
`(company, currency)` equals that company's `1020` balance in that
currency. The place this is checked is the invariant test C2-4 builds
(ADR 0005 9.3, invariants "true after every act and tested"), extended by
build item 3 to group by `(company, currency)`.

The ledger rule of ADR 0005 section 4.2 extends to the company boundary
without change: GL reports group by currency inside a company, and the
consolidated report groups by currency across companies (section 8); amounts
of two currencies are never added. No FX in v1 (section 9).

### 6. The request's company

The company is a function of the branch, and the request's company is the
company of its context branch: the `X-Branch-Id` header, a bound key's
branch, `ResolveBranchForWrite`'s default, the single branch kill switch
(`branch.go:105-110,68`). It is never sent. There is no `/c/{company_id}`
path prefix: it would double every route, the route census, the OpenAPI
contract and the goldens. There is no `X-Company-Id` header: it could
disagree with the branch and create a second, inconsistent scope. A
`company_id` in a body is a 400 unknown field, as a `currency` already is
(ADR 0005 section 4.2).

For a record by path id, PR 39's wall already loads the record's branch
(`purchase_order/handler.go:34,35`; `wire_branch_wall.go`); the record's
company follows by one lookup (the branch row's `company_id`), and a caller
whose grants reach no branch of that company gets the same 403 the branch
wall gives. No new guard layer: the existing guard grows one join.

The few routes with no branch at all (a company's GL, AP and bank reads, and
the consolidated report) take the company as the path parameter of their own
resource: `/api/v1/companies/{id}/trial-balance`, `.../profit-and-loss`,
`.../balance-sheet`, `.../accounts`, `.../journal-entries`,
`.../fiscal-periods`, and the AP and bank reads under the same segment. A
new module segment `companies` joins ADR 0002's vocabulary and the route
census test (ADR 0002 section 2), under ADR 0002's plain rule: reads need
`companies:read`, writes need `companies:write`. The writes under the
segment are owner and admin acts (creating a company, renaming it, setting
its tax code, creating its first branches, closing and reopening its
fiscal periods), so `companies:write` is never granted to a machine key
in v1: no key can create or rewrite a legal entity. This depends on ADR
0009 (finer admin and users scopes, in flight on `refactor/c5-1a-admin`,
PR 49): ADR 0009 keeps ADR 0002's plain `<module>:read` and
`<module>:write` rule for every module outside the admin segment's
declared areas (its section 2), and `companies` is such a module, so the
scope names here hold under ADR 0002 today and under ADR 0009's
`ValidScopeGrammar()` once it lands; item 7 runs after C5-1a so the
company admin routes and screens are born on that grammar.

The record rule under that segment: every repository read and write of a
record route carries `WHERE company_id = $path`, so a path id that names
another company's row answers 404, exactly as an unknown id does.
`/companies/{id}/journal-entries/{entry_id}`, `.../ap/invoices/{id}`,
`.../fiscal-periods/{id}/close` and `.../bankrecon/sessions/{id}` can
never load or touch a row the path's company does not own. Today's
handlers load by id with no scope at all (`gl/handler.go:39-61`,
`ap/handler.go:38-48`, `bankrecon/handler.go:37-50`); the conversion
closes at the company boundary the same hole PR 39 closed for branches on
path ids. A wall test in the shape of
`core/internal/app/serve/wire_branch_wall_test.go`
(`wire_company_wall_test.go` beside it) covers each moved route, and item
5's exit test names it.

Branch creation is the one write that cannot derive a company, because the
branch does not exist yet. It moves under the company segment: `POST
/api/v1/companies/{id}/branches` creates a branch of that company, and
the body carries no `company_id`; the path names it, the create stamps
every row of the subtree with it, and it is immutable after (section 1).
Today's `POST /api/v1/branches` (`location/handler.go:103`, admin) keeps
answering while exactly one company exists, defaulting to it, so every
single company deployment, script and golden keeps working; once a second
company exists it refuses with 409 `conflict`, blocker `company_required`,
naming the company scoped route. Both the new route and the old route's
refusal are rows in `docs/refactor/CONTRACT-CHANGES.md`. Sized inside
item 7.

Who reaches a company: a caller reaches a company exactly when their
grants reach a branch of it, one query over `user_locations` joined to
branch rows. Admins and owners with no grants reach every branch today
(`branch.go:105-110` point 3: an admin or owner may omit the header to
query across all branches), so they reach every company. The role guards
decide the rest, unchanged: the finance and admin roles that guard a
company's GL, AP and bank routes today still decide whether a branch user
may read them, so the derivation widens nothing. Today's branchless GL
routes (`gl/handler.go:39-61`) move under the company resource, each with
a row in `docs/refactor/CONTRACT-CHANGES.md`; AP's (`ap/handler.go:38-48`)
and bank reconciliation's (`bankrecon/handler.go:37-50`) the same.

Keys, per ADR 0002: a branch bound key (ADR 0007 5.5) reaches its branch's
company and no other, whatever its scopes. An unbound key today reaches
every branch (ADR 0002 section 6's known limit), so it reaches every
company; that reach is kept and restated as this record's known limit
rather than refused, because refusing would break every existing key the
day the build lands. This record adds `api_keys.company_id UUID NULL
REFERENCES company(id)`, minted with the key and never edited, exactly as
ADR 0007 5.5 adds `branch_id`: a company bound key is pinned to one company;
a `X-Branch-Id` or payload branch outside it is 403 `forbidden`, audited as
`key.company_refused` beside `key.branch_refused`.

The integration seam (ADR 0007 5.6) is untouched: it keeps its own key, its
goldens hold, and its writes resolve the company through the default branch
as every branchless write does today.

### 7. Tax

- The provider's company code becomes per company: `company.tax_company_code`
  (section 1). Today one process setting (`config.go:36,150`) is stamped on
  every provider call (`avalara.go:107`) and held by the tax service at
  construction (`tax/service.go:36,43`, used at `:111`; wired at
  `serve.go:473-483`). The build moves the stamp to the document's company
  at call time: the tax service takes the company code per call; a company
  row with a null code falls back to the config value, so a single company
  deployment changes nothing. The provider's commit and void paths already
  take the company code as a parameter (`avalara.go:214,242`); their callers
  pass the document's company's code.
- Registrations: the branch's `tax_jurisdiction_code` and `default_tax_rate`
  (`model.go:43,44`) already follow the branch, and through it the company;
  ADR 0005 section 3's resolution order (exemption, ship-to rate, branch
  rate, refusal) is unchanged. The jurisdiction is a branch fact; the
  provider registration is the company fact; neither moves.

### 8. Reports and consolidation

- `gl.Service.GetTrialBalance(ctx, asOfDate)` (`service.go:244`) and the
  statements (`service.go:258,280`) take the company, served by the company
  routes of section 6. No route answers a company-less trial balance after
  the conversion; the removal is a listed contract change.
- Consolidated trial balance: `GetTrialBalanceConsolidated(ctx, asOf,
  companyIDs)`, a new read over a named set of company ids the caller's
  grants reach in full; a set holding a company the caller cannot reach is
  403, the wall of section 6 applied per element. Grouped by currency:
  companies whose functional currencies differ are consolidated per currency
  only, never added (section 5). Eliminations have nothing to net in v1
  (no inter company documents exist, section 9), so the v1 answer is a plain
  sum per currency per account code across the named companies; the
  elimination rule arrives with the inter company transfer's own record.
- The reporting module's queries over invoices, orders and inventory carry
  no branch predicate today, and the dashboard cache key carries no branch
  scope; that is C5-1c's branch wall to build, per its brief. The company
  filter on those reads lands with C5-1c, not before it: this record owns
  the GL, AP and bank company filters (they move with section 6's routes),
  and C5-1c owns the rest when its wall lands.

### 9. Refused in v1, with reasons

- **A branch moving between companies.** Every per company row of that
  branch, closed books included, would have to be reassigned, and closed
  periods of both companies would be questions. A discrete item with its
  own record if a dealer ever needs it; refused here.
- **The inter company stock transfer.** v1 has no inter branch transfer at
  all: ADR 0008 section 1 refuses it ("Refused, as today. A move stays
  inside one branch"), and ADR 0008 section 2.8 pins a move inside one
  branch (blocker `cross_branch`) and posts no entry ("both sides are
  `1030` at the same cost"). An inter company transfer is first a transfer
  between two branches. The later path is named, and it is not this
  record's: first ADR 0008 section 1's own later item, a transfer order
  with in transit stock; then the inter company form on top of it, on its
  own sized record: two journal entries, one per company, each balanced, in
  one database transaction, stamped with one `intercompany_group_id`; due
  to and due from accounts in each company's chart; the transfer price rule
  against ADR 0005 section 8.4 and ADR 0008 section 3.5; an elimination
  rule for section 8's consolidation. One entry holding two companies'
  lines is refused by design: it cannot balance per company (section 3).
  The first draft of this record accepted the transfer in seven dev hour
  equivalents; that was a fraction of a fraction of the work, and the
  review was right to strike it.
- **The inter company sale**: one invoice spanning two companies, or one
  company selling on another's AR ledger. Refused. One invoice is one
  company's document: its entry, its gapless series, its tax registration,
  its currency chain (section 5) are each one company's. A dealer who must
  bill a customer from both companies issues two documents, one per
  company. (The first draft's reason, "ADR 0005's one currency per document
  rule forbids it", was wrong: a sale by one company to a customer is one
  document in one currency. The reason recorded here is the one that
  holds.)
- **FX.** No exchange rates are stored or applied (ADR 0005 section 4.2
  unchanged). A company changing its `functional_currency` after documents
  exist is a later record: history would need restating.
- **Splitting an existing ledger into two companies.** Out of scope
  (Context). The migration writes one company and attaches everything to
  it.

### 10. The migration

One numbered migration and its down file (the number is the next free when
the build item merges; another item may take it), one transaction,
idempotent on re-run, touching only tables that exist when it runs. The
census test below is the guard for tables that land later.

Up, in order:

1. Create `company` (section 1) and insert exactly one row: `code` `MAIN`
   (a fixed code the dealer can rename on the admin screen of section 11
   until the company issues a gapless number, then frozen, section 4),
   `name` `Main`, `functional_currency` from `system_settings`
   `currency.default` (`091:36`; `USD` when absent),
   `fiscal_year_start_month` 1, `tax_company_code` null (the operator sets
   it; the config fallback keeps answering, section 7).
2. `locations.company_id UUID NOT NULL REFERENCES company(id)`: branch rows
   take the seed company; every other row takes its `branch_id`'s company,
   its `branch_id` first backfilled from its ancestor chain where null
   (`057:21` allows null; `058` and `060` keep and backfill it). The
   update trigger of step 4 refuses a later move.
3. The R1 tables of section 2 that exist when it runs: add `company_id`,
   backfill from the parent, NOT NULL, the foreign key, and the
   `BEFORE INSERT OR UPDATE` trigger that keeps it and refuses a parent of
   another company. Payments and credit memos are R1 from the branch cycle
   2 adds (section 2): their `branch_id` columns arrive with C2-3 and C2-4
   and are set NOT NULL here first, then take the same trigger. The
   nullable branch rows that stay nullable (`075:21`, `078:15`, `079:42`)
   keep their named writer rule (section 2), their company backfilled from
   their register or sale; `079:79` is left alone (legacy after C2-4, ADR
   0005 9.1); `089:64` is left alone (no company column, section 2).
4. The R2 tables of section 2 that exist when it runs: add `company_id`,
   backfill every row to the seed company, NOT NULL, the foreign key. No
   trigger: the writers of section 11's third item set it from then on.
5. GL: `UNIQUE (code)` dropped and `UNIQUE (company_id, code)` added
   (`025:10`); `UNIQUE (company_id, id)` added; the composite foreign keys
   of section 3; `charge_codes.revenue_account_code`'s FK dropped
   (`092:239`); `gl_fiscal_periods` attached to the seed company; the 077
   exclusion and trigger replaced with the per company forms
   (`077:29-33,43-60`).
6. Numbering groundwork only: nothing. The per company series rows of
   companies created later are the numbering item's own concern; the seed
   company's `invoice` and `credit_memo` rows are never renamed (section
   4), and this migration does not touch tables that do not exist when it
   runs.
7. `api_keys.company_id UUID NULL REFERENCES company(id)` (section 6).
   `user_locations` is not touched.

Down: refuses, with `RAISE`, when more than one company exists, or when any
row of any table the up touched points at a company other than the seed;
otherwise it drops the triggers, the columns, the seed row and the `company`
table, in reverse order. A down that would lose a second company's data
loses nothing instead: it answers.

Tests, in the migration item:

- Up, down, up, on the repository's seed data and on a database with the
  cycle 2 to 4 tables present (the migration test shape the recipe's step 3
  names), proving idempotency and the round trip.
- The refusal paths of the down: one extra company row, and one row pointed
  at a made up company, each making the down raise.
- **The census test**: it reads the census list that lives beside the
  migration, one line per table, so the test reads as the table of
  section 2 does. It fails when a table with a `branch_id` column (minus
  the legacy renames and `events_outbox`) carries no line; when a line's
  rule is R1 but the trigger is missing; when a line's rule is R2 but the
  `company_id` column is missing; when a table's `branch_id` is NOT NULL
  and its rule is not R1, because a NOT NULL branch is always a parent
  the trigger can follow (the one recorded departure is `inventory`,
  section 2, whose census line names its parent and the reason, and the
  test fails if that line is removed); and when a table named in
  section 2's per company list lacks `company_id`. Every later item that
  adds a branch carrying table extends the census in the same pull
  request, so a table cannot land without its rule.

### 11. Items, order and sizes

The build is this record's own items, inside C5-3. There is no C5-4 or C5-5
in v1: the plan skips R5-4, the shared kit, and the cycle 5 items are C5-0,
C5-1 (with C5-1a and C5-1b already split out of it), C5-1c, C5-2a, C5-2b and
C5-3. This record is C5-3's design stop; its build items run in the order
below.

Against the cycles already running: after C2-5. The cycle 2 chain is C2-2b
(allocation, back orders, fulfilment; merged as PR 43), C2-3 (invoices,
credit memos, gapless numbering and void; in review as PR 46), then C2-4
(payments, deposits, AR) and C2-5 (POS, till). C2-3 to C2-5 add and reshape
the AR and counter tables (`ar_applications`, `document_counters`, the
payment and credit memo columns, ADR 0005 sections 4.1, 9.1, 9.2 and 13),
so the company build waits for them. After C3-2B (order, invoice, credit
memo and counter line parity per ADR 0006): it reshapes the lines the
company migration attaches. After C4-2: the real reason, on the record, is
that ADR 0008 adds the stock, adjustment, receipt, vendor credit and feed
tables of section 2, each needing a company rule; the census test then
covers them from the start. After C5-1a (ADR 0009's finer admin scopes,
PR 49): item 7's company admin routes and screens are born on ADR 0009's
scope grammar, so it runs after C5-1a. After C5-1's GL, finance and bank reconciliation
conversion and after C5-1c's branch wall: the company filter of sections 6
and 8 rides the routes and the wall those items build. The design itself,
this record, can merge now, ahead of all of them: it changes no code.

Sizes, dev hour equivalents, re argued against the code from the round 1
review's estimate (the review's envelope was 60 to 100; the buckets below
are its own, and their sum is stated rather than rounded):

| Order | Item | Size |
|---|---|---|
| 1 | The migration and the census test (section 10: about twenty tables, six rules, triggers, per company GL constraints, round trip and refusal tests) | 14 to 22 |
| 2 | GL per company (section 3: resolver signature through the `Sync*` family and `PostEntry`, chart seed per company, periods, the 077 forms, composite foreign keys) | 12 to 18 |
| 3 | Posting writers set the company (invoice, credit memo, payment, deposit, counter, AP `SyncVendorInvoice` and `SyncVendorPayment`, bank; the AR core's checks) | 14 to 24 |
| 4 | Numbering and URLs (section 4: series per company keyed by id, the code bearing prefix for companies created later, the `numbers_issued` freeze on the code, ADR 0007 section 7 patterns, contract change rows) | 6 to 10 |
| 5 | The request's company (section 6: derivation from the branch context, the path id wall's one lookup, the `companies` routes and vocabulary entry, the record rule with `wire_company_wall_test.go`, `api_keys.company_id`, `key.company_refused`) | 8 to 12 |
| 6 | Reports and consolidation (section 8: per company trial balance and statements, `GetTrialBalanceConsolidated`, the currency grouping) | 6 to 10 |
| 7 | Company admin (create a company with its chart copied from the template company and its periods seeded, rename the seed, set its tax code, create a company's first branches through the company scoped route; routes and the desk screen) | 6 to 10 |

Total: 66 to 106 dev hour equivalents. Item 1 lands first and items 2 to 7
depend on it; items 2 and 3 land together or in that order; 4 to 7 are
independent of each other; item 7 runs after C5-1a, per the run order
above.

## Consequences

- One dealer database can hold several companies' books, separated at the
  chart, the journal, the subledgers, the gapless series, the tax
  registration and the bank accounts, while sharing parties, catalog,
  pricing, stock identity, users and locations.
- The single company deployment keeps its behaviour: one seed company, the
  config tax fallback, the currency chain unchanged, and numbers keeping
  the bare forms they have always had.
- The books stop being branchless: every journal entry, payment, credit
  memo, vendor bill and bank account names its company, set by its writer
  and checked by the database where the shapes allow.
- Gapless numbers become per company, keyed by company id: companies
  created after the migration carry their code in the prefix, the seed
  company's series and number forms change nothing, and number URLs keep
  working under widened patterns, recorded as contract changes.
- The census test holds the split: no branch carrying table lands without
  a company rule after the migration item merges.
- The inter company transfer, the inter company sale, FX, the branch move
  and the ledger split are all refused, each with its reason and its later
  path on the record.

## Alternatives considered

**A company from a path prefix and a header** (the first draft): a
`/c/{company_id}/...` prefix doubles every route, the census, the contract
and the goldens, and an `X-Company-Id` header that can disagree with the
branch creates a second scope. Refused: the company is a function of the
branch (section 6).

**One trigger for every table** (the first draft): a `company_id` copied
from `branch_id` cannot reach the books, which carry no branch
(`025`, `008`, `018`, `028`, `029`), and several `branch_id` columns are
nullable, so a NOT NULL copy fails on real rows. Replaced by the six
rules of section 2.

**A shared chart with a company column on balances only**: every posting
resolves accounts by code (ADR 0005 section 8.1), and codes collide as soon
as two companies customize their charts; the trial balance would join
negatively. Refused: per company charts with a per company resolver
(section 3).

**Per company customer terms now** (a `customer_company_terms` child row):
no v1 requirement names it, and the shared master with the limit stated is
smaller. Deferred, with the shape named (section 2).

**A company column on the outbox**: every event writer pays for it, and no
consumer filters events by company today. Refused: derived from `branch_id`
where present, stated as a limit (section 2).

**Fiscal periods shared, one calendar**: smaller, and wrong the day a second
legal entity has a different year end; the closed period guard has to be
per company either way, because a period of one company must not lock
another's postings. Per company (section 3).

## Closest calls

- Fiscal periods per company versus one shared calendar: per company, for
  the reason above; it is the larger half of item 2.
- Gapped sequences shared versus per company: shared. Gaps are allowed
  there (ADR 0005 section 4.1's own table) and uniqueness holds; only the
  gapless documents are per company, because their gaplessness is the tax
  authority's.
- Unbound keys reaching every company: kept (ADR 0002 section 6's reach
  today) rather than refused on the company routes, with
  `api_keys.company_id` as the narrowing a dealer mints when it needs one.
- `api_keys.company_id` now versus later: now. It is the same mint
  mechanism as ADR 0007 5.5's branch binding, and a dealer running two
  companies needs it on the day the second company exists.
- The tax company code per company with the config value as fallback,
  rather than config only or company only: the fallback keeps every single
  company deployment byte for byte.
- Customer terms shared with a stated limit, rather than the per company
  child row now (section 2).

## Known limits

- Shared party terms (customer credit limit, payment terms, currency
  override, PO required; vendor terms) apply to every company alike, until
  the per company terms child row of section 2 is built.
- An unbound machine key reaches every company (ADR 0002 section 6's reach,
  restated for companies); a dealer narrows it by minting a bound key.
- Events carry no company column; consumers derive it from `branch_id`, and
  an event with a null branch names no company (ADR 0003 section 1).
- Consolidation sums per currency with no eliminations, until inter company
  documents exist (section 9 names the record that adds them).
- Splitting an existing ledger into two companies is out of scope; the
  migration writes one company (Context, section 10).
