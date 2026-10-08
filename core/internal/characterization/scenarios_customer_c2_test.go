// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// C2-1: the ship-to addresses and the payment terms master, both new to the
// contract, and the customer events read once through the feed. The group
// runs straight after customer_contacts and uses the customer that group made
// (a_customer), at the revision that group left it (5 after its edits).

func customerShipToTermsGroups() []groupDef {
	ifMatch := func(rev string) map[string]string { return map[string]string{"If-Match": `"` + rev + `"`} }
	ship := func(extra map[string]any) map[string]any {
		b := map[string]any{"code": "YARD", "name": "Lakeshore job site", "line1": "12 Lake Rd"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	terms := func(extra map[string]any) map[string]any {
		b := map[string]any{"code": "GOLDEN-NET15", "name": "Golden net 15", "kind": "net_days", "net_days": 15}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	return []groupDef{{
		name: "payment_terms",
		steps: []stepDef{
			{name: "payment_terms.list", method: "GET", path: "/api/v1/payment-terms?limit=3&include=total"},
			{name: "payment_terms.list.kind", method: "GET", path: "/api/v1/payment-terms?kind=due_on_receipt"},
			{name: "payment_terms.list.unsupported_kind", method: "GET", path: "/api/v1/payment-terms?kind=DUE_ON_RECEIPT"},
			{name: "payment_terms.list.unsupported_parameter", method: "GET", path: "/api/v1/payment-terms?offset=0"},
			{
				name: "payment_terms.create", method: "POST", path: "/api/v1/payment-terms",
				body:    terms(map[string]any{"discount_percent": "2", "discount_days": 10}),
				extract: map[string]string{"a_terms": "/id"},
			},
			{name: "payment_terms.create.day_of_month", method: "POST", path: "/api/v1/payment-terms",
				body: map[string]any{"code": "GOLDEN-D25", "name": "25th of next month", "kind": "day_of_month", "day_of_month": 25}},
			{name: "payment_terms.create.invalid", method: "POST", path: "/api/v1/payment-terms",
				body: map[string]any{"code": "bad code", "kind": "day_of_month", "day_of_month": 32, "discount_percent": "2"}},
			{name: "payment_terms.create.duplicate", method: "POST", path: "/api/v1/payment-terms", body: terms(nil)},
			{name: "payment_terms.get", method: "GET", path: "/api/v1/payment-terms/{a_terms}"},
			{name: "payment_terms.get.not_found", method: "GET", path: "/api/v1/payment-terms/" + r1bAMissingID},
			{name: "payment_terms.update.without_revision", method: "PUT", path: "/api/v1/payment-terms/{a_terms}", body: terms(nil)},
			{name: "payment_terms.update", method: "PUT", path: "/api/v1/payment-terms/{a_terms}", headers: ifMatch("1"),
				body: terms(map[string]any{"name": "Golden net 20", "net_days": 20})},
			{name: "payment_terms.update.stale", method: "PUT", path: "/api/v1/payment-terms/{a_terms}", headers: ifMatch("1"), body: terms(nil)},
			{name: "payment_terms.update.code_changed", method: "PUT", path: "/api/v1/payment-terms/{a_terms}", headers: ifMatch("2"),
				body: terms(map[string]any{"code": "GOLDEN-OTHER"})},
		},
	}, {
		name: "customer_ship_tos",
		steps: []stepDef{
			{name: "ship_to.list_empty", method: "GET", path: "/api/v1/customers/{a_customer}/ship-tos"},
			{
				name: "ship_to.create", method: "POST", path: "/api/v1/customers/{a_customer}/ship-tos",
				body: ship(map[string]any{
					"line2": "Gate 3", "city": "Kelowna", "region": "BC", "postal_code": "V1Y 1A1", "country": "CA",
					"phone": "250-555-0100", "delivery_instructions": "Call before arriving", "tax_rate_percent": "12",
				}),
				extract: map[string]string{"a_ship_to": "/id"},
			},
			{name: "ship_to.create.second", method: "POST", path: "/api/v1/customers/{a_customer}/ship-tos",
				body:    map[string]any{"code": "SITE2", "name": "Second site", "line1": "9 Hill St", "is_default": true},
				extract: map[string]string{"a_ship_to2": "/id"}},
			{name: "ship_to.create.invalid", method: "POST", path: "/api/v1/customers/{a_customer}/ship-tos",
				body: map[string]any{"country": "can", "tax_rate_percent": "101"}},
			{name: "ship_to.create.duplicate", method: "POST", path: "/api/v1/customers/{a_customer}/ship-tos", body: ship(nil)},
			{name: "ship_to.create.unknown_customer", method: "POST", path: "/api/v1/customers/" + r1bAMissingID + "/ship-tos", body: ship(nil)},
			{name: "ship_to.list", method: "GET", path: "/api/v1/customers/{a_customer}/ship-tos?include=total"},
			{name: "ship_to.list.inactive", method: "GET", path: "/api/v1/customers/{a_customer}/ship-tos?is_active=false"},
			{name: "ship_to.list.unsupported_parameter", method: "GET", path: "/api/v1/customers/{a_customer}/ship-tos?offset=0"},
			{name: "ship_to.get", method: "GET", path: "/api/v1/ship-tos/{a_ship_to}"},
			{name: "ship_to.get.not_found", method: "GET", path: "/api/v1/ship-tos/" + r1bAMissingID},
			{name: "ship_to.update.without_revision", method: "PUT", path: "/api/v1/ship-tos/{a_ship_to}", body: ship(nil)},
			{name: "ship_to.update", method: "PUT", path: "/api/v1/ship-tos/{a_ship_to}", headers: ifMatch("2"),
				body: ship(map[string]any{"name": "Lakeshore job site (closed)", "is_active": false})},
			{name: "ship_to.update.stale", method: "PUT", path: "/api/v1/ship-tos/{a_ship_to}", headers: ifMatch("1"), body: ship(nil)},

			// The customer takes the new terms: a customer.updated of part terms.
			{name: "customer.a.terms", method: "PUT", path: "/api/v1/customers/{a_customer}",
				headers: ifMatch("5"),
				body: map[string]any{
					"account_number": "GOLD-A-001", "name": "Golden Contacts Co", "email": "contacts@example.com",
					"phone": "250-555-0177", "address": "7 Golden Way, Kelowna BC", "tier": "gold",
					"credit_limit_cents": 5000000, "payment_terms_id": "{a_terms}",
				}},
			{name: "customer.a.get", method: "GET", path: "/api/v1/customers/{a_customer}"},

			// Every event the customer writes above, read once through the feed:
			// the outbox wiring of the module is pinned here.
			{name: "customer.events.created", method: "GET", path: "/api/v1/events?types=customer.created&limit=10"},
			{name: "customer.events.updated", method: "GET", path: "/api/v1/events?types=customer.updated&limit=50"},
		},
	}}
}
