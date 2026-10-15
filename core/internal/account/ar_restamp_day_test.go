// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// RestampDay puts a payment received on its invoice's own day after the
// invoice, so a statement orders the application behind its invoice, and
// names the invoice's day in its refusal of a day to come.
func TestARRestampDayOrdersAPaymentAfterItsInvoiceOnTheSameDay(t *testing.T) {
	w := newARWorld(t)
	day := time.Now().UTC().AddDate(0, 0, -3)
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, time.UTC)
	inv := w.invoice(8000, day)
	w.payment(8000, day, "CHECK", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 8000})
	var pay uuid.UUID
	if err := w.db.Pool.QueryRow(context.Background(), `SELECT payment_id FROM ar_applications WHERE invoice_id = $1`, inv.ID).Scan(&pay); err != nil {
		t.Fatal(err)
	}
	// the seed hands the same moment for both documents of one day
	w.mustAct("restamp", func(ctx context.Context) (*account.Effects, error) {
		return &account.Effects{}, w.svc.RestampDay(ctx, inv.ID, pay, noon, noon)
	})
	var invAt, payAt time.Time
	if err := w.db.Pool.QueryRow(context.Background(), `
		SELECT (SELECT created_at FROM customer_transactions WHERE source_kind = 'invoice' AND reference_id = $1),
		       (SELECT t.created_at FROM customer_transactions t JOIN ar_applications a ON a.id = t.reference_id
		        WHERE t.source_kind = 'application' AND a.invoice_id = $1)`, inv.ID).Scan(&invAt, &payAt); err != nil {
		t.Fatal(err)
	}
	if !payAt.After(invAt) {
		t.Errorf("the payment's row is stamped %s, the invoice's %s: the payment must follow its invoice", payAt, invAt)
	}

	// a day to come is named under the invoice's day, not the payment's
	err := w.db.RunInTx(context.Background(), func(ctx context.Context) error {
		return w.svc.RestampDay(ctx, inv.ID, uuid.Nil, time.Now().UTC().AddDate(0, 0, 400), time.Time{})
	})
	var he *httpx.Error
	if !errors.As(err, &he) || len(he.Details) != 1 || he.Details[0].Field != "invoiced_at" {
		t.Errorf("a future invoice day was refused as %v, want the field invoiced_at", err)
	}
}
