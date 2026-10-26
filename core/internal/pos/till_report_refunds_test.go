// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The X report's refunds figure (CP-08 slice 8): a mid-shift read of the
// drawer names what the session refunded by method beside the taken and
// expected figures, cash out of the drawer under CASH and back to the card
// under CARD, so the X report a manager prints answers "refunds" without a
// second read. A cashier's read of the open drawer stays blind to the
// refunds as to every money figure (P2-1), and the figure survives the
// close into the frozen Z snapshot.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: a cash return's refund out of the drawer is named under CASH in the
// report's refunds_by_method, and the expected cash already nets it.
func TestTheTillReportNamesTheCashRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(10000)
	saleID, body := f.saleOf("2", tender("cash", 1198))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	// one unit comes back, refunded out of the drawer
	if r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": "cash",
		"reason": "wrong length", "lines": []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusCreated {
		t.Fatalf("the cash return = %d: %s", r.status, r.raw)
	}

	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the manager's report = %d: %s", r.status, r.raw)
	}
	refunds, ok := r.body["refunds_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("the report carries no refunds_by_method: %s", r.raw)
	}
	if got := int64Of(refunds["CASH"]); got != 599 {
		t.Errorf("refunds CASH = %d, want 599 (the one unit back out of the drawer)", got)
	}
	if _, ok := refunds["CARD"]; ok {
		t.Errorf("a session with no card refunds carries a CARD refunds figure (%v), want none", refunds)
	}
	if got := f.expectedOf(t, sessionID, "CASH"); got != 10000+1198-599 {
		t.Errorf("expected CASH = %d, want %d (the float and the sale less the refund)", got, 10000+1198-599)
	}

	// the figure survives the close into the frozen Z snapshot
	if cr := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 10000 + 1198 - 599},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); cr.status != http.StatusOK {
		t.Fatalf("the close = %d: %s", cr.status, cr.raw)
	}
	zr := f.do("GET", "/api/v1/pos/till/"+sessionID+"/zreport", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if zr.status != http.StatusOK {
		t.Fatalf("the Z report = %d: %s", zr.status, zr.raw)
	}
	payload, ok := zr.body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("the Z report carries no payload: %s", zr.raw)
	}
	zRefunds, ok := payload["refunds_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("the frozen report carries no refunds_by_method: %s", zr.raw)
	}
	if got := int64Of(zRefunds["CASH"]); got != 599 {
		t.Errorf("the frozen report's refunds CASH = %d, want 599", got)
	}
	f.assertARInvariants(t)
}

// RULE: a card return's refund is named under CARD, and a cashier's read of
// the open drawer carries no refunds figure at all (the blind count rule
// redacts it with the rest of the money figures).
func TestTheTillReportNamesTheCardRefundsAndStaysBlindForTheCashier(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(10000)
	saleID, body := f.saleOf("3", withToken(tender("card", 1796)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the card return = %d: %s", r.status, r.raw)
	}

	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the manager's report = %d: %s", r.status, r.raw)
	}
	refunds, ok := r.body["refunds_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("the report carries no refunds_by_method: %s", r.raw)
	}
	if got := int64Of(refunds["CARD"]); got != 599 {
		t.Errorf("refunds CARD = %d, want 599 (the one unit back to the card)", got)
	}
	if _, ok := refunds["CASH"]; ok {
		t.Errorf("a session with no cash refunds carries a CASH refunds figure (%v), want none", refunds)
	}

	// the cashier's read of the open drawer is blind to the refunds too
	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the cashier's report = %d: %s", r.status, r.raw)
	}
	if refunds, ok := r.body["refunds_by_method"].(map[string]any); !ok || len(refunds) != 0 {
		t.Errorf("the cashier's refunds_by_method = %v, want it empty while the drawer is open", r.body["refunds_by_method"])
	}
	f.assertARInvariants(t)
}
