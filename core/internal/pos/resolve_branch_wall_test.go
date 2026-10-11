// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The resolution's branch wall (ninth review P1-3, tenth review P2-2): the
// route settles a claim only inside the caller's branch. The claim's sale is
// read through the branch context the middleware settled, so a finance user
// held to branch B finds branch A's claim a missing row (404) on both legs,
// nothing moves, and no audit row names them; the same claim resolves for a
// caller inside its own branch.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestTheResolutionStaysInsideTheCallersBranch(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// the kill switch is read when the fixture's middleware is built
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	mustExecB := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExecB(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-POS-R', 'resolve wall B')`, branchB)
	mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-res-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		mustExecB(`DELETE FROM user_locations WHERE user_sub = 'pos-res-b' AND branch_id = $1`, branchB)
		mustExecB(`DELETE FROM locations WHERE id = $1`, branchB)
	})

	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, db, func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded claim = (%s, %q), want PENDING carrying no refund id", state, refundTxID)
	}
	// the B finance user's headers: the branch middleware scopes them to B
	scoped := []string{"X-Test-Role", "finance", "X-Test-Sub", "pos-res-b", "X-Branch-Id", branchB.String()}

	// branch A's sale reads as a missing row through the wall
	if r := f.do("GET", "/api/v1/pos/transactions/"+saleID, nil, scoped...); r.status != http.StatusNotFound {
		t.Errorf("another branch's sale = %d, want 404: %s", r.status, r.raw)
	}
	// the no refund leg: 404, the claim stands, nothing moved, no audit row
	r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-B", "reason": "out of my branch",
	}, scoped...)
	if r.status != http.StatusNotFound {
		t.Errorf("another branch's no refund resolution = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the claim was released through the wall: %d PENDING rows, want 1", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved' AND changes->>'intent_id' = $1`, intentID); got != 0 {
		t.Errorf("%d resolution audit rows through the wall, want 0", got)
	}
	// the refund leg: 404 the same way, with the operator's own id recorded
	r = f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", map[string]any{
		"outcome": "refund", "refund_tx_id": "ref-op-b", "reason": "out of my branch",
	}, scoped...)
	if r.status != http.StatusNotFound {
		t.Errorf("another branch's refund resolution = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the claim was settled through the wall: %d PENDING rows, want 1", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (nothing moved through the wall)", got)
	}
	f.assertARInvariants(t)
	// the same claim resolves for a caller inside its own branch
	rr := f.resolveOn(t, intentID, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-A", "reason": "the terminal log shows no refund",
	}, "finance")
	if rr.status != http.StatusOK {
		t.Fatalf("the in branch resolution = %d, want 200: %s", rr.status, rr.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'FAILED'`, intentID); got != 1 {
		t.Errorf("the in branch resolution did not release the claim")
	}
	f.assertARInvariants(t)
}

// RULE: the resolution route runs the guard serve passes it (ninth review
// P1-3's probe: the route dropped the counter's guard and ran none of its
// own but the role check). A counting guard stands in for the branch
// middleware compose and must run once per resolution.
func TestTheResolutionRouteRunsTheBranchGuardItIsGiven(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, _, _, _ := pendingIntent(t, f, saleID)

	var ran int32
	wall := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&ran, 1)
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	pos.NewHandler(f.service).RegisterRoutes(mux, nil, wall)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/pos/refund-intents/"+intentID+"/resolve",
		strings.NewReader(`{"outcome":"no_refund","reconciliation_reference":"R","reason":"w"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pos-wall-test")
	req.Header.Set("X-Test-Role", "finance")
	req.Header.Set("X-Test-Sub", mustUUID(t))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the resolution behind the guard = %d, want 200", res.StatusCode)
	}
	if n := atomic.LoadInt32(&ran); n != 1 {
		t.Errorf("the guard ran %d times on the resolution route, want 1 (the route must run the branch middleware serve passes)", n)
	}
}
