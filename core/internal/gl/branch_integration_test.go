// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// The branch dimension of the ledger against a real database: the 125
// backfill stamps each source's own branch, the statements cut by branch and
// consolidate as the sum of the branches, the closed period refuses a posting
// until it is reopened with a reason, the year end close zeroes each branch's
// revenue and expense into Retained Earnings, the tie out answers its
// differences in cents, and the routes wall a one branch caller and answer a
// stale If-Match with the contract's code.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func defaultBranchOf(t *testing.T, db *database.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func secondBranch(t *testing.T, db *database.DB, code string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, $2)`, id, code)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id) })
	return id
}

func post(t *testing.T, db *database.DB, svc *gl.Service, branch uuid.UUID, when time.Time, memo string, legs ...gl.Leg) {
	t.Helper()
	err := db.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := svc.PostEntry(ctx, gl.PostingInput{EntryDate: when, Memo: memo, Source: gl.SourceManual,
			BranchID: branch, Currency: "USD", PostedBy: "t", Legs: legs})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func deleteEntriesByMemo(t *testing.T, db *database.DB, memo string) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE memo = $1)`, memo)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE memo = $1`, memo)
}

// TestMigration125BackfillsEachSource: on a database migrated to 121, each
// entry is stamped from its own source document, a reversal takes the branch
// of the entry it reverses, and whatever no source names lands on the default
// branch.
func TestMigration125BackfillsEachSource(t *testing.T) {
	db := testutil.RequireDB(t)
	admin, name := scratchDatabase(t, "gv1_m6mig")
	base, err := pgx.Connect(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close(context.Background()) })

	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var before []string
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "125_") || strings.HasPrefix(filepath.Base(f), "133_") {
			continue
		}
		before = append(before, f)
	}
	for _, f := range before {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := base.Exec(context.Background(), string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}

	ctx := context.Background()
	var def, other uuid.UUID
	if err := base.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	other = uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'M6B', 'M6 Second')`, other); err != nil {
		t.Fatal(err)
	}

	var customer, vendor uuid.UUID
	if err := base.QueryRow(ctx, `INSERT INTO customers (name, account_number, primary_branch_id) VALUES ('m6 tie out', $1, $2) RETURNING id`, "M6-"+uuid.NewString()[:8], def).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := base.QueryRow(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'm6 vendor') RETURNING id`, uuid.New()).Scan(&vendor); err != nil {
		t.Fatal(err)
	}

	doc := func(table string, branch *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		var err error
		switch table {
		case "invoices":
			_, err = base.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, number, status) VALUES ($1, $2, $3, 'M6-INV', 'UNPAID')`, id, customer, branch)
		case "payments":
			_, err = base.Exec(ctx, `INSERT INTO payments (id, customer_id, branch_id, amount, amount_unapplied, method, received_on, currency) VALUES ($1, $2, $3, 10, 0, 'CASH', CURRENT_DATE, 'USD')`, id, customer, branch)
		case "credit_memos":
			_, err = base.Exec(ctx, `INSERT INTO credit_memos (id, customer_id, branch_id, amount, subtotal, tax_amount, total_amount, memo_date, status, reason, reason_code, currency) VALUES ($1, $2, $3, -10, -10, 0, -10, CURRENT_DATE, 'DRAFT', 'm6', 'OTHER', 'USD')`, id, customer, branch)
		case "vendor_invoices":
			_, err = base.Exec(ctx, `INSERT INTO vendor_invoices (id, vendor_id, branch_id, invoice_number, invoice_date, due_date, subtotal, total) VALUES ($1, $2, $3, 'M6-VI', CURRENT_DATE, CURRENT_DATE + 30, 10, 10)`, id, vendor, branch)
		case "till_sessions":
			regID := "m6reg" + uuid.NewString()[:8]
			cashier := uuid.New()
			if _, err := base.Exec(ctx, `INSERT INTO staff (id, full_name, email) VALUES ($1, 'm6 cashier', $2)`, cashier, "m6reg_"+cashier.String()[:8]+"@example.invalid"); err != nil {
				t.Fatalf("seed cashier: %v", err)
			}
			regBranch := def
			if branch != nil {
				regBranch = *branch
			}
			if _, err = base.Exec(ctx, `INSERT INTO pos_registers (id, location_id, name, branch_id)
				VALUES ($1, $2, 'm6 reg', $2)`, regID, regBranch); err != nil {
				t.Fatalf("seed register: %v", err)
			}
			_, err = base.Exec(ctx, `INSERT INTO till_sessions (id, register_id, branch_id, cashier_id) VALUES ($1, $2, $3, $4)`, id, regID, branch, cashier)
		}
		if err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
		return id
	}

	entry := func(source string, ref uuid.UUID, reverses *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		_, err := base.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, status, posted_by, reverses_entry_id)
			VALUES ($1, CURRENT_DATE, 'm6 backfill', $2, $3, 'POSTED', 't', $4)`, id, source, ref, reverses)
		if err != nil {
			t.Fatal(err)
		}
		acc, _ := glAccount(ctx, base, "1010")
		if _, err := base.Exec(ctx, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit)
			VALUES ($1, $2, $3, 'm6', 10.00, 0)`, uuid.New(), id, acc); err != nil {
			t.Fatal(err)
		}
		return id
	}

	invoice := doc("invoices", &other)
	payment := doc("payments", &other)
	memo := doc("credit_memos", &other)
	vendorInv := doc("vendor_invoices", &other)
	session := doc("till_sessions", &other)

	payRefund := uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO payment_refunds (id, payment_id, amount, status, method, refunded_on)
		VALUES ($1, $2, 5.00, 'COMPLETE', 'CASH', CURRENT_DATE)`, payRefund, payment); err != nil {
		t.Fatalf("seed payment refund: %v", err)
	}
	cmRefund := uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO payment_refunds (id, credit_memo_id, amount, status, method, refunded_on)
		VALUES ($1, $2, 5.00, 'COMPLETE', 'CASH', CURRENT_DATE)`, cmRefund, memo); err != nil {
		t.Fatalf("seed credit memo refund: %v", err)
	}
	posTx := uuid.New()
	var regID string
	var cashierID uuid.UUID
	if err := base.QueryRow(ctx, `SELECT register_id, cashier_id FROM till_sessions WHERE id = $1`, session).Scan(&regID, &cashierID); err != nil {
		t.Fatalf("read session register/cashier: %v", err)
	}
	if _, err := base.Exec(ctx, `INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, till_session_id, subtotal, tax_amount, total, status, currency)
		VALUES ($1, $2, $3, $4, $5, 10.00, 0, 10.00, 'COMPLETED', 'USD')`, posTx, regID, cashierID, other, session); err != nil {
		t.Fatalf("seed pos_transactions: %v", err)
	}

	invEntry := entry("INVOICE", invoice, nil)
	payEntry := entry("PAYMENT", payment, nil)
	cmEntry := entry("CREDIT_MEMO", memo, nil)
	viEntry := entry("VENDOR_INVOICE", vendorInv, nil)
	adjEntry := entry("ADJUSTMENT", session, nil)
	rev1Entry := entry("REVERSAL", invEntry, &invEntry)
	rev2Entry := entry("REVERSAL", rev1Entry, &rev1Entry)
	payRefundEntry := entry("PAYMENT", payRefund, nil)
	cmRefundEntry := entry("CREDIT_MEMO", cmRefund, nil)
	posTxEntry := entry("PAYMENT", posTx, nil)
	manEntry := entry("MANUAL", uuid.New(), nil)

	sql, err := os.ReadFile("../../migrations/125_gl_branch_and_finstmt.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply 125: %v", err)
	}

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		want uuid.UUID
	}{
		{"INVOICE", invEntry, other},
		{"PAYMENT", payEntry, other},
		{"CREDIT_MEMO", cmEntry, other},
		{"VENDOR_INVOICE", viEntry, other},
		{"ADJUSTMENT", adjEntry, other},
		{"REVERSAL_1", rev1Entry, other},
		{"REVERSAL_2", rev2Entry, other},
		{"PAYMENT_REFUND", payRefundEntry, other},
		{"CREDIT_MEMO_REFUND", cmRefundEntry, other},
		{"POS_TRANSACTION", posTxEntry, other},
		{"MANUAL", manEntry, def},
	} {
		var got, line uuid.UUID
		if err := base.QueryRow(ctx, `SELECT e.branch_id, l.branch_id FROM gl_journal_entries e
			JOIN gl_journal_lines l ON l.journal_entry_id = e.id
			WHERE e.id = $1`, tc.id).Scan(&got, &line); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s entry branch = %v, want %v (def %v other %v)", tc.name, got, tc.want, def, other)
		}
		if line != got {
			t.Errorf("%s line branch = %v, want the entry's %v", tc.name, line, got)
		}
	}
	_ = name
	_ = db
}

func glAccount(ctx context.Context, conn *pgx.Conn, code string) (uuid.UUID, error) {
	var id uuid.UUID
	err := conn.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, code).Scan(&id)
	return id, err
}

func scratchDatabase(t *testing.T, prefix string) (string, string) {
	t.Helper()
	db := testutil.RequireDB(t)
	base := db.Pool.Config().ConnString()
	admin, err := pgx.Connect(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := prefix + "_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(context.Background(), fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close(context.Background())
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close(context.Background())
	})
	// The scratch name goes on the URL's path alone: swapping the first
	// substring match would eat a user or host that shares the database's
	// name.
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String(), name
}

// TestStatementsByBranchAndConsolidated: two branches post their own entries;
// each statement by branch differs, and the consolidated one is the sum of
// the branches to the cent.
func TestStatementsByBranchAndConsolidated(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	a := defaultBranchOf(t, db)
	b := secondBranch(t, db, "M6-STMT")
	memo := "m6 statements " + uuid.NewString()
	t.Cleanup(func() { deleteEntriesByMemo(t, db, memo) })

	day := time.Date(2031, 6, 15, 0, 0, 0, 0, time.UTC)
	post(t, db, svc, a, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 12000},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 12000})
	post(t, db, svc, b, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 4000},
		gl.Leg{AccountCode: gl.AccountCodeCOGS, Debit: 1500},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 4000},
		gl.Leg{AccountCode: "1030", Credit: 1500})

	pnlA, err := svc.GetProfitAndLoss(ctx, "2031-01-01", "2031-12-31", "USD", &a)
	if err != nil {
		t.Fatal(err)
	}
	pnlB, err := svc.GetProfitAndLoss(ctx, "2031-01-01", "2031-12-31", "USD", &b)
	if err != nil {
		t.Fatal(err)
	}
	pnlAll, err := svc.GetProfitAndLoss(ctx, "2031-01-01", "2031-12-31", "USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pnlA.TotalRevenue != 12000 || pnlB.TotalRevenue != 4000 {
		t.Errorf("branch revenues %d and %d, want 12000 and 4000", pnlA.TotalRevenue, pnlB.TotalRevenue)
	}
	if pnlB.TotalCOGS != 1500 || pnlA.TotalCOGS != 0 {
		t.Errorf("branch COGS %d and %d, want 0 and 1500", pnlA.TotalCOGS, pnlB.TotalCOGS)
	}
	if pnlAll.TotalRevenue != 16000 || pnlAll.TotalCOGS != 1500 {
		t.Errorf("consolidated revenue %d cogs %d, want 16000 and 1500 (the sum of the branches)", pnlAll.TotalRevenue, pnlAll.TotalCOGS)
	}

	bsA, err := svc.GetBalanceSheet(ctx, "2031-12-31", "USD", &a)
	if err != nil {
		t.Fatal(err)
	}
	bsB, err := svc.GetBalanceSheet(ctx, "2031-12-31", "USD", &b)
	if err != nil {
		t.Fatal(err)
	}
	bsAll, err := svc.GetBalanceSheet(ctx, "2031-12-31", "USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Retained earnings are inception to date, so the consolidated figure is
	// the sum over every branch that holds a line (a shared database holds
	// other tests' branches too), a and b among them.
	rows, err := db.Pool.Query(ctx, `SELECT DISTINCT branch_id FROM gl_journal_lines`)
	if err != nil {
		t.Fatal(err)
	}
	var branches []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		branches = append(branches, id)
	}
	rows.Close()
	var sumRetained, sumAssets int64
	for _, id := range branches {
		id := id
		bs, err := svc.GetBalanceSheet(ctx, "2031-12-31", "USD", &id)
		if err != nil {
			t.Fatal(err)
		}
		sumRetained += bs.RetainedEarnings
		sumAssets += bs.TotalAssets
	}
	if bsAll.RetainedEarnings != sumRetained || bsAll.TotalAssets != sumAssets {
		t.Errorf("consolidated retained earnings %d and assets %d, want the sum of the branches %d and %d",
			bsAll.RetainedEarnings, bsAll.TotalAssets, sumRetained, sumAssets)
	}
	if bsA.RetainedEarnings == bsB.RetainedEarnings {
		t.Errorf("the two branches' retained earnings are both %d, want them to differ", bsA.RetainedEarnings)
	}

	tb, err := svc.GetTrialBalanceFor(ctx, time.Date(2031, 12, 31, 0, 0, 0, 0, time.UTC), "USD", &b)
	if err != nil {
		t.Fatal(err)
	}
	var arCredit int64
	for _, row := range tb {
		if row.AccountCode == gl.AccountCodeAR {
			arCredit = row.Debit
		}
	}
	if arCredit != 4000 {
		t.Errorf("branch B trial balance AR debit %d, want 4000", arCredit)
	}
}

// TestPeriodCloseRefusesUntilReopenedWithReason: a closed period refuses a
// posting; reopening needs a reason; the reopened period takes it again.
func TestPeriodCloseRefusesUntilReopenedWithReason(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default()).WithAuditTrail(db, nil, nil)
	branch := defaultBranchOf(t, db)
	var id uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO gl_fiscal_periods (name, start_date, end_date) VALUES ('m6 close test', '2032-03-01', '2032-03-31') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM gl_fiscal_periods WHERE id = $1`, id) })

	day := time.Date(2032, 3, 10, 0, 0, 0, 0, time.UTC)
	memo := "m6 close " + uuid.NewString()
	t.Cleanup(func() { deleteEntriesByMemo(t, db, memo) })

	period, err := svc.GetFiscalPeriod(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := svc.CloseFiscalPeriodFor(ctx, id, period.Revision, "t")
	if err != nil || closed.Status != gl.PeriodClosed {
		t.Fatalf("close = %v, %v", closed, err)
	}

	err = db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := svc.PostEntry(ctx, gl.PostingInput{EntryDate: day, Memo: memo, Source: gl.SourceManual,
			BranchID: branch, Currency: "USD", Legs: []gl.Leg{
				{AccountCode: gl.AccountCodeAR, Debit: 100}, {AccountCode: gl.AccountCodeRevenue, Credit: 100}}})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("posting into the closed period = %v, want the closed period refusal", err)
	}

	if _, err := svc.ReopenFiscalPeriodFor(ctx, id, closed.Revision, "", "t"); err == nil {
		t.Fatal("reopen without a reason passed, want the refusal")
	}
	reopened, err := svc.ReopenFiscalPeriodFor(ctx, id, closed.Revision, "the March corrections", "t")
	if err != nil || reopened.Status != gl.PeriodOpen || reopened.ReopenReason != "the March corrections" {
		t.Fatalf("reopen = %v, %v", reopened, err)
	}
	post(t, db, svc, branch, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 100},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 100})
}

// TestYearEndCloseZeroesEachBranchIntoRetainedEarnings: the year end close
// writes one CLOSING entry per branch, zeroing 4xxx through 6xxx into 3020.
func TestYearEndCloseZeroesEachBranchIntoRetainedEarnings(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default()).WithAuditTrail(db, nil, nil)
	a := defaultBranchOf(t, db)
	b := secondBranch(t, db, "M6-YE")
	year := 2033
	memo := fmt.Sprintf("m6 year end %d", year)
	t.Cleanup(func() {
		deleteEntriesByMemo(t, db, memo)
		deleteEntriesByMemo(t, db, fmt.Sprintf("Year end close %d", year))
	})

	day := time.Date(year, 7, 1, 0, 0, 0, 0, time.UTC)
	post(t, db, svc, a, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 20500},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 20500})
	post(t, db, svc, a, day, memo,
		gl.Leg{AccountCode: "4030", Debit: 700},
		gl.Leg{AccountCode: gl.AccountCodeAR, Credit: 700})
	post(t, db, svc, a, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeCOGS, Debit: 8000},
		gl.Leg{AccountCode: "1030", Credit: 8000})
	post(t, db, svc, a, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeCash, Debit: 200},
		gl.Leg{AccountCode: "5030", Credit: 200})
	post(t, db, svc, b, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 9000},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 9000})
	post(t, db, svc, b, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeCOGS, Debit: 4000},
		gl.Leg{AccountCode: "1030", Credit: 4000})

	// Each branch's own net before the close: 12000 at one, 5000 at the
	// other, read off the income statement of the year.
	nets := map[uuid.UUID]int64{}
	for _, branch := range []uuid.UUID{a, b} {
		pnl, err := svc.GetProfitAndLoss(ctx, fmt.Sprintf("%d-01-01", year), fmt.Sprintf("%d-12-31", year), "USD", &branch)
		if err != nil {
			t.Fatal(err)
		}
		nets[branch] = pnl.NetIncome
	}
	if nets[a] != 12000 || nets[b] != 5000 {
		t.Fatalf("the year's nets %d and %d, want 12000 and 5000", nets[a], nets[b])
	}

	out, err := svc.CloseYear(ctx, year, "USD", "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("%d closing entries, want one per branch with activity", len(out.Entries))
	}

	for _, branch := range []uuid.UUID{a, b} {
		// After the close the year's income statement still reads what the
		// year earned (closing entries are not trading), while the revenue
		// and expense accounts themselves hold zero at the branch (the
		// closing entry zeroed them into 3020), and 3020 holds exactly the
		// branch's own net.
		pnl, err := svc.GetProfitAndLoss(ctx, fmt.Sprintf("%d-01-01", year), fmt.Sprintf("%d-12-31", year), "USD", &branch)
		if err != nil {
			t.Fatal(err)
		}
		if pnl.NetIncome != nets[branch] {
			t.Errorf("branch %v income statement after the close nets %d, want the year's %d", branch, pnl.NetIncome, nets[branch])
		}
		for _, code := range []string{"4010", "4030", "5010", "5030"} {
			var acctHeld int64
			if err := db.Pool.QueryRow(ctx, `
				SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint
				FROM gl_journal_lines l
				JOIN gl_journal_entries e ON e.id = l.journal_entry_id
				JOIN gl_accounts a ON a.id = l.account_id
				WHERE e.status = 'POSTED' AND a.code = $1 AND l.branch_id = $2
				  AND e.entry_date BETWEEN make_date($3, 1, 1) AND make_date($3, 12, 31)`, code, branch, year).Scan(&acctHeld); err != nil {
				t.Fatal(err)
			}
			if acctHeld != 0 {
				t.Errorf("branch %v account %s holds %d after the close, want zero", branch, code, acctHeld)
			}
		}
		var held int64
		if err := db.Pool.QueryRow(ctx, `
			SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint
			FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			JOIN gl_accounts a ON a.id = l.account_id
			WHERE e.status = 'POSTED' AND a.type IN ('REVENUE', 'EXPENSE') AND l.branch_id = $1
			  AND e.entry_date BETWEEN make_date($2, 1, 1) AND make_date($2, 12, 31)`, branch, year).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if held != 0 {
			t.Errorf("branch %v revenue and expense accounts hold %d after the close, want zero", branch, held)
		}
		var toRetained int64
		if err := db.Pool.QueryRow(ctx, `
			SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint
			FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			JOIN gl_accounts a ON a.id = l.account_id
			WHERE e.source = 'CLOSING' AND a.code = '3020' AND e.branch_id = $1`, branch).Scan(&toRetained); err != nil {
			t.Fatal(err)
		}
		if toRetained != nets[branch] {
			t.Errorf("branch %v closed %d into 3020, want its own net %d", branch, toRetained, nets[branch])
		}
	}
}

// TestTieOutAnswersItsDifferencesInCents: the AR control against the
// customer subledger and the AP control against the open bills, each
// difference answered in cents.
func TestTieOutAnswersItsDifferencesInCents(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	branch := secondBranch(t, db, "M6-TIE")
	memo := "m6 tie out " + uuid.NewString()
	t.Cleanup(func() { deleteEntriesByMemo(t, db, memo) })

	cust := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due, currency) VALUES ($1, 'm6 tie', $3, $2, 40.00, 'USD')`, cust, branch, "M6-"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust) })
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_transactions (customer_id, type, amount, balance_after, currency) VALUES ($1, 'ADJUSTMENT', 4000, 4000, 'USD')`, cust); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2034, 5, 2, 0, 0, 0, 0, time.UTC)
	post(t, db, svc, branch, day, memo,
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 10000},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 10000})

	report, err := svc.TieOut(ctx, day, "USD", &branch)
	if err != nil {
		t.Fatal(err)
	}
	if report.AR.ControlCents != 10000 {
		t.Errorf("AR control %d, want 10000", report.AR.ControlCents)
	}
	if report.AR.SubledgerCents != 4000 {
		t.Errorf("AR subledger %d, want 4000", report.AR.SubledgerCents)
	}
	if report.AR.DifferenceCents != 6000 {
		t.Errorf("AR difference %d, want 6000", report.AR.DifferenceCents)
	}
	if report.AP.DifferenceCents != 0 {
		t.Errorf("AP difference %d, want 0 (nothing owed)", report.AP.DifferenceCents)
	}
	// The subledgers hold only their current balances, so an earlier day is
	// refused rather than answered wrongly.
	if _, err := svc.TieOut(ctx, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), "USD", &branch); !errors.Is(err, gl.ErrTieOutHistoric) {
		t.Errorf("tie out of a past day = %v, want ErrTieOutHistoric", err)
	}
}

// TestWireBranchWallsAndIfMatch: a caller held to one branch is refused
// another branch's statement and the consolidated read, and a stale If-Match
// answers the contract's stale code.
func TestWireBranchWallsAndIfMatch(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default()).WithAuditTrail(db, nil, nil)
	a := defaultBranchOf(t, db)
	b := secondBranch(t, db, "M6-WIRE")
	h := gl.NewHandler(svc).WithBranchGuard(middleware.NewBranchGuard(db))

	sub := "m6-user-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 't')`, sub, b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub) })

	serve := func(bc *branchctx.Context, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		c := context.Background()
		if bc != nil {
			c = branchctx.With(c, bc)
		}
		r = r.WithContext(c)
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
		return w
	}

	// A one branch caller is refused the consolidated read.
	if w := serve(&branchctx.Context{UserSub: sub}, "GET", "/api/v1/gl/trial-balance", "", nil); w.Code != http.StatusForbidden {
		t.Errorf("one branch caller consolidated trial balance = %d, want 403: %s", w.Code, w.Body)
	}
	// And another branch's.
	if w := serve(&branchctx.Context{UserSub: sub}, "GET", "/api/v1/gl/trial-balance?branch_id="+a.String(), "", nil); w.Code != http.StatusForbidden {
		t.Errorf("one branch caller another branch trial balance = %d, want 403: %s", w.Code, w.Body)
	}
	// Its own branch reads.
	if w := serve(&branchctx.Context{UserSub: sub}, "GET", "/api/v1/gl/trial-balance?branch_id="+b.String(), "", nil); w.Code != http.StatusOK {
		t.Errorf("one branch caller own branch trial balance = %d, want 200: %s", w.Code, w.Body)
	}
	// An unheld caller reads the consolidated one.
	if w := serve(nil, "GET", "/api/v1/gl/trial-balance", "", nil); w.Code != http.StatusOK {
		t.Errorf("every branch caller consolidated trial balance = %d, want 200: %s", w.Code, w.Body)
	}

	// If-Match: a period update without a precondition is refused, with a
	// stale one it is a stale revision, and with the current one it closes.
	var id uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO gl_fiscal_periods (name, start_date, end_date) VALUES ('m6 wire', '2035-01-01', '2035-01-31') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM gl_fiscal_periods WHERE id = $1`, id) })
	if w := serve(nil, "POST", "/api/v1/gl/fiscal-periods/"+id.String()+"/close", "", nil); w.Code != http.StatusPreconditionRequired {
		t.Errorf("close without If-Match = %d, want 428: %s", w.Code, w.Body)
	}
	if w := serve(nil, "POST", "/api/v1/gl/fiscal-periods/"+id.String()+"/close", "", map[string]string{"If-Match": `"99"`}); w.Code != http.StatusConflict {
		t.Errorf("close with a stale If-Match = %d, want 409: %s", w.Code, w.Body)
	}
	if w := serve(nil, "POST", "/api/v1/gl/fiscal-periods/"+id.String()+"/close", "", map[string]string{"If-Match": `"1"`}); w.Code != http.StatusOK {
		t.Errorf("close with the current If-Match = %d, want 200: %s", w.Code, w.Body)
	}
	// Reopen needs its reason.
	if w := serve(nil, "POST", "/api/v1/gl/fiscal-periods/"+id.String()+"/reopen", `{}`, map[string]string{"If-Match": `"2"`}); w.Code != http.StatusBadRequest {
		t.Errorf("reopen without a reason = %d, want 400: %s", w.Code, w.Body)
	}
	if w := serve(nil, "POST", "/api/v1/gl/fiscal-periods/"+id.String()+"/reopen", `{"reason":"correction"}`, map[string]string{"If-Match": `"2"`}); w.Code != http.StatusOK {
		t.Errorf("reopen with a reason = %d, want 200: %s", w.Code, w.Body)
	}
}
