// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// The invoice and credit memo modules end to end: a real Postgres, the real
// order, invoice, inventory, general ledger and account services wired as serve
// wires them, the real handlers on one mux behind the real idempotency
// middleware and the branch middleware. Requests are JSON and responses are read
// back as JSON, so each test states a wire fact. The orders that produce the
// invoices are made through the order routes (create, confirm, fulfil).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	orders     *order.Service
	invoices   *invoice.Service
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
	branchID   uuid.UUID
	yardID     uuid.UUID
}

const defaultBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// newFixture builds a customer, a product costing 3.25 and priced 5.50 (a
// pickup order's tax is the branch rate, 8.875 percent) with 100 on hand, and
// serves the order and invoice routes. opts tune the services before they are
// served (an event recorder that fails, a branch guard).
func newFixture(t *testing.T, db *database.DB, opts ...func(*fixture)) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "INV-" + uuid.NewString()[:8]}
	if err := db.Pool.QueryRow(ctx, `SELECT `+defaultBranch).Scan(&f.branchID); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	mustExec(t, db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Wire Invoice Co', $2, `+defaultBranch+`)`,
		f.customerID, "WINV-"+uuid.NewString()[:8])
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5, 3.25)`,
		f.productID, f.sku)
	mustExec(t, db, `UPDATE locations SET default_tax_rate = 0.088750 WHERE id = `+defaultBranch)
	f.yardID = uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+defaultBranch+`, `+defaultBranch+`)`,
		f.yardID, "IV-"+f.yardID.String()[:8])
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`, f.productID, f.yardID)

	logger := slog.Default()
	glSvc := gl.NewService(gl.NewRepository(db), nil, logger)
	acct := account.NewService(db, glSvc, logger)
	stock := inventory.NewService(inventory.NewRepository(db))
	f.invoices = invoice.NewService(invoice.NewRepository(db), glSvc, acct, db).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(outbox.NewWriter(db, "")).WithStock(stock)
	f.orders = order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAuditLog(audit.NewLogger(db)).WithInventory(stock).WithInvoices(f.invoices)
	f.invoices.WithOrders(f.orders)
	for _, o := range opts {
		o(f)
	}

	mux := http.NewServeMux()
	order.NewHandler(f.orders).RegisterRoutes(mux)
	invoice.NewHandler(f.invoices).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(roleClaims(middleware.NewBranchMiddleware(db).Handler(mux))))

	t.Cleanup(func() {
		f.srv.Close()
		f.cleanup()
	})
	return f
}

func mustExec(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

// roleClaims stands in for authentication: X-Test-Role becomes the claims a JWT
// would carry; X-Test-Sub its subject.
func roleClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{Role: role, Roles: []string{role}}
			claims.Subject = r.Header.Get("X-Test-Sub")
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
		}
		next.ServeHTTP(w, r)
	})
}

// cleanup removes everything the fixture's customer produced, children first.
func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) { _, _ = f.db.Pool.Exec(ctx, sql, args...) }
	invs := `(SELECT id FROM invoices WHERE customer_id = $1)`
	cms := `(SELECT id FROM credit_memos WHERE customer_id = $1)`
	entries := `(SELECT id FROM gl_journal_entries WHERE source_ref_id IN ` + invs + ` OR source_ref_id IN ` + cms + `)`
	exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE reverses_entry_id IN `+entries+`)`, f.customerID)
	exec(`DELETE FROM gl_journal_entries WHERE reverses_entry_id IN `+entries, f.customerID)
	exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN `+entries, f.customerID)
	exec(`DELETE FROM gl_journal_entries WHERE id IN `+entries, f.customerID)
	exec(`DELETE FROM events_outbox WHERE entity_id IN `+invs+` OR entity_id IN `+cms+` OR entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
	exec(`DELETE FROM audit_log WHERE entity_id IN `+invs+` OR entity_id IN `+cms+` OR entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
	exec(`DELETE FROM customer_transactions WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id IN `+cms, f.customerID)
	exec(`DELETE FROM credit_memos WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM payment_refunds WHERE payment_id IN (SELECT id FROM payments WHERE customer_id = $1) OR credit_memo_id IN `+cms, f.customerID)
	exec(`DELETE FROM ar_applications WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM payments WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM invoice_lines WHERE invoice_id IN `+invs, f.customerID)
	exec(`DELETE FROM invoices WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
	exec(`DELETE FROM orders WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM inventory WHERE location_id = $1`, f.yardID)
	exec(`DELETE FROM locations WHERE id = $1`, f.yardID)
	exec(`DELETE FROM products WHERE id = $1`, f.productID)
	exec(`DELETE FROM customers WHERE id = $1`, f.customerID)
}

// liveApplication writes a payment of the fixture's customer and a live PAYMENT
// application of cents on the invoice, by raw SQL: the refusals under test read
// the application rows, not the money that made them.
func (f *fixture) liveApplication(invoiceID string, cents int64) {
	f.t.Helper()
	pay := uuid.New()
	mustExec(f.t, f.db, `INSERT INTO payments (id, customer_id, branch_id, currency, method, amount, amount_unapplied, received_on)
		VALUES ($1, $2, `+defaultBranch+`, 'USD', 'CASH', $3::bigint::numeric / 100, 0, CURRENT_DATE)`, pay, f.customerID, cents)
	mustExec(f.t, f.db, `INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, act_id)
		VALUES ($1, 'USD', 'PAYMENT', $2, $3, $4::bigint::numeric / 100, CURRENT_DATE, gen_random_uuid())`, f.customerID, pay, invoiceID, cents)
}

// liveCreditApplication writes a live CREDIT_MEMO application of the invoice's
// applied credit memo.
func (f *fixture) liveCreditApplication(invoiceID string) {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO ar_applications (customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, act_id)
		SELECT $1, 'USD', 'CREDIT_MEMO', id, $2, 5, CURRENT_DATE, gen_random_uuid() FROM credit_memos WHERE invoice_id = $2 AND status = 'APPLIED'`, f.customerID, invoiceID)
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	return doOn(f.t, f.srv.URL, method, path, body, headers...)
}

func doOn(t *testing.T, base, method, path string, body any, headers ...string) resp {
	t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-inv-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func num(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want an integer: %v", key, n, err)
	}
	return v
}

func rev(t *testing.T, r resp) int64 { return num(t, r.body, "revision") }

func errorOf(t *testing.T, r resp) (code string, blockers []string, fields []string) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	code, _ = e["code"].(string)
	if ds, ok := e["details"].([]any); ok {
		for _, d := range ds {
			dm := d.(map[string]any)
			if c, _ := dm["code"].(string); c != "" {
				blockers = append(blockers, c)
			}
			if fld, _ := dm["field"].(string); fld != "" {
				fields = append(fields, fld)
			}
		}
	}
	if meta, _ := r.body["meta"].(map[string]any); meta["request_id"] != "req-inv-test" {
		t.Errorf("meta.request_id = %v, want the request's id", meta["request_id"])
	}
	return code, blockers, fields
}

// ---------------------------------------------------------------------------
// Producing invoices through the order routes.
// ---------------------------------------------------------------------------

// pickupLine is a stocked product line of qty.
func (f *fixture) pickupLine(qty string) map[string]any {
	return map[string]any{"product_id": f.productID.String(), "quantity": qty}
}

// order makes a pickup order of the lines (a single line of 10 by default),
// confirmed. It answers the order id and its revision.
func (f *fixture) confirmedOrder(lines ...map[string]any) (string, int64) {
	f.t.Helper()
	if len(lines) == 0 {
		lines = []map[string]any{f.pickupLine("10")}
	}
	r := f.do("POST", "/api/v1/orders", map[string]any{"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": lines})
	if r.status != http.StatusCreated {
		f.t.Fatalf("create order = %d: %s", r.status, r.raw)
	}
	id := str(f.t, r.body, "id")
	r = f.do("POST", "/api/v1/orders/"+id+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if r.status != http.StatusOK || str(f.t, r.body, "status") != "confirmed" {
		f.t.Fatalf("confirm order = %d %v: %s", r.status, r.body["status"], r.raw)
	}
	return id, rev(f.t, r)
}

// fulfil bills the order (all of it when lines is nil) and answers the invoice
// id and the order's new revision.
func (f *fixture) fulfil(orderID string, revision int64, lines []map[string]any) (invoiceID string, newRev int64) {
	f.t.Helper()
	body := map[string]any{"revision": revision, "picked_up_by": "Counter customer"}
	if lines != nil {
		body["lines"] = lines
	}
	r := f.do("POST", "/api/v1/orders/"+orderID+"/fulfillments", body)
	if r.status != http.StatusCreated {
		f.t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, "/api/v1/invoices/") {
		f.t.Fatalf("Location = %q", loc)
	}
	return strings.TrimPrefix(loc, "/api/v1/invoices/"), rev(f.t, r)
}

// invoice makes an order of qty, confirms it and bills it all: one invoice.
func (f *fixture) invoice(qty string) (invoiceID, orderID string) {
	f.t.Helper()
	orderID, r := f.confirmedOrder(f.pickupLine(qty))
	invoiceID, _ = f.fulfil(orderID, r, nil)
	return invoiceID, orderID
}

func (f *fixture) getInvoice(id string) resp {
	f.t.Helper()
	r := f.do("GET", "/api/v1/invoices/"+id, nil)
	if r.status != http.StatusOK {
		f.t.Fatalf("get invoice = %d: %s", r.status, r.raw)
	}
	return r
}

// firstLineID is the id of the invoice's first line.
func (f *fixture) firstLineID(invoiceID string) string {
	f.t.Helper()
	r := f.getInvoice(invoiceID)
	lines := r.body["lines"].([]any)
	return lines[0].(map[string]any)["id"].(string)
}

func (f *fixture) voidInvoice(id string, revision int64, reason string, headers ...string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/invoices/"+id+"/transitions", map[string]any{"to": "void", "revision": revision, "reason": reason}, headers...)
}

// creditBody is a credit memo request of the lines against the invoice.
func (f *fixture) creditBody(invoiceID string, lines ...map[string]any) map[string]any {
	return map[string]any{"invoice_id": invoiceID, "reason_code": "return", "reason": "customer returned goods", "lines": lines}
}

func returnLine(invoiceLineID, qty string, restock bool) map[string]any {
	return map[string]any{"invoice_line_id": invoiceLineID, "quantity": qty, "restock": restock}
}

func (f *fixture) createCredit(body map[string]any) resp {
	f.t.Helper()
	r := f.do("POST", "/api/v1/credit-memos", body)
	if r.status != http.StatusCreated {
		f.t.Fatalf("create credit memo = %d: %s", r.status, r.raw)
	}
	return r
}

func (f *fixture) postCredit(id string, revision int64) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "open", "revision": revision})
}

// ---------------------------------------------------------------------------
// Reading the ledger and the stock.
// ---------------------------------------------------------------------------

type leg struct{ debit, credit int64 }

// entryLegs sums, per account code, the legs of the entries whose source
// reference is ref (the invoice or credit memo id).
func (f *fixture) entryLegs(ref string) (entries int, legs map[string]leg) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, ref).Scan(&entries); err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx, `
		SELECT a.code, ROUND(SUM(l.debit) * 100)::bigint, ROUND(SUM(l.credit) * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id = $1 GROUP BY a.code`, ref)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	legs = map[string]leg{}
	for rows.Next() {
		var code string
		var l leg
		if err := rows.Scan(&code, &l.debit, &l.credit); err != nil {
			f.t.Fatal(err)
		}
		legs[code] = l
	}
	return entries, legs
}

// reversalLegs sums the legs of the reversal of an invoice's or credit memo's
// entry.
func (f *fixture) reversalLegs(ref string) (entries int, legs map[string]leg) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id WHERE o.source_ref_id = $1`, ref).Scan(&entries); err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx, `
		SELECT a.code, ROUND(SUM(l.debit) * 100)::bigint, ROUND(SUM(l.credit) * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries r ON r.id = l.journal_entry_id
		JOIN gl_journal_entries o ON o.id = r.reverses_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE o.source_ref_id = $1 GROUP BY a.code`, ref)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	legs = map[string]leg{}
	for rows.Next() {
		var code string
		var l leg
		if err := rows.Scan(&code, &l.debit, &l.credit); err != nil {
			f.t.Fatal(err)
		}
		legs[code] = l
	}
	return entries, legs
}

// stock reads on hand/allocated of the fixture's product.
func (f *fixture) stock() string {
	f.t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT sum(quantity)::text || '/' || sum(allocated)::text FROM inventory WHERE product_id = $1`, f.productID).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// balance reads the customer's subledger balance in cents.
func (f *fixture) balance() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, f.customerID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// eventTypes lists an entity's event types in order.
func eventTypes(t *testing.T, db *database.DB, entityType, entityID string) []string {
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

func countOf(t *testing.T, db *database.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// failOn is an event recorder that fails for one event type.
type failOn struct {
	inner invoice.EventRecorder
	typ   string
}

func (f failOn) Write(ctx context.Context, ev outbox.Event) error {
	if ev.Type == f.typ {
		return fmt.Errorf("outbox insert failed")
	}
	return f.inner.Write(ctx, ev)
}

// counterNext reads the gapless counter's next value.
func counterNext(t *testing.T, db *database.DB, series string) int64 {
	t.Helper()
	return countOf(t, db, `SELECT next_value FROM document_counters WHERE series = $1`, series)
}
