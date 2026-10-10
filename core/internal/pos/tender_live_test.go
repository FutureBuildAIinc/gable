// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The tender is checked against the live total (ADR 0005 section 14.2 C2-5,
// first review P1-1, second review P1-4): once the tax is known inside the
// transaction, the change is the tenders less the total, an under tender is
// refused insufficient_tender and a change above the cash taken is refused
// change_from_cash. The pre-transaction estimate prices with the same
// resolver the transaction uses (exemption first, then the provider or the
// branch rate), so an exempt customer can pay exactly.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/testutil"
)

// stubTaxProvider answers the provider path with a fixed tax.
type stubTaxProvider struct{ taxCents int64 }

func (p *stubTaxProvider) ProviderConfigured() bool { return true }

func (p *stubTaxProvider) PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	return &tax.TaxResult{TotalTax: p.taxCents}, nil
}

// RULE: a provider tax above the branch rate estimate refuses the under
// tender against the live total, and nothing is written.
func TestProviderTaxAboveTheEstimateRefusesTheUnderTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service.WithTaxProvider(&stubTaxProvider{taxCents: 400}) // the branch rate says 195
	})
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	invoicesBefore := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`)
	paymentsBefore := countOf(t, f.db, `SELECT count(*) FROM payments`)
	r := f.completeSale(saleID, tender("cash", 2395)) // total 2600 at the provider's tax
	if r.status != http.StatusConflict {
		t.Fatalf("under tender at the provider tax = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "insufficient_tender" {
		t.Errorf("blockers = %v, want insufficient_tender", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != invoicesBefore {
		t.Errorf("%d invoices after the refusal, want %d", got, invoicesBefore)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payments`); got != paymentsBefore {
		t.Errorf("%d payments after the refusal, want %d", got, paymentsBefore)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); got != 0 {
		t.Errorf("%d tenders after the refusal, want 0", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open", got)
	}
	f.assertARInvariants(t)
}

// RULE: an exempt customer pays exactly what the exempt total asks, with no
// tax estimate standing in the way.
func TestAnExemptCustomerPaysExactly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(&fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}})
	})
	mustExec(t, f.db, `INSERT INTO tax_exemptions (customer_id, exempt_reason, certificate_number)
		VALUES ($1, 'RESALE', 'CERT-1')`, f.customerID)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2200))) // exempt: no tax, exact
	if r.status != http.StatusOK {
		t.Fatalf("exempt exact card = %d, want 200: %s", r.status, r.raw)
	}
	for key, want := range map[string]int64{"tax_cents": 0, "total_cents": 2200, "change_cents": 0} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	pays := f.paymentsOf(saleID)
	if len(pays) != 1 || pays[0].cents != 2200 {
		t.Errorf("card payment = %+v, want one of 2200", pays)
	}
	f.assertARInvariants(t)
}

// RULE: a provider tax below the estimate gives the change from the live
// total, and the drawer expects the money actually kept.
func TestProviderTaxBelowTheEstimateGivesChangeFromTheLiveTotal(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service.WithTaxProvider(&stubTaxProvider{taxCents: 100}) // the branch rate says 195
	})
	f.openTill(1000)
	saleID, body := f.saleOf("4", tender("cash", 2395)) // live total 2300, change 95
	if got := num(t, body.body, "change_cents"); got != 95 {
		t.Errorf("change_cents = %d, want 95 (the tenders less the live total)", got)
	}
	pays := f.paymentsOf(saleID)
	if len(pays) != 1 || pays[0].cents != 2300 {
		t.Errorf("cash payment = %+v, want one of 2300 (net of the 95 change)", pays)
	}
	report := f.tillReport(t)
	if v := int64Of(report["expected_by_method"].(map[string]any)["CASH"]); v != 1000+2300 {
		t.Errorf("expected CASH = %d, want 3300 (float plus the net payment)", v)
	}
	f.assertARInvariants(t)
}
