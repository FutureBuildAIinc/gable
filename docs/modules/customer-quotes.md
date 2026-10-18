<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Customer quotes (the contractor's quote and the selections loop)

A contractor is the dealer's customer. They quote their own customer, a
homeowner or an owner, from the dealer's products with a markup of their
own, send it on a link that needs no account, take the customer's
approval, and turn the approved material into an order with the dealer
at the dealer's prices. This page is the guided tour of that loop and of
the two walls that keep the money apart. The contract is
`core/api/fragments/customerquote.yaml`; the code is
`core/internal/customerquote`; the schema is migration 131.

## The three route families

| Family | Prefix | Who calls it | Authority |
|---|---|---|---|
| Contractor | `/api/portal/v1/customer-quotes` | The contractor's portal session | The portal chain; every read and write is scoped to the contractor's account, another account's quote is a 404 |
| Customer | `/api/public/v1/customer-quotes/{token}` | Anyone holding the link | The token in the path, for one quote; no session, no cookie |
| Dealer | `/api/v1/pro-projects` | The dealer's desk (`admin`, `owner`, `sales`) | The desk role guard |

## The quote

- Material lines are the dealer's products, priced by the server for the
  contractor's account through one seam (`CostSource`, answered today by
  the portal's existing price lookup). The cost is captured on the line,
  as ten thousandths per unit and as the extended cost in cents.
- The markup is three tiers in basis points, the most specific winning:
  the line's own override, then the quote's rule for the line's product
  category, then the quote default. A tier is either a markup on cost or
  a target gross margin; a margin of 100 percent or more is refused.
- Labour lines are flat or hourly, overhead lines flat or a percentage of
  the customer facing materials plus labour. A line flagged
  `contractor_only` is the contractor's own cost: it counts against the
  margin and never enters the customer's price or any customer or dealer
  surface.
- `hide_line_prices` shows the customer a lump sum. The contractor's
  payment terms, deposit percent and tax apply: the customer site rate on
  the materials, "tax included", or "material tax at checkout".
- `valid_until` is never later than the dealer holds the price: the
  source dealer quote's expiry, and for catalog priced lines the
  configured price protection horizon.
- Rounding is written once, in `money.go`: a figure that is a count times
  a ratio is computed exactly and rounded once to the cent, half away from
  zero; totals are sums of rounded lines.

## The loop

1. The contractor builds a draft (`POST`, then `PUT` with `If-Match`),
   previews the customer's page and PDF, and sends it. Sending needs a
   project, a job site address and an email or phone for the customer.
2. Send freezes the draft and issues the link: 256 random bits from the
   operating system, shown once in the send response, only the SHA-256
   hash stored. The link expires at `valid_until`, bounded by the
   configured maximum. The contractor can rotate (the old link dies) or
   revoke it, and can revise a sent or declined quote back to a draft.
3. The customer opens the page, approves or declines each selection (in
   an option group, choosing one declines the others) and signs off with a
   typed full name, an email or phone, and a typed or drawn signature. If
   the contractor requires a code, a six digit code goes to the contact on
   the quote and must be entered with the sign off.
4. The server records the time, the source address and the user agent and
   an immutable SHA-256 snapshot of the chosen selections and total, in
   one transaction with the status change. Two acceptances at once give
   exactly one approval.
5. The contractor confirms: the approved material lines become a draft
   order through the portal's order path, on the quote's project, at the
   account's own prices, in one transaction with the confirmation.
6. The dealer sees the project in its pipeline and records its acceptance.

The status flows back: the customer's page shows the order's fulfilment
state only, the contractor's quote shows the order, the dealer's project
shows the order. The approval record keeps every step with who and when,
and the database refuses to update, delete or truncate it.

## Decisions

Plain words for the choices a reader should see before merge.

- **A `contractor_only` line is the contractor's own cost.** It counts against
  the margin the contractor sees. It never enters a price, a total, the
  customer's page or PDF, the snapshot, or anything the dealer reads.
- **The markup applies to materials only.** Labour and overhead lines are
  priced as the contractor enters them, with no markup on top. A percentage
  overhead is a percentage of the customer-facing materials plus labour.
- **The site tax rate applies to materials only.** Labour and overhead carry
  no tax in the total. The other two tax treatments ("tax included" and
  "material tax at checkout") add no tax line.
- **The deposit is a percentage of the total including tax.**
- **A verification code never goes to a sender that writes message bodies to
  a log, and never to the text stub.** While the server's only email sender is
  the log-only one and text is the stub, the contractor's "require a code"
  toggle refuses to turn on ("Verification codes need a real email or text
  sender on this server."), a code request answers the same sentence, and a
  quote whose toggle was already on cannot be accepted until the contractor
  turns the toggle off; the customer's page says so in plain words. Declining
  stays possible.
- **The dealer's acceptance records the desk user's id and display name.**
  Contractors and customers see the dealer's business name and that display
  name, never a staff email address.
- **Roles.** Today's portal roles map as: `Admin` and `Buyer` may quote and see
  money, `View-Only` may do neither. The portal roles of the pro portal branch
  replace this one rule at merge.

## The two price walls

- **Price wall 2, contractor to customer.** The customer's page and PDF
  are built from `PublicView`, a strict allowlist type with no field for
  the dealer's cost, a markup, basis points (the customer site tax rate
  excepted), a margin or a contractor only line. The wire test decodes
  every public route into a generic map, fails on any key outside the
  allowlist, and searches the raw bytes (the PDF included) for every cost
  and markup figure of the fixture.
- **Price wall 1, contractor to dealer.** The dealer reads a quote only
  as `DealerProject`: the project, the job site, the customer and site
  contact, the selection status and the material pipeline at the dealer's
  own prices, plus the approval record with who, when and the hash of the
  decision. A draft is the contractor's own work and is not shared. The
  same test method covers every dealer route and the converted order.

## The dealer's own quote link

The same token module carries the dealer's quote to a contractor. A desk user
issues a link for a sent, unexpired quote with `POST
/api/v1/quotes/{id}/share-link` (the quote module's role and branch wall; a
branch bound machine key is refused), optionally emailing it. The contractor
opens `/api/public/v1/dealer-quotes/{token}` with no login, sees the lines at
their own account prices (never the dealer's cost or margin), and accepts or
declines with a typed full name. The decision goes through the quote module's
own lifecycle, once, and the audit log records who, from which address and
user agent. A link opens one kind of target: a dealer quote link never opens a
customer quote, and the converse.

## Settings and operations

| Setting (`system_settings`) | Default | Meaning |
|---|---|---|
| `customer_quote.link_max_days` | 60 | The longest a link may live |
| `customer_quote.price_protection_days` | 30 | The dealer's price protection horizon for catalog priced lines |
| `customer_quote.default_valid_days` | 14 | The validity a quote gets when it names none |

- The link email goes through the deployment's email sender. A log only
  sender would write the link into the server log, and a link is a
  credential, so nothing is sent through it and the send response says
  `delivered: false`; the contractor shares the link they were given. Set
  `SMTP_HOST` for real delivery (see `notification.md`).
- Verification codes by email use the same sender. SMS has no live carrier
  at this base: the SMS sender is a stub.
- The request log, the error log, the panic log and the metrics labels
  record a capability token's path segment as `:token`.
- The public routes are rate limited per link and per source address, and
  an address that keeps presenting unknown tokens is limited sooner.
- The contractor's one click export is `GET
  /api/portal/v1/customer-quotes/export` (JSON of everything, or one CSV
  table by `kind`).
