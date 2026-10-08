// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Fulfilment, the money moment (ADR 0005 5.6) on the wire against a real
// database: the cycle 2 exit line (a will-call order posts COGS to the GL), the
// pickup refusal, partial fulfilments that sum to the order, kits, non stock
// lines, the credit re-check, and the rollback of the whole act.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// withMoney wires the fulfilment's collaborators: the real invoice service over
// the real GL and account ledger.
func (f *fixture) withMoney() func(*order.Service) *order.Service {
	return func(s *order.Service) *order.Service {
		glSvc := gl.NewService(gl.NewRepository(f.db), nil, slog.Default())
		acct := account.NewService(account.NewRepository(f.db), f.db, slog.Default())
		inv := invoice.NewService(invoice.NewRepository(f.db), glSvc, acct, f.db).WithAuditLog(audit.NewLogger(f.db))
		return s.WithInvoices(inv)
	}
}

// failOn is an event recorder that fails for one event type.
type failOn struct {
	inner order.EventRecorder
	typ   string
	// onFail runs inside the failing act's transaction, just before the error:
	// the test reads there what the act had written so far.
	onFail func(ctx context.Context)
}

func (f failOn) Write(ctx context.Context, ev outbox.Event) error {
	if ev.Type == f.typ {
		if f.onFail != nil {
			f.onFail(ctx)
		}
		return errors.New("outbox insert failed")
	}
	return f.inner.Write(ctx, ev)
}

// setCost sets the product's average unit cost (scale 4 dollars).
func (f *fixture) setCost(productID uuid.UUID, cost string) {
	f.t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE products SET average_unit_cost = $2::numeric WHERE id = $1`, productID, cost); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) cleanMoney() {
	ctx := context.Background()
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e JOIN invoices i ON e.source_ref_id = i.id WHERE i.customer_id = $1)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'invoice' AND entity_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_transactions WHERE customer_id = $1`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'invoice' AND entity_id NOT IN (SELECT id FROM invoices)`)
	})
}

func (f *fixture) fulfil(id string, rev int64, extra map[string]any) resp {
	f.t.Helper()
	body := map[string]any{"revision": rev}
	for k, v := range extra {
		body[k] = v
	}
	return f.do("POST", "/api/v1/orders/"+id+"/fulfillments", body)
}

type legRow struct {
	code          string
	debit, credit int64
}

// entryLegs reads the legs of the invoice's journal entries (cents).
func (f *fixture) entryLegs(invoiceID string) (entries int, legs map[string]legRow) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, invoiceID).Scan(&entries); err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx, `
		SELECT a.code, ROUND(l.debit * 100)::bigint, ROUND(l.credit * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id = $1`, invoiceID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	legs = map[string]legRow{}
	for rows.Next() {
		var r legRow
		if err := rows.Scan(&r.code, &r.debit, &r.credit); err != nil {
			f.t.Fatal(err)
		}
		legs[r.code] = r
	}
	return entries, legs
}

func invoiceIDOf(t *testing.T, r resp) string {
	t.Helper()
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, "/api/v1/invoices/") {
		t.Fatalf("Location = %q, want the invoice's path", loc)
	}
	return strings.TrimPrefix(loc, "/api/v1/invoices/")
}

func (f *fixture) pickupBody(lines ...map[string]any) map[string]any {
	return f.createBody(lines...)
}

// RULE (the cycle 2 exit line, ADR 0005 14.2): a pickup order fulfilled with
// picked_up_by writes ONE entry with 1020 debit = the total, 4010 and 2020
// credits, 5010 debit = quantity x average cost and 1030 credit, in the
// fulfilment's transaction.
func TestWillCallFulfilmentPostsCOGSToTheGL(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // 10 PCS pickup at 5.50 = 5500, tax 8.875% = 488
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm = %q: %s", r.body["status"], r.raw)
	}
	// A pickup names who collected.
	if bad := f.fulfil(id, revision(t, r), nil); bad.status != 400 {
		t.Fatalf("a pickup fulfilment without picked_up_by = %d, want 400: %s", bad.status, bad.raw)
	} else if _, _, d := errorOf(t, bad); len(d) == 0 || d[0]["field"] != "picked_up_by" {
		t.Errorf("details = %v, want picked_up_by", d)
	}

	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != 201 {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)
	if str(t, r.body, "status") != "fulfilled" {
		t.Errorf("order status = %q, want fulfilled", r.body["status"])
	}
	if r.header.Get("ETag") == "" || revision(t, r) < 3 {
		t.Errorf("ETag %q revision %d, want the new revision", r.header.Get("ETag"), revision(t, r))
	}
	if ids := r.body["invoice_ids"].([]any); len(ids) != 1 || ids[0] != inv {
		t.Errorf("invoice_ids = %v, want [%s]", ids, inv)
	}

	entries, legs := f.entryLegs(inv)
	if entries != 1 {
		t.Fatalf("%d journal entries for the invoice, want exactly one", entries)
	}
	want := map[string]legRow{
		"1020": {"1020", 5988, 0}, "4010": {"4010", 0, 5500}, "2020": {"2020", 0, 488},
		"5010": {"5010", 3250, 0}, "1030": {"1030", 0, 3250},
	}
	if len(legs) != len(want) {
		t.Errorf("legs = %v, want exactly the five legs", legs)
	}
	for code, w := range want {
		if legs[code] != w {
			t.Errorf("leg %s = %+v, want %+v", code, legs[code], w)
		}
	}
	// The invoice, its lines and the stock.
	var total, cost, lines string
	if err := db.Pool.QueryRow(context.Background(), `
		SELECT (ROUND(total_amount * 100))::text, (SELECT sum(cost * 100)::bigint::text FROM invoice_lines WHERE invoice_id = i.id),
		       delivery_type || '/' || COALESCE(picked_up_by, '') || '/' || tax_source || '/' || currency
		FROM invoices i WHERE id = $1`, inv).Scan(&total, &cost, &lines); err != nil {
		t.Fatal(err)
	}
	if total != "5988" || cost != "3250" || lines != "PICKUP/Counter customer/BRANCH_RATE/USD" {
		t.Errorf("invoice = total %s cost %s %s, want 5988, 3250, PICKUP/Counter customer/BRANCH_RATE/USD", total, cost, lines)
	}
	if got := f.inventoryOf(f.productID); got != "0.0000/0.0000" {
		t.Errorf("inventory on hand/allocated = %s, want 0/0 (shipped)", got)
	}
	// The AR subledger moved by the total.
	var bal string
	if err := db.Pool.QueryRow(context.Background(), `SELECT ROUND(balance_due * 100)::bigint::text FROM customers WHERE id = $1`, f.customerID).Scan(&bal); err != nil || bal != "5988" {
		t.Errorf("balance_due cents = %s (%v), want 5988", bal, err)
	}
	if ev := eventsFor(t, db, id); fmt.Sprint(ev) != "[order.created order.confirmed order.fulfilled]" {
		t.Errorf("order events = %v", ev)
	}
	if ev := eventsForEntity(t, db, "invoice", inv); fmt.Sprint(ev) != "[invoice.created]" {
		t.Errorf("invoice events = %v", ev)
	}
	// Nothing left to fulfil, and a fulfilled order refuses a second go.
	if again := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "X"}); again.status != 409 {
		t.Errorf("a second fulfilment of a fulfilled order = %d, want 409", again.status)
	}
}

// eventsForEntity reads the events of any entity in order.
func eventsForEntity(t *testing.T, db *database.DB, entityType, entityID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = $1 AND entity_id = $2 ORDER BY position`, entityType, entityID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		out = append(out, ty)
	}
	return out
}

// RULE (ADR 0003 section 3, ADR 0005 14.2): a failing event write rolls the
// entry back with the rest of the act: no invoice, no entry, no stock moved,
// the order unchanged.
func TestFailingEventWriteRollsTheFulfilmentBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	var writtenInvoice, probeOrder string
	var entriesInside int
	f.serveWith(f.withStock(), f.withMoney(), func(s *order.Service) *order.Service {
		return s.WithOutbox(failOn{inner: outbox.NewWriter(db, ""), typ: "order.fulfilled", onFail: func(ctx context.Context) {
			ex := db.GetExecutor(ctx)
			// this order's invoice, not the newest in a shared database (another
			// test can leave a future dated one behind)
			_ = ex.QueryRow(ctx, `SELECT id::text FROM invoices WHERE order_id = $1::uuid`, probeOrder).Scan(&writtenInvoice)
			_ = ex.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1::uuid`, writtenInvoice).Scan(&entriesInside)
		}})
	})
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create()
	id := str(t, r.body, "id")
	probeOrder = id
	r = f.transition(id, 1, "confirmed")
	rev := revision(t, r)
	bad := f.fulfil(id, rev, map[string]any{"picked_up_by": "Counter customer"})
	if bad.status < 500 {
		t.Fatalf("fulfil with a failing event write = %d, want a 5xx: %s", bad.status, bad.raw)
	}
	ctx := context.Background()
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE order_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d invoices survived the rollback (%v)", n, err)
	}
	// Inside the act the entry existed; after the rollback it is gone with the invoice.
	if entriesInside != 1 {
		t.Errorf("%d entries inside the act before the failure, want 1 (the test would prove nothing)", entriesInside)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1::uuid`, writtenInvoice).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d journal entries survived the rollback (%v)", n, err)
	}
	if got := f.inventoryOf(f.productID); got != "10.0000/10.0000" {
		t.Errorf("inventory = %s, want 10/10 (allocated, not shipped)", got)
	}
	if got := f.lineQuantities(id); fmt.Sprint(got) != "[PRODUCT:10.0000/10.0000/0.0000/0.0000]" {
		t.Errorf("line quantities = %v, want unchanged", got)
	}
	var bal string
	if err := db.Pool.QueryRow(ctx, `SELECT ROUND(balance_due * 100)::bigint::text FROM customers WHERE id = $1`, f.customerID).Scan(&bal); err != nil || bal != "0" {
		t.Errorf("balance_due cents = %s (%v), want 0: the subledger debit rolled back", bal, err)
	}
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "confirmed" || revision(t, g) != rev {
		t.Errorf("order after the rollback = %q rev %d, want confirmed rev %d", g.body["status"], revision(t, g), rev)
	}
}

// RULE (ADR 0005 14.2): an amount discount prorated across two partial
// invoices sums exactly, and partial fulfilments derive the status. 3 x 33.33
// with a 0.50 discount ships 1, then 2: the invoices total 9949 and the order is
// partially_fulfilled, then fulfilled.
func TestPartialFulfilmentsSumToTheOrder(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "3")
	f.setCost(f.productID, "10")
	if _, err := db.Pool.Exec(context.Background(), `UPDATE locations SET default_tax_rate = 0 WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE locations SET default_tax_rate = $1 WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`, f.branchRate)
	})
	body := f.createBody(map[string]any{
		"product_id": f.productID.String(), "quantity": "3", "unit_price_ten_thousandths": 333300, "override_reason": "contract price",
		"discount_cents": 50, "discount_reason": "volume",
	})
	body["delivery_type"] = "delivery"
	r := f.do("POST", "/api/v1/orders", body)
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	if num(t, r.body, "total_cents") != 9949 {
		t.Fatalf("order total = %d, want 9949", num(t, r.body, "total_cents"))
	}
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
	r = f.transition(id, 1, "confirmed")

	r1 := f.fulfil(id, revision(t, r), map[string]any{"lines": []map[string]any{{"order_line_id": lineID, "quantity": "1"}}})
	if r1.status != 201 || str(t, r1.body, "status") != "confirmed" {
		t.Fatalf("first fulfilment = %d %q: %s", r1.status, r1.body["status"], r1.raw)
	}
	r2 := f.fulfil(id, revision(t, r1), nil) // the rest: 2 allocated (a delivery order)
	if r2.status != 201 || str(t, r2.body, "status") != "fulfilled" {
		t.Fatalf("second fulfilment = %d %q: %s", r2.status, r2.body["status"], r2.raw)
	}
	var sum, n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(ROUND(subtotal * 100)), 0)::int, count(*) FROM invoices WHERE order_id = $1`, id).Scan(&sum, &n); err != nil {
		t.Fatal(err)
	}
	if n != 2 || sum != 9949 {
		t.Errorf("%d invoices summing %d cents, want 2 summing the order's 9949", n, sum)
	}
	if ev := eventsFor(t, db, id); fmt.Sprint(ev) != "[order.created order.confirmed order.partially_fulfilled order.fulfilled]" {
		t.Errorf("order events = %v", ev)
	}
	// Each invoice's COGS is its own quantity x cost: 10.00 then 20.00.
	rows, err := db.Pool.Query(context.Background(), `SELECT ROUND(SUM(l.cost) * 100)::bigint FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id WHERE i.order_id = $1 GROUP BY i.id ORDER BY i.created_at, i.id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var costs []int64
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		costs = append(costs, c)
	}
	if fmt.Sprint(costs) != "[1000 2000]" {
		t.Errorf("per invoice cost = %v, want [1000 2000]", costs)
	}
}

// RULE (ADR 0005 14.2): a kit explodes, allocates in whole kits, bills whole
// kits, and posts COGS from its components only. 2 kits at 100.00 of 4 posts
// each at cost 5.00: the invoice is 20000 revenue, 40 x 5.00 = 200.00 -> 4 x 2
// posts = 8 posts = 4000 cost, none on the kit line.
func TestKitBillsWholeKitsAndPostsCOGSFromComponents(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	kit, post := uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable) VALUES ($1, $2, 'A fence kit', 'EA', 100.00, TRUE, TRUE)`, kit, "KB-KIT-"+uuid.NewString()[:6])
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'A fence post', 'EA', 12.50, 5.00)`, post, "KB-POST-"+uuid.NewString()[:6])
	must(`INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position) VALUES ($1, $2, 4, 0)`, kit, post)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE product_id IN ($1, $2)`, kit, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kit)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE product_id IN ($1, $2)`, kit, post)
	})
	f.stock(post, "9") // 2 whole kits need 8

	r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{"product_id": kit.String(), "quantity": "2"}))
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm = %q: %s", r.body["status"], r.raw)
	}
	lines := r.body["lines"].([]any)
	compID := str(t, lines[1].(map[string]any), "id")
	// A component line cannot be named.
	bad := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": compID, "quantity": "4"}}})
	if bad.status != 400 {
		t.Fatalf("naming a component = %d, want 400: %s", bad.status, bad.raw)
	} else if _, _, d := errorOf(t, bad); len(d) == 0 || d[0]["field"] != "lines[0].order_line_id" {
		t.Errorf("details = %v, want lines[0].order_line_id", d)
	}
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter"})
	if r.status != 201 || str(t, r.body, "status") != "fulfilled" {
		t.Fatalf("fulfil = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	inv := invoiceIDOf(t, r)
	_, legs := f.entryLegs(inv)
	if legs["4010"].credit != 20000 || legs["5010"].debit != 4000 || legs["1030"].credit != 4000 {
		t.Errorf("legs = %+v, want 4010 cr 20000, 5010 dr 4000, 1030 cr 4000", legs)
	}
	var kitCost, compCost, compTotal string
	if err := db.Pool.QueryRow(ctx, `SELECT
		(SELECT ROUND(cost * 100)::text FROM invoice_lines WHERE invoice_id = $1 AND line_type = 'KIT'),
		(SELECT ROUND(cost * 100)::text FROM invoice_lines WHERE invoice_id = $1 AND line_type = 'COMPONENT'),
		(SELECT ROUND(line_total * 100)::text FROM invoice_lines WHERE invoice_id = $1 AND line_type = 'COMPONENT')`, inv).Scan(&kitCost, &compCost, &compTotal); err != nil {
		t.Fatal(err)
	}
	if kitCost != "0" || compCost != "4000" || compTotal != "0" {
		t.Errorf("kit cost %s, component cost %s, component total %s, want 0, 4000, 0", kitCost, compCost, compTotal)
	}
	if got := f.inventoryOf(post); got != "1.0000/0.0000" {
		t.Errorf("post inventory = %s, want 1 left", got)
	}
}

// RULE (ADR 0005 14.2): a non stock line bills on a fulfilment with no lines,
// and when named it is checked against its unbilled quantity, not an
// allocation; it carries no cost and posts to 4010.
func TestNonStockLineBillsAndIsCheckedAgainstUnbilled(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{
		"description": "Custom cut fee", "quantity": "4", "uom": "EA", "unit_price_ten_thousandths": 250000,
	}))
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
	r = f.transition(id, 1, "confirmed")
	over := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": lineID, "quantity": "5"}}})
	if over.status != 409 {
		t.Fatalf("billing more than ordered = %d, want 409: %s", over.status, over.raw)
	} else if _, _, d := errorOf(t, over); len(d) == 0 || d[0]["code"] != "exceeds_unbilled" {
		t.Errorf("details = %v, want exceeds_unbilled", d)
	}
	part := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": lineID, "quantity": "1"}}})
	if part.status != 201 || str(t, part.body, "status") != "confirmed" {
		t.Fatalf("partial = %d %q: %s", part.status, part.body["status"], part.raw)
	}
	rest := f.fulfil(id, revision(t, part), map[string]any{"picked_up_by": "Counter"})
	if rest.status != 201 || str(t, rest.body, "status") != "fulfilled" {
		t.Fatalf("rest = %d %q: %s", rest.status, rest.body["status"], rest.raw)
	}
	_, legs := f.entryLegs(invoiceIDOf(t, rest))
	if legs["4010"].credit != 7500 {
		t.Errorf("rest posts 4010 cr %d, want 7500 (3 x 25.00)", legs["4010"].credit)
	}
	if _, has := legs["5010"]; has {
		t.Errorf("a non stock line posted COGS: %+v", legs["5010"])
	}
}

// RULE (ADR 0005 5.6 step 1, 5.3): the fulfilment re-checks the credit with
// this order counted by its unbilled remainder: an over limit customer is 409
// credit_limit and nothing moves; the second fulfilment of a partly billed
// order is not refused for the first invoice's amount.
func TestFulfilmentCreditRecheck(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "20")
	ctx := context.Background()

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	// The customer's limit falls below the order after the confirm.
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	bad := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter"})
	if bad.status != 409 {
		t.Fatalf("fulfil over the limit = %d, want 409: %s", bad.status, bad.raw)
	} else if _, _, d := errorOf(t, bad); len(d) == 0 || d[0]["code"] != "credit_limit" {
		t.Errorf("details = %v, want credit_limit", d)
	}
	if got := f.inventoryOf(f.productID); got != "20.0000/10.0000" {
		t.Errorf("inventory = %s, want untouched", got)
	}
	// A limit that fits the order once: the first partial invoice puts 3 of 10
	// in AR, and the second is not refused for it (the remainder shrinks as the
	// open receivable grows: the order is never counted twice).
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 60.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	lineID := f.lineID(id)
	one := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": lineID, "quantity": "3"}}})
	if one.status != 201 {
		t.Fatalf("first partial = %d: %s", one.status, one.raw)
	}
	two := f.fulfil(id, revision(t, one), map[string]any{"picked_up_by": "Counter"})
	if two.status != 201 {
		t.Fatalf("second fulfilment of a partly billed order = %d, want 201 (not refused for the first invoice): %s", two.status, two.raw)
	}
}

func (f *fixture) lineID(orderID string) string {
	f.t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT id::text FROM order_lines WHERE order_id = $1 ORDER BY position LIMIT 1`, orderID).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// RULE (ADR 0005 2.1, 8.2, 8.3, 14.2): each line type's treatment on a
// fulfilment. A product line posts 4010 and its cost; a charge line posts to
// its code's account (FREIGHT: 4020), untaxed per the code, in full on the first
// invoice that bills it and never again, with no cost; a text line is copied to
// the invoice with no amount. Revenue posts net of the line discount.
func TestEachLineTypeOnAFulfilment(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "2.00")

	body := f.createBody(
		map[string]any{"product_id": f.productID.String(), "quantity": "4", "discount_percent": "10", "discount_reason": "volume"},
		map[string]any{"line_type": "charge", "charge_code": "FREIGHT", "description": "Freight", "quantity": "1", "unit_price_ten_thousandths": 250000},
		map[string]any{"line_type": "text", "description": "Leave at the gate"},
	)
	r := f.do("POST", "/api/v1/orders", body)
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	lines := r.body["lines"].([]any)
	prodID := str(t, lines[0].(map[string]any), "id")
	// 4 x 5.50 less 10% = 1980; freight 25.00 untaxed; tax 8.875% of 1980 = 176
	if num(t, r.body, "subtotal_cents") != 4480 {
		t.Fatalf("subtotal = %d, want 4480", num(t, r.body, "subtotal_cents"))
	}
	r = f.transition(id, 1, "confirmed")
	// Fulfil 1 of the 4 first: the charge bills in full with it.
	one := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": prodID, "quantity": "1"}}})
	if one.status != 201 {
		t.Fatalf("first fulfilment = %d: %s", one.status, one.raw)
	}
	inv1 := invoiceIDOf(t, one)
	_, legs := f.entryLegs(inv1)
	// 1 of 4 at 10 percent off: round(550 x 0.9) = 495 on the product (cumulative),
	// plus 2500 freight in 4020 only when the charge was named - it was not named,
	// so it did NOT bill on this invoice: naming lines bills only what is named.
	if legs["4010"].credit != 495 || legs["5010"].debit != 200 || legs["1030"].credit != 200 {
		t.Errorf("legs of the first invoice = %+v, want 4010 cr 495, 5010 dr 200, 1030 cr 200", legs)
	}
	if _, has := legs["4020"]; has {
		t.Errorf("an unnamed charge billed on a partial fulfilment: %+v", legs["4020"])
	}

	// The rest with no lines: 3 more units (cumulative 1980 - 495 = 1485), the
	// freight in full, the note copied.
	rest := f.fulfil(id, revision(t, one), map[string]any{"picked_up_by": "Counter"})
	if rest.status != 201 || str(t, rest.body, "status") != "fulfilled" {
		t.Fatalf("second fulfilment = %d %q: %s", rest.status, rest.body["status"], rest.raw)
	}
	inv2 := invoiceIDOf(t, rest)
	_, legs = f.entryLegs(inv2)
	if legs["4010"].credit != 1485 || legs["4020"].credit != 2500 || legs["5010"].debit != 600 {
		t.Errorf("legs of the second invoice = %+v, want 4010 cr 1485, 4020 cr 2500, 5010 dr 600", legs)
	}
	taxable := int64(1485)
	wantTax := (taxable*88750 + 500000) / 1000000
	if legs["2020"].credit != wantTax {
		t.Errorf("2020 cr %d, want %d: the freight is untaxed, the discounted goods taxed once per document", legs["2020"].credit, wantTax)
	}
	rows, err := db.Pool.Query(context.Background(), `SELECT line_type, COALESCE(ROUND(line_total * 100)::bigint, -1), ROUND(cost * 100)::bigint FROM invoice_lines WHERE invoice_id = $1 ORDER BY position`, inv2)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var typ string
		var total, cost int64
		if err := rows.Scan(&typ, &total, &cost); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%d:%d", typ, total, cost))
	}
	if fmt.Sprint(got) != "[PRODUCT:1485:600 CHARGE:2500:0 TEXT:-1:0]" {
		t.Errorf("second invoice lines = %v, want the product (cost 6.00), the freight (no cost) and the note", got)
	}
	// The freight billed once: the first invoice had no charge line.
	var chargeLines int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id WHERE i.order_id = $1 AND l.line_type = 'CHARGE'`, id).Scan(&chargeLines); err != nil || chargeLines != 1 {
		t.Errorf("%d charge lines billed across the invoices (%v), want exactly 1", chargeLines, err)
	}
}

// RULE (ADR 0005 8.4): a non stock line carries cost only through a linked
// RECEIVED purchase order line, at that line's cost (never the estimate on the
// order line); with none received it posts no cost and that is not an error.
func TestNonStockLineCostComesFromTheReceivedPurchaseOrderLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	ctx := context.Background()
	vendor, po, poLine := uuid.New(), uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "nsv-"+vendor.String()[:8])
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'SPECIAL_ORDER', (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	mk := func(estimate int) (orderID, lineID string) {
		r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{
			"description": "Custom millwork", "quantity": "2", "uom": "EA", "unit_price_ten_thousandths": 100000,
			"is_special_order": true, "special_order_unit_cost_ten_thousandths": estimate,
		}))
		if r.status != 201 {
			t.Fatalf("create = %d: %s", r.status, r.raw)
		}
		orderID = str(t, r.body, "id")
		lineID = str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
		r = f.transition(orderID, 1, "confirmed")
		if r.status != 200 {
			t.Fatalf("confirm = %d: %s", r.status, r.raw)
		}
		return orderID, lineID
	}

	// Not received yet: no cost, no error, even with an estimate on the line.
	o1, l1 := mk(30000)
	must(`INSERT INTO purchase_order_lines (id, po_id, description, quantity, cost, qty_received, linked_so_line_id) VALUES ($1, $2, 'special', 2, 4.00, 0, $3)`, poLine, po, l1)
	g := f.do("GET", "/api/v1/orders/"+o1, nil)
	r := f.fulfil(o1, revision(t, g), map[string]any{"picked_up_by": "Counter"})
	if r.status != 201 {
		t.Fatalf("fulfil before the receipt = %d: %s", r.status, r.raw)
	}
	if _, legs := f.entryLegs(invoiceIDOf(t, r)); legs["5010"].debit != 0 || legs["4010"].credit != 2000 {
		t.Errorf("unreceived special order legs = %+v, want revenue 2000 and no cost", legs)
	}

	// Received: cost = the purchase order line's 4.00 x 2, not the 3.00 estimate.
	o2, l2 := mk(30000)
	must(`UPDATE purchase_order_lines SET linked_so_line_id = $2, qty_received = 2 WHERE id = $1`, poLine, l2)
	g = f.do("GET", "/api/v1/orders/"+o2, nil)
	r = f.fulfil(o2, revision(t, g), map[string]any{"picked_up_by": "Counter"})
	if r.status != 201 {
		t.Fatalf("fulfil after the receipt = %d: %s", r.status, r.raw)
	}
	if _, legs := f.entryLegs(invoiceIDOf(t, r)); legs["5010"].debit != 800 || legs["1030"].credit != 800 {
		t.Errorf("received special order legs = %+v, want 5010 dr 800 and 1030 cr 800 (the purchase cost, not the 600 estimate)", legs)
	}
}

// RULE (ADR 0008 3.4 row 2, ADR 0005 8.4 as PR 35 amends it; PR 35 review P2-D):
// a non stock line relieves 1030 from its linked receipt lines' posted values
// pro rata to the billed quantity, the last bill taking the remainder, never
// from the first linked purchase line's cost. Two purchase lines fill one
// order line of 3 (1 at 3.33 posts 333, 2 at 5.00 posts 1000: 1333 in all);
// the first bill of 1 relieves round(1333 / 3) = 444 and the last takes the
// remaining 889, so 1030 nets to the 1333 the receipts posted. The first line's
// cost alone would relieve 3 x 3.33 = 999.
func TestNonStockReliefComesFromTheLinkedReceiptsProRata(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	ctx := context.Background()
	vendor, po := uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "pr-"+vendor.String()[:8])
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'SPECIAL_ORDER', (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{
		"description": "Custom millwork", "quantity": "3", "uom": "EA", "unit_price_ten_thousandths": 100000,
		"is_special_order": true, "special_order_unit_cost_ten_thousandths": 30000,
	}))
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
	must(`INSERT INTO purchase_order_lines (id, po_id, description, quantity, cost, qty_received, linked_so_line_id, created_at) VALUES ($1, $2, 'first', 1, 3.33, 1, $3, now() - interval '1 minute')`, uuid.New(), po, lineID)
	must(`INSERT INTO purchase_order_lines (id, po_id, description, quantity, cost, qty_received, linked_so_line_id) VALUES ($1, $2, 'second', 2, 5.00, 2, $3)`, uuid.New(), po, lineID)
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}

	one := f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter", "lines": []map[string]any{{"order_line_id": lineID, "quantity": "1"}}})
	if one.status != 201 {
		t.Fatalf("first bill = %d: %s", one.status, one.raw)
	}
	if _, legs := f.entryLegs(invoiceIDOf(t, one)); legs["5010"].debit != 444 || legs["1030"].credit != 444 {
		t.Errorf("first bill legs = %+v, want 444 (1333 x 1 / 3)", legs)
	}
	rest := f.fulfil(id, revision(t, one), map[string]any{"picked_up_by": "Counter"})
	if rest.status != 201 {
		t.Fatalf("last bill = %d: %s", rest.status, rest.raw)
	}
	if _, legs := f.entryLegs(invoiceIDOf(t, rest)); legs["5010"].debit != 889 || legs["1030"].credit != 889 {
		t.Errorf("last bill legs = %+v, want the 889 remainder (1333 less 444)", legs)
	}
}

// RULE (ADR 0005 8.4 as PR 35 amends it; C2-2b implements the amendment ahead
// of that merge): a STOCKED special order line relieves COGS at the moving
// average, because its receipt entered stock and moved the average; the
// purchase cost applies only to non stock lines (no product, a direct ship
// whose goods never enter stock). The reviewer's worked example: 10 on hand at
// 5.00, a special order receipt of 10 at 9.00 gives 20 on hand at an average
// of 7.00; selling the 10 special order units at the 9.00 purchase cost
// credits 1030 with 90.00 against 70.00 of stock and leaves a 20.00 residue.
// At the average there is none.
func TestStockedSpecialOrderRelievesCOGSAtTheMovingAverage(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	ctx := context.Background()
	vendor, po, poLine := uuid.New(), uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "so-"+vendor.String()[:8])
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'SPECIAL_ORDER', (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	// The order: a stocked special order line for 10 (a product, so its
	// receipt entered stock).
	r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{
		"product_id": f.productID.String(), "quantity": "10",
		"is_special_order": true, "special_order_unit_cost_ten_thousandths": 50000,
	}))
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	orderID := str(t, r.body, "id")
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")

	// The state its receipt left: the received purchase order line (10 at
	// 9.00) linked to the order line, 20 on hand (10 at 5.00 plus the 10
	// received) and the average moved to 7.00.
	must(`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost, qty_received, linked_so_line_id) VALUES ($1, $2, $3, 'special', 10, 9.00, 10, $4)`, poLine, po, f.productID, lineID)
	f.stock(f.productID, "20")
	f.setCost(f.productID, "7")

	if r := f.transition(orderID, 1, "confirmed"); r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}
	g := f.do("GET", "/api/v1/orders/"+orderID, nil)
	r = f.fulfil(orderID, revision(t, g), map[string]any{"picked_up_by": "Counter"})
	if r.status != 201 {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}

	// COGS relieves at the moving average: 10 x 7.00 = 70.00, no residue in
	// 1030 against the stock that is left (10 at 7.00).
	if _, legs := f.entryLegs(invoiceIDOf(t, r)); legs["5010"].debit != 7000 || legs["1030"].credit != 7000 {
		t.Errorf("stocked special order legs = %+v, want 5010 dr 7000 and 1030 cr 7000 (the 7.00 average, not the 9.00 purchase cost)", legs)
	}
	var cost string
	if err := db.Pool.QueryRow(ctx,
		`SELECT unit_cost::text || '/' || cost::text FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id WHERE i.order_id = $1 AND l.product_id IS NOT NULL`, orderID).
		Scan(&cost); err != nil || cost != "7.0000/70.00" {
		t.Errorf("invoice line unit_cost/cost = %s (%v), want the 7.0000 average and 70.00", cost, err)
	}
}
