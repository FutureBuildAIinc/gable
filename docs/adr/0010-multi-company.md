# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0010: Multi company within one database

## Status

Accepted on `refactor/v1`. Builds on ADR 0001 (wire contract), ADR 0002
(machine keys), ADR 0005 (sales and money core), ADR 0006 (units and pricing),
ADR 0007 (drafts, links and confirm gated scopes, especially 2.3, 5 and 7),
and ADR 0008 (inventory identity and vendor intake). No prior ADR is
superseded; ADR 0005 section 4's numbering rule and section 5's one currency
per document rule extend to per company. The PRs of PR 37 (the draft link list
guard that pins its left side to the request's branch) and PR 39 (the link
target guard that pins its right side to the same branch or a denied list of
peer branches) extend to companies the same way they extend to branches.

## Context

Today's code in `core/internal/location/model.go` puts every branch on a
single `location` table with `Type = BRANCH` and `ParentID = nil`, and carries
the branch onto every other table through `branch_id` columns such as
`core/internal/invoice/model.go:24`. There is no company row anywhere, and
`grep -rn "type Company"` over `core/internal/` returns only tax engine URLs
(`core/internal/tax/avalara.go:208`, `:236`). In other words, one tenant
database already models one operation; multi company is the next shape above
it.

A company is the legal entity that owns its own books. A branch is a place
inside one company. A dealer who runs two companies under one tenant wants
each company's general ledger (GL), accounts receivable (AR), accounts payable
(AP), invoices, credit memos, payments, deposits, purchase orders, vendor
invoices, tax registrations, document number series, bank accounts and
reconciliations kept apart, while still sharing customers, vendors,
products, units, price lists, users, and locations so that the two companies
do not double key the same record. The boundary inside one tenant database
is what this ADR fixes.

## Decision

### 1. What a company is

A company is a legal entity with its own books. It carries its own general
ledger, its own subledgers, its own document number series, its own tax
registrations, its own bank accounts, and its own currency. It does not
carry cash on its own books; cash sits on a bank account that belongs to a
company and only to one of them. A company is identified by a UUID and a
short code (`code`), both unique within the tenant.

A branch is a place. A branch row in `core/internal/location/model.go` is
already a `Type = BRANCH` row of the `location` table. A branch belongs to
exactly one company. The company of a branch is read from `branch.company_id`
and is set when the branch is created. Moving a branch between companies is
not allowed in v1; it is a separate scope because every per company row on
that branch would have to be reassigned and the document number series would
have to reconcile across the move. See "Closest calls" below.

### 2. The table

A new table `company` lives in `core/migrations/`. The initial migration is
numbered by the lead as part of the C5-3 migration, and that filename is a
placeholder only; this ADR names it the C5-3 migration. The schema:

- `id uuid primary key`
- `code text not null unique` (short, uppercased, used by humans)
- `name text not null`
- `functional_currency_code text not null` (the ISO code for the GL currency)
- `reporting_currency_code text not null` (default equals functional; may differ)
- `country_code text not null`
- `timezone text not null`
- `fiscal_year_start_month smallint not null default 1` (calendar-aligned)
- `created_at`, `updated_at` not null

Every row that already carries a per branch column `branch_id` gets a
generated `company_id` column and a foreign key to `company(id)`. The
denormalization follows the same rule ADR 0007 used for branches: the
`branch` table is the source of truth, a `before insert/update` trigger on
each per company table writes `company_id` from the branch's `company_id`,
and no application code writes `company_id` directly. New triggers live in
the C5-3 migration alongside the column adds.

### 3. What is per company

Per company, owned by exactly one company and only seen inside that scope:

- `gl_account`: one chart of accounts per company. ADR 0005 section 4's
  numbering rule becomes `gl_account.code` unique within `(company_id, code)`.
- `gl_journal_entry` and every `gl_journal_line`: posts to one company only;
  the trial balance is per company.
- `ar_invoice`, `ar_credit_memo`, `ar_payment`, `ar_deposit`: AR subledger
  rows are owned by one company. An AR invoice now carries both `branch_id`
  (the issuing branch) and `company_id` (the issuer's company).
- `ap_invoice`, `ap_credit_memo`, `ap_payment`, `ap_deposit`: AP subledger
  rows mirror the AR shape with the same two columns.
- `purchase_order`, `purchase_order_line`, `vendor_invoice`: belong to the
  buying company.
- `tax_registration`: a row per `(company_id, jurisdiction, tax_type)`; the
  tax engine reads the registration of the document's company.
- `document_number_series`: one series per `(company_id, document_type)`;
  see section 7 for the numbering rule.
- `bank_account` and `bank_reconciliation`: the bank account is owned by one
  company; AR and AP cash posts hit that company's ledger.

### 4. What is shared across companies

A record that describes a person, a thing, a rule or a person-place, with no
book attached, is shared. The list:

- `customer` and `customer_address`: one master per party regardless of
  which companies that party trades with. Pricing and balance stay per
  company on `ar_invoice`.
- `vendor` and `vendor_address`: same shape, shared master.
- `product`, `product_variant`, `unit`, `unit_conversion`: shared catalog.
  ADR 0006's pricing rules at C3-1 base are shared.
- `price_list` and `price_list_rule`: shared. The price a customer is
  charged is the price list applied in the AR invoice of a given company.
- `user`, `role`, `grant`: a user can hold grants to branches of several
  companies. The grant table grows `company_id` on top of `branch_id` so
  the access check runs once. See `core/internal/location/user_repository.go`
  for the grant shape that ADR 0007 section 5's branch access check reads.
- `location` rows that are not branches (zone, aisle, rack, shelf, bin, yard)
  are shared, because their `branch_id` already pins them to one branch and
  through that to one company.

### 5. How a branch belongs to one company

`branch.company_id` is set on branch creation and is not null thereafter.
The `location` row's `Type = BRANCH` shape in
`core/internal/location/model.go:26` becomes the root of a sub-tree that
this migration adds. Updates that would clear `branch.company_id` are
refused at the trigger level. The company of a branch is denormalized onto
every per company row by the triggers described in section 2.

### 6. Inter company sales and transfers

A transfer of stock between two branches of two different companies is an
inter company transfer. It must post in both ledgers at the same instant so
that consolidated elimination can see the matching pair.

In v1, this ADR accepts the inter company stock transfer as scope, on the
following shape:

- The document type is `stock_transfer_inter_company`. Its header carries
  `(from_branch_id, to_branch_id, from_company_id, to_company_id)`. The two
  company ids must differ.
- The receiving side posts an AP interim (`ap_intercompany` as the
  `ap_invoice` subtype) and the issuing side posts an AR interim
  (`ar_intercompany` as the `ar_invoice` subtype). Both interims post in one
  GL transaction so that the consolidated trial balance inter company
  elimination rule can net them out.
- Each interim carries the same `intercompany_group_id` (a UUID set when the
  transfer is created and stamped onto both interims) so that the
  consolidated report pairs them.
- A reversal is one document and posts both legs as a reverse in the same
  transaction.

This shape uses about 7 dev hour equivalents (4 for the document, 2 for the
GL legs, 1 for the test). It is sized at the end of section 8 with the rest
of the later items.

The complement, an AR inter company sale (one company sells to a customer
of another company), is refused in v1. The reason: that sale has to honour
both companies' price lists, tax registrations and AR subledgers at once,
which the C5-3 migration does not cover. ADR 0005's one currency per document
rule pins that rule, and allowing a cross-company sale would break it. A
later ADR can pick this up when multi-currency and multi-AR are in scope.

### 7. Reports

The GL trial balance becomes per company. The path
`gl.AccountService.TrialBalance` (see `core/internal/gl/service.go`) gains a
`company_id` filter; without it, the service refuses to run and returns
`400 missing_company`. A consolidated trial balance is a new path
`gl.AccountService.TrialBalanceConsolidated(groupIDs)` which sums per
company balances and subtracts inter company eliminations keyed by
`intercompany_group_id` from section 6.

Reports that already take a `branch_id` filter accept a `company_id`
filter as a stricter superset. The consolidated report only runs when the
caller's grant list covers every company in the consolidated set.

### 8. Numbering

ADR 0005 section 4 defines a document number series per
`(document_type, location_id)`. The location id in that pair is the branch
id today. This ADR extends that pair to
`(company_id, document_type, location_id)`; the original `series` table
gains `company_id` and the unique index becomes `(company_id, document_type,
location_id, sequence_name)`. Migration of the series uses the same trigger
pattern as section 2: the series row's `company_id` is denormalized from
the branch's `company_id`. Numbering is booked against the request's
company, never against the body, the same way ADR 0007 section 2.3 pins a
branch from the path and the headers and not from the body.

### 9. Currency

ADR 0005 fixes one currency per document. A company's GL is in its
`functional_currency_code`. A document issued from a branch of that company
is in that company's functional currency. The body of a request that names a
currency other than the request's company's functional currency is rejected
with `400 currency_mismatch`; the request's company is the source of truth
here.

A document in the AR or AP subledger that belongs to a company and that
company has changed currency (a separate scope, not in v1) would conflict
with this rule; this ADR records the conflict and leaves the move to a
later ADR.

### 10. The request's company

ADR 0007 section 2.3 reads the request's branch from the path and the
headers, not from the body. The request's company is read by the same
mechanism:

- A leading path segment `/c/{company_id}/...` is accepted only when the
  signed-in user's grants include a row with that `company_id`. The
  middleware looks up the grant using
  `core/internal/location/user_repository.go` plus a new `company_id`
  column.
- A header `X-Company-Id` is accepted only when the path did not already
  pin one and the grant check passes; the header rule mirrors ADR 0007
  section 2.3's branch header rule.
- The body never names a company. A body that carries `company_id` is
  rejected at the request decode layer with `400 company_from_body_forbidden`.

When the path or header is absent and the request hits a per company route,
the server returns `400 missing_company`, the same shape as the missing
branch error. When the path and header disagree, the server returns `400
company_mismatch`. Through this, PR 37 (draft link list guards the left
side against the request's branch) and PR 39 (link target guards the right
side against the same branch or a denied list of peer branches) extend to
company ids: a draft link row is refused when its left side or right side
sits in a company the caller cannot act on, and the denied peer list is
`other_companies` only when the scope is intra-company-only and not when
the scope is inter-company.

### 11. Migration

A single migration (the C5-3 migration, number assigned by the lead, this
ADR only names it as a placeholder) does the following, in one transaction:

1. Creates the `company` table from section 2.
2. Inserts one row per existing tenant database, with `code` derived from
   the tenant name, `functional_currency_code` set by the tenant config,
   `fiscal_year_start_month` set to 1, and an `id` carried back by the
   trigger that follows.
3. Adds `branch.company_id not null references company(id)` and backfills
   every branch to the inserted company.
4. Adds the `company_id` column on every per company table listed in
   section 3, with a not null constraint that takes effect after step 5.
5. Adds the `before insert/update` triggers that denormalize
   `company_id` from `branch_id` on every per company table.
6. Extends the `document_number_series` table with `company_id` and
   backfills it from the branch the series points to, then tightens the
   unique index to `(company_id, document_type, location_id, sequence_name)`.
7. Updates the access check grants (`core/internal/location/user_repository.go`)
   with a `company_id` column and a unique index
   `(user_id, branch_id, company_id)`.

The migration is reversible. The down steps clear the triggers, drop the
columns, drop the inserted `company` row, and remove the seeding script. A
test exercises the down and a fresh up to confirm the round trip.

The cycle 2 chain (C2-3 invoices, C2-4 credit memos, C2-5 counter), the
cycle 3 item C3-2, and cycle 4's ADR 0008 are run before the C5-3
migration. The reason: those items grow the per company tables, and the
C5-3 trigger that denormalizes `company_id` from `branch_id` is correct
only when every branch has a `company_id` and every per company row's
trigger fires. Items that follow C5-3 do not change the per company /
shared split; they extend within it.

### 12. Later items, run order, sizes

Sizes are dev hour equivalents. The order below matches the dependency.

1. The C5-3 migration (this ADR). 4 dev hour equivalents.
2. Reports: per company trial balance and consolidated trial balance.
   Builds on 1. 4 dev hour equivalents.
3. Inter company stock transfer (section 6). Builds on 1. 7 dev hour
   equivalents (4 doc, 2 GL, 1 test).
4. AR inter company sale. Refused in v1 (section 6). 0 dev hour
   equivalents.
5. Numbering per company (section 8). Builds on 1. 2 dev hour equivalents.
6. Currency guard per company (section 9). Builds on 1 and 5. 1 dev hour
   equivalent.
7. Branch access grant by company (section 10). Builds on 1. 2 dev hour
   equivalents.
8. Access check consolidation, so that the per company guard and the
   per branch guard run in one call. 2 dev hour equivalents.

The order against the cycle 2 chain (C2-3 invoices, C2-4 credit memos,
C2-5 counter): those come first, because each adds a per company table that
the trigger in the C5-3 migration must denormalize. The C5-3 migration
runs after C2-5. The order against C3-2 (units and pricing): C3-2 runs
before C5-3, because the shared price list row from section 4 has to
exist before the price list per AR invoice rule lands. The order against
cycle 4 (ADR 0008, inventory identity and vendor intake): cycle 4 runs
before C5-3, because `product`, `product_variant`, and `vendor` from
section 4 are the shared masters that the C5-3 migration leaves alone.

## Consequences

- Every per company table grows a `company_id` column that is denormalized
  from `branch_id` by trigger, so application code never sets
  `company_id`. PR 37 and PR 39 extend to companies through the guard
  pattern ADR 0007 section 2.3 already uses.
- A consolidated trial balance is now possible because inter company
  transfers land in both ledgers keyed by `intercompany_group_id`. A
  third party report tool that aggregates rows by `company_id` can pair
  them.
- The AR inter company sale is refused in v1. A user of multi company who
  wants a customer of company A to buy through company B will have to
  issue the sale twice, once per company, until a later ADR allows a
  cross-company sale. The reason is on the record (ADR 0005's one
  currency per document rule, and the lack of multi-currency in v1).
- Moving a branch between companies is refused in v1. The data move
  requires a re-assignment of every per company row and a reconciliation
  of the document number series, which is a separate item.

## Closest calls

- Whether to refuse the inter company stock transfer in v1 or to ship it.
  Accepted for v1 because the document type fits the C5-3 trigger rule
  and the GL post is one transaction. The AR inter company sale was
  refused because it crosses the AR subledger of two companies and
  forces multi-currency into scope.
- Whether to allow a branch move between companies. Refused in v1
  because the per company row reassignment and the numbering series
  reconciliation are a discrete piece of work that has its own ADR.
- Whether the request's company is read from the body. Refused; the path
  and header are the only sources, mirroring ADR 0007 section 2.3 for
  branches.
- Whether `customer` and `vendor` are shared or per company. Shared,
  because a party is a master row that trades with whichever company
  the AR or AP invoice names. Balances stay per company on
  `ar_invoice` and `ap_invoice`, so a party's AR balance is the sum of
  the per company AR balances.

## What I could not settle

- The exact rename of the `document_number_series` unique index. Section
  8 names `(company_id, document_type, location_id, sequence_name)` as the
  new shape. ADR 0005 section 4 reads `series.location_id` as the branch
  id today, and a follow-up ADR may move to `(company_id, document_type,
  sequence_name)` once a per company series can have several branches.
- Whether a consolidated report respects a signed-in user's grants when
  the consolidated set spans companies. Section 7 says yes; the rule
  for one user with grants only on company A who requests a consolidated
  report across companies A and B is `403 grant_missing`, but the exact
  error code shape is owned by a later ADR.
- The cycle 5 chain (C5-1, C5-2, C5-3) order against C5-4 and C5-5.
  C5-1 and C5-2 are out of scope for this ADR. The brief lists C5-3 in
  the cycle 5 chain, and this ADR's order in section 12 is the strict
  topological order against the cycles named, not a global plan.
