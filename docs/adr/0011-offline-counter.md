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
  offline store): Ruled"), the S6 row of the shell table, and the line
  "money never queues offline" under the shell rulings;
- `docs/parity/GABLE-DISTINCT.md`, row D18 (counter till) and ruling 2
  ("Money never queues and field offline");
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
sync (section 5.3). ADR 0005's Status gains the matching "superseded in part
by ADR 0011" line in the change that accepts this record.

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
  (`session_closed`), prices tax (`tax_quote_stale`, `tax_rate_not_configured`),
  checks tenders, credit (under the 1a advisory lock) and store credit,
  issues stock (`inventory.IssueQty`, which refuses to go below zero:
  `insufficient_stock`), writes the invoice with `InvoiceDate` from
  `BranchLocalDate(now)`, the payments, the journal, the audit row and the
  events last, in ADR 0005 section 11's lock order.
- **Numbers.** `POS` numbers come from the gapped `pos_transaction_number_seq`
  minted at `StartSale` (`core/internal/pos/service.go`); invoice numbers
  come from the gapless `document_counters` row minted late in the
  completion (ADR 0005 section 4.1, `core/internal/invoice/repository.go`).
  A device that cannot reach the server can mint neither.
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
  read every page Run's documentation index lists (`docs.runpayments.io/llms.txt`,
  each page as `.md`, plus `openapi.json`): the words "offline", "store and
  forward" and "deferred" appear only as an error text ("Terminal is
  unavailable: device offline or busy"), a gateway decline reason and a
  response code. **Run documents no offline, store and forward or deferred
  authorization mode for card present.**
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

**Replay twice is a no op.** The sale row is inserted inside the same
transaction that posts it (section 3), so the primary key is the durable
idempotency guard: a second post of the same `id` finds the row and answers
`duplicate` for that item without writing anything. A sale whose `id` is
known but whose `(register, epoch, seq)` or signature differs is refused as
`replay_mismatch` (section 6). The batch request also carries an
`Idempotency-Key` (ADR 0001 section 9), which covers a retried request
inside the key store's retention window; the row guard covers every replay
after it.

### 3. Posting on sync

`POST /api/v1/pos/sync` returns, rebuilt (the body changes; a listed
contract change in `CONTRACT-CHANGES.md`). Each sale in a batch is processed
in `device_seq` order and posted in **one transaction**, through the
completion's own transaction body (`completeSaleTx`, refactored to accept a
recorded tax and a recorded business date instead of computing them), never
through the three step path of today's `sync.go`, which is deleted.

Inside the transaction, in ADR 0005 section 11's order with one step added
at the front:

0. the register's device row (`pos_register_devices`, section 6.1) `FOR
   UPDATE`: two syncs of one register serialize, so the sequence and chain
   checks see a settled high mark;
1. insert the sale row with its client `id` (a conflict on the primary key is
   the duplicate answer, the transaction rolls back), its lines as recorded,
   the session it names, `offline_ref`, epoch, sequence, snapshot id and
   signature digest; then the usual steps: the credit advisory lock for an
   account tender, payments, invoice, inventory in product order, the
   customer row, the gapless mint, the journal, the audit row and the outbox
   last.

**Stock and ledger move once.** They move only inside that transaction,
which also inserts the sale row; a replay cannot reach them. The offline
device moves nothing on the server.

**Tax as at the sale.** The device computes tax with the snapshot's rate and
the exemption flag as known, and records per sale the rate, the source
(`BRANCH_RATE` or `EXEMPT`) and the tax cents, per line the taxable flag.
The server checks the arithmetic (`salesdoc.TaxAt` of the taxable total at
the recorded rate equals the recorded cents) and that the rate and exemption
are the snapshot's, then books them with a new tax source `OFFLINE_RATE` (or
`EXEMPT`), so a report and the later provider commit (ADR 0005 section 3)
can find them. No provider call is made at sync, so nothing waits as
"pending" any more. A branch that uses the external provider must also hold
a default rate (`locations.default_tax_rate`) to enable offline selling:
counter pickups are taxed where the branch is.

**The business day is the sale's own.** The invoice date and the journal's
entry date are the business date of the till session the sale was made in
(`sessionBusinessDate`), not the sync's date. The device's own clock is
recorded (`client_created_at`, migration 044) for audit and checked only to
lie between the session's `opened_at` and the sync.

**What the server refuses, and what it does not.** The rule: the sync never
re-decides a sale the clerk was allowed to make under the rules as the
device knew them. It posts it. It refuses only what it cannot post or cannot
verify:

| Case at sync | Outcome |
|---|---|
| The account's standing changed since the snapshot (hold, limit) | Posts. The sale was within the room the device was given. An account alert row says an offline sale went on while the account changed. |
| A price changed since the snapshot | Posts at the price as known (verified, section 6.2). |
| The tax rate changed since the snapshot | Posts at the recorded rate. |
| Stock short (`insufficient_stock`) | Refused to the exception queue. Stock never goes below zero (`IssueQty`); the goods have left, so the count is wrong somewhere. |
| The till session is closed (`session_closed`) | Refused to the exception queue. |
| The branch day is closed | Refused to the exception queue. |
| A product, customer or register that no longer exists or is outside the syncing user's wall | Refused to the exception queue. |
| A price or tax that does not match the snapshot, a cap exceeded, a bad signature, a broken chain, a revoked device, a replay mismatch | Refused to the exception queue (section 6). |
| A sequence gap left at the end of the batch | The sales post; one `sequence_gap` item names the missing numbers. |

**A refused sale is never dropped.** It is written, in the same request but
its own transaction, to `pos_offline_exceptions`: the signed sale as
received, the reason code and message, the money taken by tender, the
register, session and business date, status `open`. Nothing of it posts.
The response lists it under `refused`; the device marks the item "with a
manager" and keeps it until the server says it is resolved.

**Who resolves it, where.** A manager (an owner or admin other than the
cashier, never a machine key: the rule `override.go` already applies) at the
branch, on a new **Offline exceptions** list on the branch's end of day page
(CP-09's branch day screen) with a count on the counter. The resolutions:

- **Retry**, after fixing the cause: for stock, an inventory adjustment with
  its own reason and audit (the existing adjustment path), then the post
  runs again through section 3.
- **Accept**, for a price, tax or cap mismatch the manager has checked: the
  sale posts at the recorded figures, with an audit row naming the manager
  and the reason, the same shape as an override.
- **Late post**, for a closed session or day: the sale posts on the
  earliest open business date of the branch, keeping its own date on the
  sale, and one adjustment entry moves the tender's amount from the till
  over/short account to cash (`AccountCodeOverShort` to `AccountCodeCash`,
  the pair `postTillOverShortByMethod` uses), because the counted drawer
  already booked that money as an over. Reopening the day (the existing
  reopen) and retrying is the alternative where the deposit is not yet
  matched.
- **Write off**, for a forged or unverifiable sale: nothing posts to sales;
  any cash counted in the drawer stays where the count put it (over/short),
  with the manager's reason.

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
form.

The device holds the policy as it was in its snapshot. It stops taking
offline sales when any cap is reached, and it never sells offline into a
business day other than its till session's.

**What the clerk sees while offline** (the work shell's existing
`gable-shell-banner` in its offline mode, plus the counter's own strip):

- the banner "Offline: sales are kept on this register and sent when the
  connection returns", the count of waiting sales and their total, and the
  room left under the queue caps;
- card and store credit tenders greyed with "needs the connection"; the
  account tender only for cached accounts, with the room left on that
  account; customer search over the cached accounts only;
- returns, voids, deposits, overrides, the till close and movements
  disabled with the same words;
- the receipt printed with "OFFLINE SALE", the offline reference, "prices
  and tax as known on this register", and no invoice number;
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
(the server holds the cart). When the connection drops mid sale, the open
server cart's lines (already read by the page) become the local cart; the
server side cart stays OPEN and is voided as abandoned by the sync when the
offline sale with `replaces_sale_id` naming it posts. A sale started
offline is local from its first line. The local cart prices, taxes and
checks tenders from the snapshot only, with the same arithmetic package the
counter already uses for display, and stores the sale, signed, in one local
write with its sequence number before the receipt prints.

**5.4 The snapshot.** A new `GET /api/v1/pos/offline/snapshot?register_id=`
returns, for the register's branch: the products with their unit, price
unit, taxable flag and on hand at the branch (the `GetProductCatalog`
query, branch scoped); a price sheet keyed by price key, `walkin` (ADR 0006
rungs 5 and 6) and each active customer level (rung 4), plus the contract
rows (rung 1) of the cached accounts; the branch tax rate; the cached
accounts (the branch's accounts with an `allowed` verdict: id, name,
account number, terms kind, credit limit, open receivable, check limit, PO
required, buyer roster, exempt flag); the database check limit; the ID rule;
the offline policy. It is stored server side (`pos_offline_snapshots`: id,
register, epoch, content, digest) so a sale can name it and the server can
verify against it.

Pricing rules, category rules, promotions, quantity breaks and job
overrides (rungs 2 and 3) are not applied offline: an offline sale prices at
its customer's contract, else level, else the walk in price. The receipt
says the price is as known on the register, and the clerk can tell an
account customer the sale may be adjusted later by credit memo, online, if
the dealer chooses.

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
previous sale's digest (a hash chain per register and epoch).

- **A stolen queue file** is sealed with a keychain key; without the
  device's keychain it is unreadable. Replayed as is, every sale is a
  duplicate (section 2).
- **A tampered sale** (a price, a line, a tender changed in the file) fails
  its signature and is refused as `bad_signature`.
- **A deleted sale** (cash taken, sale removed from the queue) leaves a
  sequence gap and a broken chain; the `sequence_gap` item names it to the
  manager. This is the control that makes an offline cash sale as visible
  as an online one.
- **A stolen device with its keychain** can sign sales. Revoking the
  enrolment refuses its later posts as `device_revoked` (to the exception
  queue, never dropped); the caps bound what it could put on accounts.

**6.2 Prices in the queue.** The server never reprices an offline sale. It
verifies each line's unit price against the price the named snapshot gave
for that product and the sale's price key, and each line's extension by the
arithmetic of ADR 0006 section 5.4. A mismatch refuses the sale as
`price_unverified` (exception queue, the manager accepts or writes off). A
sale naming a snapshot that was not issued to its register and epoch is
refused as `snapshot_unknown`. The same check covers the tax rate, the
exemption and every cap.

**6.3 Who may sync.** The sync is a person's act, never a machine key
(`refuseMachineKey`): a signed in user holding the counter's write grant
whose branch wall reaches the register's branch (today's
`GetRegisterBranch` check, kept), with the batch signed by the register's
current device key. A user cannot sync another register's queue without that
register's device. Each sale's cashier must be a user who held a counter
role at the branch when the session opened; the session named must belong to
the register.

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
- **Ledger and stock once under contention:** the same batch posted by two
  concurrent requests (pool size 4, three contenders, the recipe's rule)
  posts each sale once with no deadlock against a live completion of the
  same customer.
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
- **No re-decision:** an account put on hold after the snapshot, and a price
  and tax rate changed after it, still post at the recorded figures.
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
  receipt reads offline; restore the network; the sale posts once, the
  abandoned server cart is voided, the strip reads "Sent 1".
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
print, the fence refuses a foreign origin.

### 8. The build plan

In order; sizes in dev hour equivalents. Every pull request is a money,
data and security change and gets the project's money review.

| Slice | What | Size |
|---|---|---|
| O1 | Migration: `pos_offline_policy`, `pos_register_devices`, `pos_offline_snapshots`, `pos_offline_exceptions`; on `pos_transactions` the epoch, sequence, offline reference, snapshot id, signature digest and the unique index; the `OFFLINE_RATE` tax source. Policy read and write routes with audit. Contract rows. | 12 to 18 |
| O2 | Device enrolment and revocation routes; the snapshot route and its storage. | 12 to 20 |
| O3 | The sync rebuilt: verification (signature, chain, snapshot price, tax, caps), posting through `completeSaleTx` with recorded tax and business date in one transaction, duplicates, the exception write, the delete of the three step path in `sync.go`. Concurrency and numbering tests. | 28 to 44 |
| O4 | The exception list, its resolutions (retry, accept, late post, write off) and the till close and day close refusals. | 16 to 26 |
| C1 | The Rust queue store, keychain keys, signing, commands and fence in `tauri/core` and `tauri/gable-work`; the service worker for `/pos` proven in each webview. | 18 to 30 |
| C2 | The counter's offline mode in `POSTerminal.ts`: the snapshot cache, the local cart, pricing, tax and tender rules, the banner and strip, the offline receipt, the sync driver, the drain before close. `work-offline` keeps its fence and names the counter's queue as the one exception. | 28 to 44 |
| C3 | The manager screens: offline exceptions on the branch day page, the counter's offline settings, device enrolment. | 12 to 20 |
| P1 | The Playwright and two register proofs of section 7, the walk in `docs/parity/WALKS.md`, the parity row and the superseded documents of the Status updated. | 10 to 16 |
| | **Total** | **136 to 218** |

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
- An offline sale prices at contract, level or walk in price as known; it
  may cost a customer a promotion or quantity break the online counter would
  have given. Dealers who mind can keep offline off or set a low cap.
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
  manager screen. Estimated at 136 to 218 dev hour equivalents.
