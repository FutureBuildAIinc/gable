// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// CORRECTNESS: the dashboard's Outstanding AR is the yard's net open
// receivable as the AR aging reads it: invoices at their open amount (part
// payments netted), open credit memos (negative) and unapplied cash (negative),
// inside the yard's wall. One yard holds an unpaid invoice, a part paid
// invoice, an open credit memo and unapplied cash; a second yard's invoice must
// not count. The count beside it stays the number of open invoices.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB).
func TestGetDashboardSummary_OutstandingARIsNetOpenReceivable(t *testing.T) {
	testutil.LockBranchDay(t)
	outstandingARLeg(t)
}

// RULE: the same leg passes whichever zone the default branch is in, so the
// fixture reads the branch's day throughout. Pacific/Kiritimati (UTC+14) and
// Pacific/Pago_Pago (UTC-11) are on different days from each other for most
// of every day, and at any moment one of them is on a day other than UTC's.
func TestGetDashboardSummary_OutstandingARInAnyYardZone(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		t.Run(zone, func(t *testing.T) {
			testutil.SetBranchDayZone(t, testutil.RequireDB(t), zone)
			outstandingARLeg(t)
		})
	}
}

func outstandingARLeg(t *testing.T) {
	t.Helper()
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	svc := account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default())
	ctx := context.Background()
	today := time.Now()

	yardA, yardB, customer := uuid.New(), uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{yardA, yardB} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, b, "OAR-"+b.String()[:8]); err != nil {
			t.Fatalf("seed yard: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		customer, "Outstanding AR "+customer.String()[:8], "OAR-"+customer.String()[:8], yardA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN
				(SELECT id FROM invoices WHERE customer_id = $1) OR e.source_ref_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)
				OR e.source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)
				OR e.source_ref_id IN (SELECT id FROM ar_applications WHERE customer_id = $1))`,
			`DELETE FROM gl_journal_entries WHERE source_ref_id IN
				(SELECT id FROM invoices WHERE customer_id = $1) OR source_ref_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)
				OR source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)
				OR source_ref_id IN (SELECT id FROM ar_applications WHERE customer_id = $1)`,
			`DELETE FROM ar_applications WHERE customer_id = $1`,
			`DELETE FROM payments WHERE customer_id = $1`,
			`DELETE FROM credit_memo_lines WHERE credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`,
			`DELETE FROM credit_memos WHERE customer_id = $1`,
			`DELETE FROM customer_transactions WHERE customer_id = $1`,
			`DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE customer_id = $1)`,
			`DELETE FROM invoices WHERE customer_id = $1`,
			`DELETE FROM customers WHERE id = $1`,
		} {
			_, _ = db.Pool.Exec(bg, q, customer)
		}
		for _, b := range []uuid.UUID{yardA, yardB} {
			_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, b)
		}
	})

	invoice := func(yard uuid.UUID, totalCents int64) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency,
				total_amount, subtotal, tax_rate, tax_amount, amount_open, due_date, invoice_date)
			VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', $5::bigint::numeric / 100, $5::bigint::numeric / 100,
				0, 0, $5::bigint::numeric / 100, CURRENT_DATE + 30, CURRENT_DATE - 10)`,
			id, customer, yard, "IN-"+id.String()[:8], totalCents); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		err := db.RunInTx(ctx, func(ctx context.Context) error {
			_, e := svc.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: id, CustomerID: customer,
				Number: "IN-" + id.String()[:8], Currency: "USD", TotalCents: totalCents, On: today.AddDate(0, 0, -10), Actor: "u-test",
				Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: totalCents}}})
			return e
		})
		if err != nil {
			t.Fatalf("post invoice: %v", err)
		}
		return id
	}
	pay := func(yard uuid.UUID, cents int64, apps ...account.ApplyLine) {
		t.Helper()
		err := db.RunInTx(ctx, func(ctx context.Context) error {
			_, _, e := svc.RecordPayment(ctx, account.RecordPaymentIn{PaymentID: uuid.New(), CustomerID: customer,
				BranchID: yard, Currency: "USD", Method: "CHECK", AmountCents: cents, ReceivedOn: today, Actor: "u-test",
				Applications: apps})
			return e
		})
		if err != nil {
			t.Fatalf("record payment: %v", err)
		}
	}

	invoice(yardA, 100000)            // unpaid
	partPaid := invoice(yardA, 50000) // part paid below
	pay(yardA, 12000, account.ApplyLine{InvoiceID: partPaid, AmountCents: 12000})
	pay(yardA, 3000) // unapplied cash

	memo := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount,
			subtotal, tax_amount, total_amount, status, memo_date)
		VALUES ($1, $2, $3, 'USD', 'RETURN', 'returned goods', 0, 0, 0, 0, 'DRAFT', CURRENT_DATE)`, memo, customer, yardA); err != nil {
		t.Fatalf("seed memo: %v", err)
	}
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, e := svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: customer, Number: "CM-" + memo.String()[:8],
			Currency: "USD", MemoDate: today.AddDate(0, 0, -5), SubtotalCents: -4000, TotalCents: -4000, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: 4000}}})
		return e
	})
	if err != nil {
		t.Fatalf("post memo: %v", err)
	}

	invoice(yardB, 77700) // another yard: must not count

	// 100000 + (50000 - 12000) - 4000 - 3000
	const want = int64(131000)

	// The aging for the same yard, to the cent.
	agingCtx := middleware.WithBranchContext(ctx, &middleware.BranchContext{BranchID: &yardA, IsAdmin: true})
	aging, err := svc.AgingSummary(agingCtx, account.AgingQuery{AsOf: today, Basis: "due_date"})
	if err != nil {
		t.Fatalf("aging: %v", err)
	}
	if len(aging.Totals) != 1 || int64(aging.Totals[0].TotalCents) != want {
		t.Fatalf("aging totals = %+v, want one USD total of %d", aging.Totals, want)
	}

	summary, err := repo.GetDashboardSummary(ctx, &yardA, nil)
	if err != nil {
		t.Fatalf("GetDashboardSummary: %v", err)
	}
	if summary.OutstandingAR != int64(aging.Totals[0].TotalCents) {
		t.Errorf("OutstandingAR = %d, aging total = %d: the two screens disagree", summary.OutstandingAR, aging.Totals[0].TotalCents)
	}
	if summary.OutstandingAR != want {
		t.Errorf("OutstandingAR = %d, want %d (net open receivable)", summary.OutstandingAR, want)
	}
	if summary.OutstandingARCount != 2 {
		t.Errorf("OutstandingARCount = %d, want 2 open invoices", summary.OutstandingARCount)
	}
}

// RULE: the open receivable's day is the default branch's business date read
// in the database, as the aging's default, not the server process's local day.
// With the server's clock two days behind the database's, an invoice dated the
// branch's today still counts, and the figure still equals the aging's.
func TestGetDashboardSummary_OutstandingARReadsTheBranchDay(t *testing.T) {
	testutil.LockBranchDay(t)
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	repo.now = func() time.Time { return time.Now().AddDate(0, 0, -2) }
	svc := account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default())
	ctx := context.Background()

	var day time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
		WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`).Scan(&day); err != nil {
		t.Fatalf("the branch's day: %v", err)
	}

	yard, customer, inv := uuid.New(), uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, yard, "OAD-"+yard.String()[:8]); err != nil {
		t.Fatalf("seed yard: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		customer, "Branch Day "+customer.String()[:8], "OAD-"+customer.String()[:8], yard); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $2)`,
			`DELETE FROM gl_journal_entries WHERE source_ref_id = $2`,
			`DELETE FROM customer_transactions WHERE customer_id = $1`,
			`DELETE FROM invoices WHERE customer_id = $1`,
			`DELETE FROM customers WHERE id = $1`,
		} {
			_, _ = db.Pool.Exec(bg, q, customer, inv)
		}
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, yard)
	})
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency,
			total_amount, subtotal, tax_rate, tax_amount, amount_open, due_date, invoice_date)
		VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', 250, 250, 0, 0, 250, $5::date + 30, $5::date)`,
		inv, customer, yard, "IN-"+inv.String()[:8], day.Format("2006-01-02")); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		_, e := svc.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: inv, CustomerID: customer, Number: "IN-" + inv.String()[:8],
			Currency: "USD", TotalCents: 25000, On: day, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: 25000}}})
		return e
	}); err != nil {
		t.Fatalf("post invoice: %v", err)
	}

	agingCtx := middleware.WithBranchContext(ctx, &middleware.BranchContext{BranchID: &yard, IsAdmin: true})
	aging, err := svc.AgingSummary(agingCtx, account.AgingQuery{AsOf: day, Basis: "due_date"})
	if err != nil {
		t.Fatalf("aging: %v", err)
	}
	if len(aging.Totals) != 1 || int64(aging.Totals[0].TotalCents) != 25000 {
		t.Fatalf("aging totals = %+v, want one USD total of 25000", aging.Totals)
	}
	summary, err := repo.GetDashboardSummary(ctx, &yard, nil)
	if err != nil {
		t.Fatalf("GetDashboardSummary: %v", err)
	}
	if summary.OutstandingAR != 25000 {
		t.Errorf("OutstandingAR = %d with the server's day behind the branch's, want the aging's 25000", summary.OutstandingAR)
	}
}
