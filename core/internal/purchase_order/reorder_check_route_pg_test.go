// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/pkg/branchctx"
)

// The reorder check route names the products it left alone for want of a
// reorder quantity, beside the count.
func TestReorderCheckRoute_NamesWhatItLeftAlone(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	onlyYardBAtItsPoint(t, s)
	mux := http.NewServeMux()
	purchase_order.NewHandler(svc, nil).RegisterRoutes(mux)
	req := httptest.NewRequest("POST", "/api/v1/purchase-orders/reorder-check", nil)
	req = req.WithContext(branchctx.WithSystem(req.Context()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder check = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count        int      `json:"count"`
		NoReorderQty []string `json:"no_reorder_qty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.NoReorderQty) != 1 {
		t.Fatalf("answer = %s, want count 0 and the one product named in no_reorder_qty", rec.Body.String())
	}
}
