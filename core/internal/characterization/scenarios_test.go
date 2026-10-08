// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import "testing"

// The scenario script: an ordered list of groups, each group one golden file
// under testdata/goldens/. The order is load-bearing in more than one way.
//
//  1. Writes run in a fixed order, so sequence-derived numbers (order
//     numbers, journal entry numbers) land on the same values every run.
//  2. A later step can reference a value extracted from an earlier response
//     ({order}, {invoice}, ...), substituted at run time and normalised onto
//     the same stable id in the golden.
//  3. The location group runs first, before anything else writes audit rows:
//     GET /api/v1/users is the union of user_locations and audit_log
//     subjects, and its empty answer is part of the contract.
//  4. The gl group runs before the flows that post to the general ledger
//     (invoice fulfilment, payments, deposits), so its account balances show
//     the seeded chart alone; and the governance and millwork groups run
//     before the apps group toggles the governance app off and on again.
//  5. The exposure group runs after the quote group (it reuses that group's
//     quote), and the clock group runs last so its windows aggregate every
//     other group's writes.
func allGroups() []groupDef {
	return concat(
		locationGroups(),
		healthGroups(),
		productGroups(),
		customerGroups(),
		salesGroups(),
		glGroups(),
		quoteGroups(),
		exposureGroups(),
		orderGroups(),
		invoiceGroups(),
		paymentGroups(),
		accountGroups(),
		depositGroups(),
		taxGroups(),
		vendorGroups(),
		purchaseOrderGroups(),
		apGroups(),
		matchingGroups(),
		bankreconGroups(),
		posGroups(),
		pricingGroups(),
		rebateGroups(),
		escalatorGroups(),
		deliveryGroups(),
		inventoryGroups(),
		documentGroups(),
		reportingGroups(),
		dashboardGroups(),
		pimGroups(),
		parsingGroups(),
		visionGroups(),
		millworkGroups(),
		configuratorGroups(),
		governanceGroups(),
		partnerGroups(),
		portalGroups(),
		techadminGroups(),
		staffGroups(),
		appsGroups(),
		integrationGroups(),
		clockGroups(),
	)
}

type stepDef struct {
	name    string
	method  string
	path    string
	body    any
	headers map[string]string
	extract map[string]string // var name -> JSON pointer into the response
	// sortPrimaryArray marks steps whose top-level response array is sorted
	// before comparison (see capturedStep.SortPrimaryArray).
	sortPrimaryArray bool
	// maskCustomerIdentity marks steps whose customer identity fields are
	// masked to a class placeholder before comparison (see
	// capturedStep.MaskCustomerIdentity).
	maskCustomerIdentity bool
	// maskOrderCount marks steps whose order_count field is masked to a
	// placeholder before comparison (see capturedStep.MaskOrderCount).
	maskOrderCount bool
	// maskFields maps response keys to placeholders (see
	// capturedStep.MaskFields).
	maskFields map[string]any
	// setup, when set, runs before the request is built: it may insert
	// fixture rows through the harness's own SQL (h.dbURL) and set h.vars.
	// It is for state the API cannot create (an exposed quote), never for
	// anything the product writes itself.
	setup func(t *testing.T, h *harness)
	// sql, when set, makes the step a read-only probe of the throwaway
	// database instead of an HTTP request: its rows are the recorded
	// response (see doSQLStep). For effects no route exposes.
	sql string
	// captureHeaders names response headers recorded in the golden
	// (capturedResponse.Headers); all other response headers stay out.
	captureHeaders []string
}

type groupDef struct {
	name  string
	steps []stepDef
	// serverEnv, when set, runs the group against its own server process
	// started with these extra variables (on the same database), for routes
	// a feature flag mounts.
	serverEnv map[string]string
}

func concat(groups ...[]groupDef) []groupDef {
	var out []groupDef
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
