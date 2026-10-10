// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The gateway order (ADR 0005 section 14.2 C2-5, first review P1-2, second
// review P1-5): every guard that needs no lock (the revision, the status,
// the session, the returns) runs BEFORE any gateway call, so a refusal never
// moves money at the terminal; and when the transaction then refuses a
// gateway success, the charge is put back and the refund is recorded in a
// committed row of its own carrying the gateway id.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: a stale revision void makes zero gateway refunds and leaves the sale
// completed.
func TestStaleRevisionVoidMakesNoGatewayRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stale", "revision": rev(t, body) + 99},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("stale void = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	if len(gw.refunds) != 0 {
		t.Errorf("%d gateway refunds on a stale revision void, want 0", len(gw.refunds))
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed", got)
	}
	if got := f.accountBalance("1010"); got != 2395 {
		t.Errorf("card balance = %d, want 2395 (the completed sale keeps its charge; no refund was made)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a void whose transaction fails after the gateway refund leaves the
// money accounted for: the refund is recorded in its own committed audit row
// carrying the gateway id, while the sale stands as it was.
func TestAVoidTheTransactionRefusesRecordsItsGatewayRefund(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.voided"
	})
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "event fails", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	if len(gw.refunds) != 1 {
		t.Fatalf("%d gateway refunds, want 1 (the refund was made before the transaction)", len(gw.refunds))
	}
	// The refund is accounted for in a row of its own, committed though the
	// void's transaction rolled back, carrying the gateway's refund id.
	n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned'
		AND entity_id = $1 AND changes->>'gateway_refund_id' = 'ref-1'`, saleID)
	if n != 1 {
		t.Errorf("%d orphaned refund audit rows, want 1 carrying the gateway refund id", n)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void rolled back)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a completion whose transaction fails after the gateway charge gives
// the charge back (the same day void first, the refund when the void is
// refused), leaving no charge behind a sale that never was.
func TestAChargeTheTransactionRefusesIsReversed(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.completed"
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2395)))
	if r.status == http.StatusOK {
		t.Fatalf("the sale completed under a failing event write: %s", r.raw)
	}
	if gw.chargeCalls != 1 {
		t.Fatalf("%d gateway charges, want 1", gw.chargeCalls)
	}
	if len(gw.voids) != 1 {
		t.Errorf("%d gateway voids, want 1 (the charge the refused sale left behind is reversed)", len(gw.voids))
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.card_charge_reversal' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d charge reversal audit rows, want 1", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open (the completion rolled back)", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); got != 0 {
		t.Errorf("%d tenders after the refused completion, want 0", got)
	}
	f.assertARInvariants(t)
}
