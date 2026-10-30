// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// A statement dates each row from its document, not from the moment the
// subledger row was written (G1 walk note N13).

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/google/uuid"
)

func TestARStatementDatesEachRowFromItsDocument(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	invDay, payDay := today.AddDate(0, 0, -40), today.AddDate(0, 0, -10)
	inv := w.invoice(12000, invDay)
	w.payment(12000, payDay, "CHECK", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 12000})
	ctx := context.Background()

	// A period that ends before the invoice's day leaves it out, though its
	// subledger rows were stamped inside the period.
	w.exec(`UPDATE customer_transactions SET created_at = $2::date + TIME '12:00' WHERE customer_id = $1`, w.customerID, date(today.AddDate(0, 0, -55)))
	st, err := w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -60), To: today.AddDate(0, 0, -50)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Currencies {
		if len(c.Lines) != 0 || c.OpeningBalanceCents != 0 || c.ClosingBalanceCents != 0 {
			t.Errorf("a period before the invoice lists %d rows, opening %d, closing %d", len(c.Lines), c.OpeningBalanceCents, c.ClosingBalanceCents)
		}
	}

	// A period over the whole history: each row carries its document's day.
	st, err = w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -60), To: today})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Currencies) != 1 || len(st.Currencies[0].Lines) != 2 {
		t.Fatalf("statement = %+v, want one currency of two rows", st.Currencies)
	}
	lines := st.Currencies[0].Lines
	if lines[0].Date != date(invDay) || lines[1].Date != date(payDay) {
		t.Errorf("row days %s, %s; want the invoice's %s and the payment's %s", lines[0].Date, lines[1].Date, date(invDay), date(payDay))
	}

	// A period that starts between the two: the invoice is the opening balance.
	st, err = w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -20), To: today})
	if err != nil {
		t.Fatal(err)
	}
	c := st.Currencies[0]
	if c.OpeningBalanceCents != 12000 || len(c.Lines) != 1 || c.ClosingBalanceCents != 0 {
		t.Errorf("opening %d, %d rows, closing %d; want 12000, 1, 0", c.OpeningBalanceCents, len(c.Lines), c.ClosingBalanceCents)
	}
}

// The other kinds of row take their document's day too: a credit memo, its
// refund, a reversed application, and a row with no document at all keeps its
// stamp.
func TestARStatementDatesMemoRefundReversalAndLegacyRows(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	ctx := context.Background()
	inv := w.invoice(10000, today.AddDate(0, 0, -40))
	memo := w.draftMemo()
	w.mustAct("post the memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: w.customerID, Number: "CM-" + memo.String()[:8],
			Currency: "USD", MemoDate: today.AddDate(0, 0, -30), SubtotalCents: -3000, TotalCents: -3000, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: 3000}}})
	})
	w.mustAct("refund the memo", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RefundCreditMemo(ctx, account.RefundCreditMemoIn{MemoID: memo, AmountCents: 1000, Reason: "back", Actor: "u-test",
			On: today.AddDate(0, 0, -20)})
		return fx, e
	})
	w.payment(10000, today.AddDate(0, 0, -15), "CHECK", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 10000})
	app := w.appID(inv.ID, "PAYMENT")
	w.mustAct("reverse", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app, Reason: "bounced", Actor: "u-test", On: today.AddDate(0, 0, -10)})
	})
	// a legacy row: no source kind, no document, its stamp is its day
	w.exec(`INSERT INTO customer_transactions (id, customer_id, type, amount, balance_after, description, currency, created_at)
		VALUES ($1, $2, 'ADJUSTMENT', 0, 0, 'legacy', 'USD', ($3::date + TIME '12:00') AT TIME ZONE
			COALESCE((SELECT l.timezone FROM locations l WHERE l.id = `+w.branchSQL()+`), 'UTC'))`, uuid.New(), w.customerID, date(today.AddDate(0, 0, -5)))
	// every stamp is the present, as the acts write them
	w.exec(`UPDATE customer_transactions SET created_at = clock_timestamp() WHERE customer_id = $1 AND description <> 'legacy'`, w.customerID)

	st, err := w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -60), To: today})
	if err != nil {
		t.Fatal(err)
	}
	days := map[string]string{}
	for _, l := range st.Currencies[0].Lines {
		days[string(l.Type)] = l.Date
	}
	want := map[string]string{
		"INVOICE": date(today.AddDate(0, 0, -40)), "CREDIT_MEMO": date(today.AddDate(0, 0, -30)),
		"REFUND": date(today.AddDate(0, 0, -20)), "PAYMENT": date(today.AddDate(0, 0, -15)),
		"REVERSAL": date(today.AddDate(0, 0, -10)), "ADJUSTMENT": date(today.AddDate(0, 0, -5)),
	}
	for k, v := range want {
		if days[k] != v {
			t.Errorf("%s row is dated %q, want %s", k, days[k], v)
		}
	}
	for i := 1; i < len(st.Currencies[0].Lines); i++ {
		if st.Currencies[0].Lines[i].Date < st.Currencies[0].Lines[i-1].Date {
			t.Errorf("rows out of day order at %d", i)
		}
	}
}
