<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Gable's distinct flows

Part 1 lists the flows and design choices that are Gable's own,
from the MVP (the GableLBM monolith) and from the v1 refactor on `refactor/v1`, and says which to keep. Part 2 asks how
the new shell architecture would change each one, so the product owner can rule. Part 3 collects the rulings in one
list.

How to read it:

- **Distinct** means a way of working that differs from a generic ERP, or from how Rundoo, Epicor BisTrack or DMSi
  Agility do the same job. Comparisons cite the [parity matrix](LBM-PARITY-MATRIX.md) rows by id (for example QE-07); the matrix carries the
  sources. A few entries are not distinct in capability but are design choices the new shell must carry; they say so.
- **Origin** is MVP (built in the monolith and carried into the commons), v1 refactor (designed or built on
  `refactor/v1`), or both (MVP behaviour that v1 reworked). Where an MVP feature came from a later MVP line rather
  than its master branch, the entry says so.
- **Status** is built on `refactor/v1`, in an open pull request, designed (an ADR, not built), proposed (design files
  only) or prototype.
- **Verdict** is keep, keep with a change (what and why), or rule (the product owner decides).

Sources: the MVP's master branch and its later lines; the commons at `refactor/v1` 58dde7e8; Gable's design source of
truth (the design files `Gable: design system` and `Gable: screens`, with their cited token notes). Paths are
repository relative; MVP paths are marked "MVP".

## Part 1: the distinct flows

### A. Getting in and getting around

#### D1. The front door

- **What it is:** The first screen at `/`. After sign in it shows app tiles from the apps catalog, filtered by the
  person's roles, with the whole desk pinned first. A designed `/?open=<record>` link signs a person in and opens one
  record.
- **Where it lives:** `web/apps/front-door/src/front-door.ts`, `web/apps/front-door/src/apps-catalog.ts`;
  `tauri/gable-desk/` wraps it; the record link is ADR 0007 section 8 with `core/api/links.json`. MVP antecedent:
  `app/src/pages/LocalTestHub.ts`, a surface picker shown only in dev and demo builds.
- **Origin:** both. The MVP's picker chose between the desk, the portal, the driver and yard apps and the counter;
  v1 (R1-8) made it the product's front door with sign in and role filtered tiles.
- **Status:** built (sign in and tiles); the `open` handling is designed and waits on C5-2b.
- **Distinct because:** Gable opens into a catalog of separately enabled apps, not one menu tree.
- **Verdict:** keep, with a change. The tile audience table (`APP_AUDIENCE`) repeats the server's role guards in the
  browser; the catalog should send each app's roles (the door reads a `roles` field when the catalog sends one, and the core never sends it) so the door cannot drift
  from the guards.

#### D2. The desk home grid

- **What it is:** The desk's home has two tabs: Apps, a launcher grid of every enabled app with an entry point, and
  Dashboard, the KPI view. The chosen tab is remembered, and a link opens app management.
- **Where it lives:** `web/apps/desk/src/pages/Home.ts`, `web/apps/desk/src/apps/launcher.ts`,
  `web/apps/desk/src/pages/Dashboard.ts`.
- **Origin:** MVP, from a later MVP line (not on its master), carried into the commons' first public snapshot; v1
  kept it.
- **Status:** built.
- **Distinct because:** an app launcher stands where an ERP usually puts its module menu.
- **Verdict:** keep for now, with a change. It overlaps the front door: two grids and two maps from app to entry path
  (`launcher.ts` and `DESK_ENTRIES` in `apps-catalog.ts`). One map, generated from the app manifests, should serve
  both.

#### D3. Workspace tabs instead of a sidebar

- **What it is:** Opening an app adds a tab under the header, like browser tabs. Each tab keeps its own place, Home
  is pinned, open tabs survive a reload, and a band below shows the active app's own menu.
- **Where it lives:** `web/apps/desk/src/lib/workspace.ts`, `web/apps/desk/src/components/layout/app-shell.ts`.
- **Origin:** MVP, from a later MVP line that removed the left sidebar the MVP's master still has; v1 kept it.
- **Status:** built.
- **Distinct because:** several modules stay open at once, each with its state, and there is no menu tree at all.
- **Verdict:** rule. This is the flow the new shell changes most (Part 2). The design files disagree with the code:
  every rebuilt board there still draws the MVP's 280 pixel left sidebar, while the v1 desk renders tabs.

#### D4. Omnibar and the keyboard

- **What it is:** Cmd+K opens one search across customers (with a credit hold badge) and products, plus quick
  actions such as a new quote. A question mark opens a sheet of keyboard shortcuts.
- **Where it lives:** `web/apps/desk/src/components/ui/omnibar.ts`, `web/apps/desk/src/components/ui/shortcuts-modal.ts`,
  the header in `web/apps/desk/src/components/layout/app-shell.ts`.
- **Origin:** MVP; v1 kept it.
- **Status:** built, in part.
- **Distinct because:** a keyboard first desk with one search for every record, beside the menus.
- **Verdict:** keep, with three changes. Picking a customer or product result only closes the omnibar: it should
  open the record (the link resolver of D12 gives the path). The sheet promises go to shortcuts (G then D, I or O)
  and Cmd+S to save a quote that nothing implements: wire them or drop them from the sheet. The search field in the
  header has no handler (only Cmd+K searches): make it open the omnibar or remove it.

#### D5. Branch context in the header

- **What it is:** A header switcher sets the yard (branch) every request is scoped to, or All branches for people
  granted several. The server walls every read and act to the person's granted branches, even when no branch is
  selected.
- **Where it lives:** `web/apps/desk/src/components/layout/branch-switcher.ts`,
  `web/apps/desk/src/services/BranchContext.ts`, `core/pkg/branchctx/`, `core/pkg/middleware/branch.go`; the no
  branch rule is pull request 119 (open).
- **Origin:** both. The MVP built the switcher and the context; v1 built the walls.
- **Status:** built; the no branch rule is in an open pull request.
- **Distinct because:** not distinct in capability (the incumbents switch location too, MY-01 and MY-03). Listed
  because the new header must hold it.
- **Verdict:** keep.

#### D6. Installable apps

- **What it is:** Modules are apps in a registry. Each declares a manifest, can be enabled or disabled per
  installation, answers `app_disabled` when off, and feeds the door's tiles, the desk grid, the tabs and their menus.
- **Where it lives:** `core/pkg/apps/`, `web/apps/desk/src/apps/` (the registry and the millwork and governance
  manifests), `web/apps/desk/src/pages/admin/AppsPage.ts`, `docs/modularization-blueprint.md`.
- **Origin:** MVP (phase 0, two apps converted); v1 kept it.
- **Status:** the registry lists every module, but only two (millwork and governance) are converted: only their
  routes are gated and only their screens come from manifests; the rest are mapped by hand.
- **Distinct because:** the incumbents sell modules by licence; Gable switches them per installation in its own
  admin.
- **Verdict:** keep. How registry apps map to the new role micro apps is a ruling (Part 2).

### B. Selling

#### D7. The quote builder

- **What it is:** One screen prices a job: price level, credit limit, balance and available credit with an over
  limit warning; pickup or delivery, with truck and freight chosen on the quote; the lines, the AI upload and a
  running summary.
- **Where it lives:** `web/apps/desk/src/pages/QuoteBuilder.ts`, `web/apps/desk/src/components/quotes/LineItemEditor.ts`,
  `core/internal/quote/`; MVP `app/src/pages/QuoteBuilder.ts`.
- **Origin:** both. The MVP built the screen; v1 made quotes the first module on the wire contract (R1-15: integer
  money, revisions, `Q-` numbers).
- **Status:** built.
- **Distinct because:** the truck is chosen on the quote, and credit is in view while pricing, not only at order
  entry.
- **Verdict:** keep, with two changes. The quote's margin total is sent by the browser and never computed (already
  on the backlog: the server should compute it). The per line escalator in the builder is a what if calculator that
  is not saved with the quote; label it a preview so it is not mistaken for price protection (D9), or save it.

#### D8. AI material list intake

- **What it is:** A rep uploads a photo, PDF or spreadsheet of a material list. AI reads the lines and matches each
  to the catalogue with a confidence, alternatives or a special order flag, beside the original; accepted lines join
  the quote.
- **Where it lives:** `web/apps/desk/src/components/quotes/MaterialListUpload.ts`,
  `web/apps/desk/src/components/quotes/ParsedResultsPanel.ts`, `web/apps/desk/src/services/parsing.service.ts`,
  `core/internal/parsing/`, `core/internal/ai/`; the quote keeps its `source`, `parse_map` and the original file
  (`PUT /api/v1/quotes/{id}/file`); quote analytics split conversion by AI and manual source.
- **Origin:** both. The MVP built the flow; v1 made its fallback honest (a `synthetic` flag when no AI key is set,
  so a canned list never passes as the upload) and kept the file through a draft's promotion.
- **Status:** built.
- **Distinct because:** none of the three reads a free form material list in its own quote screen; plan takeoff is
  partner software for all three (QE-03), and BisTrack's OCR order entry reads customer purchase orders through an
  add on (SO-01).
- **Verdict:** keep, with a change. With no AI key the service returns a canned demo list; in a dealer installation
  it should refuse with a plain message and keep the canned list for the demo seed only.

#### D9. Commodity price protection (quote exposure)

- **What it is:** When a quote is sent, commodity lines snapshot a market index baseline and the customer's policy.
  A move past the threshold flags, escalates or requires the customer's acknowledgement; unresolved exposure blocks
  the order; reps see at risk quotes.
- **Where it lives:** `core/internal/pricing/` (`exposure_*.go`, `snapshot_service.go`, `index_admin_handler.go`),
  the exposure notifier in `core/internal/notification/` (delivered through the outbox),
  `web/apps/desk/src/pages/quotes/Exposure.ts`, `web/apps/desk/src/pages/reports/ExposureReport.ts`,
  `web/apps/desk/src/pages/admin/MarketIndices.ts`, `web/apps/desk/src/components/quotes/acknowledgment-modal.ts` and
  `exposure-banner.ts`; migrations 023 and 081.
- **Origin:** both. A later MVP line (lumber price protection), ported into the commons; v1 moved its notifications
  onto the transactional outbox and its routes onto the contract.
- **Status:** built.
- **Distinct because:** the incumbents pin or lock quoted prices, or schedule escalations (QE-07, PR-07). Gable
  watches open quotes against a market index, applies each customer's policy, and makes the customer acknowledge a
  move (recorded with how it was given) before the order proceeds.
- **Verdict:** keep. It is the most clearly Gable flow in the sales path.

#### D10. Quote to order to invoice, without loss

- **What it is:** Convert accepts the quote and creates the order in one transaction. Lines keep their exact quoted
  price, freight becomes a charge line, a second convert is refused, and each fulfilment invoices exactly the shipped
  share with its cost.
- **Where it lives:** the convert route in `core/internal/quote/`, `core/internal/order/fulfil.go`,
  `core/internal/salesdoc/`; ADR 0005 sections 2, 5.6 and 5.8.
- **Origin:** both. In the MVP, convert returned a payload for the browser to post as an order; v1 made it one
  server act.
- **Status:** built.
- **Distinct because:** not distinct in capability (all three convert quotes). Listed because the guarantee that
  quote, order and every invoice tie to the cent is Gable's, and a shell that splits the counter from the quote desk
  must not split this act.
- **Verdict:** keep.

#### D11. Agents propose, people commit

- **What it is:** An agent and a person co-edit a shared draft quote or order, with revisions and a live change feed.
  Only a person, or a key holding commit, promotes it in one transaction. Every write records the actor and tool.
- **Where it lives:** `core/internal/drafts/`, `core/pkg/confirmgate/`, `core/pkg/actor/`,
  `core/pkg/middleware/machinekey.go`, `core/internal/order/draftkind.go`; ADR 0007 and `docs/modules/drafts.md`.
- **Origin:** v1 refactor (ADR 0007).
- **Status:** server built; the desk's draft screens (`/quotes/drafts`) are designed and not built (C5-2b).
- **Distinct because:** Rundoo's assistant drafts records within the user's permissions (MY-07); Gable enforces at
  the server that an agent can propose and never commit, and lets outside agents work through scoped keys.
- **Verdict:** keep. Build the desk side on the new shell's chrome rather than on today's tabs.

#### D12. Record links and readable numbers

- **What it is:** Numbered records open by their number (`/quotes/Q-000123`). One resolver answers each record's
  links for the desk, the front door, the desktop app (`gable://`) and an optional agent screen, so agents never carry
  a route table.
- **Where it lives:** `core/internal/links/links.go`, `core/api/links.json`, `core/api/link-paths.json` (shared test
  vectors), the deep link rule in `tauri/gable-desk/src/lib.rs`; ADR 0007 sections 7 and 8.
- **Origin:** v1 refactor.
- **Status:** server built; the door's and the desk's handling wait on C5-2b.
- **Distinct because:** a public link contract instead of screen paths known only to the product.
- **Verdict:** keep. With three shells the desktop link needs a change (Part 2).

#### D13. Product configurator

- **What it is:** A stepped builder for non stock items: product type, species, grade and treatment, dimensions,
  review. Dependency rules grey out impossible choices with the reason, validate the set, and build a non stock SKU
  with its description.
- **Where it lives:** `web/apps/desk/src/pages/millwork/ProductConfigurator.ts`, `core/internal/configurator/`,
  `docs/modules/configurator.md`; behind the millwork app.
- **Origin:** both. The MVP built it; v1 put its routes on the contract (C5-1b).
- **Status:** built, except the hand off.
- **Distinct because:** BisTrack and Agility configure doors and millwork with rules (QE-06); Gable's rules also
  cover commodity lumber attributes. Rundoo has no configurator.
- **Verdict:** keep, with a change. The built SKU goes nowhere: it should become a non stock line on a quote or an
  order. The page also uses literal colour values instead of the token classes.

#### D14. Door configurator

- **What it is:** A millwork estimator: tiles for door style, material and glass, a width and height, a live drawing
  scaled to the entered size, and a running estimate built from each option's price adjustment.
- **Where it lives:** `web/apps/desk/src/pages/millwork/DoorConfigurator.ts`, `core/internal/millwork/`,
  `docs/modules/millwork.md`.
- **Origin:** both. The MVP built it; v1 kept it and disabled its Add to Order button honestly (a configured door is
  not a catalogue product yet).
- **Status:** built as an estimator only.
- **Distinct because:** the live, scaled drawing while configuring.
- **Verdict:** change: wire it to a quote line (a non stock line carrying the configuration as attributes), or name
  it an estimator. Keep the drawing. Move its colours onto the tokens.

#### D15. Blueprint verifier

- **What it is:** Paste or drop blueprint specifications; the service extracts dimensions, lengths, spacing, species
  and treatment, compares them with the configurator's selections, and flags errors and warnings. The page is marked
  an AI prototype.
- **Where it lives:** `web/apps/desk/src/pages/millwork/BlueprintVerifier.ts`, `core/internal/vision/`.
- **Origin:** MVP; v1 kept it unchanged.
- **Status:** prototype. The extraction is pattern matching on pasted text, not vision, and a dropped file is not
  read.
- **Distinct because:** checking plans against a configured item is in no parity row; it is Gable's own idea.
- **Verdict:** rule: grow it into real vision intake (the AI client already reads images for D8) or retire it. No
  parity row needs it.

### C. Pricing

#### D16. Pricing matrix with a resolution preview

- **What it is:** A grid of product categories by customer tier. Cells inherit from parent categories until
  overridden in a drawer; bulk mode edits many cells; a tab holds account rules; a preview shows which rule wins for
  a product and customer.
- **Where it lives:** `web/apps/desk/src/pages/admin/pricing/` (`PricingMatrix.ts`, `MatrixGrid.ts`, `CategoryTree.ts`,
  `RuleDrawer.ts`, `AccountRulesTable.ts`, `ResolutionPreview.ts`), `core/internal/pricing/category_*.go`.
- **Origin:** both. The MVP built it; v1 kept it with prices at exact scale 4 (C3-1).
- **Status:** built.
- **Distinct because:** matrices are parity (PR-01); the inheritance view and the "which rule wins" preview are
  Gable's.
- **Verdict:** keep. It is a desktop surface.

#### D17. Price intelligence (proposed)

- **What it is:** A proposed page that compares the dealer's prices on its own catalogue with a big box competitor's
  listed prices, normalised to the dealer's unit, with a match confidence and an above, in line or below state per
  item.
- **Where it lives:** the design files only (`Gable: screens`, the proposed pricing intelligence page and its demo
  page boards). No code.
- **Origin:** neither: a proposal in the design source of truth. The comparison it shows runs in an automation
  outside Gable.
- **Status:** proposed.
- **Distinct because:** a competitor price comparison is in no parity row; it is Gable's own idea.
- **Verdict:** rule: build, defer or drop.

### D. The counter

#### D18. The counter till

- **What it is:** Full screen counter, no app chrome: scan or search, split tenders, a till opened with a float and
  closed with an over or short count and Z report, and returns. The MVP queued cash and check sales offline.
- **Where it lives:** `web/apps/desk/src/pages/pos/POSTerminal.ts`, `web/apps/desk/src/pages/DailyTill.ts`,
  `core/internal/pos/` (till, Z report, returns, offline sync); the till and money rebuild is pull request 72
  (open). MVP: `app/src/lib/offlineStore.ts`, `app/src/components/pos/OfflineBanner.ts`.
- **Origin:** both. The MVP built the terminal and its offline queue; the commons removed the browser queue as dead
  code; v1 rebuilds the till and its money on the contract.
- **Status:** built, with the money rebuild in an open pull request.
- **Distinct because:** the counter is parity (the CP rows). Two choices are Gable's: a counter with no app chrome,
  and the MVP's offline sale queue, which none of the three documents (CP-13).
- **Verdict:** keep the focused counter, with three changes. The server still replays offline sales
  (`POST /api/v1/pos/sync`, tagged `offline-v1`) for a browser queue that no longer exists; the terminal hard codes
  register `REG-01`; and its screen is styled with literal colours outside Gable's tokens.

### E. Delivery and the yard

#### D19. The dispatch board

- **What it is:** Three panes: the day's routes, the chosen route's stops, and a map. Stops are reordered by hand
  or optimised through an open routing service for heavy vehicles (a deterministic mock without a key); overweight
  loads warn.
- **Where it lives:** `web/apps/desk/src/pages/DispatchBoard.ts`, `web/apps/desk/src/components/logistics/`,
  `core/internal/delivery/`, `docs/modules/delivery.md`.
- **Origin:** both. The MVP built the board; v1 put delivery on the contract (C5-1d).
- **Status:** built.
- **Distinct because:** open routing in place of a commercial routing partner (DD-04), switched between mock and
  real at run time without a redeploy.
- **Verdict:** keep.

#### D20. AI load management (AI_LM)

- **What it is:** A separate planning service reads fleet, drivers, yards, a day's confirmed orders and the catalogue
  with weights and 3D geometry, solves packing and routes from each load's yard, and writes approved routes and a
  packing manifest back.
- **Where it lives:** `core/internal/integrations/ailm.go` and `ailm_store_pg.go` (the `/api/integration/*` seam),
  `core/internal/staff/` (per person grants and the `ai_lm` kill switch),
  `web/apps/desk/src/pages/inventory/tabs/ProductGeometryTab.ts`, the `packing_manifest` column on orders.
- **Origin:** the commons before v1 (the seam); v1 froze its wire and pinned it with golden tests.
- **Status:** built (the seam); the planner is its own service.
- **Distinct because:** BisTrack and Agility plan loads by weight and volume (DD-02); Gable plans by 3D geometry,
  roots each route at its shipping yard, and lets an operator switch the AI off for everyone at once.
- **Verdict:** keep. The truck loading view lives in the planner, not in Gable.

#### D21. Delivered means billed

- **What it is:** Completing a delivery queues a fulfilment request in the same transaction; a worker invoices it
  with its cost of goods. A failure retries, parks after repeated failures, and stays listed for retry, so no
  delivered load goes unbilled.
- **Where it lives:** `core/internal/order/fulfil_queue.go` and `queue.go`, `core/internal/app/worker/`,
  `GET /api/v1/orders/fulfillment-requests` and its retry route; ADR 0005 section 5.5.
- **Origin:** v1 refactor (the MVP invoiced inline and only logged a failure).
- **Status:** built on the server; no desk screen lists the parked requests yet.
- **Distinct because:** BisTrack and Agility invoice on delivery too (DD-05); the never lost queue with a visible
  parked list is Gable's design.
- **Verdict:** keep, with a change: add the parked request screen.

#### D22. The field surfaces (driver and yard)

- **What it is:** Driver and yard screens are phone columns with bottom tabs and their own wordmarks (the yard's in
  amber). Drivers capture signatures, typed photos, on site quantity changes and partial or failed stops; the yard
  picks, receives, counts and scans.
- **Where it lives:** `web/apps/desk/src/components/layout/driver-layout.ts` and `yard-layout.ts`,
  `web/apps/desk/src/pages/driver/`, `web/apps/desk/src/pages/yard/`, `web/apps/desk/src/components/BarcodeScanner.ts`,
  the proof of delivery routes in `core/internal/delivery/`.
- **Origin:** both. The MVP built the surfaces; v1 put proof of delivery on the contract and typed its photos
  (signature, site, damage).
- **Status:** built; they ride the desk's session and have no sign in of their own.
- **Distinct because:** the capture is parity (DD-05, MY-04); a separate phone identity per surface inside one
  product is Gable's.
- **Verdict:** keep the capture flows. The accent per surface is a ruling under the field shell (Part 2).

### F. Customers and the catalogue

#### D23. The contractor portal

- **What it is:** A portal in the dealer's logo and colour, with its own sign in: dashboard, projects grouping
  orders, reorder, invoices, deliveries, the catalogue at the account's prices, a cart with checkout on account, and
  team invites with roles.
- **Where it lives:** `web/apps/desk/src/pages/portal/`, `web/apps/desk/src/pages/projects/`,
  `web/apps/desk/src/components/layout/portal-layout.ts`, `core/internal/portal/`, `core/internal/project/`. Built in
  the API with no screen yet: quote requests, accept and decline (`/api/portal/v1/quotes`) and delivery reschedule
  requests (`/api/portal/v1/deliveries/{id}/reschedule`).
- **Origin:** both. The MVP built the portal; the commons made its API a headless core; v1 has not converted it yet
  (the C5-1 portal item).
- **Status:** built; not yet on the contract.
- **Distinct because:** portals are parity (the PE rows). Gable's reschedule is a request the dispatcher decides,
  because a route's date is shared by every stop on that truck; projects group a contractor's orders.
- **Verdict:** keep the flows. Its frame and sign in are rulings (Part 2).

#### D24. AI product content

- **What it is:** The product page drafts its own marketing content with AI: long descriptions, search titles and
  keywords, sell sheets, social posts, and product images generated in the background and stored as product media.
- **Where it lives:** `core/internal/pim/`, the Content, SEO, Media and Collateral tabs in
  `web/apps/desk/src/pages/inventory/tabs/`, `core/internal/ai/openrouter.go`.
- **Origin:** MVP; v1 kept it (C3-1).
- **Status:** built.
- **Distinct because:** Rundoo supplies content from its own product database; Gable drafts content per item for the
  dealer's own catalogue.
- **Verdict:** keep.

### G. Platform choices

#### D25. AI that never blocks

- **What it is:** Every AI feature reads its key at run time from the admin screen and degrades without one:
  parsing marks canned output synthetic, routing falls back to a mock, and an operator can switch the load planner
  off for everyone.
- **Where it lives:** `core/internal/ai/keystore.go`, `core/internal/techadmin/`, `core/internal/parsing/service.go`,
  `core/internal/delivery/ors.go`, `core/internal/staff/`; the rule in `AGENTS.md`.
- **Origin:** both. The MVP set the rule (degrade, never fail); v1 made the degraded results honest.
- **Status:** built.
- **Distinct because:** AI is optional in every flow, never a dependency.
- **Verdict:** keep.

#### D26. Governance inside the product

- **What it is:** An optional app where contributors draft change proposals with AI help from a title, a problem and
  a proposed solution, then move them through review to approved or rejected: part of running Gable as a community
  commons.
- **Where it lives:** `core/internal/governance/`, `web/apps/desk/src/pages/governance/`,
  `web/apps/desk/src/apps/governance.ts`.
- **Origin:** MVP; v1 kept it as an installable app (C5-1a).
- **Status:** built.
- **Distinct because:** the product carries its own community change process, which follows from Gable being an
  open source commons.
- **Verdict:** rule: keep it in dealer installations (switched off by default), or move it to the commons' own site.
  It is not an LBM operation flow.

#### D27. Machine and partner doors

- **What it is:** Outside software works through scoped machine keys (read, write, propose, commit per module, bound
  to a branch when needed), a read only co-op partner API, signed agent to agent purchase orders, and the load
  planner's integration seam.
- **Where it lives:** `core/pkg/middleware/machinekey.go`, `core/internal/partner/`,
  `core/internal/purchase_order/a2a_receiver.go`, `core/internal/integrations/`; ADR 0002 and ADR 0007 section 5.
- **Origin:** both. The MVP built the partner, agent and integration surfaces; v1 built the scoped keys.
- **Status:** built.
- **Distinct because:** an open, scoped surface for agents and co-ops rather than a closed integration list.
- **Verdict:** keep. The shell does not touch it.

### H. Look and feel

#### D28. Industrial Dark

- **What it is:** A dark only theme: Deep Space `#0A0B10` behind, Slate Steel `#161821` and two raised surfaces,
  Gable Green `#00FFA3` for action, success and focus glow, Safety Red `#F43F5E` for errors and credit hold, Blueprint
  Blue `#38BDF8` for technical data.
- **Where it lives:** `web/packages/design-system/src/tokens.css`, `web/packages/design-system/tailwind/theme.js`,
  `docs/design-system.md`; the Colors page of `Gable: design system`.
- **Origin:** both. The MVP set the theme; v1 moved it into one shared package that the door and the desk import.
- **Status:** built.
- **Distinct because:** it is Gable's visual identity, which the parity goal keeps.
- **Verdict:** keep, with changes. No light variant exists (the design files list it as a gap). The POS terminal, the
  configurators, the blueprint verifier and the phone layouts bypass the tokens with literal colours; move them onto
  the tokens.

#### D29. Two typefaces, one rule for numbers

- **What it is:** Outfit for labels, headings and body; JetBrains Mono for every number and identifier (SKUs, prices,
  quantities, dimensions, document numbers) so columns align for scanning. The wordmark sets "Gable" bold and "LBM"
  light in Gable Green.
- **Where it lives:** `web/packages/design-system/tailwind/theme.js`, the text utilities in `tokens.css`
  (`font-data`, `text-display-large`), the rule in `AGENTS.md`; the Typography page of `Gable: design system`.
- **Origin:** both. The MVP's own design note named Inter while its code used Outfit; v1 corrected the note.
- **Status:** built.
- **Distinct because:** the numeric font rule is a deliberate data density choice.
- **Verdict:** keep, with a change: the brand files' lockup declares Inter while the product renders Outfit; choose
  one for the lockup.

#### D30. Icons and marks

- **What it is:** Lucide line icons through one helper; a mark of a roof over a lightbulb with a soft green glow;
  wordmarks per surface for the yard and driver screens. Brand files sit under a trademark policy, apart from the
  code.
- **Where it lives:** `web/packages/design-system/src/icons.ts` and `brand-logo.ts`, `tauri/gable-desk/icons/`; the
  brand repository's `assets/` and `USAGE.md`.
- **Origin:** both.
- **Status:** built.
- **Distinct because:** Gable's own marks.
- **Verdict:** keep. The brand files name gaps the new shells will need filled: a light background lockup, a correct
  PNG with transparency, and app icons at standard sizes.

#### D31. Density, motion and voice

- **What it is:** Condensed tables with sticky headers and mono figures; skeleton loaders, not spinners, for layouts;
  a small lift on hover and a green glow on focus; plain, warm words: "The Pile" for inventory, a time of day
  greeting.
- **Where it lives:** sections 3 and 4 of `docs/design-system.md`, the `hover-lift` and `glass-card` utilities in
  `tokens.css`, `web/apps/desk/src/pages/Inventory.ts`, `web/apps/desk/src/pages/Dashboard.ts`.
- **Origin:** MVP; v1 kept it.
- **Status:** built, unevenly: the front door's loading state uses a spinner against the rule.
- **Distinct because:** a stated house style for a data heavy tool.
- **Verdict:** keep.

## Part 2: the shell question

### The pattern, as it would apply to Gable

The new architecture puts role micro apps into desktop and mobile shells per audience, as already proven in an
internal FutureBuild codebase:

1. **Three shells:** a work shell for office and counter staff, a field shell for the yard and drivers, a pro shell
   for contractors.
2. **One header** holding a centred app and surface selector.
3. **A pinned side rail**, hidden by default, with hover reveal and pin modes. On phones, no rail and native mobile
   patterns.
4. **A front door with a role picker.**
5. **An offline store** with an outbox, sync and conflict cards.
6. **Money never queues offline.**
7. **Sign in methods chosen per dealer.**

What Gable has against each today, on `refactor/v1`:

| Pattern element | Gable today | Gap |
|---|---|---|
| Shells per audience | One Tauri shell, Gable Desk (a Linux package only, no native permission, `gable://` links) around the front door; one desk bundle serves the desk, the portal, the driver and yard screens and the counter | Two more shells, and mobile builds; the role micro apps (C6-1) and their shells (C6-2) are planned, not started |
| Header selector | Desk: brand left, a search field centred (it has no handler), branch switcher and avatar right. Door: brand left, a status word centred, the user right | The centre slot becomes the selector |
| Hidden rail | None in the desk (tabs and a menu band instead); the portal keeps an always visible sidebar; the design files draw the MVP's sidebar | A rail, its pin modes, and boards for it |
| Phones | Driver and yard are phone columns with bottom tabs; the desk only squeezes onto a phone (tables overflow) | Native patterns for every phone surface |
| Front door with role picker | Tiles filtered by the person's roles; no picker | The picker step |
| Offline store | None: a v1 planning decision deferred offline until after the cutover. An amber "you are offline" banner only. The server's transactional outbox carries server events, a different thing | All of it |
| Money never queues | The desk no longer queues sales, but the server still accepts replayed offline sales (`POST /api/v1/pos/sync`) | Retire or fence the replay |
| Sign in per dealer | Staff paste a bearer token (or name themselves in a dev build); contractors use email and password; an HttpOnly cookie through the FutureBuild bridge is planned at the cutover (ADR 0004) | A method choice per dealer |

A placement of Gable's apps in the three shells, for the product owner to confirm or change:

| Shell | Apps (from today's modules) |
|---|---|
| Work (desktop and tablet) | Counter (till, daily till); Quotes (builder, AI intake, configurators, exposure); Orders; Dispatch (board, fleet); Buying and stock (purchasing, vendors, inventory); Money (invoices, credit memos, payments and AR, AP, GL); Pricing (matrix, market indices, rebates); Manager home (dashboard, reports, exposure portfolio); Admin (tech admin, apps, branches, staff, governance) |
| Field (phone first) | Yard (pick, receive, count, look up); Drive (route, stops, proof of delivery) |
| Pro (contractors) | The portal: dashboard, projects, quotes, orders, invoices, deliveries and reschedule requests, catalogue and cart, team |

### Flow by flow

| Flow | Changed? | How the pattern changes it | What the product owner rules |
|---|---|---|---|
| D1 Front door | Yes | A role picker follows sign in and routes to the shell serving that role; today's tiles become the picker's second step or the work shell's home; `/?open=` must choose the shell | Picker before the tiles, or the tiles as the picker; whether owners keep a whole desk entry; what the door shows in a browser with no shell installed |
| D2 Desk home grid | Yes | The header's app selector replaces the grid as the way to switch apps | Keep the grid as the work shell's landing page (its Dashboard tab becoming Manager home) or drop it |
| D3 Workspace tabs | Yes, most | One selected app with its surfaces in a hidden rail replaces several apps open in tabs; the menu band's items become the rail's surfaces | Adopt the pattern and drop tabs; or keep tabs in the work shell under the header with the rail holding the active app's surfaces; or tabs on wide screens only. Then redraw the design files, which show neither |
| D4 Omnibar | Partly | The centre slot goes to the selector, so the dead search field leaves; Cmd+K stays as the shell wide search | Keep Cmd+K across every work shell app; whether a search button sits in the header |
| D5 Branch context | Partly | The header must hold the yard beside the organisation and the user; field devices usually work one yard | Where the switcher sits; whether All branches stays in the work shell; whether a field device is bound to one yard |
| D6 Installable apps | Partly | Two meanings of "app" meet: registry apps (enabled per installation) and role micro apps (what a role opens) | A micro app shows only when its registry apps are enabled, and the selector lists only enabled apps the role admits, read from the catalog with the roles the server sends |
| D7 Quote builder | Partly | It becomes the Quotes app's main surface; tablets need a layout (today a two column desktop page) | Whether quotes may be edited offline at all; sending, converting and the credit view stay online |
| D8 AI material list intake | Little | Parsing needs the network. The field and pro shells' cameras could capture a list to parse later | Whether contractors get list upload as a quote request in the pro shell; whether a captured photo may wait offline for parsing (it is not money) |
| D9 Price protection | Partly | Acknowledging a price move is a price commitment, so online only. The pro shell is the natural place for a contractor to acknowledge (today staff record a "portal" acknowledgement, but contractors have no route or screen to give one) | Confirm acknowledgement never queues; whether the pro shell shows acknowledgement requests |
| D10 Quote to order | Partly | The pattern separates the counter from the quote desk, sharing one quote to order view; convert and confirm (credit, exposure, allocation) are money acts, online only | Where the shared quote to order view lives; nothing else |
| D11 Agents propose, people commit | Partly, and it helps | Drafts give the offline store a server partner: capture offline as a draft (never money), commit online by promotion; a stale revision (409) becomes a conflict card, and the draft feed names who made the newer revision | Adopt "offline writes are drafts, commits are online" as Gable's rule; name the browser outbox so it is not confused with the server's events outbox |
| D12 Record links | Yes | `gable://` opens the one desk shell today; with three shells a link must reach the shell that serves the record for that person | One scheme with the door choosing the shell, or one scheme per shell (the resolver's `app` link then names it) |
| D13, D14 Configurators | Little | They become surfaces of the Quotes app, or of a Millwork app | Which app owns them; build the hand off into quote lines first |
| D15 Blueprint verifier | No | None | Its future only (D15) |
| D16 Pricing matrix | Little | A desktop surface of the Pricing app, not offered on phones | None |
| D17 Price intelligence | Little | It would be a Pricing or Manager home surface | Build or not |
| D18 Counter till | Yes | No sale, tender, refund or deposit completes offline: the till shows its offline state and stops tendering, and a cached catalogue may stay for price lookup only. The full screen counter must choose between the shell's header and a focused mode | Retire the offline sale replay (CP-13 then reads "refused by rule"; none of the three documents an offline counter either); full screen or shell chrome at the till; the register chosen per device instead of `REG-01` |
| D19 Dispatch board | Little | A work shell app on desktop and tablet; a yard manager who dispatches and picks installs both shells | Whether dispatch also appears in the field shell for yard managers |
| D20 AI load management | Little | The packing manifest already stored on orders could drive a loading view in the field shell | Whether the yard loads trucks from Gable's field shell or from the planner |
| D21 Delivered means billed | Partly | Proof of delivery captured offline syncs later; on sync the server marks the stop delivered and queues billing. That is not money queuing on the device. Collecting money at the door would stay online | Confirm offline proof of delivery with billing on sync; put the parked request screen in the work shell |
| D22 Field surfaces | Yes | They become the field shell's Yard and Drive apps: phone first, camera and location permissions, the offline store with conflict cards for picks, counts and receipts | Which yard acts may queue offline (a pick or a count is not money; a receipt posts inventory value to the ledger, so is it?); one accent for the field shell or the yard's amber kept; the field shell's own sign in |
| D23 Contractor portal | Yes | It becomes the pro shell: its sidebar becomes the hidden rail on desktop and native tabs on phones; checkout and any payment stay online; its API only flows (quote request, accept and decline, reschedule requests) get their first screens | The frame: Gable's tokens with the dealer's logo inside, or the dealer's colour across it (today the dealer's colour replaces Gable Green); contractor sign in methods; whether a cart may be built offline |
| D24 AI product content | No | None | None |
| D25 AI never blocks | No | None | None |
| D26 Governance | Little | An admin surface in the work shell, if it stays | As D26 |
| D27 Machine and partner doors | No | None | None |
| D28 to D31 Look and feel | Yes, by addition | The pattern brings chrome Gable has never drawn: the header selector, the rail and its pin states, sync state, conflict cards, offline states, the role picker, three app icons. Each needs a Gable board, in Industrial Dark, before it is built | The design files become the reference for the new chrome (replacing the MVP sidebar boards); dark only, or a light or high contrast mode for field phones in daylight; one accent per shell or per app |

Sign in methods per dealer cut across every shell: today staff paste a bearer token or name themselves in a dev
build, contractors use email and password, the driver and yard screens ride the desk's session, and a cookie session
is planned at the cutover. The product owner rules which methods the open source build offers by default (a self
hosting dealer needs at least a standard OpenID Connect option and an email link) and where a dealer chooses them
(Tech Admin is the natural place).

## Part 3: the rulings, in one list

In the order they block the build:

1. **Navigation (D3, D2, D4).** Workspace tabs against the selector and hidden rail: adopt the pattern, keep tabs
   inside the work shell, or tabs on wide screens only.
2. **Money never queues (D18, D11, D21, D22).** Retire the offline sale replay; adopt "offline writes are drafts,
   commits are online"; allow offline proof of delivery with billing on sync; decide whether a yard receipt counts
   as money.
3. **The front door and links (D1, D12).** Role picker before or as the tiles; whether owners keep a whole desk
   entry; one `gable://` scheme or one per shell.
4. **Placement (D6, D19, D20).** Confirm the three shell placement table; the yard manager who needs both work and
   field; whether loading moves into the field shell.
5. **The pro shell (D23, sign in).** Gable's frame or the dealer's colour; contractor sign in methods; the default
   sign in methods for every shell.
6. **The design files (D28 to D31).** Boards for the new chrome; dark only or a daylight mode for field phones; one
   accent per shell or per app.
7. **Single flows (D7, D8, D9, D13 to D17, D26).** Offline quote editing; list upload for contractors;
   acknowledgement in the pro shell; configurator hand off; the blueprint verifier's future; price intelligence;
   where governance lives.

## What this reading could not do

- It did not run the app or any shell. Screens are judged from the code and from the design files' renders of
  `refactor/v1`.
- It did not read the open pull requests' branches (72, the till and money; 119, the no branch rule). The counter
  entry describes `refactor/v1` and the MVP.
- The desk side of ADR 0007 (C5-2b) is not built, so the draft and link screens are judged from the ADR.
- Comparisons with the three incumbents lean on the parity research rows; the matrix holds their sources.
- It viewed only some boards of the design files: the proposed pricing intelligence page, its demo page board, two
  real renders of the v1 desk and the token notes.
