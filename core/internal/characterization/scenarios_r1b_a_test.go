// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"fmt"
	"time"
)

// R1-1b batch A: the per-id and secondary routes of customer, CRM, sales team,
// location, product, tax, deposit, payment, document, quote, inventory, AP,
// GL, matching, bank reconciliation and EDI. Each group runs after every
// earlier group (including the clock-window group), so it depends only on the
// seed variables and on variables extracted earlier in the script. Every
// destructive step works on a fixture the group created itself (variables are
// prefixed a_), never on a shared fixture another group reads.

const (
	r1bARepHeather = "a1b2c3d4-0001-4000-8000-000000000001"
	r1bAMissingID  = "00000000-0000-0000-0000-0000000000aa"
)

func r1bAGroups() []groupDef {
	return concat(
		r1bACustomerContactsGroups(),
		customerShipToTermsGroups(),
		r1bASalesTeamGroups(),
		r1bACRMGroups(),
		r1bALocationGroups(),
		r1bAProductGroups(),
		r1bATaxGroups(),
		r1bADepositGroups(),
		r1bAPaymentGroups(),
		r1bADocumentGroups(),
		r1bAQuoteFileGroups(),
		r1bAInventoryGroups(),
		r1bAAPGroups(),
		r1bAGLGroups(),
		r1bAMatchingGroups(),
		r1bABankreconGroups(),
		r1bAEDIGroups(),
	)
}

func r1bACustomerContactsGroups() []groupDef {
	if0 := func(rev string) map[string]string { return map[string]string{"If-Match": `"` + rev + `"`} }
	policy := "/api/v1/customers/{a_customer}/escalation-policy"
	// A contact PUT carries the controls it must not reset by leaving them out.
	contactPut := func(extra map[string]any) map[string]any {
		b := map[string]any{"first_name": "Golden", "last_name": "Contact", "role": "Buyer", "can_place_orders": true, "order_limit_cents": nil}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	contact := func(extra map[string]any) map[string]any {
		b := map[string]any{"first_name": "Golden", "last_name": "Contact", "role": "Buyer"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	return []groupDef{{
		name: "customer_contacts",
		steps: []stepDef{
			{
				name:   "customer.a.create",
				method: "POST",
				path:   "/api/v1/customers",
				body: map[string]any{
					"name": "Golden Contacts Co", "account_number": "GOLD-A-001",
					"email": "contacts@example.com", "phone": "250-555-0177",
					"address": "7 Golden Way, Kelowna BC", "tier": "gold",
					"credit_limit_cents": 5000000, "primary_branch_id": "{branch}",
				},
				extract: map[string]string{"a_customer": "/id"},
			},
			{name: "contact.list_empty", method: "GET", path: "/api/v1/customers/{a_customer}/contacts"},
			{
				name:   "contact.create",
				method: "POST",
				path:   "/api/v1/customers/{a_customer}/contacts",
				body: contact(map[string]any{
					"title": "Purchasing", "email": "golden.contact@example.com", "phone": "250-555-0155",
					"is_primary": true, "is_active": true, "can_place_orders": true, "order_limit_cents": 250000,
				}),
				extract: map[string]string{"a_contact": "/id"},
			},
			{
				name:   "contact.create.second",
				method: "POST",
				path:   "/api/v1/customers/{a_customer}/contacts",
				body: map[string]any{
					"first_name": "Golden", "last_name": "Ledger", "role": "AP", "is_active": true, "can_place_orders": false,
				},
				extract: map[string]string{"a_contact2": "/id"},
			},
			{name: "contact.create.bad_customer_id", method: "POST", path: "/api/v1/customers/not-a-uuid/contacts",
				body: contact(nil)},
			{name: "contact.create.bad_body", method: "POST", path: "/api/v1/customers/{a_customer}/contacts",
				body: "not-an-object"},
			{name: "contact.create.invalid", method: "POST", path: "/api/v1/customers/{a_customer}/contacts",
				body: map[string]any{"role": "Wizard", "order_limit_cents": -5}},
			{name: "contact.create.unknown_customer", method: "POST", path: "/api/v1/customers/" + r1bAMissingID + "/contacts",
				body: contact(nil)},
			{name: "contact.list", method: "GET", path: "/api/v1/customers/{a_customer}/contacts?include=total"},
			{name: "contact.list.unsupported_parameter", method: "GET", path: "/api/v1/customers/{a_customer}/contacts?offset=0"},
			{name: "contact.list.bad_customer_id", method: "GET", path: "/api/v1/customers/not-a-uuid/contacts"},
			{name: "contact.get", method: "GET", path: "/api/v1/contacts/{a_contact}"},
			{name: "contact.get.not_found", method: "GET", path: "/api/v1/contacts/" + r1bAMissingID},
			{name: "contact.get.bad_id", method: "GET", path: "/api/v1/contacts/not-a-uuid"},
			{name: "contact.update.without_revision", method: "PUT", path: "/api/v1/contacts/{a_contact}", body: contactPut(nil)},
			{
				name:    "contact.update",
				method:  "PUT",
				path:    "/api/v1/contacts/{a_contact}",
				headers: if0("1"),
				body: contactPut(map[string]any{
					"last_name": "Contact-Updated", "title": "Senior Purchasing",
					"email": "golden.contact.updated@example.com", "role": "Owner", "is_primary": true,
					"is_active": true, "can_place_orders": true, "order_limit_cents": 500000,
				}),
			},
			{name: "contact.update.stale", method: "PUT", path: "/api/v1/contacts/{a_contact}", headers: if0("1"), body: contactPut(nil)},
			{name: "contact.update.missing_controls", method: "PUT", path: "/api/v1/contacts/{a_contact}", headers: if0("2"), body: map[string]any{"first_name": "Golden", "last_name": "Contact"}},
			{name: "contact.get.after_update", method: "GET", path: "/api/v1/contacts/{a_contact}"},
			{name: "contact.update.bad_body", method: "PUT", path: "/api/v1/contacts/{a_contact}", headers: if0("2"), body: "not-an-object"},
			{name: "contact.update.bad_id", method: "PUT", path: "/api/v1/contacts/not-a-uuid", headers: if0("1"),
				body: contactPut(map[string]any{"first_name": "X"})},
			{name: "contact.update.not_found", method: "PUT", path: "/api/v1/contacts/" + r1bAMissingID, headers: if0("1"),
				body: contactPut(map[string]any{"first_name": "Nobody", "last_name": "Here"})},

			// Escalation policy, on the customer's revision (create 1, one per write).
			{name: "escalation_policy.get_default", method: "GET", path: policy},
			{name: "escalation_policy.get.not_found", method: "GET", path: "/api/v1/customers/" + r1bAMissingID + "/escalation-policy"},
			{name: "escalation_policy.get.bad_id", method: "GET", path: "/api/v1/customers/not-a-uuid/escalation-policy"},
			{name: "escalation_policy.set.without_revision", method: "PUT", path: policy,
				body: map[string]any{"policy": "flag_for_requote", "threshold_percent": "7.5"}},
			{
				name:    "escalation_policy.set",
				method:  "PUT",
				path:    policy,
				headers: if0("1"),
				body:    map[string]any{"policy": "flag_for_requote", "threshold_percent": "7.5"},
			},
			{
				name:    "escalation_policy.set.auto_escalate_signed",
				method:  "PUT",
				path:    policy,
				headers: if0("2"),
				body: map[string]any{
					"policy": "auto_escalate", "threshold_percent": "5",
					"agreement_signed_at": "{today}T09:00:00Z", "agreement_ref": "GOLD-AGREEMENT-1",
				},
			},
			{name: "escalation_policy.get", method: "GET", path: policy},
			{name: "escalation_policy.set.stale", method: "PUT", path: policy, headers: if0("1"),
				body: map[string]any{"policy": "require_ack", "threshold_percent": "5"}},
			{name: "escalation_policy.set.agreement_required", method: "PUT", path: policy, headers: if0("3"),
				body: map[string]any{"policy": "auto_escalate", "threshold_percent": "5"}},
			{name: "escalation_policy.set.invalid_mode", method: "PUT", path: policy, headers: if0("3"),
				body: map[string]any{"policy": "SHRUG", "threshold_percent": "5"}},
			{name: "escalation_policy.set.threshold_out_of_range", method: "PUT", path: policy, headers: if0("3"),
				body: map[string]any{"policy": "require_ack", "threshold_percent": "80"}},
			{name: "escalation_policy.set.bad_body", method: "PUT", path: policy, headers: if0("3"), body: "not-an-object"},

			// Salesperson assignment, on the revision (3 after the policy writes).
			{name: "customer.salesperson.without_revision", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				body: map[string]any{"salesperson_id": r1bARepHeather}},
			{name: "customer.salesperson.assign", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				headers: if0("3"), body: map[string]any{"salesperson_id": r1bARepHeather}},
			{name: "customer.salesperson.clear", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				headers: if0("4"), body: map[string]any{"salesperson_id": nil}},
			{name: "customer.salesperson.missing_key", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				headers: if0("5"), body: map[string]any{}},
			{name: "customer.salesperson.bad_body", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				headers: if0("5"), body: map[string]any{"salesperson_id": "not-a-uuid"}},
			{name: "customer.salesperson.bad_id", method: "PATCH", path: "/api/v1/customers/not-a-uuid/salesperson",
				headers: if0("5"), body: map[string]any{"salesperson_id": r1bARepHeather}},
			{name: "customer.salesperson.unknown_rep", method: "PATCH", path: "/api/v1/customers/{a_customer}/salesperson",
				headers: if0("5"), body: map[string]any{"salesperson_id": r1bAMissingID}},

			// Delete last: the second contact stays to prove the list.
			{name: "contact.delete.without_revision", method: "DELETE", path: "/api/v1/contacts/{a_contact}"},
			{name: "contact.delete.stale", method: "DELETE", path: "/api/v1/contacts/{a_contact}", headers: if0("1")},
			{name: "contact.delete", method: "DELETE", path: "/api/v1/contacts/{a_contact}", headers: if0("2")},
			{name: "contact.delete.again", method: "DELETE", path: "/api/v1/contacts/{a_contact}", headers: if0("2")},
			{name: "contact.delete.bad_id", method: "DELETE", path: "/api/v1/contacts/not-a-uuid", headers: if0("1")},
			{name: "contact.get.after_delete", method: "GET", path: "/api/v1/contacts/{a_contact}"},
			{name: "contact.list.after_delete", method: "GET", path: "/api/v1/customers/{a_customer}/contacts"},
		},
	}}
}

func r1bASalesTeamGroups() []groupDef {
	return []groupDef{{
		name: "sales_team_detail",
		steps: []stepDef{
			{name: "sales_team.get", method: "GET", path: "/api/v1/sales-team/" + r1bARepHeather},
			{name: "sales_team.get.not_found", method: "GET", path: "/api/v1/sales-team/" + r1bAMissingID},
			{name: "sales_team.get.bad_id", method: "GET", path: "/api/v1/sales-team/not-a-uuid"},
		},
	}}
}

func r1bACRMGroups() []groupDef {
	return []groupDef{{
		name: "crm_activity_item",
		steps: []stepDef{
			{
				name:   "crm.a.activity.create",
				method: "POST",
				path:   "/api/v1/customers/{myCustomer}/activities",
				body: map[string]any{
					"activity_type": "call", "description": "golden item call",
					"activity_date": "{today}T09:00:00Z",
				},
				extract:        map[string]string{"a_activity": "/id"},
				captureHeaders: []string{"ETag"},
			},
			{name: "crm.activity.get", method: "GET", path: "/api/v1/activities/{a_activity}", captureHeaders: []string{"ETag"}},
			{name: "crm.activity.get.not_found", method: "GET", path: "/api/v1/activities/" + r1bAMissingID},
			{name: "crm.activity.get.bad_id", method: "GET", path: "/api/v1/activities/not-a-uuid"},
			// A write with no precondition is 428 (ADR 0001 section 11).
			{
				name:   "crm.activity.update.no_precondition",
				method: "PUT", path: "/api/v1/activities/{a_activity}",
				body: map[string]any{"activity_type": "note", "description": "no revision"},
			},
			{
				name:    "crm.activity.update",
				method:  "PUT",
				path:    "/api/v1/activities/{a_activity}",
				headers: map[string]string{"If-Match": "\"1\""},
				body: map[string]any{
					"activity_type": "meeting",
					"description":   "golden item meeting", "activity_date": "{today+1}T10:30:00Z",
				},
			},
			{name: "crm.activity.get.after_update", method: "GET", path: "/api/v1/activities/{a_activity}", captureHeaders: []string{"ETag"}},
			{name: "crm.activity.update.bad_type", method: "PUT", path: "/api/v1/activities/{a_activity}",
				headers: map[string]string{"If-Match": "\"2\""},
				body:    map[string]any{"activity_type": "SMOKE_SIGNAL", "description": "x"}},
			{name: "crm.activity.update.bad_body", method: "PUT", path: "/api/v1/activities/{a_activity}",
				headers: map[string]string{"If-Match": "\"2\""}, body: "not-an-object"},
			{name: "crm.activity.update.bad_id", method: "PUT", path: "/api/v1/activities/not-a-uuid",
				headers: map[string]string{"If-Match": "\"2\""},
				body:    map[string]any{"activity_type": "note", "description": "x"}},
			{name: "crm.activity.update.not_found", method: "PUT", path: "/api/v1/activities/" + r1bAMissingID,
				headers: map[string]string{"If-Match": "\"1\""},
				body: map[string]any{
					"activity_type": "note", "description": "ghost",
					"activity_date": "{today}T09:00:00Z",
				}},
			// The delete takes the same precondition, by If-Match alone.
			{name: "crm.activity.delete.no_precondition", method: "DELETE", path: "/api/v1/activities/{a_activity}"},
			{name: "crm.activity.delete", method: "DELETE", path: "/api/v1/activities/{a_activity}",
				headers: map[string]string{"If-Match": "\"2\""}},
			{name: "crm.activity.delete.again", method: "DELETE", path: "/api/v1/activities/{a_activity}",
				headers: map[string]string{"If-Match": "\"3\""}},
			{name: "crm.activity.delete.bad_id", method: "DELETE", path: "/api/v1/activities/not-a-uuid"},
			{name: "crm.activity.get.after_delete", method: "GET", path: "/api/v1/activities/{a_activity}"},
			// The list: the envelope, a filter that filters, an unknown
			// parameter refused (the strict query posture).
			{name: "crm.activity.list", method: "GET",
				path: "/api/v1/customers/{myCustomer}/activities?limit=2"},
			{name: "crm.activity.list.filter", method: "GET",
				path: "/api/v1/customers/{myCustomer}/activities?activity_type=call"},
			{name: "crm.activity.list.unknown_param", method: "GET",
				path: "/api/v1/customers/{myCustomer}/activities?zzz=1"},
			// The module's events, read back from the feed (ADR 0003).
			{name: "crm.activity.events", method: "GET",
				path: "/api/v1/events?types=activity.created,activity.updated,activity.deleted&limit=3", sortPrimaryArray: true, sortEnvelopeItems: true},
		},
	}}
}

func r1bALocationGroups() []groupDef {
	return []groupDef{{
		name: "location_item",
		steps: []stepDef{
			{
				name:   "location.a.yard.create",
				method: "POST",
				path:   "/api/v1/locations",
				body: map[string]any{
					"code": "GOLD-A-YARD", "type": "yard", "name": "Golden A Yard",
					"parent_id": "{branch}", "description": "item fixture",
				},
				extract: map[string]string{"a_yard": "/id"},
			},
			{
				name:   "location.a.bin.create",
				method: "POST",
				path:   "/api/v1/locations",
				body: map[string]any{
					"code": "GOLD-A-BIN", "type": "bin", "name": "Golden A Bin",
					"parent_id": "{a_yard}",
				},
				extract: map[string]string{"a_bin": "/id"},
			},
			{name: "branch.tree", method: "GET", path: "/api/v1/branches/{branch}/tree", sortPrimaryArray: true, sortEnvelopeItems: true},
			{name: "branch.tree.empty_branch", method: "GET", path: "/api/v1/branches/" + r1bAMissingID + "/tree"},
			{name: "branch.tree.bad_id", method: "GET", path: "/api/v1/branches/not-a-uuid/tree"},
			{name: "location.update.without_revision", method: "PUT", path: "/api/v1/locations/{a_yard}",
				body: map[string]any{"code": "GOLD-A-YARD", "name": "Golden A Yard Renamed", "active": true}},
			// The type and parent are fixed at create; a body that names one is refused.
			{name: "location.update.type_refused", method: "PUT", path: "/api/v1/locations/{a_yard}",
				headers: map[string]string{"If-Match": `"1"`},
				body:    map[string]any{"code": "GOLD-A-YARD", "type": "yard", "name": "Golden A Yard Renamed"}},
			{
				name:    "location.update",
				method:  "PUT",
				path:    "/api/v1/locations/{a_yard}",
				headers: map[string]string{"If-Match": `"1"`},
				body: map[string]any{
					"code": "GOLD-A-YARD", "name": "Golden A Yard Renamed",
					"description": "renamed", "active": true,
				},
			},
			{name: "location.update.stale", method: "PUT", path: "/api/v1/locations/{a_yard}",
				headers: map[string]string{"If-Match": `"1"`},
				body:    map[string]any{"code": "GOLD-A-YARD", "name": "Golden A Yard Stale"}},
			{name: "location.get.after_update", method: "GET", path: "/api/v1/locations/{a_yard}"},
			{name: "location.update.bad_body", method: "PUT", path: "/api/v1/locations/{a_yard}", body: "not-an-object"},
			{name: "location.update.bad_id", method: "PUT", path: "/api/v1/locations/not-a-uuid",
				body: map[string]any{"code": "X"}},
			{name: "location.update.not_found", method: "PUT", path: "/api/v1/locations/" + r1bAMissingID,
				body: map[string]any{"code": "GHOST", "name": "Ghost", "active": true}},
			{name: "location.delete.without_revision", method: "DELETE", path: "/api/v1/locations/{a_bin}"},
			{name: "location.delete", method: "DELETE", path: "/api/v1/locations/{a_bin}",
				headers: map[string]string{"If-Match": `"1"`}},
			{name: "location.get.after_delete", method: "GET", path: "/api/v1/locations/{a_bin}"},
			{name: "location.delete.again", method: "DELETE", path: "/api/v1/locations/{a_bin}",
				headers: map[string]string{"If-Match": `"2"`}},
			{name: "location.delete.not_found", method: "DELETE", path: "/api/v1/locations/" + r1bAMissingID},
			{name: "location.delete.bad_id", method: "DELETE", path: "/api/v1/locations/not-a-uuid"},
		},
	}}
}

func r1bAProductGroups() []groupDef {
	return []groupDef{{
		name: "product_item",
		steps: []stepDef{
			{
				name:   "product.a.create",
				method: "POST",
				path:   "/api/v1/products",
				body: map[string]any{
					"sku": "GOLD-A-STUD", "description": "Golden item stud", "stock_uom": "PCS",
					"base_price_ten_thousandths": 37500, "reorder_point": "5000", "reorder_qty": "100",
				},
				extract: map[string]string{"a_product": "/id"},
			},
			{name: "product.reorder_alerts", method: "GET", path: "/api/v1/products/reorder-alerts", sortPrimaryArray: true, sortEnvelopeItems: true},
			// Every write names the revision it read: create is 1, and each
			// write below moves it by one.
			{name: "product.dimensions.without_revision", method: "PATCH", path: "/api/v1/products/{a_product}/dimensions",
				body: map[string]any{"length_in": 96}},
			{
				name:    "product.dimensions.set",
				method:  "PATCH",
				path:    "/api/v1/products/{a_product}/dimensions",
				headers: map[string]string{"If-Match": `"1"`},
				body:    map[string]any{"length_in": 96, "width_in": 3.5, "height_in": 1.5, "stackable": true},
			},
			{name: "product.dimensions.stale", method: "PATCH", path: "/api/v1/products/{a_product}/dimensions",
				headers: map[string]string{"If-Match": `"1"`}, body: map[string]any{"length_in": 97}},
			{
				name:    "product.dimensions.set_source",
				method:  "PATCH",
				path:    "/api/v1/products/{a_product}/dimensions",
				headers: map[string]string{"If-Match": `"2"`},
				body:    map[string]any{"length_in": 96, "geometry_source": "mesh"},
			},
			{name: "product.dimensions.clear", method: "PATCH", path: "/api/v1/products/{a_product}/dimensions",
				headers: map[string]string{"If-Match": `"3"`}, body: map[string]any{}},
			{name: "product.dimensions.bad_body", method: "PATCH", path: "/api/v1/products/{a_product}/dimensions",
				body: "not-an-object"},
			{name: "product.dimensions.bad_id", method: "PATCH", path: "/api/v1/products/not-a-uuid/dimensions",
				body: map[string]any{"length_in": 1}},
			{name: "product.dimensions.not_found", method: "PATCH", path: "/api/v1/products/" + r1bAMissingID + "/dimensions",
				body: map[string]any{"length_in": 1}},
			{name: "product.lead_time.set", method: "PATCH", path: "/api/v1/products/{a_product}/lead-time",
				headers: map[string]string{"If-Match": `"4"`}, body: map[string]any{"lead_time_days": 9}},
			{name: "product.lead_time.clear", method: "PATCH", path: "/api/v1/products/{a_product}/lead-time",
				headers: map[string]string{"If-Match": `"5"`}, body: map[string]any{"lead_time_days": nil}},
			{name: "product.lead_time.negative", method: "PATCH", path: "/api/v1/products/{a_product}/lead-time",
				headers: map[string]string{"If-Match": `"6"`}, body: map[string]any{"lead_time_days": -3}},
			{name: "product.lead_time.bad_body", method: "PATCH", path: "/api/v1/products/{a_product}/lead-time",
				body: "not-an-object"},
			{name: "product.lead_time.bad_id", method: "PATCH", path: "/api/v1/products/not-a-uuid/lead-time",
				body: map[string]any{"lead_time_days": 1}},
			{name: "product.margins.set", method: "PATCH", path: "/api/v1/products/{a_product}/margins",
				headers: map[string]string{"If-Match": `"6"`}, body: map[string]any{"target_margin": 22.5, "commission_rate": 3.25}},
			{name: "product.margins.bad_body", method: "PATCH", path: "/api/v1/products/{a_product}/margins",
				body: "not-an-object"},
			{name: "product.margins.bad_id", method: "PATCH", path: "/api/v1/products/not-a-uuid/margins",
				body: map[string]any{"target_margin": 1}},
			{name: "product.margins.not_found", method: "PATCH", path: "/api/v1/products/" + r1bAMissingID + "/margins",
				body: map[string]any{"target_margin": 1, "commission_rate": 1}},
			{name: "product.get.after_patches", method: "GET", path: "/api/v1/products/{a_product}"},
		},
	}}
}

func r1bATaxGroups() []groupDef {
	return []groupDef{{
		name: "tax_exemption_item",
		steps: []stepDef{
			{
				name:   "tax.a.exemption.create",
				method: "POST",
				path:   "/api/v1/tax/exemptions",
				body: map[string]any{
					"customer_id": "{a_customer}", "exempt_reason": "RESALE",
					"certificate_number": "GOLD-CERT-A1", "issuing_state": "BC",
				},
				extract: map[string]string{"a_exemption": "/id"},
			},
			{name: "tax.exemption.list_before_delete", method: "GET", path: "/api/v1/tax/exemptions/{a_customer}"},
			{name: "tax.exemption.delete", method: "DELETE", path: "/api/v1/tax/exemptions/{a_exemption}"},
			{name: "tax.exemption.list_after_delete", method: "GET", path: "/api/v1/tax/exemptions/{a_customer}"},
			{name: "tax.exemption.delete.again", method: "DELETE", path: "/api/v1/tax/exemptions/{a_exemption}"},
			{name: "tax.exemption.delete.not_found", method: "DELETE", path: "/api/v1/tax/exemptions/" + r1bAMissingID},
			{name: "tax.exemption.delete.bad_id", method: "DELETE", path: "/api/v1/tax/exemptions/not-a-uuid"},
		},
	}}
}

func r1bADepositGroups() []groupDef {
	json := func(rev string) map[string]string { return map[string]string{"If-Match": `"` + rev + `"`} }
	return []groupDef{{
		name: "payment_unapplied",
		steps: []stepDef{
			// Cash held for a customer other than the invoice's: unapplied cash
			// for a_customer, whose application to another customer's invoice is
			// refused, then applied nowhere and voided.
			{
				name:   "payment.a.create",
				method: "POST",
				path:   "/api/v1/payments",
				body: map[string]any{
					"customer_id": "{a_customer}", "branch_id": "{branch}", "amount_cents": 10000,
					"method": "check", "reference": "GOLD-PAY-A1",
				},
				extract: map[string]string{"a_payment": "/id"},
			},
			{name: "payment.a.list", method: "GET", path: "/api/v1/payments?customer_id={a_customer}"},
			{name: "payment.a.list.bad_customer", method: "GET", path: "/api/v1/payments?customer_id=not-a-uuid"},
			{name: "payment.a.apply.customer_mismatch", method: "POST", path: "/api/v1/payments/{a_payment}/applications", headers: json("1"),
				body: map[string]any{"applications": []map[string]any{{"invoice_id": "{myInvoice}", "amount_cents": 100}}}},
			{name: "payment.a.apply.unknown_invoice", method: "POST", path: "/api/v1/payments/{a_payment}/applications", headers: json("1"),
				body: map[string]any{"applications": []map[string]any{{"invoice_id": "00000000-0000-0000-0000-0000000000aa", "amount_cents": 100}}}},
			{name: "payment.a.apply.empty", method: "POST", path: "/api/v1/payments/{a_payment}/applications", headers: json("1"),
				body: map[string]any{"applications": []map[string]any{}}},
			{name: "payment.a.apply.bad_body", method: "POST", path: "/api/v1/payments/{a_payment}/applications", headers: json("1"), body: "not-an-object"},
			{name: "payment.a.apply.bad_id", method: "POST", path: "/api/v1/payments/not-a-uuid/applications", headers: json("1"),
				body: map[string]any{"applications": []map[string]any{{"invoice_id": "{myInvoice}", "amount_cents": 100}}}},
			{name: "payment.a.apply.not_found", method: "POST", path: "/api/v1/payments/" + r1bAMissingID + "/applications", headers: json("1"),
				body: map[string]any{"applications": []map[string]any{{"invoice_id": "{myInvoice}", "amount_cents": 100}}}},
			{name: "payment.a.void", method: "POST", path: "/api/v1/payments/{a_payment}/transitions", headers: json("1"),
				body: map[string]any{"to": "voided", "reason": "golden void of unapplied cash"}},
			{name: "payment.a.list.after_void", method: "GET", path: "/api/v1/payments?customer_id={a_customer}"},
		},
	}}
}

func r1bAPaymentGroups() []groupDef {
	return []groupDef{{
		name: "payment_gateway",
		steps: []stepDef{
			// The harness configures no Run Payments gateway: a charge is
			// refused with 503 payment gateway not configured, and a refund of
			// a non card payment needs no gateway.
			{name: "payment.card.no_gateway", method: "POST", path: "/api/v1/payments/card",
				body: map[string]any{"customer_id": "{a_customer}", "token_id": "tok_golden", "amount_cents": 100}},
			{name: "payment.card.missing_token", method: "POST", path: "/api/v1/payments/card",
				body: map[string]any{"customer_id": "{a_customer}", "amount_cents": 100}},
			{name: "payment.card.bad_body", method: "POST", path: "/api/v1/payments/card", body: "not-an-object"},
			{name: "payment.legacy_refund_route_gone", method: "POST", path: "/api/v1/payments/refund",
				body: map[string]any{"payment_id": "{myPayment}", "amount": 100}},
		},
	}}
}

func r1bADocumentGroups() []groupDef {
	return []groupDef{{
		name: "document_invoice",
		steps: []stepDef{
			{name: "document.print_invoice", method: "GET", path: "/api/v1/documents/print/invoice/{myInvoice}"},
			{name: "document.print_invoice.not_found", method: "GET",
				path: "/api/v1/documents/print/invoice/" + r1bAMissingID},
			{name: "document.print_invoice.bad_id", method: "GET", path: "/api/v1/documents/print/invoice/not-a-uuid"},
			{name: "invoice.email", method: "POST", path: "/api/v1/invoices/{myInvoice}/email"},
			{name: "invoice.email.not_found", method: "POST", path: "/api/v1/invoices/" + r1bAMissingID + "/email"},
			{name: "invoice.email.bad_id", method: "POST", path: "/api/v1/invoices/not-a-uuid/email"},
		},
	}}
}

func r1bAQuoteFileGroups() []groupDef {
	return []groupDef{{
		name: "quote_file",
		steps: []stepDef{
			{
				name:   "quote.a.create_with_file",
				method: "POST",
				path:   "/api/v1/quotes",
				body: map[string]any{
					"branch_id": "{branch}", "customer_id": "{customer}", "delivery_type": "pickup",
					"original_file": "R29sZGVuIGZpbGU=", "original_filename": "golden-quote.txt",
					"lines": []map[string]any{{
						"product_id": "{product}", "sku": "LUM-248-PREM", "description": "2x4x8 SPF Premium",
						"quantity": "2", "uom": "PCS", "unit_price_ten_thousandths": 55000,
					}},
				},
				extract: map[string]string{"a_quote": "/id"},
			},
			{name: "quote.file.download", method: "GET", path: "/api/v1/quotes/{a_quote}/file"},
			{name: "quote.file.none_stored", method: "GET", path: "/api/v1/quotes/{myQuote}/file"},
			{name: "quote.file.not_found", method: "GET", path: "/api/v1/quotes/" + r1bAMissingID + "/file"},
			{name: "quote.file.bad_id", method: "GET", path: "/api/v1/quotes/not-a-uuid/file"},
		},
	}}
}

func r1bAInventoryGroups() []groupDef {
	return []groupDef{{
		name: "inventory_transfer",
		steps: []stepDef{
			{
				name:    "inventory.a.yard_from.create",
				method:  "POST",
				path:    "/api/v1/locations",
				body:    map[string]any{"code": "GOLD-T-FROM", "type": "yard", "name": "Golden Transfer From", "parent_id": "{branch}"},
				extract: map[string]string{"a_from": "/id"},
			},
			{
				name:    "inventory.a.yard_to.create",
				method:  "POST",
				path:    "/api/v1/locations",
				body:    map[string]any{"code": "GOLD-T-TO", "type": "yard", "name": "Golden Transfer To", "parent_id": "{branch}"},
				extract: map[string]string{"a_to": "/id"},
			},
			{name: "inventory.a.seed_stock", method: "POST", path: "/api/v1/inventory/adjust",
				body: map[string]any{
					"product_id": "{a_product}", "location_id": "{a_from}", "quantity": 30,
					"reason": "golden transfer stock", "is_delta": true,
				}},
			{name: "inventory.transfer", method: "POST", path: "/api/v1/inventory/transfer",
				body: map[string]any{
					"product_id": "{a_product}", "from_location_id": "{a_from}", "to_location_id": "{a_to}",
					"quantity": 12, "reason": "golden transfer",
				}},
			{name: "inventory.list_after_transfer", method: "GET", path: "/api/v1/inventory?product_id={a_product}"},
			{name: "inventory.list_include_product", method: "GET", path: "/api/v1/inventory?product_id={a_product}&include=product"},
			{name: "inventory.list_unknown_param", method: "GET", path: "/api/v1/inventory?product_id={a_product}&warehouse=x"},
			{name: "inventory.transfer.insufficient", method: "POST", path: "/api/v1/inventory/transfer",
				body: map[string]any{
					"product_id": "{a_product}", "from_location_id": "{a_from}", "to_location_id": "{a_to}",
					"quantity": 500, "reason": "too much",
				}},
			{name: "inventory.transfer.zero_quantity", method: "POST", path: "/api/v1/inventory/transfer",
				body: map[string]any{
					"product_id": "{a_product}", "from_location_id": "{a_from}", "to_location_id": "{a_to}",
					"quantity": 0,
				}},
			{name: "inventory.transfer.no_source_stock", method: "POST", path: "/api/v1/inventory/transfer",
				body: map[string]any{
					"product_id": "{productSheet}", "from_location_id": "{a_to}", "to_location_id": "{a_from}",
					"quantity": 1,
				}},
			{name: "inventory.transfer.unknown_destination", method: "POST", path: "/api/v1/inventory/transfer",
				body: map[string]any{
					"product_id": "{a_product}", "from_location_id": "{a_from}",
					"to_location_id": r1bAMissingID, "quantity": 1,
				}},
			{name: "inventory.transfer.bad_body", method: "POST", path: "/api/v1/inventory/transfer", body: "not-an-object"},
		},
	}}
}

func r1bAAPGroups() []groupDef {
	return []groupDef{{
		name: "ap_payments",
		steps: []stepDef{
			{
				name:   "ap.a.invoice.create",
				method: "POST",
				path:   "/api/v1/ap/invoices",
				body: map[string]any{
					"vendor_id": "{vendor}", "invoice_number": "GOLD-AP-A01",
					"invoice_date": "{today}", "due_date": "{today+30}", "tax_amount": 0,
					"lines": []map[string]any{
						{"description": "golden ap payable line", "quantity": 2, "unit_price": 50.0},
					},
				},
				extract: map[string]string{"a_apinvoice": "/id"},
			},
			// AUTH_MODE=dev attaches no claims, and the approve handler
			// requires an authenticated approver: 401 on every dev call.
			{name: "ap.invoice.approve.no_claims", method: "POST", path: "/api/v1/ap/invoices/{a_apinvoice}/approve"},
			{name: "ap.invoice.approve.bad_id", method: "POST", path: "/api/v1/ap/invoices/not-a-uuid/approve"},
			{name: "ap.payments.list_empty_for_vendor", method: "GET", path: "/api/v1/ap/payments?vendor_id={vendor}"},
			{
				name:   "ap.payment.create",
				method: "POST",
				path:   "/api/v1/ap/payments",
				body: map[string]any{
					"vendor_id": "{vendor}", "amount": 40.0, "method": "CHECK", "check_number": "GOLD-CHK-1",
					"reference": "golden partial", "payment_date": "{today}",
					"invoice_ids": []any{"{a_apinvoice}"},
				},
				extract: map[string]string{"a_appayment": "/id"},
			},
			{name: "ap.invoice.get.after_partial", method: "GET", path: "/api/v1/ap/invoices/{a_apinvoice}"},
			{
				name:   "ap.payment.create.settle",
				method: "POST",
				path:   "/api/v1/ap/payments",
				body: map[string]any{
					"vendor_id": "{vendor}", "amount": 60.0, "method": "ACH",
					"reference": "golden settle", "payment_date": "{today}",
					"invoice_ids": []any{"{a_apinvoice}"},
				},
			},
			{name: "ap.invoice.get.after_settle", method: "GET", path: "/api/v1/ap/invoices/{a_apinvoice}"},
			{name: "ap.payments.list_vendor", method: "GET", path: "/api/v1/ap/payments?vendor_id={vendor}", sortPrimaryArray: true, sortEnvelopeItems: true},
			{name: "ap.payment.create.bad_date", method: "POST", path: "/api/v1/ap/payments",
				body: map[string]any{
					"vendor_id": "{vendor}", "amount": 1.0, "method": "CHECK", "payment_date": "31/12/2000",
				}},
			{name: "ap.payment.create.unknown_invoice", method: "POST", path: "/api/v1/ap/payments",
				body: map[string]any{
					"vendor_id": "{vendor}", "amount": 1.0, "method": "CHECK", "payment_date": "{today}",
					"invoice_ids": []any{r1bAMissingID},
				}},
			{name: "ap.payment.create.bad_body", method: "POST", path: "/api/v1/ap/payments", body: "not-an-object"},
		},
	}}
}

func r1bAGLGroups() []groupDef {
	periodIdx := r1bACurrentPeriodIndex()
	entryBody := func(memo string, amount float64) map[string]any {
		return map[string]any{
			"memo": memo, "entry_date": "{today}",
			"lines": []map[string]any{
				{"account_id": "{glAccount}", "description": memo + " debit", "debit": amount, "credit": 0},
				{"account_id": "{glAccount2}", "description": memo + " credit", "debit": 0, "credit": amount},
			},
		}
	}
	return []groupDef{{
		name: "gl_accounts_entries",
		steps: []stepDef{
			{
				name:   "gl.account.create",
				method: "POST",
				path:   "/api/v1/gl/accounts",
				body: map[string]any{
					"code": "1990", "name": "Golden Clearing", "type": "ASSET", "subtype": "Other",
					"description": "golden characterisation account",
				},
				extract: map[string]string{"a_glaccount": "/id"},
			},
			{name: "gl.account.create.duplicate_code", method: "POST", path: "/api/v1/gl/accounts",
				body: map[string]any{"code": "1990", "name": "Golden Clearing Twin", "type": "ASSET"}},
			{name: "gl.account.create.invalid_type", method: "POST", path: "/api/v1/gl/accounts",
				body: map[string]any{"code": "1991", "name": "Golden Bad Type", "type": "PETTY"}},
			{name: "gl.account.create.missing_name", method: "POST", path: "/api/v1/gl/accounts",
				body: map[string]any{"code": "1992", "type": "ASSET"}},
			{name: "gl.account.create.bad_body", method: "POST", path: "/api/v1/gl/accounts", body: "not-an-object"},
			{
				name:   "gl.account.update",
				method: "PUT",
				path:   "/api/v1/gl/accounts/{a_glaccount}",
				body: map[string]any{
					"code": "1990", "name": "Golden Clearing Renamed", "type": "ASSET", "subtype": "Other",
					"normal_balance": "DEBIT", "description": "renamed",
				},
			},
			{name: "gl.account.update.bad_id", method: "PUT", path: "/api/v1/gl/accounts/not-a-uuid",
				body: map[string]any{"code": "1", "name": "x", "type": "ASSET"}},
			{name: "gl.account.update.bad_body", method: "PUT", path: "/api/v1/gl/accounts/{a_glaccount}", body: "not-an-object"},
			{name: "gl.account.update.not_found", method: "PUT", path: "/api/v1/gl/accounts/" + r1bAMissingID,
				body: map[string]any{"code": "1993", "name": "Ghost", "type": "ASSET", "normal_balance": "DEBIT"}},

			// Journal entries: one reversed, one voided, each created and posted here.
			{name: "gl.a.entry_reverse.create", method: "POST", path: "/api/v1/gl/journal-entries",
				body: entryBody("golden reverse source", 25.0), extract: map[string]string{"a_je_reverse": "/id"}},
			{name: "gl.a.entry_reverse.post", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_reverse}/post"},
			{name: "gl.a.entry_void.create", method: "POST", path: "/api/v1/gl/journal-entries",
				body: entryBody("golden void source", 15.0), extract: map[string]string{"a_je_void": "/id"}},
			{name: "gl.journal_entry.void.draft_refused", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_void}/void"},
			{name: "gl.journal_entry.reverse.draft_refused", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_void}/reverse",
				body: map[string]any{"reason": "draft"}},
			{name: "gl.a.entry_void.post", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_void}/post"},
			{name: "gl.journal_entry.get", method: "GET", path: "/api/v1/gl/journal-entries/{a_je_reverse}"},
			{name: "gl.journal_entry.get.not_found", method: "GET", path: "/api/v1/gl/journal-entries/" + r1bAMissingID},
			{name: "gl.journal_entry.get.bad_id", method: "GET", path: "/api/v1/gl/journal-entries/not-a-uuid"},
			{name: "gl.journal_entry.reverse", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_reverse}/reverse",
				body: map[string]any{"reason": "golden reversal"}, extract: map[string]string{"a_je_reversal": "/reversal_entry_id"}},
			{name: "gl.journal_entry.reverse.again", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_reverse}/reverse",
				body: map[string]any{"reason": "second"}},
			{name: "gl.journal_entry.get.reversal", method: "GET", path: "/api/v1/gl/journal-entries/{a_je_reversal}"},
			{name: "gl.journal_entry.reverse.not_found", method: "POST", path: "/api/v1/gl/journal-entries/" + r1bAMissingID + "/reverse"},
			{name: "gl.journal_entry.reverse.bad_id", method: "POST", path: "/api/v1/gl/journal-entries/not-a-uuid/reverse"},
			{name: "gl.journal_entry.void", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_void}/void"},
			{name: "gl.journal_entry.void.again", method: "POST", path: "/api/v1/gl/journal-entries/{a_je_void}/void"},
			{name: "gl.journal_entry.void.not_found", method: "POST", path: "/api/v1/gl/journal-entries/" + r1bAMissingID + "/void"},
			{name: "gl.journal_entry.void.bad_id", method: "POST", path: "/api/v1/gl/journal-entries/not-a-uuid/void"},
			{name: "gl.journal_entry.list", method: "GET", path: "/api/v1/gl/journal-entries"},
		},
	}, {
		name: "gl_fiscal_periods",
		steps: []stepDef{
			// The list is calendar bound (twelve monthly periods of the current
			// year); the current month's period is picked by position.
			{
				name:    "gl.fiscal_periods.list",
				method:  "GET",
				path:    "/api/v1/gl/fiscal-periods",
				extract: map[string]string{"a_period": fmt.Sprintf("/%d/id", periodIdx)},
				// The migration seeds twelve monthly periods of the current
				// calendar year: the name carries the year and the dates move
				// with the calendar, so they mask; ids, status and order pin.
				maskFields: map[string]any{"name": "<period>", "start_date": "<period-start>", "end_date": "<period-end>"},
			},
			{name: "gl.fiscal_period.close", method: "POST", path: "/api/v1/gl/fiscal-periods/{a_period}/close"},
			{name: "gl.fiscal_period.close.again", method: "POST", path: "/api/v1/gl/fiscal-periods/{a_period}/close"},
			{name: "gl.fiscal_period.close.not_found", method: "POST", path: "/api/v1/gl/fiscal-periods/" + r1bAMissingID + "/close"},
			{name: "gl.fiscal_period.close.bad_id", method: "POST", path: "/api/v1/gl/fiscal-periods/not-a-uuid/close"},
			{name: "gl.fiscal_period.reopen", method: "POST", path: "/api/v1/gl/fiscal-periods/{a_period}/reopen"},
			{name: "gl.fiscal_period.reopen.not_closed", method: "POST", path: "/api/v1/gl/fiscal-periods/{a_period}/reopen"},
			{name: "gl.fiscal_period.reopen.not_found", method: "POST", path: "/api/v1/gl/fiscal-periods/" + r1bAMissingID + "/reopen"},
			{name: "gl.fiscal_period.reopen.bad_id", method: "POST", path: "/api/v1/gl/fiscal-periods/not-a-uuid/reopen"},
		},
	}}
}

func r1bAMatchingGroups() []groupDef {
	return []groupDef{{
		name: "matching_runs",
		steps: []stepDef{
			{name: "matching.exceptions.list_before", method: "GET", path: "/api/v1/matching/exceptions"},
			{name: "matching.results.none_yet", method: "GET", path: "/api/v1/matching/results/{myPO}"},
			{name: "matching.run", method: "POST", path: "/api/v1/matching/run/{myPO}"},
			{name: "matching.results", method: "GET", path: "/api/v1/matching/results/{myPO}"},
			{name: "matching.exceptions.list_after", method: "GET", path: "/api/v1/matching/exceptions"},
			{name: "matching.run.unknown_po", method: "POST", path: "/api/v1/matching/run/" + r1bAMissingID},
			{name: "matching.run.bad_id", method: "POST", path: "/api/v1/matching/run/not-a-uuid"},
			{name: "matching.results.not_found", method: "GET", path: "/api/v1/matching/results/" + r1bAMissingID},
			{name: "matching.results.bad_id", method: "GET", path: "/api/v1/matching/results/not-a-uuid"},
		},
	}}
}

func r1bABankreconGroups() []groupDef {
	return []groupDef{{
		name: "bankrecon_sessions",
		steps: []stepDef{
			{name: "bankrecon.sessions.list_empty", method: "GET", path: "/api/v1/bankrecon/sessions?bank_account_id={myBankAccount}"},
			{
				name:   "bankrecon.session.create",
				method: "POST",
				path:   "/api/v1/bankrecon/sessions",
				body: map[string]any{
					"bank_account_id": "{myBankAccount}", "period_start": "{today-30}",
					"period_end": "{today}", "statement_balance": 1234.56,
				},
				extract: map[string]string{"a_session": "/id"},
			},
			{name: "bankrecon.session.create.bad_dates", method: "POST", path: "/api/v1/bankrecon/sessions",
				body: map[string]any{
					"bank_account_id": "{myBankAccount}", "period_start": "last month",
					"period_end": "{today}", "statement_balance": 1.0,
				}},
			{name: "bankrecon.session.create.bad_body", method: "POST", path: "/api/v1/bankrecon/sessions", body: "not-an-object"},
			{
				name:   "bankrecon.import",
				method: "POST",
				path:   "/api/v1/bankrecon/import",
				body: map[string]any{
					"bank_account_id": "{myBankAccount}", "reconciliation_id": "{a_session}",
					"csv_content": "date,amount,description,reference\n" +
						"{today-3},-731.19,GOLD BANK FEE,GOLD-REF-1\n" +
						"not-a-date,12.00,BAD ROW,GOLD-REF-2\n" +
						"{today-2},4421.07,GOLD DEPOSIT,GOLD-REF-3\n",
				},
			},
			{name: "bankrecon.import.empty", method: "POST", path: "/api/v1/bankrecon/import",
				body: map[string]any{"bank_account_id": "{myBankAccount}", "csv_content": ""}},
			{name: "bankrecon.import.bad_body", method: "POST", path: "/api/v1/bankrecon/import", body: "not-an-object"},
			{
				name:    "bankrecon.session.get",
				method:  "GET",
				path:    "/api/v1/bankrecon/sessions/{a_session}",
				extract: map[string]string{"a_txn": "/transactions/0/id"},
			},
			{name: "bankrecon.session.get.not_found", method: "GET", path: "/api/v1/bankrecon/sessions/" + r1bAMissingID},
			{name: "bankrecon.session.get.bad_id", method: "GET", path: "/api/v1/bankrecon/sessions/not-a-uuid"},
			{name: "bankrecon.match", method: "POST", path: "/api/v1/bankrecon/match",
				body: map[string]any{"bank_transaction_id": "{a_txn}", "journal_entry_id": "{myJournalEntry}"}},
			{name: "bankrecon.session.get.after_match", method: "GET", path: "/api/v1/bankrecon/sessions/{a_session}"},
			{name: "bankrecon.match.unknown_transaction", method: "POST", path: "/api/v1/bankrecon/match",
				body: map[string]any{"bank_transaction_id": r1bAMissingID, "journal_entry_id": "{myJournalEntry}"}},
			{name: "bankrecon.match.bad_body", method: "POST", path: "/api/v1/bankrecon/match", body: "not-an-object"},
			{name: "bankrecon.unmatch", method: "POST", path: "/api/v1/bankrecon/unmatch",
				body: map[string]any{"bank_transaction_id": "{a_txn}"}},
			{name: "bankrecon.session.get.after_unmatch", method: "GET", path: "/api/v1/bankrecon/sessions/{a_session}"},
			{name: "bankrecon.unmatch.unknown_transaction", method: "POST", path: "/api/v1/bankrecon/unmatch",
				body: map[string]any{"bank_transaction_id": r1bAMissingID}},
			{name: "bankrecon.unmatch.bad_body", method: "POST", path: "/api/v1/bankrecon/unmatch", body: "not-an-object"},
			{name: "bankrecon.sessions.list", method: "GET", path: "/api/v1/bankrecon/sessions?bank_account_id={myBankAccount}"},
			{name: "bankrecon.session.complete", method: "POST", path: "/api/v1/bankrecon/sessions/{a_session}/complete"},
			{name: "bankrecon.session.complete.again", method: "POST", path: "/api/v1/bankrecon/sessions/{a_session}/complete"},
			{name: "bankrecon.session.complete.not_found", method: "POST", path: "/api/v1/bankrecon/sessions/" + r1bAMissingID + "/complete"},
			{name: "bankrecon.session.complete.bad_id", method: "POST", path: "/api/v1/bankrecon/sessions/not-a-uuid/complete"},
		},
	}}
}

func r1bAEDIGroups() []groupDef {
	// The catalog import reads the raw request body, which the harness can
	// only send as JSON. A JSON object whose one string value carries a real
	// X12 832 body between its quotes is still a valid segment stream for the
	// parser (segments split on "~"), so the happy path is reachable.
	x832 := "ST*832*0001~N1*SU*Golden Supplier~LIN*1*VP*GOLD-VP-1*SK*GOLD-SK-1~" +
		"PID*F****Golden stud~CTP*RS*RES*12.50*1*EA~LIN*2*VP*GOLD-VP-2~PID*F****Golden plate~" +
		"CTP*RS*RES*4.25*1*EA~SE*8*0001"
	return []groupDef{{
		name: "edi_partner_catalog",
		steps: []stepDef{
			{
				name:   "edi.a.partner.create",
				method: "POST",
				path:   "/api/v1/edi/partners",
				body: map[string]any{
					"name": "Golden Catalog Partner", "isa_sender_id": "GOLDCATS",
					"isa_receiver_id": "GOLDCATR", "notes": "item fixture",
				},
				extract: map[string]string{"a_partner": "/id"},
			},
			{name: "edi.partner.list", method: "GET", path: "/api/v1/edi/partners", sortPrimaryArray: true, sortEnvelopeItems: true},
			{
				name:   "edi.partner.update",
				method: "PUT",
				path:   "/api/v1/edi/partners/{a_partner}",
				body: map[string]any{
					"name": "Golden Catalog Partner Renamed", "isa_sender_id": "GOLDCATS",
					"isa_receiver_id": "GOLDCATR", "edi_version": "004010", "transport_type": "AS2",
					"transport_config": "{}", "supported_documents": []string{"832", "850"},
					"is_active": true, "notes": "renamed",
				},
			},
			{name: "edi.partner.get.after_update", method: "GET", path: "/api/v1/edi/partners/{a_partner}"},
			{name: "edi.partner.update.not_found", method: "PUT", path: "/api/v1/edi/partners/" + r1bAMissingID,
				body: map[string]any{"name": "Ghost"}},
			{name: "edi.partner.update.bad_id", method: "PUT", path: "/api/v1/edi/partners/not-a-uuid",
				body: map[string]any{"name": "Ghost"}},
			{name: "edi.partner.update.bad_body", method: "PUT", path: "/api/v1/edi/partners/{a_partner}", body: "not-an-object"},
			{name: "edi.catalog.list_empty", method: "GET", path: "/api/v1/edi/partners/{a_partner}/catalog"},
			{name: "edi.catalog.import_x12", method: "POST", path: "/api/v1/edi/partners/{a_partner}/import-catalog",
				body: map[string]any{"x": x832}},
			{name: "edi.catalog.list", method: "GET", path: "/api/v1/edi/partners/{a_partner}/catalog", sortPrimaryArray: true, sortEnvelopeItems: true},
			{name: "edi.catalog.import_not_x12", method: "POST", path: "/api/v1/edi/partners/{a_partner}/import-catalog",
				body: map[string]any{}},
			{name: "edi.catalog.import_csv_unreadable", method: "POST",
				path: "/api/v1/edi/partners/{a_partner}/import-catalog?format=csv", body: map[string]any{"a": "b"}},
			{name: "edi.catalog.import.bad_id", method: "POST", path: "/api/v1/edi/partners/not-a-uuid/import-catalog",
				body: map[string]any{"x": x832}},
			{name: "edi.catalog.list.bad_id", method: "GET", path: "/api/v1/edi/partners/not-a-uuid/catalog"},
			{name: "edi.partner.delete", method: "DELETE", path: "/api/v1/edi/partners/{a_partner}"},
			{name: "edi.partner.get.after_delete", method: "GET", path: "/api/v1/edi/partners/{a_partner}"},
			{name: "edi.partner.delete.again", method: "DELETE", path: "/api/v1/edi/partners/{a_partner}"},
			{name: "edi.partner.delete.bad_id", method: "DELETE", path: "/api/v1/edi/partners/not-a-uuid"},
		},
	}}
}

// r1bACurrentPeriodIndex is the position of the current month's fiscal period
// in GET /gl/fiscal-periods, which the migration seeds as twelve monthly
// periods of the current year ordered by start date. The harness refuses runs
// that cross UTC midnight, so the month at script-build time is the seed month.
func r1bACurrentPeriodIndex() int {
	return int(time.Now().UTC().Month()) - 1
}
