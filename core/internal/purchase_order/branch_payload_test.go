// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
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
