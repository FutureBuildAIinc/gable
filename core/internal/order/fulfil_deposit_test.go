// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Deposit application at fulfilment (ADR 0005 5.6 step 7, 14.2 C2-4) on the
// wire: an order with a posted payment against it is fulfilled through the
// route and the invoice, the payment, the ledger and the event feed are read
// back. The account core's own test calls ApplyDeposits directly; this one
// proves the fulfilment calls it.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// deposit records cash against an order through the AR core, as the payment
// route does with an order_id and no invoice.
func (f *fixture) deposit(orderID string, cents int64) uuid.UUID {
	f.t.Helper()
	ctx := context.Background()
	oid := uuid.MustParse(orderID)
	var branch uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		f.t.Fatal(err)
	}
	acct := account.NewService(f.db, gl.NewService(gl.NewRepository(f.db), nil, slog.Default()), slog.Default())
	var id uuid.UUID
	err := f.db.RunInTx(ctx, func(ctx context.Context) error {
		var e error
		id, _, e = acct.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: f.customerID, BranchID: branch, Currency: "USD",
			Method: "CASH", AmountCents: cents, ReceivedOn: time.Now(), OrderID: &oid, Actor: "u-test"})
		return e
	})
	if err != nil {
		f.t.Fatalf("record the deposit: %v", err)
	}
	return id
}

func (f *fixture) cleanDeposits() {
	ctx := context.Background()
	f.t.Cleanup(func() {
		entries := `SELECT e.id FROM gl_journal_entries e WHERE e.source_ref_id IN (SELECT id FROM payments WHERE customer_id = $1)
			OR e.source_ref_id IN (SELECT id FROM ar_applications WHERE customer_id = $1)`
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (`+entries+`)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE id IN (`+entries+`)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type IN ('payment', 'customer') AND entity_id IN
			(SELECT id FROM payments WHERE customer_id = $1 UNION SELECT $1::uuid)`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM ar_applications WHERE customer_id = $1`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM payments WHERE customer_id = $1`, f.customerID)
	})
}

func (f *fixture) paymentRead(id uuid.UUID) (status string, unapplied int64) {
	f.t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status, ROUND(amount_unapplied * 100)::bigint FROM payments WHERE id = $1`, id).Scan(&status, &unapplied); err != nil {
		f.t.Fatal(err)
	}
	return
}

func (f *fixture) invoiceRead(id string) (status string, open int64) {
	f.t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status, ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, id).Scan(&status, &open); err != nil {
		f.t.Fatal(err)
	}
	return
}

// applicationLegs reads the legs of the entries the payment's applications
// posted (cents, debit less credit by account).
func applicationLegs(t *testing.T, db *database.DB, paymentID uuid.UUID) string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `
		SELECT a.code, ROUND(SUM(l.debit - l.credit) * 100)::bigint FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id IN (SELECT id FROM ar_applications WHERE payment_id = $1) GROUP BY a.code ORDER BY a.code`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var code string
		var net int64
		if err := rows.Scan(&code, &net); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", code, net))
	}
	return strings.Join(out, ",")
}

// RULE: a fulfilment applies the order's deposits to the invoice it just
// billed. A deposit smaller than the invoice leaves it partial and the payment
// fully applied; one larger than the invoice pays it and keeps the rest
// unapplied; each application posts its own 2200 / 1020 entry and raises
// payment.applied with the invoice's own event.
func TestFulfilmentAppliesTheOrdersDeposits(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	for _, tc := range []struct {
		name                string
		deposit             int64
		invoiceStatus       string
		invoiceOpen         int64
		unapplied           int64
		applied             string
		wantInvoiceEvents   string
		wantPaymentEvents   string
		wantBalance         string
		wantApplicationLegs string
	}{
		{name: "smaller than the invoice", deposit: 4000, invoiceStatus: "PARTIAL", invoiceOpen: 1988, unapplied: 0,
			wantInvoiceEvents: "[invoice.created invoice.partial]", wantPaymentEvents: "[payment.applied]",
			wantBalance: "1988", wantApplicationLegs: "1020=-4000,2200=4000"},
		{name: "larger than the invoice", deposit: 9000, invoiceStatus: "PAID", invoiceOpen: 0, unapplied: 3012,
			wantInvoiceEvents: "[invoice.created invoice.paid]", wantPaymentEvents: "[payment.applied]",
			wantBalance: "0", wantApplicationLegs: "1020=-5988,2200=5988"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, db)
			f.serveWith(f.withStock(), f.withMoney())
			f.cleanMoney()
			f.cleanDeposits()
			f.stock(f.productID, "10")
			f.setCost(f.productID, "3.2500")

			r := f.create() // 10 PCS pickup at 5.50 = 5500, tax 8.875% = 488, total 5988
			id := str(t, r.body, "id")
			r = f.transition(id, 1, "confirmed")
			pay := f.deposit(id, tc.deposit)
			if _, unapplied := f.paymentRead(pay); unapplied != tc.deposit {
				t.Fatalf("setup: the deposit has %d unapplied, want %d", unapplied, tc.deposit)
			}

			r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
			if r.status != 201 {
				t.Fatalf("fulfil = %d: %s", r.status, r.raw)
			}
			inv := invoiceIDOf(t, r)
			if status, open := f.invoiceRead(inv); status != tc.invoiceStatus || open != tc.invoiceOpen {
				t.Errorf("invoice = %s open %d, want %s open %d", status, open, tc.invoiceStatus, tc.invoiceOpen)
			}
			if status, unapplied := f.paymentRead(pay); status != "POSTED" || unapplied != tc.unapplied {
				t.Errorf("payment = %s unapplied %d, want POSTED %d", status, unapplied, tc.unapplied)
			}
			if got := applicationLegs(t, db, pay); got != tc.wantApplicationLegs {
				t.Errorf("the application's entry nets %s, want %s", got, tc.wantApplicationLegs)
			}
			var apps int
			if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ar_applications WHERE payment_id = $1 AND invoice_id = $2 AND reversed_at IS NULL`, pay, inv).Scan(&apps); err != nil || apps != 1 {
				t.Errorf("%d live applications of the deposit to the invoice (%v), want 1", apps, err)
			}
			var bal string
			if err := db.Pool.QueryRow(context.Background(), `SELECT ROUND(balance_due * 100)::bigint::text FROM customers WHERE id = $1`, f.customerID).Scan(&bal); err != nil || bal != tc.wantBalance {
				t.Errorf("balance_due cents = %s (%v), want %s", bal, err, tc.wantBalance)
			}
			if ev := eventsForEntity(t, db, "invoice", inv); fmt.Sprint(ev) != tc.wantInvoiceEvents {
				t.Errorf("invoice events = %v, want %s", ev, tc.wantInvoiceEvents)
			}
			if ev := eventsForEntity(t, db, "payment", pay.String()); fmt.Sprint(ev) != tc.wantPaymentEvents {
				t.Errorf("payment events = %v, want %s", ev, tc.wantPaymentEvents)
			}
		})
	}
}
