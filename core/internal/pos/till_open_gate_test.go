// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-08: the API itself refuses a counter sale at a register with no open
// till session. The desk already gates the screen; a script or a second
// client must meet the same wall.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func (f *fixture) startSaleNoTill() resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// RULE: a sale cannot start at a register with no open till.
func TestASaleCannotStartWithoutAnOpenTill(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withoutTill())
	r := f.startSaleNoTill()
	if r.status != http.StatusConflict {
		t.Fatalf("start with no till = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "till_not_open" {
		t.Errorf("blockers = %v, want till_not_open", blockers)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pos_transactions WHERE register_id = $1`, f.register).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d sales written (%v), want none", n, err)
	}
	f.openTill(0)
	if r := f.startSaleNoTill(); r.status != http.StatusCreated {
		t.Fatalf("start with a till = %d: %s", r.status, r.raw)
	}
}

// RULE: a cart that never got a drawer cannot complete either; with the
// drawer open it completes into it.
func TestACartWithNoDrawerCannotComplete(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withoutTill())
	sessionID := f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// a cart left from before the rule: no drawer on it, and the drawer is closed
	mustExec(t, f.db, `UPDATE pos_transactions SET till_session_id = NULL WHERE id = $1`, saleID)
	if r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, tender("cash", 599))
	if r.status != http.StatusConflict {
		t.Fatalf("complete with no drawer = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "till_not_open" {
		t.Errorf("blockers = %v, want till_not_open", blockers)
	}
	// the drawer opens again: the cart completes into it and counts there
	second := f.openTill(0)
	r = f.completeSale(saleID, tender("cash", 599))
	if r.status != http.StatusOK {
		t.Fatalf("complete into the open drawer = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "till_session_id"); got != second {
		t.Errorf("till_session_id = %s, want %s", got, second)
	}
}
