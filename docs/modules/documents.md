<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Documents: the printed forms, the dealer profile and the delivery record

Every paper a yard hands over or mails is made here: the invoice a customer
is asked to pay, the pick ticket the yard works from, the customer statement,
the quote, and the purchase order that goes to a vendor. Each form carries the
dealer's identity and the identity of the yard that issued it, and each send
by email is recorded. The Go code is `core/internal/document/` (the forms and
their routes), `core/internal/dealerprofile/` (the profile and its logo) and
`core/internal/emaildelivery/` (the record). Migration 115
`core/migrations/115_dealer_profile_email_deliveries.sql` adds the two tables.
The sender itself is described in [notification.md](notification.md).

## What a form shows

Every form opens with the same block, built by one function:

- the dealer's logo (when one is set), the dealer's trading name, legal name
  when it differs, phone, email and website, from the dealer profile;
- the issuing yard's own name, street address, city, state and zip and phone,
  from the `locations` row of the document's branch;
- the form's title: `INVOICE` (`INVOICE - VOID` for a void one), `PICK TICKET`,
  `STATEMENT`, `QUOTE`, `PURCHASE ORDER`.

Invoices and statements end with the remit to block from the profile. No form
carries a fixed product name or a pay link.

Which yard a form names is the branch of the record it prints: the invoice's,
the order's, the quote's, the purchase order's, and for a statement the
customer's primary branch. A form printed without a profile, a logo or a yard
still renders what it has; a missing piece never fails a print.

| Form | Route | Roles | Shows beyond the block |
|---|---|---|---|
| Invoice | `GET /api/v1/documents/print/invoice/{id}` | admin, owner, sales, finance | bill to with the customer's address, number, date, lines with quantity, unit, price per price unit and extension, subtotal, tax, total due, remit to |
| Pick ticket | `GET /api/v1/documents/print/pickticket/{id}` | admin, owner, sales, finance | order number, date, the job's name (`Job: none` when the order has no job), the ship to site, product lines with quantity and unit; no prices |
| Statement | `GET /api/v1/documents/print/statement/{id}` (`{id}` is the customer) | admin, owner, sales, finance | per currency the opening balance, the period's subledger rows, the closing balance and the open documents; the parameters `from`, `to` and `job_id` are the AR statement route's own; remit to |
| Quote | `GET /api/v1/documents/print/quote/{id}` | admin, owner, sales, finance | prepared for, number, date, valid until, lines, freight, total |
| Purchase order | `GET /api/v1/documents/print/purchase-order/{id}` | admin, owner, purchasing | vendor, PO number, date, lines with quantity, cost and extension, total |

The purchase order has no number column yet, so the form prints `PO-` and the
first block of its id in capitals. The purchase order module's amounts are
floating point dollars, so its form rounds each figure to cents for display;
the form never stores or sums money for any other module.

Each print route follows the wall of the module that owns the record. The
invoice, order, quote and purchase order repositories filter on the caller's
context branch, so another branch's record is that module's own 404; with no
context branch the handler holds the record's own branch to the caller's
grants and answers 403. The statement holds the customer's primary branch the
same way, and a branch bound machine key reaches it only while the customer
holds its pin.

## The dealer profile

One row, `dealer_profile` (the id is pinned to 1 by a CHECK, so a second
profile cannot exist). `GET /api/v1/dealer-profile` is read by admin, owner,
sales, finance and purchasing. `PUT /api/v1/dealer-profile` is admin and owner
only, under the revision precondition of ADR 0001 (428 without `If-Match` or a
body revision, 409 `stale_revision` when stale), validated in one 400 with
every offending field (`legal_name` required, `email` an address, `website` an
http or https address). The write is one transaction: lock the row, check the
revision, update, and write an audit row (`dealer_profile.update`) holding the
before and after. A failed audit write fails the update.

The logo is `PUT /api/v1/dealer-profile/logo` with the image as the raw body
and `Content-Type: image/png` or `image/jpeg`; it is at most 524288 bytes and
its bytes must decode as the type declared (415 for another type, 400 for
bytes that are not that image, 413 for too large). It takes the same
revision precondition and writes `dealer_profile.logo`, whose audit row holds
the size and a digest, never the bytes. `GET /api/v1/dealer-profile/logo`
serves it with `X-Content-Type-Options: nosniff`. A branch bound machine key
is refused both writes (REFUSED in the allowlist) and may read.

The demo seed fills a still blank profile with an invented one and a generated
logo, and never overwrites a profile an operator has set.

## The delivery record

`email_deliveries` holds one row per customer or vendor facing send: the
recipient, the subject, the document type (`invoice`, `statement`, `quote`,
`purchase_order`), the document id (the customer id for a statement) and
number (the account number for a statement), the status (`sent` or `failed`),
the sender's answer, any error, the actor that sent it and the document's
branch. `GET /api/v1/email-deliveries` lists newest first on the list envelope
with the filters `document_type`, `status` and `document_id`, `limit`,
`cursor` and `include=total`; admin, owner and finance read it, behind the
branch wall.

## The email routes

`POST /api/v1/invoices/{id}/email`, `POST /api/v1/quotes/{id}/email` and
`POST /api/v1/ar/customers/{id}/statement/email` render the form as the print
route does, refuse a customer with no email address in words (a 400 naming
`customer`; the invoice route keeps its legacy envelope), and answer 202
`{"status":"queued"}` at once. The send runs in the background on a context
that keeps the request's actor but not its cancellation, with a time bound, a
panic guard, and the delivery row written afterwards: `sent` with the answer
the sender gave, or `failed` with its error. A failed send is therefore read
from the record, not from the response.

## Limits

- The send is not retried; a failed row is the operator's cue to send again.
- The delivery record is written after the send, so a crash between the two
  loses the row of a send that happened.
- The purchase order form is not emailed yet (the purchase order module is
  being rewritten by another item).
- There is no desk screen for the profile or the record yet; both are API
  only until their drawings land.

## Try it locally

1. `make up`, then print a form for each of two yards:
   `curl -s http://127.0.0.1:8080/api/v1/documents/print/invoice/<id> -o invoice.pdf`.
2. Read the profile: `curl -s http://127.0.0.1:8080/api/v1/dealer-profile`.
3. See the walk in [`../parity/WALKS.md`](../parity/WALKS.md) for the full
   path including the mail catcher.
