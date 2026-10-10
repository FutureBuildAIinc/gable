// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// R1-1b drafter D: pricing (categories, category rules, escalation, market
// index refresh, exposure, rebate detail), reporting (builder, saved reports,
// schedules, statements), PIM media/collateral/generation, tech admin
// settings, staff detail and module grants, governance RFC listing/update,
// configurator reads, and the server's /metrics and /uploads/ routes.
//
// These groups run after every other group. Every fixture they write is their
// own (extracted vars are prefixed d_), and every destructive step targets a
// row created in the same group.

const zeroUUID = "00000000-0000-0000-0000-000000000000"

func r1bDGroups() []groupDef {
	return concat(
		r1bDPricingGroups(),
		withServerEnv(r1bDCategoryGroups(), map[string]string{"CATEGORY_PRICING_ENABLED": "true"}),
		r1bDReportingGroups(),
		r1bDPlatformGroups(),
		r1bDServerGroups(),
	)
}

func r1bDPricingGroups() []groupDef {
	return []groupDef{
		{
			name: "pricing_escalation",
			steps: []stepDef{
				{
					name:   "pricing.escalation.percentage",
					method: "POST",
					path:   "/api/v1/pricing/calculate-escalation",
					body: map[string]any{
						"base_price": 100.0, "escalation_type": "PERCENTAGE", "escalation_rate": 2.0,
						"effective_date": "{today}", "target_date": "{today+180}",
					},
				},
				{
					name:   "pricing.escalation.index_delta",
					method: "POST",
					path:   "/api/v1/pricing/calculate-escalation",
					body: map[string]any{
						"base_price": 100.0, "escalation_type": "INDEX_DELTA",
						"market_index_id": "{marketIndex}",
						"effective_date":  "{today}", "target_date": "{today+90}",
					},
				},
				{
					name:   "pricing.escalation.target_not_after_effective",
					method: "POST",
					path:   "/api/v1/pricing/calculate-escalation",
					body: map[string]any{
						"base_price": 50.0, "escalation_type": "PERCENTAGE", "escalation_rate": 3.0,
						"effective_date": "{today}", "target_date": "{today}",
					},
				},
				{name: "pricing.escalation.bad_price", method: "POST",
					path: "/api/v1/pricing/calculate-escalation",
					body: map[string]any{"base_price": 0, "escalation_type": "PERCENTAGE"}},
				{name: "pricing.escalation.missing_type", method: "POST",
					path: "/api/v1/pricing/calculate-escalation", body: map[string]any{"base_price": 10.0}},
				{name: "pricing.escalation.bad_type", method: "POST",
					path: "/api/v1/pricing/calculate-escalation",
					body: map[string]any{"base_price": 10.0, "escalation_type": "COMPOUND"}},
				{name: "pricing.escalation.missing_dates", method: "POST",
					path: "/api/v1/pricing/calculate-escalation",
					body: map[string]any{"base_price": 10.0, "escalation_type": "PERCENTAGE"}},
			},
		},
		{
			name: "market_index_refresh",
			steps: []stepDef{
				{name: "market_index.history.before", method: "GET", path: "/api/v1/market-indices/{marketIndex}/history"},
				{name: "market_index.history.days", method: "GET",
					path: "/api/v1/market-indices/{marketIndex}/history?days=30"},
				{name: "market_index.history.bad_id", method: "GET", path: "/api/v1/market-indices/not-a-uuid/history"},
				{name: "market_index.refresh_preview", method: "POST",
					path: "/api/v1/market-indices/{marketIndex}/refresh/preview", body: map[string]any{"new_value": 123.45}},
				{name: "market_index.refresh_preview.bad_value", method: "POST",
					path: "/api/v1/market-indices/{marketIndex}/refresh/preview", body: map[string]any{"new_value": 0}},
				{name: "market_index.refresh_preview.not_found", method: "POST",
					path: "/api/v1/market-indices/" + zeroUUID + "/refresh/preview", body: map[string]any{"new_value": 100.0}},
				{name: "market_index.refresh_preview.bad_id", method: "POST",
					path: "/api/v1/market-indices/not-a-uuid/refresh/preview", body: map[string]any{"new_value": 100.0}},
				{name: "market_index.refresh", method: "POST",
					path: "/api/v1/market-indices/{marketIndex}/refresh",
					body: map[string]any{"new_value": 123.45, "source": "GOLDEN"}},
				{name: "market_index.refresh.bad_value", method: "POST",
					path: "/api/v1/market-indices/{marketIndex}/refresh", body: map[string]any{"new_value": -1}},
				{name: "market_index.refresh.not_found", method: "POST",
					path: "/api/v1/market-indices/" + zeroUUID + "/refresh", body: map[string]any{"new_value": 100.0}},
				{name: "market_index.refresh.bad_id", method: "POST",
					path: "/api/v1/market-indices/not-a-uuid/refresh", body: map[string]any{"new_value": 100.0}},
				{name: "market_index.history.after", method: "GET", path: "/api/v1/market-indices/{marketIndex}/history"},
				{name: "market_index.list_after", method: "GET", path: "/api/v1/market-indices"},
			},
		},
		{
			name: "exposure_admin",
			steps: []stepDef{
				{name: "exposure.admin_scan", method: "POST", path: "/api/v1/admin/exposure-scan"},
				{name: "exposure.report", method: "GET", path: "/api/v1/reports/exposure"},
				{name: "exposure.report.summary", method: "GET", path: "/api/v1/reports/exposure?summary=true"},
			},
		},
		{
			name: "rebate_programs",
			steps: []stepDef{
				{name: "rebate.program.get", method: "GET", path: "/api/v1/pricing/rebates/programs/{myRebateProgram}"},
				{name: "rebate.program.get.not_found", method: "GET", path: "/api/v1/pricing/rebates/programs/" + zeroUUID},
				{name: "rebate.program.get.bad_id", method: "GET", path: "/api/v1/pricing/rebates/programs/not-a-uuid"},
				{name: "rebate.claims.list_before", method: "GET",
					path: "/api/v1/pricing/rebates/programs/{myRebateProgram}/claims"},
				{
					name:   "rebate.claims.calculate",
					method: "POST",
					path:   "/api/v1/pricing/rebates/programs/{myRebateProgram}/claims/calculate",
					body: map[string]any{
						"period_start": "{today}T00:00:00Z", "period_end": "{today+90}T00:00:00Z", "mock_volume": 250,
					},
				},
				{name: "rebate.claims.calculate.bad_body", method: "POST",
					path: "/api/v1/pricing/rebates/programs/{myRebateProgram}/claims/calculate", body: "nope"},
				{name: "rebate.claims.calculate.bad_id", method: "POST",
					path: "/api/v1/pricing/rebates/programs/not-a-uuid/claims/calculate", body: map[string]any{}},
				{name: "rebate.claims.calculate.not_found", method: "POST",
					path: "/api/v1/pricing/rebates/programs/" + zeroUUID + "/claims/calculate",
					body: map[string]any{"period_start": "{today}T00:00:00Z", "period_end": "{today+90}T00:00:00Z", "mock_volume": 1}},
				{name: "rebate.claims.list", method: "GET", path: "/api/v1/pricing/rebates/programs/{myRebateProgram}/claims"},
				{name: "rebate.claims.list.bad_id", method: "GET", path: "/api/v1/pricing/rebates/programs/not-a-uuid/claims"},
			},
		},
	}
}

func r1bDReportingGroups() []groupDef {
	ordersDef := map[string]any{
		"columns": []any{
			map[string]any{"field": "status", "label": "Status"},
			map[string]any{"field": "id", "label": "Orders", "aggregation": "COUNT"},
		},
		"filters":   []any{},
		"groupings": []any{map[string]any{"field": "status"}},
	}
	return []groupDef{
		{
			name: "reporting_builder",
			steps: []stepDef{
				{name: "reporting.builder.preview", method: "POST", path: "/api/v1/reporting/builder/preview",
					body: map[string]any{"entity_type": "orders", "definition": ordersDef}, sortPrimaryArray: true},
				{name: "reporting.builder.preview.invalid_column", method: "POST", path: "/api/v1/reporting/builder/preview",
					body: map[string]any{"entity_type": "orders", "definition": map[string]any{
						"columns": []any{map[string]any{"field": "no_such_field", "label": "x"}}}}},
				{name: "reporting.builder.preview.bad_body", method: "POST",
					path: "/api/v1/reporting/builder/preview", body: "nope"},
				{name: "reporting.builder.export.csv", method: "POST", path: "/api/v1/reporting/builder/export",
					body: map[string]any{"entity_type": "orders", "format": "csv", "definition": ordersDef}},
				// One aggregate row: a multi-row sheet's byte length moves with the
				// GROUP BY row order, which the query leaves unspecified.
				// The workbook is a zip: its byte length differs between hosts
				// (it measured 6140 here and 6058 on the CI runner for the same
				// request), and the text hash is empty for a non PDF, so the
				// step pins status and content type; the length is masked.
				{name: "reporting.builder.export.xlsx", method: "POST", path: "/api/v1/reporting/builder/export",
					body: map[string]any{"entity_type": "orders", "format": "xlsx", "definition": map[string]any{
						"columns": []any{map[string]any{"field": "id", "label": "Orders", "aggregation": "COUNT"}},
					}},
					maskFields: map[string]any{"length": "<xlsx-length>"}},
				{name: "reporting.builder.export.bad_format", method: "POST", path: "/api/v1/reporting/builder/export",
					body: map[string]any{"entity_type": "orders", "format": "pdf", "definition": ordersDef}},
				{name: "reporting.builder.export.bad_body", method: "POST",
					path: "/api/v1/reporting/builder/export", body: "nope"},
				// At this base every BI entity schema names a column that does not
				// exist (i.invoice_number, o.order_number, p.name): all three
				// well formed exports answer 500.
				{name: "reporting.bi_export.invoices", method: "GET", path: "/api/v1/reporting/export/invoices"},
				{name: "reporting.bi_export.orders", method: "GET", path: "/api/v1/reporting/export/orders"},
				{name: "reporting.bi_export.inventory", method: "GET", path: "/api/v1/reporting/export/inventory"},
				{name: "reporting.bi_export.invalid_entity", method: "GET", path: "/api/v1/reporting/export/no_such_entity"},
			},
		},
		{
			name: "reporting_saved",
			steps: []stepDef{
				{
					name:   "reporting.saved.create",
					method: "POST",
					path:   "/api/v1/reporting/save",
					body: map[string]any{
						"name": "Golden D Orders By Status", "description": "r1b drafter d",
						"entity_type": "orders", "definition_json": ordersDef,
					},
					extract: map[string]string{"d_saved": "/id"},
				},
				{name: "reporting.saved.get", method: "GET", path: "/api/v1/reporting/saved/{d_saved}"},
				{name: "reporting.saved.get.not_found", method: "GET", path: "/api/v1/reporting/saved/" + zeroUUID},
				{
					name:   "reporting.saved.update",
					method: "PUT",
					path:   "/api/v1/reporting/saved/{d_saved}",
					body: map[string]any{
						"name": "Golden D Orders By Status v2", "description": "updated",
						"entity_type": "orders", "definition_json": ordersDef,
					},
				},
				{name: "reporting.saved.update.bad_body", method: "PUT",
					path: "/api/v1/reporting/saved/{d_saved}", body: "nope"},
				{name: "reporting.saved.get_after_update", method: "GET", path: "/api/v1/reporting/saved/{d_saved}"},
				{name: "reporting.saved.run", method: "POST", path: "/api/v1/reporting/saved/{d_saved}/run",
					sortPrimaryArray: true},
				{name: "reporting.saved.run.not_found", method: "POST",
					path: "/api/v1/reporting/saved/" + zeroUUID + "/run"},
				{
					name:   "reporting.schedule.create_own",
					method: "POST",
					path:   "/api/v1/reporting/schedules",
					body: map[string]any{
						"report_id": "{d_saved}", "cron_expression": "0 30 6 * * *",
						"recipients": []any{"goldens-d@example.com"}, "format": "XLSX",
					},
					extract: map[string]string{"d_schedule": "/schedule/id"},
				},
				{name: "reporting.schedule.delete", method: "DELETE", path: "/api/v1/reporting/schedules/{d_schedule}"},
				{name: "reporting.schedule.delete.again", method: "DELETE", path: "/api/v1/reporting/schedules/{d_schedule}"},
				{name: "reporting.schedule.delete.unknown", method: "DELETE",
					path: "/api/v1/reporting/schedules/" + zeroUUID},
				{name: "reporting.saved.delete", method: "DELETE", path: "/api/v1/reporting/saved/{d_saved}"},
				{name: "reporting.saved.delete.again", method: "DELETE", path: "/api/v1/reporting/saved/{d_saved}"},
				{name: "reporting.saved.get_after_delete", method: "GET", path: "/api/v1/reporting/saved/{d_saved}"},
			},
		},
		{
			name: "reporting_reports",
			steps: []stepDef{
				{name: "reporting.daily_till", method: "GET", path: "/api/v1/reports/daily-till?date={today}"},
				{name: "reporting.customer_statement", method: "GET",
					path: "/api/v1/reports/customer-statement/{myCustomer}?start={today-30}&end={today}"},
				{name: "reporting.customer_statement.empty_window", method: "GET",
					path: "/api/v1/reports/customer-statement/{myCustomer}?start={today+400}&end={today+401}"},
				{name: "reporting.customer_statement.bad_id", method: "GET",
					path: "/api/v1/reports/customer-statement/not-a-uuid"},
			},
		},
	}
}

func r1bDPlatformGroups() []groupDef {
	return []groupDef{
		{
			name: "pim_media",
			steps: []stepDef{
				{name: "pim.detail", method: "GET", path: "/api/v1/products/{product}/detail"},
				{name: "pim.detail.not_found", method: "GET", path: "/api/v1/products/" + zeroUUID + "/detail"},
				{name: "pim.detail.bad_id", method: "GET", path: "/api/v1/products/not-a-uuid/detail"},
				{name: "pim.media.list", method: "GET", path: "/api/v1/products/{product}/pim/media"},
				{name: "pim.collateral.list", method: "GET", path: "/api/v1/products/{product}/pim/collateral"},
				{name: "pim.media.list.bad_id", method: "GET", path: "/api/v1/products/not-a-uuid/pim/media"},
				{name: "pim.collateral.list.bad_id", method: "GET", path: "/api/v1/products/not-a-uuid/pim/collateral"},
				{name: "pim.generate.descriptions", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/descriptions",
					body: map[string]any{"tone": "professional", "audience": "contractors"}},
				{name: "pim.generate.seo", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/seo",
					body: map[string]any{"target_keywords": []any{"lumber", "stud"}}},
				{name: "pim.generate.image", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/image",
					body: map[string]any{"style": "photographic", "prompt": "golden stud"}},
				{name: "pim.generate.collateral", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/collateral",
					body: map[string]any{"type": "spec_sheet", "tone": "professional", "audience": "contractors"}},
				{name: "pim.generate.descriptions.bad_body", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/descriptions", body: "nope"},
				{name: "pim.generate.seo.bad_body", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/seo", body: "nope"},
				{name: "pim.generate.image.bad_body", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/image", body: "nope"},
				{name: "pim.generate.collateral.bad_body", method: "POST",
					path: "/api/v1/products/{product}/pim/generate/collateral", body: "nope"},
				{name: "pim.generate.descriptions.bad_id", method: "POST",
					path: "/api/v1/products/not-a-uuid/pim/generate/descriptions", body: map[string]any{}},
				{name: "pim.media.list_after_generate", method: "GET", path: "/api/v1/products/{product}/pim/media"},
				{name: "pim.collateral.list_after_generate", method: "GET", path: "/api/v1/products/{product}/pim/collateral"},
				{name: "pim.media.delete.unknown", method: "DELETE",
					path: "/api/v1/products/{product}/pim/media/" + zeroUUID},
				{name: "pim.media.delete.bad_id", method: "DELETE",
					path: "/api/v1/products/{product}/pim/media/not-a-uuid"},
				{name: "pim.media.primary.unknown", method: "PATCH",
					path: "/api/v1/products/{product}/pim/media/" + zeroUUID + "/primary"},
				{name: "pim.media.primary.bad_media_id", method: "PATCH",
					path: "/api/v1/products/{product}/pim/media/not-a-uuid/primary"},
				{name: "pim.media.primary.bad_product_id", method: "PATCH",
					path: "/api/v1/products/not-a-uuid/pim/media/" + zeroUUID + "/primary"},
				{name: "pim.collateral.delete.unknown", method: "DELETE",
					path: "/api/v1/products/{product}/pim/collateral/" + zeroUUID},
				{name: "pim.collateral.delete.bad_id", method: "DELETE",
					path: "/api/v1/products/{product}/pim/collateral/not-a-uuid"},
			},
		},
		{
			name: "techadmin_keys",
			steps: []stepDef{
				{
					name:    "techadmin.key.create_own",
					method:  "POST",
					path:    "/api/v1/admin/keys",
					body:    map[string]any{"name": "golden-d-key", "scopes": []any{"quotes:read"}},
					extract: map[string]string{"d_key": "/key/id"},
				},
				{name: "techadmin.key.revoke", method: "DELETE", path: "/api/v1/admin/keys/{d_key}"},
				{name: "techadmin.key.revoke.again", method: "DELETE", path: "/api/v1/admin/keys/{d_key}"},
				{name: "techadmin.key.revoke.unknown", method: "DELETE", path: "/api/v1/admin/keys/" + zeroUUID},
			},
		},
		{
			name: "techadmin_settings",
			steps: []stepDef{
				// The settings are documents on a revision anchor: every write
				// carries the revision it read (If-Match or the body form),
				// and a delete moves the revision, never backwards.
				{name: "techadmin.ai.get_initial", method: "GET", path: "/api/v1/admin/settings/ai"},
				{name: "techadmin.ai.save.missing_key", method: "PUT", path: "/api/v1/admin/settings/ai",
					body: map[string]any{"api_key": "", "revision": 1}},
				{name: "techadmin.ai.save.bad_body", method: "PUT", path: "/api/v1/admin/settings/ai", body: "nope"},
				{name: "techadmin.ai.save.missing_precondition", method: "PUT", path: "/api/v1/admin/settings/ai",
					body: map[string]any{"api_key": "golden-placeholder-value"}},
				{name: "techadmin.ai.save.stale_precondition", method: "PUT", path: "/api/v1/admin/settings/ai",
					body: map[string]any{"api_key": "golden-placeholder-value", "revision": 9}},
				{name: "techadmin.ai.save.bad_base_url", method: "PUT", path: "/api/v1/admin/settings/ai",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"api_key": "golden-placeholder-value", "base_url": "ftp://not-allowed.invalid"}},
				{name: "techadmin.ai.get_after_bad_base_url", method: "GET", path: "/api/v1/admin/settings/ai"},
				{name: "techadmin.ai.save", method: "PUT", path: "/api/v1/admin/settings/ai",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"api_key": "golden-placeholder-value"}},
				{name: "techadmin.ai.get_saved", method: "GET", path: "/api/v1/admin/settings/ai"},
				{name: "techadmin.ai.delete.missing_precondition", method: "DELETE", path: "/api/v1/admin/settings/ai"},
				{name: "techadmin.ai.delete", method: "DELETE", path: "/api/v1/admin/settings/ai",
					headers: map[string]string{"If-Match": `"2"`}},
				{name: "techadmin.ai.get_after_delete", method: "GET", path: "/api/v1/admin/settings/ai"},
				{name: "techadmin.ai.delete.again", method: "DELETE", path: "/api/v1/admin/settings/ai",
					headers: map[string]string{"If-Match": `"3"`}},
				{name: "techadmin.routing.get_initial", method: "GET", path: "/api/v1/admin/settings/routing"},
				{name: "techadmin.routing.save.missing_key", method: "PUT", path: "/api/v1/admin/settings/routing",
					body: map[string]any{"api_key": "", "revision": 1}},
				{name: "techadmin.routing.save.bad_body", method: "PUT", path: "/api/v1/admin/settings/routing", body: "nope"},
				{name: "techadmin.routing.save", method: "PUT", path: "/api/v1/admin/settings/routing",
					body: map[string]any{"api_key": "golden-placeholder-routing", "revision": 1}},
				{name: "techadmin.routing.get_saved", method: "GET", path: "/api/v1/admin/settings/routing"},
				{name: "techadmin.routing.delete", method: "DELETE", path: "/api/v1/admin/settings/routing",
					headers: map[string]string{"If-Match": `"2"`}},
				{name: "techadmin.routing.get_after_delete", method: "GET", path: "/api/v1/admin/settings/routing"},
			},
		},
		{
			name: "staff_detail",
			steps: []stepDef{
				// Every write carries the staff revision (If-Match or the
				// body form); the module grants move it, because the modules
				// list is part of the staff document.
				{
					name:   "staff.detail.create_own",
					method: "POST",
					path:   "/api/v1/admin/staff",
					body: map[string]any{
						"email": "goldens-d@example.com", "full_name": "Golden D Staff", "role": "counter",
					},
					extract: map[string]string{"d_staff": "/id"},
				},
				{name: "staff.detail.get", method: "GET", path: "/api/v1/admin/staff/{d_staff}"},
				{name: "staff.detail.get.not_found", method: "GET", path: "/api/v1/admin/staff/" + zeroUUID},
				{name: "staff.detail.get.bad_id", method: "GET", path: "/api/v1/admin/staff/not-a-uuid"},
				{name: "staff.detail.update.missing_revision", method: "PUT", path: "/api/v1/admin/staff/{d_staff}",
					body: map[string]any{"full_name": "Golden D Staff Renamed"}},
				{name: "staff.detail.update.stale_revision", method: "PUT", path: "/api/v1/admin/staff/{d_staff}",
					body: map[string]any{"full_name": "Golden D Staff Renamed", "revision": 9}},
				{name: "staff.detail.update", method: "PUT", path: "/api/v1/admin/staff/{d_staff}",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"full_name": "Golden D Staff Renamed", "role": "yard", "active": true}},
				{name: "staff.detail.update.not_found", method: "PUT", path: "/api/v1/admin/staff/" + zeroUUID,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"role": "yard"}},
				{name: "staff.detail.update.bad_id", method: "PUT", path: "/api/v1/admin/staff/not-a-uuid",
					body: map[string]any{"role": "yard"}},
				{name: "staff.detail.update.bad_body", method: "PUT", path: "/api/v1/admin/staff/{d_staff}", body: "nope"},
				{name: "staff.modules.list_before", method: "GET", path: "/api/v1/admin/modules"},
				{name: "staff.modules.set_disabled", method: "PUT", path: "/api/v1/admin/modules/ai_lm",
					body: map[string]any{"enabled": false, "revision": 1}},
				{name: "staff.modules.set_disabled.stale", method: "PUT", path: "/api/v1/admin/modules/ai_lm",
					body: map[string]any{"enabled": false, "revision": 1}},
				{name: "staff.modules.set_enabled.bad_body", method: "PUT", path: "/api/v1/admin/modules/ai_lm",
					body: "nope"},
				{name: "staff.modules.list_after_disable", method: "GET", path: "/api/v1/admin/modules"},
				{name: "staff.modules.grant", method: "POST", path: "/api/v1/admin/staff/{d_staff}/modules",
					body: map[string]any{"module_id": "ai_lm", "revision": 2}},
				{name: "staff.modules.grant.again_idempotent", method: "POST", path: "/api/v1/admin/staff/{d_staff}/modules",
					body: map[string]any{"module_id": "ai_lm", "revision": 3}},
				{name: "staff.modules.grant.missing_module", method: "POST", path: "/api/v1/admin/staff/{d_staff}/modules",
					body: map[string]any{"module_id": "", "revision": 3}},
				{name: "staff.modules.grant.unknown_module", method: "POST", path: "/api/v1/admin/staff/{d_staff}/modules",
					body: map[string]any{"module_id": "no_such", "revision": 3}},
				{name: "staff.modules.grant.bad_id", method: "POST", path: "/api/v1/admin/staff/not-a-uuid/modules",
					body: map[string]any{"module_id": "ai_lm"}},
				{name: "staff.modules.grant.unknown_staff", method: "POST", path: "/api/v1/admin/staff/" + zeroUUID + "/modules",
					body: map[string]any{"module_id": "ai_lm", "revision": 1}},
				{name: "staff.modules.revoke", method: "DELETE", path: "/api/v1/admin/staff/{d_staff}/modules/ai_lm",
					headers: map[string]string{"If-Match": `"3"`}},
				{name: "staff.modules.revoke.again", method: "DELETE", path: "/api/v1/admin/staff/{d_staff}/modules/ai_lm",
					headers: map[string]string{"If-Match": `"4"`}},
				{name: "staff.modules.revoke.bad_id", method: "DELETE", path: "/api/v1/admin/staff/not-a-uuid/modules/ai_lm"},
				{name: "staff.modules.set_enabled_restore", method: "PUT", path: "/api/v1/admin/modules/ai_lm",
					body: map[string]any{"enabled": true, "revision": 2}},
				{name: "staff.detail.get_final", method: "GET", path: "/api/v1/admin/staff/{d_staff}"},
			},
		},
		{
			name: "governance_rfcs",
			steps: []stepDef{
				{
					name:   "governance.rfc.create_own",
					method: "POST",
					path:   "/api/v1/governance/rfcs",
					body: map[string]any{
						"title": "Golden D RFC", "problem_statement": "r1b drafter d", "proposed_solution": "none",
					},
					extract: map[string]string{"d_rfc": "/id"},
				},
				// The list no longer fails on the seed's NULL content rows; it
				// is the cursor envelope.
				{name: "governance.rfc.list", method: "GET", path: "/api/v1/governance/rfcs?limit=10"},
				{name: "governance.rfc.list.filter", method: "GET", path: "/api/v1/governance/rfcs?status=approved&limit=5"},
				{name: "governance.rfc.get.bad_id", method: "GET", path: "/api/v1/governance/rfcs/not-a-uuid"},
				{name: "governance.rfc.get.not_found", method: "GET", path: "/api/v1/governance/rfcs/" + zeroUUID},
				{name: "governance.rfc.update", method: "PUT", path: "/api/v1/governance/rfcs/{d_rfc}",
					body: map[string]any{
						"title": "Golden D RFC v2", "problem_statement": "updated problem",
						"proposed_solution": "updated solution", "content": "body text", "revision": 1,
					}},
				{name: "governance.rfc.update.bad_id", method: "PUT", path: "/api/v1/governance/rfcs/not-a-uuid",
					body: map[string]any{"title": "x"}},
				{name: "governance.rfc.update.bad_body", method: "PUT", path: "/api/v1/governance/rfcs/{d_rfc}", body: "nope"},
				{name: "governance.rfc.update.not_found", method: "PUT", path: "/api/v1/governance/rfcs/" + zeroUUID,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"title": "x"}},
				{name: "governance.rfc.transition", method: "POST", path: "/api/v1/governance/rfcs/{d_rfc}/transitions",
					body: map[string]any{"to": "review", "revision": 2}},
				{name: "governance.rfc.transition.bad_body", method: "POST", path: "/api/v1/governance/rfcs/{d_rfc}/transitions",
					body: "nope"},
				{name: "governance.rfc.get_after_update", method: "GET", path: "/api/v1/governance/rfcs/{d_rfc}"},
			},
		},
		{
			name: "configurator_reads",
			steps: []stepDef{
				{name: "configurator.presets", method: "GET", path: "/api/v1/configurator/presets"},
				{name: "configurator.rules", method: "GET", path: "/api/v1/configurator/rules"},
			},
		},
	}
}

func r1bDServerGroups() []groupDef {
	return []groupDef{{
		name: "server_routes",
		steps: []stepDef{
			// GET /metrics is not recorded: its body carries Go runtime and
			// process counters (go_build_info, memstats, goroutines, start
			// time) that differ on every run and the normaliser has no rule
			// for them.
			{name: "server.uploads.directory", method: "GET", path: "/uploads/"},
		},
	}}
}

// withServerEnv returns the groups with the extra server environment set, so
// the harness runs each against its own server process.
func withServerEnv(groups []groupDef, env map[string]string) []groupDef {
	out := make([]groupDef, len(groups))
	for i, g := range groups {
		g.serverEnv = env
		out[i] = g
	}
	return out
}

// r1bDCategoryGroups characterise the category pricing engine routes. They
// only answer when the server runs with CATEGORY_PRICING_ENABLED=true, which
// the main harness server does not set (the flag off is what the other
// goldens record), so r1bDGroups runs them against a server of their own.
func r1bDCategoryGroups() []groupDef {
	return []groupDef{
		{
			name: "pricing_categories",
			steps: []stepDef{
				{name: "pricing.categories.list_tree", method: "GET", path: "/api/v1/pricing/categories"},
				{name: "pricing.categories.list_flat", method: "GET", path: "/api/v1/pricing/categories?view=flat"},
				{
					name:   "pricing.categories.create",
					method: "POST",
					path:   "/api/v1/pricing/categories",
					body: map[string]any{
						"name": "Golden D Root", "slug": "golden-d-root", "path": "golden_d_root",
						"sort_order": 7, "is_active": true,
					},
					extract: map[string]string{"d_catRoot": "/id"},
				},
				{
					name:   "pricing.categories.create_child",
					method: "POST",
					path:   "/api/v1/pricing/categories",
					body: map[string]any{
						"name": "Golden D Child", "slug": "golden-d-child", "path": "golden_d_root.golden_d_child",
						"parent_id": "{d_catRoot}", "sort_order": 1, "is_active": true,
					},
					extract: map[string]string{"d_catChild": "/id"},
				},
				{name: "pricing.categories.create.missing_fields", method: "POST",
					path: "/api/v1/pricing/categories", body: map[string]any{"name": "No Slug"}},
				{name: "pricing.categories.create.bad_body", method: "POST",
					path: "/api/v1/pricing/categories", body: "not-an-object"},
				{
					name:   "pricing.categories.update",
					method: "PUT",
					path:   "/api/v1/pricing/categories/{d_catChild}",
					body: map[string]any{
						"name": "Golden D Child Renamed", "slug": "golden-d-child", "path": "golden_d_root.golden_d_child",
						"parent_id": "{d_catRoot}", "sort_order": 2, "is_active": true,
					},
				},
				{name: "pricing.categories.create.duplicate_slug", method: "POST",
					path: "/api/v1/pricing/categories",
					body: map[string]any{"name": "Golden D Root Again", "slug": "golden-d-root", "path": "golden_d_root_again"}},
				{name: "pricing.categories.update.not_found", method: "PUT",
					path: "/api/v1/pricing/categories/" + zeroUUID,
					body: map[string]any{"name": "Ghost", "slug": "golden-d-ghost", "path": "golden_d_ghost"}},
				{name: "pricing.categories.update.bad_id", method: "PUT",
					path: "/api/v1/pricing/categories/not-a-uuid", body: map[string]any{"name": "x"}},
				{name: "pricing.categories.list_tree_after", method: "GET", path: "/api/v1/pricing/categories"},
			},
		},
		{
			name: "pricing_category_rules",
			steps: []stepDef{
				{
					name:   "pricing.category_rules.setup_category",
					method: "POST",
					path:   "/api/v1/pricing/categories",
					body: map[string]any{
						"name": "Golden D Rules Cat", "slug": "golden-d-rules-cat", "path": "golden_d_rules_cat",
						"sort_order": 8, "is_active": true,
					},
					extract: map[string]string{"d_ruleCat": "/id"},
				},
				{name: "pricing.category_rules.list_before", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}"},
				{
					name:   "pricing.category_rules.create_tier",
					method: "POST",
					path:   "/api/v1/pricing/category-rules",
					body: map[string]any{
						"target_type": "tier", "tier": "CONTRACTOR", "category_id": "{d_ruleCat}",
						"rule_type": "markup", "value_pct": "12.5", "margin_floor_pct": "5",
						"is_active": true, "priority": 3,
					},
					extract: map[string]string{"d_rule": "/id"},
				},
				{
					name:   "pricing.category_rules.create_account",
					method: "POST",
					path:   "/api/v1/pricing/category-rules",
					body: map[string]any{
						"target_type": "account", "customer_id": "{myCustomer}", "category_id": "{d_ruleCat}",
						"rule_type": "markdown", "value_pct": "4", "is_active": true, "priority": 1,
					},
					extract: map[string]string{"d_ruleAccount": "/id"},
				},
				{
					name:   "pricing.category_rules.create_duplicate",
					method: "POST",
					path:   "/api/v1/pricing/category-rules",
					body: map[string]any{
						"target_type": "tier", "tier": "CONTRACTOR", "category_id": "{d_ruleCat}",
						"rule_type": "markup", "value_pct": "20", "is_active": true, "priority": 3,
					},
				},
				{name: "pricing.category_rules.create.bad_target", method: "POST",
					path: "/api/v1/pricing/category-rules",
					body: map[string]any{"target_type": "nope", "category_id": "{d_ruleCat}", "rule_type": "markup"}},
				{name: "pricing.category_rules.create.account_without_customer", method: "POST",
					path: "/api/v1/pricing/category-rules",
					body: map[string]any{"target_type": "account", "category_id": "{d_ruleCat}", "rule_type": "markup"}},
				{name: "pricing.category_rules.create.bad_rule_type", method: "POST",
					path: "/api/v1/pricing/category-rules",
					body: map[string]any{"target_type": "tier", "tier": "RETAIL", "category_id": "{d_ruleCat}", "rule_type": "nope"}},
				{name: "pricing.category_rules.list", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}", sortPrimaryArray: true},
				{name: "pricing.category_rules.list_by_tier", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}&target_type=tier&tier=CONTRACTOR"},
				{name: "pricing.category_rules.list_paginated", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}&limit=1&include=total"},
				{name: "pricing.category_rules.list.unsupported_parameter", method: "GET",
					path: "/api/v1/pricing/category-rules?offset=0"},
				{name: "pricing.category_rules.list.bad_target_type", method: "GET",
					path: "/api/v1/pricing/category-rules?target_type=TIER"},
				{name: "pricing.category_rules.update.without_revision", method: "PUT",
					path: "/api/v1/pricing/category-rules/{d_rule}",
					body: map[string]any{"rule_type": "markup", "value_pct": "15"}},
				{name: "pricing.category_rules.update.target_refused", method: "PUT",
					path:    "/api/v1/pricing/category-rules/{d_rule}",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"rule_type": "markup", "value_pct": "15", "tier": "OTHER"}},
				{
					name:    "pricing.category_rules.update",
					method:  "PUT",
					path:    "/api/v1/pricing/category-rules/{d_rule}",
					headers: map[string]string{"If-Match": `"1"`},
					body: map[string]any{
						"rule_type": "markup", "value_pct": "15", "margin_floor_pct": "6",
						"is_active": true, "priority": 3,
					},
				},
				{name: "pricing.category_rules.update.stale", method: "PUT",
					path:    "/api/v1/pricing/category-rules/{d_rule}",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"rule_type": "markup", "value_pct": "16"}},
				{name: "pricing.category_rules.update.not_found", method: "PUT",
					path:    "/api/v1/pricing/category-rules/" + zeroUUID,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"rule_type": "markup", "value_pct": "1"}},
				{name: "pricing.category_rules.update.bad_id", method: "PUT",
					path: "/api/v1/pricing/category-rules/not-a-uuid", body: map[string]any{}},
				{name: "pricing.category_rules.audit", method: "GET",
					path: "/api/v1/pricing/category-rules/{d_rule}/audit"},
				{name: "pricing.category_rules.audit.bad_id", method: "GET",
					path: "/api/v1/pricing/category-rules/not-a-uuid/audit"},
				{
					name:   "pricing.category_rules.bulk_upsert",
					method: "POST",
					path:   "/api/v1/pricing/category-rules/bulk",
					body: []map[string]any{
						{
							"target_type": "tier", "tier": "WHOLESALE", "category_id": "{d_ruleCat}",
							"rule_type": "margin", "value_pct": "22", "is_active": true, "priority": 2,
						},
						{
							"target_type": "tier", "tier": "RETAIL", "category_id": "{d_ruleCat}",
							"rule_type": "fixed", "value_ten_thousandths": 99900, "is_active": true, "priority": 2,
						},
					},
				},
				{name: "pricing.category_rules.bulk_upsert.empty", method: "POST",
					path: "/api/v1/pricing/category-rules/bulk", body: []map[string]any{}},
				{name: "pricing.category_rules.bulk_upsert.invalid_rule", method: "POST",
					path: "/api/v1/pricing/category-rules/bulk",
					body: []map[string]any{{"target_type": "tier", "category_id": "{d_ruleCat}", "rule_type": "markup"}}},
				{name: "pricing.category_rules.list_after_bulk", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}", sortPrimaryArray: true},
				{name: "pricing.matrix", method: "GET", path: "/api/v1/pricing/matrix"},
				{name: "pricing.resolve", method: "GET",
					path: "/api/v1/pricing/resolve?product_id={product}&customer_id={myCustomer}"},
				{name: "pricing.resolve.tier", method: "GET",
					path: "/api/v1/pricing/resolve?product_id={product}&tier=CONTRACTOR"},
				{name: "pricing.resolve.missing_product", method: "GET", path: "/api/v1/pricing/resolve"},
				{name: "pricing.resolve.bad_product", method: "GET", path: "/api/v1/pricing/resolve?product_id=not-a-uuid"},
				{name: "pricing.category_rules.delete.without_revision", method: "DELETE",
					path: "/api/v1/pricing/category-rules/{d_ruleAccount}"},
				{name: "pricing.category_rules.delete", method: "DELETE",
					path: "/api/v1/pricing/category-rules/{d_ruleAccount}", headers: map[string]string{"If-Match": `"1"`}},
				{name: "pricing.category_rules.delete.bad_id", method: "DELETE",
					path: "/api/v1/pricing/category-rules/not-a-uuid"},
				{name: "pricing.category_rules.delete.unknown", method: "DELETE",
					path: "/api/v1/pricing/category-rules/" + zeroUUID},
				{name: "pricing.category_rules.bulk_delete", method: "DELETE",
					path: "/api/v1/pricing/category-rules/bulk", body: map[string]any{"ids": []any{"{d_rule}"}}},
				{name: "pricing.category_rules.bulk_delete.empty", method: "DELETE",
					path: "/api/v1/pricing/category-rules/bulk", body: map[string]any{"ids": []any{}}},
				{name: "pricing.category_rules.list_final", method: "GET",
					path: "/api/v1/pricing/category-rules?category_id={d_ruleCat}", sortPrimaryArray: true},
			},
		},
	}
}
