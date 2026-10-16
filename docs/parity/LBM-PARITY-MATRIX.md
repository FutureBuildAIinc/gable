<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# LBM parity matrix: Gable against Rundoo, Epicor BisTrack and DMSi Agility

What a multi yard lumber and building materials (LBM) dealer's core operation needs, what each of the three
products does for it, what Gable has today, and a priority for each capability. The plan that follows the
matrix builds the MUST rows in order. Sizes are in dev hour equivalents.

## How this was made

- **The rows.** One list of capabilities in thirteen groups, written before the research: the twelve areas of
  core LBM operation (counter and POS through integrations) and a thirteenth for what being multi yard needs
  everywhere. Each product's research added the capabilities it found outside the list; those are the rows
  after each group's last numbered row, and the products not checked for them say so.
- **The products, from public sources only.** Each product's own site, help centre, documentation, data sheets,
  release notes, press releases and customer stories, and reputable reviews and trade press. Every claim links
  the page it rests on (R, B and A numbers, listed in [SOURCES.md](SOURCES.md)). A separate check fetched every
  cited page it could reach and corrected each claim its page did not support:
  Rundoo, 114 of 114 pages fetched, 22 rows corrected.
  BisTrack, 72 of 82 pages fetched, 31 rows corrected. The other 10 are Epicor training transcripts behind its customer sign in; a claim resting only on them is marked behind a demo or login.
  Agility, 161 of 161 pages fetched, 19 rows corrected.
- **Evidence.** "documented" means a help page, document or data sheet describes it; "marketing only" means
  only a product page or a review claims it, without detail; "behind a demo" means the vendor says it exists
  and the detail sits behind a demo, a customer login or a sales call; "Not public" means nothing public was
  found, which is not the same as the product lacking it. BisTrack's documentation sits behind Epicor's customer
  login, so most of its rows rest on Epicor's product pages and data sheets.
- **Gable today** is read from the code on `refactor/v1` at `695d587c`, with file and line links. "Built" means
  routes, storage and tests exist for the row's core and it holds across yards where the row needs it;
  "Partial" names what is missing; work still open (an open pull request or an unmerged branch) is named.
  The matrix was first read at `58dde7e8`; each row's detail says what the pull requests merged since then
  changed, and the At a glance table gives both counts.
- **The MVP** is the earlier monolith (GableLBM) that `refactor/v1` was refactored from, read at its master and at
  the lines that carry work master lacks, by the same bar as Gable. `refactor/v1` already carries master, so the
  MVP rates above Gable only where one of those lines holds work `refactor/v1` lacks, and the row says which.
- **The product owner's rows.** PE-08, PE-09 and PE-10 were added after the research, for the contractor Pro
  Portal goal; the three products were not researched for them.
- **Priority** is for a multi yard dealer's core operation. MUST: daily core operation needs it and the products
  compared document it. SHOULD: expected, but a dealer runs without it for a while, or it is an add on or partial
  elsewhere. LATER: a differentiator, an add on in one product, blocked from outside, or not core operation.
  A MUST row the product owner raised or added says so in its detail.

## At a glance

| | Yes | Partial | No | Not public |
|---|---|---|---|---|
| Rundoo | 57 | 55 | 9 | 1 |
| BisTrack | 72 | 42 | 0 | 8 |
| Agility | 100 | 20 | 0 | 2 |

| Over the 122 listed rows | Built | Partial | Missing |
|---|---|---|---|
| Gable at `695d587c` | 14 | 79 | 29 |
| Gable at `58dde7e8`, the first reading | 8 | 85 | 29 |
| The MVP | 1 | 77 | 44 |

Priorities over all 145 rows: 84 MUST, 32 SHOULD, 29 LATER.

The three products, in their own words and their reviewers':

- **Rundoo.** Rundoo (rundoo.ai) is an AI-first, browser based POS and ERP for independent supply stores (hardware, paint, farm and feed, garden, lumber), with 500+ stores in the US, Canada and the Caribbean. It runs on Google Cloud, uses Stripe as its only card processor and has no offline mode. One bundle covers POS, inventory, purchasing, pricing, AR and statements, a built in GL exporting to QuickBooks or Sage, deliveries, e-commerce, staff and customer apps, and the Dooey AI assistant. Rundoo says it fits one yard to a few, not large multi branch dealers, distribution centers or millwork and truss work. No public price list: quotes scale with locations, registers and add-ons plus Stripe fees; a review site cites from $499 monthly.
- **BisTrack.** Epicor BisTrack is Epicor's ERP for lumber and building materials dealers, merchants and distributors, from single yards to dealers with 100 or more stores. Epicor now leads with BisTrack in the Cloud on Microsoft Azure and the browser based BisTrack Web; on-prem installs remain but face limited support. Core covers counter and POS, quotes and orders, pricing, inventory across branches, purchasing, Journey Planner dispatch, AR, AP, GL, CRM and dashboards. Add-ons include BisTrack Delivery, Rentals, Field Scheduling, Customer Stratification and the BisTrack API; Epicor applications include WMS, Financials, IP&O, Payments, Grow BI and Commerce; partners include Trimble, Avalara, TradeTek and AR or AP Assist. Pricing is not public; cloud is sold as a subscription.
- **Agility.** DMSi Agility is DMSi's flagship ERP for building materials distributors, lumber wholesalers, millwork shops and pro dealers; DMSi now steers retail LBM dealers to its separate web product, Frameworks. Agility runs DMSi hosted (SaaS over terminal services, with Agility Web replacing classic screens) or locally hosted. DMSi cites 400+ customers across 2,500+ locations, so multi branch work is native: branch ship-to records, organization security and shared tables, though branch transfers need the separately licensed Branch Transfers module (A125). Core spans POS, quotes, orders, pricing, inventory, purchasing, reman, dispatch, A/R, A/P and G/L. Separate apps and add ons include Mobile POD, Mobile Sales, Mobile Warehouse Tools, Commerce Cloud, Billing Portal, CRM, BInformed, Installed Sales and the Branch Transfers module, plus EDI, Worldpay, Avalara, ZIP Tax, E-Commerce Gateway and Loftware interfaces. No public pricing.

## The matrix

Each row links its detail: every product's claim with the pages it rests on, and Gable's code with file and
line links (`rows/`, one file per group). Every cited page is listed in [SOURCES.md](SOURCES.md).

### CP: Counter and POS

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [CP-01](rows/CP.md#cp-01) | Cash sale at the counter (walk in or cash customer, receipt) | Yes | Yes | Yes | Partial | Built | **MUST** |
| [CP-02](rows/CP.md#cp-02) | Account (charge) sale at the counter, with the customer PO and job where the account requires them | Yes | Partial | Yes | Partial | Built | **MUST** |
| [CP-03](rows/CP.md#cp-03) | Tender types and split tender (cash, check, card, on account, store credit or gift card) | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [CP-04](rows/CP.md#cp-04) | Tender rules (check limits, cash only accounts, PO required, ID, change, over and under tender) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [CP-05](rows/CP.md#cp-05) | Integrated card processing at the counter (card present terminal, EMV, tap) | Yes | Yes | Yes | Missing | Missing | **MUST** |
| [CP-06](rows/CP.md#cp-06) | Returns and exchanges at the counter (to the original tender, restocking fees, reason codes) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [CP-07](rows/CP.md#cp-07) | Deposits and prepayments on orders, applied at invoice | Yes | Partial | Yes | Partial | Built | **MUST** |
| [CP-08](rows/CP.md#cp-08) | Cash drawer and till (open, close, over and short, X and Z reports, drops and pickups) | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [CP-09](rows/CP.md#cp-09) | End of day by branch (daily close, deposit reconciliation, daily sales per branch) | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [CP-10](rows/CP.md#cp-10) | Sales tax at the sale (jurisdiction by branch or ship to, exemption certificates) | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [CP-11](rows/CP.md#cp-11) | Counter alerts and holds (credit hold at the sale, account notes, warnings) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [CP-12](rows/CP.md#cp-12) | Mobile sales in the yard (tablet or handheld) | Partial | Yes | Partial | Missing | Missing | **SHOULD** |
| [CP-13](rows/CP.md#cp-13) | Offline counter (keeps selling when the connection drops) | No | Not public | Not public | Partial | Partial | **LATER** |
| [CP-14](rows/CP.md#cp-14) | Counter documents (printed or emailed receipts and invoices, signature capture) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [CP-15](rows/CP.md#cp-15) | Card surcharges | Yes | Not checked | Yes | Missing | Missing | **LATER** |
| [CP-16](rows/CP.md#cp-16) | Paint tinting and colour formulas | Yes | Not checked | Not checked | Missing | Missing | **LATER** |
| [CP-17](rows/CP.md#cp-17) | Large cash payment reporting | Not checked | Not checked | Yes | Missing | Missing | **LATER** |
| [CP-18](rows/CP.md#cp-18) | Cash rounding without pennies | Not checked | Not checked | Yes | Missing | Missing | **LATER** |
| [CP-19](rows/CP.md#cp-19) | Scale integration for bulk materials | Not checked | Yes | Not checked | Missing | Missing | **LATER** |

### QE: Quotes, estimates and takeoffs

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [QE-01](rows/QE.md#qe-01) | Quotes with expiry and revisions, converted to orders | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [QE-02](rows/QE.md#qe-02) | Job estimates (phases, house plans, packages, bid sections) | Partial | Partial | Yes | Missing | Missing | **SHOULD** |
| [QE-03](rows/QE.md#qe-03) | Plan takeoff (from drawings or PDF plans; takeoff tools) | No | Partial | Partial | Partial | Partial | **LATER** |
| [QE-04](rows/QE.md#qe-04) | Quote follow up and win or loss tracking | Partial | Partial | Partial | Partial | Partial | **SHOULD** |
| [QE-05](rows/QE.md#qe-05) | Kits, assemblies and templates on quotes and orders | Partial | Yes | Yes | Missing | Partial | **SHOULD** |
| [QE-06](rows/QE.md#qe-06) | Millwork, door and window configurators and vendor quote integration | No | Yes | Yes | Partial | Partial | **SHOULD** |
| [QE-07](rows/QE.md#qe-07) | Quote price protection (holding quoted prices while costs move) | Yes | Not public | Yes | Built | Built | **SHOULD** |

### SO: Sales orders, special orders, drop ship and will call

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [SO-01](rows/SO.md#so-01) | Sales orders (lines, ship to, job, customer PO, several ship dates or releases) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [SO-02](rows/SO.md#so-02) | Special orders (non stock; a linked PO from the order; the receipt allocated to the order) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [SO-03](rows/SO.md#so-03) | Drop ship (the vendor ships direct to the customer; linked PO) | Partial | Not public | Yes | Missing | Missing | **MUST** |
| [SO-04](rows/SO.md#so-04) | Will call and customer pickup (staging, notice, release with signature) | Yes | Partial | Partial | Missing | Partial | **MUST** |
| [SO-05](rows/SO.md#so-05) | Back orders on sales orders and release on receipt | Partial | Partial | Yes | Missing | Built | **MUST** |
| [SO-06](rows/SO.md#so-06) | Order holds and approvals (credit hold, margin approval, order authority) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [SO-07](rows/SO.md#so-07) | Pick tickets, picking and loading (yard tickets, mobile picking) | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [SO-08](rows/SO.md#so-08) | Fulfil from another yard (the sourcing branch differs from the selling branch) | Partial | Yes | Yes | Missing | Missing | **MUST** |
| [SO-09](rows/SO.md#so-09) | Jobs and projects on orders (job sites, lot and block) | Partial | Yes | Yes | Missing | Partial | **MUST** |
| [SO-10](rows/SO.md#so-10) | Charge lines and text lines (delivery and other charges, notes) | Yes | Yes | Yes | Missing | Built | **MUST** |
| [SO-11](rows/SO.md#so-11) | Blanket or call off orders (contract quantities released over time) | Partial | Yes | Yes | Missing | Missing | **SHOULD** |
| [SO-12](rows/SO.md#so-12) | Sales history and last price paid at order entry | Partial | Yes | Yes | Missing | Missing | **MUST** |
| [SO-13](rows/SO.md#so-13) | Customer PO capture into orders (OCR and EDI) | Not checked | Yes | Not checked | Partial | Partial | **LATER** |
| [SO-14](rows/SO.md#so-14) | Equipment and tool rentals | Not checked | Yes | Not checked | Missing | Missing | **LATER** |
| [SO-15](rows/SO.md#so-15) | Installed sales and crew scheduling | Not checked | Yes | Yes | Missing | Missing | **LATER** |
| [SO-16](rows/SO.md#so-16) | Need date commitment and promise alerts | Not checked | Not checked | Yes | Missing | Missing | **LATER** |

### PR: Pricing

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [PR-01](rows/PR.md#pr-01) | Price matrices (customer class or level by product group; margin, markup or list less) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [PR-02](rows/PR.md#pr-02) | Customer specific and contract pricing with effective dates | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [PR-03](rows/PR.md#pr-03) | Quantity breaks | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [PR-04](rows/PR.md#pr-04) | Branch pricing (prices differ by yard) | Partial | Yes | Yes | Missing | Missing | **MUST** |
| [PR-05](rows/PR.md#pr-05) | Margin floors (minimum gross margin, override with approval) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [PR-06](rows/PR.md#pr-06) | Price overrides and line discounts with reason codes and audit | Partial | Partial | Partial | Missing | Partial | **SHOULD** |
| [PR-07](rows/PR.md#pr-07) | Commodity and market cost updates (lumber market moves, mass price changes) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [PR-08](rows/PR.md#pr-08) | Promotions and sale pricing (including mix and match) | Partial | Yes | Partial | Partial | Partial | **SHOULD** |
| [PR-09](rows/PR.md#pr-09) | Job pricing held for a job or quote | Yes | Not public | Yes | Partial | Partial | **MUST** |
| [PR-10](rows/PR.md#pr-10) | Rounding rules and sub cent unit prices (per thousand board feet and the like) | Partial | Not public | Yes | Missing | Partial | **SHOULD** |
| [PR-11](rows/PR.md#pr-11) | Spiffs (per unit clerk incentives) | Yes | Not checked | Not checked | Missing | Missing | **LATER** |
| [PR-12](rows/PR.md#pr-12) | Customer profitability stratification | Not checked | Yes | Not checked | Missing | Missing | **LATER** |
| [PR-13](rows/PR.md#pr-13) | External price guidance | Not checked | Not checked | Yes | Missing | Partial | **LATER** |

### UM: Units of measure and conversions

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [UM-01](rows/UM.md#um-01) | Several units per item (stocking, selling, purchasing) with conversions | Yes | Yes | Yes | Missing | Partial | **MUST** |
| [UM-02](rows/UM.md#um-02) | Piece and lineal foot conversions (dimensional lumber by length) | Yes | Partial | Yes | Missing | Partial | **MUST** |
| [UM-03](rows/UM.md#um-03) | Board foot and per thousand (MBF) pricing | Yes | Partial | Yes | Missing | Partial | **MUST** |
| [UM-04](rows/UM.md#um-04) | Tally (mixed lengths on one line, at sale and at receipt) | Partial | Yes | Yes | Missing | Partial | **MUST** |
| [UM-05](rows/UM.md#um-05) | Bundle and unit (pieces per bundle or lift) | Yes | Yes | Yes | Missing | Partial | **MUST** |
| [UM-06](rows/UM.md#um-06) | Area, sheets, coverage and weight | Partial | Yes | Yes | Missing | Partial | **SHOULD** |

### IN: Inventory across branches

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [IN-01](rows/IN.md#in-01) | On hand by branch or yard and by bin or location | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [IN-02](rows/IN.md#in-02) | Committed or allocated quantities | Yes | Yes | Yes | Partial | Built | **MUST** |
| [IN-03](rows/IN.md#in-03) | Available by yard, seen across branches at order entry | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [IN-04](rows/IN.md#in-04) | Transfers between branches (request, ship, in transit, receive) | Yes | Yes | Yes | Missing | Missing | **MUST** |
| [IN-05](rows/IN.md#in-05) | Min and max and reorder points per branch; suggested replenishment | Yes | Yes | Yes | Partial | Partial (C4-1a) | **MUST** |
| [IN-06](rows/IN.md#in-06) | Cycle counts and physical inventory | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [IN-07](rows/IN.md#in-07) | Lot tracking and tally inventory (stock by length, lots, serials) | Partial | Yes | Yes | Missing | Missing | **MUST** |
| [IN-08](rows/IN.md#in-08) | Reman, cut to size and light manufacturing (work orders converting stock) | No | Yes | Yes | Missing | Missing | **SHOULD** |
| [IN-09](rows/IN.md#in-09) | Adjustments with reason codes | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [IN-10](rows/IN.md#in-10) | Valuation by branch (average cost, cost layers) and the GL tie out | Yes | Yes | Yes | Partial | Partial (C4-1a) | **MUST** |
| [IN-11](rows/IN.md#in-11) | Product master (item setup, attributes, images, vendors per item) | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [IN-12](rows/IN.md#in-12) | Bundle or lift identity (tagged units of stock) | No | Partial | Yes | Missing | Missing | **SHOULD** |
| [IN-13](rows/IN.md#in-13) | Store use and mistint accounts | Yes | Not checked | Not checked | Missing | Missing | **LATER** |
| [IN-14](rows/IN.md#in-14) | Consignment and vendor managed inventory | Not checked | Yes | Yes | Missing | Missing | **LATER** |
| [IN-15](rows/IN.md#in-15) | Vendor maintained product content catalog | Yes | Not checked | Not checked | Missing | Missing | **LATER** |

### PU: Purchasing and receiving

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [PU-01](rows/PU.md#pu-01) | Purchase orders (stock, special order, drop ship), by branch and central buying | Yes | Yes | Yes | Partial | Partial (C4-1a) | **MUST** |
| [PU-02](rows/PU.md#pu-02) | Receiving (partial, against the PO, mobile, into bins) | Yes | Yes | Yes | Partial | Partial (C4-1a) | **MUST** |
| [PU-03](rows/PU.md#pu-03) | Back orders on POs and expected dates | Yes | Not public | Yes | Partial | Partial | **MUST** |
| [PU-04](rows/PU.md#pu-04) | Landed cost (freight and duty into item cost) | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [PU-05](rows/PU.md#pu-05) | Vendor rebates and programs (accruals, buying group programs) | Partial | Yes | Partial | Partial | Partial | **SHOULD** |
| [PU-06](rows/PU.md#pu-06) | EDI with vendors (850, 855, 856, 810) and vendor catalogs and price files | Partial | Partial | Yes | Partial | Partial | **SHOULD** |
| [PU-07](rows/PU.md#pu-07) | Purchase suggestions (demand based, seasonal, min and max) | Yes | Yes | Yes | Partial | Partial (C4-1a) | **MUST** |
| [PU-08](rows/PU.md#pu-08) | Vendor invoice matching (two or three way) into AP | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [PU-09](rows/PU.md#pu-09) | PO approval limits | No | Partial | Not public | Missing | Missing | **SHOULD** |
| [PU-10](rows/PU.md#pu-10) | Supplier returns and vendor credit memos | Yes | Partial | Yes | Missing | Missing | **MUST** |
| [PU-11](rows/PU.md#pu-11) | Vendor price levels and vendor stock availability | Partial | Not public | Partial | Missing | Missing | **SHOULD** |

### DD: Delivery and dispatch

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [DD-01](rows/DD.md#dd-01) | Delivery scheduling and the dispatch board | Yes | Yes | Partial | Partial | Partial | **MUST** |
| [DD-02](rows/DD.md#dd-02) | Loads and load planning (weight, volume, truck capacity) | Partial | Yes | Yes | Partial | Partial | **SHOULD** |
| [DD-03](rows/DD.md#dd-03) | Trucks and equipment (fleet, trailers, boom or forklift, capacity) | Partial | Partial | Partial | Partial | Partial | **SHOULD** |
| [DD-04](rows/DD.md#dd-04) | Routes and runs (stop sequence, route optimisation) | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [DD-05](rows/DD.md#dd-05) | Proof of delivery (driver app, signature, photos, location) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [DD-06](rows/DD.md#dd-06) | Delivery tracking and customer notices | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [DD-07](rows/DD.md#dd-07) | Delivery charges and zones | Partial | Yes | Yes | Partial | Partial | **SHOULD** |
| [DD-08](rows/DD.md#dd-08) | Supplier pickups and customer returns on runs | Not public | Partial | Yes | Missing | Missing | **SHOULD** |
| [DD-09](rows/DD.md#dd-09) | Delivery from a yard other than the selling yard | Partial | Yes | Yes | Missing | Missing | **MUST** |
| [DD-10](rows/DD.md#dd-10) | Telematics integration | Not checked | Yes | Not checked | Missing | Missing | **LATER** |
| [DD-11](rows/DD.md#dd-11) | Parcel carrier shipping | Not checked | Not checked | Yes | Missing | Missing | **LATER** |

### AR: AR, credit, statements, finance charges, liens and job accounts

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [AR-01](rows/AR.md#ar-01) | Customer accounts, credit limits and terms | Yes | Yes | Yes | Partial | Built | **MUST** |
| [AR-02](rows/AR.md#ar-02) | Credit holds and the credit approval workflow | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [AR-03](rows/AR.md#ar-03) | Cash receipts and payment application (unapplied cash, write offs) | Yes | Yes | Yes | Partial | Built | **MUST** |
| [AR-04](rows/AR.md#ar-04) | Statements (by customer, by job) | Partial | Yes | Yes | Partial | Built | **MUST** |
| [AR-05](rows/AR.md#ar-05) | Finance or service charges | Yes | Yes | Yes | Missing | Missing | **MUST** |
| [AR-06](rows/AR.md#ar-06) | Lien management (preliminary notices, lien waivers, deadlines) | No | Partial | Partial | Missing | Missing | **SHOULD** |
| [AR-07](rows/AR.md#ar-07) | Job accounts (sub accounts per job, AR by job) | Partial | Partial | Yes | Missing | Partial | **MUST** |
| [AR-08](rows/AR.md#ar-08) | Collections and aging (dunning, collector notes) | Partial | Yes | Partial | Partial | Partial | **SHOULD** |
| [AR-09](rows/AR.md#ar-09) | Terms master and early payment discounts | Yes | Yes | Yes | Partial | Built | **MUST** |
| [AR-10](rows/AR.md#ar-10) | Retainage and progress billing | Partial | Partial | Partial | Missing | Missing | **LATER** |

### GL: AP, GL and branch consolidated reporting

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [GL-01](rows/GL.md#gl-01) | AP (vendor bills, approvals, payments, payment runs, 1099) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [GL-02](rows/GL.md#gl-02) | GL (chart of accounts, journals, periods, close) | Partial | Partial | Yes | Partial | Partial | **MUST** |
| [GL-03](rows/GL.md#gl-03) | Branch P and L and branch consolidated reporting | Yes | Yes | Yes | Missing | Missing | **MUST** |
| [GL-04](rows/GL.md#gl-04) | Bank reconciliation | Partial | Not public | Yes | Partial | Partial | **SHOULD** |
| [GL-05](rows/GL.md#gl-05) | Dashboards, KPIs and a report writer | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [GL-06](rows/GL.md#gl-06) | Sales tax reporting | Yes | Partial | Yes | Missing | Missing | **MUST** |
| [GL-07](rows/GL.md#gl-07) | Multi company (separate legal entities) | Partial | Partial | Yes | Missing | Missing | **LATER** |
| [GL-08](rows/GL.md#gl-08) | Accounting package export (where the dealer keeps an outside GL) | Yes | Yes | Partial | Missing | Missing | **SHOULD** |
| [GL-09](rows/GL.md#gl-09) | Foreign currency on purchasing and AP | Not checked | Yes | Yes | Missing | Partial | **LATER** |
| [GL-10](rows/GL.md#gl-10) | Embedded business bank account | Yes | Not checked | Not checked | Missing | Missing | **LATER** |

### PE: Contractor portal and eCommerce

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [PE-01](rows/PE.md#pe-01) | Contractor portal (account, invoices, statements, documents) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [PE-02](rows/PE.md#pe-02) | Online bill pay | Yes | Yes | Yes | Missing | Missing | **MUST** |
| [PE-03](rows/PE.md#pe-03) | Online ordering at the customer's own prices (B2B) | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [PE-04](rows/PE.md#pe-04) | Order and delivery status for customers (including proof of delivery) | Partial | Yes | Yes | Partial | Built | **MUST** |
| [PE-05](rows/PE.md#pe-05) | Quote requests and approvals online | Partial | Yes | Yes | Missing | Partial | **MUST** |
| [PE-06](rows/PE.md#pe-06) | Public web store (B2C) | Yes | Yes | Partial | Missing | Missing | **LATER** |
| [PE-07](rows/PE.md#pe-07) | Loyalty, rewards and gift cards | Partial | Yes | Yes | Missing | Missing | **LATER** |
| [PE-08](rows/PE.md#pe-08) | Scheduling by the contractor: a delivery window and a pickup slot per yard | Not checked | Not checked | Not checked | Not read | Partial | **MUST** |
| [PE-09](rows/PE.md#pe-09) | Customer quoting with markups: the contractor quotes their own customer from the dealer's products | Not checked | Not checked | Not checked | Not read | Missing | **MUST** |
| [PE-10](rows/PE.md#pe-10) | The selections loop: customer to contractor to dealer, with price walls and an approval record | Not checked | Not checked | Not checked | Not read | Missing | **MUST** |

### IX: Integrations

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [IX-01](rows/IX.md#ix-01) | Payment processors and card terminals | Partial | Yes | Yes | Partial | Partial | **MUST** |
| [IX-02](rows/IX.md#ix-02) | Sales tax engines | No | Yes | Yes | Partial | Partial | **SHOULD** |
| [IX-03](rows/IX.md#ix-03) | Buying groups and vendor programs (catalog and price feeds) | Partial | Partial | Partial | Partial | Partial | **SHOULD** |
| [IX-04](rows/IX.md#ix-04) | Accounting packages | Yes | Yes | Partial | Missing | Missing | **SHOULD** |
| [IX-05](rows/IX.md#ix-05) | Open API and webhooks | Partial | Partial | Yes | Partial | Partial | **SHOULD** |
| [IX-06](rows/IX.md#ix-06) | Takeoff and estimating software | No | Yes | Yes | Partial | Partial | **LATER** |
| [IX-07](rows/IX.md#ix-07) | CRM, email and text messaging | Partial | Yes | Partial | Partial | Built | **MUST** |
| [IX-08](rows/IX.md#ix-08) | Data migration from the incumbent system | Yes | Yes | Yes | Missing | Missing | **SHOULD** |
| [IX-09](rows/IX.md#ix-09) | BI and data warehouse export | Yes | Yes | Yes | Partial | Partial | **SHOULD** |

### MY: Multi yard platform

| Row | Capability | Rundoo | BisTrack | Agility | The MVP | Gable today | Priority |
|---|---|---|---|---|---|---|---|
| [MY-01](rows/MY.md#my-01) | Branch level security (users and roles held to branches) | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [MY-02](rows/MY.md#my-02) | Branch as a dimension on every document, GL entry and report | Yes | Partial | Yes | Partial | Partial | **MUST** |
| [MY-03](rows/MY.md#my-03) | Central and local control (central purchasing, pricing and setup across yards) | Partial | Partial | Yes | Partial | Partial | **SHOULD** |
| [MY-04](rows/MY.md#my-04) | Handhelds, barcode scanning and label printing | Yes | Yes | Yes | Partial | Partial | **MUST** |
| [MY-05](rows/MY.md#my-05) | Document forms (pick tickets, invoices, statements) with dealer branding | Partial | Partial | Yes | Partial | Built | **MUST** |
| [MY-06](rows/MY.md#my-06) | Cloud hosting, uptime and access from any device | Partial | Yes | Partial | Partial | Partial | **LATER** |
| [MY-07](rows/MY.md#my-07) | AI assistants and automation | Yes | Partial | Partial | Partial | Partial | **LATER** |
| [MY-08](rows/MY.md#my-08) | Document management and storage | Not checked | Yes | Yes | Partial | Missing | **LATER** |

## The plan: the MUST rows in build order

### Led by the Gable Building Supply demo cut

The build is led by the **Gable Building Supply** multi yard demo cut: the slices that make the multi yard operation
real end to end come first, with the rest of parity following in staged cuts.

- **The demo dealer.** Gable Building Supply, a three yard dealer in a US state with multiple tax counties: a main
  yard with a millwork shop, a contractor yard and a town store, each with its own register, stock, trucks and people;
  contractors with jobs and crews, plus walk in cash customers; dimensional lumber sold by the piece, the lineal foot
  and MBF with tallies, sheet goods, bundles, millwork and hardware; vendors; price levels, contracts and job prices;
  open documents across the yards so every walk starts from live data.
- **How each item is built.** One item is one branch `refactor/<area>-<item>` off `refactor/v1`: red first (a failing
  test before the code), a pull request into `refactor/v1`, one fresh independent review clean of P1 and P2 (a second
  read for the money, security and data classes), checks green, merged with a merge commit. Each item adds its walk to
  `docs/parity/WALKS.md`.
- **Ruled design and workspace path.** Rather than gating code on a separate drawing phase, surfaces build straight in
  code from the accepted design boards and reference workspace shapes in Gable's own colours and brand: **Industrial
  Dark** (`[data-theme="dark"]`, the default) and **Gable Light** (`[data-theme="light"]`, merged in PRs 147 and 158),
  white labeled per dealer, and verified in the **12-step walk order**:
  1. Front Door (`/`, role and persona launcher, sign in, session recovery)
  2. Counter Till (Panels counter board, scan/search, split tender, till open/close, Z report, receipts)
  3. Quote Desk (split pane builder, AI material list intake, price protection, revision and conversion)
  4. Buying and Stock (per yard stock, transfers, reorder points, POs, receiving, cycle counts, landed cost, vendors)
  5. Money Desk (AR worklist, credit holds, statements, cash application, AP bills, GL journals, financial statements, bank rec, tax, liens)
  6. Dispatch Board (order queue, truck capacity gauges, route sequence, multi yard fulfilment)
  7. Manager Home (role profiles, end of day by yard, branch and consolidated KPIs, scheduled reports)
  8. Dealer Admin (dealer profile, branches, staff roles and grants, sign in methods, hotkeys, exports, apps)
  9. Yard Pick (phone first pick queue, bin pull lines, will call staging and release, receiving, cycle counts)
  10. Drive Run (phone first driver route, stop progression, multi photo and signature proof of delivery)
  11. Contractor Pro Portal (phone first contractor shell, quotes, ordering at own prices, scheduling, invoices and online bill pay, customer quoting with markups, homeowner selections loop)
  12. Work Shell (desktop and tablet outer frame hosting the work apps with one header band, centred app selector, hidden side rail, and cross frame sign out)
- **Existing ADRs.** ADR 0006 (units and pricing), ADR 0008 (inventory identity and vendor intake, packages A to F)
  and ADR 0005 (money and AR) govern their modules; items build their packages without redesigning them.
- **Sizes.** Dev hour equivalents, including review and fix rounds. **Standalone** is the build size from scratch on
  `refactor/v1`; **Re-sized** is the net build size after reusing proven module, shell and portal foundations. Items
  already merged on `refactor/v1` at `695d587c` are marked `Done` (`0` remaining).

### The demo scenes and three build cuts

| Scene | What a person walks | Rows it closes | Items |
|---|---|---|---|
| 0. The world | Three yards, their people, stock, trucks, customers and open work | MY-01, AR-01 | M0a, M1 (merged at `695d587c`) |
| 1. Sign in and the shells | The front door, a role and yard picked, the work shell with its centred selector and hidden rail, the field shell on a phone, the pro shell | (the shell track) | S1 to S13 |
| 2. The counter at the town store | A walk in cash sale; a contractor charge with PO and job and live credit standing; split tender with a card on the test terminal; a receipt printed and emailed with the yard's own identity; a return with a restocking fee | CP-01 to CP-06, CP-11, CP-14, IX-01, IX-07, MY-05 | M0b, M3 (merged at `695d587c`), M24, M28, M29 |
| 3. A contractor's quote at the contractor yard | A deck package in MBF and pieces with a tally; the job's price; a margin floor warning and its approval; accept; convert to an order with two releases; a special order line that raises its purchase order; a deposit; stock at the main yard shown at entry; the last price paid | QE-01, UM-01 to UM-05, PR-01 to PR-05, PR-09, SO-01, SO-02, SO-03, SO-05, SO-06, SO-09, SO-10, SO-12, CP-07, IN-01 to IN-03, IN-07 | M5, M2, M23, M4, M16, M17, M18, M19, M32 |
| 4. Fulfil from the main yard and deliver | A transfer or a direct ship from the main yard; the pick and load in the field shell; the main yard's dispatch board (order queue, truck lanes, map); the driver's run with proof of delivery (signer, photo, place); the contractor's texted link; a will call at the counter | IN-04, SO-04, SO-07, SO-08, DD-01, DD-04 to DD-06, DD-09, MY-04 | M9, M20, M21, M33, M36 |
| 5. Buying at the main yard | Suggestions per yard; a purchase order; a receipt with freight spread into cost; a cycle count; a vendor return; a lumber market move applied as a mass price change; a vendor bill matched | IN-05, IN-06, IN-09 to IN-11, PU-01 to PU-04, PU-07, PU-08, PU-10, PR-07, GL-01 | M0c, M7, M8, M10 to M15, M22 |
| 6. The money desk and the Pro Portal | The controller's worklist; a statement run by job with pay links; finance charges; a credit hold at the counter lifted by a payment; job accounts; the contractor in the Pro Portal ordering at own prices, booking a delivery window, paying invoices online, quoting a homeowner with markups, and confirming approved homeowner selections into a dealer order | AR-02 to AR-05, AR-07, AR-09, PE-01 to PE-05, PE-08 to PE-10 | M25, M30, M31, M32, M35 |
| 7. Manager home | The end of day by yard; P and L by yard and consolidated; tax by jurisdiction and its report; dashboards by yard | CP-08 to CP-10, MY-02, GL-02, GL-03, GL-05, GL-06 | M6, M26, M27, M34 |

- **Cut 1, three yards, the counter, money and the Pro Portal (Ruled, Ruling 2):** scenes 0, 1, 2, 6 and 7's end of day
  (`M0a`, `M0b`, `M1`, `M3` already merged; `M5`, `M2`, `M6`, `M24`, `M25`, `M26`, `M28`, `M29`, `M30`, `M35`, and the
  shell track).
- **Cut 2, selling and delivering across yards (Ruled, Rulings 2 and 3):** scenes 3 and 4 (`M0c`, `M4`, `M7`, `M8`, `M9`,
  `M16` to `M21`, `M23`, `M33`, `M36`, plus the field shell offline store pulled into Cut 2 alongside `M21` and `M33`).
- **Cut 3, buying and the books (Ruled, Ruling 2):** scene 5 and the rest of scene 7 (`M10` to `M15`, `M22`, `M27`,
  `M31`, `M32`, `M34`).

### The items, in build order

Each MUST row (84 in all) closes in exactly one item, the one that writes its walk; an earlier item may advance it.
"After" names the items that must merge first.

**Phase 0, baseline foundations (`M0a` and `M0b` merged at `695d587c`; `M0c` in flight).**

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M0a | SEC-NOHEADER-1 (PR 119, merged) | | MY-01 | A user with no branch in context held to the user's own grants on every read and act; the tax exemption routes walled; the seed's customer branch rows and their backfill | | security, data | 12 to 24 (merged) | Done (PR 119) |
| M0b | C2-5, POS and till (PR 72, merged) | | CP-01, CP-03, CP-06, CP-08, CP-11 | The counter on the contract: tenders as payments, returns as credit memos bounded by the sale with reason codes, till sessions, the refund intents and their rail; Panels counter board and blind till close merged in PRs 128, 138, 143, 148, 150, 168 | M0a | money | 16 to 32 (merged) | Done (PR 72) |
| M0c | C4-1a, inventory, purchase orders, vendors and EDI partners on the module recipe | | PU-01, PU-02, IN-05 | The paused work (`refactor/c4-1a-inventory-purchasing-2`) resumed: the recipe's errors, numbers, money and events on those modules; stock levels and reorder recommendations per branch, counting on order and back orders | | data | 40 to 70 | 40 to 70 |

**Phase 1, the multi yard base (`M1` and `M3` merged at `695d587c`).** Pricing, then units on documents, then stock
identity, run one after another, as ADR 0006 (section 9.1) and ADR 0008 require; `M6` runs beside that chain.

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M1 | Multi yard setup and the two yard demo (PRs 119, 131, 137, merged) | MY-01, AR-01 | | Multi yard on at setup and in the demo seed; each yard with its own register, stock and people granted to it; customers as dealer wide accounts any yard sells to, linked to each yard they trade at by their first document there (Ruling A) | M0a | security, data | 16 to 32 (merged) | Done (PRs 119, 131, 137) |
| M5 | Pricing by yard (C3-2A-pricing, ADR 0006) | PR-01, PR-02, PR-03, PR-04, PR-09 | | Price levels and profiles; yard base prices; the resolver; contracts and rules with effective dates, a yard, and update and delete routes; job price lists; category pricing on by default; within the pricing module only, as ADR 0006 sets | | money | 40 to 64 | 39 to 62 |
| M3 | Documents and a real sender (PRs 125, 153, 154, 156, 163, merged) | IX-07, MY-05 | CP-14, AR-04 | An SMTP email sender behind the notifier with `email_deliveries` logging; the dealer's profile and locale; forms carrying the dealer's and the yard's name, logo, address and remit to; invoice, statement (tied to aging and unapplied cash), quote, purchase order, counter receipt and pick ticket PDFs | | none | 32 to 52 (merged) | Done (PRs 125, 153, 154, 163) |
| M6 | A branch on the ledger | MY-02, GL-02, GL-03 | | A branch on every journal line from every posting path; fiscal periods, subledger tie out and year end close to retained earnings; P and L, Balance Sheet and Trial Balance by branch and consolidated, with GL detail drill down | | money | 48 to 80 | 28 to 42 |
| M2 | Units and the resolver on documents (C3-2B, ADR 0006) | UM-01, UM-02, UM-03, UM-05 | | Orders, invoices, credit memos, the counter, the portal, purchase orders and receipts in any unit of the product's set, the resolved factor stored on each line; the resolver in every document; the stocking unit price hold lifted (MBF) only now | M0b, M5 | money | 48 to 80 | 48 to 80 |
| M4 | Stock identity (ADR 0008 package A) | IN-01, IN-02, IN-03, IN-07, CP-01 | | One stock row per product, yard, bin, lot and length; the move ledger; allocations that carry identity; counter sales and voids at the register's own yard; available by yard at quote and order entry; tally and lots in stock | M0b, M0c, M2 | data, money | 56 to 90 | 43 to 65 |

**Phase 2, stock and buying across yards.**

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M7 | Coded adjustments, cost and the receipt's posting (ADR 0008 package B) | IN-09, IN-10 | PU-02 | Reason codes, movement rows and events, GL at average cost by branch; the receipt's posting (inventory against received not invoiced); valuation by branch tied to the ledger | M4, M6 | money, data | 32 to 56 | 22 to 38 |
| M8 | Purchase orders and receiving | PU-01, PU-02, PU-03 | | Revisions, single ordering contact dispatch and status rules on top of M0c; the branch chosen on create; a receipt document, receiving difference log and the over receipt rule; expected dates per line; the back order report; close short | M0c, M7 | money | 28 to 44 | 16 to 22 |
| M9 | Transfers between yards | IN-04 | | Request, pick, ship, stock and value in transit with transit days ETA, receipt at the far yard, GL by branch | M4, M7 | money, data | 40 to 64 | 32 to 50 |
| M10 | Min and max per yard, and purchase suggestions: what M0c leaves | IN-05, PU-07 | | Points per yard maintained through the API; projected position (available plus in transit plus on order) and order multiple suggestions priced from vendor costs, by vendor, with seasonality left to SHOULD | M0c, M4 | none | 8 to 16 | 4 to 8 |
| M11 | Counts by bin | IN-06 | | Count sessions, frozen snapshots, maker checker variance review and reason coded posting with the GL | M4, M7 | money | 28 to 44 | 20 to 30 |
| M12 | The product master | IN-11 | | General product edit; vendors per item with item numbers, UOM factors and costs; UPCs; multi image gallery; per yard settings (single product pictures and AI illustrations landed in PRs 142, 144, 152) | M0c | none | 20 to 32 | 17 to 26 |
| M13 | Landed cost | PU-04 | | Freight, duty and handling at receipt, spread by value, weight or quantity into cost (with special buy exclusion and late bill retro adjustment), with GL | M8 | money | 20 to 32 | 12 to 18 |
| M14 | Vendor returns and credit memos (ADR 0008 package E) | PU-10 | | Returns taking stock out at cost; vendor warranty/RMA claims, credit memos matched to returns and applied to bills; recovery board and GL | M7 | money | 24 to 40 | 18 to 28 |
| M15 | Matching, payment runs and 1099s | PU-08, GL-01 | | Bills matched to receipts by item (two and three way, several bills per order); payment runs with check and ACH files; 1099 tracking; a branch on vendor payments | M6, M8 | money | 36 to 56 | 26 to 40 |

**Phase 3, selling across yards.**

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M16 | Quotes that expire and revise | QE-01 | | Expiry enforced, stored revision lineage and deep clone (accept and convert in either order landed in PR 122; split pane builder and intake landed in PRs 130 and 136) | | none | 16 to 24 | 15 to 21 |
| M17 | Orders with releases, jobs and the last price paid | SO-01, SO-05, SO-09, SO-12 | | Ship dates per line and release; job routes with site, lot and block, and contact; back order expected dates from the linked purchase order; sales history and the last price paid at quote and order entry | M8 | none | 36 to 60 | 31 to 51 |
| M18 | Special orders and drop ship (ADR 0008 package C) | SO-02, SO-03 | | The linked purchase order from the order line; the receipt allocated to that line; direct ship billed from the vendor's confirmation | M4, M8 | money | 32 to 52 | 32 to 52 |
| M19 | Margin floors and approvals | PR-05, SO-06 | | A margin floor on quote, order and counter lines (Market Check floor suggestions landed in PR 145); an approval queue whose every override carries a reason; the credit and margin holds in one place | M2 | money | 28 to 44 | 20 to 30 |
| M33 | Delivery by yard | DD-01, DD-04, DD-05, DD-06 | | A home yard per truck and driver and a load building board per yard; each route starting at its yard; route edit and cancel; proof of delivery attestation with place, photos and signer; customer notices through the sender | M3 | none | 32 to 52 | 16 to 20 |
| M20 | Fulfil and ship from another yard | SO-08, DD-09 | | A sourcing yard per line; availability and allocation there; a transfer or direct ship from it; the margin and GL split between yards | M7, M9, M33 | money | 32 to 52 | 31 to 49 |
| M21 | Picking and will call | SO-04, SO-07 | | Picks that persist (picked and loaded states, bins, quantities, status guards on cancelled/done tasks, a load sheet); will call staging, a ready notice and release with a signature | M4 | none | 32 to 48 | 22 to 28 |
| M22 | Mass price and cost change | PR-07 | | Changes by category, vendor or yard, by percent or amount, with a 4-way diff preview and an audit; vendor price files into costs (the price file part of ADR 0008 package F; its feed intake endpoints stay SHOULD with PU-06) | M5, M0c | money | 20 to 32 | 14 to 22 |
| M23 | Tally on documents | UM-04 | | Tally on order, invoice, fulfilment, counter and receipt lines, kept through convert | M2, M4 | money | 24 to 40 | 24 to 40 |

**Phase 4, the counter and the money.**

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M24 | Account sales, tender rules and counter documents | CP-02, CP-03, CP-04, CP-11, CP-14 | | Contact buyer authority, cash only accounts, check limits, ID capture and store credit on top of PRs 72, 128, 138, 143, 148, 150; account notes at entry; 80mm thermal receipt layout and signature capture at the counter | M0b, M2, M3 | money | 40 to 64 | 33 to 48 |
| M25 | Credit holds and approvals | AR-02 | | Customer level `credit_holds` table, automated hold evaluation sweep with dry run preview, time bounded overrides with audit, and credit limit change workflow on top of PR 72's counter gate | M0b | money | 24 to 40 | 16 to 28 |
| M26 | Till control and end of day by branch | CP-08, CP-09 | | Drops, pickups, paid in and out, and variance sign off threshold on top of PR 72 and PR 168's blind close; API enforcement of open till on every sale; the daily close by branch with deposit reconciliation; daily sales by branch | M0b, M6 | money | 32 to 52 | 26 to 40 |
| M27 | Tax by jurisdiction, and sales tax reports | CP-10, GL-06 | | Jurisdictions and rates by yard or ship to; order exemption certificate validity checks (expiry, effective date, state); taxability per line; GL 2200 tie out, remittance filing and reports by jurisdiction and branch | M2, M6 | money | 40 to 64 | 32 to 49 |
| M29 | The card terminal and payment gateway seams | CP-05, IX-01 | PE-02 | A terminal seam with a pre-charge attempt ledger, sweep and test terminal (sale, void and refund at the terminal, settlement); an online gateway seam with a test gateway; one live processor needs the dealer's account | M0b | money, security | 40 to 64 | 33 to 50 |
| M28 | Returns: what M0b leaves | CP-06 | | Restocking fees, exchanges in one act, card refunds at the terminal, and a closed reason code list behind the return's reason | M0b, M29 | money | 12 to 20 | 7 to 11 |
| M30 | Statements and finance charges | AR-04, AR-05 | | A batch statement run (mass or interim) with outbox queueing and run history on top of PRs 153, 154 and 163's single customer statement PDF and email; finance charge rules and a periodic run that bills them, with GL | M3, M6 | money | 32 to 52 | 26 to 42 |
| M31 | Job accounts | AR-07 | | Job sub accounts with a credit limit, terms, billing contact, balance and aging; payments to a job | M17 | money | 32 to 48 | 32 to 48 |
| M32 | Deposits, charge lines and terms where people work | CP-07, SO-10, AR-03, AR-09 | | The API follow ups their screens need: a deposit at the counter, the early pay discount at apply, multi invoice batch receipt allocation with dry run preview and payment reversal | M0b | money | 12 to 20 | 6 to 8 |

**Phase 5, reporting, the contractor Pro Portal and the yard.**

| # | Item | Closes | Advances | What it delivers | After | Class | Standalone | Re-sized |
|---|---|---|---|---|---|---|---|---|
| M34 | Reports by branch, delivered | GL-05 | | A branch field and cut on every canned and builder report; 7 role dashboard profiles and quick actions; scheduled reports emailed as PDF or CSV; margin and GL reports | M3, M6 | none | 24 to 40 | 18 to 30 |
| M35 | The contractor Pro Portal (native B2B portal, scheduling, customer quoting and selections loop) | PE-01, PE-02, PE-03, PE-04, PE-05, PE-08, PE-09, PE-10 | | Phone first Pro shell in both themes with dealer branding and 5 contractor roles (`PE-01`); quote requests from a list or photo, share links and online accept/decline (`PE-05`); B2B catalog at contractor waterfall prices, per yard stock/lead time, reorder with price diff preview and checkout with yard/PO/ship to (`PE-03`); delivery windows and pickup slots per yard feeding dispatch (`PE-08`); 5-step order/delivery timeline with POD and notices (`PE-04`); statements, aging and invoice PDFs plus online bill pay and saved methods through the gateway seam (`PE-01, PE-02`); offline read cache on the change feed; contractor quoting to the contractor's own customer with three tier basis point markups (`line -> category -> quote`), labour/overhead lines, `hide_line_prices` toggle, and contractor branded PDF with own terms and tax (`PE-09`, Ruling 5c); homeowner selections capability link with no account and optional 6-digit verification code on sign off (`Ruling 5b`), shared project and customer pipeline visibility for the dealer while Price Wall 1 hides contractor markup/sell price from the dealer and Price Wall 2 hides dealer cost/markup from the customer (`Ruling 5a`), and 1-click confirmation into the dealer order (`PE-10`) | M3, M5, M2, M29 | money, security | 180 to 280 | 116 to 178 |
| M36 | Scanning and labels | MY-04 | | UPC and bin barcodes matched; labels (bin, bundle, shelf, item) printed | M4 | none | 20 to 32 | 16 to 24 |

Outside Phase 1's serial chain, an item starts when its "After" items have merged; two items that change the same
module (the counter, allocation, jobs) do not run at once.

### The contractor Pro Portal breakdown (`M35`)

`M35` is sized as a complete vertical slice (backend, Pro shell frame `S12`, mobile/web screens, second reads and
walks), already crediting `refactor/v1`'s existing `core/internal/portal/`, `core/internal/project/`, PR 153/154/163
statement and invoice PDFs, and PR 158 portal theme tokens:

| # | Part | Rows | Standalone | Re-sized |
|---|---|---|---|---|
| 1 | Phone first Pro frame in the shell, both themes, sign in and 5-role contractor RBAC (covers `S12`) | PE-01 | 16 to 24 | 8 to 12 |
| 2 | Quotes: request from a list or photo, expiring share link, accept with attestation, decline | PE-05 | 12 to 20 | 6 to 10 |
| 3 | Ordering at own prices: catalog allowlist with per yard stock/lead time, cart, checkout, reorder with price diffs | PE-03 | 12 to 20 | 6 to 10 |
| 4 | Scheduling: delivery windows and pickup slots per yard, core and portal, feeding dispatch | PE-08 | 20 to 32 | 18 to 28 |
| 5 | Payments, reads: cursor paginated invoices, statements, aging bucket tiles and PDFs in integer cents | PE-01 | 12 to 16 | 6 to 8 |
| 6 | Online bill pay through the gateway seam: saved methods, single flight retry guard, sandboxed card token handoff | PE-02 | 20 to 32 | 10 to 16 |
| 7 | Offline reads on the `since`/ETag change feed and 5-step order/delivery status timeline with POD | PE-04 | 12 to 20 | 10 to 16 |
| 8 | Reviews, visual reviews and the contractor's walk for parts 1 to 7 | PE-01 to PE-05, PE-08 | 12 to 16 | 12 to 16 |
| 9 | Customer quoting with markups (Ruling 5c): contractor's customer quote, three tier `line -> category -> quote` basis point markup, labour/overhead, `hide_line_prices` lump sum toggle, contractor terms/tax and PDF | PE-09 | 24 to 40 | 12 to 20 |
| 10 | Selections loop (Rulings 5a, 5b): capability link with no account and optional 6-digit code on sign off, shared project/customer pipeline visibility for the dealer, Price Wall 1 (no markup/sell price to dealer) and Price Wall 2 (no dealer cost/markup to customer) with wire tests, 1-click confirm to dealer order | PE-10 | 32 to 48 | 20 to 30 |
| 11 | Security and money second reads, and the selections loop in the contractor's walk | PE-09, PE-10 | 8 to 12 | 8 to 12 |
| | **Pro Portal total (`M35`)** | | **180 to 280** | **116 to 178** |

### The screens

Each MUST row with a staff or field screen gets one in its role workspace. Of the original 48 surfaces (`240 to 480`),
the portal's surfaces (`10 to 20`) are counted inside `M35` above, and `refactor/v1` at `695d587c` has already landed
the Panels counter board and blind till close (PRs 128, 138, 143, 148, 150, 168), the split pane quote builder and
embedded material list intake (PRs 130, 132, 136, 142), Market Check (PR 145), and document print/email actions (PRs
125, 153, 154, 163) (`30 to 60` delivered). The remaining ~40 MUST staff and field surfaces (across Counter, Quotes,
Orders, Pricing, Units and Tally, Buying and Stock, Dispatch, Money Desk, Manager Home, Dealer Admin, Yard Pick, and
Drive Run) size at **200 to 400 standalone**, and **120 to 240 re-sized** by adapting existing role micro app views.

### The shell track (beside the items)

The role micro apps run in Gable's own Tauri shells in Gable's design system: a work shell for office and counter
people (desktop and tablet), a field shell for the yard and drivers (phone first), and a pro shell for contractors.
One header holds a centred app and surface selector; a pinned rail is hidden by default with hover reveal and pin
modes; phones get no rail and native mobile patterns; a front door with a role picker leads into them; money never
queues offline; each dealer chooses its sign in methods.

| # | Item | Standalone | Re-sized |
|---|---|---|---|
| S1 | CI runs every web package's tests | 3 to 5 | 2 to 3 |
| S2 | The design system's shell layer and framed header protocol (Gable's tokens) | 8 to 12 | 4 to 6 |
| S3 | The shell chrome: state, elements, the centred selector, the hidden rail, phones without a rail | 26 to 38 | 14 to 20 |
| S4 | The Tauri workspace and its shared core (keychain custody, profiles, deep links) | 10 to 14 | 5 to 7 |
| S5 | The manifest and generated shell profiles | 6 to 9 | 3 to 5 |
| S6 | The money never queues guard (the offline sale replay retired or refused by rule) | 6 to 12 | 6 to 12 |
| S7 | The shell host on the work profile (single header band, chrome action forwarding, nonce sign out) | 16 to 24 | 8 to 12 |
| S8 | An installed shell reaches the dealer's API (CORS for the branch header, runtime API config) | 8 to 12 | 4 to 6 |
| S9 | Sign in, and sign in methods per dealer (Ruling 4: `email_password`, `passkey`, `magic_link`, `google`, `microsoft`, OIDC allowlist, runtime discovery, session recovery) | 24 to 36 | 16 to 24 |
| S10 | The micro app entries, and the work shell's apps (counter, sales, dispatch, buying and stock, money, pricing, manager home, admin) | 42 to 78 | 24 to 44 |
| S11 | The field shell (yard and drive), with an Android build | 24 to 36 | 14 to 20 |
| S12 | The pro shell (the portal frame) | Counted in M35 (Part 1) | Counted in M35 (Part 1) |
| S13 | Demo people per role, signed in on the two yard demo (front door persona cards landed in PR 123; 13-role catalog remaining) | 8 to 12 (after PR 123) | 4 to 6 |
| | **Shell track remaining** | **181 to 288** | **104 to 165** |

An offline store for the field shell (`Ruling 3`, pulled into Cut 2 alongside `M21` and `M33`: `wa-sqlite` over OPFS
with IndexedDB fallback, canonical outbox, idempotency key ledger, and status guarded sync doors for yard picks, counts
and driver proof of delivery with multi photo queueing; money never queues) is **64 to 99 standalone**, and **28 to 44
re-sized**.

### The design track

- **Ruled design path.** Gable Light (`G3` and `G4`, `24 to 36`) is already merged on `refactor/v1` in PRs 147 and 158
  alongside Industrial Dark, with automated contrast verification across desk and portal tokens. Under the product
  owner's design path ruling, surfaces build straight in code from accepted design boards in Gable's colours and brand
  (white labeled per dealer) and are walked in the 12-step walk order, removing the separate phase-ahead drawing gate
  (`G2`, `96 to 144`).
- **Remaining design system pass (`G1`).** Closing the remaining design system component and brand asset gaps (shell
  header selector, hidden rail pin states, sync and conflict cards, light background lockup, and three shell app icons)
  sizes at **16 to 24 standalone**, and **10 to 14 re-sized**.

| # | Item | Standalone | Re-sized |
|---|---|---|---|
| G1 | The design system's shell chrome components and brand asset pass | 16 to 24 | 10 to 14 |
| G2 | MUST surfaces built straight in code from accepted boards (ruled design path, walked in 12-step order) | Ruled (in screens/M35) | Ruled (in screens/M35) |
| G3 | Gable Light design system palette and contrast scale (PRs 147, 158, merged) | 16 to 24 (merged) | Done (PRs 147, 158) |
| G4 | Gable Light in `tokens.css` with automated contrast tests (PRs 147, 158, merged) | 8 to 12 (merged) | Done (PRs 147, 158) |
| | **Design track remaining** | **16 to 24** | **10 to 14** |

### Totals

| Track | Full standalone (before G1) | Delivered at `695d587c` / ruled | Remaining standalone at `695d587c` | Saved by reuse | Remaining re-sized at `695d587c` |
|---|---|---|---|---|---|
| MUST items (`M0a` to `M36`, including `M35` Pro Portal) | 1,284 to 2,088 | 76 to 140 (`M0a, M0b, M1, M3`) | 1,208 to 1,948 | 277 to 500 | **931 to 1,448** |
| Their screens (staff and field; Pro Portal in `M35`) | 230 to 460 | 30 to 60 (counter, quote, market check, docs) | 200 to 400 | 80 to 160 | **120 to 240** |
| The shell track (`S1` to `S13`; `S12` in `M35`) | 185 to 294 | 4 to 6 (`S13` PR 123) | 181 to 288 | 77 to 123 | **104 to 165** |
| The design track (`G1` to `G4`) | 136 to 204 | 120 to 180 (`G3, G4` PRs 147/158; `G2` ruled) | 16 to 24 | 6 to 10 | **10 to 14** |
| **The MUST plan** | **1,835 to 3,046** | **230 to 386** | **1,605 to 2,660** | **440 to 793** | **1,165 to 1,867** |
| Field offline store (`Ruling 3`, pulled into Cut 2) | 64 to 99 | 0 | 64 to 99 | 36 to 55 | **28 to 44** |
| **The MUST plan + Field offline store (`Cuts 1 to 3`)** | **1,899 to 3,145** | **230 to 386** | **1,669 to 2,759** | **476 to 848** | **1,193 to 1,911** |
| Other SHOULD rows (when briefed) | 600 to 1,000 | 0 | 600 to 1,000 | 110 to 200 | **490 to 800** |

### Rulings

- **1. Customers across yards (`M1`): Ruled and merged (PRs 119, 131, 137).** Every customer is a dealer wide account
  that any yard's people may sell to; `customer_branches` records the yards it trades at, with its home yard; the first
  document at a new yard adds the link via `customerlink.Ensure`, audited; branch bound keys keep their pin; finance
  and reports keep following grants.
- **1b. Navigation, shells and design path (`G1` to `G4`, `S1` to `S13`): Ruled.** Adopt the three shell architecture
  (work shell with centred header selector and hidden rail replacing workspace tabs, field shell for yard and drivers,
  phone first pro shell for contractors), built straight in code from accepted boards in Gable's colours and brand
  (Industrial Dark default and Gable Light, white labeled per dealer), and walked in the 12-step walk order. See
  [GABLE-DISTINCT.md](GABLE-DISTINCT.md).
- **2. The build order and demo cut (`Cuts 1, 2, 3`): Ruled.** Stage the build in three demo cuts (`Cut 1 -> Cut 2 ->
  Cut 3`) walked in the 12-step design walk order (`0` brand and tokens through `11` admin and multi yard settings),
  with a US county/state seed profile added for county tax, 1099 and lien walks. Cut 1 delivers the shells (`S1` to
  `S12`), counter completion (`M24, M28, M29`), pricing/units foundation (`M5, M2`), money desk (`M25, M30`), manager
  end of day (`M6, M26`), and the Contractor Pro Portal (`M35`), while Cut 2 (`M0c, M4, M7, M8, M9, M16..M21, M23, M33,
  M36` plus the field offline store) and Cut 3 (`M10..M15, M22, M27, M31, M32, M34`) backend packages run ahead in
  parallel once their ADR prerequisites (`M5 -> M2 -> M4`) merge.
- **3. Offline (`S6` and Field offline store): Ruled.** Enforce "money never queues offline" everywhere (`S6` retires
  the legacy offline sale replay route in Cut 1; `CP-13` counter offline stays LATER); build the Pro Portal read only
  cache on the `since` and ETag change feed in `M35` Part 7 (`10 to 16` re-sized); and pull the `28 to 44` re-sized
  field shell offline store (yard picks, cycle counts, and driver proof of delivery with multi photo queueing) into Cut
  2 alongside `M21` and `M33`.
- **4. Sign in methods per dealer (`S9`): Ruled.** Per dealer sign in method allowlist (`email_password`, `passkey`,
  `magic_link`, `google`, `microsoft`, plus standard OpenID Connect for self hosted dealers) discovered at runtime by
  the front door and shells, with token free role identity retention across reloads and native OS keychain token custody
  in Tauri (`16 to 24` re-sized, down from `24 to 36` standalone).
- **5. Contractor Pro Portal rulings (`M35`, `PE-08` to `PE-10`): Ruled.**
  - **5a. Shared project and customer visibility vs markup privacy (`PE-09`, `PE-10`): Ruled.** Shared project and
    customer visibility by default between the contractor and the dealer (so the dealer gets project level insights: job
    site address, project/selection status, customer/site contact, and material pipeline as the natural quid pro quo for
    providing the contractor a free business tool), while keeping the contractor's markup and customer sell price
    strictly private (`Price Wall 1`: the dealer never sees the contractor's markup rules, basis points, labour/overhead
    lines, or customer sell price; `Price Wall 2`: the customer never sees the dealer's price or the markup), plus
    1-click CSV/JSON export for the contractor.
  - **5b. Customer identity on an approval and link lifetime (`PE-10`): Ruled.** Capability link with no homeowner
    account required, plus an optional email/SMS 6-digit verification code on final acceptance (256-bit random token
    stored as a SHA-256 hash in `pro_portal_share_tokens`, lifetime defaulting to the quote's `valid_until` timestamp
    bounded by a configurable maximum TTL, revocable and rotatable by the contractor), with identity captured at sign off
    (typed full name, email or phone, drawn or typed signature, timestamp, IP/user-agent, and an immutable SHA-256
    snapshot of the approved option selections and total).
  - **5c. The contractor's mark, terms and tax on the customer quote (`PE-09`): Ruled.** Three tier markup precedence
    (`line override -> category rule -> quote default` in basis points, supporting both markup on cost and target gross
    margin), separate flat/hourly labour and flat/percent overhead lines, a `hide_line_prices` lump sum toggle, the
    contractor's own logo, payment terms, deposit percent and customer site tax rate (or tax included display) under
    `Price Wall 2` (dealer cost and contractor markup never enter the homeowner's JSON response or PDF, wire tested),
    and `valid_until` bounded by the dealer's source quote price protection horizon.

