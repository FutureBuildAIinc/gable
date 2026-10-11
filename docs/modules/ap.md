<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Accounts payable

The accounts payable module records vendor bills (the money a yard owes
its vendors) and the payments the yard makes against them. A bill is the
header the desk enters (the vendor, the vendor's own number, the date,
the due date, the tax) and the priced lines (the goods or services, the
quantity, the unit price, the GL account the line's money lands on). A
payment names the bills it applies to (in the order the desk pays them)
and locks that order on the database so concurrent payments cannot
deadlock or both spend the same open amount (a repeated bill id in one
payment's `invoice_ids` is refused as `invoice_ids[i] (appears twice)`
by `PayRequest.Parse`, and over-payment as `exceeds_open` by
`PayVendor`; a repeated payment, the same request sent twice, is a
replay caught by the `Idempotency-Key`, not by these checks; a payment
naming bills of two currencies is refused as `currency_mismatch`). Both
acts post one journal entry
inside one transaction: a payment that posts `DR 2010 / CR 1010` in the
bills' currency, and an
approve that builds the entry from the lines and credits the whole
total to `2010`.

This module is the work of C4-1b (item C4-1b: the AP fixes of ADR 0008
section 7.4 on the wire contract of ADR 0001, plus migration 106). The
Go code is in `core/internal/ap/`. The table migrations are in
`core/migrations/106_ap_wire_contract.sql` (the `ap_wire_contract` item
the ADR planned as 098 in the C4-1b section, renumbered to 106 at merge).

## What it does in a yard

A yard buys lumber, fasteners and concrete from vendors on terms. The
bill lands on the desk from the carrier or by email. The clerk enters
the bill through `POST /ap/invoices`: the line money and the tax are
integers in cents and the unit price is an integer at scale four. The
bill starts pending, owing its whole total, with Gable's own number
(prefix `AP-`); the vendor's own number is `vendor_invoice_number`
(1 to 64 characters, unique per vendor).

When the bill is right, the desk approves it through
`POST /ap/invoices/{id}/transitions` with `to: "approved"`. The
approve loads the lines, builds the journal entry in the act's
transaction (a debit leg per line on the line's account, the tax spread
across the line accounts pro rata with the last line taking the
remainder, the credit to `2010` for the whole total), and a posting
failure fails the act: a closed fiscal period is the `period_closed`
blocker; a chart of accounts code that is missing is the
`account_missing` blocker; a line with no account is the
`line_needs_account` blocker; an entry that does not balance is the
`entry_unbalanced` blocker.

When the bill is wrong, the desk voids it through the same route with
`to: "voided"` and a reason. A pending bill voids with nothing posted.
A bill's credit is live exactly when the POSTED entries of its
`VENDOR_INVOICE` entry and that entry's whole reversal chain leave a net
amount on `2010`, and the void decides on that net, never on one step of
the chain: a reversal that a later reversal put back leaves the credit
live at any depth, and a chain whose POSTED entries cancel holds none.
The net is summed per currency, never across two (the trial balance never
sums across two), so a chain holding two currencies whose whole sums
cancel still reads live in the currency that holds the amount, however
the chain arose: legacy rows included. A
bill whose entry holds no live credit (one that prices to zero, or one
the GL routes have already undone, linked or not) voids with nothing to
reverse; a linked live entry with no reversal row is reversed whole
through `gl.PostReversal`, in the entry's own currency. A bill with
money applied is refused with `has_payments` (the payments have to be
undone first); a partial bill is refused there before the void reaches
the linked-entry checks. A bill holding a live entry it is not linked to
is refused with `entry_not_linked` (the duplicate is named when the bill
was posted twice), one whose linked entry does not balance with
`entry_unbalanced`, and one whose linked entry is live while its one
reversal row is already taken (the row is not posted, or the chain was
reinstated behind it, so no new reversal can post) with
`reversal_not_posted`: all stay approved and payable until a later
repair item (no route today links an entry to a bill). Every refusal
writes nothing, and a reversal insert that loses a race on the one
reversal index answers the same `reversal_not_posted` 409, never a 500.
The approve decides on the same liveness: a pending bill that already
holds a live journal entry is refused `entry_not_linked` naming it,
rather than posting a second credit no bill owes.

Payments land as the desk cuts the checks. `POST /ap/payments` takes
the vendor, the amount in cents, the method (`CHECK`, `ACH` or
`WIRE`), the date and the bills in the order the desk pays them. The
act locks the named bills in id order (so two callers can never race
on the same bill) and applies only to bills in `approved` or `partial`
(a `pending`, `paid` or `voided` bill is the `invoice_not_approved`
blocker). A bill of another vendor is a 400 naming `invoice_ids[i]`, a
repeated id a 400 naming its index. More than the named bills still
owe is the `exceeds_open` blocker and writes nothing. The named bills
must share one currency: a payment spanning two is the
`currency_mismatch` blocker naming the bills that differ, and writes
nothing. The applied sum posts `DR 2010 / CR 1010` in the bills'
currency inside the act's transaction. Each settled
bill writes `vendor_invoice.partial` (when more is still owed) or
`vendor_invoice.paid` (when this payment closes it).

The aging (`GET /ap/aging`) is the desk's open balances by vendor, in
int64 cents buckets (`current`, `past_30`, `past_60`, `past_90`,
`total`). A bill that is paid or voided owes nothing anywhere. The
payments list (`GET /ap/payments`) carries the wall through the bills
its payments apply to. A payment that spans branches shows at its
whole amount to a caller who sees any one of its bills (the wall
filters which payments, not their amounts).

## Routes

Every route below is in `core/api/fragments/ap.yaml`; the registered
handles are in `core/internal/ap/handler.go`; the route census
(`core/api/ROUTES.txt`) lists each one under the `internal/ap`
package.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/ap/invoices` | Cursor list of vendor invoices, newest first by `(created_at, id)`; `vendor_id`, `po_id`, `status` filter; `total` under `include=total`; revision as ETag. |
| POST | `/api/v1/ap/invoices` | Enter a vendor invoice (the bill starts pending, owing its whole total); `Idempotency-Key` rides the standard header; `X-Branch-Id` header; money in `*_cents` and the line unit price in `*_ten_thousandths`. |
| GET | `/api/v1/ap/invoices/{id}` | One bill with its lines and revision as ETag; held to the caller's branch wall (another branch's bill is a 404). |
| POST | `/api/v1/ap/invoices/{id}/transitions` | Approve (`to: "approved"`, only from pending) or void (`to: "voided"`, reason required) a bill; on the revision (`If-Match` or body `revision`); `Idempotency-Key` rides the standard header. |
| GET | `/api/v1/ap/payments` | List vendor payments, filtered by `vendor_id`; held to the caller's branch wall through the bills each payment applies to. |
| POST | `/api/v1/ap/payments` | Record a vendor payment; `amount_cents` int64; `method` enum `CHECK`, `ACH` or `WIRE`; `invoice_ids` in the order the desk pays them; `Idempotency-Key`. |
| GET | `/api/v1/ap/aging` | Aging buckets in int64 cents by vendor (`current`, `past_30`, `past_60`, `past_90`, `total`); held to the caller's branch wall. |

Every route above (reads included) sits behind `scoped("admin", "owner", "finance")`
(`core/internal/app/serve/serve.go:679`), the branch wall
(`BranchGuard.CheckPayloadBranch` at `core/internal/ap/service.go:156-167`)
and the user's branch context. A bound machine key without the right
scope is `403 forbidden` (ADR 0002); the audit row carries the refused
scope.

## The main resources

### The bill

`ApVendorInvoice` (see `core/api/fragments/ap.yaml`
`components.schemas.ApVendorInvoice`,
`core/internal/ap/model.go` `Invoice`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The bill id. |
| `number` | text | Gable's own number, prefix `AP-`, gapped sequence. |
| `vendor_id` | UUID | The vendor this bill is from. |
| `vendor_name` | text | Snapshot of the vendor name. |
| `branch_id` | UUID | The branch the bill belongs to. |
| `vendor_invoice_number` | text | The vendor's own number, 1 to 64 characters, unique per vendor. |
| `currency` | ISO 4217 | The bill's currency; an omitted currency comes from the `currency.default` setting (`USD` where the setting is absent). |
| `invoice_date` | date | YYYY-MM-DD. |
| `due_date` | date | YYYY-MM-DD. |
| `po_id` | UUID, nullable | The purchase order this bill was matched to (FK since migration 106). |
| `subtotal_cents` | integer | Sum of `line_total_cents`. |
| `tax_cents` | integer | The bill's tax. |
| `total_cents` | integer | `subtotal_cents + tax_cents`. |
| `amount_paid_cents` | integer | The total the vendor has been paid against this bill. |
| `amount_open_cents` | integer | The total the bill still owes, read only. |
| `status` | lowercase enum | `pending`, `approved`, `partial`, `paid`, `voided`. |
| `approved_by` | UUID, nullable | The approver. |
| `approved_at` | timestamp, nullable | When it was approved. |
| `notes` | text | At most 2000 characters. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `gl_entry_id` | UUID, nullable | The entry the approve posted (set on approve; kept after a void). |
| `created_at` | timestamp | RFC 3339 UTC. |
| `lines` | array of `ApVendorInvoiceLine` | The priced lines, in position order. |

`ApVendorInvoiceSummary` (`fragments/ap.yaml`) is the same head, the
list item and the shape the full document embeds, without `lines`.

### The line

`ApVendorInvoiceLine` (`fragments/ap.yaml`,
`core/internal/ap/model.go` `InvoiceLine`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The line id. |
| `position` | integer | The line's place on the bill. |
| `description` | text | 1 to 256 characters. |
| `quantity` | decimal string | A plain decimal with at most 4 fraction digits, more than zero. |
| `unit_price_ten_thousandths` | integer | Never negative; the value `httpx.Price` on the Go side. |
| `line_total_cents` | integer | The extension priced by the server through `httpx.Extend` with the pair 1 and 1 (AP lines carry no conversion pair until the purchase line link carries units, ADR 0008 sections 1 and 7.3). |
| `gl_account_id` | UUID | The GL account the approve entry debits for this line; required on create. |
| `purchase_order_line_id` | UUID, nullable | The purchase order line this was matched to (since migration 106; today's lines are not linked). |
| `product_id` | UUID, nullable | The product this line refers to. |
| `po_freight_charge_id` | UUID, nullable | The carrier freight line this line links to (written from package C's freight rule on, ADR 0008 7.3). |
| `created_at` | timestamp | RFC 3339 UTC. |

### The payment

`ApPayment` (`fragments/ap.yaml`, `core/internal/ap/model.go`
`APPayment`). The payment routes keep their today shape until their own
conversion (ADR 0008 section 11); the field set below is that shape.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The payment id. |
| `vendor_id` | UUID | The vendor the payment is to. |
| `vendor_name` | text | Snapshot of the vendor name, filled by the list join. |
| `batch_id` | UUID, nullable | The batch this payment is in (today's shape). |
| `amount` | integer | Cents (the response); the request uses `amount_cents`. |
| `method` | enum | `CHECK`, `ACH`, `WIRE` (UPPERCASE storage; the wire is accepted case insensitively). |
| `check_number` | text, nullable | For a check. |
| `reference` | text, nullable | Free text. |
| `payment_date` | date | YYYY-MM-DD. |
| `status` | enum | `PENDING`, `COMPLETE`, `VOIDED` (UPPERCASE storage, today's wire). |
| `created_at` | timestamp | RFC 3339 UTC. |

`ApPaymentApplication` (the join row, `model.go` `APPaymentApplication`)
links a payment to the bill it applies to with `id`, `payment_id`,
`invoice_id`, `amount` (cents) and `created_at`. It is a storage row,
not on the wire: no AP route returns it; `PayVendor` writes it inside
the payment's transaction and the response only returns the
`ApPayment`.

`ApAgingSummary` (the aging row, `fragments/ap.yaml`) carries
`vendor_id`, `vendor_name`, `current`, `past_30`, `past_60`, `past_90`,
`total`, every bucket int64 cents.

### Money and quantity conventions

Money is integer cents under `_cents`. The unit price is integer at
scale four under `_ten_thousandths`. The line quantity is a decimal
string with at most four fraction digits. The extension is the
quantity multiplied by the unit price and by the pair 1 and 1 (AP
lines carry no conversion pair until the purchase line link carries
units), rounded once to cents half away from zero through
`httpx.Extend`. The create body takes `tax_cents` as int64 and the line
`unit_price_ten_thousandths` as int64; a non-integer `amount_cents` on
a payment is a 400 naming `amount_cents` (never silent rounding).

### Create body fields

`ApCreateVendorInvoiceRequest` (`fragments/ap.yaml`): `vendor_id`
required, optional `branch_id` (defaults from the caller's context
branch or the `default_branch_id` setting; a branch the caller may not
target is a 403 naming `branch_id`), `vendor_invoice_number` (1 to 64
characters, unique per vendor; a duplicate is a 409 naming the field),
`invoice_date`, `due_date` (both YYYY-MM-DD), optional `po_id`
(an unknown PO is a 400 naming `po_id`), optional `currency`
(an omitted currency comes from the `currency.default` setting, which
the create's `resolve` reads (`USD` where the setting is absent); a
request's currency
must be an ISO 4217 code of three capital letters), `tax_cents`, `notes`
(at most 2000 characters) and `lines` (at least one line, the same
set of fields as the line schema minus `id`, `position` and
`created_at`, with the addition of the optional `purchase_order_line_id`
and `product_id`). The server prices each line and never accepts a
status: a created bill starts `pending`.

`ApCreatePaymentRequest` (`fragments/ap.yaml`): `vendor_id` required,
`amount_cents` int64 (> 0 and `<= 999999999999`), `method` enum
(`CHECK`, `ACH`, `WIRE`), `payment_date` (YYYY-MM-DD), optional
`check_number`, `reference` and `invoice_ids` (the bills in the order
the desk pays them; repeated ids are 400 naming the index).

## Lifecycle and transitions

The wire vocabulary is lowercase; the storage column is UPPERCASE, the
mapping at `model.go` `storageStatus` and `wireStatus`.

The bill's `transitions` map (model.go lines 53 to 62) is the edges the
module allows:

| From | To | Reason | Event |
|---|---|---|---|
| `pending` | `approved` | The bill is right; the GL entry can post. | `vendor_invoice.approved` |
| `pending` | `voided` | The bill was wrong; nothing posted yet. | `vendor_invoice.voided` |
| `approved` | `voided` | The bill is wrong; the entry is reversed whole. | `vendor_invoice.voided` |
| `partial` | none | Partial carries payments; the bill cannot be voided while money has left. |  |
| `paid` | none | Closed. |  |
| `voided` | none | Terminal. |  |

A payment sets a bill's `status` to `partial` (when more is still owed)
or `paid` (when this payment closes it) at the moment of the apply.
Transitions `to: "pending"`, `to: "partial"` and `to: "paid"` from
`POST /ap/invoices/{id}/transitions` are refused with
`invalid_state_transition`; a payment is the only writer for those.

### Approve

`POST /ap/invoices/{id}/transitions` with `to: "approved"` and the
revision (in the body or in `If-Match`). The act runs in one
transaction:

1. Lock the bill (`repo.Lock`); check the revision under the lock.
2. Refuse `409 invoice_not_pending` if the bill is not `pending`.
3. Refuse `409 has_paid_amount` if `amount_paid_cents` is not zero: a
   payment applies only to an `approved` or `partial` bill, so a paid
   amount on a `pending` bill is a legacy or hand edited row whose
   `amount_open` sits where migration 106 clamped it, below the total;
   the approve would post the whole total while `amount_open` stands
   still, leaving money in `2010` no bill owes. The row is left for the
   operator to mend by hand (a stated limit).
4. Refuse `409 entry_not_linked` if the bill already holds a live
   journal entry, naming it by its number and id: a legacy `pending`
   row whose posting ran before the status moved (the base's sync could
   post first; migration 106 links only `approved` or `partial`, so the
   row carries its entry linked by hand or not at all) already holds its
   credit in `2010`, and the approve would post a second one no bill
   owes. Live is the chain net of the void (below), never one reversal
   level; nothing is written.
5. Load the lines with their account codes (`repo.LinesWithCodes`).
6. Refuse `409 line_needs_account` if any line's code is empty, naming
   the line's position.
7. Build the entry (`postInvoiceEntry`): a debit leg per line on the
   line's account, the tax spread across the line accounts pro rata to
   their extensions with the last line taking the remainder
   (`tax_cents * line_total_cents / subtotal_cents`, the remainder accumulated
   into the last line), the credit to `gl.AccountCodeAP = "2010"` for the
   whole total.
8. Post the entry through `gl.PostEntry` in the same transaction. The
   posting can raise `period_closed` (closed fiscal period),
   `account_missing` (chart of accounts code absent) or
   `entry_unbalanced` (debits and credits do not balance); each is
   mapped to a 409 by the module and fails the act (ADR 0008 7.4).
9. Mark the bill approved with the linked `gl_entry_id` and the
   approver (the caller's UUID when the actor is a parseable UUID,
   else nil for an in-process caller; `s.ApproveInvoice` passes the
   caller-given approver id).
10. Write the audit row, then the event
   (`vendor_invoice.approved`) in the same transaction (ADR 0003
   section 2).

The `Idempotency-Key` rides the standard header (ADR 0001 section 9).
The transaction commits or rolls back whole: a posting refusal writes
nothing.

### Void

`POST /ap/invoices/{id}/transitions` with `to: "voided"`, the revision,
and a `reason` (required, at most 500 characters). The act runs in one
transaction:

1. Refuse `400 reason required` if `reason` is empty.
2. Lock the bill; check the revision under the lock.
3. Refuse `409 has_payments` if `amount_paid_cents > 0`, naming the
   cents applied; the payments have to be undone first.
4. Refuse `409 invalid_state_transition` if the bill's `from` does not
   allow `voided` (the `partial` and `paid` rows in the map).
5. Refuse `409 entry_not_linked` if the bill holds a live
   `VENDOR_INVOICE` journal entry it is not linked to (`repo
   .HasPostedInvoiceEntry`, the predicate migration 106 links by, made
   chain wide): a credit may stay in `2010` with nothing to reverse,
   whatever the bill's `total_cents` says now (a hand edit may even have
   zeroed the total after the approve), so the bill keeps its status (a
   stated limit).
6. If the bill has a `gl_entry_id`, read the ledger through `repo
   .LinkedEntryLedger` and post the reversal through `gl.PostReversal`
   with the void time as `EntryDate` and the reason. `PostReversal` can
   raise `period_closed` (mapped to 409) or `entry_unbalanced` (the
   removed sync's one leg shape, mapped to 409 `entry_unbalanced`); the
   latter also keeps its status (a stated limit).
7. Mark the bill voided; write the audit row, then the event
   (`vendor_invoice.voided`) in the same transaction.

Liveness, for the linked and the unlinked arm alike, is the net the
POSTED entries of the entry's whole reversal chain leave on `2010` (the
trial balance's own lens): an entry whose chain nets nothing holds no
credit and voids with nothing to reverse (a bill that prices to zero
posted nothing, and a legacy bill whose posting was swallowed or whose
entry the GL routes have already undone holds nothing to undo), a live
entry with no reversal row is reversed whole in the entry's own
currency, and a live entry whose one reversal row is already taken (the
row is not posted, or a reversal of a reversal put the credit back
behind it, so no new reversal can post under the one reversal index) is
refused `409 reversal_not_posted` naming the shape, nothing written. A
reversal insert that loses a race with a concurrent GL reverse meets
the one reversal index and answers the same 409, never a 500.

### Pay

`POST /ap/payments`. The act runs in one transaction:

1. Create the payment row with `status: "COMPLETE"` (`repo.CreatePayment`).
2. Lock the named bills in `id` order (`LockForPayment` with the
   payment's vendor); id order makes every contender take the locks in
   one order. A bill of another vendor returns no row and the act
   refuses `400 invoice_ids[i]` naming the index. A repeated id in
   `invoice_ids` is `400 invoice_ids[i] (appears twice)`.
3. Refuse `409 invoice_not_approved` if any locked bill is `pending`,
   `paid` or `voided`, naming the bill's `number` and `status`.
4. Apply the money in `in.InvoiceIDs` order (the desk's order). Each
   application writes an `APPaymentApplication` row and updates the
   bill's `amount_paid_cents`. A bill that closes on this payment
   triggers `EventPaid`; one that still owes triggers `EventPartial`.
5. Refuse `409 exceeds_open` if the applied sum was less than the
   request's amount, naming the cents asked and the cents open.
6. When at least one bill was settled, post the entry
   `DR 2010 / CR 1010` for the applied sum through `gl.PostEntry`,
   `Source: VENDOR_PAYMENT`, `SourceRefID: &payment.ID`. The posting
   can raise `period_closed` (mapped to 409) or `account_missing`
   (mapped to 409); each fails the act and writes nothing.
7. Write the audit row, then the events
   (`vendor_invoice.partial` or `vendor_invoice.paid` for each settled
   bill) in the same transaction.

The `Idempotency-Key` rides the standard header.

The payment's `VendorID` is the one the body names; the bills in
`invoice_ids` must match (a different vendor's bill is a 400). The
`method` enum is `CHECK`, `ACH` or `WIRE` (UPPERCASE); the date is
YYYY-MM-DD; the money is int64 cents in `amount_cents`. The payment
row's `amount` field on the response stays cents, the response's
`status` stays the UPPERCASE storage enum, and the rest of the request
shape (uppercase methods, `payment_date`) keeps its today form until
the payment routes' own conversion.

## Postings

The approve builds the entry from the lines. Every line contributes
one debit leg on its own account (`gl.Leg` with `AccountCode` set to
the line's account, `Description` set to the line's description, and
`Debit` set to the line's extension); for the tax, each line's debit
is increased by its pro rata share
(`tax_cents * line.LineTotalCents / subtotal`), and the last line's
debit absorbs any remainder. The credit leg is one
`gl.Leg` with `AccountCode: gl.AccountCodeAP = "2010"`,
`Description: "Accounts Payable"` and `Credit: TotalCents`. The entry
posts with `Source: VENDOR_INVOICE`, `SourceRefID: &bill.ID`, the bill's
currency and the bill's `invoice_date` as `EntryDate`, the
`Memo: "Vendor invoice " + bill.Number`. The returned `entry.ID` is
stored on the bill as `gl_entry_id`.

The pay entry is one entry: `DR 2010 / CR 1010` for the applied sum
(`AmountCents - remaining`), `Source: VENDOR_PAYMENT`,
`SourceRefID: &payment.ID`, `Memo: "Vendor payment " + payment.ID`. It
posts only when `len(locked) > 0`.

The void posts a reversal of the bill's `gl_entry_id` through
`gl.PostReversal`, with `EntryDate: time.Now()` (the void time) and the
call's `reason`. The reversal carries the original entry's own currency
(the entry priced its credit into that currency when it posted; the
bill's current currency, which a hand edit may have changed, never
moves it). The bill's `gl_entry_id` stays set (the link is kept
for the audit trail).

`AccountCodeAP = "2010"` (Accounts Payable) is declared at
`core/internal/gl/postentry.go:22`. `AccountCodeCash = "1010"` (Cash)
is declared at `core/internal/gl/service.go:35`.

## Events the module writes

Every mutation writes the event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md) section 2). The
event constants and the strings they hold:

| Event | Constant (source) | Written at |
|---|---|---|
| `vendor_invoice.created` | `EventCreated` (`core/internal/ap/service.go:50`) | `core/internal/ap/service.go:302` (`s.event(EventCreated, "", &inv.Summary)` in the create transaction) |
| `vendor_invoice.approved` | `EventApproved` (`service.go:51`) | `service.go:409` (`s.event(EventApproved, StatusPending, after)` in the approve transaction) |
| `vendor_invoice.partial` | `EventPartial` (`service.go:52`) | `service.go:670` (`s.event(typ, from, after)`, `typ` is `EventPartial` when `paid` is false) |
| `vendor_invoice.paid` | `EventPaid` (`service.go:53`) | `service.go:670` (`typ` is `EventPaid` when the apply closed the bill) |
| `vendor_invoice.voided` | `EventVoided` (`service.go:54`) | `service.go:545` (`s.event(EventVoided, from, after)` in the void transaction) |

The events carry a small summary (the body's `data`), never the
document: `number`, `vendor_invoice_number`, `vendor_id`, `branch_id`,
`status`, `from_status`, `revision`, `total_cents`, `amount_open_cents`,
`currency` (the body of `event()` at `service.go:189-198`). They ride
the transactional outbox with `EntityType: "vendor_invoice"`, the bill id
and the bill's `branch_id`.

A `POST /ap/payments` writes one event per settled bill: a bill that
still owes after the payment writes `vendor_invoice.partial`; one that
the payment closed writes `vendor_invoice.paid`. The payment row itself
does not have its own event today.

## Scopes, roles and keys

A machine key reaching the AP routes needs `ap:read` for `GET` and
`HEAD`, and `ap:write` for every other method (ADR 0002; the segment
is the first path segment under `/api/v1/`, here `ap`; the segment
table is in `pkg/middleware/machinekey.go`). The user guard at the serve
layer is `admin`, `owner`, `finance` (registered at
`core/internal/app/serve/serve.go:679`:
`ap.NewHandler(apSvc).RegisterRoutes(mux, scoped("admin", "owner", "finance"))`).
No other module's guard reaches an AP route. A key without the right
scope is `403 forbidden`; the audit row carries the refused scope (ADR
0002 section 5). A machine key skips the role guard entirely: ADR 0002
section 4 holds that a key has no roles and `RequireRole` skips its
check when the request authenticated with a key, so a key is admitted
on the scope check and the role guard never runs.

A branch bound machine key (ADR 0007 section 5.5) is pinned to its one
branch: a `GET` and a `POST` with `X-Branch-Id` set to another branch
are `403 forbidden`; a payload `branch_id` outside the key's bound is
`403 forbidden` naming `branch_id` (the AP code names the field
`branch_id`). The payload rule (`BranchGuard.CheckPayloadBranch`,
`service.go:156-167`) is what `POST /ap/invoices` applies; it is the
only AP route with a payload branch. `POST /ap/payments` carries no
`branch_id` and is held to the wall through the bills it names (a bill
outside the caller's wall is a 400 naming `invoice_ids[i]`,
`LockForPayment` at `repository.go:307-319`). The branch resolves in
three steps (`service.go:263-278`): the payload `branch_id`, the
caller's context branch, or the `default_branch_id` setting. An omitted
`currency` is left empty by `Parse` and the create's `resolve` reads the
`currency.default` setting for it (`USD` where the setting is absent),
on the route and in process alike.

## Stated limits

These are limits the module lives with until a later repair item.

- A bill that holds a live `VENDOR_INVOICE` journal entry it is not
  linked to is refused a `to: "voided"` with `409 entry_not_linked`: the
  credit may stay in `2010` with nothing to reverse, so the bill keeps
  its status. This is the legacy unlinked bill (its posting was
  swallowed, it holds the removed sync's one leg credit, or its total
  was hand edited after the approve); live means the entry's whole
  reversal chain nets a nonzero amount on `2010` over its POSTED
  entries. A legacy bill with no live entry at all voids with nothing to
  reverse, owing nothing through the ledger.
- A bill whose linked entry is live while its one reversal row is
  already taken (the row is not posted, or a reversal of a reversal put
  the credit back behind it) is refused the same void with `409
  reversal_not_posted`: no new reversal can post under the one reversal
  index, so the chain must be mended by hand first.
- A pending bill that already holds a live journal entry is refused the
  approve with `409 entry_not_linked` naming it: the row already holds
  its credit in `2010`, and mending the link by hand comes before any
  approve.
- A bill linked to an entry that does not balance (the removed sync's
  one leg shape, debit absent) is refused the same void with `409
  entry_unbalanced`. Its reversal would mirror the imbalance into
  another entry, so the bill stays approved and payable.
- No route links an entry to a bill; the link `gl_entry_id` is
  populated only by the module's own approve today.
- A payment that spans branches shows at its whole amount to a caller
  who sees any one of its bills: the wall filters which payments are
  visible, not the amounts of the ones shown.

## Money ceilings (the create-time limits)

Each is a `400 validation_failed` naming the field that tipped it over.
Nothing is written.

- `tax_cents` past `999999999999`.
- `lines[i].unit_price_ten_thousandths` past `999999999999`.
- `lines[i].quantity` when its line extension passes `999999999999`
  cents.
- `lines` when their subtotal passes `999999999999` cents.
- `tax_cents` when the lines fit but the lines and the tax together
  pass `999999999999` cents.
- `amount_cents` on a payment past `999999999999`, and one that is not
  more than zero.

The constants live at `core/internal/ap/input.go:261-265`:
`maxBillCents = 999_999_999_999` and
`maxUnitPriceTenThousandths = 999_999_999_999` (and `maxPaymentCents =
999_999_999_999` at line 257). The columns are NUMERIC(12,2) for money
and NUMERIC(12,4) for unit price, so the ceiling is the column's.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md):
  the list envelope, cursors, the error envelope (3), the lowercase
  status vocabulary (6), money in `_cents` (7), unit prices in
  `_ten_thousandths` (7a), the `Idempotency-Key` header (9), the
  revision concurrency rule and the 428 / 409 `stale_revision`
  preconditions (11), the field name casing (12).
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md): the
  segment rule for `ap:read` and `ap:write` (section 2).
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md):
  the events table, the ordering guarantee and the writer that runs as
  the last statement of the mutation's transaction (sections 1, 2, 3,
  5).
- [`docs/adr/0007-drafts-links-and-confirm-gated-scopes.md`](../adr/0007-drafts-links-and-confirm-gated-scopes.md):
  section 5.5, the branch bound key and the payload `branch_id`
  refusal (the rule the create's `branch_id` applies).
- [`docs/adr/0008-inventory-identity-and-vendor-intake.md`](../adr/0008-inventory-identity-and-vendor-intake.md):
  section 7.4 (the AP fixes C4-1b makes: approve loads the lines, the
  entry posts through `gl.PostEntry` in the approve transaction and a
  failure fails the act, `VOIDED` is reachable by a transition that
  reverses the entry, a payment applies only to `approved` or
  `partial` invoices locked in id order); section 12 (the
  `ap_wire_contract` migration, numbered 098 in the ADR and 106 at
  merge); section 13.1 (the C4-1b item, its builds and its tests).

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

Migration 106 brings the AP tables onto the wire contract: a
`vendor_invoices.number`, a `branch_id`, a `currency`, a status CHECK,
a `po_id` FK, `UNIQUE (vendor_id, invoice_number)`, an `amount_open`,
and `vendor_invoice_lines.position`, `purchase_order_line_id NULL`,
`product_id NULL`, `unit_price` widened to NUMERIC(12,4) and
`po_freight_charge_id NULL`. The demo seed keeps no vendor bill on
`AP`. Then, with the seeded branch (dev mode needs no
`Authorization` header, ADR 0002 section 3):

```
curl -X POST http://localhost:8080/api/v1/ap/invoices \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 1' \
  -H 'Content-Type: application/json' \
  -d '{"vendor_id":"<seeded vendor uuid>","vendor_invoice_number":"INV-1001","invoice_date":"<YYYY-MM-DD>","due_date":"<YYYY-MM-DD>","tax_cents":0,"lines":[{"description":"2x4x8 SPF","quantity":"100","unit_price_ten_thousandths":41250,"gl_account_id":"<seeded 5020 account uuid>"}]}'
```

The body names the seeded vendor, a 1 to 64 character
`vendor_invoice_number`, the two dates as YYYY-MM-DD, optional `notes`
and at least one line (`description`, `quantity` as a decimal string,
`unit_price_ten_thousandths` int64 and the line's `gl_account_id`; the
currency may be named and otherwise comes from the `currency.default`
setting). The approve then follows:

```
curl -X POST http://localhost:8080/api/v1/ap/invoices/<id>/transitions \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Idempotency-Key: 2' \
  -H 'If-Match: "1"' \
  -H 'Content-Type: application/json' \
  -d '{"to":"approved"}'
```

The transition's ETag response is the new revision; a `to: "voided"`
follows the same shape and adds a `reason`. The payment needs the
same scope, an `Idempotency-Key`, `amount_cents` int64 (> 0 and
`<= 999999999999`), a `method` of `CHECK`, `ACH` or `WIRE`, the date
as YYYY-MM-DD, and `invoice_ids` in the order the desk pays them.

The posting and rejection proofs in
`core/internal/ap/service.go` `postInvoiceEntry`, the wiring tests in
`core/internal/ap/wire_test.go` and the transaction proofs in
`core/internal/ap/tx_test.go` (the revision race, the wallet
transitions, the linked and unlinked voids, the money ceilings, the
across-branches payment) pin the behaviour. The migration proofs in
`migration106_test.go` pin the column types, the backfill and the
indexes.
