// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Finance and operations groups: AP matching, bank reconciliation, POS,
// pricing (rules, rebates, market indices), reporting and the dashboard.
//
// Endpoint selection note: endpoints whose answers are windows over the
// clock (AP/AR aging buckets, revenue trend, sales summary defaults,
// exposure days_open) are deliberately NOT goldened: their content is real
// behaviour but shifts every day relative to the seeded, now-relative demo
// dates. Each module is covered through its date-free reads and writes.

func matchingGroups() []groupDef {
	return []groupDef{{
		name: "matching",
		steps: []stepDef{
			{name: "matching.config.get", method: "GET", path: "/api/v1/matching/config"},
			{
				name:   "matching.config.update",
				method: "PUT",
				path:   "/api/v1/matching/config",
				body: map[string]any{
					"qty_tolerance_pct": 2.5, "price_tolerance_pct": 1.5,
					"dollar_tolerance": 10.0, "auto_approve_on_match": true,
				},
			},
		},
	}}
}

func bankreconGroups() []groupDef {
	return []groupDef{{
		name: "bankrecon",
		steps: []stepDef{
			{
				name:   "bankrecon.account.create",
				method: "POST",
				path:   "/api/v1/bankrecon/accounts",
				body: map[string]any{
					"name": "Golden Bank", "account_number": "000123456789",
					"routing_number": "012345678", "gl_account_id": "{glAccount}",
				},
				extract: map[string]string{"myBankAccount": "/id"},
			},
			{name: "bankrecon.account.list", method: "GET", path: "/api/v1/bankrecon/accounts"},
		},
	}}
}

func posGroups() []groupDef {
	return []groupDef{{
		name: "pos",
		steps: []stepDef{
			// A dedicated register id keeps this run independent of whatever
			// till state the demo seed left behind.
			{
				name:    "pos.till.open",
				method:  "POST",
				path:    "/api/v1/pos/till/open",
				body:    map[string]any{"register_id": "REG-01", "opening_float": 100.0},
				extract: map[string]string{"myTill": "/id"},
			},
			{name: "pos.till.current", method: "GET", path: "/api/v1/pos/till/current?register_id=REG-01"},
			{
				name:    "pos.transaction.start",
				method:  "POST",
				path:    "/api/v1/pos/transactions",
				body:    map[string]any{"register_id": "REG-01", "customer_id": "{myCustomer}"},
				extract: map[string]string{"myPOSTx": "/id"},
			},
			{name: "pos.catalog", method: "GET", path: "/api/v1/pos/catalog"},
		},
	}}
}

func pricingGroups() []groupDef {
	return []groupDef{{
		name: "pricing",
		steps: []stepDef{
			{name: "pricing.rules.list", method: "GET", path: "/api/v1/pricing/rules"},
			{
				name:   "pricing.rule.create",
				method: "POST",
				path:   "/api/v1/pricing/rules",
				body: map[string]any{
					"name": "Golden quantity break", "rule_type": "QUANTITY_BREAK",
					"product_id": "{product}", "discount_pct": 5.0, "min_quantity": 100,
					"priority": 5,
				},
				extract: map[string]string{"myPricingRule": "/id"},
			},
			{
				name:   "pricing.calculate",
				method: "GET",
				path:   "/api/v1/pricing/calculate?customer_id={myCustomer}&product_id={product}&quantity=10",
			},
			{name: "pricing.calculate.missing_params", method: "GET", path: "/api/v1/pricing/calculate"},
		},
	}}
}

func rebateGroups() []groupDef {
	return []groupDef{{
		name: "rebate",
		steps: []stepDef{
			{
				name:   "rebate.program.create",
				method: "POST",
				path:   "/api/v1/pricing/rebates/programs",
				body: map[string]any{
					"program": map[string]any{
						"vendor_id": "{myVendor}", "name": "Golden Volume Rebate",
						"program_type": "VOLUME", "start_date": "{today}T00:00:00Z",
						"end_date": "2030-01-01T00:00:00Z", "is_active": true,
					},
					"tiers": []map[string]any{{"min_volume": 100, "rebate_pct": 2.0}},
				},
				extract: map[string]string{"myRebateProgram": "/id"},
			},
			// Filtered to this run's vendor, so only the program above shows.
			{name: "rebate.program.list", method: "GET",
				path: "/api/v1/pricing/rebates/programs?vendor_id={myVendor}"},
		},
	}}
}

func escalatorGroups() []groupDef {
	return []groupDef{{
		name: "escalator",
		steps: []stepDef{
			{name: "market_index.list", method: "GET", path: "/api/v1/market-indices"},
			{
				name:   "market_index.update_metadata",
				method: "PUT",
				path:   "/api/v1/market-indices/{marketIndex}",
				body:   map[string]any{"description": "golden characterisation description"},
			},
		},
	}}
}

func reportingGroups() []groupDef {
	return []groupDef{{
		name: "reporting",
		steps: []stepDef{
			{
				name:   "reporting.save",
				method: "POST",
				path:   "/api/v1/reporting/save",
				body: map[string]any{
					"name": "Golden Report", "description": "characterisation",
					"entity_type": "orders", "definition_json": map[string]any{"metric": "total_amount"},
				},
				extract: map[string]string{"mySavedReport": "/id"},
			},
			{name: "reporting.saved.list", method: "GET", path: "/api/v1/reporting/saved"},
			{
				name:   "reporting.schedule.create",
				method: "POST",
				path:   "/api/v1/reporting/schedules",
				body: map[string]any{
					"report_id": "{mySavedReport}", "cron_expression": "0 6 * * * *",
					"recipients": []any{"goldens@example.com"}, "format": "CSV",
				},
				extract: map[string]string{"mySchedule": "/schedule/id"},
			},
			{name: "reporting.schedule.list", method: "GET", path: "/api/v1/reporting/schedules"},
			{name: "reporting.schedule.create.missing_fields", method: "POST",
				path: "/api/v1/reporting/schedules", body: map[string]any{}},
		},
	}}
}

func dashboardGroups() []groupDef {
	return []groupDef{{
		name: "dashboard",
		steps: []stepDef{
			// The only dashboard read with no clock window: stock on hand
			// against reorder points.
			{name: "dashboard.inventory_alerts", method: "GET", path: "/api/v1/dashboard/inventory-alerts"},
		},
	}}
}
