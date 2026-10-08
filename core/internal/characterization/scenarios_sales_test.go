// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import "fmt"

// Sales and logistics groups: platform health, product, customer, sales team,
// CRM, quotes, orders (with the exposure gate), invoices (created through the
// order confirm/fulfil flow), payments, AR accounts, deposits, delivery,
// vendors, purchase orders, EDI partners and printed documents.

func healthGroups() []groupDef {
	return []groupDef{{
		name: "health",
		steps: []stepDef{
			{name: "health.live", method: "GET", path: "/healthz/live"},
			{name: "health.legacy", method: "GET", path: "/health"},
			// Readiness: status, database pool census and uptime. The pool
			// counts and the uptime string measure the run, not behaviour, so
			// they normalise; pool_max stays pinned (configured, observed).
			{name: "health.ready", method: "GET", path: "/healthz/ready"},
			// net/http's own method mismatch answer: text/plain, not JSON.
			{name: "health.method_not_allowed", method: "DELETE", path: "/healthz/live"},
			// The A2A purchase-order receiver mounts only when FB Brain is
			// enabled with a public key; the harness environment configures
			// neither, so today's answer is the unmuxed 404. That is the
			// mount-gated shape at this base, recorded as it is.
			{
				name:   "a2a.purchase_order.unmounted",
				method: "POST",
				path:   "/api/v1/a2a/purchase-order",
				body:   map[string]any{"external_id": "GOLD-A2A-1", "vendor_name": "Golden Vendor Co"},
			},
		},
	}}
}

func productGroups() []groupDef {
	return []groupDef{{
		name: "product",
		steps: []stepDef{
			// Seeded product read: includes the joined inventory aggregates.
			{name: "product.get", method: "GET", path: "/api/v1/products/{product}"},
			{name: "product.list", method: "GET", path: "/api/v1/products?limit=3"},
			{
				name:   "product.create",
				method: "POST",
				path:   "/api/v1/products",
				body: map[string]any{
					"sku": "GOLD-HARNESS-2X4", "description": "Golden harness stud", "uom_primary": "PCS",
					"base_price": 4.25, "reorder_point": 10, "reorder_qty": 50,
				},
				extract: map[string]string{"myProduct": "/id"},
			},
			// Cheap error paths: unparseable id, unknown id.
			{name: "product.get.bad_uuid", method: "GET", path: "/api/v1/products/not-a-uuid"},
			{name: "product.get.not_found", method: "GET", path: "/api/v1/products/00000000-0000-0000-0000-0000000000aa"},
		},
	}}
}

func customerGroups() []groupDef {
	// The customer module is on the wire contract (C2-1): lowercase tier,
	// _cents money with a credit limit that is null for no limit, the list
	// envelope with filters that filter, and a revision every edit names in
	// If-Match (the revision of a scripted customer is deterministic: create
	// 1, then one per write).
	header := func(extras ...map[string]any) map[string]any {
		b := map[string]any{
			"account_number": "GOLD-001", "name": "Golden Harness Co", "email": "goldens@example.com",
			"phone": "250-555-0199", "address": "1 Golden Way, Kelowna BC", "tier": "gold",
		}
		for _, extra := range extras {
			for k, v := range extra {
				b[k] = v
			}
		}
		return b
	}
	// A PUT carries the controls it must not reset by leaving them out.
	putHeader := func(extra map[string]any) map[string]any {
		return header(map[string]any{"payment_terms_id": "{myTerms}", "po_required": false}, extra)
	}
	return []groupDef{{
		name: "customer",
		steps: []stepDef{
			{
				name:   "customer.create",
				method: "POST",
				path:   "/api/v1/customers",
				body: header(map[string]any{
					"credit_limit_cents": 10000000, "primary_branch_id": "{branch}",
				}),
				extract: map[string]string{"myCustomer": "/id", "myTerms": "/payment_terms_id"},
			},
			{name: "customer.get", method: "GET", path: "/api/v1/customers/{myCustomer}"},
			{name: "customer.list", method: "GET", path: "/api/v1/customers?limit=3"},
			// The filters filter; an unsupported parameter, a tier outside the
			// lowercase vocabulary and a broken cursor are 400s naming the field.
			{name: "customer.list.search", method: "GET", path: "/api/v1/customers?q=GOLD-001&tier=gold&is_active=true&include=total"},
			{name: "customer.list.unsupported_parameter", method: "GET", path: "/api/v1/customers?offset=0"},
			{name: "customer.list.unsupported_tier", method: "GET", path: "/api/v1/customers?tier=GOLD"},
			{name: "customer.list.bad_cursor", method: "GET", path: "/api/v1/customers?cursor=garbage"},
			// Registered by the customer handler; the list envelope now.
			{name: "price_level.list", method: "GET", path: "/api/v1/price_levels?limit=2"},
			{name: "price_level.list.unsupported_parameter", method: "GET", path: "/api/v1/price_levels?offset=1"},
			{name: "customer.get.not_found", method: "GET", path: "/api/v1/customers/00000000-0000-0000-0000-0000000000aa"},
			{name: "customer.get.bad_id", method: "GET", path: "/api/v1/customers/not-a-uuid"},
			{
				name:   "customer.create.invalid",
				method: "POST",
				path:   "/api/v1/customers",
				body:   map[string]any{"tier": "GOLD", "email": "not an email", "credit_limit_cents": -1, "currency": "usd"},
			},
			{name: "customer.create.unknown_field", method: "POST", path: "/api/v1/customers",
				body: header(map[string]any{"account_number": "GOLD-002", "balance_due": 5})},
			{name: "customer.create.duplicate", method: "POST", path: "/api/v1/customers", body: header(nil)},
			// An edit names the revision; the first edit moves it to 2.
			{name: "customer.update.without_revision", method: "PUT", path: "/api/v1/customers/{myCustomer}", body: putHeader(nil)},
			{
				name:    "customer.update",
				method:  "PUT",
				path:    "/api/v1/customers/{myCustomer}",
				headers: map[string]string{"If-Match": `"1"`},
				body:    putHeader(map[string]any{"credit_limit_cents": 10000000, "po_required": true}),
			},
			{name: "customer.update.stale", method: "PUT", path: "/api/v1/customers/{myCustomer}",
				headers: map[string]string{"If-Match": `"1"`}, body: putHeader(nil)},
			{name: "customer.update.missing_controls", method: "PUT", path: "/api/v1/customers/{myCustomer}",
				headers: map[string]string{"If-Match": `"2"`}, body: header(nil)},
			{name: "customer.update.primary_branch", method: "PUT", path: "/api/v1/customers/{myCustomer}",
				headers: map[string]string{"If-Match": `"2"`}, body: putHeader(map[string]any{"primary_branch_id": "{branch}"})},
			{name: "customer.update.currency_not_enabled", method: "PUT", path: "/api/v1/customers/{myCustomer}",
				headers: map[string]string{"If-Match": `"2"`}, body: putHeader(map[string]any{"currency": "EUR"})},
			{name: "customer.get.after_update", method: "GET", path: "/api/v1/customers/{myCustomer}"},
		},
	}}
}

func salesGroups() []groupDef {
	return []groupDef{{
		name: "salesteam",
		steps: []stepDef{
			{name: "sales_team.list", method: "GET", path: "/api/v1/sales-team"},
		},
	}, {
		name: "crm",
		steps: []stepDef{
			{
				name:   "crm.activity.create",
				method: "POST",
				path:   "/api/v1/customers/{myCustomer}/activities",
				body: map[string]any{
					"activity_type": "NOTE", "description": "golden characterisation note",
				},
				extract: map[string]string{"myActivity": "/id"},
			},
			{name: "crm.activity.list", method: "GET", path: "/api/v1/customers/{myCustomer}/activities"},
			{name: "crm.activity.bad_type", method: "POST", path: "/api/v1/customers/{myCustomer}/activities",
				body: map[string]any{"activity_type": "SMOKE_SIGNAL", "description": "x"}},
		},
	}}
}

func glGroups() []groupDef {
	return []groupDef{{
		name: "gl",
		steps: []stepDef{
			{name: "gl.accounts.list", method: "GET", path: "/api/v1/gl/accounts"},
			{
				name:   "gl.journal_entry.create",
				method: "POST",
				path:   "/api/v1/gl/journal-entries",
				body: map[string]any{
					"memo": "golden characterisation entry", "entry_date": "{today}",
					"lines": []map[string]any{
						{"account_id": "{glAccount}", "description": "golden debit", "debit": 100.0, "credit": 0},
						{"account_id": "{glAccount2}", "description": "golden credit", "debit": 0, "credit": 100.0},
					},
				},
				extract: map[string]string{"myJournalEntry": "/id"},
			},
			{name: "gl.journal_entry.post", method: "POST", path: "/api/v1/gl/journal-entries/{myJournalEntry}/post"},
			// Unbalanced entry: the service's own 400.
			{name: "gl.journal_entry.unbalanced", method: "POST", path: "/api/v1/gl/journal-entries",
				body: map[string]any{
					"memo": "unbalanced", "entry_date": "{today}",
					"lines": []map[string]any{
						{"account_id": "{glAccount}", "description": "debit only", "debit": 50.0, "credit": 0},
					},
				}},
		},
	}}
}

func quoteGroups() []groupDef {
	// The quote module is on the wire contract (R1-15): lowercase status,
	// _cents money, quantities as decimal strings, a document number, and a
	// revision every update and transition names in If-Match. The revision
	// of a scripted quote is deterministic (create 1, then one per write).
	line := func(qty string) map[string]any {
		return map[string]any{
			"product_id": "{product}", "sku": "LUM-248-PREM", "description": "2x4x8 SPF Premium",
			"quantity": qty, "uom": "PCS", "unit_price_ten_thousandths": 55000,
		}
	}
	steps := []stepDef{
		{
			name:   "quote.create",
			method: "POST",
			path:   "/api/v1/quotes",
			body: map[string]any{
				"branch_id": "{branch}", "customer_id": "{customer}", "delivery_type": "pickup",
				"lines": []map[string]any{line("10")},
			},
			extract: map[string]string{"myQuote": "/id"},
		},
		{name: "quote.get", method: "GET", path: "/api/v1/quotes/{myQuote}"},
		{name: "quote.list", method: "GET", path: "/api/v1/quotes?limit=1"},
		// The four live failures the refactor inputs name for quotes, now
		// refused or served correctly: a line with neither a unit of measure nor
		// a product is a 400 naming the field, ?status filters, an unsupported status or
		// parameter is a 400, and the list pages by cursor.
		{
			name:   "quote.create.missing_uom",
			method: "POST",
			path:   "/api/v1/quotes",
			body: map[string]any{
				"branch_id": "{branch}", "customer_id": "{customer}", "delivery_type": "pickup",
				"lines": []map[string]any{{
					"sku": "LUM-248-PREM", "description": "2x4x8 SPF Premium",
					"quantity": "10", "unit_price_ten_thousandths": 55000,
				}},
			},
		},
		{name: "quote.list.status_filter", method: "GET", path: "/api/v1/quotes?status=sent&limit=2&include=total"},
		{name: "quote.list.unsupported_status", method: "GET", path: "/api/v1/quotes?status=SENT"},
		{name: "quote.list.unsupported_parameter", method: "GET", path: "/api/v1/quotes?offset=1"},
		{name: "quote.list.bad_cursor", method: "GET", path: "/api/v1/quotes?cursor=garbage"},
		// PUT on a DRAFT quote at its revision: the editable window. The
		// doubled line quantity is the observable difference.
		{
			name:   "quote.update.without_revision",
			method: "PUT",
			path:   "/api/v1/quotes/{myQuote}",
			body:   map[string]any{"customer_id": "{customer}", "lines": []map[string]any{line("20")}},
		},
		{
			name:    "quote.update",
			method:  "PUT",
			path:    "/api/v1/quotes/{myQuote}",
			headers: map[string]string{"If-Match": `"1"`},
			body: map[string]any{
				"customer_id": "{customer}", "delivery_type": "pickup",
				"lines": []map[string]any{line("20")},
			},
		},
		{
			name:    "quote.update.stale",
			method:  "PUT",
			path:    "/api/v1/quotes/{myQuote}",
			headers: map[string]string{"If-Match": `"1"`},
			body:    map[string]any{"customer_id": "{customer}", "lines": []map[string]any{line("30")}},
		},
		// A PUT applies the header fields and lines only: a create-only field
		// is a 400 naming it, and the revision stays where it was.
		{
			name:    "quote.update.create_only_field",
			method:  "PUT",
			path:    "/api/v1/quotes/{myQuote}",
			headers: map[string]string{"If-Match": `"2"`},
			body: map[string]any{
				"branch_id": "{branch}", "customer_id": "{customer}", "lines": []map[string]any{line("20")},
			},
		},
		{name: "quote.state.draft_to_sent", method: "POST", path: "/api/v1/quotes/{myQuote}/transitions",
			headers: map[string]string{"If-Match": `"2"`}, body: map[string]any{"to": "sent"}},
		// Convert out of SENT: returns the order payload the frontend is
		// meant to map onto POST /orders, and marks the quote accepted.
		{name: "quote.convert", method: "POST", path: "/api/v1/quotes/{myQuote}/convert",
			headers: map[string]string{"If-Match": `"3"`}},
		// accepted is terminal: the refused transition, with the
		// service's own error text.
		{name: "quote.state.refused_from_accepted", method: "POST", path: "/api/v1/quotes/{myQuote}/transitions",
			headers: map[string]string{"If-Match": `"4"`}, body: map[string]any{"to": "sent"}},
		{name: "quote.state.unknown_target", method: "POST", path: "/api/v1/quotes/{myQuote}/transitions",
			headers: map[string]string{"If-Match": `"4"`}, body: map[string]any{"to": "SENT"}},
	}
	// The remaining allowed transitions, each on its own fresh quote so the
	// source state is exactly what the transition map requires. Together
	// with the steps above, every allowed edge of the state machine is
	// pinned exactly once.
	steps = append(steps, appendSteps(
		quoteTransitionSteps("to_accepted", "accepted"),
		quoteTransitionSteps("rejected_reopened", "rejected", "draft"),
		quoteTransitionSteps("expired_reopened", "expired", "draft"),
		quoteTransitionSteps("sent_accepted", "sent", "accepted"),
		quoteTransitionSteps("sent_rejected", "sent", "rejected"),
		quoteTransitionSteps("sent_expired", "sent", "expired"),
	)...)
	// The events the writes above committed, read once through the feed: the
	// outbox wiring of the module is pinned here (quote.created and the
	// transition events are written in the mutation's transaction).
	steps = append(steps,
		stepDef{name: "quote.events.created", method: "GET", path: "/api/v1/events?types=quote.created&limit=3"},
		stepDef{name: "quote.events.transitions", method: "GET", path: "/api/v1/events?types=quote.sent,quote.accepted,quote.rejected,quote.expired,quote.reopened&limit=4"},
	)
	// Window aggregates over the whole quote book as this group leaves it
	// (deterministic: the group's writes are ordered).
	steps = append(steps,
		stepDef{name: "quote.analytics", method: "GET", path: "/api/v1/quotes/analytics"})
	return []groupDef{{name: "quote", steps: steps}}
}

// quoteTransitionSteps builds the steps for one fresh quote walked through
// the given statuses in order, starting from draft. The quote's {var} is named
// after the whole sequence, so several transition quotes can live in one
// script without colliding. Each transition names the quote's revision, which
// starts at 1 on create and moves by one per write.
func quoteTransitionSteps(sequence string, statuses ...string) []stepDef {
	quoteVar := "quote_" + sequence
	steps := []stepDef{{
		name:   "quote.create." + sequence,
		method: "POST",
		path:   "/api/v1/quotes",
		body: map[string]any{
			"branch_id": "{branch}", "customer_id": "{customer}", "delivery_type": "pickup",
			"lines": []map[string]any{{
				"product_id": "{product}", "sku": "LUM-248-PREM", "description": "2x4x8 SPF Premium",
				"quantity": "5", "uom": "PCS", "unit_price_ten_thousandths": 55000,
			}},
		},
		extract: map[string]string{quoteVar: "/id"},
	}}
	from := "draft"
	for i, target := range statuses {
		steps = append(steps, stepDef{
			name:    "quote.state." + from + "_to_" + target,
			method:  "POST",
			path:    "/api/v1/quotes/{" + quoteVar + "}/transitions",
			headers: map[string]string{"If-Match": fmt.Sprintf(`"%d"`, i+1)},
			body:    map[string]any{"to": target},
		})
		from = target
	}
	return steps
}

func appendSteps(groups ...[]stepDef) []stepDef {
	var out []stepDef
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func orderGroups() []groupDef {
	return []groupDef{{
		name: "order",
		steps: []stepDef{
			{
				name:   "order.create",
				method: "POST",
				path:   "/api/v1/orders",
				body: map[string]any{
					"customer_id": "{myCustomer}",
					"lines": []map[string]any{
						{"product_id": "{product}", "quantity": 5, "price_each": 550},
					},
				},
				extract: map[string]string{"myOrder": "/id"},
			},
			{name: "order.get", method: "GET", path: "/api/v1/orders/{myOrder}"},
			// No source quote on this order: the gate fails open.
			{name: "order.exposure_gate", method: "GET", path: "/api/v1/orders/{myOrder}/exposure-gate"},
			// Paged list envelope. The demo seed draws each customer's order
			// book from the global rand source inside a map-iteration loop,
			// so WHICH customer owns which drawn order is random per run;
			// only aggregate counts are stable. A limit=1 page is therefore
			// the deterministic read: the newest order is this script's own
			// (seeded orders are dated in the past), and the total counts
			// the whole book.
			{name: "order.list", method: "GET", path: "/api/v1/orders?limit=1"},
			// A second order, cancelled in this group: the order above stays
			// live for the invoice group's confirm/fulfil flow.
			{
				name:   "order.create.for_cancel",
				method: "POST",
				path:   "/api/v1/orders",
				body: map[string]any{
					"customer_id": "{myCustomer}",
					"lines": []map[string]any{
						{"product_id": "{product}", "quantity": 1, "price_each": 550},
					},
				},
				extract: map[string]string{"myCancelOrder": "/id"},
			},
			{name: "order.cancel", method: "POST", path: "/api/v1/orders/{myCancelOrder}/cancel",
				body: map[string]any{"reason": "golden characterisation cancel"}},
			// Cancelling twice: the state machine's 409 refusal.
			{name: "order.cancel.already_cancelled", method: "POST", path: "/api/v1/orders/{myCancelOrder}/cancel",
				body: map[string]any{"reason": "second attempt"}},
			// Owner override of the pre-ship gate on a clear order: the
			// write succeeds and records the event even without a block.
			{name: "order.exposure_override", method: "POST", path: "/api/v1/orders/{myOrder}/exposure-override",
				body: map[string]any{"notes": "golden characterisation override"}},
			{name: "order.get.not_found", method: "GET", path: "/api/v1/orders/00000000-0000-0000-0000-0000000000aa"},
		},
	}}
}

func invoiceGroups() []groupDef {
	return []groupDef{{
		name: "invoice",
		steps: []stepDef{
			// The invoice module has no direct create route; the ERP mints
			// invoices on order fulfilment. Both steps are 204 No Content.
			{name: "order.confirm", method: "POST", path: "/api/v1/orders/{myOrder}/confirm"},
			{name: "order.fulfill", method: "POST", path: "/api/v1/orders/{myOrder}/fulfill"},
			// Newest invoice is the one fulfilment just created.
			{name: "invoice.list_newest", method: "GET", path: "/api/v1/invoices?limit=1",
				extract: map[string]string{"myInvoice": "/data/0/id"}},
			{name: "invoice.get", method: "GET", path: "/api/v1/invoices/{myInvoice}"},
			{
				name:   "invoice.credit_memo.create",
				method: "POST",
				path:   "/api/v1/invoices/{myInvoice}/credit-memo",
				body:   map[string]any{"amount_cents": 500, "reason": "golden characterisation"},
			},
			{name: "invoice.credit_memo.list", method: "GET", path: "/api/v1/credit-memos/{myCustomer}"},
			{name: "invoice.get.not_found", method: "GET", path: "/api/v1/invoices/00000000-0000-0000-0000-0000000000aa"},
		},
	}}
}

func paymentGroups() []groupDef {
	return []groupDef{{
		name: "payment",
		steps: []stepDef{
			{
				name:   "payment.create",
				method: "POST",
				path:   "/api/v1/payments",
				body: map[string]any{
					"invoice_id": "{myInvoice}", "amount": 1000, "method": "CASH",
					"reference": "GOLD-PAY-1",
				},
				extract: map[string]string{"myPayment": "/id"},
			},
			{name: "payment.history", method: "GET", path: "/api/v1/invoices/{myInvoice}/payments"},
			// No Run Payments public key is configured: the gateway intent
			// answers 503, which is its configured-state behaviour.
			{name: "payment.intent.no_gateway", method: "POST", path: "/api/v1/payments/intent",
				body: map[string]any{"invoice_id": "{myInvoice}", "amount": 100}},
		},
	}}
}

func accountGroups() []groupDef {
	return []groupDef{{
		name: "account",
		steps: []stepDef{
			{name: "account.summary", method: "GET", path: "/api/v1/accounts/{myCustomer}"},
			// The customer was created by this run: pins the nullable list.
			{name: "account.transactions", method: "GET", path: "/api/v1/accounts/{myCustomer}/transactions"},
		},
	}}
}

func depositGroups() []groupDef {
	return []groupDef{{
		name: "deposit",
		steps: []stepDef{
			{
				name:   "deposit.create",
				method: "POST",
				path:   "/api/v1/deposits",
				body: map[string]any{
					"customer_id": "{myCustomer}", "branch_id": "{branch}", "amount_cents": 25000,
					"method": "CHECK", "reference": "GOLD-DEP-1",
				},
				extract: map[string]string{"myDeposit": "/id"},
			},
			{name: "deposit.get", method: "GET", path: "/api/v1/deposits/{myDeposit}"},
			{name: "deposit.create.bad_amount", method: "POST", path: "/api/v1/deposits",
				body: map[string]any{"customer_id": "{myCustomer}", "amount_cents": 0}},
		},
	}}
}

func taxGroups() []groupDef {
	return []groupDef{{
		name: "tax",
		steps: []stepDef{
			{name: "tax.exemption.list_empty", method: "GET", path: "/api/v1/tax/exemptions/{myCustomer}"},
			{
				name:   "tax.exemption.create",
				method: "POST",
				path:   "/api/v1/tax/exemptions",
				body: map[string]any{
					"customer_id": "{myCustomer}", "exempt_reason": "RESALE",
					"certificate_number": "GOLD-CERT-1", "issuing_state": "BC",
				},
				extract: map[string]string{"myExemption": "/id"},
			},
			{
				name:   "tax.preview",
				method: "POST",
				path:   "/api/v1/tax/preview",
				body: map[string]any{
					"customer_id": "{myCustomer}",
					"ship_from": map[string]any{
						"line1": "2450 Enterprise Way", "city": "Kelowna", "state": "BC",
						"postal_code": "V1X 7K2", "country": "CA",
					},
					"ship_to": map[string]any{
						"line1": "1885 Formwork Rd", "city": "Kelowna", "state": "BC",
						"postal_code": "V1Y 4X1", "country": "CA",
					},
					"lines": []map[string]any{{
						"line_number": 1, "item_code": "LUM-248-PREM", "description": "2x4x8 SPF",
						"quantity": 5, "amount": 2750, "tax_code": "PF",
					}},
				},
			},
		},
	}}
}

func vendorGroups() []groupDef {
	return []groupDef{{
		name: "vendor",
		steps: []stepDef{
			{
				name:    "vendor.create",
				method:  "POST",
				path:    "/api/v1/vendors",
				body:    map[string]any{"name": "Golden Vendor Co", "payment_terms": "NET15"},
				extract: map[string]string{"myVendor": "/id"},
			},
			{name: "vendor.get", method: "GET", path: "/api/v1/vendors/{myVendor}"},
			{name: "vendor.list", method: "GET", path: "/api/v1/vendors?limit=3"},
		},
	}}
}

func purchaseOrderGroups() []groupDef {
	return []groupDef{{
		name: "purchase_order",
		steps: []stepDef{
			{
				name:   "purchase_order.create",
				method: "POST",
				path:   "/api/v1/purchase-orders",
				body: map[string]any{
					"vendor_id": "{vendor}",
					"lines": []map[string]any{
						{"product_id": "{productSheet}", "description": "golden sheet goods", "quantity": 20, "cost": 24.0},
					},
				},
				extract: map[string]string{"myPO": "/id"},
			},
			{name: "purchase_order.get", method: "GET", path: "/api/v1/purchase-orders/{myPO}"},
			{name: "purchase_order.create.bad_vendor", method: "POST", path: "/api/v1/purchase-orders",
				body: map[string]any{"vendor_id": "not-a-uuid", "lines": []map[string]any{}}},
		},
	}}
}

func deliveryGroups() []groupDef {
	return []groupDef{{
		name: "delivery",
		steps: []stepDef{
			{
				name:   "delivery.vehicle.create",
				method: "POST",
				path:   "/api/v1/delivery/vehicles",
				body: map[string]any{
					"name": "Golden Truck 01", "vehicle_type": "BOX_TRUCK", "license_plate": "GLD0001",
					"capacity_weight_lbs": 5000,
				},
				extract: map[string]string{"myVehicle": "/id"},
			},
			{name: "delivery.vehicle.list", method: "GET", path: "/api/v1/delivery/vehicles"},
			{
				name:   "delivery.driver.create",
				method: "POST",
				path:   "/api/v1/delivery/drivers",
				body: map[string]any{
					"name": "Golden Driver", "license_number": "GLD-DL-1", "phone_number": "250-555-0198",
				},
				extract: map[string]string{"myDriver": "/id"},
			},
			{name: "delivery.driver.get", method: "GET", path: "/api/v1/delivery/drivers/{myDriver}"},
		},
	}}
}

func inventoryGroups() []groupDef {
	return []groupDef{{
		name: "inventory",
		steps: []stepDef{
			// New product from this run has no stock rows: the module's
			// nullable-array shape is part of the contract.
			{name: "inventory.list_empty", method: "GET", path: "/api/v1/inventory?product_id={myProduct}"},
			{
				name:   "inventory.adjust",
				method: "POST",
				path:   "/api/v1/inventory/adjust",
				body: map[string]any{
					"product_id": "{productSheet}", "quantity": 25,
					"reason": "golden characterisation", "is_delta": true,
				},
			},
			{name: "inventory.list_missing_param", method: "GET", path: "/api/v1/inventory"},
		},
	}}
}

func documentGroups() []groupDef {
	return []groupDef{{
		name: "document",
		steps: []stepDef{
			// PDF: hashed, not inlined (see normaliseBytes in golden_test.go).
			{name: "document.print_pickticket", method: "GET", path: "/api/v1/documents/print/pickticket/{myOrder}"},
			{name: "document.print_not_found", method: "GET",
				path: "/api/v1/documents/print/pickticket/00000000-0000-0000-0000-0000000000aa"},
		},
	}}
}

func apGroups() []groupDef {
	return []groupDef{{
		name: "ap",
		steps: []stepDef{
			{
				name:   "ap.invoice.create",
				method: "POST",
				path:   "/api/v1/ap/invoices",
				body: map[string]any{
					"vendor_id": "{vendor}", "invoice_number": "GOLD-AP-001",
					"invoice_date": "{today}", "due_date": "{today}", "tax_amount": 0,
					"lines": []map[string]any{
						{"description": "golden ap line", "quantity": 1, "unit_price": 100.0},
					},
				},
				extract: map[string]string{"myAPInvoice": "/id"},
			},
			{name: "ap.invoice.get", method: "GET", path: "/api/v1/ap/invoices/{myAPInvoice}"},
			{name: "ap.invoice.list", method: "GET", path: "/api/v1/ap/invoices?limit=3"},
		},
	}, {
		name: "edi",
		steps: []stepDef{
			{
				name:   "edi.partner.create",
				method: "POST",
				path:   "/api/v1/edi/partners",
				body: map[string]any{
					"name": "Golden EDI Partner", "isa_sender_id": "GOLDSENDER",
					"isa_receiver_id": "GOLDRECEIVER",
				},
				extract: map[string]string{"myEDIPartner": "/id"},
			},
			{name: "edi.partner.get", method: "GET", path: "/api/v1/edi/partners/{myEDIPartner}"},
			{name: "edi.partner.create.missing_name", method: "POST", path: "/api/v1/edi/partners", body: map[string]any{}},
		},
	}}
}
