<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Parity walks

Short scripts a person can follow on the demo seed to see each MUST row of the
[parity matrix](LBM-PARITY-MATRIX.md) working across more than one yard. Each item of the matrix's plan adds its
walk here when it merges; a row is not done until its walk passes.

## The Gable Building Supply demo

A guided walk of the whole demo, one person at a time, for a visitor who has never seen Gable.

### Start it

From the repository root, one command:

```
DEMO_SEED_IMPORT=1 PAYMENT_VAULT_KEY=$(openssl rand -hex 32) make up
```

Then open <http://127.0.0.1:8080>. It builds and starts Postgres, migrates and seeds the demo dealer with its larger
invented catalogue and an invented trading history through today, and serves the API, the worker and the web app,
waiting until all are healthy. `make down` removes it and its data. Where ports 8080 or 5434 are taken, move them:
`GABLE_WEB_PORT=18080 GABLE_PG_PORT=15434`, and give the stack its own name with `STACK_PROJECT` and
`GABLE_PG_CONTAINER` (the same settings on `make down`).

`PAYMENT_VAULT_KEY` seals the keys an administrator enters in Tech Admin. The command above makes a fresh one for the
run and writes it nowhere. Without a vault key no AI key can be saved, and every AI feature says plainly that AI is off.

### Turn on AI (optional)

The walk runs without AI: a pasted material list, a text file or a spreadsheet is read without it. To turn it on:

1. Sign in as Morgan Hale, open Tech Admin from the front door (or go to `/admin`), then the AI Settings tab.
2. Choose "Anthropic (native)" as the provider, paste the Anthropic API key into the "Anthropic API key" field and
   press "Save provider".
3. Press "Test connection": it answers "Connection ok". The material list intake now also reads photos and PDFs.

The AI key is kept for this run only; after a restart, paste it again in Tech Admin. To keep it across runs instead,
start every run with the same 64 hex character `PAYMENT_VAULT_KEY` (`docs/modules/tech-admin.md`).

### The dealer

Gable Building Supply is an invented building supply dealer with three yards: Kelowna Main Yard (`KEL-MAIN`), West
Kelowna Satellite (`WK`) and Lake Country Outpost (`LK`). Each yard has its own register, stock and people. The seed
carries about 900 products, 160 contractor and homeowner accounts and 30 vendors.

### Signing in

The front door shows one card per person. A card signs in as that person: the role decides the apps the door offers,
and the desk opens on the person's own yard. This is the local demo (`AUTH_MODE=dev`): the cards shape what the screens
offer and the server enforces no role; a real dealer signs in through its identity provider.

| Person | Role | Yard | In the walk |
|---|---|---|---|
| Morgan Hale | Owner | Kelowna Main Yard | stock across yards, the manager's view |
| Robin Diaz | Finance | Kelowna Main Yard | AR and a statement |
| Avery Brooks | Sales | Kelowna Main Yard | a quote from a material list |
| Jordan Pike | Cashier | Kelowna Main Yard | a counter sale on account |
| Casey Lin | Purchasing | Kelowna Main Yard | receiving a purchase order |
| Drew Patel | Cashier | Lake Country Outpost | a sale at a second yard |
| Sam Ortiz, Taylor Reed, Alex Moreno | Warehouse, Sales (West Kelowna), Driver | | |

### The walk

1. **Sign in.** The door reads "Sign in to Gable Building Supply" with nine cards. Pick Jordan Pike: the door offers
   the desk and Point of Sale only, and the header names Jordan Pike, Cashier.
2. **A counter sale on account (Jordan Pike).** Open Point of Sale: the Counter opens on Kelowna Main Yard's register,
   `REG-01`, with the till closed: nothing can be rung until it opens. Open Till with a float of $200.00: the till's
   sale opens, `POS-000001`. In the Customer panel find "Tarnwick": Tarnwick Construction shows its credit available and
   its open balance; enter a purchase order (`TC-2219`) and pick the job Glenmore Lofts. Type a quantity of 12, search
   `LUM-248-PREM` (the result shows the stock on hand at this yard) and press Enter: twelve 2x4x8 SPF Premium at the
   customer's price. Then 4 of `OSB-12` (7/16 OSB 4x8). Choose Charge to account and Pay: "Sale POS-000001 complete",
   and the customer's credit available drops by the sale's total. The completed sale stays on screen, with Void and
   Return, until New Sale. The sale posts an invoice to Tarnwick's account carrying the purchase order; Robin sees it in
   step 7.
3. **A quote from a material list (Avery Brooks).** Open Quotes and the Quote Builder. Pick the customer. Open "From a
   material list", paste a list (for example `24 2x4x8 SPF`, `6 sheets 7/16 OSB`, `10 boxes 3 in deck screws`, `4
   2x6x12 SPF`) and Read pasted list: each line comes back with the product it matched and how sure the reader is.
   Accept confident lines puts them on the quote at the customer's prices (a Contractor account's 7/16 OSB at 16.575,
   not the list's 19.50), exactly as a line added by hand; a line the reader is unsure of offers its alternatives. Save
   draft: the saved quote says where its lines came from (Material list), shows the list as it was pasted, and maps
   each line to its product. Mark it sent and Convert to Order: the order opens with the quote's lines and prices,
   ready to confirm.
4. **A sale at a second yard for a customer of the first (Drew Patel).** Sign in as Drew Patel: the Counter opens on Lake
   Country Outpost's register, `REG-LK`. Open Till. Find "Snowfern": Snowfern Cabin Co, a Kelowna Main Yard account,
   is found here too, with its standing (every customer is a dealer wide account that any yard may sell to). Sell 10 of
   `LUM-248-PREM` on account (the stock shown is Lake Country's own) and Pay. The customer now trades at Lake Country as
   well: its first document here linked it to the yard, and the link is audited.
5. **Receiving a purchase order (Casey Lin).** Open Purchasing: the list shows each order's number, vendor, yard and
   total. Open the newest order, at the top of the list: sent to Fernbank Hardware Distribution for Kelowna Main Yard,
   with two lines of joist hangers (the seed draws its other orders at random, so the numbers differ from run to run).
   Receive Items: the bins
   offered are Kelowna Main Yard's only (`KEL-A` to `KEL-D`). Confirm Receipt: the order moves to Received and the stock
   rises ("Items received into inventory").
6. **Stock across yards (Morgan Hale).** Open Inventory and the product 2x4x8 SPF Premium, then its Stock & Locations
   tab: Kelowna Main Yard's stock. Switch the yard in the header to West Kelowna Satellite, then to Lake Country Outpost:
   the tab shows each yard's own stock.
7. **AR and a statement (Robin Diaz).** Open Reporting and AR Aging: each Kelowna Main Yard customer's open balance by
   age, with Tarnwick Construction carrying the counter's charge. Switch the yard in the header to Lake Country Outpost:
   Snowfern Cabin Co carries its Lake Country charge there. Open Customer Statement for Tarnwick Construction: each
   invoice and payment in the period with the running balance, and Print for the dealer's statement.
8. **The manager's view (Morgan Hale).** Open the Dashboard: active orders, pending dispatch, outstanding AR (the
   counter's account charges included), the collected trend (the chart headed "Collected, last 7 days"), the top customers, the stock alerts
   and the recent activity, with the order converted in step 3 on top. Collected today counts the cash received at the
   yard today, less refunds, against yesterday's; the walk's sales went on account, so they show in outstanding AR instead.

### Not in this demo

- Real sign in (the cards are the local demo's), the desktop shells per role, and working offline.
- AI unless an administrator enters an Anthropic key in Tech Admin, AI (and tests it there): with a key, the material
  list intake also reads photos and PDFs; without one, pasted lists, text files and spreadsheets are read without AI and
  every AI feature says plainly that AI is off.
- Deposits and parked sales at the counter; a printed counter receipt.
- One screen of every yard's stock or AR at once (steps 6 and 7 switch yards).
- A void or a return of an earlier sale: the counter voids or returns the sale on its screen; there is no sale lookup
  yet.
- A job on a quote; product images.
- Documents are in the seed's currency (USD), though the dealer is Canadian.

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

### Scene 0: the Gable Building Supply demo world (M1)

Proves: MY-01 (a dealer with several yards), AR-01 (customers across yards). The tenant is Gable Building Supply,
three yards: `KEL-MAIN` Kelowna Main Yard, `WK` West Kelowna Satellite, `LK` Lake Country Outpost. The multi yard
switch is on in the seed (`multi_branch_enabled`; the seed leaves it as it was when run with `DEMO_MULTI_YARD=0`, which
the browser specs' CI job does).

Note on who is signed in: the local stack runs `AUTH_MODE=dev`, where the server enforces no role, so a persona shapes
what the screens offer, not what the server allows. The persona's subject, name, role, home yard and granted yards are
the rows below; the front door's dev sign in shows one card each.

| Subject | Name | Role | Home yard | Granted yards |
|---|---|---|---|---|
| `demo-owner` | Morgan Hale | owner | `KEL-MAIN` | all three |
| `demo-finance` | Robin Diaz | finance | `KEL-MAIN` | all three |
| `demo-sales-kel` | Avery Brooks | sales | `KEL-MAIN` | `KEL-MAIN` |
| `demo-cashier-kel` | Jordan Pike | cashier | `KEL-MAIN` | `KEL-MAIN` |
| `demo-warehouse-kel` | Sam Ortiz | warehouse | `KEL-MAIN` | `KEL-MAIN` |
| `demo-purchasing` | Casey Lin | purchasing | `KEL-MAIN` | all three |
| `demo-sales-wk` | Taylor Reed | sales | `WK` | `WK` |
| `demo-cashier-lk` | Drew Patel | cashier | `LK` | `LK` |
| `demo-driver-kel` | Alex Moreno | driver | `KEL-MAIN` | `KEL-MAIN` |

Steps:

1. Open the desk and open the yard switcher. It must show the three yards by name. Through the API:
   `curl -s http://127.0.0.1:8080/api/v1/branches | jq '.items[].name'` must list the three yard names.
2. Registers: each yard has its own. `psql` through `make pg-shell`: `select id, branch_id from pos_registers order by 1;`
   must show `REG-01` on `KEL-MAIN`, `REG-WK` on `WK` and `REG-LK` on `LK`.
3. Stock differs by yard. Pick the product `2x4x8 SPF Premium` (`LUM-248-PREM`): in the desk's inventory page, or
   `curl -s "http://127.0.0.1:8080/api/v1/inventory?product_id=<product id>"`, it must show three rows with three
   different quantities, one per yard. Then pick `Ice and Water Barrier` (`RF-ICE-WTR`): it must show a row at
   `KEL-MAIN` only, and none at `WK` or `LK`.
4. People: `select s.staff_no, s.full_name, s.role, l.code, ul.is_home from staff s join user_locations ul on
   ul.user_sub = s.staff_no join locations l on l.id = ul.branch_id where s.staff_no like 'demo-%' order by 1, 4;`
   must list the nine people above, each with its home yard flagged and the owner, finance and purchasing people on
   all three yards.
5. The yard's receiving work: purchasing's purchase order list at `KEL-MAIN` must show one `SENT` order to
   Fernbank Hardware Distribution with two lines and nothing received, ready to receive.
6. Credit: the customer directory shows `Glenmore Heritage Reno` over its credit limit (orders land on hold) and
   `Quillfield Formwork Ltd` close to its limit (a limit of $20,000.00 and $18,816.00 open).
7. AR: the AR aging and `Tarnwick Construction`'s statement have open invoices at more than one yard.

### The cross yard sale (M1, ruling A)

Proves: AR-01 (customers are dealer wide accounts, the link follows trade). Act as `demo-sales-wk` (Taylor Reed, home
yard `WK`, granted `WK` only), or send `X-Branch-Id` of `WK` through the API.

1. Find a customer whose home yard is the main yard. In the desk's customer directory with the switcher on `WK`,
   search `Snowfern Cabin`. It must be found, with its home yard shown as Kelowna Main Yard. Through the API:
   `curl -s -H "X-Branch-Id: <WK branch id>" "http://127.0.0.1:8080/api/v1/customers?q=Snowfern"` must return it
   (before ruling A a yard's desk saw only its own customers), and its `branch_ids` must list `KEL-MAIN` only.
2. Quote it at `KEL-MAIN`: switcher on `KEL-MAIN`, new quote for Snowfern Cabin Co with one line of any product, send
   it. API: `curl -s -X POST -H "X-Branch-Id: <KEL-MAIN id>" -H 'Content-Type: application/json'
   -d '{"customer_id":"<customer id>","lines":[{"product_id":"<product id>","quantity":"10","unit_price_ten_thousandths":10000}]}'
   http://127.0.0.1:8080/api/v1/quotes` answers 201. The customer's `branch_ids` must still list `KEL-MAIN` only (it
   already trades there), and no `customer.branch_linked` audit row exists for it.
3. Order it at `WK`: switcher on `WK`, new order for the same customer (or the same call to `/api/v1/orders` with
   `X-Branch-Id` of `WK` and `"delivery_type":"pickup"`). It must save: a yard's desk may sell to any customer.
4. The customer's yards grew: reload the customer (desk, or `GET /api/v1/customers/<id>`). `branch_ids` must now list
   `KEL-MAIN` and `WK`; `primary_branch_id` is still `KEL-MAIN`.
5. The audit row names the order: `select changes from audit_log where action = 'customer.branch_linked' and
   entity_id = '<customer id>';` must show one row with `branch_id` of `WK`, `document_type` `order` and
   `document_id` of the order just made. A second order at `WK` for the same customer adds no further row.
6. A key bound to `KEL-MAIN` keeps its pin: with such a key, `GET /api/v1/customers/<id of a customer linked only to
   WK>` must answer 404, whatever the customer's other yards.

### M6: a branch on the ledger (GL-02, GL-03, MY-02)

Proves: every journal entry and line carries its branch, the statements cut by one yard or consolidated, a period
closes and reopens, a year closes into Retained Earnings by yard, and the AR and AP subledgers tie out. Sign in as
Morgan Hale (owner), who reaches every yard, on the demo seed with its three yards (`KEL-MAIN`, `WK`, `LK`).

1. Open Accounting, Trial Balance. The branch control reads Consolidated or the yard in the header's switcher; choose
   Consolidated. Every account shows one row, and Total Debits equals Total Credits. Choose `WK`: the figures shrink to
   that yard's own and the page still balances. Choose `LK`, then Consolidated again: the consolidated debit total is
   the sum of the three yards' (read each off the page).
2. Click the Cash row (1010) with `WK` chosen. The ledger lines of Cash for that yard open under the table: entry
   number, date, memo, branch (every row names the West Kelowna Satellite, none another) and debit and credit, with a total that
   matches the row clicked.
3. Open Accounting, Profit & Loss. With Consolidated chosen, note Total Revenue; choose each yard and note theirs: the
   three add up to the consolidated figure to the cent. Press "Compare to previous period": a second column holds the
   previous period of the same length beside each total. Click the Sales Revenue row: its lines open for the window
   and the chosen yard.
4. Open Accounting, Balance Sheet. Consolidated and each yard balance (Total Assets equals Total Liabilities plus
   Total Equity). Choose a yard and click a row to see its ledger lines as of the date.
5. Book a hand made entry at one yard. Journal Entries, New Entry: choose the yard `LK`, a date, debit Cash and credit
   Sales Revenue for 100.00, Create Entry, then Post. Its row shows the entry's yard. Back on the Trial Balance with
   `LK` chosen the new amounts appear; with `WK` chosen they do not.
6. Close and reopen a period through the API (the owner holds every yard): `GET /api/v1/gl/fiscal-periods` to read a
   period's `id` and `revision`, then `POST /api/v1/gl/fiscal-periods/<id>/close` with `If-Match: "<revision>"` answers
   200 with the revision moved on. Post another hand made entry dated inside that period: the post is refused (the
   period is closed). `POST /api/v1/gl/fiscal-periods/<id>/reopen` with the new revision and `{"reason":"a late bill"}`
   answers 200; the same post now succeeds. A reopen without a reason answers 400, a close with a stale revision
   answers 409 `stale_revision`, and the audit log holds one row for each act (`gl.period_closed`,
   `gl.period_reopened`).
7. Close a year: `POST /api/v1/gl/years/<year>/close` answers the closing entry ids, one per yard with activity. On the
   Trial Balance of that year's last day, every revenue and expense account holds zero at each yard and Retained Earnings
   (3020) holds each yard's own net; the Profit & Loss of that year still shows what each yard earned. Closing the same
   year again writes nothing new.
8. Tie out the subledgers: `GET /api/v1/gl/tie-out` answers, for the AR control account (1020) against the customer
   subledger and the AP control account (2010) against the open vendor bills, the control, the subledger and the
   difference, each in cents. A clean ledger answers a zero difference on each. `GET /api/v1/gl/tie-out?as_of=` of an
   earlier day answers 400: the subledgers hold only their current balances.
9. The wall: with a key or user held to `WK` only, `GET /api/v1/gl/trial-balance` with no `X-Branch-Id` and no
   `branch_id` answers 403 (the consolidated view needs every yard), `branch_id` of `LK` answers 403, and `branch_id`
   of `WK` answers 200. The same caller closing a period or a year answers 403.

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

1. Read the dealer profile: `GET /api/v1/dealer-profile`. The demo seed fills it with an invented dealer (Gable Building
   Supply Ltd, a remit to block and a generated logo). It must show `legal_name`, `remit_to`, `logo_url` set and a
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

### The work shell: one header over the desk's apps (S2, S3, S7, S10)

Proves: the work shell host, the shell chrome and the single header framed protocol. Runs on the demo seed with its
three yards; sign in is the shell's own dev card (the stack runs `AUTH_MODE=dev`).

1. Start the stack (`make up`) and open <http://127.0.0.1:8080/shell/>. The shell's sign in card shows: a name and a
   role. Sign in as Avery Brooks, role Sales. The shell opens on the Desk app: ONE header band (the dealer's name
   leading, the app selector centred, then the yard, theme, feedback and account controls), and the desk framed under
   it with NO header band of its own (its own band is hidden by the framed flag, never rendered twice).
2. The yard control reads the seeded yards. Pick West Kelowna Satellite (`WK`) in the shell's header, then open Orders
   inside the frame: the list is the west yard's, and the desk's own requests carry the new yard (the framed desk
   followed the shell's pick).
3. The rail is hidden by default. Rest the pointer on the left edge strip (or press the rail control in the header):
   the rail reveals, listing the apps the role holds (Desk and Point of Sale for sales) and, wide, their few top
   level places. Press the pin: the rail stays, as icons on a narrow window, wide on a wide one. Reload the page: the
   pin survives. The contractor portal is NOT in the list: it is the contractors' surface with its own sign in, and
   the shell never pretends a staff session is a contractor one.
4. The centred selector opens the app list; pick Point of Sale. The frame switches to the register tree; the yard and
   the signed in person carry over (the handoff is the shared session store, no sign in again). Open the account
   chip and switch back through the selector to Desk.
5. The account chip opens Switch role and Sign out. Press Switch role: the framed app is asked to run its own sign
   out (a nonce verified handshake with a bounded wait), the shell's own custody clears, and the sign in card comes
   back. Sign in again as Alex Moreno, role Driver: the selector and rail now hold Desk and Driver only.
6. Theme: the header's theme control flips Industrial Dark and Gable light for the shell AND the framed app at once
   (the shared theme store); the choice survives a reload.
7. Sign out from a desk opened standalone (open <http://127.0.0.1:8080/home> directly): the desk shows its own header
   band, exactly as it does without the shell.

### Market check, our prices against a live store through FB Connect

Needs the demo seed, a vault key (`PAYMENT_VAULT_KEY`), an FB Connect address and the pricing flow's webhook id.

1. As admin open Tech Admin and find the Market check card. It says the check is off. Enter the FB Connect address and
   the webhook id, the store label ("Home Depot, Spokane WA: nearest store with live prices"), band 5 and floor 15,
   and Save. The card now shows the id as "Saved" with Replace and Clear, and never the value.
   `GET /api/v1/admin/market-check/settings` answers `webhook_id_saved: true` and no id.
2. Open Pricing, then Market check. The yard's six top lumber sellers are picked (the counter tiles; without that route the dimensional lumber (2x4 to 2x12, 8 to 16 feet) with the most on hand, then the first catalogue products), and the page says which. Remove one chip, add another with the
   search, and press Run check. While it runs the rows are placeholders reading "Reading live prices through FB Connect".
3. Read the summary line and the table: each matched row shows our price, the Home Depot title and price with its
   confidence and unit note, the difference in percent and dollars, the position and the suggested price with its
   reason. An unmatched or error row shows its note across the row.
4. Check a suggestion by hand: a row above the band suggests the competitor's price plus the band, never under the
   margin floor (cost divided by one less the floor percent).
5. As a cashier or a sales user, `POST /api/v1/pricing/market-check` is a 403.
6. Clear the id in Tech Admin and open Market check again: it says the check is off, with a link that opens Tech Admin
   on the Market check tab.
7. Change the FB Connect address and Save without entering the id again: the id shows empty with "Enter the webhook id
   again for the new address", and the Market check says it is off until it is entered.

### Counter account sales, tenders, returns and receipts

Needs the demo seed with at least two yards (Kelowna Main Yard and Lake Country Outpost), signed in as the cashier
of a yard (Jordan Pike at Kelowna Main Yard, Drew Patel at Lake Country Outpost) with the till open. The demo
runs with sign in off, so a manager's approval in step 3 is recorded as "dev-mode"; a real deployment needs a
signed in admin or owner other than the cashier. The setup calls are the same ones the customer screens make, shown
as `curl` against `http://127.0.0.1:8080`; `$TARNWICK` is the id from
`curl ".../api/v1/customers?q=Tarnwick"`, `$REV` its current revision.

1. **Set up the account.** Give Tarnwick Construction a small credit limit, a named buyer requirement and a check
   limit: `curl -X PUT .../api/v1/customers/$TARNWICK -H 'If-Match: "'$REV'"'` with the customer's own fields and
   `"credit_limit_cents": 100000, "named_buyer_required": true, "check_limit_cents": 20000`. Add a buyer:
   `POST .../api/v1/customers/$TARNWICK/contacts` with `"first_name": "Ada", "last_name": "Buyer",
   "authorized_buyer": true, "buyer_sale_cap_cents": 50000`.
2. **Can this go on account? (Jordan Pike, Kelowna Main Yard).** Open Point of Sale and find "Tarnwick". Under the
   standing line the screen says "Credit available" and, once there is a line, whether the sale can go on the
   account. Add 12 of `LUM-248-PREM` (more than the limit allows): the standing line turns red and says by how much
   the sale would put the account over its limit, and offers three actions: Take the amount on the account (opens
   the account's own page), Ask a manager to approve, and Pay another way. Charge to account is off, and the words
   under the tenders are the same as the standing line's. The Buyer pick says the account asks for a named buyer.
3. **A manager approves one sale.** Pick Ada Buyer. Press Ask a manager to approve, type a reason: the standing line
   now says a manager approved the sale on account and the Charge to account tender opens. Pay: "Sale complete". Start
   the next sale for Tarnwick with the same lines: it is blocked again, because the approval went with its sale.
   (A cashier's own approval, or a sale taken by another sale's approval, is refused in words.)
4. **A cash only account.** Give Snowfern Cabin Co the terms "Due on receipt" (the terms master's kind
   `DUE_ON_RECEIPT`, whatever the terms are named). At either yard find Snowfern: the standing line says the
   account's terms are due on receipt and nothing can be put on the account; the only way forward is Pay another
   way, and the sale is not released until it is paid in full.
5. **A check, its ID and its limit.** Choose Check: the screen asks for the check number, the check limit
   ($200.00) and "ID checked"; ticking it asks for the kind of ID (never its number). A check of $250.00 is refused
   in words with Ask a manager beside it; with the approval the check goes, and the sale's tender shows the ID kind.
6. **Store credit.** Take a return of one line of a completed sale for Tarnwick as "Account credit". On the next sale
   for Tarnwick the Store credit tender is offered with what the account holds; pay half in store credit and half in
   cash. The credit memo is used up by that much, the till expects only the cash, and Void of that sale gives the
   credit back.
7. **Returns with reasons and a fee.** Set a fee for one reason (`INSERT INTO system_settings (key, value) VALUES
   ('pos.restocking_fee_bps.changed_mind', '1500')`). On a completed sale press Return, name a line and pick "Changed
   their mind (15% restocking fee)": the return refunds the line less the fee, and the credit memo (open it from
   Credit Memos) shows "Restocking fee" as its own line. "Other" asks for a note.
8. **An exchange.** On a completed sale press Return, name a line and a reason, press Exchange and pick the goods
   that replace it. The screen states the credit the return is worth, the replacement and what the customer pays or
   is paid back; take the exchange: one credit memo and one sale point at each other, only the difference is
   tendered, and the stock goes back and out at the yard's register. At Lake Country Outpost the same walk uses
   that yard's stock.
9. **The signature and the reprint.** On the completed account sale type the signer's name, draw in the box and
   press Seal signature: "Signed by ... sealed" with the first characters of the seal. Print the 80mm receipt
   twice: the second says Reprint with its count (the 80mm layout needs the receipt renderer; without it the screen
   says the layout is not available).

### The manager step up at the counter

Needs the demo seed with at least two yards, at the Point of Sale with the till open, signed in as the yard's cashier
(Jordan Pike at Kelowna Main Yard). A manager (Morgan Hale, the owner) has a counter PIN: an admin sets it on the
admin page's Staff tab (Set PIN beside the manager's row; a PIN is 4 to 8 digits, never one repeated digit or a plain
run), which is the same as
`curl -X PUT .../api/v1/admin/staff/$MANAGER/counter-pin -d '{"pin":"4481"}'` with `$MANAGER` the manager's staff id.
The walk proves the approval is the manager's own, whoever is signed in at the terminal.

1. **The manager list.** `curl ".../api/v1/pos/step-up/managers?act=override"` lists only staff with a PIN whose role
   may approve an override (admin or owner): Morgan is there, Jordan is not.
2. **A wrong PIN says so, plainly.** Ask a manager to approve (an over limit check or a blocked account sale), type
   the reason, pick the manager and type a wrong PIN: the dialog says "That PIN is not right" and nothing is
   recorded. Five wrong tries in a row lock the PIN: the dialog says "This manager's PIN is locked: an admin can
   unlock it", and the Staff tab shows Locked with an Unlock that clears it.
3. **The approval names the manager.** With the right PIN the terminal says "Approved by Morgan Hale" and the sale
   goes through. The sale's override row names the manager by name
   (`select approved_by from pos_account_overrides order by created_at desc limit 1;`), and so does its audit row.
   The manager cannot approve their own sale: a sale rung up under the manager's own login is refused with
   "A manager cannot approve their own sale".
4. **One approval, one act.** The approval token is spent by its one act: replaying it, spending it on another sale or
   different terms, or after its few minutes have passed is refused ("This approval does not cover this, is used up or
   expired: ask the manager again").
5. **A pickup taken by the manager's PIN.** In Drawer Cash pick Pickup, type the amount and press "Ask the manager
   taking it": the manager picks themselves and types the PIN. The movement records with the manager as the taker
   (`select taken_by from till_movements where kind='pickup' order by created_at desc limit 1;`), and the same
   approval cannot take a different amount.
6. **A held variance signed off by the manager's PIN.** Close the drawer with a count far over the branch's
   threshold: the count waits for a manager. On the Daily Till page the queue lists it; type why the variance is
   accepted, press Sign off, and the manager steps up with the same PIN: the session closes signed off by the
   manager by name, and its over and short posts. The closer and the cashier cannot sign their own drawer off.

### A credit hold from accounts receivable stops the charge (CP-11)

Needs the demo seed with at least two yards and the counter open (the walk above). `$TARNWICK` is the customer id as
before; the hold calls are accounts receivable's own routes, the ones the AR screen drives.

1. **AR places the hold.** `curl -X POST .../api/v1/ar/credit-holds -H 'Content-Type: application/json'
   -d '{"customer_id":"'$TARNWICK'","reason":"past due 60 days"}'` answers 201 with the hold. The account's flag is
   set in the same transaction: `GET .../api/v1/ar/credit-holds?customer_id=$TARNWICK` lists it as active.
2. **The counter states it.** At Point of Sale find Tarnwick and attach the account: the standing line says
   "On credit hold" in the red the zero limit uses, and the line under it carries the hold's own words ("past due
   60 days") and that nothing can be put on the account. Ring a line: Charge to account is off, and the ways
   forward are Ask a manager to approve and Pay another way; there is no Take the amount on the account, because
   paying the balance does not lift a hold.
3. **The server refuses the charge.** `GET .../api/v1/pos/customers/$TARNWICK/standing?amount_cents=599` answers
   the verdict blocked with the reason `account_hold`; a completion with an account tender
   (`POST .../api/v1/pos/transactions/<sale>/complete`) answers 409 with the same blocker and writes nothing: no
   invoice, no balance, no ledger row.
4. **The account still buys.** Pay the same sale in cash: "Sale complete", and the account's balance does not move.
5. **A manager may approve the one sale.** Start a sale, press Ask a manager to approve, type a reason: the charge
   goes through for that sale only; the next sale is blocked again.
6. **An override window on the hold.** `POST .../api/v1/ar/credit-holds/<hold id>/override` with
   `"override_reason": "agreed on the phone", "override_until": "<an hour ahead>", "max_order_cents": 599`: a sale
   of at most $5.99 on the account completes, one over the cap is refused with the cap in the words, and after the
   window the hold stands as before.
7. **AR releases the hold.** `POST .../api/v1/ar/credit-holds/<hold id>/release -d '{"reason":"paid up"}'`: the
   counter attaches the account again and the charge completes.

### The card terminal at the counter, from the claim to the manager's queue

Needs the demo seed, two yards, a register each, and the simulated terminal selected by configuration
(`POS_TERMINAL_SIMULATOR=approve` for the first half, `no_answer` for the second; an unset value wires no terminal,
so a production configuration runs none of this).

1. Sign in as a cashier at yard one's register, start a sale, add a stocked line, and tender the whole total to the
   card. The desk shows the plain waiting state (the card leg is claimed and at the device); the completion answers
   only when the terminal does. With the approve script the sale completes: the card tender is a payment applied to
   the sale's invoice, and `GET /api/v1/pos/terminal-intents/<id>` (the id the desk held) shows `state authorized`
   with only safe answer fields (brand, last four, auth code, the provider reference).
2. Sign in as a cashier at yard two's register, `POS_TERMINAL_SIMULATOR=decline`, and tender a card: the completion
   refuses `card_declined`, the sale stays open, and the intent stands `declined`.
3. With `POS_TERMINAL_SIMULATOR=no_answer`, tender a card: the desk's waiting state holds, its cancel answers 409
   `terminal_at_device` (honest about the device: the money may still move), and the completion answered the waiting
   refusal naming the intent.
4. Try to close the drawer while the intent stands: the close refuses `terminal_intent_in_flight` naming the intent.
5. After the intent's lease runs out, `POST /api/v1/pos/terminal-intents/sweep` as a manager (a cashier is a 403):
   the pass reports what it closed and queued. `GET /api/v1/pos/terminal-lost-items` shows the manager item: who
   resolves it (the register), what amount, the device reference, the transaction.
6. Read the terminal's own record, then `POST /api/v1/pos/terminal-lost-items/<id>/resolve` with `outcome declined`
   (the terminal said no): the item closes `resolved_declined`, nothing moved, and the drawer now closes. A second
   resolve is 409 `lost_item_settled`.
7. For an `authorized` resolution, the money follows the sale. A sale still open keeps the authorized leg: complete
   it with the same card tender and the sale uses that leg (the device is not called again, and a completion that
   would leave the leg behind is refused `authorized_leg_unused`). A sale that completed another way has the leg
   applied up to its invoice's open amount and the rest put back to the card; a sale paid in full, or voided, has the
   whole charge put back (the same day void first, the refund when the void is refused; a failed reversal is 502
   `charge_not_reversed` and the item is open again; these reversal branches require a payment gateway configured on
   the stack, and with the simulator alone they answer 409 `no_reversal_gateway`). A stranded card void, card return
   or payback settles through its refund claim's own resolution, so the drawer closes after it. A second resolve of
   any item is 409 `lost_item_settled` and calls nothing.
8. Only the manager roles resolve, and every route runs behind the branch wall: sign in as yard two's manager and
   yard one's intents and items read as missing rows (a branch header) or are refused 403 (no header and no grant),
   its queue lists none of yard one's items, and its sweep takes only yard two's expired legs.

### M26: till control and end of day by branch

Proves: CP-08 (no sale without an open till, drops, pickups, paid in and paid out, the variance sign off) and CP-09 (the
end of day by branch and its deposit reconciliation), across two yards: `KEL-MAIN` (`REG-01`) and `LK` (`REG-LK`).

Note on who is signed in: the local stack runs `AUTH_MODE=dev`, where the server sees no person, so it cannot tell the
cashier from the manager and the rule that a different person signs off is held by the API tests
(`TestTheSignOffPostsTheVarianceAndNamesTheManager`), not by this walk. The walk shows the queue, the sign off and what
it posts. Run `curl` against <http://127.0.0.1:8080>; each yard's id is in `GET /api/v1/branches`.

1. **No sale without a till.** With no drawer open at `REG-LK`:
   `curl -s -X POST -H 'Content-Type: application/json' -d '{"register_id":"REG-LK"}' http://127.0.0.1:8080/api/v1/pos/transactions`
   answers 409 with the blocker `till_not_open`, and no sale exists. In the desk (Jordan Pike, Point of Sale) the
   counter says the same in words, and Open Till is the way in.
2. **Drawer cash at the first yard (Jordan Pike).** Open Till at `REG-01` with a float of $200.00. Ring a cash sale of
   one 2x4x8 SPF Premium and pay it with the exact cash. Press Drawer Cash and record, one at a time: a Safe drop of
   $50.00; a Paid out of $5.00 for postage; a Pickup of $20.00 taken by `demo-owner` (the cashier cannot be the taker:
   leave it blank or name the cashier and the screen or the server refuses); a Paid in of $3.00 as a fee received. A
   Paid out for Other asks what for. Read the drawer: `GET /api/v1/pos/till/<session id>/report` lists the four
   movements, `movement_totals_by_kind` carries 5000, 2000, 500 and 300, and the expected cash is the float plus the
   sale, less the drop, the pickup and the paid out, plus the paid in.
3. **What the movements post.** `make pg-shell`, then `select e.memo, a.code, l.debit, l.credit from gl_journal_lines l
   join gl_journal_entries e on e.id = l.journal_entry_id join gl_accounts a on a.id = l.account_id where e.memo like
   'Till %' order by e.created_at, a.code;` shows each movement as one balanced entry: the drop and the pickup move cash
   (`1010`) to Undeposited Funds (`1015`), the paid out debits Operating Expenses (`5020`), the paid in credits Fees and
   Charges Revenue (`4030`).
4. **A close inside the limit (Jordan Pike).** Close Till and count the cash $0.50 short of the expected figure (the
   count is blind: the expected figures show only after it posts). The result reads "Z-Report - Till Closed" with
   the short, and `5030` carries it. The Z report (`GET /api/v1/pos/till/<id>/zreport`) lists the four movements.
5. **A close over the limit (Drew Patel, second yard).** Open Till at `REG-LK` with a float of $200.00, ring one cash
   sale, and close counting $20.00 short. The default limit is $5.00 (`GET /api/v1/pos/till/policy?branch_id=<LK id>`
   answers the default and its source): the result reads "Count Posted - Waiting for Sign Off", the session is
   `pending_signoff`, `5030` carries nothing for it yet, there is no Z report, and the register takes no sale
   (step 1's call answers `till_not_open` again).
6. **The manager signs it off (Robin Diaz).** Open Daily Till: under "Drawers waiting for sign off" register `REG-LK`
   shows the short, the limit and who closed it. Type a reason and Sign off: the drawer leaves the queue, `5030` now
   carries the $20.00, the session is `closed` and `GET /api/v1/pos/till/<id>/report` names who signed off and why. The
   Z report now exists. Set the yard's own limit with
   `curl -s -X PUT -H 'Content-Type: application/json' -d '{"branch_id":"<LK id>","variance_threshold_cents":5000}' .../api/v1/pos/till/policy`
   and repeat step 5 with a $20.00 short: it closes at once, inside the new limit.
7. **The end of day by branch (Robin Diaz).** Open Daily Till and its "End of Day" card for Kelowna Main Yard: the
   register `REG-01` with its drawer, the sales, the expected and the counted cash, the over and short, and the cash,
   checks and card to deposit. The cash to deposit is the counted cash less the float, plus the drop and the pickup.
   Switch the yard in the header to Lake Country Outpost: its own card, with `REG-LK` only. Nothing of one yard shows in
   the other.
8. **Closing the day.** First make the bank account the deposit goes to: `POST /api/v1/bankrecon/accounts` with a name and
   the id of the cash account `1010` (or a bank account of its own in the chart). At Kelowna Main Yard open a second
   drawer (Open Till at `REG-01`) and return to the End of Day card: "Close the day" is off and the card says a drawer
   is still open. Close that drawer too, type a deposit slip reference (`SLIP-0001`), choose the bank account under
   "Deposit to" and Close the day: the card names who closed it, when and the slip. The close posted the deposit
   (`select e.memo, a.code, l.debit, l.credit from gl_journal_lines l join gl_journal_entries e on e.id = l.journal_entry_id
   join gl_accounts a on a.id = l.account_id where e.memo like 'Branch deposit%';`): the bank account's GL account is
   debited the cash and checks deposited, `1015` is credited what the day's drop and pickup put there and `1010` the
   rest, so `1015` nets to nothing for the day. A second close of the same day answers 409 `day_already_closed`
   through the API (`POST /api/v1/pos/end-of-day/close` with the yard, the date, a slip and the bank account), and Open
   Till at `REG-01` now answers 409 `day_closed`. Lake Country Outpost's day is still open.
9. **Reopening the day (Robin Diaz).** A late drawer or a correction before the deposit is matched: on the End of Day
   card press Reopen the day, type why and confirm. The deposit entry is reversed (the same legs the other way, naming
   the entry it reverses), the card is open again and Open Till at `REG-01` works. Close the day again with a slip
   reference: a new deposit is posted. Once the deposit is matched to a bank line (step 10) the reopen answers 409
   `deposit_already_matched`.
10. **Matching the deposit to the bank.** Import a statement line for the deposit (`POST /api/v1/bankrecon/import` with
   `{"bank_account_id":"<id>","csv_content":"Date,Amount,Description,Reference\n<the day>,<the deposit>,BRANCH DEPOSIT,SLIP-0001"}`;
   the day is the business date the card shows). On the End of Day card press "Match the deposit to the bank": the line is
   offered, the nearest amount first, and only a line of the day's bank account can be taken. Match it: the card shows the
   bank line, who matched it and the difference in cents between the bank and the slip (nothing when they agree). The
   line is now matched in bank reconciliation to the day's deposit entry, so no other day can take it.

### The X report mid shift, and the stored Z reports (CP-08)

Needs the demo seed with at least two yards, signed in as the cashier of a yard (Jordan Pike at Kelowna Main Yard,
register `REG-01`). The demo runs with sign in off, so the blind count rule (a cashier reads no money figures of the
open drawer, a manager reads them) is held by the API tests
(`TestSweepPR178OpenTillReportRedactsExpectedFiguresForCashier`), and on the demo stack the X report shows the
figures in full.

1. **The X report of the open drawer (Jordan Pike).** Open Till at `REG-01` with a float of $100.00. Ring one sale
   of a 2x4x8 SPF Premium paid $1.00 in cash and the rest by check, and record a Safe drop of $5.00 under Drawer
   Cash. Press X Report beside Close Till: a panel headed X REPORT says "Not a close; the drawer stays open" and
   reads the register, the cashier, when the drawer opened, the float, the sale count and total with its tax, the
   cash and the check taken and expected, any refunds, the drop, and the expected drawer cash ($100.00 plus the
   $1.00 less the $5.00: $96.00). Press Print: the sheet prints through the browser's print, headed X REPORT. Close
   the panel: the drawer is still open and the register still takes sales.
2. **A cashier's X report stays blind.** On a signed in deployment a cashier's X report of the open drawer shows
   the register, the sale count and the tax only, and says the money figures stay blind until the count is posted;
   a manager's (finance, owner or admin) shows every figure. On the demo stack there is no sign in, so the report
   shows the figures in full and the rule is proven by the API tests instead.
3. **The close writes the Z.** Close Till, count the cash $96.00 and the check figure the sale took, and post: the
   result reads "Z-Report - Till Closed", balanced.
4. **The stored Z reports.** Press Z Reports (it is there with or without an open drawer): the register's frozen
   closes of the day list, the one just made first among them, each with its balanced or over and short words. Open
   one: the Z REPORT sheet reads the frozen close, the count against the expected, read only, and prints the same
   way. At Lake Country Outpost the list holds only that register's reports.
### The yard sale: the counter on a handheld (CP-12)

Proves: CP-12 (mobile sales in the yard, on a tablet or handheld). The counter is the yard sale: the same `/pos` screen
made touch first and framed by the field shell, not a second sale screen.

Note on who is signed in: the cashier persona is Jordan Pike (cashier, `KEL-MAIN`). The browser runs narrow: set the
window to a phone width (390) or a tablet width (820). The walk needs the field shell's dev sign in (dev mode on the
local stack) and the browser's own dev tools to narrow the viewport.

1. **The way in (Jordan Pike).** Open the field shell (`/field/`) and sign in as Cashier. The bottom tab bar offers one
   tab, Sale; the Yard and Driver tabs are not the cashier's. Press Sale: the counter opens inside the shell's frame,
   on `KEL-MAIN`'s register, with the shell's top bar (the yard picker, the account chip) and no second header of its
   own.
2. **The screen fits a phone.** At 390 the document never scrolls sideways: the header is one row (the sale's number,
   the yard and register on one truncated line; the till state as a chip; the till's money controls behind the Till
   button; no keyboard legend). The working order is the thumb's: the Items card first, then the ticket, then payment,
   with a pinned pay bar at the bottom carrying the total and Pay.
3. **A cash sale in the yard (Jordan Pike).** Open Till with a float if none is open (the Till button holds Open Till
   when there is none). Find `2x4x8 SPF Premium` by typing `LUM-248-PREM` in the search and pressing Enter. Choose
   Cash, press Pay on the pinned bar: the sale completes, the receipt prints (Print receipt, then the button reads
   Reprint), and `GET /api/v1/pos/transactions?register_id=REG-01` lists it completed.
4. **The scanner.** Press Scan on the Items card: the barcode scanner opens. The camera is the webview's own
   permission (the OS asks once); on a desktop browser allow the camera and scan `LUM-248-PREM`: the line lands. The
   camera inside a Tauri webview is proved by hand on the target device, once, and noted here.
5. **A tablet at 820.** Widen to 820: the same screen becomes two columns (the Items card on the left, the panels on
   the right), the pinned bar is gone and the Pay button is in the order summary card. The same sale completes.
6. **Money never queues at the yard.** Cut the connection (dev tools, offline): the shell's banner says the truth, and
   Pay is refused in the same words ("You are offline; the shell and its apps need the connection, and money never
   queues."). No sale is created: the transaction list is unchanged. Offline selling stays the work shell's alone.
7. **Who may sell.** The cashier opens the handheld (the Field profile lists the cashier next to warehouse and driver);
   the warehouse hand does not sell: signing in as Warehouse offers the Yard tab only, and the server's counter wall
   (admin, owner, cashier) is unchanged.

### CP-10: the exempt sale, the certificate on file and the branch's own rate

Proves: CP-10 without the ship to (decision D4 stays out): the exemption certificate recorded, shown and ended on the
account, the counter's running totals honouring it (the untaxed total at Pay), the expiry honoured at the counter and
on orders, and the branch's tax jurisdiction and rate set on the admin screen. Across two yards: `KEL-MAIN`
(`REG-01`) and `LK` (`REG-LK`).

1. **A certificate on file (Robin Diaz).** On an account's page open the Tax Exemption tab: Add certificate, with the
   number, the reason (RESALE), the issuing state, the effective day and (when it has one) the expiry day. The list
   shows it Active. `GET /api/v1/tax/exemptions/<customer id>` carries the same row.
2. **The untaxed total at Pay (Jordan Pike).** At `REG-01` ring lines for the account: the Order summary says
   `Tax exempt` with the certificate number and its expiry in place of the tax figure, and the total is the subtotal
   alone. Charge to account for exactly that total: the sale completes with `tax_cents` 0 (the money moment and the
   screen agree, so the old "only a cash tender can give change" refusal is gone). `GET /api/v1/pos/transactions/<id>`
   carries `tax_exemption` with the certificate number.
3. **Ending it (Robin Diaz).** On the account's Tax Exemption tab press End and confirm: the row reads Ended, and a new
   sale for the account carries the branch's tax again.
4. **An expired window exempts nothing.** Add a certificate whose expiry day is past: the panel reads Expired. A new
   counter sale for the account carries the tax, and an order for the account prices at the branch rate
   (`tax_source` `branch_rate`, `tax_exempt` false): the counter and the order path read one window rule.
5. **A future window exempts nothing yet.** Add a certificate whose effective day is ahead: the panel reads Not in
   effect yet, and the sale and the order both carry the tax.
6. **The branch's own rate (Robin Diaz).** On Admin, Branches edit `KEL-MAIN`: the Tax jurisdiction code and the
   Default tax rate (typed as a percent, 8.875, stored as the fraction the column holds) save with the rest. Ring a
   sale at `REG-01`: the summary's tax is the new rate on the subtotal. `LK` keeps its own rate, and its register
   `REG-LK` prices at it, not at `KEL-MAIN`'s.

None of the other walks of the plan are written yet; they land with their items.
