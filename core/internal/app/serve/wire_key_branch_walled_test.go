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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/platform/httpx"
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
	vendor                                         uuid.UUID
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
	w.vendor = vendor
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
	for _, fr := range []struct{ id, po uuid.UUID }{{w.freightA, w.poA}, {w.freightB, w.poB}} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO po_freight_charges (id, po_id, carrier_name, invoice_number, total_amount_cents, status)
			 VALUES ($1, $2, 'pin carrier', 'PIN-FRT', 100, 'PENDING')`, fr.id, fr.po); err != nil {
			t.Fatalf("seed freight charge: %v", err)
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
		exec(`DELETE FROM po_freight_charges WHERE po_id IN ($1, $2)`, w.poA, w.poB)
		exec(`DELETE FROM purchase_order_lines WHERE po_id IN ($1, $2)`, w.poA, w.poB)
		exec(`DELETE FROM purchase_orders WHERE id IN ($1, $2)`, w.poA, w.poB)
		exec(`DELETE FROM drafts WHERE id IN ($1, $2, $3, $4)`, w.draftQA, w.draftQB, w.draftOA, w.draftOB)
		for _, child := range []string{"delivery_pod_photos", "delivery_qty_adjustments", "delivery_notifications", "order_fulfillment_requests"} {
			exec(`DELETE FROM `+child+` WHERE delivery_id IN (SELECT id FROM deliveries WHERE id IN ($1, $2) OR route_id IN ($3, $4))`, w.delA, w.delB, w.routeA, w.routeB)
		}
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

// TestKeyBranchPin_WalledRowsHoldThePin drives every WALLED route that takes
// an id (or a list) with branch B's twin row: the bound key's answer for the
// foreign row equals its answer for a missing row (or a 403 or 404) and never
// names the row, nothing is written anywhere the families write, and the
// unbound key tells the twin from a missing row, so the twin is real. The
// lists carry the pin's branch only.
func TestKeyBranchPin_WalledRowsHoldThePin(t *testing.T) {
	f := newKeyPinFixture(t)
	// The sweep's key is the document family mint: the families it drives
	// live in the second scope list. The first mint keeps the pos and AR scopes.
	firstBound := f.bound
	f.bound, f.unbound, f.boundKeyID = f.bound2, f.unbound2, f.bound2KeyID
	w := f.seedWalledTwins(t)
	missing := uuid.NewString()
	A, B := f.branchA.String(), f.branchB.String()

	f.record()
	defer driverDone(t, "sweep")
	f.boundFirst = firstBound
	line, qline := f.productLine(), f.quoteLine()
	ownCust := func(row uuid.UUID) string {
		if row == w.ordB || row == w.memoB || row == w.payB || row == w.invB {
			return f.custB.String()
		}
		return f.custID.String()
	}
	quotePut := func(uuid.UUID) string {
		return fmt.Sprintf(`{"customer_id":%q,"revision":1,"lines":[%s]}`, f.custID.String(), qline)
	}
	orderPut := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","revision":1,"lines":[%s]}`, ownCust(row), line)
	}
	creditPut := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"customer_id":%q,"reason_code":"other","reason":"pin","revision":1,"lines":[`+
			`{"description":"pin adjustment","quantity":"-1","uom":"EA","unit_price_ten_thousandths":10000}]}`, ownCust(row))
	}
	activityPut := func(uuid.UUID) string {
		return `{"activity_type":"call","description":"pin twin","revision":1}`
	}
	contactPut := func(uuid.UUID) string {
		return `{"first_name":"pin","last_name":"twin","can_place_orders":true,"order_limit_cents":null,"is_primary":false,"is_active":true,"revision":1}`
	}
	shipToPut := func(uuid.UUID) string {
		return `{"code":"twin","name":"pin twin","line1":"pin","revision":1}`
	}
	draftPut := func(payloadLine string) func(uuid.UUID) string {
		return func(uuid.UUID) string {
			return fmt.Sprintf(`{"revision":1,"payload":{"customer_id":%q,"lines":[%s]}}`, f.custID.String(), payloadLine)
		}
	}
	fixed := func(s string) func(uuid.UUID) string { return func(uuid.UUID) string { return s } }
	ifMatch := map[string]string{"If-Match": `"1"`}
	reorderBody := func(row uuid.UUID) string {
		d := w.delA
		if row == w.routeB {
			d = w.delB
		}
		return fmt.Sprintf(`{"ordered_delivery_ids":[%q]}`, d.String())
	}
	// The proof of delivery control stores the photo under uploads/ in the
	// package directory; the test removes what it made.
	if _, err := os.Stat("uploads"); os.IsNotExist(err) {
		t.Cleanup(func() { _ = os.RemoveAll("uploads") })
	}
	podPhoto, podType := multipartFile("photo", "pin.png", "pin photo")
	freightFile, freightType := multipartFile("file", "pin.pdf", "pin freight invoice")
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE customers SET email = 'pin@example.com' WHERE id IN ($1, $2)`, f.custID, f.custB); err != nil {
		t.Fatalf("seed customer email: %v", err)
	}

	// The cashier transaction the complete and void legs act on: one extra
	// open transaction at the pin's branch, so the completion does not spend
	// the fixture's.
	txnA2 := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id, till_session_id, currency)
		 SELECT $1, register_id, cashier_id, 'OPEN', branch_id, till_session_id, currency FROM pos_transactions WHERE id = $2`, txnA2, f.txnA); err != nil {
		t.Fatalf("seed the second open transaction: %v", err)
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE pos_transactions SET subtotal = 1, total = 1 WHERE id IN ($1, $2, $3)`, f.txnA, f.txnB, txnA2); err != nil {
		t.Fatalf("seed the transaction totals: %v", err)
	}
	vehicle, driver := uuid.New(), uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'pin truck', 'FLATBED', $2)`,
		vehicle, "PIN"+vehicle.String()[:6]); err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO drivers (id, name) VALUES ($1, 'pin driver')`, driver); err != nil {
		t.Fatalf("seed driver: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_tenders WHERE transaction_id = $1`, txnA2)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id = $1`, txnA2)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, txnA2)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_ship_tos WHERE customer_id IN ($1, $2)`, f.custID, f.custB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number LIKE 'PINNEW-%'`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM delivery_routes WHERE vehicle_id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	customerPut := func(row uuid.UUID) string { return f.customerReplace(t, row, nil) }
	escalationPut := func(row uuid.UUID) string {
		cur := f.readJSON(t, "/api/v1/customers/"+row.String())
		raw, _ := json.Marshal(map[string]any{"policy": "require_ack", "threshold_percent": "1.5", "revision": cur["revision"]})
		return string(raw)
	}
	salespersonPut := func(row uuid.UUID) string {
		cur := f.readJSON(t, "/api/v1/customers/"+row.String())
		raw, _ := json.Marshal(map[string]any{"salesperson_id": nil, "revision": cur["revision"]})
		return string(raw)
	}
	regOf := func(row uuid.UUID) string {
		if row == f.txnB {
			return f.regB
		}
		return f.regA
	}
	posTender := fixed(`{"tenders":[{"method":"cash","amount_cents":500}]}`)
	posReturn := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"register_id":%q,"refund_method":"cash","reason":"pin","customer_id":%q,`+
			`"lines":[{"product_id":%q,"quantity":"1","unit_price_ten_thousandths":10000,"restock":false}]}`,
			regOf(row), f.custID.String(), f.posProd.String())
	}
	posSync := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"batch_id":"pin-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
			`"tenders":[{"method":"cash","amount_cents":105}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], regOf(row), uuid.NewString(), uuid.NewString(), f.custID.String(),
			f.posProd.String(), time.Now().UTC().Format(time.RFC3339))
	}
	posOpen := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"register_id":%q,"opening_float_cents":1000}`, regOf(row))
	}
	posStart := func(row uuid.UUID) string {
		return fmt.Sprintf(`{"register_id":%q,"cashier_id":%q}`, regOf(row), uuid.NewString())
	}
	inBranch := func(prefix string) func(uuid.UUID) string {
		return func(branch uuid.UUID) string {
			return fmt.Sprintf(prefix, uuid.NewString()[:8], branch.String())
		}
	}

	// Every by id route with the foreign twin it drives, and the pin's own
	// twin that proves its body valid (the control).
	type leg struct {
		method, path  string
		body          func(row uuid.UUID) string // the body for a row; nil is an empty body
		twin, own     uuid.UUID
		hdr           map[string]string // headers beyond the body (an If-Match revision)
		twin2, own2   uuid.UUID         // the second id of a route that names two
		first         bool              // sent with the first mint (the pos and AR scopes)
		sameAsMissing bool              // the module answers a missing row with one error code whatever the cause (pos), so the wall verdict is the same answer and an unchanged state
		noControl     string            // why no A control can prove the body valid here
		reach         string            // why the leg names no foreign row: it proves the call reaches its module and leaves branch B's rows as they were
		tolerate500   string            // the reason a reach leg may answer 500 for every caller (the module's own wiring is not in this fixture)
		landsOn       string            // the table whose new row must land at the pin's branch
		madeTable     string            // the table of the row a reach leg makes, removed when the test ends
		controlUser   bool              // the control is the admin user (a machine key is refused the route outright)
	}
	// The routes whose user answer for the twin is not a plain 200: a quote
	// with no stored file answers its own 404.
	noRealUserAnswer := map[string]bool{
		"/api/v1/quotes/%s/file":             true,
		"/api/v1/matching/results/%s":        true, // no match results are seeded, both rows answer 404
		"/api/v1/purchase-orders/%s/freight": true, // an empty freight list answers 200 for any order
	}
	legs := []leg{
		{method: "GET", path: "/api/v1/quotes/%s", twin: f.quoteB, own: f.quoteA},
		{method: "PUT", path: "/api/v1/quotes/%s", body: quotePut, twin: f.quoteB, own: f.quoteA},
		{method: "GET", path: "/api/v1/quotes/%s/file", twin: f.quoteB, own: f.quoteA},
		{method: "PUT", path: "/api/v1/quotes/%s/file", body: fixed("pin file"), hdr: ifMatch, twin: f.quoteB, own: f.quoteA},
		{method: "POST", path: "/api/v1/quotes/%s/transitions", body: fixed(`{"to":"sent","revision":1}`), twin: f.quoteB, own: f.quoteA},
		{method: "GET", path: "/api/v1/orders/%s", twin: w.ordB, own: w.ordA},
		{method: "PUT", path: "/api/v1/orders/%s", body: orderPut, twin: w.ordB, own: w.ordA},
		{method: "GET", path: "/api/v1/orders/%s/exposure-gate", twin: w.ordB, own: w.ordA},
		{method: "POST", path: "/api/v1/orders/%s/allocate", body: fixed(`{}`), hdr: ifMatch, twin: w.ordB, own: w.ordA},
		{method: "POST", path: "/api/v1/orders/%s/exposure-override", body: fixed(`{"notes":"a pin override note"}`), twin: w.ordB, own: w.ordA},
		{method: "POST", path: "/api/v1/orders/%s/fulfillments", body: fixed(`{}`), hdr: ifMatch, twin: w.ordB, own: w.ordA,
			noControl: "the seeded order has no lines to fulfil, so the control answers 500 past the body"},
		{method: "POST", path: "/api/v1/orders/%s/transitions", body: fixed(`{"to":"cancelled","reason":"pin","revision":1}`), twin: w.ordB, own: w.ordA},
		{method: "POST", path: "/api/v1/orders/fulfillment-requests/%s/retry", twin: w.delB, own: w.delA,
			noControl: "no fulfilment request row is seeded for the stop"},
		{method: "GET", path: "/api/v1/invoices/%s", twin: w.invB, own: w.invA},
		{method: "GET", path: "/api/v1/invoices/%s/payments", twin: w.invB, own: w.invA},
		{method: "POST", path: "/api/v1/invoices/%s/email", body: fixed(`{"to":"pin@example.com"}`), twin: w.invB, own: w.invA},
		{method: "POST", path: "/api/v1/invoices/%s/transitions", body: fixed(`{"to":"void","reason":"pin","revision":1}`), twin: w.invB, own: w.invA},
		{method: "POST", path: "/api/v1/invoices/%s/write-offs", body: fixed(`{"amount_cents":1,"reason":"pin","revision":1}`), twin: w.invB, own: w.invA},
		{method: "GET", path: "/api/v1/documents/print/invoice/%s", twin: w.invB, own: w.invA},
		{method: "GET", path: "/api/v1/documents/print/pickticket/%s", twin: w.ordB, own: w.ordA},
		{method: "GET", path: "/api/v1/payments/%s", twin: w.payB, own: w.payA},
		{method: "POST", path: "/api/v1/payments/%s/applications", twin: w.payB, own: w.payA,
			body: func(uuid.UUID) string {
				return fmt.Sprintf(`{"revision":1,"applications":[{"invoice_id":%q,"amount_cents":1}]}`, w.invA.String())
			}},
		{method: "POST", path: "/api/v1/payments/%s/refunds", body: fixed(`{"amount_cents":1,"reason":"pin","revision":1}`), twin: w.payB, own: w.payA},
		{method: "POST", path: "/api/v1/payments/%s/transitions", body: fixed(`{"to":"voided","reason":"pin","revision":1}`), twin: w.payB, own: w.payA},
		{method: "POST", path: "/api/v1/ar/applications/%s/reverse", body: fixed(`{"reason":"pin"}`), twin: w.appB, own: w.appA,
			noControl: "the seeded application posted no GL entry, so the reversal fails 500 after the body validated"},
		{method: "GET", path: "/api/v1/credit-memos/%s", twin: w.memoB, own: w.memoA},
		{method: "PUT", path: "/api/v1/credit-memos/%s", body: creditPut, twin: w.memoB, own: w.memoA},
		{method: "POST", path: "/api/v1/credit-memos/%s/applications", twin: w.memoB, own: w.memoA,
			body: func(uuid.UUID) string {
				return fmt.Sprintf(`{"revision":1,"applications":[{"invoice_id":%q,"amount_cents":1}]}`, w.invA.String())
			}},
		{method: "POST", path: "/api/v1/credit-memos/%s/refunds", body: fixed(`{"amount_cents":1,"method":"cash","reason":"pin","revision":1}`), twin: w.memoB, own: w.memoA},
		{method: "POST", path: "/api/v1/credit-memos/%s/transitions", body: fixed(`{"to":"void","reason":"pin","revision":1}`), twin: w.memoB, own: w.memoA},
		{method: "GET", path: "/api/v1/activities/%s", twin: w.actB, own: w.actA},
		{method: "PUT", path: "/api/v1/activities/%s", body: activityPut, twin: w.actB, own: w.actA},
		{method: "DELETE", path: "/api/v1/activities/%s", hdr: ifMatch, twin: w.actB, own: w.actA},
		{method: "GET", path: "/api/v1/contacts/%s", twin: w.conB, own: w.conA},
		{method: "PUT", path: "/api/v1/contacts/%s", body: contactPut, twin: w.conB, own: w.conA},
		{method: "DELETE", path: "/api/v1/contacts/%s", hdr: ifMatch, twin: w.conB, own: w.conA},
		{method: "GET", path: "/api/v1/ship-tos/%s", twin: w.shipB, own: w.shipA},
		{method: "PUT", path: "/api/v1/ship-tos/%s", body: shipToPut, twin: w.shipB, own: w.shipA},
		{method: "GET", path: "/api/v1/drafts/quotes/%s", twin: w.draftQB, own: w.draftQA},
		{method: "PUT", path: "/api/v1/drafts/quotes/%s", body: draftPut(qline), twin: w.draftQB, own: w.draftQA},
		{method: "POST", path: "/api/v1/drafts/quotes/%s/promote", body: fixed(`{"revision":1}`), twin: w.draftQB, own: w.draftQA},
		{method: "POST", path: "/api/v1/drafts/quotes/%s/transitions", body: fixed(`{"to":"discarded","revision":1}`), twin: w.draftQB, own: w.draftQA},
		{method: "GET", path: "/api/v1/drafts/orders/%s", twin: w.draftOB, own: w.draftOA},
		{method: "PUT", path: "/api/v1/drafts/orders/%s", body: draftPut(line), twin: w.draftOB, own: w.draftOA},
		{method: "POST", path: "/api/v1/drafts/orders/%s/promote", body: fixed(`{"revision":1}`), twin: w.draftOB, own: w.draftOA},
		{method: "POST", path: "/api/v1/drafts/orders/%s/transitions", body: fixed(`{"to":"discarded","revision":1}`), twin: w.draftOB, own: w.draftOA},
		{method: "GET", path: "/api/v1/links/quotes/%s", twin: f.quoteB, own: f.quoteA},
		{method: "GET", path: "/api/v1/links/orders/%s", twin: w.ordB, own: w.ordA},
		{method: "GET", path: "/api/v1/links/invoices/%s", twin: w.invB, own: w.invA},
		{method: "GET", path: "/api/v1/links/customers/%s", twin: f.custB, own: f.custID},
		{method: "GET", path: "/api/v1/links/drafts/quotes/%s", twin: w.draftQB, own: w.draftQA},
		{method: "GET", path: "/api/v1/links/drafts/orders/%s", twin: w.draftOB, own: w.draftOA},
		{method: "GET", path: "/api/v1/delivery/deliveries/%s", twin: w.delB, own: w.delA},
		{method: "GET", path: "/api/v1/delivery/deliveries/%s/pod-photos", twin: w.delB, own: w.delA},
		{method: "POST", path: "/api/v1/delivery/deliveries/%s/adjust-qty", twin: w.delB, own: w.delA,
			noControl: "an adjustment needs a delivered order line to adjust; the seeded stop has none, so the control answers its own 400",
			body:      fixed(fmt.Sprintf(`{"adjusted_by":%q,"adjustments":[{"product_id":%q,"original_qty":"1","adjusted_qty":"1","reason_code":"damaged"}]}`, uuid.NewString(), f.posProd.String()))},
		{method: "POST", path: "/api/v1/delivery/deliveries/%s/pod-photo", twin: w.delB, own: w.delA, body: fixed(podPhoto), hdr: map[string]string{"Content-Type": podType}},
		{method: "POST", path: "/api/v1/delivery/deliveries/%s/transitions", body: fixed(`{"to":"out_for_delivery","revision":1}`), twin: w.delB, own: w.delA},
		{method: "GET", path: "/api/v1/delivery/routes/%s", twin: w.routeB, own: w.routeA},
		{method: "GET", path: "/api/v1/delivery/routes/%s/deliveries", twin: w.routeB, own: w.routeA},
		{method: "POST", path: "/api/v1/delivery/routes/%s/optimize", hdr: ifMatch, twin: w.routeB, own: w.routeA},
		{method: "POST", path: "/api/v1/delivery/routes/%s/reorder", hdr: ifMatch, body: reorderBody, twin: w.routeB, own: w.routeA},
		{method: "POST", path: "/api/v1/delivery/routes/%s/transitions", body: fixed(`{"to":"scheduled","revision":1}`), twin: w.routeB, own: w.routeA},
		{method: "GET", path: "/api/v1/matching/results/%s", twin: w.poB, own: w.poA},
		{method: "POST", path: "/api/v1/matching/run/%s", twin: w.poB, own: w.poA,
			noControl: "the matcher's purchase order source is the fixture stub, which answers 500 for the pin's own purchase order"},
		{method: "GET", path: "/api/v1/purchase-orders/%s", twin: w.poB, own: w.poA},
		{method: "GET", path: "/api/v1/purchase-orders/%s/freight", twin: w.poB, own: w.poA},
		{method: "POST", path: "/api/v1/purchase-orders/%s/freight", twin: w.poB, own: w.poA, body: fixed(freightFile), hdr: map[string]string{"Content-Type": freightType},
			noControl: "the upload extracts the invoice with the AI provider, which this harness does not configure, so the control cannot finish"},
		{method: "POST", path: "/api/v1/purchase-orders/%s/freight/%s/apply", twin: w.poB, own: w.poA, twin2: w.freightB, own2: w.freightA,
			noControl: "the apply answers 400 for a freight charge with no extracted lines (the module maps every apply error to 400)"},
		{method: "POST", path: "/api/v1/purchase-orders/%s/receive", twin: w.poB, own: w.poA,
			body: func(row uuid.UUID) string {
				line, yard := w.poLineA, f.yardA
				if row == w.poB {
					line, yard = w.poLineB, f.yardB
				}
				return fmt.Sprintf(`{"lines":[{"line_id":%q,"qty_received":1,"location_id":%q}]}`, line.String(), yard.String())
			}},
		{method: "POST", path: "/api/v1/purchase-orders/%s/submit", twin: w.poB, own: w.poA},
		{method: "POST", path: "/api/v1/quotes/%s/convert", body: fixed(`{}`), hdr: ifMatch, twin: f.quoteB, own: f.quoteA},
		// The customer record, its children and its policy.
		{method: "GET", path: "/api/v1/customers/%s", twin: f.custB, own: f.custID},
		{method: "PUT", path: "/api/v1/customers/%s", body: customerPut, twin: f.custB, own: f.custID},
		{method: "GET", path: "/api/v1/customers/%s/escalation-policy", twin: f.custB, own: f.custID},
		{method: "PUT", path: "/api/v1/customers/%s/escalation-policy", body: escalationPut, twin: f.custB, own: f.custID},
		{method: "PATCH", path: "/api/v1/customers/%s/salesperson", body: salespersonPut, twin: f.custB, own: f.custID},
		{method: "POST", path: "/api/v1/customers/%s/activities", body: fixed(`{"activity_type":"call","description":"pin new"}`), twin: f.custB, own: f.custID},
		{method: "POST", path: "/api/v1/customers/%s/contacts", body: fixed(`{"first_name":"pin","last_name":"new"}`), twin: f.custB, own: f.custID},
		{method: "POST", path: "/api/v1/customers/%s/ship-tos", body: fixed(`{"code":"pinnew","name":"pin new","line1":"1 pin"}`), twin: f.custB, own: f.custID},
		// A completion and a void act on a transaction the module reads under
		// the wall (it answers a missing one 500).
		{method: "POST", path: "/api/v1/pos/transactions/%s/complete", body: posTender, twin: f.txnB, own: f.txnA, first: true, sameAsMissing: true,
			noControl: "the completion deducts stock from a location this fixture does not stock, so the control answers 422 past the body and the transaction read"},
		{method: "POST", path: "/api/v1/pos/transactions/%s/void", body: fixed(`{"reason":"pin"}`), twin: f.txnB, own: txnA2, first: true, sameAsMissing: true},
		// The sync is held by the module itself now: a batch whose register
		// names another branch is refused before any item runs (round 7), so
		// the foreign leg is a held leg and the control on the pin's own
		// register reaches the module.
		{method: "POST", path: "/api/v1/pos/sync", body: posSync, twin: f.txnB, own: f.txnA, first: true},
		// The creates that take the branch they land on from the body.
		{method: "POST", path: "/api/v1/customers", twin: f.branchB, own: f.branchA,
			body: inBranch(`{"account_number":"PINNEW-%s","name":"pin new","primary_branch_id":%q}`)},
		{method: "POST", path: "/api/v1/locations", twin: f.branchB, own: f.branchA,
			body: inBranch(`{"type":"yard","code":"pin-new-%s","name":"pin new yard","parent_id":%q}`)},
		{method: "POST", path: "/api/v1/delivery/deliveries", twin: w.ordB, own: w.ordA,
			body: func(order uuid.UUID) string {
				return fmt.Sprintf(`{"route_id":%q,"order_id":%q}`, w.routeA.String(), order.String())
			}},
		// The creates that take the branch from the caller name no foreign
		// row to refuse: the leg is the call and the unchanged twin state.
		{method: "POST", path: "/api/v1/delivery/routes", twin: uuid.Nil, own: uuid.Nil, madeTable: "delivery_routes",
			body:  fixed(fmt.Sprintf(`{"vehicle_id":%q,"driver_id":%q,"scheduled_date":%q}`, vehicle.String(), driver.String(), time.Now().UTC().Format("2006-01-02"))),
			reach: "a route carries no branch fact until a stop is assigned, so there is no foreign row to name"},
		{method: "POST", path: "/api/v1/purchase-orders", twin: uuid.Nil, own: uuid.Nil, landsOn: "purchase_orders",
			body:  fixed(fmt.Sprintf(`{"vendor_id":%q,"lines":[{"product_id":%q,"description":"pin","quantity":1,"cost":1}]}`, w.vendor.String(), f.posProd.String())),
			reach: "the purchase order takes its branch from the caller; the leg proves it lands at the pin's branch"},
		{method: "POST", path: "/api/v1/purchase-orders/reorder-check", twin: uuid.Nil, own: uuid.Nil,
			reach:       "the reorder check reads the caller's branch stock; the leg proves it leaves branch B's purchase orders as they were",
			tolerate500: "the reorder pass needs the vendor catalogue wiring this fixture does not build, so it answers 500 to every caller"},
		// The cashier routes refuse every machine key; the user control proves
		// the body valid.
		{method: "POST", path: "/api/v1/pos/returns", twin: f.txnB, own: f.txnA, body: posReturn, first: true, controlUser: true},
		{method: "POST", path: "/api/v1/pos/till/open", twin: f.txnB, own: f.txnA, body: posOpen, first: true, controlUser: true},
		{method: "POST", path: "/api/v1/pos/transactions", twin: f.txnB, own: f.txnA, body: posStart, first: true, controlUser: true},
	}
	pathFor := func(l leg, row, row2 uuid.UUID) string {
		if !strings.Contains(l.path, "%s") {
			return l.path
		}
		if strings.Count(l.path, "%s") == 2 {
			return fmt.Sprintf(l.path, row, row2)
		}
		return fmt.Sprintf(l.path, row)
	}
	bodyFor := func(l leg, row uuid.UUID) string {
		if l.body == nil {
			return ""
		}
		return l.body(row)
	}
	raw := func(l leg) string {
		if l.first {
			return firstBound
		}
		return f.bound
	}
	// Control statuses that prove a body valid and a row reached: anything
	// the module answers past its validation and its wall.
	controlOK := func(status int) bool {
		switch status {
		case 200, 201, 202, 204, 409, 402, 503:
			return true
		}
		return false
	}

	before := f.twinState(t, w)
	for _, l := range legs {
		if l.reach != "" {
			// There is no foreign row to name: the call is made once, must
			// reach its module past the key's scopes, and (for a create that
			// takes its branch from the caller) must land at the pin's branch.
			status, buf := f.key(t, raw(l), l.method, pathFor(l, l.twin, l.twin2), bodyFor(l, l.twin), l.hdr)
			f.twinLeg(l.method, pathFor(l, l.twin, l.twin2))
			if !controlOK(status) && !(l.tolerate500 != "" && status == http.StatusInternalServerError) {
				t.Errorf("bound key %s %s: %d %s, want the call to reach its module", l.method, l.path, status, buf)
			}
			var made struct {
				ID uuid.UUID `json:"id"`
			}
			if status == http.StatusCreated && json.Unmarshal(buf, &made) == nil && made.ID != uuid.Nil && l.madeTable != "" {
				f.madeRow(t, l.madeTable, made.ID)
			}
			if l.landsOn != "" && status == http.StatusCreated {
				doc := made
				var branch uuid.UUID
				if err := json.Unmarshal(buf, &doc); err != nil {
					t.Errorf("bound key %s %s: the answer holds no id: %s", l.method, l.path, buf)
				} else if err := f.db.Pool.QueryRow(context.Background(),
					`SELECT branch_id FROM `+l.landsOn+` WHERE id = $1`, doc.ID).Scan(&branch); err != nil || branch != f.branchA {
					t.Errorf("bound key %s %s landed on branch %v (%v), want the pin's %v", l.method, l.path, branch, err, f.branchA)
				}
			}
			continue
		}
		bPath := pathFor(l, l.twin, l.twin2)
		mPath := pathFor(l, uuid.MustParse(missing), uuid.MustParse(missing))
		sb, bb := f.key(t, raw(l), l.method, bPath, bodyFor(l, l.twin), l.hdr)
		f.twinLeg(l.method, bPath)
		sm, _ := f.key(t, raw(l), l.method, mPath, bodyFor(l, l.twin), l.hdr)
		scopeRefusal := sb == http.StatusForbidden && strings.Contains(string(bb), "lacks required scope")
		held := !scopeRefusal && (sb == http.StatusForbidden || sb == http.StatusNotFound ||
			(sb == sm && (l.method == "GET" || (l.sameAsMissing && sm >= 400))))
		if !held {
			t.Errorf("bound key %s %s: %d %s for the foreign row, want a 403 or a 404 (the missing row answers %d)", l.method, bPath, sb, bb, sm)
		}
		if strings.Contains(string(bb), l.twin.String()) {
			t.Errorf("bound key %s %s: the answer names the foreign row: %s", l.method, bPath, bb)
		}
		// The twin is real: a user reads it where a missing row is a 404.
		if l.method == "GET" && !noRealUserAnswer[l.path] {
			ub, ubuf := f.user(t, l.method, bPath, "", nil)
			um, _ := f.user(t, l.method, mPath, "", nil)
			if ub != http.StatusOK || um != http.StatusNotFound {
				t.Errorf("user %s %s: %d for the twin and %d for a missing row, want 200 and 404 (%s)", l.method, bPath, ub, um, ubuf)
			}
		}
	}
	if changed := changedTables(before, f.twinState(t, w)); len(changed) > 0 {
		t.Errorf("the foreign legs changed rows that name branch B's twins: %v", changed)
	}
	// The controls: the same body against the pin's own twin must pass the
	// module's validation and reach the row, or the foreign leg above never
	// reached the wall it claims to test. Writes only; a read has no body.
	for _, l := range legs {
		if l.method == "GET" || l.reach != "" {
			continue
		}
		var sa int
		var ba []byte
		if l.controlUser {
			sa, ba = f.user(t, l.method, pathFor(l, l.own, l.own2), bodyFor(l, l.own), l.hdr)
		} else {
			sa, ba = f.key(t, raw(l), l.method, pathFor(l, l.own, l.own2), bodyFor(l, l.own), l.hdr)
		}
		if l.noControl == "" && !controlOK(sa) {
			t.Errorf("control: bound key %s %s on the pin's own twin: %d %s, want the body valid and the row reached", l.method, pathFor(l, l.own, l.own2), sa, ba)
		}
	}

	// The lists carry the pin's branch only: the foreign twin ids never
	// appear, the pin's own do, and the unbound key sees the list too.
	lists := []struct {
		path            string
		absent, present []string
		first           bool // sent with the first mint (the pos scopes)
	}{
		{path: "/api/v1/customers", absent: []string{f.custB.String()}, present: []string{f.custID.String()}},
		{path: "/api/v1/credit-memos", absent: []string{w.memoB.String()}, present: []string{w.memoA.String()}},
		{path: "/api/v1/invoices", absent: []string{w.invB.String()}, present: []string{w.invA.String()}},
		{path: "/api/v1/payments", absent: []string{w.payB.String()}, present: []string{w.payA.String()}},
		{path: "/api/v1/quotes", absent: []string{f.quoteB.String()}, present: []string{f.quoteA.String()}},
		{path: "/api/v1/orders", absent: []string{w.ordB.String()}, present: []string{w.ordA.String()}},
		{path: "/api/v1/orders/fulfillment-requests", absent: []string{w.ordB.String()}, present: nil},
		{path: "/api/v1/drafts/quotes", absent: []string{w.draftQB.String()}, present: []string{w.draftQA.String()}},
		{path: "/api/v1/drafts/orders", absent: []string{w.draftOB.String()}, present: []string{w.draftOA.String()}},
		{path: "/api/v1/purchase-orders", absent: []string{w.poB.String()}, present: []string{w.poA.String()}},
		{path: "/api/v1/purchase-orders/recommendations", absent: []string{}, present: nil},
		{path: "/api/v1/purchase-orders/reorder-runs", absent: []string{}, present: nil},
		{path: "/api/v1/purchase-orders/source-summary", absent: []string{}, present: nil},
		{path: "/api/v1/delivery/routes", absent: []string{w.routeB.String()}, present: []string{w.routeA.String()}},
		{path: "/api/v1/matching/exceptions", absent: []string{w.poB.String()}, present: nil},
		{path: "/api/v1/customers/" + f.custID.String() + "/activities", absent: []string{w.actB.String()}, present: []string{w.actA.String()}},
		{path: "/api/v1/customers/" + f.custID.String() + "/contacts", absent: []string{w.conB.String()}, present: []string{w.conA.String()}},
		{path: "/api/v1/customers/" + f.custID.String() + "/ship-tos", absent: []string{w.shipB.String()}, present: []string{w.shipA.String()}},
		{path: "/api/v1/dashboard/summary", absent: []string{f.custB.String()}, present: nil},
		{path: "/api/v1/dashboard/top-customers", absent: []string{f.custB.String()}, present: nil},
		{path: "/api/v1/dashboard/order-activity", absent: []string{w.ordB.String()}, present: nil},
		{path: "/api/v1/dashboard/revenue-trend", absent: []string{w.invB.String()}, present: nil},
		{path: "/api/v1/dashboard/inventory-alerts", absent: []string{f.yardB.String()}, present: nil},
		{path: "/api/v1/inventory", absent: []string{f.yardB.String()}, present: nil},
		{path: "/api/v1/locations", absent: []string{f.yardB.String()}, present: nil},
		{path: "/api/v1/products/reorder-alerts", absent: []string{f.yardB.String()}, present: nil},
		{path: "/api/v1/quotes/analytics", absent: []string{f.quoteB.String()}, present: nil},
		{path: "/api/v1/ar/aging", absent: []string{f.custB.String(), w.invB.String()}, present: nil},
		{path: "/api/v1/ar/aging/summary", absent: []string{f.custB.String(), w.invB.String()}, present: nil},
		{path: "/api/v1/pos/transactions?register_id=" + f.regB, absent: []string{f.txnB.String()}, present: nil, first: true},
		{path: "/api/v1/pos/transactions?register_id=" + f.regA, absent: []string{f.txnB.String()}, present: []string{f.txnA.String()}, first: true},
	}
	for _, l := range lists {
		key := f.bound
		if l.first {
			key = firstBound
		}
		sb, bb := f.key(t, key, "GET", l.path, "", nil)
		f.twinLeg("GET", l.path)
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
		if !l.first {
			if sk, _ := f.key(t, f.unbound, "GET", l.path, "", nil); sk != http.StatusOK {
				t.Errorf("unbound key GET %s: %d, want 200", l.path, sk)
			}
		}
	}

	// The draft change feeds are streams: from the first position, a key
	// pinned to A sees the draft it made at A and never the one a user made at
	// B (the feed applies the branch wall to each row).
	cursor0, err := httpx.MintCursor(drafts.FeedCursorScope, "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []struct{ module, payload string }{
		{"quotes", fmt.Sprintf(`{"payload":{"customer_id":%%q,"lines":[%s]}}`, qline)},
		{"orders", fmt.Sprintf(`{"payload":{"customer_id":%%q,"delivery_type":"pickup","lines":[%s]}}`, line)},
	} {
		foreign := f.madeWith(t, "user", "drafts", "POST", "/api/v1/drafts/"+kind.module, fmt.Sprintf(kind.payload, f.custB.String()),
			map[string]string{"X-Branch-Id": B}, http.StatusCreated)
		own := f.madeWith(t, "bound", "drafts", "POST", "/api/v1/drafts/"+kind.module, fmt.Sprintf(kind.payload, f.custID.String()), nil, http.StatusCreated)
		status, body := f.stream(t, f.bound, "/api/v1/drafts/"+kind.module+"/feed?cursor="+cursor0, 1500*time.Millisecond)
		f.twinLeg("GET", "/api/v1/drafts/"+kind.module+"/feed")
		if status != http.StatusOK {
			t.Errorf("bound key GET /drafts/%s/feed: %d %s, want the stream open", kind.module, status, body)
			continue
		}
		if foreign != uuid.Nil && strings.Contains(body, foreign.String()) {
			t.Errorf("bound key GET /drafts/%s/feed carries the draft made at branch B", kind.module)
		}
		if own != uuid.Nil && !strings.Contains(body, own.String()) {
			t.Errorf("bound key GET /drafts/%s/feed does not carry the draft made at the pin's branch: %.300s", kind.module, body)
		}
	}

	// The branch tree and the location row read hold the same way: the
	// foreign branch's tree and yard are refused or missing, the pin's own
	// answer. The tree's refusal is the branch middleware's own (the wall is
	// not what refuses a path branch on these mounts).
	if sb, _ := f.key(t, f.bound, "GET", "/api/v1/branches/"+B+"/tree", "", nil); sb != http.StatusForbidden {
		t.Errorf("bound key foreign branch tree: %d, want 403", sb)
	}
	f.twinLeg("GET", "/api/v1/branches/"+B+"/tree")
	f.allow(t, "bound", "GET", "/api/v1/branches/"+A+"/tree", "", nil, http.StatusOK)
	if sb, bb := f.key(t, f.bound, "GET", "/api/v1/locations/"+f.yardB.String(), "", nil); sb != http.StatusForbidden && sb != http.StatusNotFound {
		t.Errorf("bound key foreign yard read: %d %s, want 403 or 404", sb, bb)
	}
	f.twinLeg("GET", "/api/v1/locations/"+f.yardB.String())
	f.allow(t, "bound", "GET", "/api/v1/locations/"+f.yardA.String(), "", nil, http.StatusOK)

	// The inventory adjust and transfer bodies name locations: the foreign
	// yard is refused (the branch guard's own refusal) with nothing written.
	beforeInv := f.twinState(t, w)
	if sb, bb := f.key(t, f.bound, "POST", "/api/v1/inventory/adjust",
		fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":5,"reason":"pin"}`, f.posProd.String(), f.yardB.String()), nil); sb != http.StatusForbidden {
		t.Errorf("bound key inventory adjust naming the foreign yard: %d %s, want 403", sb, bb)
	}
	f.twinLeg("POST", "/api/v1/inventory/adjust")
	if sb, bb := f.key(t, f.bound, "POST", "/api/v1/inventory/transfer",
		fmt.Sprintf(`{"product_id":%q,"from_location_id":%q,"to_location_id":%q,"quantity":1,"reason":"pin"}`,
			f.posProd.String(), f.yardA.String(), f.yardB.String()), nil); sb != http.StatusForbidden {
		t.Errorf("bound key inventory transfer into the foreign yard: %d %s, want 403", sb, bb)
	}
	f.twinLeg("POST", "/api/v1/inventory/transfer")
	if changed := changedTables(beforeInv, f.twinState(t, w)); len(changed) > 0 {
		t.Errorf("refused inventory writes changed rows that name branch B's twins: %v", changed)
	}
}
