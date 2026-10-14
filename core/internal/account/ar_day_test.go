// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The business day of the AR core's money acts (G1 walk note N12): money is
// never dated after the branch's business day.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
)

// businessDay is the branch's business day at the service clock.
func (w *arWorld) businessDay() time.Time {
	w.t.Helper()
	d, err := w.svc.LocalDate(context.Background(), w.branchID, w.svc.Now())
	if err != nil {
		w.t.Fatal(err)
	}
	return d
}

func TestARRefusesAPaymentReceivedOnADayToCome(t *testing.T) {
	w := newARWorld(t)
	tomorrow := w.businessDay().AddDate(0, 0, 1)
	_, err := w.act("record", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customerID, BranchID: w.branchID,
			Currency: "USD", Method: "CHECK", AmountCents: 5000, ReceivedOn: tomorrow, Actor: "u-test"})
		return fx, e
	})
	if err == nil {
		t.Fatal("a payment received tomorrow was recorded")
	}
	if n := w.queryInt(`SELECT COUNT(*) FROM payments WHERE customer_id = $1`, w.customerID); n != 0 {
		t.Errorf("%d payments were written by a refused act", n)
	}
}

func TestARRefusesAnApplicationOnADayToCome(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	inv := w.invoice(10000, today.AddDate(0, 0, -5))
	pay := w.payment(10000, today.AddDate(0, 0, -1), "CHECK")
	tomorrow := today.AddDate(0, 0, 1)
	_, err := w.act("apply", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 10000}},
			On: tomorrow, Actor: "u-test"})
	})
	if err == nil {
		t.Fatal("an application dated tomorrow was applied")
	}
	if status, _ := w.invoiceRow(inv.ID); status != "UNPAID" {
		t.Errorf("invoice is %s after a refused application", status)
	}
}
