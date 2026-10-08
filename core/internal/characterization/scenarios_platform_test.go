// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Platform groups: PIM, AI parsing, vision, the millwork app (millwork +
// configurator), governance, partner surface, dealer portal, projects, tech
// admin, staff roster, the apps registry and the AI_LM integration surface.

func pimGroups() []groupDef {
	return []groupDef{{
		name: "pim",
		steps: []stepDef{
			// No row exists yet: the synthetic empty-content shape.
			{name: "pim.content.get_empty", method: "GET", path: "/api/v1/products/{product}/pim/content"},
			{
				name:   "pim.content.update",
				method: "PUT",
				path:   "/api/v1/products/{product}/pim/content",
				body: map[string]any{
					"short_description": "Golden 2x4 premium stud",
					"seo_title":         "Golden SPF stud",
				},
			},
			{name: "pim.content.get", method: "GET", path: "/api/v1/products/{product}/pim/content"},
		},
	}}
}

func parsingGroups() []groupDef {
	return []groupDef{{
		name: "parsing",
		steps: []stepDef{
			// With no AI key the parser answers with its fixed synthetic demo
			// list; parse_time_ms is a timing measurement and normalises.
			{
				name:   "parsing.upload",
				method: "POST",
				path:   "/api/v1/parsing/upload",
				body: &multipartDef{
					FieldName: "file", Filename: "material-list.txt",
					ContentType: "text/plain", Content: "2x4x8 SPF Premium x10\n",
				},
			},
		},
	}}
}

func visionGroups() []groupDef {
	return []groupDef{{
		name: "vision",
		steps: []stepDef{
			{
				name:   "vision.scan",
				method: "POST",
				path:   "/api/v1/vision/scan",
				body: map[string]any{
					"blueprint_text":    "Wall: 2x4 studs at 16in OC, 92-5/8in studs, SPF species",
					"config_selections": map[string]any{"Species": "SPF", "Grade": "#2"},
				},
			},
			{name: "vision.scan.empty", method: "POST", path: "/api/v1/vision/scan",
				body: map[string]any{"blueprint_text": ""}},
		},
	}}
}

func millworkGroups() []groupDef {
	return []groupDef{{
		name: "millwork",
		steps: []stepDef{
			{
				name:   "millwork.option.create",
				method: "POST",
				path:   "/api/v1/millwork/options",
				body: map[string]any{
					"category": "Species", "name": "Golden Douglas Fir",
					"price_adjustment": 1.5, "attributes": map[string]any{"grade": "Clear"},
				},
				extract: map[string]string{"myMillworkOption": "/id"},
			},
			{name: "millwork.option.list", method: "GET", path: "/api/v1/millwork/options?category=Species"},
		},
	}}
}

func configuratorGroups() []groupDef {
	return []groupDef{{
		name: "configurator",
		steps: []stepDef{
			{name: "configurator.options", method: "GET", path: "/api/v1/configurator/options?attribute_type=Species"},
			{
				name:   "configurator.validate",
				method: "POST",
				path:   "/api/v1/configurator/validate",
				body:   map[string]any{"selections": map[string]any{"Species": "SYP", "Grade": "#2"}},
			},
			{
				name:   "configurator.build_sku",
				method: "POST",
				path:   "/api/v1/configurator/build-sku",
				body: map[string]any{
					"product_type": "Lumber",
					"selections":   map[string]any{"Species": "SYP", "Grade": "#2", "Length": "8"},
				},
			},
			{name: "configurator.validate.empty", method: "POST", path: "/api/v1/configurator/validate",
				body: map[string]any{"selections": map[string]any{}}},
		},
	}}
}

func governanceGroups() []groupDef {
	return []groupDef{{
		name: "governance",
		steps: []stepDef{
			{
				name:   "governance.rfc.create",
				method: "POST",
				path:   "/api/v1/governance/rfcs",
				body: map[string]any{
					"title": "Golden RFC", "problem_statement": "characterisation",
					"proposed_solution": "none",
				},
				extract: map[string]string{"myRFC": "/id"},
			},
			{name: "governance.rfc.get", method: "GET", path: "/api/v1/governance/rfcs/{myRFC}"},
		},
	}}
}

func partnerGroups() []groupDef {
	return []groupDef{{
		name: "partner",
		steps: []stepDef{
			// The partner surface authenticates ERP JWT claims; AUTH_MODE=dev
			// mounts no auth middleware, so claims are nil and every partner
			// route answers 401. That is the dev-mode behaviour at this base.
			{name: "partner.dashboard.unauthenticated", method: "GET", path: "/api/partner/v1/dashboard"},
		},
	}}
}

func portalGroups() []groupDef {
	return []groupDef{{
		name: "portal",
		steps: []stepDef{
			{name: "portal.config", method: "GET", path: "/api/portal/v1/config"},
			{
				name:   "portal.login",
				method: "POST",
				path:   "/api/portal/v1/login",
				body:   map[string]any{"email": "demo@kelbrook.ca", "password": "password"},
			},
			// Session cookie from login rides along for the remaining steps.
			{name: "portal.catalog", method: "GET", path: "/api/portal/v1/catalog?q=LUM-248"},
			{
				name:   "portal.cart.add",
				method: "POST",
				path:   "/api/portal/v1/cart/items",
				body:   map[string]any{"product_id": "{product}", "quantity": 2},
			},
			{name: "portal.cart.get", method: "GET", path: "/api/portal/v1/cart"},
		},
	}, {
		name: "project",
		steps: []stepDef{
			{
				name:    "project.create",
				method:  "POST",
				path:    "/api/portal/v1/projects",
				body:    map[string]any{"name": "Golden Project"},
				extract: map[string]string{"myProject": "/id"},
			},
			{name: "project.get", method: "GET", path: "/api/portal/v1/projects/{myProject}"},
		},
	}}
}

func techadminGroups() []groupDef {
	return []groupDef{{
		name: "techadmin",
		steps: []stepDef{
			{name: "techadmin.keys.list", method: "GET", path: "/api/v1/admin/keys"},
			{
				name:   "techadmin.key.create",
				method: "POST",
				path:   "/api/v1/admin/keys",
				body:   map[string]any{"name": "golden-key", "scopes": []any{"quotes:read"}},
			},
		},
	}}
}

func staffGroups() []groupDef {
	return []groupDef{{
		name: "staff",
		steps: []stepDef{
			{name: "staff.list", method: "GET", path: "/api/v1/admin/staff"},
			{
				name:   "staff.create",
				method: "POST",
				path:   "/api/v1/admin/staff",
				body: map[string]any{
					"email": "goldens@example.com", "full_name": "Golden Staff", "role": "sales",
				},
				extract: map[string]string{"myStaff": "/id"},
			},
			{name: "staff.modules", method: "GET", path: "/api/v1/admin/modules"},
			{name: "staff.create.missing_fields", method: "POST", path: "/api/v1/admin/staff",
				body: map[string]any{"email": "", "full_name": ""}},
		},
	}}
}

func appsGroups() []groupDef {
	return []groupDef{{
		name: "apps",
		steps: []stepDef{
			{name: "apps.list", method: "GET", path: "/api/v1/apps"},
			// Core apps refuse to be disabled.
			{name: "apps.disable_core_conflict", method: "POST", path: "/api/v1/apps/product/disable"},
			// Governance is converted and non-core: off, then back on. The
			// governance scenarios have already run by now.
			{name: "apps.governance.disable", method: "POST", path: "/api/v1/apps/governance/disable"},
			{name: "apps.governance.enable", method: "POST", path: "/api/v1/apps/governance/enable"},
			{name: "apps.unknown_key", method: "POST", path: "/api/v1/apps/no-such-app/disable"},
		},
	}}
}

func integrationGroups() []groupDef {
	const integrationKey = "fb-brain-demo-key-2026" // dev-mode default from internal/app/serve/serve.go

	withKey := map[string]string{"X-Integration-Key": integrationKey}

	return []groupDef{{
		name: "integrations",
		steps: []stepDef{
			// Unconfigured and wrong-key answers first: the whole surface
			// refuses when the key is absent, and 401s on a bad value.
			{name: "integration.no_key", method: "GET", path: "/api/integration/locations"},
			{name: "integration.wrong_key", method: "GET", path: "/api/integration/locations",
				headers: map[string]string{"X-Integration-Key": "definitely-the-wrong-key"}},
			{name: "integration.locations", method: "GET", path: "/api/integration/locations", headers: withKey},
			{name: "integration.vehicles", method: "GET", path: "/api/integration/vehicles", headers: withKey},
			{name: "integration.drivers", method: "GET", path: "/api/integration/drivers", headers: withKey},
			{name: "integration.products", method: "GET", path: "/api/integration/products?category=Lumber", headers: withKey},
			// The date filter is the seeded dispatch-day fixture's date.
			// The fixture's orders all share one created_at, so the handler's
			// ORDER BY tiebreaks on random row ids: the top-level array's
			// order on the wire is not stable, and this step pins the content
			// of every order while each order's own line arrays keep their
			// wire order (the flag is recorded in the golden).
			{name: "integration.orders", method: "GET", path: "/api/integration/orders?date={today}",
				headers: withKey, sortPrimaryArray: true},
			{
				name:    "integration.quote.create",
				method:  "POST",
				path:    "/api/integration/quotes",
				headers: withKey,
				body: map[string]any{
					"customer_id": "{customer}",
					"lines":       []map[string]any{{"product_id": "{product}", "quantity": 12, "unit_price": 550}},
				},
				extract: map[string]string{"myAIQuote": "/id"},
			},
			// Bulk pricing for the same customer: prices in cents per item.
			{
				name:    "integration.quotes.bulk_price",
				method:  "POST",
				path:    "/api/integration/quotes/bulk-price",
				headers: withKey,
				body: map[string]any{
					"customer_id": "{customer}",
					"items": []map[string]any{
						{"product_id": "{product}", "quantity": 10},
						{"product_id": "{productSheet}", "quantity": 40},
					},
				},
			},
			// Accept the quote created above and convert it: the order is
			// created AND confirmed in one call.
			{
				name:    "integration.quotes.accept_and_convert",
				method:  "POST",
				path:    "/api/integration/quotes/{myAIQuote}/accept-and-convert",
				headers: withKey,
			},
			{
				name:    "integration.validate_staff",
				method:  "POST",
				path:    "/api/integration/validate-staff",
				headers: withKey,
				body:    map[string]any{"email": "goldens@example.com"},
			},
			{
				name:    "integration.validate_staff.unknown",
				method:  "POST",
				path:    "/api/integration/validate-staff",
				headers: withKey,
				body:    map[string]any{"email": "nobody@example.com"},
			},
			{
				name:    "integration.delivery_route.create",
				method:  "POST",
				path:    "/api/integration/delivery-routes",
				headers: withKey,
				body: map[string]any{
					"vehicle_id": "{myVehicle}", "driver_id": "{myDriver}", "scheduled_date": "{today}",
					"notes": "golden characterisation route",
					"stops": []map[string]any{{"order_id": "{myOrder}", "sequence": 1}},
				},
			},
		},
	}}
}
