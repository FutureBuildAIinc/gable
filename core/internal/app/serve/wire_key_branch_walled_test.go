// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The WALLED class as a tested claim (the lead's ruling on item 37): every
// WALLED entry names the column or join its module filters on, and this sweep
// drives every WALLED route that takes an id (or a list) with branch B's row
// as the target of a key bound to branch A. The foreign row must answer
// exactly what a missing row answers (or a plain 403), the answer must never
// name the foreign row, nothing may be written (the family tables are counted
// before and after), and the unbound key must answer differently for the twin
// and for a missing row, so the twin is a real row the wall holds back. The
// lists carry the pin's branch's rows only. The pos family is driven the same
// way by TestKeyBranchPin_PosAndAccountRows.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// walledTwins is one row per branch for every walled family the sweep drives.
type walledTwins struct {
	ordA, ordB                                     uuid.UUID
	invA, invB                                     uuid.UUID
	memoA, memoB                                   uuid.UUID
	payA, payB                                     uuid.UUID
	actA, actB                                     uuid.UUID
	conA, conB                                     uuid.UUID
	delA, delB                                     uuid.UUID
	routeA, routeB                                 uuid.UUID
	draftQA, draftQB, draftOA, draftOB             uuid.UUID
	poA, poB, poLineA, poLineB, freightA, freightB uuid.UUID
	appA, appB                                     uuid.UUID
	shipA, shipB                                   uuid.UUID
}

// seedWalledTwins seeds the twin rows with their cleanup.
func (f *keyPinFixture) seedWalledTwins(t *testing.T) *walledTwins {
	t.Helper()
	ctx := context.Background()
	w := &walledTwins{
		ordA: uuid.New(), ordB: uuid.New(), invA: uuid.New(), invB: uuid.New(),
		memoA: uuid.New(), memoB: uuid.New(), payA: uuid.New(), payB: uuid.New(),
		actA: uuid.New(), actB: uuid.New(), conA: uuid.New(), conB: uuid.New(),
		delA: uuid.New(), delB: uuid.New(), routeA: uuid.New(), routeB: uuid.New(),
		draftQA: uuid.New(), draftQB: uuid.New(), draftOA: uuid.New(), draftOB: uuid.New(),
		poA: uuid.New(), poB: uuid.New(), poLineA: uuid.New(), poLineB: uuid.New(),
		freightA: uuid.New(), freightB: uuid.New(), appA: uuid.New(), appB: uuid.New(),
		shipA: uuid.New(), shipB: uuid.New(),
	}
	vendor := uuid.New()
	vendorName := "pin twin vendor " + vendor.String()[:8]
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, vendorName); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	// The seeds below this point can fail before the cleanup is registered,
	// so the vendor carries its own cleanup from the moment it exists.
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})
	for _, o := range []struct {
		id, cust, branch, inv, pay, act, con, ship uuid.UUID
	}{
		{w.ordA, f.custID, f.branchA, w.invA, w.payA, w.actA, w.conA, w.shipA},
		{w.ordB, f.custB, f.branchB, w.invB, w.payB, w.actB, w.conB, w.shipB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
			 VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`, o.id, o.cust, o.branch); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO invoices (id, order_id, customer_id, status, total_amount, branch_id)
			 VALUES ($1, $2, $3, 'UNPAID', 10, $4)`, o.inv, o.id, o.cust, o.branch); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO payments (id, customer_id, branch_id, currency, amount, method, status, received_on, amount_unapplied)
			 VALUES ($1, $2, $3, 'USD', 5, 'CASH', 'POSTED', CURRENT_DATE, 5)`, o.pay, o.cust, o.branch); err != nil {
			t.Fatalf("seed payment: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO crm_activities (id, customer_id, activity_type, description, activity_date)
			 VALUES ($1, $2, 'CALL', 'pin twin', now())`, o.act, o.cust); err != nil {
			t.Fatalf("seed activity: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO customer_contacts (id, customer_id, first_name, last_name)
			 VALUES ($1, $2, 'pin', 'twin')`, o.con, o.cust); err != nil {
			t.Fatalf("seed contact: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'twin', 'pin twin', 'pin')`,
			o.ship, o.cust); err != nil {
			t.Fatalf("seed ship to: %v", err)
		}
	}
	for _, m := range []struct{ id, inv, cust, branch uuid.UUID }{
		{w.memoA, w.invA, f.custID, f.branchA}, {w.memoB, w.invB, f.custB, f.branchB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO credit_memos (id, invoice_id, customer_id, branch_id, currency, reason_code, reason, amount, status, number,
				 memo_date, subtotal, tax_amount, total_amount, tax_rate)
			 VALUES ($1, $2, $3, $4, 'USD', 'OTHER', 'pin twin', 1, 'OPEN', credit_memo_next_number(), CURRENT_DATE, -1, 0, -1, 0)`,
			m.id, m.inv, m.cust, m.branch); err != nil {
			t.Fatalf("seed credit memo: %v", err)
		}
	}
	for _, r := range []struct{ id uuid.UUID }{{w.routeA}, {w.routeB}} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO delivery_routes (id, status, scheduled_date) VALUES ($1, 'DRAFT', CURRENT_DATE)`, r.id); err != nil {
			t.Fatalf("seed route: %v", err)
		}
	}
	for _, d := range []struct{ id, route, ord uuid.UUID }{
		{w.delA, w.routeA, w.ordA}, {w.delB, w.routeB, w.ordB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO deliveries (id, route_id, order_id, status) VALUES ($1, $2, $3, 'PENDING')`,
			d.id, d.route, d.ord); err != nil {
			t.Fatalf("seed delivery: %v", err)
		}
	}
	for _, d := range []struct {
		id, branch uuid.UUID
		module     string
	}{
		{w.draftQA, f.branchA, "quotes"}, {w.draftQB, f.branchB, "quotes"},
		{w.draftOA, f.branchA, "orders"}, {w.draftOB, f.branchB, "orders"},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO drafts (id, module, branch_id, status, payload, created_by_kind, updated_by_kind)
			 VALUES ($1, $2, $3, 'OPEN', '{}', 'user', 'user')`, d.id, d.module, d.branch); err != nil {
			t.Fatalf("seed draft: %v", err)
		}
	}
	for _, po := range []struct {
		id, line, branch uuid.UUID
	}{
		{w.poA, w.poLineA, f.branchA}, {w.poB, w.poLineB, f.branchB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', $3)`,
			po.id, vendor, po.branch); err != nil {
			t.Fatalf("seed purchase order: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost) VALUES ($1, $2, $3, 'pin twin', 5, 1)`,
			po.line, po.id, f.posProd); err != nil {
			t.Fatalf("seed purchase order line: %v", err)
		}
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, 'pin', 7), ($1, $3, 'pin', 9)`,
		f.posProd, f.yardA, f.yardB); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	for _, a := range []struct {
		id, cust, inv, pay uuid.UUID
	}{
		{w.appA, f.custID, w.invA, w.payA}, {w.appB, f.custB, w.invB, w.payB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO ar_applications (id, customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, act_id)
			 VALUES ($1, $2, 'USD', 'PAYMENT', $3, $4, 1, CURRENT_DATE, $1)`, a.id, a.cust, a.pay, a.inv); err != nil {
			t.Fatalf("seed application: %v", err)
		}
	}
	t.Cleanup(func() {
		exec := func(stmt string, args ...any) {
			_, _ = f.db.Pool.Exec(ctx, stmt, args...)
		}
		exec(`DELETE FROM inventory WHERE product_id = $1 AND location_id IN ($2, $3)`, f.posProd, f.yardA, f.yardB)
		exec(`DELETE FROM ar_applications WHERE id IN ($1, $2)`, w.appA, w.appB)
		exec(`DELETE FROM purchase_order_lines WHERE po_id IN ($1, $2)`, w.poA, w.poB)
		exec(`DELETE FROM purchase_orders WHERE id IN ($1, $2)`, w.poA, w.poB)
		exec(`DELETE FROM drafts WHERE id IN ($1, $2, $3, $4)`, w.draftQA, w.draftQB, w.draftOA, w.draftOB)
		exec(`DELETE FROM deliveries WHERE id IN ($1, $2) OR route_id IN ($3, $4)`, w.delA, w.delB, w.routeA, w.routeB)
		exec(`DELETE FROM delivery_routes WHERE id IN ($1, $2)`, w.routeA, w.routeB)
		exec(`DELETE FROM credit_memos WHERE id IN ($1, $2)`, w.memoA, w.memoB)
		exec(`DELETE FROM customer_ship_tos WHERE id IN ($1, $2)`, w.shipA, w.shipB)
		exec(`DELETE FROM customer_contacts WHERE id IN ($1, $2)`, w.conA, w.conB)
		exec(`DELETE FROM crm_activities WHERE id IN ($1, $2)`, w.actA, w.actB)
		exec(`DELETE FROM payments WHERE id IN ($1, $2)`, w.payA, w.payB)
		exec(`DELETE FROM invoices WHERE id IN ($1, $2)`, w.invA, w.invB)
		exec(`DELETE FROM orders WHERE id IN ($1, $2)`, w.ordA, w.ordB)
		exec(`DELETE FROM vendors WHERE id = $1`, vendor)
	})
	return w
}

// familyCounts reads the row counts of every table the sweep's write legs
// could touch, so a confined write that still wrote shows as a diff.
func (f *keyPinFixture) familyCounts(t *testing.T) map[string]int {
	t.Helper()
	tables := []string{"orders", "invoices", "credit_memos", "payments", "crm_activities", "customer_contacts",
		"deliveries", "delivery_routes", "drafts", "purchase_orders", "purchase_order_lines",
		"po_freight_charges", "inventory", "quotes", "customer_ship_tos", "ar_applications"}
	out := map[string]int{}
	for _, tb := range tables {
		var n int
		if err := f.db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tb] = n
	}
	return out
}

// TestKeyBranchPin_WalledRowsHoldThePin drives every WALLED route that takes
// an id (or a list) with branch B's twin row: the bound key's answer for the
// foreign row equals its answer for a missing row (or a 403 or 404) and never
// names the row, nothing is written anywhere the families write, and the
// unbound key tells the twin from a missing row, so the twin is real. The
// lists carry the pin's branch only.
func TestKeyBranchPin_WalledRowsHoldThePin(t *testing.T) {
	f := newKeyPinFixture(t)
	// The sweep's key is the document family mint: the families it drives
	// live in the second scope list.
	f.bound, f.unbound, f.boundKeyID = f.bound2, f.unbound2, f.bound2KeyID
	w := f.seedWalledTwins(t)
	missing := uuid.NewString()
	A, B := f.branchA.String(), f.branchB.String()

	// Every by id route with the foreign twin it drives.
	// The routes whose user answer for the twin is not a plain 200: a quote
	// with no stored file answers its own 404, and the exposure gate answers
	// a missing order rather than echoing it.
	noRealUserAnswer := map[string]bool{
		"/api/v1/quotes/%s/file":             true,
		"/api/v1/orders/%s/exposure-gate":    true,
		"/api/v1/matching/results/%s":        true, // no match results are seeded, both rows answer 404
		"/api/v1/purchase-orders/%s/freight": true, // an empty freight list answers 200 for any order
	}
	idRoutes := []struct {
		method, path, body string
		twin               uuid.UUID
	}{
		{"GET", "/api/v1/quotes/%s", "", f.quoteB},
		{"PUT", "/api/v1/quotes/%s", `{}`, f.quoteB},
		{"POST", "/api/v1/quotes/%s/convert", `{}`, f.quoteB},
		{"POST", "/api/v1/quotes/%s/transitions", `{"action":"send"}`, f.quoteB},
		{"GET", "/api/v1/quotes/%s/file", "", f.quoteB},
		{"PUT", "/api/v1/quotes/%s/file", `{}`, f.quoteB},
		{"GET", "/api/v1/orders/%s", "", w.ordB},
		{"PUT", "/api/v1/orders/%s", `{}`, w.ordB},
		{"GET", "/api/v1/orders/%s/exposure-gate", "", w.ordB},
		{"POST", "/api/v1/orders/%s/allocate", `{}`, w.ordB},
		{"POST", "/api/v1/orders/%s/exposure-override", `{"notes":"pin"}`, w.ordB},
		{"POST", "/api/v1/orders/%s/fulfillments", `{}`, w.ordB},
		{"POST", "/api/v1/orders/%s/transitions", `{"action":"confirm"}`, w.ordB},
		{"POST", "/api/v1/orders/fulfillment-requests/%s/retry", "", w.delB},
		{"GET", "/api/v1/invoices/%s", "", w.invB},
		{"GET", "/api/v1/invoices/%s/payments", "", w.invB},
		{"POST", "/api/v1/invoices/%s/email", `{}`, w.invB},
		{"POST", "/api/v1/invoices/%s/transitions", `{"action":"send"}`, w.invB},
		{"POST", "/api/v1/invoices/%s/write-offs", `{"amount_cents":1,"reason":"pin"}`, w.invB},
		{"GET", "/api/v1/documents/print/invoice/%s", "", w.invB},
		{"GET", "/api/v1/documents/print/pickticket/%s", "", w.ordB},
		{"GET", "/api/v1/payments/%s", "", w.payB},
		{"POST", "/api/v1/payments/%s/applications", `{}`, w.payB},
		{"POST", "/api/v1/payments/%s/refunds", `{}`, w.payB},
		{"POST", "/api/v1/payments/%s/transitions", `{}`, w.payB},
		{"POST", "/api/v1/ar/applications/%s/reverse", `{}`, w.appB},
		{"GET", "/api/v1/credit-memos/%s", "", w.memoB},
		{"PUT", "/api/v1/credit-memos/%s", `{}`, w.memoB},
		{"POST", "/api/v1/credit-memos/%s/applications", `{}`, w.memoB},
		{"POST", "/api/v1/credit-memos/%s/refunds", `{}`, w.memoB},
		{"POST", "/api/v1/credit-memos/%s/transitions", `{}`, w.memoB},
		{"GET", "/api/v1/activities/%s", "", w.actB},
		{"PUT", "/api/v1/activities/%s", `{}`, w.actB},
		{"DELETE", "/api/v1/activities/%s", "", w.actB},
		{"GET", "/api/v1/contacts/%s", "", w.conB},
		{"PUT", "/api/v1/contacts/%s", `{}`, w.conB},
		{"DELETE", "/api/v1/contacts/%s", "", w.conB},
		{"GET", "/api/v1/ship-tos/%s", "", w.shipB},
		{"PUT", "/api/v1/ship-tos/%s", `{}`, w.shipB},
		{"GET", "/api/v1/drafts/quotes/%s", "", w.draftQB},
		{"PUT", "/api/v1/drafts/quotes/%s", `{}`, w.draftQB},
		{"POST", "/api/v1/drafts/quotes/%s/promote", `{}`, w.draftQB},
		{"POST", "/api/v1/drafts/quotes/%s/transitions", `{}`, w.draftQB},
		{"GET", "/api/v1/drafts/orders/%s", "", w.draftOB},
		{"PUT", "/api/v1/drafts/orders/%s", `{}`, w.draftOB},
		{"POST", "/api/v1/drafts/orders/%s/promote", `{}`, w.draftOB},
		{"POST", "/api/v1/drafts/orders/%s/transitions", `{}`, w.draftOB},
		{"GET", "/api/v1/links/quotes/%s", "", f.quoteB},
		{"GET", "/api/v1/links/orders/%s", "", w.ordB},
		{"GET", "/api/v1/links/invoices/%s", "", w.invB},
		{"GET", "/api/v1/links/customers/%s", "", f.custB},
		{"GET", "/api/v1/links/drafts/quotes/%s", "", w.draftQB},
		{"GET", "/api/v1/links/drafts/orders/%s", "", w.draftOB},
		{"GET", "/api/v1/delivery/deliveries/%s", "", w.delB},
		{"GET", "/api/v1/delivery/deliveries/%s/pod-photos", "", w.delB},
		{"POST", "/api/v1/delivery/deliveries/%s/adjust-qty", `{}`, w.delB},
		{"POST", "/api/v1/delivery/deliveries/%s/pod-photo", `{}`, w.delB},
		{"POST", "/api/v1/delivery/deliveries/%s/transitions", `{"status":"COMPLETED"}`, w.delB},
		{"GET", "/api/v1/delivery/routes/%s", "", w.routeB},
		{"GET", "/api/v1/delivery/routes/%s/deliveries", "", w.routeB},
		{"POST", "/api/v1/delivery/routes/%s/optimize", "", w.routeB},
		{"POST", "/api/v1/delivery/routes/%s/reorder", "", w.routeB},
		{"POST", "/api/v1/delivery/routes/%s/transitions", `{"status":"PLANNED"}`, w.routeB},
		{"GET", "/api/v1/matching/results/%s", "", w.poB},
		{"POST", "/api/v1/matching/run/%s", "", w.poB},
		{"GET", "/api/v1/purchase-orders/%s", "", w.poB},
		{"GET", "/api/v1/purchase-orders/%s/freight", "", w.poB},
		{"POST", "/api/v1/purchase-orders/%s/freight", `{"carrier":"pin","cost_cents":1}`, w.poB},
		{"POST", "/api/v1/purchase-orders/%s/freight/%s/apply", "", w.freightB},
		{"POST", "/api/v1/purchase-orders/%s/receive", `{}`, w.poB},
		{"POST", "/api/v1/purchase-orders/%s/submit", "", w.poB},
	}
	before := f.familyCounts(t)
	for _, c := range idRoutes {
		var bPath, mPath string
		if strings.Count(c.path, "%s") == 2 {
			// The freight apply route names the order and the freight quote.
			bPath = fmt.Sprintf(c.path, w.poB, w.freightB)
			mPath = fmt.Sprintf(c.path, uuid.MustParse(missing), uuid.MustParse(missing))
		} else {
			bPath = fmt.Sprintf(c.path, c.twin)
			mPath = fmt.Sprintf(c.path, uuid.MustParse(missing))
		}
		sb, bb := f.key(t, f.bound, c.method, bPath, c.body, nil)
		sm, _ := f.key(t, f.bound, c.method, mPath, c.body, nil)
		if sb != sm && sb != http.StatusForbidden && sb != http.StatusNotFound {
			t.Errorf("bound key %s %s: %d %s for the foreign row, want the missing row answer (%d), a 403 or a 404",
				c.method, bPath, sb, bb, sm)
		}
		if strings.Contains(string(bb), c.twin.String()) {
			t.Errorf("bound key %s %s: the answer names the foreign row: %s", c.method, bPath, bb)
		}
		// The twin is real: a user reads it where a missing row is a 404 (the
		// write legs prove the same rows exist through their family tables).
		if c.method == "GET" && !noRealUserAnswer[c.path] {
			ub, ubuf := f.user(t, c.method, bPath, c.body, nil)
			um, _ := f.user(t, c.method, mPath, c.body, nil)
			if ub != http.StatusOK || um != http.StatusNotFound {
				t.Errorf("user %s %s: %d for the twin and %d for a missing row, want 200 and 404 (%s)",
					c.method, bPath, ub, um, ubuf)
			}
		}
	}
	after := f.familyCounts(t)
	for tb, n := range before {
		if after[tb] != n {
			t.Errorf("table %s: %d rows before the sweep, %d after, want no writes", tb, n, after[tb])
		}
	}

	// The lists carry the pin's branch only: the foreign twin ids never
	// appear, the pin's own do, and the unbound key sees the list too.
	lists := []struct {
		path            string
		absent, present []string
	}{
		{"/api/v1/customers", []string{f.custB.String()}, []string{f.custID.String()}},
		{"/api/v1/credit-memos", []string{w.memoB.String()}, []string{w.memoA.String()}},
		{"/api/v1/invoices", []string{w.invB.String()}, []string{w.invA.String()}},
		{"/api/v1/payments", []string{w.payB.String()}, []string{w.payA.String()}},
		{"/api/v1/quotes", []string{f.quoteB.String()}, []string{f.quoteA.String()}},
		{"/api/v1/orders", []string{w.ordB.String()}, []string{w.ordA.String()}},
		{"/api/v1/orders/fulfillment-requests", []string{w.ordB.String()}, nil},
		{"/api/v1/drafts/quotes", []string{w.draftQB.String()}, []string{w.draftQA.String()}},
		{"/api/v1/drafts/orders", []string{w.draftOB.String()}, []string{w.draftOA.String()}},
		{"/api/v1/purchase-orders", []string{w.poB.String()}, []string{w.poA.String()}},
		{"/api/v1/purchase-orders/recommendations", []string{}, nil},
		{"/api/v1/purchase-orders/reorder-runs", []string{}, nil},
		{"/api/v1/purchase-orders/source-summary", []string{}, nil},
		{"/api/v1/delivery/routes", []string{w.routeB.String()}, []string{w.routeA.String()}},
		{"/api/v1/matching/exceptions", []string{w.poB.String()}, nil},
		{"/api/v1/customers/" + f.custID.String() + "/activities", []string{w.actB.String()}, []string{w.actA.String()}},
		{"/api/v1/customers/" + f.custID.String() + "/contacts", []string{w.conB.String()}, []string{w.conA.String()}},
		{"/api/v1/customers/" + f.custID.String() + "/ship-tos", []string{w.shipB.String()}, []string{w.shipA.String()}},
		{"/api/v1/dashboard/summary", []string{f.custB.String()}, nil},
		{"/api/v1/dashboard/top-customers", []string{f.custB.String()}, nil},
		{"/api/v1/dashboard/order-activity", []string{w.ordB.String()}, nil},
		{"/api/v1/dashboard/revenue-trend", []string{w.invB.String()}, nil},
		{"/api/v1/dashboard/inventory-alerts", []string{f.yardB.String()}, nil},
		{"/api/v1/inventory", []string{f.yardB.String()}, nil},
		{"/api/v1/locations", []string{f.yardB.String()}, nil},
		{"/api/v1/products/reorder-alerts", []string{f.yardB.String()}, nil},
	}
	for _, l := range lists {
		sb, bb := f.key(t, f.bound, "GET", l.path, "", nil)
		if sb != http.StatusOK {
			t.Errorf("bound key GET %s: %d %s, want 200", l.path, sb, bb)
			continue
		}
		for _, id := range l.absent {
			if strings.Contains(string(bb), id) {
				t.Errorf("bound key GET %s: the list carries the foreign row %s", l.path, id)
			}
		}
		for _, id := range l.present {
			if !strings.Contains(string(bb), id) {
				t.Errorf("bound key GET %s: the list does not carry the pin's own row %s", l.path, id)
			}
		}
		if sk, _ := f.key(t, f.unbound, "GET", l.path, "", nil); sk != http.StatusOK {
			t.Errorf("unbound key GET %s: %d, want 200", l.path, sk)
		}
	}

	// The branch tree and the location row read hold the same way: the
	// foreign branch's tree and yard are refused or missing, the pin's own
	// answer. The tree's refusal is the branch middleware's own (the wall is
	// not what refuses a path branch on these mounts).
	if sb, _ := f.key(t, f.bound, "GET", "/api/v1/branches/"+B+"/tree", "", nil); sb != http.StatusForbidden {
		t.Errorf("bound key foreign branch tree: %d, want 403", sb)
	}
	f.allow(t, "bound", "GET", "/api/v1/branches/"+A+"/tree", "", nil, http.StatusOK)
	if sb, bb := f.key(t, f.bound, "GET", "/api/v1/locations/"+f.yardB.String(), "", nil); sb != http.StatusForbidden && sb != http.StatusNotFound {
		t.Errorf("bound key foreign yard read: %d %s, want 403 or 404", sb, bb)
	}
	f.allow(t, "bound", "GET", "/api/v1/locations/"+f.yardA.String(), "", nil, http.StatusOK)

	// The inventory adjust and transfer bodies name locations: the foreign
	// yard is refused (the branch guard's own refusal) with nothing written.
	beforeInv := f.familyCounts(t)["inventory"]
	if sb, bb := f.key(t, f.bound, "POST", "/api/v1/inventory/adjust",
		fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":5,"reason":"pin"}`, f.posProd.String(), f.yardB.String()), nil); sb != http.StatusForbidden {
		t.Errorf("bound key inventory adjust naming the foreign yard: %d %s, want 403", sb, bb)
	}
	if sb, bb := f.key(t, f.bound, "POST", "/api/v1/inventory/transfer",
		fmt.Sprintf(`{"product_id":%q,"from_location_id":%q,"to_location_id":%q,"quantity":1,"reason":"pin"}`,
			f.posProd.String(), f.yardA.String(), f.yardB.String()), nil); sb != http.StatusForbidden {
		t.Errorf("bound key inventory transfer into the foreign yard: %d %s, want 403", sb, bb)
	}
	if n := f.familyCounts(t)["inventory"]; n != beforeInv {
		t.Errorf("refused inventory writes moved rows: %d then %d", beforeInv, n)
	}
}
