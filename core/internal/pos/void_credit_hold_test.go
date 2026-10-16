// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The declined void tender's credit is held to its own tender
// (FROM-PRINCIPAL 60a): a tender whose refund the terminal declined keeps
// its charge as unapplied credit on the shared walk-in customer once the
// void completes, and that credit may never be spent on another invoice.
// The payment apply route answered 200 for it (the fourteenth review's
// Area 5 shape: a later walk-in invoice absorbed an earlier walk-in's
// declined card credit), so the refusal is proved on an open walk-in
// invoice no void holds, the sharpest case the shared customer allows.

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// openWalkInInvoice inserts and posts an open invoice of total cents for
// the shared walk-in customer, the AR desk's own document: no sale backs
// it, so no void claim can hold it, and its subledger row keeps the ties.
func (f *fixture) openWalkInInvoice(t *testing.T, total int64) string {
	t.Helper()
	walkIn := f.scalar(`SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`).(string)
	id := uuid.New()
	number := "IN-" + id.String()[:8]
	mustExec(t, f.db, `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency, total_amount, subtotal,
			tax_rate, tax_amount, amount_open, due_date, invoice_date)
		VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', $5::bigint::numeric / 100, $5::bigint::numeric / 100,
			0, 0, $5::bigint::numeric / 100, CURRENT_DATE + 30, CURRENT_DATE - 5)`, id, walkIn, f.branchID, number, total)
	acct := account.NewService(f.db, gl.NewService(gl.NewRepository(f.db), nil, slog.Default()), slog.Default())
	if err := f.db.RunInTx(context.Background(), func(ctx context.Context) error {
		// the branch's own day: the core refuses a day that has not come there
		day, err := acct.LocalDate(ctx, f.branchID, time.Now())
		if err != nil {
			return err
		}
		_, err = acct.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: id, CustomerID: uuid.MustParse(walkIn),
			Number: number, Currency: "USD", TotalCents: total, On: day, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: total}}})
		return err
	}); err != nil {
		t.Fatalf("post the open walk-in invoice: %v", err)
	}
	return id.String()
}

// invoiceOpenOf reads the invoice's open cents.
func (f *fixture) invoiceOpenOf(t *testing.T, invoiceID string) int64 {
	t.Helper()
	var open int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, invoiceID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	return open
}

// RULE (FROM-PRINCIPAL 60a): a walk-in sale paid by two cards whose void
// refunded one and declined the other leaves the declined card's charge as
// unapplied credit on the shared walk-in customer; the payment apply route
// applied it to another walk-in invoice, so the refusal must name the
// credit's origin (the void and its claim) and nothing may be written: the
// credit stays on its own payment, held to its tender.
func TestADeclinedVoidTendersCreditAppliesToNoInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(10000)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "declined card", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the void with one tender declined = %d, want 409: %s", r.status, r.raw)
	}
	var standing, declined map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["state"] == "PENDING" {
			standing = in
		}
		if in["state"] == "FAILED" {
			declined = in
		}
	}
	if standing == nil || declined == nil {
		t.Fatalf("the claims = %v, want one PENDING and one FAILED", f.saleIntents(t, saleID))
	}
	if r := f.resolveOn(t, standing["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": standing["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the resolution of the refunded tender = %d, want 200: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("sale status = %q, want voided", got)
	}
	credit := f.unappliedOf(t, declined["tender_id"].(string))
	if credit != declined["amount"].(int64) {
		t.Fatalf("the declined tender's payment holds %d unapplied cents, want %d (the credit owed)", credit, declined["amount"].(int64))
	}
	f.assertARInvariants(t)

	// an open walk-in invoice no void holds: the only invoice the shared
	// customer's credit could be spent on
	invoiceID := f.openWalkInInvoice(t, 500)
	payID, payRev := f.paymentOfTender(t, declined["tender_id"].(string))
	r := f.do("POST", "/api/v1/payments/"+payID+"/applications", map[string]any{
		"revision": payRev,
		"applications": []map[string]any{
			{"invoice_id": invoiceID, "amount_cents": credit},
		},
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the credit applied to another walk-in invoice = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "tender_credit_held" {
		t.Errorf("blockers = %v, want tender_credit_held (the credit is held to its tender)", blockers)
	}
	// the refusal names the origin: the void and the claim it released
	if !containsClaim(r.raw, declined["id"].(string)) {
		t.Errorf("the refusal does not name the declined claim %s: %s", declined["id"], r.raw)
	}
	var saleNumber string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT number FROM pos_transactions WHERE id = $1`, saleID).Scan(&saleNumber); err != nil {
		t.Fatal(err)
	}
	if !containsClaim(r.raw, saleNumber) {
		t.Errorf("the refusal does not name the void's sale %s: %s", saleNumber, r.raw)
	}
	// nothing was written: the credit stays on its own payment, the invoice
	// stays open, and no application row exists
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != credit {
		t.Errorf("the credit after the refused apply = %d, want %d (nothing was absorbed)", got, credit)
	}
	if got := f.invoiceOpenOf(t, invoiceID); got != 500 {
		t.Errorf("the invoice's open cents after the refused apply = %d, want 500", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM ar_applications WHERE payment_id = $1 AND reversed_at IS NULL`, payID); got != 0 {
		t.Errorf("%d live application rows for the held credit's payment, want 0", got)
	}
	if got := f.walkInBalance(t); got != 500 {
		t.Errorf("walk-in balance = %d, want 500 (the open invoice only; the credit owes nothing)", got)
	}
	f.assertARInvariants(t)
}
