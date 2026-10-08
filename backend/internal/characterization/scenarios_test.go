// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// The scenario script: an ordered list of groups, each group one golden file
// under testdata/goldens/. The order is load-bearing in three ways.
//
//  1. Writes run in a fixed order, so sequence-derived numbers (order
//     numbers, journal entry numbers) land on the same values every run.
//  2. A later step can reference a value extracted from an earlier response
//     ({order}, {invoice}, ...), substituted at run time and normalised onto
//     the same stable id in the golden.
//  3. The gl group runs before the flows that post to the general ledger
//     (invoice fulfilment, payments, deposits), so its account balances show
//     the seeded chart alone; and the governance and millwork groups run
//     before the apps group toggles the governance app off and on again.
func allGroups() []groupDef {
	return concat(
		healthGroups(),
		productGroups(),
		customerGroups(),
		salesGroups(),
		glGroups(),
		quoteGroups(),
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
	)
}

type stepDef struct {
	name    string
	method  string
	path    string
	body    any
	headers map[string]string
	extract map[string]string // var name -> JSON pointer into the response
	// sortBodyArrays marks steps whose response arrays are sorted before
	// comparison (see capturedStep.SortBodyArrays).
	sortBodyArrays bool
}

type groupDef struct {
	name  string
	steps []stepDef
}

func concat(groups ...[]groupDef) []groupDef {
	var out []groupDef
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
