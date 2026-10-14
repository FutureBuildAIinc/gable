// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// A statement dates each row from its document, not from the moment the
// subledger row was written (G1 walk note N13).

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/account"
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
