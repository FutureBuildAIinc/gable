<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Parity walks

Short scripts a person can follow on the demo seed to see each MUST row of the
[parity matrix](LBM-PARITY-MATRIX.md) working across more than one yard. Each item of the matrix's plan adds its
walk here when it merges; a row is not done until its walk passes.

## Before any walk

1. Start the whole stack: `make up` (Postgres, migrate and seed, `core serve`, `core worker` and the web image), then
   open <http://127.0.0.1:8080>. `make smoke` checks it end to end; `make down` removes it and its data.
2. The demo seed is invented data. It carries three yards, the branches `KEL-MAIN`, `WK` and `LK`, each a top level
   `BRANCH` location with its own address and tax rate.
3. The stack runs with `AUTH_MODE=dev`, which gives every request full reach. A walk that depends on who the person
   is (a yard's cashier, a finance user granted one yard) says how to act as that person; until sign in lands (the
   plan's shell track, S9 and S13) those walks run through the API with a test token, as each walk states.
4. Each walk names the rows it proves, its steps, and what each step must show. A step that fails is a bug against
   the row, filed with the walk's name.

## The walks

### M3: documents with the dealer's and the yard's identity, and a real email sender

Proves the matrix rows IX-07 (a real email sender), MY-05 (document forms with the dealer's and the yard's branding)
and, in part, CP-14 (counter documents; the receipt itself follows the counter work).

Start the stack with the mail catcher profile and the sender pointed at the catcher, from the repository root:

```
GABLE_SMTP_HOST=mailpit GABLE_SMTP_PORT=1025 GABLE_SMTP_TLS=none \
GABLE_SMTP_FROM=desk@example.test docker compose --profile mail up -d --build
```

The catcher's page is <http://127.0.0.1:8025>. Every request below goes to <http://127.0.0.1:8080>, where the web
image proxies `/api`; `AUTH_MODE=dev` gives each one the admin reach that the profile write and the delivery record
need. Save a PDF with `curl -s <url> -o form.pdf` and read it with `pdftotext form.pdf -` or any viewer.

1. Read the dealer profile: `GET /api/v1/dealer-profile`. The demo seed fills it with an invented dealer (Gable Lumber
   & Supply Ltd, a remit to block and a generated logo). It must show `legal_name`, `remit_to`, `logo_url` set and a
   `revision`; the `ETag` header carries the revision.
2. Find a customer at each of two yards: `GET /api/v1/customers?limit=200` carries `primary_branch_id`, and
   `GET /api/v1/branches` names the branches (`KEL-MAIN` Kelowna Main Yard, `WK` West Kelowna Satellite, `LK` Lake
   Country Outpost). Pick one customer of `KEL-MAIN` and one of `WK`, then one invoice of each:
   `GET /api/v1/invoices?customer_id=<id>`.
3. Print each invoice: `GET /api/v1/documents/print/invoice/<id>`. Each PDF must show the dealer's logo, name and
   contact lines at the top left and its own yard's name and address at the top right (2450 Enterprise Way for Kelowna
   Main Yard, 2475 Sawkerf Rd for West Kelowna Satellite, and not the other's), the remit to block at the end, and no
   pay link.
4. Print a statement for each customer: `GET /api/v1/documents/print/statement/<customer id>`. Each must show its own
   customer's primary yard's address, the opening balance, the period's rows, the closing balance, the open
   documents and the remit to block. A payment row shows as `-$22.70`, never `$-22.70`. Add `?job_id=<project id>`
   for a customer with a job and see the rows narrow to the job and the job's name appear.
5. Print a pick ticket for an order that has a job (`GET /api/v1/orders?limit=200` lists `job_id`):
   `GET /api/v1/documents/print/pickticket/<order id>`. It must show the order's document number (`SO-` and six digits,
   not a raw id), its yard, `Job:` followed by the job's name, and each line with its unit in brackets.
6. Email the statement of the Kelowna customer: `POST /api/v1/ar/customers/<customer id>/statement/email`. The answer
   is 202 `{"status":"queued"}`. Open the catcher: the message is there, from `desk@example.test`, with the statement
   attached and the dealer's name in the subject.
7. Read the record: `GET /api/v1/email-deliveries?document_type=statement`. The newest row must show the recipient,
   the subject, `status` `sent`, `document_number` the customer's account number, and `sender_answer` the catcher's
   own reply (`250 2.0.0 Ok: queued as ...`).
8. Email a statement for a customer with no email address (clear the address first): the answer is a 400 whose
   detail names `customer`, never a 500, and no delivery row is written.
9. Stop the catcher (`docker compose --profile mail stop mailpit`) and email the statement again: the answer is still
   202, and a second row appears in `GET /api/v1/email-deliveries?status=failed` with the connection error in `error`.
   Start the catcher again.
10. Change the profile: `PUT /api/v1/dealer-profile` with the current values and a new `trading_name`, `If-Match` set
    to the `ETag` from step 1. The answer is 200 with the revision one higher; a second `PUT` with the old `If-Match`
    is a 409 `stale_revision`. Print the invoice again: the new trading name shows.
11. Email a quote (`POST /api/v1/quotes/<id>/email`) and print a purchase order
    (`GET /api/v1/documents/print/purchase-order/<id>`): the quote arrives in the catcher; the purchase order shows
    the vendor, the placing yard's address and its lines.
