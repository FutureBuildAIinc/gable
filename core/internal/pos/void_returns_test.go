// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A void after any return of the sale is refused (ADR 0005 section 14.2
// C2-5, second review P1-2): the return already paid part of the money out
// and put its goods back, so a void would pay the whole sale a second time
// and restock the returned units again. The rest is returned instead.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestVoidAfterAReturnIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("4", tender("cash", 2395))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.returnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("return of 1 = %d: %s", r.status, r.raw)
	}
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Fatalf("stock = %s, want 97 after the return", got)
	}
	// The void, on the fresh revision the return left, is refused: after a
	// return the way back is another return.
	cur := f.getSale(t, saleID)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "second payout", "revision": num(t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("void after a return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "has_returns" {
		t.Errorf("blockers = %v, want has_returns", blockers)
	}
	// Nothing moved: the sale stands completed, the stock keeps the returned
	// unit, the drawer keeps its one bounded refund.
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed", got)
	}
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97 (the void restocked nothing)", got)
	}
	if got := f.accountBalance("1010"); got != 2395-599 {
		t.Errorf("cash balance = %d, want %d (the sale in, the one refund out)", got, 2395-599)
	}
	if got := f.accountBalance("1030"); got != -975 {
		t.Errorf("inventory balance = %d, want -975 (3 units still out at 3.25, credited on the sale)", got)
	}
	f.assertARInvariants(t)
}
