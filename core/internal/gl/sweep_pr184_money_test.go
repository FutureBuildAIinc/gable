// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// The PR 184 sweep money tests: cross-branch settlements ride 1095
// Inter-Branch Clearing and leave 1010 Cash alone (P1-1), historic
// cross-branch applications tie out after migration 125 (P2-1), and the GL
// reverse refuses entries a subledger document owns (P2-2).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// sweepAcctBalance answers one account's posted debit-less-credit balance in
// cents on one branch, straight off the lines.
func sweepAcctBalance(t *testing.T, db *database.DB, ctx context.Context, branch uuid.UUID, code string) int64 {
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

// sweepCleanupCustomer removes one customer's documents and both branches'
// entries, the way the branch acts test does.
func sweepCleanupCustomer(t *testing.T, db *database.DB, cust uuid.UUID, branches ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
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
	if len(branches) > 0 {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE branch_id = ANY($1) OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = ANY($1))`, branches)
		_, _ = db.Pool.Exec(ctx, `UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE branch_id = ANY($1)`, branches)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE branch_id = ANY($1)`, branches)
	}
}

// sweepScratchDatabase creates an empty scratch database and answers its
// URL, with the database name swapped on the path (the caller's URL keeps
// its own user and password).
func sweepScratchDatabase(t *testing.T, prefix string) string {
	t.Helper()
	db := testutil.RequireDB(t)
	admin, err := pgx.Connect(context.Background(), db.Pool.Config().ConnString())
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
	u, err := url.Parse(db.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// TestSweepPR184_P1_1_InterBranchSettlementsUseClearingAccountAndLeaveCashUnchanged:
// a credit memo applied across branches and a payment taken at one branch and
// applied to another branch's invoice both settle over 1095 Inter-Branch
// Clearing, so 1010 Cash moves only by the cash the branch actually took,
// each branch's statements balance with 1095 on the face, and the
// consolidated statements eliminate 1095 unless include_inter_branch asks
// for it.
func TestSweepPR184_P1_1_InterBranchSettlementsUseClearingAccountAndLeaveCashUnchanged(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	arSvc := account.NewService(db, glSvc, slog.Default())

	branchA := secondBranch(t, db, "SW184-A")
	branchB := secondBranch(t, db, "SW184-B")

	cust := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due, currency) VALUES ($1, 'sw184 cust', $2, $3, 0, 'USD')`,
		cust, "SW184-"+cust.String()[:6], branchA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sweepCleanupCustomer(t, db, cust, branchA, branchB) })

	today, err := glSvc.BusinessDay(ctx, &branchA)
	if err != nil {
		t.Fatal(err)
	}
	day := today.Format("2006-01-02")

	postInvoice := func(id uuid.UUID, branch uuid.UUID, cents int64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, number, status, subtotal, tax_amount, total_amount, amount_open, currency, invoice_date)
			VALUES ($1, $2, $3, $4, 'UNPAID', $5, 0, $5, $5, 'USD', $6::date)`,
			id, cust, branch, "SW184-"+id.String()[:6], float64(cents)/100, day); err != nil {
			t.Fatal(err)
		}
		if err := db.RunInTx(ctx, func(ctx context.Context) error {
			_, err := arSvc.PostInvoice(ctx, account.PostInvoiceIn{
				InvoiceID: id, CustomerID: cust, Currency: "USD",
				TotalCents: cents, On: today, Actor: "t",
				Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Credit: cents}},
			})
			return err
		}); err != nil {
			t.Fatalf("PostInvoice %s: %v", id, err)
		}
	}

	// An invoice of 100.00 at branch B and one of 150.00 at branch B.
	invSmall := uuid.New()
	invLarge := uuid.New()
	postInvoice(invSmall, branchB, 10000)
	postInvoice(invLarge, branchB, 15000)

	// A credit memo of 100.00 at branch A, applied to the small invoice at
	// branch B: no cash is involved at all.
	cm := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO credit_memos (id, customer_id, branch_id, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency)
		VALUES ($1, $2, $3, 100.00, -100.00, 0, -100.00, 0, $4::date, 'DRAFT', 'sw184', 'OTHER', 'USD')`,
		cm, cust, branchA, day); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostCreditMemo(ctx, account.PostCreditMemoIn{
			MemoID: cm, CustomerID: cust, Number: "SW184-CM-" + cm.String()[:6], Currency: "USD",
			MemoDate: today, SubtotalCents: -10000, TaxCents: 0, TotalCents: -10000, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Debit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostCreditMemo: %v", err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{
			MemoID: cm, Lines: []account.ApplyLine{{InvoiceID: invSmall, AmountCents: 10000}}, On: today, Actor: "t",
		})
		return err
	}); err != nil {
		t.Fatalf("ApplyCreditMemo cross-branch: %v", err)
	}

	// A payment of 150.00 taken at branch A and applied to the large invoice
	// at branch B: branch A keeps the cash it took, branch B sees none.
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, _, err := arSvc.RecordPayment(ctx, account.RecordPaymentIn{
			CustomerID: cust, BranchID: branchA, Currency: "USD", Method: "CASH",
			AmountCents: 15000, ReceivedOn: today, Actor: "t",
			Applications: []account.ApplyLine{{InvoiceID: invLarge, AmountCents: 15000}},
		})
		return err
	}); err != nil {
		t.Fatalf("RecordPayment cross-branch: %v", err)
	}

	// 1010 Cash moves only by the payment branch A itself took.
	if got := sweepAcctBalance(t, db, ctx, branchA, gl.AccountCodeCash); got != 15000 {
		t.Errorf("branchA 1010 Cash = %d cents, want 15000 (only the payment it took)", got)
	}
	if got := sweepAcctBalance(t, db, ctx, branchB, gl.AccountCodeCash); got != 0 {
		t.Errorf("branchB 1010 Cash = %d cents, want 0 (no cash reached branch B)", got)
	}

	// The inter-branch legs ride 1095: branch A owes 250.00, branch B is owed
	// 250.00 (the 100.00 memo application and the 150.00 payment application).
	if got := sweepAcctBalance(t, db, ctx, branchA, "1095"); got != -25000 {
		t.Errorf("branchA 1095 Inter-Branch Clearing = %d cents, want -25000", got)
	}
	if got := sweepAcctBalance(t, db, ctx, branchB, "1095"); got != 25000 {
		t.Errorf("branchB 1095 Inter-Branch Clearing = %d cents, want 25000", got)
	}

	hasRow := func(rows []gl.TrialBalanceRow, code string) (gl.TrialBalanceRow, bool) {
		for _, r := range rows {
			if r.AccountCode == code {
				return r, true
			}
		}
		return gl.TrialBalanceRow{}, false
	}

	// Each single branch's trial balance shows 1095 and balances.
	for _, b := range []uuid.UUID{branchA, branchB} {
		rows, err := glSvc.GetTrialBalanceFor(ctx, today, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := hasRow(rows, "1095"); !ok {
			t.Errorf("branch %s trial balance has no 1095 row, want the inter-branch clearing on the face", b)
		}
		var dr, cr int64
		for _, r := range rows {
			dr += r.Debit
			cr += r.Credit
		}
		if dr != cr {
			t.Errorf("branch %s trial balance debits %d != credits %d", b, dr, cr)
		}
		bs, err := glSvc.GetBalanceSheetFor(ctx, day, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if bs.TotalAssets != bs.TotalLiabilities+bs.TotalEquity {
			t.Errorf("branch %s balance sheet assets %d != liabilities %d + equity %d", b, bs.TotalAssets, bs.TotalLiabilities, bs.TotalEquity)
		}
	}

	// The consolidated trial balance eliminates 1095 by default.
	rows, err := glSvc.GetTrialBalanceFor(ctx, today, "USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hasRow(rows, "1095"); ok {
		t.Errorf("consolidated trial balance shows 1095 by default, want it eliminated")
	}
	// With the inter-branch arm turned on, it is included.
	rows, err = glSvc.GetTrialBalanceFor(gl.WithIncludeInterBranch(ctx, true), today, "USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hasRow(rows, "1095"); !ok {
		t.Errorf("consolidated trial balance with include_inter_branch shows no 1095 row, want it included")
	}

	// The routes answer the same: eliminated by default, included on
	// include_inter_branch=true.
	h := gl.NewHandler(glSvc).WithBranchGuard(middleware.NewBranchGuard(db))
	serve := func(target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
		return w
	}
	tbRows := func(w *httptest.ResponseRecorder) []gl.TrialBalanceRow {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", w.Result().Request.URL.Path, w.Code, w.Body)
		}
		var rows []gl.TrialBalanceRow
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if _, ok := hasRow(tbRows(serve("/api/v1/gl/trial-balance")), "1095"); ok {
		t.Errorf("consolidated trial balance route shows 1095 by default, want it eliminated")
	}
	if _, ok := hasRow(tbRows(serve("/api/v1/gl/trial-balance?include_inter_branch=true")), "1095"); !ok {
		t.Errorf("trial balance route with include_inter_branch=true shows no 1095 row, want it included")
	}

	bsAssets := func(w *httptest.ResponseRecorder) map[string]int64 {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("balance sheet route = %d: %s", w.Code, w.Body)
		}
		var report struct {
			Assets []gl.AccountLineItem `json:"assets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, a := range report.Assets {
			out[a.AccountCode] = a.Amount
		}
		return out
	}
	if _, ok := bsAssets(serve("/api/v1/gl/balance-sheet?as_of=" + day))["1095"]; ok {
		t.Errorf("consolidated balance sheet shows 1095 by default, want it eliminated")
	}
	if _, ok := bsAssets(serve("/api/v1/gl/balance-sheet?as_of=" + day + "&include_inter_branch=true"))["1095"]; !ok {
		t.Errorf("balance sheet route with include_inter_branch=true shows no 1095 asset, want it included")
	}
}

// TestSweepPR184_P2_1_HistoricCrossBranchApplicationsTieOut: a credit memo
// application shaped the way the pre-M6 core wrote it (no GL transfer entry)
// leaves both branches' AR tie out at zero.
func TestSweepPR184_P2_1_HistoricCrossBranchApplicationsTieOut(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	arSvc := account.NewService(db, glSvc, slog.Default())

	branchA := secondBranch(t, db, "SW184-HA")
	branchB := secondBranch(t, db, "SW184-HB")

	cust := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due, currency) VALUES ($1, 'sw184 historic cust', $2, $3, 0, 'USD')`,
		cust, "SW184-"+cust.String()[:6], branchA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sweepCleanupCustomer(t, db, cust, branchA, branchB) })

	today, err := glSvc.BusinessDay(ctx, &branchA)
	if err != nil {
		t.Fatal(err)
	}

	invB := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, number, status, subtotal, tax_amount, total_amount, amount_open, currency, invoice_date)
		VALUES ($1, $2, $3, $4, 'UNPAID', 100.00, 0, 100.00, 100.00, 'USD', $5::date)`,
		invB, cust, branchB, "SW184-"+invB.String()[:6], today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostInvoice(ctx, account.PostInvoiceIn{
			InvoiceID: invB, CustomerID: cust, Currency: "USD",
			TotalCents: 10000, On: today, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Credit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	cm := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO credit_memos (id, customer_id, branch_id, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency)
		VALUES ($1, $2, $3, 100.00, -100.00, 0, -100.00, 0, $4::date, 'DRAFT', 'sw184', 'OTHER', 'USD')`,
		cm, cust, branchA, today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostCreditMemo(ctx, account.PostCreditMemoIn{
			MemoID: cm, CustomerID: cust, Number: "SW184-CM-" + cm.String()[:6], Currency: "USD",
			MemoDate: today, SubtotalCents: -10000, TaxCents: 0, TotalCents: -10000, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Debit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostCreditMemo: %v", err)
	}

	// The pre-M6 shape: an application row with no GL transfer entry behind it.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'CREDIT_MEMO', $3, $4, 100.00, $5::date, 't', $6)`,
		uuid.New(), cust, cm, invB, today.Format("2006-01-02"), uuid.New()); err != nil {
		t.Fatal(err)
	}

	for _, b := range []uuid.UUID{branchA, branchB} {
		tie, err := glSvc.TieOut(ctx, today, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AR.DifferenceCents != 0 {
			t.Errorf("branch %s AR tie out with a pre-M6 cross-branch application = %+v, want DifferenceCents 0", b, tie.AR)
		}
	}
}

// TestSweepPR184_P2_1_Migrations125Then133BackfillHistoricCrossBranchSources:
// on a database migrated to just before 125, a historic cross-branch credit
// memo application (no entry) and a historic cross-branch payment application
// (a two line DR 2200 / CR 1020 entry) are backfilled with balanced 1095
// Inter-Branch Clearing legs by migration 133 (125 still stamps the branches),
// both branches balance per entry and tie out, and the down then up cycle
// lands on the same state.
func TestSweepPR184_P2_1_Migrations125Then133BackfillHistoricCrossBranchSources(t *testing.T) {
	scratchURL := sweepScratchDatabase(t, "gv1_sw184mig")
	base, err := pgx.Connect(context.Background(), scratchURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close(context.Background()) })
	ctx := context.Background()

	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "125_") || strings.HasPrefix(filepath.Base(f), "133_") {
			continue
		}
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := base.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}

	var def, other uuid.UUID
	if err := base.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	other = uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'SW184-M', 'SW184 Second')`, other); err != nil {
		t.Fatal(err)
	}

	var cust uuid.UUID
	if err := base.QueryRow(ctx, `INSERT INTO customers (name, account_number, primary_branch_id) VALUES ('sw184 mig', $1, $2) RETURNING id`, "SW184-"+uuid.NewString()[:8], def).Scan(&cust); err != nil {
		t.Fatal(err)
	}

	// The pre-M6 documents: a memo of 100.00 on the default branch applied to
	// an invoice of 100.00 on the second branch, and a payment of 150.00 on
	// the default branch applied to an invoice of 150.00 on the second branch.
	invCM := uuid.New()
	invPay := uuid.New()
	cm := uuid.New()
	pay := uuid.New()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'SW184-I1', 'UNPAID', 100.00, 0, 'USD')`, []any{invCM, cust, other}},
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'SW184-I2', 'UNPAID', 150.00, 0, 'USD')`, []any{invPay, cust, other}},
		{`INSERT INTO credit_memos (id, customer_id, branch_id, number, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency) VALUES ($1, $2, $3, 'SW184-CM', -100.00, -100.00, 0, -100.00, 0, CURRENT_DATE, 'OPEN', 'sw184', 'OTHER', 'USD')`, []any{cm, cust, def}},
		{`INSERT INTO payments (id, customer_id, branch_id, amount, amount_unapplied, method, received_on, currency) VALUES ($1, $2, $3, 150.00, 0, 'CASH', CURRENT_DATE, 'USD')`, []any{pay, cust, def}},
	}
	for _, s := range stmts {
		if _, err := base.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cmApp := uuid.New()
	payApp := uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'CREDIT_MEMO', $3, $4, 100.00, CURRENT_DATE, 't', $5)`, cmApp, cust, cm, invCM, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'PAYMENT', $3, $4, 150.00, CURRENT_DATE, 't', $5)`, payApp, cust, pay, invPay, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// The pre-M6 entries: both invoices and the memo posted their own two leg
	// entries (no branch stamps yet), and the payment application posted the
	// two line DR 2200 / CR 1020 entry with no transfer legs.
	entry := func(source string, ref uuid.UUID, lines [][3]any) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := base.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, status, posted_by, currency)
			VALUES ($1, CURRENT_DATE, 'sw184 mig', $2, $3, 'POSTED', 't', 'USD')`, id, source, ref); err != nil {
			t.Fatalf("seed entry %s: %v", source, err)
		}
		for _, l := range lines {
			var lineID uuid.UUID
			if err := base.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, l[0]).Scan(&lineID); err != nil {
				t.Fatal(err)
			}
			if _, err := base.Exec(ctx, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit)
				VALUES ($1, $2, $3, 'sw184', $4, $5)`, uuid.New(), id, lineID, l[1], l[2]); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	_ = entry("INVOICE", invCM, [][3]any{{"1020", 100.00, 0}, {"4010", 0, 100.00}})
	_ = entry("INVOICE", invPay, [][3]any{{"1020", 150.00, 0}, {"4010", 0, 150.00}})
	_ = entry("CREDIT_MEMO", cm, [][3]any{{"4010", 100.00, 0}, {"1020", 0, 100.00}})
	payEntry := entry("PAYMENT", payApp, [][3]any{{"2200", 150.00, 0}, {"1020", 0, 150.00}})

	// The pre-M6 customer transactions the subledger reads.
	tx := func(amount int64, ref uuid.UUID, typ string) {
		t.Helper()
		if _, err := base.Exec(ctx, `INSERT INTO customer_transactions (customer_id, amount, balance_after, reference_id, description, type, currency, source_kind)
			VALUES ($1, $2, 0, $3, 'sw184', $4, 'USD', 'core')`, cust, amount, ref, typ); err != nil {
			t.Fatal(err)
		}
	}
	tx(10000, invCM, "INVOICE")
	tx(15000, invPay, "INVOICE")
	tx(-10000, cm, "CREDIT_MEMO")
	tx(-15000, payApp, "PAYMENT")

	apply := func(name string) {
		t.Helper()
		path := "../../migrations/" + name
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := base.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	apply("125_gl_branch_and_finstmt.sql")
	apply("133_gl_interbranch_clearing_and_ar_tieout.sql")

	legsOf := func(entryID uuid.UUID) map[string]map[uuid.UUID][2]float64 {
		t.Helper()
		out := map[string]map[uuid.UUID][2]float64{}
		rows, err := base.Query(ctx, `SELECT a.code, l.branch_id, l.debit, l.credit
			FROM gl_journal_lines l JOIN gl_accounts a ON a.id = l.account_id
			WHERE l.journal_entry_id = $1 ORDER BY a.code`, entryID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var branch uuid.UUID
			var dr, cr float64
			if err := rows.Scan(&code, &branch, &dr, &cr); err != nil {
				t.Fatal(err)
			}
			if out[code] == nil {
				out[code] = map[uuid.UUID][2]float64{}
			}
			out[code][branch] = [2]float64{dr, cr}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The credit memo application gained its balanced four leg transfer entry.
	var cmEntry uuid.UUID
	if err := base.QueryRow(ctx, `SELECT gl_entry_id FROM ar_applications WHERE id = $1`, cmApp).Scan(&cmEntry); err != nil {
		t.Fatalf("credit memo application has no backfilled entry: %v", err)
	}
	cmLegs := legsOf(cmEntry)
	wantCMLegs := []struct {
		code   string
		branch uuid.UUID
		dr, cr float64
	}{
		{"1020", def, 100.00, 0}, {"1095", def, 0, 100.00},
		{"1095", other, 100.00, 0}, {"1020", other, 0, 100.00},
	}
	for _, w := range wantCMLegs {
		legs, ok := cmLegs[w.code][w.branch]
		if !ok {
			t.Errorf("memo transfer entry has no %s leg on branch %v", w.code, w.branch)
			continue
		}
		if legs[0] != w.dr || legs[1] != w.cr {
			t.Errorf("memo transfer entry %s leg on branch %v = DR %v CR %v, want DR %v CR %v", w.code, w.branch, legs[0], legs[1], w.dr, w.cr)
		}
	}

	// The payment application entry grew its two 1095 legs and moved its 2200
	// leg to the payment's branch.
	payLegs := legsOf(payEntry)
	if legs, ok := payLegs["2200"][def]; !ok || legs[0] != 150.00 {
		t.Errorf("payment entry 2200 leg on the payment branch = %v, want DR 150.00", payLegs["2200"][def])
	}
	if legs, ok := payLegs["1095"][def]; !ok || legs[0] != 0 || legs[1] != 150.00 {
		t.Errorf("payment entry 1095 leg on the payment branch = %v, want CR 150.00", payLegs["1095"][def])
	}
	if legs, ok := payLegs["1095"][other]; !ok || legs[0] != 150.00 {
		t.Errorf("payment entry 1095 leg on the invoice branch = %v, want DR 150.00", payLegs["1095"][other])
	}

	// Every entry balances per branch.
	rows, err := base.Query(ctx, `SELECT e.id, l.branch_id, SUM(l.debit), SUM(l.credit)
		FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		GROUP BY e.id, l.branch_id HAVING SUM(l.debit) <> SUM(l.credit)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, branch uuid.UUID
		var dr, cr float64
		if err := rows.Scan(&id, &branch, &dr, &cr); err != nil {
			t.Fatal(err)
		}
		t.Errorf("entry %s branch %v debits %v != credits %v after the backfill", id, branch, dr, cr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Both branches tie out.
	scratchDB, err := database.Connect(scratchURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scratchDB.Pool.Close() })
	svc := gl.NewService(gl.NewRepository(scratchDB), nil, slog.Default())
	for _, b := range []uuid.UUID{def, other} {
		tie, err := svc.TieOut(ctx, time.Time{}, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AR.DifferenceCents != 0 {
			t.Errorf("branch %v AR tie out after the 125 backfill = %+v, want DifferenceCents 0", b, tie.AR)
		}
	}

	// The down migration undoes the backfill and the account, and a second up
	// lands on the same state.
	apply("down/133_gl_interbranch_clearing_and_ar_tieout_down.sql")
	var gone int
	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_accounts WHERE code = '1095'`).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Errorf("1095 still seeded after the down migration")
	}
	apply("133_gl_interbranch_clearing_and_ar_tieout.sql")
	var cmEntries int
	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries e
		JOIN ar_applications a ON a.gl_entry_id = e.id WHERE a.id = $1`, cmApp).Scan(&cmEntries); err != nil {
		t.Fatal(err)
	}
	if cmEntries != 1 {
		t.Errorf("credit memo application has %d backfill entries after down then up, want 1", cmEntries)
	}
	var redone uuid.UUID
	if err := base.QueryRow(ctx, `SELECT gl_entry_id FROM ar_applications WHERE id = $1`, cmApp).Scan(&redone); err != nil {
		t.Fatalf("second up left no backfilled entry: %v", err)
	}
	if legs, ok := legsOf(redone)["1095"][other]; !ok || legs[0] != 100.00 {
		t.Errorf("second up 1095 leg on the invoice branch = %v, want DR 100.00", legsOf(redone)["1095"][other])
	}
}

// TestSweepPR184_P2_2_GLReverseRefusesSubledgerEntries: the GL reverse
// refuses the entries a subledger document owns, the tie outs stay clean, the
// document's own void still works, and a MANUAL entry still reverses.
func TestSweepPR184_P2_2_GLReverseRefusesSubledgerEntries(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	glSvc := wiredService(db)
	arSvc := account.NewService(db, glSvc, slog.Default())
	branch := secondBranch(t, db, "SW184-R")
	h := gl.NewHandler(glSvc).WithBranchGuard(middleware.NewBranchGuard(db))

	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE branch_id = $1 OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE branch_id = $1`, branch)
	})

	post := func(source string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := db.RunInTx(ctx, func(ctx context.Context) error {
			entry, err := glSvc.PostEntry(ctx, gl.PostingInput{
				EntryDate: time.Now(), Memo: "sw184 " + source, Source: source, BranchID: branch,
				Currency: "USD", PostedBy: "t",
				Legs: []gl.Leg{
					{AccountCode: gl.AccountCodeCash, Debit: 2000},
					{AccountCode: gl.AccountCodeRevenue, Credit: 2000},
				},
			})
			if err != nil {
				return err
			}
			id = entry.ID
			return nil
		}); err != nil {
			t.Fatalf("PostEntry %s: %v", source, err)
		}
		return id
	}

	serve := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(`{"reason":"sw184"}`))
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
		return w
	}

	sources := []string{gl.SourceInvoice, gl.SourcePayment, gl.SourceCreditMemo, gl.SourceVendorInv}
	for _, source := range sources {
		id := post(source)
		if _, err := glSvc.ReverseJournalEntry(ctx, id, "sw184"); err == nil || !strings.Contains(err.Error(), "can only reverse MANUAL journal entries") {
			t.Errorf("ReverseJournalEntry on a %s entry = %v, want the MANUAL-only refusal", source, err)
		}
		w := serve(http.MethodPost, "/api/v1/gl/journal-entries/"+id.String()+"/reverse")
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST reverse on a %s entry = %d, want 400: %s", source, w.Code, w.Body)
		}
	}

	// The tie outs stay clean.
	tie, err := glSvc.TieOut(ctx, time.Time{}, "USD", &branch)
	if err != nil {
		t.Fatal(err)
	}
	if tie.AR.DifferenceCents != 0 || tie.AP.DifferenceCents != 0 {
		t.Errorf("tie out after the refused reversals = AR %+v AP %+v, want both differences 0", tie.AR, tie.AP)
	}

	// The document's own void still works: a real invoice, its entry refused
	// on the GL reverse, voids cleanly afterwards.
	cust := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due, currency) VALUES ($1, 'sw184 void cust', $2, $3, 0, 'USD')`,
		cust, "SW184-"+cust.String()[:6], branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sweepCleanupCustomer(t, db, cust) })

	today, err := glSvc.BusinessDay(ctx, &branch)
	if err != nil {
		t.Fatal(err)
	}
	inv := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, number, status, subtotal, tax_amount, total_amount, amount_open, currency, invoice_date)
		VALUES ($1, $2, $3, $4, 'UNPAID', 100.00, 0, 100.00, 100.00, 'USD', $5::date)`,
		inv, cust, branch, "SW184-"+inv.String()[:6], today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.PostInvoice(ctx, account.PostInvoiceIn{
			InvoiceID: inv, CustomerID: cust, Currency: "USD",
			TotalCents: 10000, On: today, Actor: "t",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Credit: 10000}},
		})
		return err
	}); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}
	var entryID *uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT gl_entry_id FROM invoices WHERE id = $1`, inv).Scan(&entryID); err != nil || entryID == nil {
		t.Fatal("the invoice has no posted entry")
	}
	if _, err := glSvc.ReverseJournalEntry(ctx, *entryID, "sw184"); err == nil || !strings.Contains(err.Error(), "can only reverse MANUAL journal entries") {
		t.Errorf("ReverseJournalEntry on a real invoice entry = %v, want the MANUAL-only refusal", err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := arSvc.VoidInvoice(ctx, account.VoidInvoiceIn{InvoiceID: inv, EntryID: entryID, Actor: "t", Reason: "sw184", On: today})
		return err
	}); err != nil {
		t.Fatalf("VoidInvoice after the refused GL reverse: %v", err)
	}
	// A MANUAL entry still reverses.
	manual := post(gl.SourceManual)
	if _, err := glSvc.ReverseJournalEntry(ctx, manual, "sw184"); err != nil {
		t.Errorf("ReverseJournalEntry on a MANUAL entry = %v, want nil", err)
	}
}
