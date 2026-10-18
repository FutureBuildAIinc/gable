// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// The ledger's acts against a real database: each rides its audit row and its
// outbox event in its own transaction, the by-id acts and the company wide
// acts are walled to the caller's reach, and the contended acts finish at
// pool size 4 with three contenders and exactly one winner.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

func wiredService(db *database.DB) *gl.Service {
	return gl.NewService(gl.NewRepository(db), nil, slog.Default()).
		WithAuditTrail(db, audit.NewLogger(db), outbox.NewWriter(db, ""))
}

func countOf(t *testing.T, db *database.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newPeriod(t *testing.T, db *database.DB, name, from, to string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO gl_fiscal_periods (name, start_date, end_date) VALUES ($1, $2, $3) RETURNING id`, name, from, to).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, id)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, id)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_fiscal_periods WHERE id = $1`, id)
	})
	return id
}

// TestPeriodActsWriteAuditRowAndEvent: closing and reopening a period each
// leave one audit row and one outbox event, the reopen carrying its reason.
func TestPeriodActsWriteAuditRowAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wiredService(db)
	id := newPeriod(t, db, "m6 acts "+uuid.NewString()[:8], "2036-01-01", "2036-01-31")

	closed, err := svc.CloseFiscalPeriodFor(ctx, id, 1, "auditor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReopenFiscalPeriodFor(ctx, id, closed.Revision, "late bill", "auditor"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"gl.period_closed", "gl.period_reopened"} {
		if n := countOf(t, db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = $2`, id, action); n != 1 {
			t.Errorf("%d audit rows for %s, want 1", n, action)
		}
		if n := countOf(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = $2`, id, action); n != 1 {
			t.Errorf("%d outbox events for %s, want 1", n, action)
		}
	}
}

// TestAccountAndEntryActsWriteAuditRowAndEvent: a hand made entry, its post
// and its reversal, and an account's create and update, each leave their row.
func TestAccountAndEntryActsWriteAuditRowAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wiredService(db)
	branch := defaultBranchOf(t, db)
	memo := "m6 entry acts " + uuid.NewString()
	t.Cleanup(func() {
		deleteEntriesByMemo(t, db, "Reversal of #%")
		deleteEntriesByMemo(t, db, memo)
	})

	var ar, rev uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, gl.AccountCodeAR).Scan(&ar); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, gl.AccountCodeRevenue).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	entry := &gl.JournalEntry{EntryDate: time.Date(2036, 2, 1, 0, 0, 0, 0, time.UTC), Memo: memo, Source: gl.SourceManual,
		BranchID: branch, PostedBy: "t", Status: gl.StatusDraft, Lines: []gl.JournalLine{
			{AccountID: ar, Debit: 500}, {AccountID: rev, Credit: 500}}}
	if err := svc.CreateManualJournalEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, entry.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, entry.ID)
	})
	if err := svc.PostJournalEntry(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	reversal, err := svc.ReverseJournalEntry(ctx, entry.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, reversal)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, reversal)
	})
	for _, c := range []struct {
		id     uuid.UUID
		action string
	}{
		{entry.ID, "gl.journal_entry_created"},
		{entry.ID, "gl.journal_entry_posted"},
		{reversal, "gl.journal_entry_reversed"},
	} {
		if n := countOf(t, db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = $2`, c.id, c.action); n != 1 {
			t.Errorf("%d audit rows for %s, want 1", n, c.action)
		}
		if n := countOf(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = $2`, c.id, c.action); n != 1 {
			t.Errorf("%d outbox events for %s, want 1", n, c.action)
		}
	}

	acct := &gl.GLAccount{Code: "9" + uuid.NewString()[:5], Name: "m6 acts", Type: gl.AccountTypeAsset}
	if err := svc.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, acct.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, acct.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_accounts WHERE id = $1`, acct.ID)
	})
	acct.Name = "m6 acts renamed"
	acct.Revision = 1
	if err := svc.UpdateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"gl.account_created", "gl.account_updated"} {
		if n := countOf(t, db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = $2`, acct.ID, action); n != 1 {
			t.Errorf("%d audit rows for %s, want 1", n, action)
		}
		if n := countOf(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = $2`, acct.ID, action); n != 1 {
			t.Errorf("%d outbox events for %s, want 1", n, action)
		}
	}
}

// TestByIdAndCompanyWideActsAreWalled: a caller held to one branch finds
// another branch's entry missing for the get, post, void and reverse, is
// refused the period close, reopen and year end close (they span every
// branch), and a caller whose header names one branch is refused another
// branch's statement.
func TestByIdAndCompanyWideActsAreWalled(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default()).WithAuditTrail(db, nil, nil)
	a := defaultBranchOf(t, db)
	b := secondBranch(t, db, "M6-WALL")
	h := gl.NewHandler(svc).WithBranchGuard(middleware.NewBranchGuard(db))

	sub := "m6-wall-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 't')`, sub, b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub) })

	memo := "m6 wall " + uuid.NewString()
	t.Cleanup(func() { deleteEntriesByMemo(t, db, memo) })
	post(t, db, svc, a, time.Date(2036, 3, 1, 0, 0, 0, 0, time.UTC), memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 100}, gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 100})
	post(t, db, svc, b, time.Date(2036, 3, 1, 0, 0, 0, 0, time.UTC), memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 70}, gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 70})
	var entryID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_journal_entries WHERE memo = $1 AND branch_id = $2`, memo, a).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	periodID := newPeriod(t, db, "m6 wall "+uuid.NewString()[:8], "2036-04-01", "2036-04-30")

	serve := func(bc *branchctx.Context, method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("If-Match", `"1"`)
		r = r.WithContext(branchctx.With(context.Background(), bc))
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
		return w
	}
	one := &branchctx.Context{UserSub: sub}
	for _, c := range []struct {
		method, target, body string
		want                 int
	}{
		{"GET", "/api/v1/gl/journal-entries/" + entryID.String(), "", http.StatusNotFound},
		{"POST", "/api/v1/gl/journal-entries/" + entryID.String() + "/post", "", http.StatusNotFound},
		{"POST", "/api/v1/gl/journal-entries/" + entryID.String() + "/void", "", http.StatusNotFound},
		{"POST", "/api/v1/gl/journal-entries/" + entryID.String() + "/reverse", `{}`, http.StatusNotFound},
		{"POST", "/api/v1/gl/fiscal-periods/" + periodID.String() + "/close", "", http.StatusForbidden},
		{"POST", "/api/v1/gl/fiscal-periods/" + periodID.String() + "/reopen", `{"reason":"x"}`, http.StatusForbidden},
		{"POST", "/api/v1/gl/years/2036/close", "", http.StatusForbidden},
	} {
		if w := serve(one, c.method, c.target, c.body); w.Code != c.want {
			t.Errorf("%s %s as a one branch caller = %d, want %d: %s", c.method, c.target, w.Code, c.want, w.Body)
		}
	}
	// A header naming branch b reads b, never a.
	header := &branchctx.Context{UserSub: sub, BranchID: &b}
	if w := serve(header, "GET", "/api/v1/gl/trial-balance?branch_id="+a.String(), ""); w.Code != http.StatusForbidden {
		t.Errorf("header branch reading another branch = %d, want 403: %s", w.Code, w.Body)
	}
	if w := serve(header, "GET", "/api/v1/gl/trial-balance?branch_id="+b.String(), ""); w.Code != http.StatusOK {
		t.Errorf("header branch reading its own = %d, want 200: %s", w.Code, w.Body)
	}
	// A header naming branch b with no branch_id reads b (the platform's scope
	// rule), not a refusal and not the company.
	if w := serve(header, "GET", "/api/v1/gl/trial-balance?as_of=2037-01-01", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), b.String()) || strings.Contains(w.Body.String(), a.String()) {
		t.Errorf("header branch without branch_id = %d, want branch b's own rows: %s", w.Code, w.Body)
	}
	// An administrator has every branch's reach: the consolidated read, any
	// named branch and the company wide acts.
	admin := &branchctx.Context{UserSub: sub, IsAdmin: true}
	if w := serve(admin, "GET", "/api/v1/gl/trial-balance?branch_id="+a.String(), ""); w.Code != http.StatusOK {
		t.Errorf("administrator reading a named branch = %d, want 200: %s", w.Code, w.Body)
	}
	if w := serve(admin, "GET", "/api/v1/gl/trial-balance", ""); w.Code != http.StatusOK {
		t.Errorf("administrator consolidated read = %d, want 200: %s", w.Code, w.Body)
	}
	if w := serve(admin, "POST", "/api/v1/gl/fiscal-periods/"+periodID.String()+"/close", ""); w.Code != http.StatusOK {
		t.Errorf("administrator closing a period = %d, want 200: %s", w.Code, w.Body)
	}
	// An unknown entry is a 404, not a 400.
	if w := serve(&branchctx.Context{}, "POST", "/api/v1/gl/journal-entries/"+uuid.NewString()+"/void", ""); w.Code != http.StatusNotFound {
		t.Errorf("void of an unknown entry = %d, want 404: %s", w.Code, w.Body)
	}
}

// TestContendedActsAtPool4: three contenders at pool size 4 close the same
// period on the same revision (one wins, the others are told it is stale) and
// close the same year (one closing entry per branch, however many close it).
func TestContendedActsAtPool4(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	svc := wiredService(db)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	id := newPeriod(t, db, "m6 pool "+uuid.NewString()[:8], "2037-01-01", "2037-01-31")
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CloseFiscalPeriodFor(ctx, id, 1, "t")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, gl.ErrStaleRevision):
			stale++
		default:
			t.Errorf("a period closer failed with %v", err)
		}
	}
	if won != 1 || stale != 2 {
		t.Errorf("%d winners and %d stale, want 1 and 2", won, stale)
	}
	if n := countOf(t, db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'gl.period_closed'`, id); n != 1 {
		t.Errorf("%d close audit rows, want 1", n)
	}

	year := 2038
	branch := secondBranch(t, db, "M6-POOL")
	memo := "m6 pool year"
	closeMemo := fmt.Sprintf("Year end close %d", year)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE memo = $1)`, closeMemo)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE memo = $1)`, closeMemo)
		deleteEntriesByMemo(t, db, memo)
		deleteEntriesByMemo(t, db, closeMemo)
	})
	post(t, db, svc, branch, time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC), memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 700}, gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 700})
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CloseYear(ctx, year, "USD", "t")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a year closer failed with %v", err)
		}
	}
	if n := countOf(t, db, `SELECT count(*) FROM gl_journal_entries WHERE source = 'CLOSING' AND branch_id = $1 AND entry_date = make_date($2, 12, 31)`, branch, year); n != 1 {
		t.Errorf("%d closing entries for the branch, want 1", n)
	}
	if ctx.Err() != nil {
		t.Fatal("the contenders at pool size 4 did not finish")
	}
}

// TestCrossBranchSubledgerTieOutAndSettlements verifies per-branch AR/AP
// tie-outs when a customer's documents sit outside primary_branch_id, cross-
// branch payment and credit-memo applications (and their reversals), and
// multi-branch AP vendor payments.
func TestCrossBranchSubledgerTieOutAndSettlements(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	arSvc := account.NewService(db, glSvc, slog.Default())
	apSvc := ap.NewService(db, ap.NewRepository(db), glSvc, slog.Default())

	branchA := secondBranch(t, db, "M6-XBA")
	branchB := secondBranch(t, db, "M6-XBB")

	cust := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due, currency) VALUES ($1, 'm6 xbranch cust', $2, $3, 0, 'USD')`,
		cust, "M6XB-"+cust.String()[:6], branchA); err != nil {
		t.Fatal(err)
	}
	vendor := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'm6 xbranch vendor')`, vendor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM ap_payment_applications WHERE payment_id IN (SELECT id FROM ap_payments WHERE vendor_id = $1)`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM ap_payments WHERE vendor_id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE vendor_id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendor_invoice_lines WHERE invoice_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendor_invoices WHERE vendor_id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)

		_, _ = db.Pool.Exec(ctx, `UPDATE ar_applications SET gl_entry_id = NULL, reversal_gl_entry_id = NULL WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `UPDATE payments SET gl_entry_id = NULL WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `UPDATE credit_memos SET gl_entry_id = NULL WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `UPDATE invoices SET gl_entry_id = NULL WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM ar_applications WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_transactions WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payments WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM credit_memos WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)

		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE branch_id IN ($1, $2) OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id IN ($1, $2))`, branchA, branchB)
		_, _ = db.Pool.Exec(ctx, `UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE branch_id IN ($1, $2)`, branchA, branchB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE branch_id IN ($1, $2)`, branchA, branchB)
	})

	today, err := glSvc.BusinessDay(ctx, &branchA)
	if err != nil {
		t.Fatal(err)
	}

	acctBalance := func(branch uuid.UUID, code string) int64 {
		t.Helper()
		var cents int64
		if err := db.Pool.QueryRow(ctx, `
			SELECT COALESCE(ROUND(SUM(l.debit - l.credit) * 100), 0)::bigint
			FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			JOIN gl_accounts a ON a.id = l.account_id
			WHERE e.status = 'POSTED' AND l.branch_id = $1 AND a.code = $2`, branch, code).Scan(&cents); err != nil {
			t.Fatal(err)
		}
		return cents
	}

	// 1. Customer with primary_branch_id = branchA has an open invoice on branchB.
	invB := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, number, status, subtotal, tax_amount, total_amount, amount_open, currency, invoice_date)
		VALUES ($1, $2, $3, $4, 'UNPAID', 100.00, 0, 100.00, 100.00, 'USD', $5::date)`,
		invB, cust, branchB, "M6-INV-"+invB.String()[:6], today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostInvoice(ctx, account.PostInvoiceIn{
			InvoiceID: invB, CustomerID: cust, Number: "M6-INV-" + invB.String()[:6], Currency: "USD",
			TotalCents: 10000, On: today, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Credit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	tieA, err := glSvc.TieOut(ctx, today, "USD", &branchA)
	if err != nil {
		t.Fatal(err)
	}
	tieB, err := glSvc.TieOut(ctx, today, "USD", &branchB)
	if err != nil {
		t.Fatal(err)
	}
	if tieA.AR.ControlCents != 0 || tieA.AR.SubledgerCents != 0 || tieA.AR.DifferenceCents != 0 {
		t.Errorf("branchA AR tie-out with customer invoice on branchB = %+v, want all 0", tieA.AR)
	}
	if tieB.AR.ControlCents != 10000 || tieB.AR.SubledgerCents != 10000 || tieB.AR.DifferenceCents != 0 {
		t.Errorf("branchB AR tie-out with customer invoice on branchB = %+v, want 10000/10000/0", tieB.AR)
	}

	// 2. Payment recorded on branchA applied to invoice on branchB, then reversed.
	var payAppID uuid.UUID
	var payID uuid.UUID
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		var fx *account.Effects
		var err error
		payID, fx, err = arSvc.RecordPayment(ctx, account.RecordPaymentIn{
			CustomerID: cust, BranchID: branchA, Currency: "USD", Method: "CASH",
			AmountCents: 10000, ReceivedOn: today, Actor: "t",
			Applications: []account.ApplyLine{{InvoiceID: invB, AmountCents: 10000}},
		})
		if err != nil {
			return err
		}
		payAppID = fx.ApplicationIDs[0]
		return nil
	}); err != nil {
		t.Fatalf("RecordPayment cross-branch: %v", err)
	}

	for _, b := range []uuid.UUID{branchA, branchB} {
		if got := acctBalance(b, gl.AccountCodeCustomerDeposit); got != 0 {
			t.Errorf("branch %s account 2200 after cross-branch payment apply = %d, want 0", b, got)
		}
		if got := acctBalance(b, gl.AccountCodeAR); got != 0 {
			t.Errorf("branch %s account 1020 after cross-branch payment apply = %d, want 0", b, got)
		}
		tie, err := glSvc.TieOut(ctx, today, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AR.ControlCents != 0 || tie.AR.SubledgerCents != 0 || tie.AR.DifferenceCents != 0 {
			t.Errorf("branch %s AR tie-out after cross-branch payment apply = %+v, want 0/0/0", b, tie.AR)
		}
	}

	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := arSvc.Reverse(ctx, account.ReverseIn{ApplicationID: payAppID, Reason: "undo", Actor: "t", On: today}); err != nil {
			return err
		}
		_, err := arSvc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: payID, Reason: "void", Actor: "t", On: today})
		return err
	}); err != nil {
		t.Fatalf("Reverse + VoidPayment cross-branch: %v", err)
	}
	if got := acctBalance(branchA, gl.AccountCodeCustomerDeposit); got != 0 {
		t.Errorf("branchA 2200 after Reverse+VoidPayment = %d, want 0", got)
	}
	if got := acctBalance(branchB, gl.AccountCodeAR); got != 10000 {
		t.Errorf("branchB 1020 after Reverse+VoidPayment = %d, want 10000", got)
	}

	// 3. Credit memo on branchA applied to invoice on branchB, then reversed.
	cmA := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO credit_memos (id, customer_id, branch_id, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency)
		VALUES ($1, $2, $3, 100.00, -100.00, 0, -100.00, 0, $4::date, 'DRAFT', 'm6', 'OTHER', 'USD')`,
		cmA, cust, branchA, today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostCreditMemo(ctx, account.PostCreditMemoIn{
			MemoID: cmA, CustomerID: cust, Number: "CM-" + cmA.String()[:6], Currency: "USD",
			MemoDate: today, SubtotalCents: -10000, TaxCents: 0, TotalCents: -10000, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Debit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostCreditMemo: %v", err)
	}

	var cmAppID uuid.UUID
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		fx, err := arSvc.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{
			MemoID: cmA, Lines: []account.ApplyLine{{InvoiceID: invB, AmountCents: 10000}},
			On: today, Actor: "t",
		})
		if err != nil {
			return err
		}
		cmAppID = fx.ApplicationIDs[0]
		return nil
	}); err != nil {
		t.Fatalf("ApplyCreditMemo cross-branch: %v", err)
	}

	for _, b := range []uuid.UUID{branchA, branchB} {
		if got := acctBalance(b, gl.AccountCodeAR); got != 0 {
			t.Errorf("branch %s account 1020 after cross-branch credit-memo apply = %d, want 0", b, got)
		}
		tie, err := glSvc.TieOut(ctx, today, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AR.ControlCents != 0 || tie.AR.SubledgerCents != 0 || tie.AR.DifferenceCents != 0 {
			t.Errorf("branch %s AR tie-out after cross-branch credit-memo apply = %+v, want 0/0/0", b, tie.AR)
		}
	}

	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.Reverse(ctx, account.ReverseIn{ApplicationID: cmAppID, Reason: "undo cm", Actor: "t", On: today})
		return err
	}); err != nil {
		t.Fatalf("Reverse cross-branch credit memo: %v", err)
	}
	if got := acctBalance(branchA, gl.AccountCodeAR); got != -10000 {
		t.Errorf("branchA 1020 after reversing cross-branch credit memo = %d, want -10000", got)
	}
	if got := acctBalance(branchB, gl.AccountCodeAR); got != 10000 {
		t.Errorf("branchB 1020 after reversing cross-branch credit memo = %d, want 10000", got)
	}

	// 4. Multi-branch AP vendor payment across branchA and branchB.
	var expID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = '5020'`).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	qty1, _ := httpx.ParseQuantity("1")
	createAndApproveBill := func(branch uuid.UUID, num string, cents httpx.Cents) uuid.UUID {
		t.Helper()
		b := branch
		inv, err := apSvc.Create(ctx, &ap.Input{
			VendorID: vendor, BranchID: &b, VendorInvoiceNumber: num,
			InvoiceDate: today.Format("2006-01-02"), DueDate: today.Format("2006-01-02"), Currency: "USD",
			Lines: []ap.LineInput{{Description: "bill", Quantity: qty1, UnitPrice: httpx.Price(int64(cents) * 100), GLAccountID: &expID, LineTotalCents: cents}},
		}, ap.Caller{Actor: "t"})
		if err != nil {
			t.Fatalf("apSvc.Create: %v", err)
		}
		if _, err := apSvc.ApproveInvoice(ctx, inv.ID, uuid.New()); err != nil {
			t.Fatalf("apSvc.ApproveInvoice: %v", err)
		}
		return inv.ID
	}
	billA := createAndApproveBill(branchA, "VI-A-"+uuid.NewString()[:6], 15000)
	billB := createAndApproveBill(branchB, "VI-B-"+uuid.NewString()[:6], 25000)

	if _, err := apSvc.PayVendor(ctx, &ap.PayInput{
		VendorID: vendor, AmountCents: 40000, Method: ap.PaymentMethodCheck,
		PaymentDate: today.Format("2006-01-02"), InvoiceIDs: []uuid.UUID{billA, billB},
	}, ap.Caller{Actor: "t"}); err != nil {
		t.Fatalf("apSvc.PayVendor multi-branch: %v", err)
	}

	for _, b := range []uuid.UUID{branchA, branchB} {
		if got := acctBalance(b, gl.AccountCodeAP); got != 0 {
			t.Errorf("branch %s account 2010 after multi-branch PayVendor = %d, want 0", b, got)
		}
		tie, err := glSvc.TieOut(ctx, today, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AP.ControlCents != 0 || tie.AP.SubledgerCents != 0 || tie.AP.DifferenceCents != 0 {
			t.Errorf("branch %s AP tie-out after multi-branch PayVendor = %+v, want 0/0/0", b, tie.AP)
		}
	}
}

// TestPeriodAndAccountGuardsOnJournalAndAccountActs verifies period-close,
// reversal, source, and actor guards on VoidJournalEntry, PostJournalEntry,
// and ReverseJournalEntry, plus everyBranch and input/balance guards on
// HandleCreateAccount and HandleUpdateAccount.
func TestPeriodAndAccountGuardsOnJournalAndAccountActs(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wiredService(db)
	branch := secondBranch(t, db, "M6-GRD")
	h := gl.NewHandler(svc).WithBranchGuard(middleware.NewBranchGuard(db))

	var ar, rev uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, gl.AccountCodeAR).Scan(&ar); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, gl.AccountCodeRevenue).Scan(&rev); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE branch_id = $1 OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE branch_id = $1`, branch)
	})

	// 1. PostJournalEntry preserves actor and VoidJournalEntry refuses non-MANUAL,
	// already-reversed, and closed-period entries.
	closedPeriodID := newPeriod(t, db, "m6 grd closed "+uuid.NewString()[:8], "2039-01-01", "2039-01-31")
	openEntry := &gl.JournalEntry{
		EntryDate: time.Date(2039, 1, 15, 0, 0, 0, 0, time.UTC),
		Memo:      "m6 grd manual",
		Source:    gl.SourceManual,
		BranchID:  branch,
		PostedBy:  "creator-actor",
		Status:    gl.StatusDraft,
		Lines: []gl.JournalLine{
			{AccountID: ar, Debit: 1000},
			{AccountID: rev, Credit: 1000},
		},
	}
	if err := svc.CreateManualJournalEntry(ctx, openEntry); err != nil {
		t.Fatal(err)
	}
	postCtx := actor.WithKeyID(ctx, "poster-actor")
	if err := svc.PostJournalEntry(postCtx, openEntry.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.GetJournalEntry(ctx, openEntry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PostedBy != "poster-actor" {
		t.Errorf("PostJournalEntry posted_by = %q, want %q", loaded.PostedBy, "poster-actor")
	}

	if _, err := svc.CloseFiscalPeriodFor(ctx, closedPeriodID, 1, "auditor"); err != nil {
		t.Fatal(err)
	}
	if err := svc.VoidJournalEntry(ctx, openEntry.ID); err == nil {
		t.Errorf("VoidJournalEntry in CLOSED period = nil, want error")
	}

	// Non-MANUAL entry cannot be voided via VoidJournalEntry.
	var invEntry *gl.JournalEntry
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		var err error
		invEntry, err = svc.PostEntry(ctx, gl.PostingInput{
			EntryDate: time.Date(2039, 2, 10, 0, 0, 0, 0, time.UTC),
			Memo:      "m6 grd inv",
			Source:    gl.SourceInvoice,
			BranchID:  branch,
			PostedBy:  "t",
			Legs: []gl.Leg{
				{AccountCode: gl.AccountCodeAR, Debit: 2000},
				{AccountCode: gl.AccountCodeRevenue, Credit: 2000},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.VoidJournalEntry(ctx, invEntry.ID); err == nil {
		t.Errorf("VoidJournalEntry on non-MANUAL entry = nil, want error")
	}

	// Already-reversed entry (and the reversal entry itself) cannot be voided.
	revTarget := &gl.JournalEntry{
		EntryDate: time.Date(2039, 2, 12, 0, 0, 0, 0, time.UTC),
		Memo:      "m6 grd rev target",
		Source:    gl.SourceManual,
		BranchID:  branch,
		PostedBy:  "creator-actor",
		Status:    gl.StatusPosted,
		Lines: []gl.JournalLine{
			{AccountID: ar, Debit: 3000},
			{AccountID: rev, Credit: 3000},
		},
	}
	if err := svc.CreateManualJournalEntry(ctx, revTarget); err != nil {
		t.Fatal(err)
	}
	revCtx := actor.WithKeyID(ctx, "reverser-actor")
	revID, err := svc.ReverseJournalEntry(revCtx, revTarget.ID, "mistake")
	if err != nil {
		t.Fatalf("ReverseJournalEntry: %v", err)
	}
	revLoaded, err := svc.GetJournalEntry(ctx, revID)
	if err != nil {
		t.Fatal(err)
	}
	if revLoaded.PostedBy != "reverser-actor" {
		t.Errorf("ReverseJournalEntry posted_by = %q, want %q", revLoaded.PostedBy, "reverser-actor")
	}
	if err := svc.VoidJournalEntry(ctx, revTarget.ID); err == nil {
		t.Errorf("VoidJournalEntry on already-reversed entry = nil, want error")
	}
	if err := svc.VoidJournalEntry(ctx, revID); err == nil {
		t.Errorf("VoidJournalEntry on reversal entry = nil, want error")
	}

	// 2. ReverseJournalEntry refused when branch business day falls in a CLOSED period.
	today, err := svc.BusinessDay(ctx, &branch)
	if err != nil {
		t.Fatal(err)
	}
	var todayPeriodID uuid.UUID
	var todayPeriodRev int64
	err = db.Pool.QueryRow(ctx, `SELECT id, revision FROM gl_fiscal_periods WHERE $1::date BETWEEN start_date AND end_date LIMIT 1`, today.Format("2006-01-02")).Scan(&todayPeriodID, &todayPeriodRev)
	if err != nil {
		todayStr := today.Format("2006-01-02")
		todayPeriodID = newPeriod(t, db, "m6 grd today "+uuid.NewString()[:8], todayStr, todayStr)
		todayPeriodRev = 1
	}
	closedToday, err := svc.CloseFiscalPeriodFor(ctx, todayPeriodID, todayPeriodRev, "auditor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE gl_fiscal_periods SET status = 'OPEN', closed_at = NULL, closed_by = NULL WHERE id = $1`, todayPeriodID)
	})
	unreversedEntry := &gl.JournalEntry{
		EntryDate: time.Date(2039, 2, 14, 0, 0, 0, 0, time.UTC),
		Memo:      "m6 grd unreversed",
		Source:    gl.SourceManual,
		BranchID:  branch,
		PostedBy:  "creator-actor",
		Status:    gl.StatusPosted,
		Lines: []gl.JournalLine{
			{AccountID: ar, Debit: 4000},
			{AccountID: rev, Credit: 4000},
		},
	}
	if err := svc.CreateManualJournalEntry(ctx, unreversedEntry); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReverseJournalEntry(ctx, unreversedEntry.ID, "closed today"); err == nil {
		t.Errorf("ReverseJournalEntry when business day period is CLOSED = nil, want error")
	}
	if _, err := svc.ReopenFiscalPeriodFor(ctx, todayPeriodID, closedToday.Revision, "reopen today", "auditor"); err != nil {
		t.Fatal(err)
	}

	// Voiding unreversedEntry in an open period succeeds and preserves PostedBy.
	if err := svc.VoidJournalEntry(ctx, unreversedEntry.ID); err != nil {
		t.Fatalf("VoidJournalEntry in open period: %v", err)
	}
	voidedLoaded, err := svc.GetJournalEntry(ctx, unreversedEntry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if voidedLoaded.PostedBy != "creator-actor" {
		t.Errorf("VoidJournalEntry posted_by = %q, want %q", voidedLoaded.PostedBy, "creator-actor")
	}

	// 3. HandleCreateAccount and HandleUpdateAccount enforce everyBranch and validate/preserve fields.
	serve := func(bc *branchctx.Context, method, target, body, ifMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if ifMatch != "" {
			r.Header.Set("If-Match", ifMatch)
		}
		r = r.WithContext(branchctx.With(context.Background(), bc))
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
		return w
	}

	sub := "m6-grd-" + uuid.NewString()[:8]
	oneBranchCaller := &branchctx.Context{UserSub: sub, BranchID: &branch}
	if w := serve(oneBranchCaller, "POST", "/api/v1/gl/accounts", `{"code":"9881","name":"Branch Acct","type":"ASSET"}`, ""); w.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/gl/accounts as branch-confined caller = %d, want 403: %s", w.Code, w.Body)
	}

	adminCaller := &branchctx.Context{UserSub: sub, IsAdmin: true}
	acctCode := "9" + strings.ReplaceAll(uuid.NewString()[:5], "-", "0")
	wCreate := serve(adminCaller, "POST", "/api/v1/gl/accounts",
		fmt.Sprintf(`{"code":%q,"name":"Grd Acct","type":"ASSET"}`, acctCode), "")
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/gl/accounts as admin = %d, want 201: %s", wCreate.Code, wCreate.Body)
	}
	var created gl.GLAccount
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1 OR entity_id IN (SELECT id FROM gl_accounts WHERE code = '9881')`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1 OR entity_id IN (SELECT id FROM gl_accounts WHERE code = '9881')`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_accounts WHERE id = $1 OR code = '9881'`, created.ID)
	})

	if w := serve(oneBranchCaller, "PUT", "/api/v1/gl/accounts/"+created.ID.String(),
		fmt.Sprintf(`{"code":%q,"name":"Renamed","type":"ASSET"}`, acctCode), `"1"`); w.Code != http.StatusForbidden {
		t.Errorf("PUT /api/v1/gl/accounts/{id} as branch-confined caller = %d, want 403: %s", w.Code, w.Body)
	}

	// Post activity to created account so it has a non-zero balance.
	post(t, db, svc, branch, time.Date(2039, 2, 16, 0, 0, 0, 0, time.UTC), "m6 grd acct bal",
		gl.Leg{AccountCode: acctCode, Debit: 5500}, gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 5500})

	// Update omitting normal_balance defaults it and preserves balance and created_at.
	wUpdate := serve(adminCaller, "PUT", "/api/v1/gl/accounts/"+created.ID.String(),
		fmt.Sprintf(`{"code":%q,"name":"Grd Acct Updated","type":"ASSET"}`, acctCode), `"1"`)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/gl/accounts/{id} omitting normal_balance = %d, want 200: %s", wUpdate.Code, wUpdate.Body)
	}
	var updated gl.GLAccount
	if err := json.Unmarshal(wUpdate.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.NormalBalance != gl.NormalDebit {
		t.Errorf("PUT account normal_balance = %q, want %q", updated.NormalBalance, gl.NormalDebit)
	}
	if updated.Balance != 5500 {
		t.Errorf("PUT account balance = %d, want 5500", updated.Balance)
	}
	if updated.CreatedAt.IsZero() {
		t.Errorf("PUT account created_at is zero, want non-zero")
	}

	// Invalid type on PUT returns 400, not 500.
	wBad := serve(adminCaller, "PUT", "/api/v1/gl/accounts/"+created.ID.String(),
		fmt.Sprintf(`{"code":%q,"name":"Grd Acct","type":"BOGUS"}`, acctCode), `"2"`)
	if wBad.Code != http.StatusBadRequest {
		t.Errorf("PUT /api/v1/gl/accounts/{id} with invalid type = %d, want 400: %s", wBad.Code, wBad.Body)
	}
}

// TestGLDetailAndAccountBalancesRespectCurrency verifies that GetGLDetail and
// GET /api/v1/gl/detail filter by currency (defaulting to dealer default), and
// that GetAccount and ListAccountsPage compute exact integer cents in the
// default currency without mixing foreign currencies or rounding negative balances
// toward zero.
func TestGLDetailAndAccountBalancesRespectCurrency(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wiredService(db)
	branch := secondBranch(t, db, "M6-CUR")
	h := gl.NewHandler(svc).WithBranchGuard(middleware.NewBranchGuard(db))

	acctCode := "9" + strings.ReplaceAll(uuid.NewString()[:5], "-", "1")
	acct := &gl.GLAccount{Code: acctCode, Name: "m6 cur acct", Type: gl.AccountTypeAsset}
	if err := svc.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1 OR entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $2)`, acct.ID, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1 OR entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $2)`, acct.ID, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE branch_id = $1 OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_accounts WHERE id = $1`, acct.ID)
	})

	day := time.Date(2039, 5, 10, 0, 0, 0, 0, time.UTC)
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := svc.PostEntry(ctx, gl.PostingInput{
			EntryDate: day, Memo: "m6 cur usd", Source: gl.SourceManual,
			BranchID: branch, Currency: "USD", PostedBy: "t",
			Legs: []gl.Leg{
				{AccountCode: gl.AccountCodeAR, Debit: 10000},
				{AccountCode: acctCode, Credit: 10000},
			},
		}); err != nil {
			return err
		}
		_, err := svc.PostEntry(ctx, gl.PostingInput{
			EntryDate: day, Memo: "m6 cur cad", Source: gl.SourceManual,
			BranchID: branch, Currency: "CAD", PostedBy: "t",
			Legs: []gl.Leg{
				{AccountCode: acctCode, Debit: 25000},
				{AccountCode: gl.AccountCodeRevenue, Credit: 25000},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Default GetGLDetail returns only USD lines (not CAD).
	usdRows, err := svc.GetGLDetail(ctx, acct.ID, day, day, &branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(usdRows) != 1 || usdRows[0].CreditCents != 10000 {
		t.Errorf("GetGLDetail default (USD) rows = %+v, want 1 row with credit 10000", usdRows)
	}

	// HTTP GET /api/v1/gl/detail?currency=CAD returns only CAD lines.
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/gl/detail?account_id=%s&from=2039-05-01&to=2039-05-31&branch_id=%s&currency=CAD", acct.ID, branch), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/gl/detail?currency=CAD = %d, want 200: %s", w.Code, w.Body)
	}
	var cadRows []gl.GLDetailRow
	if err := json.Unmarshal(w.Body.Bytes(), &cadRows); err != nil {
		t.Fatal(err)
	}
	if len(cadRows) != 1 || cadRows[0].DebitCents != 25000 {
		t.Errorf("GET /api/v1/gl/detail?currency=CAD rows = %+v, want 1 row with debit 25000", cadRows)
	}

	// GetAccount and ListAccountsPage compute exact -10000 cents in default USD without CAD mixing or -9999 float rounding bug.
	gotAcct, err := svc.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAcct.Balance != -10000 {
		t.Errorf("GetAccount balance = %d, want -10000", gotAcct.Balance)
	}
	page, err := svc.ListAccountsPage(ctx, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *gl.GLAccount
	for i := range page {
		if page[i].ID == acct.ID {
			found = &page[i]
			break
		}
	}
	if found == nil || found.Balance != -10000 {
		t.Errorf("ListAccountsPage account = %+v, want balance -10000", found)
	}
}


