// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// The ledger's acts against a real database: each rides its audit row and its
// outbox event in its own transaction, the by-id acts and the company wide
// acts are walled to the caller's reach, and the contended acts finish at
// pool size 4 with three contenders and exactly one winner.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
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
