// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Clock-window group: endpoints whose answers are windows over the clock -
// aging buckets, period totals, statements. The seed dates every row
// relative to its own clock, so with calendar dates normalised to their
// offset from the seed day these windows are as deterministic as the rest of
// the script: an invoice seeded 45 days back is 45 days back on every run,
// whatever the calendar says. The group runs last, after every other group's
// writes, so the windows aggregate the script's final state.
//
// Two deliberate choices:
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
//     row, so nothing is hidden. At this base that row set is only the
//     script's own AP invoice (the demo seed writes no vendor invoices).
//   - reports/ar-aging is NOT recorded: its rows are per-customer AR sums,
//     and the demo seed draws each customer's invoices from the global rand
//     source inside a map-iteration loop, so which customer owns which drawn
//     invoice is random per run. Global sums over the same draws are stable
//     (that is what the sales summary and dashboard windows pin), but the
//     per-customer ledger is not. Same for dashboard/top-customers and
//     dashboard/order-activity, whose per-row content rides on the same
//     random assignment (and whose LIMIT makes even membership random).
//     Pinning all three needs seed determinism or a contract decision - see
//     the deferred list in GOLDENS.md.

func clockGroups() []groupDef {
	return []groupDef{{
		name: "clockwindow",
		steps: []stepDef{
			{name: "ap.aging", method: "GET", path: "/api/v1/ap/aging", sortPrimaryArray: true},
			{name: "reports.sales_summary", method: "GET", path: "/api/v1/reports/sales-summary"},
			{name: "dashboard.summary", method: "GET", path: "/api/v1/dashboard/summary"},
			{name: "dashboard.revenue_trend", method: "GET", path: "/api/v1/dashboard/revenue-trend"},
			{name: "gl.trial_balance", method: "GET", path: "/api/v1/gl/trial-balance"},
			{name: "gl.profit_and_loss", method: "GET", path: "/api/v1/gl/profit-and-loss?start={today-30}&end={today}"},
			{name: "gl.balance_sheet", method: "GET", path: "/api/v1/gl/balance-sheet"},
		},
	}}
}
