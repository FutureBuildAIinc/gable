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
   different quantities, one per yard. Then pick `Ice & Water Shield` (`RF-ICE-WTR`): it must show a row at
   `KEL-MAIN` only, and none at `WK` or `LK`.
4. People: `select s.staff_no, s.full_name, s.role, l.code, ul.is_home from staff s join user_locations ul on
   ul.user_sub = s.staff_no join locations l on l.id = ul.branch_id where s.staff_no like 'demo-%' order by 1, 4;`
   must list the nine people above, each with its home yard flagged and the owner, finance and purchasing people on
   all three yards.
5. The yard's receiving work: purchasing's purchase order list at `KEL-MAIN` must show one `SENT` order to
   Okanagan Hardware Distribution with two lines and nothing received, ready to receive.
6. Credit: the customer directory shows `Glenmore Heritage Reno` over its credit limit (orders land on hold) and
   `Ridgeline Formwork Ltd` close to its limit (a limit of $20,000.00 and $18,816.00 open).
7. AR: the AR aging and `Kelbrook Construction`'s statement have open invoices at more than one yard.

### The cross yard sale (M1, ruling A)

Proves: AR-01 (customers are dealer wide accounts, the link follows trade). Act as `demo-sales-wk` (Taylor Reed, home
yard `WK`, granted `WK` only), or send `X-Branch-Id` of `WK` through the API.

1. Find a customer whose home yard is the main yard. In the desk's customer directory with the switcher on `WK`,
   search `Big White Cabin`. It must be found, with its home yard shown as Kelowna Main Yard. Through the API:
   `curl -s -H "X-Branch-Id: <WK branch id>" "http://127.0.0.1:8080/api/v1/customers?q=Big%20White"` must return it
   (before ruling A a yard's desk saw only its own customers), and its `branch_ids` must list `KEL-MAIN` only.
2. Quote it at `KEL-MAIN`: switcher on `KEL-MAIN`, new quote for Big White Cabin Co with one line of any product, send
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

None of the other walks of the plan are written yet; they land with their items.
