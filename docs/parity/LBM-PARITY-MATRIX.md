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
  the page it rests on (R, B and A numbers, listed in the appendix). A separate check fetched every cited page
  and corrected each claim its page did not support:
  Rundoo, 114 pages fetched, 22 rows corrected.
  BisTrack, 72 pages fetched, 31 rows corrected.
  Agility, 161 pages fetched, 19 rows corrected.
- **Evidence.** "documented" means a help page, document or data sheet describes it; "marketing only" means
  only a product page or a review claims it, without detail; "behind a demo" means the vendor says it exists
  and the detail sits behind a demo, a customer login or a sales call; "Not public" means nothing public was
  found, which is not the same as the product lacking it. BisTrack's documentation sits behind Epicor's customer
  login, so most of its rows rest on Epicor's product pages and data sheets.
- **Gable today** is read from the code on `refactor/v1` at `58dde7e8`, with file and line links. "Built" means
  routes, storage and tests exist for the row's core and it holds across yards where the row needs it;
  "Partial" names what is missing; work still on an open pull request is named.
- **Priority** is for a multi yard dealer's core operation. MUST: daily core operation needs it and the products
  compared document it. SHOULD: expected, but a dealer runs without it for a while, or it is an add on or partial
  elsewhere. LATER: a differentiator, an add on in one product, blocked from outside, or not core operation.

## At a glance

| | Yes | Partial | No | Not public |
|---|---|---|---|---|
| Rundoo | 57 | 55 | 9 | 1 |
| BisTrack | 72 | 42 | 0 | 8 |
| Agility | 100 | 20 | 0 | 2 |

Gable at `58dde7e8`, over the 122 listed rows: 9 built, 84 partial, 29 missing. Priorities over all 142 rows: 80 MUST, 33 SHOULD, 29 LATER.

The three products, in their own words and their reviewers':

- **Rundoo.** Rundoo (rundoo.ai) is an AI-first, browser based POS and ERP for independent supply stores (hardware, paint, farm and feed, garden, lumber), with 500+ stores in the US, Canada and the Caribbean. It runs on Google Cloud, uses Stripe as its only card processor and has no offline mode. One bundle covers POS, inventory, purchasing, pricing, AR and statements, a built in GL exporting to QuickBooks or Sage, deliveries, e-commerce, staff and customer apps, and the Dooey AI assistant. Rundoo says it fits one yard to a few, not large multi branch dealers, distribution centers or millwork and truss work. No public price list: quotes scale with locations, registers and add-ons plus Stripe fees; a review site cites from $499 monthly.
- **BisTrack.** Epicor BisTrack is Epicor's ERP for lumber and building materials dealers, merchants and distributors, from single yards to dealers with 100 or more stores. Epicor now leads with BisTrack in the Cloud on Microsoft Azure and the browser based BisTrack Web; on-prem installs remain but face limited support. Core covers counter and POS, quotes and orders, pricing, inventory across branches, purchasing, Journey Planner dispatch, AR, AP, GL, CRM and dashboards. Add-ons include BisTrack Delivery, Rentals, Field Scheduling, Customer Stratification and the BisTrack API; Epicor applications include WMS, Financials, IP&O, Payments, Grow BI and Commerce; partners include Trimble, Avalara, TradeTek and AR or AP Assist. Pricing is not public; cloud is sold as a subscription.
- **Agility.** DMSi Agility is DMSi's flagship ERP for building materials distributors, lumber wholesalers, millwork shops and pro dealers; DMSi now steers retail LBM dealers to its separate web product, Frameworks. Agility runs DMSi hosted (SaaS over terminal services, with Agility Web replacing classic screens) or locally hosted. DMSi cites 400+ customers across 2,500+ locations, so multi branch work is native: branch ship-to records, organization security and shared tables, though branch transfers need the separately licensed Branch Transfers module (A125). Core spans POS, quotes, orders, pricing, inventory, purchasing, reman, dispatch, A/R, A/P and G/L. Separate apps and add ons include Mobile POD, Mobile Sales, Mobile Warehouse Tools, Commerce Cloud, Billing Portal, CRM, BInformed, Installed Sales and the Branch Transfers module, plus EDI, Worldpay, Avalara, ZIP Tax, E-Commerce Gateway and Loftware interfaces. No public pricing.

## The matrix

Each row links its detail: every product's claim with the pages it rests on, and Gable's code with file and
line links (`rows/`, one file per group). Every cited page is listed in [SOURCES.md](SOURCES.md).

### CP: Counter and POS

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [CP-01](rows/CP.md#cp-01) | Cash sale at the counter (walk in or cash customer, receipt) | Yes | Yes | Yes | Partial (PR 72) | **MUST** |
| [CP-02](rows/CP.md#cp-02) | Account (charge) sale at the counter, with the customer PO and job where the account requires them | Yes | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-03](rows/CP.md#cp-03) | Tender types and split tender (cash, check, card, on account, store credit or gift card) | Yes | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-04](rows/CP.md#cp-04) | Tender rules (check limits, cash only accounts, PO required, ID, change, over and under tender) | Partial | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-05](rows/CP.md#cp-05) | Integrated card processing at the counter (card present terminal, EMV, tap) | Yes | Yes | Yes | Missing (PR 72) | **MUST** |
| [CP-06](rows/CP.md#cp-06) | Returns and exchanges at the counter (to the original tender, restocking fees, reason codes) | Partial | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-07](rows/CP.md#cp-07) | Deposits and prepayments on orders, applied at invoice | Yes | Partial | Yes | Built | **MUST** |
| [CP-08](rows/CP.md#cp-08) | Cash drawer and till (open, close, over and short, X and Z reports, drops and pickups) | Yes | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-09](rows/CP.md#cp-09) | End of day by branch (daily close, deposit reconciliation, daily sales per branch) | Yes | Partial | Yes | Partial (PR 72, PR 119) | **MUST** |
| [CP-10](rows/CP.md#cp-10) | Sales tax at the sale (jurisdiction by branch or ship to, exemption certificates) | Partial | Yes | Yes | Partial (PR 72) | **MUST** |
| [CP-11](rows/CP.md#cp-11) | Counter alerts and holds (credit hold at the sale, account notes, warnings) | Partial | Partial | Yes | Partial (PR 72) | **MUST** |
| [CP-12](rows/CP.md#cp-12) | Mobile sales in the yard (tablet or handheld) | Partial | Yes | Partial | Missing | **SHOULD** |
| [CP-13](rows/CP.md#cp-13) | Offline counter (keeps selling when the connection drops) | No | Not public | Not public | Partial (PR 72) | **LATER** |
| [CP-14](rows/CP.md#cp-14) | Counter documents (printed or emailed receipts and invoices, signature capture) | Yes | Yes | Yes | Partial | **MUST** |
| [CP-15](rows/CP.md#cp-15) | Card surcharges | Yes | Not checked | Yes | Missing | **LATER** |
| [CP-16](rows/CP.md#cp-16) | Paint tinting and colour formulas | Yes | Not checked | Not checked | Missing | **LATER** |
| [CP-17](rows/CP.md#cp-17) | Large cash payment reporting | Not checked | Not checked | Yes | Missing | **LATER** |
| [CP-18](rows/CP.md#cp-18) | Cash rounding without pennies | Not checked | Not checked | Yes | Missing | **LATER** |
| [CP-19](rows/CP.md#cp-19) | Scale integration for bulk materials | Not checked | Yes | Not checked | Missing | **LATER** |

### QE: Quotes, estimates and takeoffs

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [QE-01](rows/QE.md#qe-01) | Quotes with expiry and revisions, converted to orders | Yes | Partial | Yes | Partial | **MUST** |
| [QE-02](rows/QE.md#qe-02) | Job estimates (phases, house plans, packages, bid sections) | Partial | Partial | Yes | Missing | **SHOULD** |
| [QE-03](rows/QE.md#qe-03) | Plan takeoff (from drawings or PDF plans; takeoff tools) | No | Partial | Partial | Partial | **LATER** |
| [QE-04](rows/QE.md#qe-04) | Quote follow up and win or loss tracking | Partial | Partial | Partial | Partial | **SHOULD** |
| [QE-05](rows/QE.md#qe-05) | Kits, assemblies and templates on quotes and orders | Partial | Yes | Yes | Partial | **SHOULD** |
| [QE-06](rows/QE.md#qe-06) | Millwork, door and window configurators and vendor quote integration | No | Yes | Yes | Partial | **SHOULD** |
| [QE-07](rows/QE.md#qe-07) | Quote price protection (holding quoted prices while costs move) | Yes | Not public | Yes | Built | **SHOULD** |

### SO: Sales orders, special orders, drop ship and will call

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [SO-01](rows/SO.md#so-01) | Sales orders (lines, ship to, job, customer PO, several ship dates or releases) | Yes | Yes | Yes | Partial | **MUST** |
| [SO-02](rows/SO.md#so-02) | Special orders (non stock; a linked PO from the order; the receipt allocated to the order) | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [SO-03](rows/SO.md#so-03) | Drop ship (the vendor ships direct to the customer; linked PO) | Partial | Not public | Yes | Missing | **MUST** |
| [SO-04](rows/SO.md#so-04) | Will call and customer pickup (staging, notice, release with signature) | Yes | Partial | Partial | Partial | **MUST** |
| [SO-05](rows/SO.md#so-05) | Back orders on sales orders and release on receipt | Partial | Partial | Yes | Built | **MUST** |
| [SO-06](rows/SO.md#so-06) | Order holds and approvals (credit hold, margin approval, order authority) | Partial | Partial | Yes | Partial | **MUST** |
| [SO-07](rows/SO.md#so-07) | Pick tickets, picking and loading (yard tickets, mobile picking) | Partial | Yes | Yes | Partial | **MUST** |
| [SO-08](rows/SO.md#so-08) | Fulfil from another yard (the sourcing branch differs from the selling branch) | Partial | Yes | Yes | Missing | **MUST** |
| [SO-09](rows/SO.md#so-09) | Jobs and projects on orders (job sites, lot and block) | Partial | Yes | Yes | Partial | **MUST** |
| [SO-10](rows/SO.md#so-10) | Charge lines and text lines (delivery and other charges, notes) | Yes | Yes | Yes | Built | **MUST** |
| [SO-11](rows/SO.md#so-11) | Blanket or call off orders (contract quantities released over time) | Partial | Yes | Yes | Missing | **SHOULD** |
| [SO-12](rows/SO.md#so-12) | Sales history and last price paid at order entry | Partial | Yes | Yes | Missing | **SHOULD** |
| [SO-13](rows/SO.md#so-13) | Customer PO capture into orders (OCR and EDI) | Not checked | Yes | Not checked | Partial | **LATER** |
| [SO-14](rows/SO.md#so-14) | Equipment and tool rentals | Not checked | Yes | Not checked | Missing | **LATER** |
| [SO-15](rows/SO.md#so-15) | Installed sales and crew scheduling | Not checked | Yes | Yes | Missing | **LATER** |
| [SO-16](rows/SO.md#so-16) | Need date commitment and promise alerts | Not checked | Not checked | Yes | Missing | **LATER** |

### PR: Pricing

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [PR-01](rows/PR.md#pr-01) | Price matrices (customer class or level by product group; margin, markup or list less) | Yes | Yes | Yes | Partial | **MUST** |
| [PR-02](rows/PR.md#pr-02) | Customer specific and contract pricing with effective dates | Yes | Yes | Yes | Partial | **MUST** |
| [PR-03](rows/PR.md#pr-03) | Quantity breaks | Yes | Yes | Yes | Partial | **MUST** |
| [PR-04](rows/PR.md#pr-04) | Branch pricing (prices differ by yard) | Partial | Yes | Yes | Missing | **MUST** |
| [PR-05](rows/PR.md#pr-05) | Margin floors (minimum gross margin, override with approval) | Partial | Partial | Yes | Partial | **MUST** |
| [PR-06](rows/PR.md#pr-06) | Price overrides and line discounts with reason codes and audit | Partial | Partial | Partial | Partial (PR 72) | **MUST** |
| [PR-07](rows/PR.md#pr-07) | Commodity and market cost updates (lumber market moves, mass price changes) | Partial | Partial | Yes | Partial (C4-1a) | **MUST** |
| [PR-08](rows/PR.md#pr-08) | Promotions and sale pricing (including mix and match) | Partial | Yes | Partial | Partial | **SHOULD** |
| [PR-09](rows/PR.md#pr-09) | Job pricing held for a job or quote | Yes | Not public | Yes | Partial | **MUST** |
| [PR-10](rows/PR.md#pr-10) | Rounding rules and sub cent unit prices (per thousand board feet and the like) | Partial | Not public | Yes | Partial | **SHOULD** |
| [PR-11](rows/PR.md#pr-11) | Spiffs (per unit clerk incentives) | Yes | Not checked | Not checked | Missing | **LATER** |
| [PR-12](rows/PR.md#pr-12) | Customer profitability stratification | Not checked | Yes | Not checked | Missing | **LATER** |
| [PR-13](rows/PR.md#pr-13) | External price guidance | Not checked | Not checked | Yes | Missing | **LATER** |

### UM: Units of measure and conversions

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [UM-01](rows/UM.md#um-01) | Several units per item (stocking, selling, purchasing) with conversions | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [UM-02](rows/UM.md#um-02) | Piece and lineal foot conversions (dimensional lumber by length) | Yes | Partial | Yes | Partial (C4-1a) | **MUST** |
| [UM-03](rows/UM.md#um-03) | Board foot and per thousand (MBF) pricing | Yes | Partial | Yes | Partial | **MUST** |
| [UM-04](rows/UM.md#um-04) | Tally (mixed lengths on one line, at sale and at receipt) | Partial | Yes | Yes | Partial | **MUST** |
| [UM-05](rows/UM.md#um-05) | Bundle and unit (pieces per bundle or lift) | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [UM-06](rows/UM.md#um-06) | Area, sheets, coverage and weight | Partial | Yes | Yes | Partial | **SHOULD** |

### IN: Inventory across branches

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [IN-01](rows/IN.md#in-01) | On hand by branch or yard and by bin or location | Yes | Yes | Yes | Partial (PR 72) | **MUST** |
| [IN-02](rows/IN.md#in-02) | Committed or allocated quantities | Yes | Yes | Yes | Built | **MUST** |
| [IN-03](rows/IN.md#in-03) | Available by yard, seen across branches at order entry | Yes | Yes | Yes | Partial | **MUST** |
| [IN-04](rows/IN.md#in-04) | Transfers between branches (request, ship, in transit, receive) | Yes | Yes | Yes | Missing | **MUST** |
| [IN-05](rows/IN.md#in-05) | Min and max and reorder points per branch; suggested replenishment | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [IN-06](rows/IN.md#in-06) | Cycle counts and physical inventory | Yes | Yes | Yes | Partial (PR 72) | **MUST** |
| [IN-07](rows/IN.md#in-07) | Lot tracking and tally inventory (stock by length, lots, serials) | Partial | Yes | Yes | Missing | **MUST** |
| [IN-08](rows/IN.md#in-08) | Reman, cut to size and light manufacturing (work orders converting stock) | No | Yes | Yes | Missing | **SHOULD** |
| [IN-09](rows/IN.md#in-09) | Adjustments with reason codes | Partial | Partial | Yes | Partial | **MUST** |
| [IN-10](rows/IN.md#in-10) | Valuation by branch (average cost, cost layers) and the GL tie out | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [IN-11](rows/IN.md#in-11) | Product master (item setup, attributes, images, vendors per item) | Yes | Partial | Yes | Partial | **MUST** |
| [IN-12](rows/IN.md#in-12) | Bundle or lift identity (tagged units of stock) | No | Partial | Yes | Missing | **SHOULD** |
| [IN-13](rows/IN.md#in-13) | Store use and mistint accounts | Yes | Not checked | Not checked | Missing | **LATER** |
| [IN-14](rows/IN.md#in-14) | Consignment and vendor managed inventory | Not checked | Yes | Yes | Missing | **LATER** |
| [IN-15](rows/IN.md#in-15) | Vendor maintained product content catalog | Yes | Not checked | Not checked | Missing | **LATER** |

### PU: Purchasing and receiving

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [PU-01](rows/PU.md#pu-01) | Purchase orders (stock, special order, drop ship), by branch and central buying | Yes | Yes | Yes | Partial (C4-1a, PR 119) | **MUST** |
| [PU-02](rows/PU.md#pu-02) | Receiving (partial, against the PO, mobile, into bins) | Yes | Yes | Yes | Partial (C4-1a, PR 72) | **MUST** |
| [PU-03](rows/PU.md#pu-03) | Back orders on POs and expected dates | Yes | Not public | Yes | Partial | **MUST** |
| [PU-04](rows/PU.md#pu-04) | Landed cost (freight and duty into item cost) | Partial | Yes | Yes | Partial | **MUST** |
| [PU-05](rows/PU.md#pu-05) | Vendor rebates and programs (accruals, buying group programs) | Partial | Yes | Partial | Partial | **SHOULD** |
| [PU-06](rows/PU.md#pu-06) | EDI with vendors (850, 855, 856, 810) and vendor catalogs and price files | Partial | Partial | Yes | Partial | **SHOULD** |
| [PU-07](rows/PU.md#pu-07) | Purchase suggestions (demand based, seasonal, min and max) | Yes | Yes | Yes | Partial (C4-1a) | **MUST** |
| [PU-08](rows/PU.md#pu-08) | Vendor invoice matching (two or three way) into AP | Partial | Yes | Yes | Partial | **MUST** |
| [PU-09](rows/PU.md#pu-09) | PO approval limits | No | Partial | Not public | Missing | **SHOULD** |
| [PU-10](rows/PU.md#pu-10) | Supplier returns and vendor credit memos | Yes | Partial | Yes | Missing | **MUST** |
| [PU-11](rows/PU.md#pu-11) | Vendor price levels and vendor stock availability | Partial | Not public | Partial | Missing | **SHOULD** |

### DD: Delivery and dispatch

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [DD-01](rows/DD.md#dd-01) | Delivery scheduling and the dispatch board | Yes | Yes | Partial | Partial | **MUST** |
| [DD-02](rows/DD.md#dd-02) | Loads and load planning (weight, volume, truck capacity) | Partial | Yes | Yes | Partial | **SHOULD** |
| [DD-03](rows/DD.md#dd-03) | Trucks and equipment (fleet, trailers, boom or forklift, capacity) | Partial | Partial | Partial | Partial | **MUST** |
| [DD-04](rows/DD.md#dd-04) | Routes and runs (stop sequence, route optimisation) | Partial | Yes | Yes | Built | **MUST** |
| [DD-05](rows/DD.md#dd-05) | Proof of delivery (driver app, signature, photos, location) | Yes | Yes | Yes | Partial | **MUST** |
| [DD-06](rows/DD.md#dd-06) | Delivery tracking and customer notices | Yes | Yes | Yes | Partial | **MUST** |
| [DD-07](rows/DD.md#dd-07) | Delivery charges and zones | Partial | Yes | Yes | Partial | **SHOULD** |
| [DD-08](rows/DD.md#dd-08) | Supplier pickups and customer returns on runs | Not public | Partial | Yes | Missing | **SHOULD** |
| [DD-09](rows/DD.md#dd-09) | Delivery from a yard other than the selling yard | Partial | Yes | Yes | Missing | **MUST** |
| [DD-10](rows/DD.md#dd-10) | Telematics integration | Not checked | Yes | Not checked | Missing | **LATER** |
| [DD-11](rows/DD.md#dd-11) | Parcel carrier shipping | Not checked | Not checked | Yes | Missing | **LATER** |

### AR: AR, credit, statements, finance charges, liens and job accounts

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [AR-01](rows/AR.md#ar-01) | Customer accounts, credit limits and terms | Yes | Yes | Yes | Partial (PR 119) | **MUST** |
| [AR-02](rows/AR.md#ar-02) | Credit holds and the credit approval workflow | Partial | Yes | Yes | Partial (PR 72) | **MUST** |
| [AR-03](rows/AR.md#ar-03) | Cash receipts and payment application (unapplied cash, write offs) | Yes | Yes | Yes | Built | **MUST** |
| [AR-04](rows/AR.md#ar-04) | Statements (by customer, by job) | Partial | Yes | Yes | Partial | **MUST** |
| [AR-05](rows/AR.md#ar-05) | Finance or service charges | Yes | Yes | Yes | Missing | **MUST** |
| [AR-06](rows/AR.md#ar-06) | Lien management (preliminary notices, lien waivers, deadlines) | No | Partial | Partial | Missing | **SHOULD** |
| [AR-07](rows/AR.md#ar-07) | Job accounts (sub accounts per job, AR by job) | Partial | Partial | Yes | Partial | **MUST** |
| [AR-08](rows/AR.md#ar-08) | Collections and aging (dunning, collector notes) | Partial | Yes | Partial | Partial (PR 119) | **SHOULD** |
| [AR-09](rows/AR.md#ar-09) | Terms master and early payment discounts | Yes | Yes | Yes | Built | **MUST** |
| [AR-10](rows/AR.md#ar-10) | Retainage and progress billing | Partial | Partial | Partial | Missing | **LATER** |

### GL: AP, GL and branch consolidated reporting

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [GL-01](rows/GL.md#gl-01) | AP (vendor bills, approvals, payments, payment runs, 1099) | Partial | Partial | Yes | Partial (PR 119) | **MUST** |
| [GL-02](rows/GL.md#gl-02) | GL (chart of accounts, journals, periods, close) | Partial | Partial | Yes | Partial | **MUST** |
| [GL-03](rows/GL.md#gl-03) | Branch P and L and branch consolidated reporting | Yes | Yes | Yes | Missing | **MUST** |
| [GL-04](rows/GL.md#gl-04) | Bank reconciliation | Partial | Not public | Yes | Partial | **SHOULD** |
| [GL-05](rows/GL.md#gl-05) | Dashboards, KPIs and a report writer | Yes | Yes | Yes | Partial (PR 119) | **MUST** |
| [GL-06](rows/GL.md#gl-06) | Sales tax reporting | Yes | Partial | Yes | Missing | **MUST** |
| [GL-07](rows/GL.md#gl-07) | Multi company (separate legal entities) | Partial | Partial | Yes | Missing | **LATER** |
| [GL-08](rows/GL.md#gl-08) | Accounting package export (where the dealer keeps an outside GL) | Yes | Yes | Partial | Missing | **SHOULD** |
| [GL-09](rows/GL.md#gl-09) | Foreign currency on purchasing and AP | Not checked | Yes | Yes | Partial | **LATER** |
| [GL-10](rows/GL.md#gl-10) | Embedded business bank account | Yes | Not checked | Not checked | Missing | **LATER** |

### PE: Contractor portal and eCommerce

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [PE-01](rows/PE.md#pe-01) | Contractor portal (account, invoices, statements, documents) | Yes | Yes | Yes | Partial | **MUST** |
| [PE-02](rows/PE.md#pe-02) | Online bill pay | Yes | Yes | Yes | Missing | **MUST** |
| [PE-03](rows/PE.md#pe-03) | Online ordering at the customer's own prices (B2B) | Yes | Yes | Yes | Partial | **SHOULD** |
| [PE-04](rows/PE.md#pe-04) | Order and delivery status for customers (including proof of delivery) | Partial | Yes | Yes | Built | **MUST** |
| [PE-05](rows/PE.md#pe-05) | Quote requests and approvals online | Partial | Yes | Yes | Partial | **SHOULD** |
| [PE-06](rows/PE.md#pe-06) | Public web store (B2C) | Yes | Yes | Partial | Missing | **LATER** |
| [PE-07](rows/PE.md#pe-07) | Loyalty, rewards and gift cards | Partial | Yes | Yes | Missing | **LATER** |

### IX: Integrations

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [IX-01](rows/IX.md#ix-01) | Payment processors and card terminals | Partial | Yes | Yes | Partial | **MUST** |
| [IX-02](rows/IX.md#ix-02) | Sales tax engines | No | Yes | Yes | Partial | **SHOULD** |
| [IX-03](rows/IX.md#ix-03) | Buying groups and vendor programs (catalog and price feeds) | Partial | Partial | Partial | Partial | **SHOULD** |
| [IX-04](rows/IX.md#ix-04) | Accounting packages | Yes | Yes | Partial | Missing | **SHOULD** |
| [IX-05](rows/IX.md#ix-05) | Open API and webhooks | Partial | Partial | Yes | Partial | **SHOULD** |
| [IX-06](rows/IX.md#ix-06) | Takeoff and estimating software | No | Yes | Yes | Partial | **LATER** |
| [IX-07](rows/IX.md#ix-07) | CRM, email and text messaging | Partial | Yes | Partial | Partial | **MUST** |
| [IX-08](rows/IX.md#ix-08) | Data migration from the incumbent system | Yes | Yes | Yes | Missing | **SHOULD** |
| [IX-09](rows/IX.md#ix-09) | BI and data warehouse export | Yes | Yes | Yes | Partial | **SHOULD** |

### MY: Multi yard platform

| Row | Capability | Rundoo | BisTrack | Agility | Gable today | Priority |
|---|---|---|---|---|---|---|
| [MY-01](rows/MY.md#my-01) | Branch level security (users and roles held to branches) | Yes | Partial | Yes | Partial (PR 119) | **MUST** |
| [MY-02](rows/MY.md#my-02) | Branch as a dimension on every document, GL entry and report | Yes | Partial | Yes | Partial (PR 119) | **MUST** |
| [MY-03](rows/MY.md#my-03) | Central and local control (central purchasing, pricing and setup across yards) | Partial | Partial | Yes | Partial | **SHOULD** |
| [MY-04](rows/MY.md#my-04) | Handhelds, barcode scanning and label printing | Yes | Yes | Yes | Partial | **MUST** |
| [MY-05](rows/MY.md#my-05) | Document forms (pick tickets, invoices, statements) with dealer branding | Partial | Partial | Yes | Partial | **MUST** |
| [MY-06](rows/MY.md#my-06) | Cloud hosting, uptime and access from any device | Partial | Yes | Partial | Partial | **LATER** |
| [MY-07](rows/MY.md#my-07) | AI assistants and automation | Yes | Partial | Partial | Partial | **LATER** |
| [MY-08](rows/MY.md#my-08) | Document management and storage | Not checked | Yes | Yes | Missing | **LATER** |

## The plan: the MUST rows in build order

### How each item is built

- One item is one branch `refactor/<area>-<item>` off `refactor/v1`: red first (a failing test before the code), a
  pull request into `refactor/v1`, one fresh review clean of P1 and P2 (a second read for the money, security and
  data classes), checks green, merged with a merge commit.
- Each item adds its walk to [WALKS.md](WALKS.md): steps a person can follow on the demo seed with at least two
  yards to see the rows it closes working.
- A row's API, data and tests come first. Its screen is drawn in the design files before it is coded (the design
  track below), and becomes its own screen item once drawn.
- The ADRs already designed several items: ADR 0006 (units and pricing), ADR 0008 (inventory identity and vendor
  intake, packages A to F), ADR 0005 (money and AR). An item that builds one of their packages says so; it does
  not redesign it.
- Sizes are dev hour equivalents and include review and fix rounds.

### The items, in build order

**Phase 0, in flight.** These close the work already open.

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M0a | SEC-NOHEADER-1 (pull request 119) to merge | MY-01 | A user with no branch in context held to the user's own grants on every read and act; the tax exemption routes walled; the seed's customer branch rows and their backfill | security, data | 12 to 24 |
| M0b | C2-5, POS and till (pull request 72) to merge | CP-01, CP-03, CP-06, CP-08, CP-11 | The counter on the contract: tenders as payments, returns as credit memos, till sessions, the refund intents and their rail; takes `refactor/v1` in after M0a, then its money review pair | money | 16 to 32 |
| M0c | C4-1a, inventory, purchase orders, vendors and EDI partners on the module recipe | PU-01, PU-02, IN-05 | The paused work resumed: the recipe's errors, numbers, money and events on those modules, so the inventory and purchasing items build on one contract | data | 40 to 70 |

**Phase 1, the multi yard base.** Everything after it reads a yard.

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M1 | Multi yard setup and the two yard demo | MY-01, AR-01 | Multi yard on at setup and in the demo seed; at least two yards with registers, stock and people granted per yard; customers every yard can sell to, by the rule the product owner sets; the first walks | security, data | 16 to 32 |
| M2 | Units on documents (C3-2B, ADR 0006) | UM-01, UM-02, UM-03, UM-05 | Orders, invoices, the counter, purchase orders and receipts in any unit of the product's set, the resolved factor stored on each line, a price unit apart from the stocking unit (MBF) | money | 40 to 64 |
| M3 | Documents and a real sender | IX-07, MY-05, CP-14 | An email sender behind the notifier (SMTP; a local catcher in the dev stack); forms carrying the dealer's and the yard's name, logo, address and remit to; receipt, invoice, statement, quote, purchase order and pick ticket documents | none | 32 to 52 |
| M4 | Stock identity (ADR 0008 package A) | IN-01, IN-02, IN-03, IN-07, CP-01 | One stock row per product, yard, bin, lot and length; the move ledger; allocations that carry identity; counter sales and voids at the register's own yard; available by yard at quote and order entry; tally and lots in stock | data, money | 56 to 90 |
| M5 | Pricing in documents and by yard (C3-2A-pricing, ADR 0006) | PR-01, PR-02, PR-03, PR-04, PR-09 | The scaled price engine in orders and at the counter; yard base prices and price levels; contracts and rules with effective dates, a yard, and update and delete routes; job price lists; category pricing on by default | money | 48 to 80 |
| M6 | A branch on the ledger | MY-02, GL-02, GL-03 | A branch on every journal line from every posting path; fiscal periods and year end close; P and L by branch and consolidated, with a branch column | money | 48 to 80 |

**Phase 2, stock and buying across yards.**

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M7 | Coded adjustments, cost and valuation (ADR 0008 package B) | IN-09, IN-10 | Reason codes, movement rows and events, GL at average cost by branch; receipts posting to inventory; valuation by branch tied to the ledger | money, data | 32 to 52 |
| M8 | Purchase orders and receiving | PU-01, PU-02, PU-03 | Numbers, revisions and status rules; the branch chosen on create; a receipt document and the over receipt rule; received not invoiced posting; expected dates per line; the back order report; close short | money | 32 to 52 |
| M9 | Transfers between yards | IN-04 | Request, pick, ship, stock and value in transit, receipt at the far yard, GL by branch | money, data | 40 to 64 |
| M10 | Min and max per yard, and purchase suggestions | IN-05, PU-07 | Points per yard; suggestions that count on order and back orders, with vendor costs | none | 20 to 32 |
| M11 | Counts by bin | IN-06 | Count sessions, snapshots, variance review and posting with the GL | money | 28 to 44 |
| M12 | The product master | IN-11 | General product edit; vendors per item with item numbers and costs; UPCs; images; per yard settings | none | 20 to 32 |
| M13 | Landed cost | PU-04 | Freight and other landed costs at receipt, spread by value, weight or quantity into cost, with GL | money | 20 to 32 |
| M14 | Vendor returns and credit memos (ADR 0008 package E) | PU-10 | Returns taking stock out at cost; vendor credit memos matched to returns and applied to bills; GL | money | 24 to 40 |
| M15 | Matching and payment runs | PU-08, GL-01 | Bills matched to receipts by item (two and three way, several bills per order); payment runs with check and ACH files; a branch on vendor payments | money | 32 to 52 |

**Phase 3, selling across yards.**

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M16 | Quotes that expire and revise | QE-01 | Expiry enforced, stored revisions, accept and convert in either order | none | 16 to 24 |
| M17 | Orders with releases and jobs | SO-01, SO-09, SO-05 | Ship dates per line and release; job routes with site, lot and block, and contact; back order expected dates from the linked purchase order | none | 32 to 52 |
| M18 | Special orders and drop ship (ADR 0008 package C) | SO-02, SO-03 | The linked purchase order from the order line; the receipt allocated to that line; direct ship billed from the vendor's confirmation | money | 32 to 52 |
| M19 | Margin floors, override reasons and approvals | PR-05, PR-06, SO-06 | A margin floor on quote, order and counter lines; overrides with reason codes, an approval queue and an audit report | money | 32 to 52 |
| M20 | Fulfil and ship from another yard | SO-08, DD-09 | A sourcing yard per line; availability and allocation there; a transfer or direct ship from it; the margin and GL split between yards | money | 32 to 52 |
| M21 | Picking and will call | SO-07, SO-04 | Picks that persist (picked and loaded states, bins, quantities, a load sheet); will call staging, a ready notice and release with a signature | none | 32 to 48 |
| M22 | Mass price and cost change | PR-07 | Changes by category, vendor or yard, by percent or amount, with a preview and an audit; vendor price files into costs | money | 20 to 32 |
| M23 | Tally on documents | UM-04 | Tally on order, invoice, fulfilment, counter and receipt lines, kept through convert | money | 24 to 40 |

**Phase 4, the counter and the money.**

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M24 | Account sales and tender rules | CP-02, CP-04, CP-11, CP-03 | A PO and job on a counter sale; PO required and cash only accounts; the credit check and account notes at the counter; validated tender methods; split tender and store credit | money | 32 to 52 |
| M25 | Credit holds and approvals | AR-02 | Customer level hold and stop flags; a credit limit change workflow with its history; the check on every order, counter and delivery path | money | 24 to 40 |
| M26 | Till control and end of day by branch | CP-08, CP-09 | Drops, pickups, paid in and out; no sale without an open till; the daily close by branch with deposit reconciliation; daily sales by branch | money | 32 to 52 |
| M27 | Tax by jurisdiction, and sales tax reports | CP-10, GL-06 | Jurisdictions and rates by yard or ship to; exemption certificates checked for validity; taxability per line; reports by jurisdiction and branch | money | 40 to 64 |
| M28 | Returns against the original sale | CP-06 | Returns checked against the sale's lines and quantities, reason codes, restocking fees, exchanges, to the original tender | money | 20 to 32 |
| M29 | The card terminal and payment gateway seams | CP-05, IX-01, PE-02 | A terminal seam with a test terminal (sale, void and refund at the terminal, settlement); an online gateway seam with a test gateway; one live processor needs the dealer's account | money, security | 40 to 64 |
| M30 | Statements and finance charges | AR-04, AR-05 | A statement run for all or chosen customers, by job, as documents and email; finance charge rules and a periodic run that bills them, with GL | money | 32 to 52 |
| M31 | Job accounts | AR-07 | Job sub accounts with a credit limit, terms, billing contact, balance and aging; payments to a job | money | 32 to 48 |
| M32 | Deposits, charge lines and terms where people work | CP-07, SO-10, AR-09, AR-03 | The API follow ups their screens need: a deposit at the counter, the discount at apply, batch receipt entry | money | 12 to 20 |

**Phase 5, delivery, reporting, the portal and the yard.**

| # | Item | Rows | What it delivers | Class | Size |
|---|---|---|---|---|---|
| M33 | Delivery by yard | DD-01, DD-03, DD-04, DD-05, DD-06 | A home yard per truck and driver and a board per yard; each route starting at its yard; route edit and cancel; proof of delivery with place and signer; customer notices through the sender | none | 32 to 52 |
| M34 | Reports by branch, delivered | GL-05 | A branch field and cut on every canned and builder report; scheduled reports emailed as PDF or CSV; margin and GL reports | none | 24 to 40 |
| M35 | The contractor portal | PE-01, PE-02, PE-04 | Statements and aging in the portal; invoice documents; money in cents on the recipe; paying invoices through the gateway seam; notices | money | 32 to 52 |
| M36 | Scanning and labels | MY-04 | UPC and bin barcodes matched; labels (bin, bundle, shelf, item) printed | none | 20 to 32 |

Items can run in parallel where their rows do not depend on each other: phase 1's items after M0b and M0c, and
then one item from each of phases 2, 3 and 4 at a time, since each phase holds its own modules. Phase 2 needs M4
and M6; phase 3 needs M2, M4 and M5; phase 4 needs M0b, M5 and M6.

### The screens

Each MUST row with a screen gets one once its drawing is accepted: about 48 surfaces (the counter 5, quotes 1,
orders 5, pricing 5, units and tally 2, inventory 7, purchasing 6, delivery 3, AR 5, ledger and tax 3, the portal 2,
setup and the yard 4). Many extend a page the desk already has. **About 240 to 480.**

### The shell track (beside the items)

The role micro apps run in Gable's own Tauri shells, on a shell architecture adapted from an internal FutureBuild
codebase, in Gable's design system: a work shell for office and counter people (desktop and tablet), a field shell
for the yard and drivers (phone first), a pro shell for contractors. One header holds a centred app and surface
selector; a pinned rail is hidden by default with hover reveal and pin modes; phones get no rail and native mobile
patterns; a front door with a role picker leads into them; money never queues offline; each dealer chooses its sign
in methods.

| # | Item | Size |
|---|---|---|
| S1 | CI runs every web package's tests | 3 to 5 |
| S2 | The design system's shell layer (Gable's tokens) | 8 to 12 |
| S3 | The shell chrome: state, elements, the centred selector, the hidden rail, phones without a rail | 26 to 38 |
| S4 | The Tauri workspace and its shared core | 10 to 14 |
| S5 | The manifest and generated shell profiles | 6 to 9 |
| S6 | The money never queues guard (the offline sale replay retired or refused by rule) | 6 to 12 |
| S7 | The shell host on the work profile | 16 to 24 |
| S8 | An installed shell reaches the dealer's API (CORS for the branch header, the API address at run time) | 8 to 12 |
| S9 | Sign in, and sign in methods per dealer | 24 to 36 |
| S10 | The micro app entries, and the work shell's apps (counter, sales, dispatch, buying and stock, money, pricing, manager home, admin) | 42 to 78 |
| S11 | The field shell (yard and drive), with an Android build | 24 to 36 |
| S12 | The pro shell (the portal) | 12 to 18 |
| S13 | Demo people per role, signed in on the two yard demo | 12 to 18 |
| | **Shell track** | **about 200 to 315** |

An offline store for the field shell (picks, counts and proof of delivery queue; money never does) is SHOULD, after
the shells: about 64 to 99.

### The design track

- **The design files.** "Gable: design system" is production: its colours, type, components and gaps pages stay,
  and its gaps are closed first, with the shell chrome drawn as components in Gable's tokens. "Gable: screens"
  holds renders of today's desk pages and boards rebuilt from them, plus a proposed pricing page: they move to an
  archive page as each surface's production board replaces them.
- **Industrial Dark first.** Every MUST surface is drawn in Industrial Dark, Gable's default, inside its shell, per
  role, at desktop, wide and tablet, and at phone where a field role uses it; drawings run a phase ahead of the
  screens that need them.
- **Gable light.** A calm, natural light mode is its own design system item before any light board is drawn: a warm
  stone ground, cards a step lighter (never pure white), raised elements a step above, soft tinted shadows; ramps on
  one shared lightness scale; Gable Green, Blueprint Blue and Safety Red deepened on their own hues to meet 3:1 for
  controls and 4.5:1 for text; the same typefaces and radius. Its token change follows in code with a contrast test.

| # | Item | Size |
|---|---|---|
| G1 | The design system's production pass: gaps closed, shell chrome components | 16 to 24 |
| G2 | MUST surfaces drawn, phase by phase (about 48) | 96 to 144 |
| G3 | Gable light, the design system item | 16 to 24 |
| G4 | Gable light in the code's tokens, with a contrast test | 8 to 12 |
| | **Design track** | **about 136 to 204** |

### Totals

| | Size |
|---|---|
| MUST items (M0a to M36) | 1,148 to 1,876 |
| Their screens | 240 to 480 |
| The shell track | 200 to 315 |
| The design track | 136 to 204 |
| **The MUST plan** | **about 1,724 to 2,875** |

The SHOULD rows follow in the same way once the MUST rows are built; a first estimate, before they are briefed, is
about 600 to 1,000.

### Rulings this plan waits on

- **Customers across yards (M1).** Whether a customer one yard opened is an account every yard sells to, and how the
  link between a customer and the yards it trades at is kept. M1 and every multi yard walk wait on it.
- **The shells' navigation and the distinct flows.** See [GABLE-DISTINCT.md](GABLE-DISTINCT.md): what the shell
  pattern changes in Gable's own flows.
- **Offline.** This plan puts an offline store for the field shell at SHOULD, keeps the counter online (CP-13 is
  LATER), and lets no money queue.
- **Sign in methods.** Which methods the open source build offers by default and where a dealer chooses them (S9).

