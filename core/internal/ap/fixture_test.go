// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap_test

// The AP module end to end (C4-1b, ADR 0008 7.4 on ADR 0001): a real
// Postgres, the real service wired as serve wires it (outbox, audit log,
// database as transaction runner, branch guard), the real handler on one mux
// behind the real idempotency middleware and a branch context stand-in.
// Requests are JSON and responses are read back as JSON, so each test states
// a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

const defaultBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

type fixture struct {
	t       *testing.T
	db      *database.DB
	srv     *httptest.Server
	svc     *ap.Service
	repo    *ap.Repository
	glSvc   *gl.Service
	vendor  uuid.UUID
	expense uuid.UUID // account 5020, the line account the fixture's bills use
	branch  uuid.UUID
	prefix  string
}

// service builds a service variant with the given event recorder and audit
// sink, everything else as the fixture wires it.
func (f *fixture) service(events ap.EventRecorder, sink ap.AuditSink, tx ap.TxRunner) *ap.Service {
	svc := ap.NewService(f.db, f.repo, f.glSvc, slog.Default()).WithOutbox(events).WithTxRunner(tx)
	if sink != nil {
		svc = svc.WithAuditLog(sink)
	}
	return svc
}

// newFixture builds a vendor and serves the AP routes the way serve does. A
// request header X-Test-Sub names the acting user (the audit subject and the
// approve's approver) and X-Test-Branch puts a branch wall on the request's
// context, as the branch middleware does.
func newFixture(t *testing.T, db *database.DB) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	f := &fixture{t: t, db: db, vendor: uuid.New(), prefix: "C41B-" + uuid.NewString()[:8] + "-"}
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, `SELECT `+defaultBranch).Scan(&f.branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = '5020'`).Scan(&f.expense); err != nil {
		t.Fatalf("expense account: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO vendors (id, name, contact_email) VALUES ($1, $2, 'c41b@example.invalid')`,
		f.vendor, "C41B Vendor "+f.vendor.String()[:8]); err != nil {
		t.Fatal(err)
	}
	f.repo = ap.NewRepository(db)
	f.glSvc = gl.NewService(gl.NewRepository(db), nil, slog.Default())
	f.svc = ap.NewService(db, f.repo, f.glSvc, slog.Default()).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAuditLog(audit.NewLogger(db)).WithBranchGuard(middleware.NewBranchGuard(db))
	mux := http.NewServeMux()
	ap.NewHandler(f.svc).RegisterRoutes(mux)
	// The GL routes ride the same server (unauthenticated, as the desk's stand
	// in serves them to the fixture's administrator): the void tests reach the
	// ledger through the public reverse and void routes, not hand SQL.
	gl.NewHandler(f.glSvc).RegisterRoutes(mux)
	wallMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := &middleware.UserClaims{Role: "finance", Roles: []string{"finance"}}
		claims.Subject = r.Header.Get("X-Test-Sub")
		if claims.Subject == "" {
			claims.Subject = "11111111-2222-4333-8444-555555555555"
		}
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims.Role, claims.Roles = role, []string{role}
		}
		ctx := context.WithValue(r.Context(), middleware.UserContextKey, claims)
		// What the branch middleware settles for a real request: without a
		// branch header an administrator reads every branch; X-Test-Branch
		// holds the request to one branch (a branch-bound key); X-Test-User-Bound
		// holds it to the branches the user's user_locations rows name.
		bc := &branchctx.Context{UserSub: claims.Subject, IsAdmin: true}
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			bc = &branchctx.Context{UserSub: claims.Subject, IsAdmin: r.Header.Get("X-Test-Admin") == "1", BranchID: &id}
		} else if r.Header.Get("X-Test-User-Bound") == "1" {
			bc = &branchctx.Context{UserSub: claims.Subject, IsAdmin: false}
		}
		mux.ServeHTTP(w, r.WithContext(branchctx.With(ctx, bc)))
	})
	f.srv = httptest.NewServer(middleware.Idempotency(db)(wallMux))
	t.Cleanup(func() {
		f.srv.Close()
		f.cleanup()
	})
	return f
}

// fixtureEntries is every journal entry the fixture's acts wrote for its
// vendor: the entries whose source reference is one of the vendor's bills or
// payments, plus each such entry's whole reversal chain (a reversal of a
// reversal chains two levels down reverses_entry_id, so a one level walk would
// miss it).
const fixtureEntries = `WITH RECURSIVE mine(id) AS (
	SELECT id FROM gl_journal_entries
	WHERE source_ref_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)
	   OR source_ref_id IN (SELECT id FROM ap_payments WHERE vendor_id = $1)
	UNION
	SELECT e.id FROM gl_journal_entries e JOIN mine ON e.reverses_entry_id = mine.id
) SELECT id FROM mine`

// cleanup removes everything the fixture's vendor produced, children first.
func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) { _, _ = f.db.Pool.Exec(ctx, sql, args...) }
	inv := `SELECT id FROM vendor_invoices WHERE vendor_id = $1`
	exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (`+fixtureEntries+`)`, f.vendor)
	exec(`DELETE FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor)
	exec(`DELETE FROM events_outbox WHERE entity_type = 'vendor_invoice' AND entity_id IN `+inv, f.vendor)
	exec(`DELETE FROM audit_log WHERE entity_type = 'vendor_invoice' AND entity_id IN `+inv, f.vendor)
	exec(`DELETE FROM ap_payment_applications WHERE invoice_id IN `+inv, f.vendor)
	exec(`DELETE FROM ap_payments WHERE vendor_id = $1`, f.vendor)
	exec(`DELETE FROM vendor_invoice_lines WHERE invoice_id IN `+inv, f.vendor)
	exec(`DELETE FROM vendor_invoices WHERE vendor_id = $1`, f.vendor)
	exec(`DELETE FROM vendors WHERE id = $1`, f.vendor)
}

func mustExec(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
	// err carries a transport level fault (marshal, request build, do) so a
	// goroutine can return it to its test instead of calling t.Fatal there.
	err error
}

// doSafe is do without t.Fatal: a transport fault is answered in resp.err
// for the caller (a goroutine must never call t.Fatal itself) to fail the
// test with on its own goroutine.
func (f *fixture) doSafe(method, path string, body any, headers ...string) resp {
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			return resp{err: err}
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		return resp{err: err}
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-ap-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return resp{err: err}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	r := f.doSafe(method, path, body, headers...)
	if r.err != nil {
		f.t.Fatal(r.err)
	}
	return r
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
	if meta, _ := r.body["meta"].(map[string]any); meta["request_id"] != "req-ap-test" {
		t.Errorf("meta.request_id = %v, want the request's id", meta["request_id"])
	}
	return code, blockers, fields
}

// ---------------------------------------------------------------------------
// Making bills and payments through the routes.
// ---------------------------------------------------------------------------

// lineBody is one bill line: quantity qty at price (dollars at scale 4 given
// as ten thousandths).
func (f *fixture) lineBody(qty string, price int64) map[string]any {
	return map[string]any{"description": "2x4x8 SPF", "quantity": qty,
		"unit_price_ten_thousandths": price, "gl_account_id": f.expense.String()}
}

// createBody is a bill body of one line of qty at price (dollars at scale 4).
func (f *fixture) createBody(number string, qty string, price int64) map[string]any {
	return map[string]any{
		"vendor_id": f.vendor.String(), "vendor_invoice_number": number,
		"invoice_date": "2026-01-10", "due_date": "2026-02-10",
		"lines": []map[string]any{f.lineBody(qty, price)},
	}
}

// enter makes a pending bill of one line of qty at price (ten thousandths).
func (f *fixture) enter(number string, qty string, price int64) (string, int64) {
	f.t.Helper()
	r := f.do("POST", "/api/v1/ap/invoices", f.createBody(number, qty, price))
	if r.status != http.StatusCreated {
		f.t.Fatalf("enter = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id"), rev(f.t, r)
}

// approve runs the approve transition.
func (f *fixture) approve(id string, revision int64) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions",
		map[string]any{"to": "approved", "revision": revision})
}

// voided runs the void transition.
func (f *fixture) void(id string, revision int64, reason string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions",
		map[string]any{"to": "voided", "revision": revision, "reason": reason})
}

// pay pays cents against the invoices.
func (f *fixture) pay(cents int64, invoiceIDs ...string) resp {
	f.t.Helper()
	return f.payHeaders(nil, cents, invoiceIDs...)
}

// payHeaders pays cents against the invoices behind extra request headers.
func (f *fixture) payHeaders(headers []string, cents int64, invoiceIDs ...string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/ap/payments", map[string]any{
		"vendor_id": f.vendor.String(), "amount_cents": cents, "method": "CHECK",
		"payment_date": "2026-01-15", "invoice_ids": invoiceIDs,
	}, headers...)
}

// ---------------------------------------------------------------------------
// Reaching the ledger through the public GL routes.
// ---------------------------------------------------------------------------

// glReverse runs the GL reverse route on an entry. The route refuses
// non-MANUAL entries (an entry a subledger document owns is undone through
// its document), and these chains need a bill's entry reversed anyway, so
// the refusal falls back to writing the same reversal by hand. That fallback
// calls t.Fatal on failure, so glReverse is for sequential main goroutine
// setup only: never call it from a child goroutine, whose Fatal would exit
// the goroutine without unwinding the test (call the HTTP route directly
// there instead). glVoidEntry's fallback holds the same rule.
func (f *fixture) glReverse(entry string) resp {
	f.t.Helper()
	r := f.do("POST", "/api/v1/gl/journal-entries/"+entry+"/reverse", map[string]any{"reason": "fixture reversal"})
	if r.status == http.StatusBadRequest {
		var reversal string
		if err := f.db.Pool.QueryRow(context.Background(), `
			WITH orig AS (
				SELECT id, entry_number, currency, branch_id
				FROM gl_journal_entries WHERE id = $1 AND status = 'POSTED'
			), ins AS (
				INSERT INTO gl_journal_entries (entry_date, memo, source, source_ref_id, branch_id, status, posted_by, currency, reverses_entry_id)
				SELECT CURRENT_DATE, 'Reversal of #' || entry_number || ': fixture reversal', 'REVERSAL', orig.id, orig.branch_id, 'POSTED', 't', orig.currency, orig.id
				FROM orig
				RETURNING id
			)
			INSERT INTO gl_journal_lines (journal_entry_id, account_id, description, debit, credit, branch_id)
			SELECT ins.id, l.account_id, 'Reversal: ' || l.description, l.credit, l.debit, l.branch_id
			FROM gl_journal_lines l, ins
			WHERE l.journal_entry_id = $1
			RETURNING (SELECT id FROM ins)`, entry).Scan(&reversal); err != nil {
			f.t.Fatal(err)
		}
		r.status = http.StatusOK
	}
	return r
}

// glVoidEntry marks an entry VOID to exercise AP's legacy voided-chain guards
// now that the GL void route refuses non-MANUAL and already-reversed entries.
func (f *fixture) glVoidEntry(entry string) resp {
	f.t.Helper()
	r := f.do("POST", "/api/v1/gl/journal-entries/"+entry+"/void", nil)
	if r.status == http.StatusBadRequest {
		if _, err := f.db.Pool.Exec(context.Background(),
			`UPDATE gl_journal_entries SET status = 'VOID', updated_at = NOW() WHERE id = $1 AND status = 'POSTED'`, entry); err != nil {
			f.t.Fatal(err)
		}
		r.status = http.StatusOK
	}
	return r
}

// glEntryOf is the bill's linked entry id.
func (f *fixture) glEntryOf(invoiceID string) string {
	f.t.Helper()
	var entry string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT gl_entry_id::text FROM vendor_invoices WHERE id = $1`, invoiceID).Scan(&entry); err != nil {
		f.t.Fatal(err)
	}
	return entry
}

// glReversalOf is the id of the one reversal pointing at an entry.
func (f *fixture) glReversalOf(entry string) string {
	f.t.Helper()
	var rev string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM gl_journal_entries WHERE reverses_entry_id = $1`, entry).Scan(&rev); err != nil {
		f.t.Fatal(err)
	}
	return rev
}

// number is the bill's own number (AP-).
func (f *fixture) number(invoiceID string) string {
	f.t.Helper()
	var n string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT number FROM vendor_invoices WHERE id = $1`, invoiceID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Reading the ledger, the events and the control account.
// ---------------------------------------------------------------------------

type leg struct{ debit, credit int64 }

// entryLegs sums, per account code, the legs of the entries whose source
// reference is the bill.
func (f *fixture) entryLegs(invoiceID string) (entries int, legs map[string]leg) {
	f.t.Helper()
	return f.entryLegsWhere(f.t, f.db, `e.source_ref_id = $1`, invoiceID)
}

func (f *fixture) entryLegsWhere(t *testing.T, db *database.DB, where string, args ...any) (int, map[string]leg) {
	t.Helper()
	ctx := context.Background()
	var entries int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries e WHERE `+where, args...).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT a.code, ROUND(SUM(l.debit) * 100)::bigint, ROUND(SUM(l.credit) * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.id IN (SELECT id FROM gl_journal_entries e WHERE `+where+`) GROUP BY a.code`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	legs := map[string]leg{}
	for rows.Next() {
		var code string
		var l leg
		if err := rows.Scan(&code, &l.debit, &l.credit); err != nil {
			t.Fatal(err)
		}
		legs[code] = l
	}
	return entries, legs
}

// controlBalance is the AP control account's balance in cents, from the
// POSTED entries this fixture's acts wrote (approves credit it, payments and
// reversals debit it; the trial balance sums POSTED entries alone, so an
// entry the GL void route voided counts nowhere), scoped to the fixture's
// own bills and payments.
func (f *fixture) controlBalance() int64 {
	f.t.Helper()
	return f.controlBalanceIn("")
}

// controlBalanceIn is controlBalance for one currency (every currency when
// the code is empty): the tie the trial balance states per currency, never
// across two.
func (f *fixture) controlBalanceIn(currency string) int64 {
	f.t.Helper()
	var balance int64
	err := f.db.Pool.QueryRow(context.Background(), `
		SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint
		FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = '2010' AND e.status = 'POSTED' AND ($2 = '' OR e.currency = $2) AND e.id IN (
		    `+fixtureEntries+`)`, f.vendor, currency).Scan(&balance)
	if err != nil {
		f.t.Fatal(err)
	}
	return balance
}

// openSum is what the vendor's open bills owe, in cents.
func (f *fixture) openSum() int64 {
	f.t.Helper()
	return f.openSumIn("")
}

// openSumIn is openSum for one currency (every currency when the code is
// empty).
func (f *fixture) openSumIn(currency string) int64 {
	f.t.Helper()
	var open int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(COALESCE(SUM(amount_open), 0) * 100)::bigint FROM vendor_invoices
		WHERE vendor_id = $1 AND status IN ('APPROVED', 'PARTIAL') AND ($2 = '' OR currency = $2)`,
		f.vendor, currency).Scan(&open); err != nil {
		f.t.Fatal(err)
	}
	return open
}

// balances answers whether every journal entry this fixture's acts wrote is
// balanced.
func (f *fixture) balanced() bool {
	f.t.Helper()
	var bad int64
	err := f.db.Pool.QueryRow(context.Background(), `
		SELECT count(*) FROM gl_journal_entries e
		JOIN (SELECT journal_entry_id, SUM(debit) d, SUM(credit) c FROM gl_journal_lines GROUP BY journal_entry_id) s
		  ON s.journal_entry_id = e.id
		WHERE e.id IN (`+fixtureEntries+`)
		  AND s.d <> s.c`, f.vendor).Scan(&bad)
	if err != nil {
		f.t.Fatal(err)
	}
	return bad == 0
}

// eventTypes lists the vendor invoice's event types in order.
func eventTypes(t *testing.T, db *database.DB, entityID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'vendor_invoice' AND entity_id = $1 ORDER BY position`, entityID)
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

func (f *fixture) count(sql string, args ...any) int64 {
	f.t.Helper()
	return countOf(f.t, f.db, sql, args...)
}
