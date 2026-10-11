// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

// The key branch wall's database backed unit tests: the location row lookup's
// SQL error arm must fail closed (500, the handler never reached), not fail
// open by swallowing the query error into "no branch named".

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// TestKeyBranchWall_LocationRowQueryErrorFailsClosed drives the location row
// lookup with a context the database refuses (cancelled before the query
// runs): the wall answers 500 and the handler never runs. A lookup that
// swallowed its query error into "no branch named" would pass the request
// through, and this test fails.
func TestKeyBranchWall_LocationRowQueryErrorFailsClosed(t *testing.T) {
	db := testutil.RequireDB(t)
	w := NewKeyBranchWall(db, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pin := uuid.New()
	ctx = branchctx.WithKeyBranch(ctx, pin)

	ran := false
	handler := w.LocationRowBranch()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPut, "/api/v1/locations/"+uuid.NewString(), nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", uuid.NewString())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("cancelled query: %d, want 500 (fail closed)", rec.Code)
	}
	if ran {
		t.Error("the handler ran under a failed branch lookup; the wall failed open")
	}
}

// TestKeyBranchWall_ExclusiveNamesAnotherBranchDeterministically holds the
// refusal's "also held by" branch (round 7 P3): it is never the pin itself, and
// among several other branches it is the lowest numbered one every time, so a
// reviewer comparing two answers sees the same branch.
func TestKeyBranchWall_ExclusiveNamesAnotherBranchDeterministically(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	pin, c, d := uuid.New(), uuid.New(), uuid.New()
	own, shared, nobody := uuid.New(), uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{pin, c, d} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'wall order')`,
			b, "wall-ord-"+b.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	for _, cu := range []uuid.UUID{own, shared, nobody} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'wall order', $2, $3)`,
			cu, "WALLORD-"+cu.String()[:8], pin); err != nil {
			t.Fatalf("seed customer: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($3, $4), ($3, $5)`,
		own, pin, shared, c, d); err != nil {
		t.Fatalf("seed customer branches: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN ($1, $2, $3)`, own, shared, nobody)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id IN ($1, $2, $3)`, own, shared, nobody)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2, $3)`, pin, c, d)
	})

	lower := c
	if d.String() < c.String() {
		lower = d
	}
	w := NewKeyBranchWall(db, nil)
	refusal := func(customers ...uuid.UUID) string {
		t.Helper()
		handler := w.CustomerBranchExclusive(func(context.Context, *http.Request) ([]uuid.UUID, error) {
			return customers, nil
		})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the handler ran for a refused customer") }))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tax/exemptions", nil).WithContext(branchctx.WithKeyBranch(ctx, pin))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("answer %d, want 403: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	for i := 0; i < 5; i++ {
		got := refusal(own, shared)
		if !strings.Contains(got, "also held by branch "+lower.String()) {
			t.Errorf("run %d: %s, want it to name the lowest numbered other branch %s", i, got, lower)
		}
		if strings.Contains(got, pin.String()) {
			t.Errorf("run %d: the refusal names the pin itself: %s", i, got)
		}
	}
	// A customer held by nobody next to one the pin holds: refused, and the
	// message names no branch (the pin must not be offered as the other one).
	if got := refusal(own, nobody); strings.Contains(got, "also held by branch") {
		t.Errorf("a customer held by nobody: %s, want no branch named", got)
	}
}
