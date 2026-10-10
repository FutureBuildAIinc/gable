// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The branch wall covers the register, the session and the till routes (ADR
// 0002 section 6, second review P2-3): a cashier granted only branch B
// cannot take a free return on branch A's register, nor read or close A's
// drawer. The sale, void and linked return routes already carried the wall
// through the sale row; the register and session reads now carry it too.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

func TestTheBranchWallCoversTheFreeReturnAndTheTill(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// the kill switch is read when the fixture's middleware is built
	setPosSetting(t, db, "multi_branch_enabled", "true")
	branchB := uuid.New()
	mustExecB := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(contextB(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExecB(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-POS-B', 'wall branch B')`, branchB)
	mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-wall-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		mustExecB(`DELETE FROM user_locations WHERE user_sub = 'pos-wall-b' AND branch_id = $1`, branchB)
		mustExecB(`DELETE FROM locations WHERE id = $1`, branchB)
	})

	f := newFixture(t, db)
	// a drawer open on the fixture's own branch (the default branch, A),
	// opened by a caller with no claims: the middleware's dev fallback,
	// permissive with no branch context
	if r := f.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": f.register, "opening_float_cents": 0}); r.status != http.StatusCreated {
		t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	sessionID := f.currentTillID(t)
	// the B-cashier's headers: the branch middleware scopes them to branch B
	scoped := []string{"X-Test-Role", "cashier", "X-Test-Sub", "pos-wall-b", "X-Branch-Id", branchB.String()}

	// a free cash return on branch A's register: 404, not a payout
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "cash",
		"reason": "out of my branch", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000, "restock": false,
		}},
	}, scoped...)
	if r.status != http.StatusNotFound {
		t.Errorf("free return on another branch's register = %d, want 404: %s", r.status, r.raw)
	}
	// branch A's till report: 404
	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, scoped...)
	if r.status != http.StatusNotFound {
		t.Errorf("another branch's till report = %d, want 404: %s", r.status, r.raw)
	}
	// branch A's till close: 404, and the session still open
	r = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 0}}, scoped...)
	if r.status != http.StatusNotFound {
		t.Errorf("another branch's till close = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, sessionID); got != 1 {
		t.Errorf("the session was closed through the wall")
	}
	// the same routes inside the caller's own branch still work: an unscoped
	// caller (no branch context) reads and closes the drawer
	r = f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil)
	if r.status != http.StatusOK {
		t.Errorf("unscoped till report = %d, want 200: %s", r.status, r.raw)
	}
}

func contextB() context.Context { return context.Background() }

// setPosSetting upserts a system_settings row for the wall test.
func setPosSetting(t *testing.T, db *database.DB, key, value string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO system_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}
