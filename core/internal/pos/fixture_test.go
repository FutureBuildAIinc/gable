// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter end to end (ADR 0005 section 14.2 C2-5): a real Postgres, the
// real invoice, inventory, general ledger and account services wired as
// serve wires them, the real pos handler on one mux behind the real
// idempotency and branch middleware. Requests are JSON and responses are
// read back as JSON, so each test states a wire fact.

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
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t            *testing.T
	db           *database.DB
	srv          *httptest.Server
	service      *pos.Service
	customerID   uuid.UUID
	productID    uuid.UUID
	kitID        uuid.UUID
	sku          string
	register     string
	branchID     uuid.UUID
	yardID       uuid.UUID
	events       *recordingWriter
	priorEntries map[string]bool
}

const defaultBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// recordingWriter wraps the outbox writer, remembering what was written and
// optionally failing one type (the transaction proofs).
type recordingWriter struct {
	inner pos.EventRecorder
	fail  string
	seen  []string
}

func (w *recordingWriter) Write(ctx context.Context, ev outbox.Event) error {
	if ev.Type == w.fail {
		return fmt.Errorf("outbox insert failed")
	}
	w.seen = append(w.seen, ev.Type)
	return w.inner.Write(ctx, ev)
}

// newFixture builds a walk-in-ready branch: a customer, a product costing
// 3.25 and priced 5.50 with 100 on hand (the branch rate is 8.875 percent),
// a kit of two of them, and serves the pos routes. opts tune the service
// before it is served.
func newFixture(t *testing.T, db *database.DB, opts ...func(*fixture)) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), kitID: uuid.New(),
		sku: "POS-" + uuid.NewString()[:8], register: "REG-" + uuid.NewString()[:8]}
	if err := db.Pool.QueryRow(ctx, `SELECT `+defaultBranch).Scan(&f.branchID); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	mustExec(t, db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Counter Co', $2, `+defaultBranch+`)`,
		f.customerID, "WPOS-"+uuid.NewString()[:8])
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5, 3.25)`,
		f.productID, f.sku)
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost, is_kit) VALUES ($1, $2, 'bundle kit', 'EA', 11.00, 6.50, TRUE)`,
		f.kitID, "KIT-"+uuid.NewString()[:8])
	mustExec(t, db, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position) VALUES ($1, $2, 2, 0)`,
		f.kitID, f.productID)
	mustExec(t, db, `UPDATE locations SET default_tax_rate = 0.088750 WHERE id = `+defaultBranch)
	f.yardID = uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+defaultBranch+`, `+defaultBranch+`)`,
		f.yardID, "PY-"+f.yardID.String()[:8])
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`, f.productID, f.yardID)
	mustExec(t, db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'Counter test', `+defaultBranch+`) ON CONFLICT (id) DO UPDATE SET location_id = EXCLUDED.location_id, branch_id = EXCLUDED.branch_id`, f.register, f.yardID)

	logger := slog.Default()
	glSvc := gl.NewService(gl.NewRepository(db), nil, logger)
	acct := account.NewService(db, glSvc, logger)
	stock := inventory.NewService(inventory.NewRepository(db))
	invoices := invoice.NewService(invoice.NewRepository(db), glSvc, acct, db).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(outbox.NewWriter(db, "")).WithStock(stock)
	f.events = &recordingWriter{inner: outbox.NewWriter(db, "")}
	f.service = pos.NewService(db, pos.NewRepository(db), logger).
		WithInvoices(invoices).WithAR(acct).WithInventory(stock).WithLedger(glSvc).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(f.events).WithTxRunner(db)
	for _, o := range opts {
		o(f)
	}

	// the ledger entries that exist before this fixture's acts: its balance
	// reads exclude them, so one test never counts another's entries.
	f.priorEntries = map[string]bool{}
	if rows, err := db.Pool.Query(ctx, `SELECT id::text FROM gl_journal_entries`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				f.priorEntries[id] = true
			}
		}
		rows.Close()
	}

	mux := http.NewServeMux()
	pos.NewHandler(f.service).RegisterRoutes(mux)
	invoice.NewHandler(invoices).RegisterRoutes(mux)
	// the payment route, as serve mounts it: the contention tests race real
	// walk-in payments against the sales
	payments := payment.NewService(db, payment.NewRepository(db), acct)
	payment.NewHandler(payments).RegisterRoutes(mux)
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

// roleClaims stands in for authentication: X-Test-Role becomes the claims a
// JWT would carry; X-Test-Sub its subject.
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

// cleanup removes everything the fixture produced, children first, in the
// foreign key order the sweep proved: the walk-in customer's rows back the
// fixture's sales, so they go too.
func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.db.Pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("cleanup: %s: %v", strings.Join(strings.Fields(sql), " ")[:min(80, len(strings.Join(strings.Fields(sql), " ")))], err)
		}
	}
	walkIn := `(SELECT id FROM customers WHERE account_number = 'WALK-IN')`
	sales := `(SELECT id FROM pos_transactions)`
	invs := `(SELECT id FROM invoices WHERE customer_id IN ($1, ` + walkIn + `) OR id IN (SELECT invoice_id FROM pos_transactions WHERE invoice_id IS NOT NULL))`
	cms := `(SELECT id FROM credit_memos WHERE customer_id IN ($1, ` + walkIn + `) OR id IN (SELECT credit_memo_id FROM pos_returns WHERE credit_memo_id IS NOT NULL))`
	pays := `(SELECT id FROM payments WHERE customer_id IN ($1, ` + walkIn + `))`
	// the ledger entries are captured before their source documents go, so
	// the entry delete never misses one that named a payment or an
	// application deleted below
	var entryIDs []string
	// Every entry the fixture's documents own, and every reversal of one of
	// those entries (a void reverses receipt and application entries too, and
	// a reversal whose original goes would trip the reverses foreign key and
	// strand the whole family).
	if rows, err := f.db.Pool.Query(ctx, `SELECT e.id::text FROM gl_journal_entries e WHERE e.source_ref_id::text IN
		(SELECT id::text FROM invoices WHERE id IN `+invs+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM payments WHERE id IN `+pays+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM ar_applications WHERE invoice_id IN `+invs+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms+`)
		OR e.reverses_entry_id IN (SELECT o.id FROM gl_journal_entries o WHERE o.source_ref_id::text IN
			((SELECT id::text FROM invoices WHERE id IN `+invs+`)
			UNION (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
			UNION (SELECT id::text FROM payments WHERE id IN `+pays+`)
			UNION (SELECT id::text FROM ar_applications WHERE invoice_id IN `+invs+`)
			UNION (SELECT id::text FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms+`)))`, f.customerID); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				entryIDs = append(entryIDs, id)
			}
		}
		rows.Close()
	}
	exec(`DELETE FROM events_outbox WHERE entity_id::text IN (SELECT id::text FROM invoices WHERE id IN `+invs+`)
		OR entity_id::text IN (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
		OR entity_id::text IN (SELECT id::text FROM payments WHERE id IN `+pays+`)
		OR entity_id::text IN (SELECT id::text FROM pos_transactions) OR entity_id::text IN (SELECT id::text FROM pos_returns)
		OR entity_id::text IN (SELECT id::text FROM till_sessions) OR entity_id = $1`, f.customerID)
	exec(`DELETE FROM audit_log WHERE entity_id::text IN (SELECT id::text FROM invoices WHERE id IN `+invs+`)
		OR entity_id::text IN (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
		OR entity_id::text IN (SELECT id::text FROM pos_transactions) OR entity_id::text IN (SELECT id::text FROM pos_returns)`, f.customerID)
	exec(`DELETE FROM ar_applications WHERE invoice_id IN `+invs+` OR payment_id IN `+pays+` OR credit_memo_id IN `+cms, f.customerID)
	exec(`DELETE FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms, f.customerID)
	exec(`DELETE FROM customer_transactions WHERE customer_id IN ($1, `+walkIn+`)`, f.customerID)
	exec(`DELETE FROM pos_return_lines WHERE return_id IN (SELECT id FROM pos_returns)`)
	exec(`DELETE FROM pos_returns`)
	exec(`DELETE FROM pos_tenders WHERE transaction_id IN ` + sales)
	exec(`DELETE FROM pos_line_items WHERE transaction_id IN ` + sales)
	exec(`DELETE FROM pos_transactions`)
	exec(`DELETE FROM payments WHERE id IN `+pays, f.customerID)
	exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id IN `+cms, f.customerID)
	exec(`DELETE FROM credit_memos WHERE id IN `+cms, f.customerID)
	exec(`DELETE FROM invoice_lines WHERE invoice_id IN `+invs, f.customerID)
	exec(`DELETE FROM invoices WHERE id IN `+invs, f.customerID)
	if len(entryIDs) > 0 {
		exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id = ANY($1::uuid[])`, entryIDs)
		exec(`DELETE FROM gl_journal_entries WHERE id = ANY($1::uuid[])`, entryIDs)
	}

	exec(`DELETE FROM pos_sync_log`)
	exec(`DELETE FROM till_z_reports`)
	exec(`DELETE FROM till_sessions`)
	exec(`DELETE FROM inventory WHERE location_id = $1`, f.yardID)
	exec(`DELETE FROM pos_registers WHERE id = $1`, f.register)
	exec(`DELETE FROM locations WHERE id = $1`, f.yardID)
	exec(`DELETE FROM product_kit_components WHERE kit_product_id = $1`, f.kitID)
	exec(`UPDATE customers SET balance_due = 0 WHERE id IN ($1, `+walkIn+`)`, f.customerID)
	exec(`DELETE FROM products WHERE id = $1 OR id = $2`, f.productID, f.kitID)
	exec(`DELETE FROM customers WHERE id = $1`, f.customerID)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-pos-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
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
	return code, blockers, fields
}

// ---------------------------------------------------------------------------
// Making sales through the routes.
// ---------------------------------------------------------------------------

// startSale opens a cart on the fixture's register.
func (f *fixture) startSale(customer *uuid.UUID) string {
	f.t.Helper()
	body := map[string]any{"register_id": f.register}
	if customer != nil {
		body["customer_id"] = customer.String()
	}
	r := f.do("POST", "/api/v1/pos/transactions", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		f.t.Fatalf("start sale = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/pos/transactions/") {
		f.t.Errorf("Location = %q", loc)
	}
	return str(f.t, r.body, "id")
}

func mustUUID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

// addLine adds a product line of qty to the sale.
func (f *fixture) addLine(saleID string, line map[string]any) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items", map[string]any{"line": line},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// productLine is a stocked product line of qty.
func (f *fixture) productLine(qty string) map[string]any {
	return map[string]any{"product_id": f.productID.String(), "quantity": qty}
}

// tender is one tender of cents by method.
func tender(method string, cents int64) map[string]any {
	return map[string]any{"method": method, "amount_cents": cents}
}

// completeSale completes the sale with the tenders and the sale's current
// revision, answering the body.
func (f *fixture) completeSale(saleID string, tenders ...map[string]any) resp {
	f.t.Helper()
	cur := f.getSale(f.t, saleID)
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": tenders, "revision": num(f.t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// saleOf makes a one-line sale of qty and completes it with the tenders.
func (f *fixture) saleOf(qty string, tenders ...map[string]any) (saleID string, body resp) {
	f.t.Helper()
	saleID = f.startSale(nil)
	if r := f.addLine(saleID, f.productLine(qty)); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, tenders...)
	if r.status != http.StatusOK {
		f.t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return saleID, r
}

// cashTender is a cash tender of exactly the total of qty at 5.50 plus the
// branch tax on it.
func (f *fixture) cashFor(qty string) map[string]any {
	f.t.Helper()
	var total int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(total * 100)::bigint FROM pos_transactions ORDER BY created_at DESC LIMIT 1`).Scan(&total); err != nil {
		f.t.Fatal(err)
	}
	return tender("cash", total)
}

// openTill opens the fixture register's drawer with a float.
func (f *fixture) openTill(floatCents int64) string {
	f.t.Helper()
	r := f.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": f.register, "opening_float_cents": floatCents},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		f.t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

// ---------------------------------------------------------------------------
// Reading the ledger, the stock and the events.
// ---------------------------------------------------------------------------

type leg struct{ debit, credit int64 }

// legsFor sums, per account code, the legs of every entry whose source
// reference is one of the ids.
func (f *fixture) legsFor(ids ...string) (entries int, legs map[string]leg) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id::text = ANY($1)`, ids).Scan(&entries); err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx, `
		SELECT a.code, ROUND(SUM(l.debit) * 100)::bigint, ROUND(SUM(l.credit) * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id::text = ANY($1) GROUP BY a.code`, ids)
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

// accountBalance is an account's balance over the entries this fixture's
// own documents sourced: its customers' (and the walk-in's) invoices, credit
// memos, payments, applications and refunds, the counter's own acts and this
// register's till sessions, and every reversal of one of those. A package
// running beside this one against the same database never enters the count
// (fourth review P2-5).
func (f *fixture) accountBalance(code string) int64 {
	f.t.Helper()
	walkIn := `(SELECT id FROM customers WHERE account_number = 'WALK-IN')`
	invs := `(SELECT id FROM invoices WHERE customer_id IN ($2, ` + walkIn + `) OR id IN (SELECT invoice_id FROM pos_transactions WHERE invoice_id IS NOT NULL))`
	cms := `(SELECT id FROM credit_memos WHERE customer_id IN ($2, ` + walkIn + `) OR id IN (SELECT credit_memo_id FROM pos_returns WHERE credit_memo_id IS NOT NULL))`
	pays := `(SELECT id FROM payments WHERE customer_id IN ($2, ` + walkIn + `))`
	docs := `((SELECT id::text FROM invoices WHERE id IN ` + invs + `)
		UNION (SELECT id::text FROM credit_memos WHERE id IN ` + cms + `)
		UNION (SELECT id::text FROM payments WHERE id IN ` + pays + `)
		UNION (SELECT id::text FROM ar_applications WHERE invoice_id IN ` + invs + ` OR payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `)
		UNION (SELECT id::text FROM payment_refunds WHERE payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `)
		UNION (SELECT id::text FROM pos_transactions)
		UNION (SELECT id::text FROM pos_returns)
		UNION (SELECT id::text FROM till_sessions WHERE register_id = $3))`
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(ROUND((SUM(l.debit) - SUM(l.credit)) * 100)::bigint, 0)
		FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = $1 AND NOT (e.id::text = ANY($4::text[]))
			AND (e.source_ref_id::text IN `+docs+`
				OR e.reverses_entry_id IN (SELECT o.id FROM gl_journal_entries o WHERE o.source_ref_id::text IN `+docs+`))`,
		code, f.customerID, f.register, f.entryExclusions()).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// The fixture's own document counts (fourth review P2-5): every count a
// test asserts is scoped to what this fixture's acts wrote, so the pos
// suite passes beside the other packages writing the same tables in one
// database (the way CI runs it): the counter's invoices, the memos of its
// customers and its returns, its register's returns, and its customers'
// payment refunds.

func (f *fixture) counterInvoices(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM invoices WHERE id IN (SELECT invoice_id FROM pos_transactions WHERE invoice_id IS NOT NULL)`)
}

func (f *fixture) counterMemos(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id IN ($1,
		(SELECT id FROM customers WHERE account_number = 'WALK-IN'))
		OR id IN (SELECT credit_memo_id FROM pos_returns WHERE credit_memo_id IS NOT NULL)`, f.customerID)
}

func (f *fixture) counterReturns(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM pos_returns WHERE register_id = $1`, f.register)
}

func (f *fixture) counterPayments(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM payments WHERE customer_id IN ($1,
		(SELECT id FROM customers WHERE account_number = 'WALK-IN'))`, f.customerID)
}

func (f *fixture) counterEntries(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries e WHERE e.source_ref_id::text IN
		((SELECT id::text FROM invoices WHERE id IN (SELECT invoice_id FROM pos_transactions WHERE invoice_id IS NOT NULL))
		UNION (SELECT id::text FROM payments WHERE customer_id IN ($1, (SELECT id FROM customers WHERE account_number = 'WALK-IN')))
		UNION (SELECT id::text FROM pos_transactions)
		UNION (SELECT id::text FROM pos_returns)
		UNION (SELECT id::text FROM till_sessions WHERE register_id = $2))`, f.customerID, f.register)
}

func (f *fixture) counterRefunds(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE payment_id IN
		(SELECT id FROM payments WHERE customer_id IN ($1, (SELECT id FROM customers WHERE account_number = 'WALK-IN')))
		OR credit_memo_id IN (SELECT credit_memo_id FROM pos_returns WHERE credit_memo_id IS NOT NULL)`, f.customerID)
}

// entryExclusions lists the prior entry ids, with a sentinel that is never
// an id so the array is never empty.
func (f *fixture) entryExclusions() []string {
	out := make([]string, 0, len(f.priorEntries)+1)
	out = append(out, "00000000-0000-0000-0000-000000000000")
	for id := range f.priorEntries {
		out = append(out, id)
	}
	return out
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

func (f *fixture) scalar(sql string, args ...any) any {
	f.t.Helper()
	var v any
	if err := f.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func countOf(t *testing.T, db *database.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
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
