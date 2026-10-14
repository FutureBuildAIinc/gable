// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Receiving names a location per line (ADR 0007 section 2.3): a caller bound
// to one branch may not receive stock into another branch's location. The
// purchase order here does not exist, so the path id branch wall passes and
// the refusal comes from the lines' locations, before the receipt is
// attempted; the service is needed only for the path id's branch lookup.
func TestReceivePO_RefusesForeignBranchLocation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	own, other, yard := uuid.New(), uuid.New(), uuid.New()
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{own, "BRANCH", nil}, {other, "BRANCH", nil}, {yard, "WAREHOUSE", other}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, $2, $3, $4)`,
			r.id, r.typ, "po-"+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, own, other)
	})

	h := purchase_order.NewHandler(purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil), nil).
		WithBranchGuard(middleware.NewBranchGuard(db))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := fmt.Sprintf(`{"lines":[{"line_id":%q,"qty_received":1,"location_id":%q}]}`, uuid.New(), yard)
	req := httptest.NewRequest("POST", "/api/v1/purchase-orders/"+uuid.NewString()+"/receive", strings.NewReader(body)).
		WithContext(branchctx.With(ctx, &branchctx.Context{UserSub: "u", BranchID: &own}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bound caller receiving into a foreign yard: %d, want 403: %s", rec.Code, rec.Body)
	}
}

// The draft by vendor lookup the reorder run and the special order line read
// (SEC-NOHEADER-1): a caller held to one branch's grants resolves no draft
// of another branch, so the caller's own create or reorder opens a fresh
// draft rather than appending to a foreign one. Only B holds a draft here,
// so a grants arm that stopped matching would resolve it and fail the leg.
func TestGetDraftPOByVendorHoldsTheGrants(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	branchA, branchB := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{branchA, branchB} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, id, "pd-"+id.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	sub := "user-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, branchA); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	vendor := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'pin draft vendor')`, vendor); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	po := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'DRAFT', 'MANUAL', $3)`,
		po, vendor, branchB); err != nil {
		t.Fatalf("seed draft po: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	repo := purchase_order.NewRepository(db)
	granted := branchctx.With(ctx, &branchctx.Context{UserSub: sub})
	if _, err := repo.GetDraftPOByVendor(granted, &vendor); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("granted A only resolves B's draft: %v, want no row", err)
	}
	at := branchB
	if got, err := repo.GetDraftPOByVendor(branchctx.With(ctx, &branchctx.Context{BranchID: &at}), &vendor); err != nil || got.ID != po {
		t.Errorf("branch B resolves its own draft: %v, want %s", err, po)
	}
}
