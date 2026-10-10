// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import "testing"

// R1-1b depth for the dealer portal: catalog detail, cart edits, checkout,
// orders, invoices, deliveries, quotes, team management, projects and the
// partner quote reads. Every group logs in again itself where it needs the
// session and creates its own fixtures (variables prefixed b_), so it depends
// only on the seed variables and the earlier portal and project groups.

const bNoSuchID = "00000000-0000-4000-8000-0000000000b1"

func bLogin(name string) stepDef {
	return stepDef{
		name:   name,
		method: "POST",
		path:   "/api/portal/v1/login",
		body:   map[string]any{"email": "demo@kelbrook.ca", "password": "password"},
	}
}

func r1bBGroups() []groupDef {
	return concat(
		r1bBCatalogGroups(),
		r1bBCartOrderGroups(),
		r1bBBillingGroups(),
		r1bBQuoteGroups(),
		r1bBTeamGroups(),
		r1bBProjectPartnerGroups(),
		r1bBLogoutGroups(),
	)
}

func r1bBCatalogGroups() []groupDef {
	return []groupDef{{
		name: "portal_catalog",
		steps: []stepDef{
			bLogin("portal_catalog.login"),
			{name: "portal_catalog.categories", method: "GET", path: "/api/portal/v1/catalog/categories"},
			{name: "portal_catalog.product", method: "GET", path: "/api/portal/v1/catalog/{product}"},
			{name: "portal_catalog.product.bad_id", method: "GET", path: "/api/portal/v1/catalog/not-a-uuid"},
			{name: "portal_catalog.product.unknown", method: "GET", path: "/api/portal/v1/catalog/" + bNoSuchID},
			{name: "portal_catalog.volume_breaks", method: "GET", path: "/api/portal/v1/catalog/{product}/volume-breaks"},
			{name: "portal_catalog.volume_breaks.bad_id", method: "GET", path: "/api/portal/v1/catalog/not-a-uuid/volume-breaks"},
			{name: "portal_catalog.volume_breaks.unknown", method: "GET", path: "/api/portal/v1/catalog/" + bNoSuchID + "/volume-breaks"},
		},
	}}
}

func r1bBCartOrderGroups() []groupDef {
	return []groupDef{{
		name: "portal_orders",
		steps: []stepDef{
			bLogin("portal_orders.login"),
			{name: "portal_orders.project.create", method: "POST", path: "/api/portal/v1/projects",
				body:    map[string]any{"name": "Golden Orders Project"},
				extract: map[string]string{"b_ordProject": "/id"}},
			// Cart edits on a line this group adds (the earlier portal group
			// left one line of {product} in the cart; the new line is second).
			{name: "portal_orders.cart.add", method: "POST", path: "/api/portal/v1/cart/items",
				body:    map[string]any{"product_id": "{productSheet}", "quantity": 1},
				extract: map[string]string{"b_cartItem": "/items/1/id"}},
			{name: "portal_orders.cart.update", method: "PUT", path: "/api/portal/v1/cart/items/{b_cartItem}",
				body: map[string]any{"quantity": 3}},
			{name: "portal_orders.cart.update.bad_id", method: "PUT", path: "/api/portal/v1/cart/items/not-a-uuid",
				body: map[string]any{"quantity": 3}},
			{name: "portal_orders.cart.update.bad_body", method: "PUT", path: "/api/portal/v1/cart/items/{b_cartItem}",
				body: "not an object"},
			{name: "portal_orders.cart.update.unknown", method: "PUT", path: "/api/portal/v1/cart/items/" + bNoSuchID,
				body: map[string]any{"quantity": 3}},
			{name: "portal_orders.cart.remove", method: "DELETE", path: "/api/portal/v1/cart/items/{b_cartItem}"},
			{name: "portal_orders.cart.remove.bad_id", method: "DELETE", path: "/api/portal/v1/cart/items/not-a-uuid"},
			{name: "portal_orders.cart.remove.unknown", method: "DELETE", path: "/api/portal/v1/cart/items/" + bNoSuchID},
			{name: "portal_orders.checkout.bad_body", method: "POST", path: "/api/portal/v1/checkout", body: "not an object"},
			{name: "portal_orders.checkout", method: "POST", path: "/api/portal/v1/checkout",
				body: map[string]any{
					"delivery_method": "PICKUP", "payment_method": "ACCOUNT",
					"notes": "golden checkout", "project_id": "{b_ordProject}",
				},
				extract: map[string]string{"b_order": "/order_id"}},
			// The cart is empty after a successful checkout.
			{name: "portal_orders.checkout.empty_cart", method: "POST", path: "/api/portal/v1/checkout",
				body: map[string]any{"delivery_method": "PICKUP", "payment_method": "ACCOUNT"}},
			// The unfiltered list and the dashboard carry the seeded book,
			// which varies per run; see r1bBSeededReadGroups.
			{name: "portal_orders.list.by_project", method: "GET", path: "/api/portal/v1/orders?project_id={b_ordProject}"},
			{name: "portal_orders.list.not_modified", method: "GET", path: "/api/portal/v1/orders?project_id={b_ordProject}",
				headers: map[string]string{"If-None-Match": "*"}},
			{name: "portal_orders.list.since_future", method: "GET", path: "/api/portal/v1/orders?since=2999-01-01T00:00:00Z"},
			{name: "portal_orders.list.bad_since", method: "GET", path: "/api/portal/v1/orders?since=yesterday"},
			{name: "portal_orders.list.bad_project", method: "GET", path: "/api/portal/v1/orders?project_id=nope"},
			{name: "portal_orders.get", method: "GET", path: "/api/portal/v1/orders/{b_order}"},
			{name: "portal_orders.get.bad_id", method: "GET", path: "/api/portal/v1/orders/not-a-uuid"},
			{name: "portal_orders.get.unknown", method: "GET", path: "/api/portal/v1/orders/" + bNoSuchID},
			{name: "portal_orders.project.detach", method: "PUT", path: "/api/portal/v1/orders/{b_order}/project",
				body: map[string]any{"project_id": nil}},
			{name: "portal_orders.project.attach", method: "PUT", path: "/api/portal/v1/orders/{b_order}/project",
				body: map[string]any{"project_id": "{b_ordProject}"}},
			{name: "portal_orders.project.unknown_project", method: "PUT", path: "/api/portal/v1/orders/{b_order}/project",
				body: map[string]any{"project_id": bNoSuchID}},
			{name: "portal_orders.project.unknown_order", method: "PUT", path: "/api/portal/v1/orders/" + bNoSuchID + "/project",
				body: map[string]any{"project_id": nil}},
			{name: "portal_orders.project.bad_id", method: "PUT", path: "/api/portal/v1/orders/not-a-uuid/project",
				body: map[string]any{"project_id": nil}},
			{name: "portal_orders.reorder", method: "POST", path: "/api/portal/v1/orders/reorder",
				body: map[string]any{"order_id": "{b_order}"}},
			{name: "portal_orders.reorder.bad_body", method: "POST", path: "/api/portal/v1/orders/reorder", body: "not an object"},
			{name: "portal_orders.reorder.unknown", method: "POST", path: "/api/portal/v1/orders/reorder",
				body: map[string]any{"order_id": bNoSuchID}},
			// Destructive steps act on this group's own checkout order.
			{name: "portal_orders.cancel", method: "POST", path: "/api/portal/v1/orders/{b_order}/cancel",
				body: map[string]any{"reason": "golden cancel"}},
			{name: "portal_orders.cancel.again", method: "POST", path: "/api/portal/v1/orders/{b_order}/cancel"},
			{name: "portal_orders.cancel.unknown", method: "POST", path: "/api/portal/v1/orders/" + bNoSuchID + "/cancel"},
			{name: "portal_orders.cancel.bad_id", method: "POST", path: "/api/portal/v1/orders/not-a-uuid/cancel"},
		},
	}}
}

func r1bBBillingGroups() []groupDef {
	return []groupDef{{
		name: "portal_billing",
		steps: []stepDef{
			bLogin("portal_billing.login"),
			// Two orders of this group's own: one is confirmed and fulfilled
			// into an invoice, the other goes onto a route as a delivery.
			{name: "portal_billing.cart.add_invoice", method: "POST", path: "/api/portal/v1/cart/items",
				body: map[string]any{"product_id": "{product}", "quantity": 2}},
			{name: "portal_billing.checkout_invoice", method: "POST", path: "/api/portal/v1/checkout",
				body:    map[string]any{"delivery_method": "PICKUP", "payment_method": "ACCOUNT"},
				extract: map[string]string{"b_invOrder": "/order_id"}},
			{name: "portal_billing.cart.add_delivery", method: "POST", path: "/api/portal/v1/cart/items",
				body: map[string]any{"product_id": "{product}", "quantity": 1}},
			{name: "portal_billing.checkout_delivery", method: "POST", path: "/api/portal/v1/checkout",
				body:    map[string]any{"delivery_method": "DELIVERY", "delivery_address": "1 Golden Way", "payment_method": "ACCOUNT"},
				extract: map[string]string{"b_delOrder": "/order_id"}},
			{name: "portal_billing.erp_confirm", method: "POST", path: "/api/v1/orders/{b_invOrder}/transitions",
				body: map[string]any{"to": "confirmed", "revision": 1}},
			// The fulfilment route lands with C2-2b (ADR 0005 5.6); until
			// then the invoice it would mint is seeded in its shape, and the
			// count proves it. C2-2b replaces the seed with the route.
			{name: "portal_billing.erp_fulfill",
				sql: `SELECT count(*) AS invoices FROM invoices WHERE order_id = '{b_invOrder}'::uuid`,
				setup: func(t *testing.T, h *harness) {
					goldenExec(t, h, `INSERT INTO invoices (id, order_id, customer_id, branch_id, status,
						total_amount, subtotal, tax_rate, tax_amount, due_date, created_at, updated_at)
						VALUES (gen_random_uuid(), '{b_invOrder}'::uuid,
						(SELECT customer_id FROM orders WHERE id = '{b_invOrder}'::uuid),
						(SELECT branch_id FROM orders WHERE id = '{b_invOrder}'::uuid), 'UNPAID',
						ROUND((SELECT total_amount FROM orders WHERE id = '{b_invOrder}'::uuid), 2),
						ROUND((SELECT subtotal FROM orders WHERE id = '{b_invOrder}'::uuid), 2),
						COALESCE((SELECT tax_rate FROM orders WHERE id = '{b_invOrder}'::uuid), 0),
						ROUND((SELECT tax_amount FROM orders WHERE id = '{b_invOrder}'::uuid), 2),
						CURRENT_DATE + 30, NOW(), NOW())`)
				}},
			{name: "portal_billing.erp_invoices", method: "GET", path: "/api/v1/invoices?limit=1",
				extract: map[string]string{"b_invoice": "/items/0/id"}},
			{name: "portal_billing.invoice.get", method: "GET", path: "/api/portal/v1/invoices/{b_invoice}"},
			{name: "portal_billing.invoice.bad_id", method: "GET", path: "/api/portal/v1/invoices/not-a-uuid"},
			{name: "portal_billing.invoice.unknown", method: "GET", path: "/api/portal/v1/invoices/" + bNoSuchID},
			// The AI_LM route writer puts the order on a route without the
			// mock geocoder, whose coordinates derive from the random order id
			// and would not repeat between runs. The date is one no other
			// group uses, because the writer replaces a route on the same
			// vehicle and day.
			{name: "portal_billing.erp_route", method: "POST", path: "/api/integration/delivery-routes",
				headers: map[string]string{"X-Integration-Key": "fb-brain-demo-key-2026"},
				body: map[string]any{
					"vehicle_id": "{vehicle}", "driver_id": "{driver}", "scheduled_date": "{today+11}",
					"stops": []map[string]any{{"order_id": "{b_delOrder}", "sequence": 1}},
				},
				extract: map[string]string{"b_route": "/route_id"}},
			{name: "portal_billing.erp_route_deliveries", method: "GET", path: "/api/v1/delivery/routes/{b_route}/deliveries",
				extract: map[string]string{"b_delivery": "/items/0/id"}},
			{name: "portal_billing.delivery.get", method: "GET", path: "/api/portal/v1/deliveries/{b_delivery}"},
			{name: "portal_billing.delivery.bad_id", method: "GET", path: "/api/portal/v1/deliveries/not-a-uuid"},
			{name: "portal_billing.delivery.unknown", method: "GET", path: "/api/portal/v1/deliveries/" + bNoSuchID},
			{name: "portal_billing.reschedule.none", method: "GET", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule"},
			{name: "portal_billing.reschedule.bad_id", method: "GET", path: "/api/portal/v1/deliveries/not-a-uuid/reschedule"},
			{name: "portal_billing.reschedule.unknown", method: "GET", path: "/api/portal/v1/deliveries/" + bNoSuchID + "/reschedule"},
			{name: "portal_billing.reschedule.request", method: "POST", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule",
				body: map[string]any{"requested_date": "{today+12}", "reason": "golden reschedule"}},
			{name: "portal_billing.reschedule.past", method: "POST", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule",
				body: map[string]any{"requested_date": "{today-3}", "reason": "too late"}},
			{name: "portal_billing.reschedule.bad_date", method: "POST", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule",
				body: map[string]any{"requested_date": "soon"}},
			{name: "portal_billing.reschedule.bad_body", method: "POST", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule", body: "not an object"},
			{name: "portal_billing.reschedule.unknown_post", method: "POST", path: "/api/portal/v1/deliveries/" + bNoSuchID + "/reschedule",
				body: map[string]any{"requested_date": "{today+3}"}},
			{name: "portal_billing.reschedule.get", method: "GET", path: "/api/portal/v1/deliveries/{b_delivery}/reschedule"},
		},
	}}
}

// r1bBSeededReadGroups holds the portal reads that return the demo customer's
// seeded book (dashboard, unfiltered order list, invoice list, delivery list).
// The seed assigns its drawn amounts to customers in map-iteration order, so
// these bodies differ run to run even with the fixed draw sequence; the group
// is therefore NOT part of r1bBGroups until the harness can pin them by shape
// (see the report that accompanies this file).
func r1bBSeededReadGroups() []groupDef {
	return []groupDef{{
		name: "portal_seeded_reads",
		steps: []stepDef{
			bLogin("portal_seeded_reads.login"),
			{name: "portal_seeded_reads.dashboard", method: "GET", path: "/api/portal/v1/dashboard"},
			{name: "portal_seeded_reads.orders", method: "GET", path: "/api/portal/v1/orders"},
			{name: "portal_seeded_reads.invoices", method: "GET", path: "/api/portal/v1/invoices"},
			{name: "portal_seeded_reads.deliveries", method: "GET", path: "/api/portal/v1/deliveries"},
		},
	}}
}

func r1bBQuoteGroups() []groupDef {
	return []groupDef{{
		name: "portal_quotes",
		steps: []stepDef{
			bLogin("portal_quotes.login"),
			{name: "portal_quotes.create", method: "POST", path: "/api/portal/v1/quotes",
				body: map[string]any{
					"notes": "golden scope", "delivery_type": "DELIVERY",
					"lines": []map[string]any{
						{"product_id": "{product}", "quantity": 12, "note": "catalog line"},
						{"description": "Custom cedar bracket", "quantity": 4, "uom": "ea"},
					},
				},
				extract: map[string]string{"b_quoteA": "/id"}},
			{name: "portal_quotes.create.second", method: "POST", path: "/api/portal/v1/quotes",
				body: map[string]any{
					"lines": []map[string]any{{"product_id": "{product}", "quantity": 2}},
				},
				extract: map[string]string{"b_quoteB": "/id"}},
			{name: "portal_quotes.create.no_lines", method: "POST", path: "/api/portal/v1/quotes",
				body: map[string]any{"notes": "empty"}},
			{name: "portal_quotes.create.bad_body", method: "POST", path: "/api/portal/v1/quotes", body: "not an object"},
			{name: "portal_quotes.create.bad_uom", method: "POST", path: "/api/portal/v1/quotes",
				body: map[string]any{"lines": []map[string]any{{"description": "x", "quantity": 1, "uom": "parsec"}}}},
			{name: "portal_quotes.list", method: "GET", path: "/api/portal/v1/quotes"},
			{name: "portal_quotes.get", method: "GET", path: "/api/portal/v1/quotes/{b_quoteA}"},
			{name: "portal_quotes.get.bad_id", method: "GET", path: "/api/portal/v1/quotes/not-a-uuid"},
			{name: "portal_quotes.get.unknown", method: "GET", path: "/api/portal/v1/quotes/" + bNoSuchID},
			// Not priced yet: refused with the portal's 409.
			{name: "portal_quotes.accept.unpriced", method: "POST", path: "/api/portal/v1/quotes/{b_quoteA}/accept"},
			{name: "portal_quotes.decline.unpriced", method: "POST", path: "/api/portal/v1/quotes/{b_quoteB}/decline"},
			// The dealer prices (sends) both quotes through the ERP route.
			{name: "portal_quotes.erp_send_a", method: "POST", path: "/api/v1/quotes/{b_quoteA}/transitions",
				headers: map[string]string{"If-Match": `"1"`}, body: map[string]any{"to": "sent"}},
			{name: "portal_quotes.erp_send_b", method: "POST", path: "/api/v1/quotes/{b_quoteB}/transitions",
				headers: map[string]string{"If-Match": `"1"`}, body: map[string]any{"to": "sent"}},
			{name: "portal_quotes.accept", method: "POST", path: "/api/portal/v1/quotes/{b_quoteA}/accept"},
			{name: "portal_quotes.accept.again", method: "POST", path: "/api/portal/v1/quotes/{b_quoteA}/accept"},
			{name: "portal_quotes.decline", method: "POST", path: "/api/portal/v1/quotes/{b_quoteB}/decline"},
			{name: "portal_quotes.accept.unknown", method: "POST", path: "/api/portal/v1/quotes/" + bNoSuchID + "/accept"},
			{name: "portal_quotes.decline.bad_id", method: "POST", path: "/api/portal/v1/quotes/not-a-uuid/decline"},
		},
	}}
}

func r1bBTeamGroups() []groupDef {
	return []groupDef{{
		name: "portal_team",
		steps: []stepDef{
			bLogin("portal_team.login"),
			{name: "portal_team.users", method: "GET", path: "/api/portal/v1/users",
				extract: map[string]string{"b_user": "/0/id"}},
			{name: "portal_team.invites.empty", method: "GET", path: "/api/portal/v1/invites"},
			{name: "portal_team.invite", method: "POST", path: "/api/portal/v1/invites",
				body: map[string]any{"email": "golden.buyer@example.com", "role": "Buyer"}},
			{name: "portal_team.invite.bad_role", method: "POST", path: "/api/portal/v1/invites",
				body: map[string]any{"email": "golden.bad@example.com", "role": "Owner"}},
			{name: "portal_team.invite.bad_body", method: "POST", path: "/api/portal/v1/invites", body: "not an object"},
			{name: "portal_team.invites", method: "GET", path: "/api/portal/v1/invites"},
			// Role and status writes restate the demo user's own values, so
			// the later steps still act as an Admin on an Active account.
			{name: "portal_team.role", method: "PUT", path: "/api/portal/v1/users/{b_user}/role",
				body: map[string]any{"role": "Admin"}},
			{name: "portal_team.role.bad_role", method: "PUT", path: "/api/portal/v1/users/{b_user}/role",
				body: map[string]any{"role": "Owner"}},
			{name: "portal_team.role.bad_id", method: "PUT", path: "/api/portal/v1/users/not-a-uuid/role",
				body: map[string]any{"role": "Admin"}},
			{name: "portal_team.role.bad_body", method: "PUT", path: "/api/portal/v1/users/{b_user}/role", body: "not an object"},
			{name: "portal_team.role.unknown", method: "PUT", path: "/api/portal/v1/users/" + bNoSuchID + "/role",
				body: map[string]any{"role": "Admin"}},
			{name: "portal_team.status", method: "PUT", path: "/api/portal/v1/users/{b_user}/status",
				body: map[string]any{"status": "Active"}},
			{name: "portal_team.status.bad_status", method: "PUT", path: "/api/portal/v1/users/{b_user}/status",
				body: map[string]any{"status": "Frozen"}},
			{name: "portal_team.status.bad_id", method: "PUT", path: "/api/portal/v1/users/not-a-uuid/status",
				body: map[string]any{"status": "Active"}},
			{name: "portal_team.status.bad_body", method: "PUT", path: "/api/portal/v1/users/{b_user}/status", body: "not an object"},
			{name: "portal_team.status.unknown", method: "PUT", path: "/api/portal/v1/users/" + bNoSuchID + "/status",
				body: map[string]any{"status": "Active"}},
		},
	}}
}

func r1bBProjectPartnerGroups() []groupDef {
	return []groupDef{{
		name: "portal_projects",
		steps: []stepDef{
			bLogin("portal_projects.login"),
			{name: "portal_projects.create", method: "POST", path: "/api/portal/v1/projects",
				body:    map[string]any{"name": "Golden Project Two"},
				extract: map[string]string{"b_project": "/id"}},
			{name: "portal_projects.list", method: "GET", path: "/api/portal/v1/projects"},
			{name: "portal_projects.update", method: "PUT", path: "/api/portal/v1/projects/{b_project}",
				body: map[string]any{"name": "Golden Project Two Renamed", "status": "Completed"}},
			{name: "portal_projects.update.bad_id", method: "PUT", path: "/api/portal/v1/projects/not-a-uuid",
				body: map[string]any{"name": "x"}},
			{name: "portal_projects.update.bad_body", method: "PUT", path: "/api/portal/v1/projects/{b_project}", body: "not an object"},
			{name: "portal_projects.update.unknown", method: "PUT", path: "/api/portal/v1/projects/" + bNoSuchID,
				body: map[string]any{"name": "x"}},
		},
	}, {
		name: "partner_quotes",
		steps: []stepDef{
			{name: "partner_quotes.list.unauthenticated", method: "GET", path: "/api/partner/v1/quotes"},
			{name: "partner_quotes.get.unauthenticated", method: "GET", path: "/api/partner/v1/quotes/{myQuote}"},
		},
	}}
}

func r1bBLogoutGroups() []groupDef {
	return []groupDef{{
		name: "portal_logout",
		steps: []stepDef{
			bLogin("portal_logout.login"),
			{name: "portal_logout.logout", method: "POST", path: "/api/portal/v1/logout"},
			// Under AUTH_MODE=dev the portal auth chain ignores the cookie and
			// injects the demo claims, so a protected read after logout still
			// answers 200 (the dev-mode behaviour at this base).
			{name: "portal_logout.after", method: "GET", path: "/api/portal/v1/quotes/{b_quoteA}"},
		},
	}}
}
