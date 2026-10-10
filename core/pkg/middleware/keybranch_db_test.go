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
