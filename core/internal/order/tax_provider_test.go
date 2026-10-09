// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The tax provider path of ADR 0005 section 3 and the credit exposure of
// section 5.3, on the wire: a configured provider prices the confirm BEFORE
// the transaction opens (tax_source provider, no rate); a provider failure is
// 503 with nothing written; lines that move between the provider call and
// the lock are 409 tax_quote_stale; an OVERDUE invoice counts in the credit
// exposure.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
)

// fakeTax is a configured provider: it answers a fixed tax, can fail, and
// can run a hook while the call is in flight (to move the lines under it).
type fakeTax struct {
	calls int
	tax   int64
	err   error
	hook  func()
}

func (f *fakeTax) Configured() bool { return true }

func (f *fakeTax) PreviewTax(context.Context, *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	f.calls++
	if f.hook != nil {
		f.hook()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &tax.TaxResult{TotalTax: f.tax}, nil
}

// withProvider serves the order routes again with the provider wired.
func (f *fixture) withProvider(p order.TaxProvider) {
	f.t.Helper()
	svc := order.NewService(order.NewRepository(f.db)).WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).
		WithAuditLog(audit.NewLogger(f.db)).WithTaxProvider(p)
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(middleware.Idempotency(f.db)(middleware.NewBranchMiddleware(f.db).Handler(mux)))
	f.t.Cleanup(srv.Close)
	f.srv = srv
}

// RULE (ADR 0005 3): the provider prices the confirm; the order carries its
// answer as tax_source provider with no rate.
func TestOrderConfirmTaxProviderPath(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	prov := &fakeTax{tax: 321}
	f.withProvider(prov)

	r := f.create() // 5500 subtotal
	id := str(t, r.body, "id")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 {
		t.Fatalf("confirm = %d: %s", r.status, r.raw)
	}
	if prov.calls != 2 {
		t.Errorf("provider calls = %d, want 2 (the create's estimate and the confirm's price)", prov.calls)
	}
	if src := str(t, r.body, "tax_source"); src != "provider" {
		t.Errorf("tax_source = %q, want provider", src)
	}
	if r.body["tax_rate_percent"] != nil {
		t.Errorf("tax_rate_percent = %v, want null: the provider answered, no rate", r.body["tax_rate_percent"])
	}
	if tax := num(t, r.body, "tax_cents"); tax != 321 {
		t.Errorf("tax_cents = %d, want the provider's 321", tax)
	}
	if total := num(t, r.body, "total_cents"); total != 5500+321 {
		t.Errorf("total_cents = %d, want 5821", total)
	}
}

// RULE (ADR 0005 3): a provider failure is 503 and nothing is written: the
// order stays a draft on its revision with no event beyond order.created.
func TestOrderConfirmTaxProviderFailureIs503AndWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	prov := &fakeTax{err: errors.New("avalara down")}
	f.withProvider(prov)

	// The create is priced by the provider too: it fails before any row.
	if r := f.do("POST", "/api/v1/orders", f.createBody()); r.status != http.StatusServiceUnavailable {
		t.Fatalf("create with the provider down = %d, want 503: %s", r.status, r.raw)
	}
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d orders written by the refused create (%v)", n, err)
	}
	prov.err = nil
	r := f.create()
	id := str(t, r.body, "id")
	prov.err = errors.New("avalara down")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != http.StatusServiceUnavailable {
		t.Fatalf("confirm with the provider down = %d, want 503: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "unavailable" {
		t.Errorf("code = %q, want unavailable", code)
	}
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "draft" || revision(t, g) != 1 {
		t.Errorf("the refused confirm moved the order: %s", g.raw)
	}
	if ev := eventsFor(t, db, id); len(ev) != 1 || ev[0] != "order.created" {
		t.Errorf("events = %v, want only order.created", ev)
	}
}

// RULE (ADR 0005 3): lines that change between the provider call and the
// transaction's lock are 409 tax_quote_stale; the order is untouched and a
// retry prices afresh.
func TestOrderConfirmTaxQuoteStale(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	prov := &fakeTax{tax: 100}
	f.withProvider(prov)

	r := f.create()
	id := str(t, r.body, "id")
	moved := false
	prov.hook = func() {
		if moved {
			return
		}
		moved = true
		if _, err := db.Pool.Exec(context.Background(), `UPDATE order_lines SET quantity = quantity + 1 WHERE order_id = $1`, id); err != nil {
			t.Errorf("move the lines under the provider: %v", err)
		}
	}
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != http.StatusConflict {
		t.Fatalf("confirm with moved lines = %d, want 409: %s", r.status, r.raw)
	}
	if _, _, details := errorOf(t, r); len(details) != 1 || details[0]["code"] != "tax_quote_stale" {
		t.Errorf("details = %v, want tax_quote_stale", details)
	}
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "draft" {
		t.Errorf("the refused confirm moved the order: %s", g.raw)
	}
	// The retry prices afresh and confirms.
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Errorf("the retry = %d %q: %s", r.status, r.body["status"], r.raw)
	}
}

// RULE (ADR 0005 5.3): an overdue invoice (unpaid, past its due date; OVERDUE is
// no longer a stored status) counts in the credit exposure like any open one;
// a PAID one does not, and neither does a VOID one.
func TestOrderCreditExposureCountsOverdueInvoices(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	// A limit of 100.00 against a 59.88 order: within it on its own.
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 100.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	seedInvoice := func(status string, total float64) {
		t.Helper()
		_, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, order_id, customer_id, branch_id, status, total_amount, subtotal, tax_amount, due_date, voided_at)
			VALUES (gen_random_uuid(), (SELECT id FROM orders WHERE customer_id = $1 LIMIT 1), $1,
			        (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), $2, $3, $3, 0, CURRENT_DATE - 40,
			        CASE WHEN $2 = 'VOID' THEN NOW() END)`,
			f.customerID, status, total)
		if err != nil {
			t.Fatal(err)
		}
	}
	// invoices need an order row to point at: a settled earlier order.
	seed := f.create()
	seedID := str(t, seed.body, "id")
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, f.customerID) })
	if _, err := db.Pool.Exec(ctx, `UPDATE orders SET status = 'CANCELLED' WHERE id = $1`, seedID); err != nil {
		t.Fatal(err)
	}

	seedInvoice("PAID", 500)
	seedInvoice("VOID", 500)
	r := f.create()
	r = f.do("POST", "/api/v1/orders/"+str(t, r.body, "id")+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm beside a PAID and a VOID invoice = %d %q, want confirmed: %s", r.status, r.body["status"], r.raw)
	}
	// Cancel it: a live order's unbilled remainder counts in the exposure too,
	// and this test isolates the invoice.
	r = f.do("POST", "/api/v1/orders/"+str(t, r.body, "id")+"/transitions", map[string]any{"to": "cancelled", "revision": 2, "reason": "test"})
	if r.status != 200 {
		t.Fatalf("cancel = %d: %s", r.status, r.raw)
	}

	seedInvoice("UNPAID", 60) // due 40 days ago: overdue
	r = f.create()
	r = f.do("POST", "/api/v1/orders/"+str(t, r.body, "id")+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != 200 || str(t, r.body, "status") != "on_hold" {
		t.Fatalf("confirm beside an overdue invoice = %d %q, want on_hold (60 open plus 59.88 is over 100): %s", r.status, r.body["status"], r.raw)
	}
	if reason := str(t, r.body, "hold_reason"); reason != "credit_limit" {
		t.Errorf("hold_reason = %q, want credit_limit", reason)
	}
}
