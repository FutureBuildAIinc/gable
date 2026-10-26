# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0011: the offline counter

## Status

Proposed for parity row CP-13 ("Offline counter (keeps selling when the
connection drops)"). It becomes accepted when the lead merges it after
review. No code is written for CP-13 before it is accepted.

It stands on ADR 0001 (the wire contract), ADR 0003 (the outbox), ADR 0005
(the sales and money core) and ADR 0006 (units and pricing).

**It supersedes, for the counter only, the rule "money never queues
offline"** (Ruling 3 = A of the parity plan, built as item S6). That rule is
written in the repository here, and each place changes when this record's
slices land (section 8, slices O1 and P1):

- `docs/parity/LBM-PARITY-MATRIX.md`, ruling 3 ("Offline (`S6` and Field
  offline store): Ruled"), the S6 row of the shell table, the field offline
  store paragraph ("money never queues", line 477), and the line "money
  never queues offline" under the shell rulings;
- `docs/parity/GABLE-DISTINCT.md`, the shell list item "Money never queues
  offline" (line 505), the "Money never queues" row of the gap table (line
  518), row D18 (counter till) and ruling 2 ("Money never queues and field
  offline");
- `docs/parity/rows/CP.md`, row CP-13;
- `docs/refactor/CONTRACT-CHANGES.md`, row S6 (the retirement of
  `POST /api/v1/pos/sync` to a 409);
- `core/internal/pos/handler.go`, `SyncOffline` (answers 409
  `money_never_queues` for every batch that parses);
- `core/internal/platform/httpx/errors.go`, `CodeMoneyNeverQueues`;
- `web/packages/work-offline/src/scopes.ts` and `signal.ts` (the money scope
  fence, `pos_transactions` among the money scopes, and the no signal guard).

ADR 0005 section 3 (the "one exception" for the counter's offline sync) and
section 14.2 C2-5 (the offline sync test lines) describe the replay as it
stood before S6. This record replaces both descriptions: an offline sale's
tax is the tax recorded at the sale, so no sale waits on the tax provider at
sync (section 3). ADR 0005's Status gains the matching "superseded in part
by ADR 0011" line in the change that accepts this record, and the same
change amends ADR 0005 section 11 with the two lock kinds this record adds
ahead of step 1 (section 3.2).

Everything else in the earlier rule stands: the field shell's offline store,
the pro shell's read cache and every other surface keep "money never
queues". The exception is the counter alone, under the rules below.

## Context

The ruling for CP-13 is full offline selling: the counter keeps selling when
the connection drops. Item S6 had retired the server side replay instead,
and the desk had already dropped its browser queue. What exists today:

- **The retired replay.** `core/internal/pos/sync.go`
  (`SyncOfflineTransactions`, `StartSaleAt`) still holds the earlier replay:
  one sale rebuilt through three transactions (`StartSaleAt` inserts the
  sale with the client's id, `AddLine` prices the lines afresh with the live
  engine, `CompleteSale` completes it), duplicates found by
  `SaleExists(client_id)`, and a tax provider failure left pending in
  `pos_sync_log` (migration `044_offline_sync.sql`). The handler no longer
  calls it. Its flaws for real offline selling: it reprices at sync (the
  customer paid the offline price), it can leave an OPEN sale between its
  transactions, it stamps the invoice with the sync's date, and it trusts
  the batch entirely.
- **The completion.** `CompleteSale` in `core/internal/pos/complete.go` is
  the one money moment: card charges and the tax provider call before the
  transaction, then one transaction that checks the session is open
  (`session_closed`), prices tax (`tax_quote_stale`,
  `tax_rate_not_configured`), checks tenders, credit (under the 1a advisory
  lock) and store credit, issues stock (`inventory.IssueQty`, which refuses
  to go below zero: `insufficient_stock`), writes the invoice with
  `InvoiceDate` from `BranchLocalDate(now)`, the payments, the journal, the
  audit row and the events last, in ADR 0005 section 11's lock order.
- **Numbers.** A sale's `POS` number is the column default
  `nextval('pos_transaction_number_seq')`, taken when the row is inserted
  (`core/migrations/105_pos_wire_contract.sql`); invoice numbers come from
  the gapless `document_counters` row, through the SQL default and
  `httpx.NextGaplessNumber` (`core/internal/platform/httpx/docnum.go`, ADR
  0005 section 4.1), taken late in the completion. A device that cannot
  reach the server can take neither.
- **Tenders.** `CASH`, `CHECK`, `CARD`, `ACCOUNT`, `STORE_CREDIT`
  (`core/internal/pos/model.go`). The account verdict
  (`core/internal/pos/standing.go`) is allowed or blocked for
  `cash_only_terms`, `credit_hold` or `credit_limit`; the check limit is the
  customer's own or the database wide `pos.check_limit_cents`
  (`counter_repository.go`); "ID checked" rides on a check or account tender
  (`input.go`), and CP-04 slice S04 is adding the rule that requires it.
  Overrides are approved online by an owner or admin other than the cashier
  (`override.go`).
- **Card present.** CP-05 chose Run Payments' Terminal API, a cloud API: the
  server calls Run over HTTPS and Run drives the device
  (`docs.runpayments.io`, the card present overview). This record's author
  read every page Run's documentation index lists
  (`docs.runpayments.io/llms.txt`, each page as `.md`, plus `openapi.json`):
  the words "offline", "store and forward" and "deferred" appear only as an
  error text ("Terminal is unavailable: device offline or busy"), a gateway
  decline reason and a response code. **Run documents no offline, store and
  forward or deferred authorization mode for card present.**
- **Branch days.** A till session's business date is the branch local date
  of its `opened_at` (`sessionBusinessDate`, `endofday_repository.go`).
  `CloseTill` (`till.go`) locks the session and refuses while card claims
  or terminal intents stand; `CloseBranchDay` (`endofday.go`) refuses while
  any session of the day is open or waiting for sign off, and a closed day
  can be reopened (`till_day_closes.status = 'REOPENED'`, migration 127).
- **Settings.** Branch policies follow one pattern: a per branch table over a
  database wide `system_settings` default (`till_variance_policy` and
  `till.variance_threshold_cents`, `GetVariancePolicy` in
  `till_repository.go`).
- **The client.** The counter is `web/apps/desk/src/pages/pos/POSTerminal.ts`:
  every line and tender is a server call; the cart lives on the server. The
  desk shows an offline banner only (`app-shell.ts`). The work shell
  (`web/apps/work-shell`, `tauri/gable-work`) frames the desk's `/pos` route
  from the server's origin; the Tauri core (`tauri/core`) holds tokens in the
  OS keychain (`custody.rs`, fail closed) behind an origin fence
  (`fence.rs`). `web/packages/work-offline` is the no signal guard and money
  fence. No offline store exists yet in any shell (the field shell's is
  planned). `GET /api/v1/pos/catalog` serves products at the walk in price
  with on hand.
- **Pricing.** `pricing.Resolve` (ADR 0006 section 5.3) walks contract,
  rules, category rules, level, branch base, base. Only the server runs it.

## Decision

### 1. What sells offline

An offline sale is a cash and carry pickup at the branch, made on an
enrolled register (section 6.1) whose till session was open when the
connection dropped, under the branch's offline policy (section 4). Each
tender:

| Tender | Offline | How it is made safe |
|---|---|---|
| Cash | Yes | Change only from cash, as online. Bounded by the queue caps. |
| Check | Yes | Amount at most the check limit as last known (the customer's own, else the database default, both in the snapshot) and at most the branch's offline check cap. The ID rule as last known (S04's rule, carried in the snapshot) is enforced on the device. No override offline: a check over the limit is refused at the counter. |
| Account | Yes, cached accounts only | Only an account whose verdict was `allowed` in the snapshot. The device's room on that account is the least of: the credit limit less the open receivable as at the snapshot, the branch's per account offline cap, less what this register has already queued on the account. Cash only terms, a hold, a missing PO where the account requires one, or a buyer not on the roster refuse at the counter. No override offline. |
| Card | **No** | Run documents no store and forward (Context). The card present path is server to Run to device, so with the server unreachable there is no card path at all. The tender is disabled while offline. A later ADR may add it if Run documents such a mode. |
| Store credit | No | Open credit memos are a shared balance: the AR desk or another register can spend the same memo while this one is offline, and the memo lock (ADR 0005 section 11, step 3) cannot be taken offline. Disabled while offline. |

Online only, always: returns, exchanges, voids, deposits, payments on
account, refunds, till open, close, movements and sign off, manager
overrides, a new customer, a cashier change. A walk in sale offline takes
cash or check only.

Two registers offline at once can each use their per account room, so the
worst case exposure on one account is the per account cap times the number
of registers offline. The cap is set with that in mind (section 4).

### 2. Numbering and idempotency

**The device never mints a server number.** Each offline sale carries:

- `id`: a random UUID the device generates (version 4, the repository's
  convention). It becomes `pos_transactions.id`, as `StartSaleAt` already
  does.
- `register_id`, `device_epoch` and `device_seq`: the register's enrolment
  epoch (section 6.1) and a counter the device increments in the same local
  write that stores the sale. A new unique index on
  `pos_transactions (register_id, device_epoch, device_seq)` makes a
  collision impossible across registers and across a device replaced on the
  same register (a new enrolment is a new epoch).
- `offline_ref`: the printed number, `<register id>-<epoch>-<seq>` (for
  example `REG-01-2-000041`), the receipt's number while the sale waits.

At sync the posting transaction mints the `POS` number from its sequence and
the invoice number from the gapless counter at ADR 0005's step 8, exactly as
a live completion does. Invoices stay gapless; offline sales take their
numbers in the order they post. The receipt reprinted after sync carries
both the offline reference and the invoice number.

**Replay twice is a no op.** Every item the server has seen is recorded in
`pos_transactions` (posted, the primary key), in `pos_offline_exceptions`
(refused, a unique `sale_id` over the rows whose signature verified), or in
both once a resolution posts it. A `bad_signature` row does **not** count as
seen: a tampered copy keeps the true sale's `id`, so it must never lock that
sale out. The duplicate check reads both tables, posted first, under the
register's device lock (section 3.2):

- `id` posted: answered `duplicate`, nothing written;
- `id` only in a `bad_signature` row, and the item's signature verifies:
  processed as a new item, and the bad row is closed as `superseded` in the
  same transaction that posts or refuses the true item;
- `id` in a verified exception row: answered `refused` again with the stored
  reason and status, nothing reprocessed. Fixing the cause (adjusting stock,
  say) never lets a replay post the sale behind its exception; only the
  exception's own resolution posts it (section 3.4), so a later write off
  or late post can never act on a sale that already posted;
- `id` known but its `(register, epoch, seq)` or signature differs:
  refused as `replay_mismatch`;
- `id` new but its `(register, epoch, seq)` already used by another `id`
  whose signature verified, in either table: refused as `sequence_reused` (a
  device whose local store was lost or restored and restarted its counter
  under the same epoch). The item is written to the exception queue under
  its own `id`, never posted through the unique index, and the earlier sales
  of that sequence stay visible to the gap control.

The unique `(register_id, device_epoch, device_seq)` index spans both tables
in effect: `pos_offline_exceptions` carries the same three columns with its
own unique index, partial so that it excludes `sequence_reused` rows (which
repeat a used sequence by definition) and `bad_signature` rows (whose
sequence and `id` are not proven, so the true sale of that number and `id`
can still be recorded; the unique `sale_id` is partial the same way), and
the check above reads both before either insert. A `sequence_reused` item
that a manager accepts posts with its device sequence left null on the sale
row and kept on its exception row, so the posting never collides with the
index. The batch request also carries an `Idempotency-Key` (ADR 0001 section
9), which covers a retried request inside the key store's retention window;
the row guards cover every replay after it.

### 3. Posting on sync

`POST /api/v1/pos/sync` returns, rebuilt (the body changes; a listed
contract change in `CONTRACT-CHANGES.md`). Each sale in a batch is processed
in `device_seq` order and posted in **one transaction**, through the
completion's own transaction body (`completeSaleTx`), never through the
three step path of today's `sync.go`, which is deleted.

#### 3.1 The offline verdict

`completeSaleTx` is refactored to take an optional **offline verdict**: the
snapshot id, the recorded tax (rate, source, cents), the recorded business
date, the recorded line prices and the outcome of the section 6 checks
(signature, chain, snapshot prices, caps). With no verdict, the body is
unchanged and every live sale behaves exactly as today. With a verdict,
each check that judges whether the clerk **may** make the sale is replaced
by its snapshot verification (section 6.2), and each check that guards
whether the server **can post** it stays live. Every check the body makes
today (`complete.go`, `completeSaleTx` and the helpers it calls), and the
side it falls on:

| Check today | Code | With an offline verdict |
|---|---|---|
| Sale branch hold and named customer wall | `holdSaleBranch` (in `CompleteSale`, before the transaction, on an existing row), `namedCustomer` | Replaced and live: no sale row exists yet, so the sync's wall is the register's branch (`GetRegisterBranch`, before any item runs, section 6.3) plus `namedCustomer` inside the transaction; a failure is `out_of_wall` to the queue |
| Sale status OPEN or HELD | `InvalidStateTransition` | Not reached: the sale row is inserted new in the same transaction |
| The session open, `FOR SHARE` | `LockTillSession`, `session_closed` | Live: `session_closed` to the queue (except under a late post resolution, 3.4) |
| Tax quote fingerprint | `tax_quote_stale` | Replaced: no provider call; the recorded tax is verified against the snapshot (3.3) |
| Customer exemption and branch rate | `CustomerExempt`, `BranchTaxRate`, `tax_rate_not_configured` | Replaced: the snapshot's exemption flag and rate |
| Nothing due, tender short, change only from cash, legs sum | `no_tender_due`, `insufficient_tender`, `change_from_cash`, `legs_do_not_sum` | Live: arithmetic over the recorded figures; a failure is `tender_mismatch` to the queue |
| Walk in on account | `walk_in_account` | Live (the device never offers it; a failure is a forged item) |
| PO required | `po_required` | Replaced: the snapshot's PO flag; a PO present when the snapshot required one |
| Buyer roster | `checkBuyer` | Replaced: the snapshot's roster |
| Account standing and credit limit, override spend | `checkOnAccount` (`credit_hold`, `credit_limit`, `cash_only_terms`) | Replaced: the snapshot verdict was `allowed` and the sale is within the account's offline room (6.2); no override is spent |
| Check limit and ID rule, override spend | `checkTenderLimits` (`check_over_limit`, S04's ID rule) | Replaced: the snapshot's check limit, ID rule and check cap; no override is spent |
| Store credit | `walk_in_store_credit`, `LockOpenCreditMemos`, `store_credit_short` | Not reached: store credit is refused offline (section 1); an item carrying it is `tender_not_offline` to the queue |
| Card tenders and terminal legs | `terminal_leg_spent` and the charge path | Not reached: card is refused offline; `tender_not_offline` |
| Stock units and stock on hand | `checkLineStockUnits`, `IssueQty`, `insufficient_stock` | Live: `insufficient_stock` to the queue |
| The journal's period | `gl.ErrPeriodClosed` (`period_closed`) | Live: `period_closed` to the queue |

Line pricing is never rerun: the recorded lines are stored as they are, and
`priceLine` (the engine) is not called for an offline sale.

#### 3.2 Locks

Inside the transaction, in ADR 0005 section 11's order, with two kinds
added ahead of its step 1 (ADR 0005 section 11 is amended to name them in
the change that accepts this record; its step 0, the queue request rows, is
unchanged and never taken here):

- **0a. the register's device row** (`pos_register_devices`, section 6.1)
  `FOR UPDATE`: two syncs of one register serialize, so the duplicate,
  sequence and chain checks see a settled high mark. Taken by the sync and
  by an exception resolution, first;
- **0b. the exception row**, `FOR UPDATE`, taken only by a resolution of
  that exception (3.4), after 0a;
- **1. the sale rows**, in id order: the offline sale's row, inserted with
  its client `id` (a conflict on the primary key is the duplicate answer,
  the transaction rolls back), its lines as recorded, the session it names,
  `offline_ref`, epoch, sequence, snapshot id and signature digest; and,
  when the sale names `replaces_sale_id` (5.3), that live sale's row `FOR
  NO KEY UPDATE`. Then the usual steps: the session `FOR SHARE`, the credit
  advisory lock for an account tender, payments, invoice, inventory in
  product order, the customer row, the gapless mint, the journal, the audit
  row and the outbox last.

**The replaced cart.** If the replaced sale is no longer OPEN or HELD, or
any terminal intent of it is still open, the offline sale is refused to the
queue as `replaced_sale_settled` and **nothing is voided**: a manager reads
both (3.4). If it is OPEN or HELD with no open intent, it moves to a new
status, `ABANDONED`, in the same transaction, with an audit row naming the
offline sale that replaced it. This is a new transition (OPEN or HELD to
ABANDONED), not `VoidSale`, which refuses anything but a completed sale
(`core/internal/pos/void.go`); slice O1 adds the status to the sale status
CHECK (migration 105) and the wire enum. A cart that was never completed
holds no payment, stock move or entry, so the transition moves no money and
writes no reversal. The void list and void reports exclude it, the till
report and Z report count it nowhere (it carries no tender), and the sale
list shows it as "abandoned: replaced by offline sale `<offline_ref>`".

**Stock and ledger move once.** They move only inside that transaction,
which also inserts the sale row; a replay cannot reach them. The offline
device moves nothing on the server.

#### 3.3 Tax and the business day

**Tax as at the sale.** The device computes tax with the snapshot's rate and
the exemption flag as known, and records per sale the rate, the source
(`BRANCH_RATE` or `EXEMPT`) and the tax cents, per line the taxable flag.
The server checks the arithmetic (`salesdoc.TaxAt` of the taxable total at
the recorded rate equals the recorded cents) and that the rate and exemption
are the snapshot's, then books them with a new tax source `OFFLINE_RATE` (or
`EXEMPT`), so a report and the later provider commit (ADR 0005 section 3)
can find them. Slice O1 widens the tax source CHECK constraints (migrations
092 and 094) for the new value. No provider call is made at sync, so
nothing waits as "pending" any more. A branch that uses the external
provider must also hold a default rate (`locations.default_tax_rate`) to
enable offline selling: counter pickups are taxed where the branch is.

**The business day is the sale's own.** The invoice date and the journal's
entry date are the business date of the till session the sale was made in
(`sessionBusinessDate`), not the sync's date. The device's own clock is
recorded (`client_created_at`, migration 044) for audit and checked only to
lie between the session's `opened_at` and the sync.

#### 3.4 What the server refuses, and the exception queue

The rule: the sync never re-decides a sale the clerk was allowed to make
under the rules as the device knew them (3.1). It posts it. It refuses only
what it cannot post or cannot verify:

| Case at sync | Outcome |
|---|---|
| The account's standing changed since the snapshot (hold, limit, terms), the check limit lowered, the ID rule tightened, PO required or the buyer roster changed | Posts. The sale was within what the device was given. An account alert row says an offline sale went on while the account changed. |
| A price changed since the snapshot | Posts at the price as known (verified, section 6.2). |
| The tax rate changed since the snapshot | Posts at the recorded rate. |
| Stock short (`insufficient_stock`) | Refused to the queue. Stock never goes below zero (`IssueQty`); the goods have left, so the count is wrong somewhere. |
| The till session is closed (`session_closed`) | Refused to the queue. |
| The branch day is closed | Refused to the queue. |
| The business date falls in a closed fiscal period (`gl.ErrPeriodClosed`, `period_closed`) | Refused to the queue; resolved by late post. |
| A product, customer or register that no longer exists or is outside the syncing user's wall (`out_of_wall`) | Refused to the queue. |
| The replaced live cart is settled or has an open terminal intent (`replaced_sale_settled`) | Refused to the queue; nothing voided. |
| A price or tax that does not match the snapshot, a cap exceeded, a tender not allowed offline, tenders that do not add up, a bad signature, a broken chain, a revoked device, an unknown snapshot, a replay mismatch, a reused sequence | Refused to the queue (sections 2 and 6). |
| A sequence gap, or a link that does not meet the chain head (`broken_chain`) | The items after it are held without posting and re-offered automatically on each sync until the link arrives (chain paragraph below); a gap still open at the till close is a manager item, and an accepted gap leaves a `sequence_gap` item naming the missing numbers. |

**A refused sale is never dropped.** It is written, in the same request but
its own transaction, to `pos_offline_exceptions`: the sale's `id` (unique),
register, epoch and sequence (unique together, `sequence_reused` and
`bad_signature` rows excepted, section 2), the signed sale as received, the
reason code and message, the money taken by tender, the session and business
date, status `open`. The posting transaction that found the refusal has
rolled back and released 0a, so the exception write takes the register's
device row (0a) again and **repeats the duplicate check** of section 2
under it: if a concurrent sync of the same batch posted or recorded the
item meanwhile, it writes nothing and answers as that check does. It then
takes the named session `FOR SHARE` (as `completeSaleTx` does), so a
`CloseTill` holding the session `FOR UPDATE` either sees the exception (and
refuses, below) or waits for it. Nothing of the sale posts. The response
lists it under `refused`; the device marks the item "with a manager" and
keeps it until the server says it is resolved.

**The chain and the high mark advance only over items whose signature and
chain link both verify**, posted or refused, strictly in sequence order
(the item is the high mark plus one and its link equals the chain head),
inside the transaction that records it (the posting or the exception
write). A refused but verified sale is a fact of the register's history, so
the next sale's link verifies against it.

- An item whose signature fails advances nothing and is recorded as
  `bad_signature`; the true sale of that sequence and `id` is still
  accepted when it arrives (section 2).
- A verified item whose link does not meet the chain head (its predecessor
  is missing, or arrived only as a bad copy) goes to the queue as
  `broken_chain` without advancing, status `held`. **Held items are
  re-offered automatically**: at the end of every sync of the register, and
  whenever an item advances the mark, the server takes the held
  `broken_chain` item at the high mark plus one, checks its link again and,
  if it now meets the head, processes it through section 3 as if it had
  just arrived, in one transaction: if it posts, the held row reads
  `relinked`; if it is refused for a reason of its own (stock, say), the
  held row itself takes that reason and opens for a manager, so no second
  row repeats its `id` or sequence. Either way the mark advances over it
  and the server repeats for the next. No manager is needed for the
  relinking itself.
- A held item whose link has not arrived when its till session is to close
  is an open exception like any other: `CloseTill` refuses
  (`offline_exception_open`) and a manager resolves it, either by write off
  or by accept, which advances the mark over it with a `sequence_gap` item
  naming the missing number.
- A `sequence_reused` item repeats a number already placed and advances
  nothing.

**Who resolves it, where.** A manager (an owner or admin other than the
cashier, never a machine key: the rule `override.go` already applies) at the
branch, on a new **Offline exceptions** list on the branch's end of day page
(CP-09's branch day screen) with a count on the counter. Each resolution
runs in one transaction under locks 0a and 0b and moves the exception to
`resolved` with the sale's posting, if any. The resolutions:

- **Retry**, after fixing the cause: for stock, an inventory adjustment with
  its own reason and audit (the existing adjustment path), then the post
  runs through 3.1 to 3.3.
- **Accept**, for a price, tax or cap mismatch the manager has checked: the
  sale posts at the recorded figures, with an audit row naming the manager
  and the reason, the same shape as an override.
- **Late post**, for a closed session, day or fiscal period: the sale posts
  on the earliest open business date in an open period, keeping its own
  date on the sale. It attaches to **no** till session (`till_session_id`
  null, the closed session kept in a new `offline_session_id` column for
  the record): the verdict's late post flag skips the session check and the
  drawer attach of `completeSaleTx`, so no open drawer's figures change.
  For the **cash and check portions only**, one adjustment entry moves that
  amount from the till over/short account to cash (`AccountCodeOverShort`
  to `AccountCodeCash`, the pair `postTillOverShortByMethod` uses), because
  the counted drawer already booked that money as an over; an account
  tender put nothing in the drawer and posts to the receivable alone.
  Reopening the day (the existing reopen) and retrying is the alternative
  where the deposit is not yet matched.
- **Write off**, for a forged or unverifiable sale: nothing posts to sales;
  any cash counted in the drawer stays where the count put it (over/short),
  with the manager's reason.
- **Settled replaced cart** (`replaced_sale_settled`): the manager compares
  the two. If the live sale completed (the answer was lost), the offline
  sale is the same sale and is written off with no posting, and any money
  the clerk took twice is refunded online through the live sale's own
  return or refund. If the live sale is still unsettled for another reason,
  the manager settles it first (its terminal intent through the existing
  resolve route) and retries.

**How the money already taken is accounted.** Normally a register drains
its queue before its till closes (section 4), so an exception is resolved
while its money is still in the drawer: `CloseTill` refuses
(`offline_exception_open`) while an open exception names the session, and
`CloseBranchDay` refuses while one names a session of the day or earlier,
beside the unsettled sessions it already refuses. The late post path above
is for the device that never came back before the count (lost, broken,
restored later).

### 4. Branch settings and what the clerk sees

**The policy.** A new table `pos_offline_policy` (one row per branch: an
`enabled` flag, the tenders allowed offline from `CASH`, `CHECK`,
`ACCOUNT`, a cap per sale, a check cap, a per account cap, a queue total
cap and a queue count cap, all in cents or counts, with `updated_by` and a
revision), over database wide defaults in `system_settings`
(`pos.offline.enabled`, `pos.offline.tenders`, `pos.offline.*_cents`,
`pos.offline.max_queued`), resolved branch, then default, then none, as
`GetVariancePolicy` resolves the variance threshold. **Offline selling is
off unless set**: no row and no default means the sync answers 409
`money_never_queues` for that branch, so the code keeps its meaning where a
dealer has not opted in. Writing the policy is an admin act with an audit
row (the `pos.till.variance_policy` pattern) and a small counter settings
form. The form says, beside the per account cap, that the worst case
exposure on one account is that cap times the number of the branch's
registers offline at once, and shows that figure for the branch's enrolled
registers.

The device holds the policy as it was in its snapshot. It stops taking
offline sales when any cap is reached, and it never sells offline into a
business day other than its till session's.

**What the clerk sees while offline** (the work shell's existing
`gable-shell-banner` in its offline mode, plus the counter's own strip):

- the banner "Offline: sales are kept on this register and sent when the
  connection returns", the count of waiting sales and their total, and the
  room left under the queue caps;
- on the cart, a standing line "Offline prices: promotions, quantity
  breaks and job prices are not applied", and each line's price as it will
  be charged;
- card and store credit tenders greyed with "needs the connection"; the
  account tender only for cached accounts, with the room left on that
  account; customer search over the cached accounts only;
- returns, voids, deposits, overrides, the till close and movements
  disabled with the same words;
- the receipt printed with "OFFLINE SALE", the offline reference, each
  line's price charged, "prices and tax as known on this register:
  promotions, quantity breaks and job prices not applied offline", and no
  invoice number;
- on reconnect, the sync runs by itself and the strip reads "Sent 12, 1
  needs a manager"; the till close button waits until the queue is empty
  and says why.

### 5. The client

**5.1 Where the queue lives. Recommended: the Tauri work shell, in its Rust
core.** A new module in `tauri/core` keeps the queue in a SQLite file in the
app's data directory, each row sealed with an AEAD key held in the OS
keychain under the existing custody rules (`custody.rs`: fail closed, no
file fallback). The page reaches it through named Tauri commands behind the
same origin fence as the token commands (`fence.rs`). The device signing key
(section 6.1) lives in the keychain beside it. The work shell's capability
file grants only these commands, by name.

**A missing store refuses offline selling.** If the keychain holds an
enrolment (the device key and its epoch) but the queue file is missing,
unreadable or older than the high mark the server last confirmed to it, the
shell refuses offline selling and says "This register must be enrolled again
before it can sell offline". A manager re-enrols online (a new epoch,
section 6.1). This keeps a restored or replaced store from restarting the
sequence under the old epoch; the server's `sequence_reused` (section 2) is
the backstop if a device does it anyway.

Options weighed:

- **A browser tab (IndexedDB).** Rejected for selling. The browser may evict
  or the user may clear site storage, a tab has no stable device identity,
  two tabs share one store, and a signing key in IndexedDB is weaker than
  the keychain. A browser tab keeps today's honest refusal: the counter in a
  tab sells online only and says so.
- **Rust side store in the shell (recommended).** Survives a page crash, a
  reload and a shell restart; the keychain gives custody; one store per
  installed register.
- **`wa-sqlite` over OPFS in the page** (the field shell's planned store).
  Durable enough in a shell, but the key custody stays in the page. It is
  the better fit for the field shell's drafts, which carry no money; the
  counter's queue does.

**5.2 The page while offline.** The work shell frames `/pos` from the
server's origin (`web/apps/work-shell/src/config.ts`), so a reload with no
connection would show nothing. The desk bundle gains a service worker that
precaches its assets for the `/pos` route only (no API response is cached
by it). Slice C1 proves it loads offline in each shell's webview; where a
webview refuses a service worker, the shell bundles the desk build as its
fallback start page. Either way, a counter already open never navigates
away while offline.

**5.3 The offline cart.** While online, the counter keeps working as today
(the server holds the cart). When the connection drops mid sale, what the
page does depends on what it last sent:

- **No completion or terminal call in flight.** The open server cart's
  lines (already read by the page) become the local cart, and the offline
  sale names it in `replaces_sale_id`. The sync locks that cart and marks
  it `ABANDONED` only if it is still OPEN or HELD with no open terminal
  intent; otherwise the offline sale goes to the queue as
  `replaced_sale_settled` and nothing is voided (section 3.2).
- **A completion or a terminal call sent and not answered.** The cart is
  **result unknown**: the server may have committed the sale, or the device
  may have approved a card, with the answer lost. The page never converts
  it. It shows "Result unknown: this sale may already be complete. Wait for
  the connection", keeps the cart and its tender on screen, and offers no
  offline tender for it. When the connection returns, the page reads the
  sale and its terminal intents first and follows what they say, never
  re-sending without looking it up (the rule CP-05 sets for the card
  terminal).
- **New offline sales while a result unknown cart stands.** The clerk may
  sell offline meanwhile, but every new offline sale on the register
  carries the unknown cart's id in `replaces_sale_id` **by default**, so a
  re-ring of the same goods is caught by the settled check of section 3.2
  (if the live completion committed, the offline sale is refused as
  `replaced_sale_settled` and nothing posts twice). The clerk can clear it
  only by declaring "different customer" on the sale, which is a signed
  field of the sale (`different_customer: true` with the unknown cart's id
  in `unknown_cart_id`), shown on the posted sale and listed for the
  manager on the branch day page beside the unknown cart's outcome. The
  first offline sale that keeps the default is the one the sync matches
  against the cart; later ones that keep it are refused as
  `replaced_sale_settled` once the cart is matched, for the manager to
  compare.
- **The till close waits** while a result unknown cart stands on the
  register: the close button says "A sale's result is unknown: reconnect to
  resolve it first", and the page clears the state only after it has read
  the cart and its intents online.

A sale started offline is local from its first line. The local cart prices,
taxes and checks tenders from the snapshot only, with the same arithmetic
package the counter already uses for display, and stores the sale, signed,
in one local write with its sequence number before the receipt prints.

**5.4 The snapshot.** A new `GET /api/v1/pos/offline/snapshot?register_id=`
returns, for the register's branch:

- the products with their unit, price unit, taxable flag and on hand at the
  branch (the `GetProductCatalog` query, branch scoped);
- a price sheet keyed by price key: `walkin`, the engine's answer for the
  walk in customer (the customer row `priceLine` prices a walk in sale for,
  so the offline walk in price is the online one), and, for each cached
  account, the server's own resolution of ADR 0006 rungs 1, 3 and 4 per
  product (its contract, else its category rules, account then profile then
  level, else its level), precomputed in the product's price unit. Rungs 1,
  3 and 4 take no quantity, so each row is exactly what the engine answers
  online for any quantity when no rung 2 rule applies;
- the branch tax rate;
- the cached accounts (the branch's accounts with an `allowed` verdict: id,
  name, account number, terms kind, credit limit, open receivable, check
  limit, PO required, buyer roster, exempt flag);
- the database check limit, the ID rule and the offline policy.

It is stored server side (`pos_offline_snapshots`: id, register, epoch,
content, digest) so a sale can name it and the server can verify against
it.

Only rung 2 is skipped offline: its pricing rules, which are promotions,
quantity breaks and job overrides (job prices). An offline sale prices a
cached account at its precomputed sheet price and a walk in sale at the walk
in sheet price. The strip and the receipt name the three skipped kinds
(section 4), and the clerk can tell an account customer the sale may be
adjusted later by credit memo, online, if the dealer chooses. The sheet
grows with cached accounts times products; slice O2 measures it on the demo
seed and, if it is too large, limits the precomputed rows to the products
each account has bought, with the level price as that account's fallback.
Where a line prices at that fallback, the strip adds "Level price: this
account's own price is not on this register" beside the line, and the
receipt marks the line "level price".

**The price key is bound to the customer.** A sale's price key is not the
device's choice: for a cached account it is that account's own key (its
precomputed row, or, only where the sheet has no row for that product, its
level's row); for the walk in customer it is `walkin`; nothing else. The
server derives the key from the sale's customer and the snapshot, and a
line priced under any other key the snapshot holds is `price_unverified`,
so a signed sale cannot price an account at a lower key.

**Freshness.** The device fetches a snapshot when the till opens (offline
selling requires a snapshot fetched since the session opened), and refetches
whenever the snapshot route's ETag moves, checked while online (ADR 0001
section 11's revision headers). A snapshot older than the session never
sells.

### 6. Security

**6.1 The register's device.** A manager enrols a register's device online:
the shell generates an Ed25519 key pair in the keychain and sends the public
key; the server records `pos_register_devices` (register, epoch, public
key, enrolled by, revoked at, synced high sequence and chain head). A new
enrolment increments the epoch and revokes the previous key. Every offline
sale is a canonical JSON document signed by the device key, carrying the
previous sale's digest (a hash chain per register and epoch). The server's
chain head and high sequence advance over every item whose signature
verifies, posted or refused (section 3.4), so a refused sale never breaks
the chain for the sales after it.

- **A stolen queue file** is sealed with a keychain key; without the
  device's keychain it is unreadable. Replayed as is, every sale is a
  duplicate (section 2).
- **A tampered sale** (a price, a line, a tender changed in the file) fails
  its signature and is refused as `bad_signature`.
- **A deleted sale** (cash taken, sale removed from the queue) leaves a
  sequence gap and a broken chain: every later sale of the register is held
  as `broken_chain`, the till cannot close, and a manager must resolve the
  gap, which leaves a `sequence_gap` item naming the missing number (section
  3.4). This is the control that makes an offline cash sale as visible
  as an online one.
- **A lost queue store** under a live enrolment: the shell refuses offline
  selling until re-enrolled (section 5.1); a reused sequence is refused as
  `sequence_reused` (section 2).
- **A stolen device with its keychain** can sign sales, and the same
  keychain holds the signed in user's tokens. Revoking the enrolment refuses
  its later posts as `device_revoked` (to the exception queue, never
  dropped), and the revoke route also ends the web sessions signed in on that
  register's shell where the sign in method can revoke them (ADR 0004). Where
  it cannot, the stolen tokens reach only what that user may do online,
  which disabling the user stops; offline, the caps alone bound what the
  device can put on accounts.

**6.2 Prices in the queue.** The server never reprices an offline sale. It
verifies each line's unit price against the price the named snapshot gave
for that product and the sale's price key, and each line's extension by the
arithmetic of ADR 0006 section 5.4. A mismatch refuses the sale as
`price_unverified` (exception queue, the manager accepts or writes off). A
sale naming a snapshot that was not issued to its register and epoch is
refused as `snapshot_unknown`. The same check covers the tax rate, the
exemption, the check limit, the ID rule, the PO and buyer rules, and every
cap.

**Caps across batches.** A forged sale with a valid signature could stay
under a cap within one batch and exceed it across two. So the server checks
each cap against everything the register has recorded under the same
snapshot, in both tables and every batch: the account room is the
snapshot's room less the sum of that register's offline account tenders on
that account naming that snapshot, posted or refused, and the queue total
and count caps sum the same way. Over the cap is `cap_exceeded`. A new
snapshot restarts the sums, because its open receivable already counts the
sales that posted; but the register's **open account exceptions** (refused,
not yet resolved, under any snapshot) are in no open receivable and a Retry
or Accept can still post them, so the new snapshot's room for each account
is computed less them, and the server's check subtracts them the same way.
The stated bound (the per account cap times the registers offline) then
holds across snapshots.

**6.3 Who may sync.** The sync is a person's act, never a machine key
(`refuseMachineKey`): a signed in user holding the counter's write grant
whose branch wall reaches the register's branch (today's
`GetRegisterBranch` check, kept), with the batch signed by the register's
current device key. A user cannot sync another register's queue without that
register's device. The session named must belong to the register, and each
sale's cashier must equal that session's `cashier_id`
(`core/migrations/075_till_sessions_pos_payments.sql`): a cashier change is
online only (section 1), so an offline sale has no other cashier. No role
history is needed.

**6.4 Audit rows and events** (ADR 0003: every event in the posting
transaction, written last):

- `pos.offline_sale.posted` (audit, per sale: offline reference, epoch,
  sequence, snapshot id, signature digest, sync batch) and the existing
  `pos_transaction.completed` event with `offline: true` in its data;
- `pos.offline_sale.refused` (audit and event `pos_offline.exception_opened`,
  per exception, in its own transaction);
- `pos.offline_exception.resolved` (audit and event, naming the manager, the
  resolution and the reason);
- `pos.register_device.enrolled` and `pos.register_device.revoked`;
- `pos_sync_log` keeps one row per batch, extended with refused and gap
  counts.

### 7. The tests that prove it

Server (Go, a named test database, `CI=true`):

- **Replay twice:** a batch of three sales synced twice moves the ledger,
  stock, invoices and payments exactly once; the second answers three
  duplicates; a third replay of one sale with a changed signature answers
  `replay_mismatch`.
- **Ledger and stock once under contention:** the same batch, holding one
  item that is refused for stock, posted by two concurrent requests beside
  a live completion of the same customer (pool size 4, three contenders,
  the recipe's rule) posts each good sale once and writes exactly one
  exception row for the refused item, with no deadlock.
- **Numbering under two registers offline:** two registers each queue
  sales offline with overlapping sequence numbers, sync interleaved and
  concurrently: every sale gets one `POS` number and one invoice number, the
  invoice series stays gapless, no unique index fires.
- **Refused syncs:** one test per refusal row of section 3 (stock short,
  session closed, day closed, price unverified, tax mismatch, cap exceeded,
  bad signature, revoked device, unknown snapshot) proves the exception row,
  no posting, and the till close and day close refusals while it is open;
  one per resolution proves the posting and, for the late post, the ledger
  balancing to the money counted.
- **No re-decision** (3.1): after the snapshot, each of these changes and
  the sale still posts at the recorded figures, with no override spent: the
  account put on hold, its limit lowered, its terms set to due on receipt;
  the customer's check limit and the database check limit lowered; the ID
  rule tightened; PO required switched on (the sale carried none) and the
  buyer removed from the roster; a price and the tax rate changed.
- **Duplicates across both tables** (section 2): a batch whose first sale
  was refused for stock, replayed after the stock was adjusted, answers
  `refused` again and posts nothing; only the retry resolution posts it, and
  the exception then reads `resolved`. A new `id` under a used `(register,
  epoch, seq)` answers `sequence_reused` and writes an exception row, never
  a posting.
- **The chain over refused items:** a batch of three where the second is
  refused (stock short) posts the first and third with their chain links
  verified, and the high mark ends at the third.
- **A tampered copy never locks out the true sale:** a batch whose second
  item is a tampered copy (same `id`, changed price) records it as
  `bad_signature`, leaves the high mark at the first, and holds the third
  as `broken_chain`. The next sync carries the true second: it posts, the
  bad row reads `superseded`, and the held third is re-offered
  automatically in the same sync, posts, and its held row reads `relinked`,
  with no manager act and the mark at the third. A variant where the true
  second never arrives: the till close refuses `offline_exception_open`
  and a manager's accept leaves one `sequence_gap` item.
- **Caps across batches:** two batches under one snapshot whose account
  tenders are each under the per account room and together over it: the
  sale that crosses the room is `cap_exceeded`; the queue total and count
  caps the same way.
- **Exception write against the till close:** the exception write and
  `CloseTill` on the same session race at pool size 4 with three contenders
  (two syncs refused for stock, one close): either the close refuses
  `offline_exception_open` or the exception lands after a close it waited
  for and is then a late post case; never a closed drawer that missed an
  exception written before it.
- **Settled replaced cart (P1 case):** a live cart whose completion commits
  on the server while the answer is cut, then an offline sale naming it:
  the sync refuses the offline sale as `replaced_sale_settled`, the live
  sale stays COMPLETED with its one posting, nothing is voided, and the
  ledger and stock moved once. The same with an open terminal intent on the
  cart. An OPEN cart with no intent becomes `ABANDONED` in the posting
  transaction, with no reversal, and is absent from the void report and the
  till report.
- **Re-ring under a result unknown cart:** with the cart's completion
  committed and its answer lost, a new offline sale keeping the default
  `replaces_sale_id` is refused as `replaced_sale_settled` (one posting
  only); a second offline sale declared "different customer" posts, and
  the branch day page lists it with the declaration beside the cart's
  outcome.
- **Fiscal period closed:** a sale whose business date falls in a closed
  period is `period_closed` in the queue and posts by late post into an
  open period.
- **Business day:** a sale made in an earlier business day's session posts
  with that session's date on the invoice and the journal.
- **Policy off:** a branch with no policy still answers 409
  `money_never_queues`, keeping the existing wire tests green.
- **Money suites green:** the existing `core/internal/pos` money tests
  (completion, voids, returns, tills, end of day, terminal) and
  `core/internal/app/serve` stay green unchanged, apart from the S6 tests
  that assert the 409 for an opted in branch.

Client (vitest for the queue and cart; Playwright in
`web/apps/work-shell`, with the specs beside `web/e2e/pos.spec.ts`):

- **Connection cut mid sale:** start a sale online with two lines, cut the
  network (`context.setOffline(true)`), add a line, tender cash, print; the
  receipt reads offline and names the skipped price kinds; restore the
  network; the sale posts once, the server cart reads `abandoned`, the
  strip reads "Sent 1".
- **Connection cut during the completion:** let the complete request reach
  the server and drop its answer (route interception, then
  `setOffline(true)`): the cart shows "Result unknown" and offers no
  offline tender, and the till close button waits; on reconnect the page
  reads the sale, shows it complete, and nothing is queued. Default case: a
  new offline sale re-rung for the same goods carries the cart's id
  without the clerk doing anything; on sync, one posting, one
  `replaced_sale_settled` exception, nothing voided. Declared case: a sale
  marked "different customer" carries no `replaces_sale_id`, posts, and
  shows the declaration to the manager.
- **Store missing:** with an enrolment in the keychain and the queue file
  removed, the counter refuses offline selling and asks for re-enrolment.
- **Refused sync shown:** a sale queued offline against stock the test then
  removes appears as "needs a manager", and the till close waits.
- **Disabled tenders:** card and store credit are disabled offline, and an
  account over its room is refused at the counter.
- **Reload offline:** the counter reloads with no network and the queue is
  still there (proves 5.2 and the Rust store).
- **Two registers:** two work shell contexts on two registers, both offline,
  both sync; the receipts' offline references and the invoice numbers are
  unique.

Rust (`tauri/core` unit tests): sealing and unsealing, a refused keychain
fails closed, the sequence never repeats across a crash between write and
print, a missing or stale store under a live enrolment refuses offline
selling, the fence refuses a foreign origin.

### 8. The build plan

In order; sizes in dev hour equivalents. Every pull request is a money,
data and security change and gets the project's money review.

| Slice | What | Size |
|---|---|---|
| O1 | Migration: `pos_offline_policy`, `pos_register_devices`, `pos_offline_snapshots`, `pos_offline_exceptions` (unique `sale_id`, unique register, epoch and sequence); on `pos_transactions` the epoch, sequence, offline reference, snapshot id, signature digest, `offline_session_id` and the unique index; the `OFFLINE_RATE` tax source and its CHECK constraints widened; the `ABANDONED` sale status in the status CHECK and the wire enum. Policy read and write routes with audit. Contract rows. | 12 to 18 |
| O2 | Device enrolment and revocation routes (with the session revocation of 6.1); the snapshot route with the precomputed account price sheet, its storage and its size measured on the demo seed. | 16 to 26 |
| O3 | The sync rebuilt: the offline verdict in `completeSaleTx` (3.1), verification (signature, chain over every verified item, snapshot price, tax, caps across batches), the replaced cart lock and check, duplicates across both tables (bad signature rows not counted, superseded), the strict chain with held `broken_chain` items re-offered automatically, the price key bound to the customer, `sequence_reused`, the exception write repeating the duplicate check under 0a and the session lock, open account exceptions subtracted from the room, the OPEN to ABANDONED transition, the delete of the three step path in `sync.go`. Concurrency, numbering and no re-decision tests. | 36 to 54 |
| O4 | The exception list, its resolutions (retry, accept, late post with no session and the cash and check reclass, write off, settled replaced cart) under locks 0a and 0b, and the till close and day close refusals with their race test. | 18 to 30 |
| C1 | The Rust queue store, keychain keys, signing, commands and fence in `tauri/core` and `tauri/gable-work`; the refusal to sell offline when the store is missing under a live enrolment; the service worker for `/pos` proven in each webview. | 18 to 30 |
| C2 | The counter's offline mode in `POSTerminal.ts`: the snapshot cache, the local cart, the result unknown cart with the default `replaces_sale_id`, the "different customer" declaration and the waiting till close, pricing, tax and tender rules, the banner and strip, the offline receipt, the sync driver, the drain before close. `work-offline` keeps its fence and names the counter's queue as the one exception. | 32 to 48 |
| C3 | The manager screens: offline exceptions on the branch day page, the counter's offline settings, device enrolment. | 12 to 20 |
| P1 | The Playwright and two register proofs of section 7, the walk in `docs/parity/WALKS.md`, the parity row and the superseded documents of the Status updated. | 10 to 16 |
| | **Total** | **154 to 242** |

O1 to O4 can run beside C1. C2 needs O2 and C1; C3 needs O4; P1 is last.

**Nothing must come before the section's other slices.** The ruled order
(slices S01 to S11 first) suits this design: S04 (the ID rule and check
limit), S01 (credit hold) and S11 (cash rounding, CP-18) each change what
the snapshot must carry and what the local cart must compute, so building
after them avoids building the offline rules twice. Card is out of scope
offline, so CP-05 does not gate it.

## Consequences

- The counter keeps selling cash, check and cached account sales when the
  connection drops, on an enrolled work shell register at a branch that
  opted in. A browser tab still sells online only.
- Money taken offline is never lost silently: it posts once, or it waits in
  a manager's queue that blocks the till and day close until it is resolved.
- An offline sale prices at the engine's answer as known for its customer
  without rung 2; it may cost a customer a promotion, quantity break or job
  price the online counter would have given, and the strip and receipt say
  so. Dealers who mind can keep offline off or set a low cap.
- Account exposure can exceed a credit limit by the per account cap times
  the registers offline at once. This is the price of selling on account
  offline, set per branch.
- Card and store credit stay online only. A dealer who wants card offline
  needs a processor that documents store and forward; Run does not.
- The invoice series stays gapless and server minted; offline receipts
  carry a register reference until sync. A jurisdiction that requires an
  invoice number on the customer's receipt at the moment of sale would need
  per register invoice series (ADR 0005 section 4.1's named escape), a
  later decision.
- Tax on offline sales is booked as charged at the branch rate. A dealer on
  the external provider sees offline sales tagged `OFFLINE_RATE`, ready for
  the provider commit when that lands.
- The Tauri work shell gains its first local data store and its first
  signing key; its capability file grows by the queue commands only.
- The S6 tests that assert 409 for every batch narrow to branches with
  offline off; the `money_never_queues` code stays in the enum with that
  meaning.
- New surface to maintain: four tables, three routes, one shell module, one
  manager screen. Estimated at 154 to 242 dev hour equivalents.

**Open items** (left for later decisions):

- Per register invoice series, for a jurisdiction that requires an invoice
  number on the receipt at the moment of sale (ADR 0005 section 4.1).
- Card offline, if the card processor later documents store and forward.
- Promotions, quantity breaks and job prices offline, if dealers ask for
  them.
