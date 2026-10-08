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
}

func (f failOn) Write(ctx context.Context, ev outbox.Event) error {
	if ev.Type == f.typ {
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
	f.serveWith(f.withStock(), f.withMoney(), func(s *order.Service) *order.Service {
		return s.WithOutbox(failOn{inner: outbox.NewWriter(db, ""), typ: "order.fulfilled"})
	})
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create()
	id := str(t, r.body, "id")
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
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		JOIN gl_accounts a ON a.id = l.account_id WHERE a.code = '5010' AND e.memo LIKE 'Invoice %' AND e.created_at > now() - interval '1 minute' AND e.source_ref_id NOT IN (SELECT id FROM invoices)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d orphan COGS entries survived the rollback (%v)", n, err)
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

