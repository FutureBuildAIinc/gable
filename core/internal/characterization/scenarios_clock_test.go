// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Clock-window group: endpoints whose answers are windows over the clock -
// aging buckets, period totals, statements. The seed dates every row
// relative to its own clock, so with calendar dates normalised to their
// offset from the seed day these windows are as deterministic as the rest
// of the script: an invoice seeded 45 days back is 45 days back on every
// run, whatever the calendar says. The group runs last, after every other
// group's writes, so the windows aggregate the script's final state.
//
// The demo seed lands every payment and invoice it writes on the seed day,
// so the harness seeds its own fixture rows at day offsets on either side
// of each window edge after the seed binary runs (see
// fixtures_clock_test.go): without them a shrunk or widened window
// answered identically, because nothing sat near an edge.
//
// Deliberate choices:
//
//   - The profit-and-loss statement is called with an explicit relative
//     window (start 30 days back, end on the seed day). Its DEFAULT start is
//     the first of the current month - a calendar boundary, not a clock
//     offset - so a golden of the default window would differ between a run
//     on the 5th and one on the 25th of the same month. The explicit window
//     exercises the same code path with a stable one; the default resolution
//     itself stays pinned by this note rather than by a golden.
//   - The balance sheet, trial balance, AP aging, sales summary, dashboard
//     summary and revenue trend all default to now-relative windows (30-day
//     buckets, today/yesterday, last 7 days), so their defaults are recorded
//     as-is. AP aging orders by total with no tiebreak, so its (complete,
//     unpaginated) row set is sorted before comparison; membership is every
//     row, so nothing is hidden.
//   - AR aging, top customers and order activity return per-customer rows,
//     and the demo seed assigns its drawn invoices and orders to customers
//     inside map-iteration loops, so which customer owns which drawn amount
//     is random per run. The draw sequence itself is fixed
//     (randautoseed=0), so the amounts, buckets, counts, dates and the
//     sorted row order are stable; only the names on the rows vary. Those
//     steps mask the customer identity fields to "<customer>" and pin
//     everything else, including row order. The masks are recorded per step
//     in the golden (mask_customer_identity), like sort_primary_array. Top
//     customers also masks order_count: the script itself writes orders for
//     one seeded customer (the quote and integration converts), so the
//     count on a rank owned by that customer carries the script's own
//     orders on top of the randomly assigned segment's count - the revenue
//     sequence and row order stay pinned, that one count does not.

func clockGroups() []groupDef {
	return []groupDef{{
		name: "clockwindow",
		steps: []stepDef{
			{name: "ap.aging", method: "GET", path: "/api/v1/ap/aging", sortPrimaryArray: true, sortEnvelopeItems: true},
			{name: "reports.ar_aging", method: "GET", path: "/api/v1/reports/ar-aging", maskCustomerIdentity: true},
			{name: "reports.sales_summary", method: "GET", path: "/api/v1/reports/sales-summary"},
			{name: "dashboard.summary", method: "GET", path: "/api/v1/dashboard/summary"},
			{name: "dashboard.revenue_trend", method: "GET", path: "/api/v1/dashboard/revenue-trend"},
			{name: "dashboard.top_customers", method: "GET", path: "/api/v1/dashboard/top-customers", maskCustomerIdentity: true, maskOrderCount: true},
			{name: "dashboard.order_activity", method: "GET", path: "/api/v1/dashboard/order-activity", maskCustomerIdentity: true},
			{name: "gl.trial_balance", method: "GET", path: "/api/v1/gl/trial-balance"},
			{name: "gl.profit_and_loss", method: "GET", path: "/api/v1/gl/profit-and-loss?start={today-30}&end={today}"},
			{name: "gl.balance_sheet", method: "GET", path: "/api/v1/gl/balance-sheet"},
		},
	}}
}
