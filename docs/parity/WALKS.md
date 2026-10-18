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
   `charge_not_reversed` and the item is open again). A stranded card void or card return settles through its refund
   claim's own resolution, so the drawer closes after it. A second resolve of any item is 409 `lost_item_settled` and
   calls nothing.
8. Only the manager roles resolve, and every route runs behind the branch wall: sign in as yard two's manager and
   yard one's intents and items read as missing rows (a branch header) or are refused 403 (no header and no grant),
   its queue lists none of yard one's items, and its sweep takes only yard two's expired legs.

None of the other walks of the plan are written yet; they land with their items.
