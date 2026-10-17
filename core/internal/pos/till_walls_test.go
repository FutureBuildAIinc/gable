// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The branch wall on the till's new routes (CP-08, CP-09): a caller held to
// branch B finds branch A's drawer a missing record, a caller with no branch
// header and a grant on B alone is refused by name, and nothing is written.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// wallFixture is a fixture on the default branch (A) with a second branch B
// and a cashier granted B only, multi branch on.
type wallFixture struct {
	*fixture
	branchB uuid.UUID
	scoped  []string // headers: the B cashier naming branch B
	noHdr   []string // headers: the B cashier naming no branch
}

func newWallFixture(t *testing.T) *wallFixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	sub := "pos-till-wall-" + branchB.String()[:8]
	mustExec(t, db, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'till wall branch B')`, branchB, "TW-"+branchB.String()[:8])
	mustExec(t, db, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		mustExec(t, db, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		mustExec(t, db, `DELETE FROM locations WHERE id = $1`, branchB)
	})
	f := newFixture(t, db)
	return &wallFixture{fixture: f, branchB: branchB,
		scoped: []string{"X-Test-Role", "cashier", "X-Test-Sub", sub, "X-Branch-Id", branchB.String()},
		noHdr:  []string{"X-Test-Role", "cashier", "X-Test-Sub", sub}}
}

// openDrawerA opens branch A's drawer with no claims (the middleware's dev
// fallback, permissive with no branch context), as the existing wall test does.
func (w *wallFixture) openDrawerA() string {
	w.t.Helper()
	if r := w.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": w.register, "opening_float_cents": 5000}); r.status != http.StatusCreated {
		w.t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	return w.currentTillID(w.t)
}

// RULE: the movement routes carry the branch wall.
func TestTheMovementRoutesCarryTheBranchWall(t *testing.T) {
	w := newWallFixture(t)
	sessionID := w.openDrawerA()
	body := map[string]any{"kind": "safe_drop", "amount_cents": 1000}

	if r := w.do("POST", "/api/v1/pos/till/"+sessionID+"/movements", body, w.scoped...); r.status != http.StatusNotFound {
		t.Errorf("another branch's drawer, naming branch B = %d, want 404: %s", r.status, r.raw)
	}
	if r := w.do("POST", "/api/v1/pos/till/"+sessionID+"/movements", body, w.noHdr...); r.status != http.StatusForbidden {
		t.Errorf("another branch's drawer, naming no branch = %d, want the 403 record rule: %s", r.status, r.raw)
	}
	if r := w.do("GET", "/api/v1/pos/till/"+sessionID+"/movements", nil, w.scoped...); r.status != http.StatusNotFound {
		t.Errorf("another branch's movement list = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, w.db, `SELECT count(*) FROM till_movements WHERE till_session_id = $1`, sessionID); got != 0 {
		t.Errorf("%d movements written through the wall", got)
	}
	// inside its own branch the same call lands, and the list reads it back
	own := w.do("POST", "/api/v1/pos/till/"+sessionID+"/movements", body)
	if own.status != http.StatusCreated {
		t.Fatalf("unscoped movement = %d: %s", own.status, own.raw)
	}
	if got := str(t, own.body, "branch_id"); got != w.branchID.String() {
		t.Errorf("the movement's branch_id = %s, want the register's branch %s", got, w.branchID)
	}
	if r := w.do("GET", "/api/v1/pos/till/"+sessionID+"/movements", nil); r.status != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Errorf("unscoped movement list = %d: %s, want the one movement", r.status, r.raw)
	}
}
