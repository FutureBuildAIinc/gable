// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// With no key the freight invoice upload answers the plain AI off sentence
// in the error envelope (stable code ai_off), not a bare "Bad Request".
func TestWire_FreightUploadSaysAIOffWithNoKey(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := branchctx.WithSystem(context.Background())
	vendor, po := uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "ai-off-"+vendor.String()[:6])
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'MANUAL', `+branch+`)`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil)
	svc.WithAIClient(ai.NewClient(""))
	h := purchase_order.NewHandler(svc, nil)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "freight.pdf")
	_, _ = fw.Write([]byte("%PDF-1.4 not a real invoice"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-orders/x/freight", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", po.String())
	rec := httptest.NewRecorder()
	h.HandleUploadFreight(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
	}
	var b struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != ai.CodeAIOff {
		t.Errorf("code = %q, want %q", b.Error.Code, ai.CodeAIOff)
	}
	if !strings.Contains(b.Error.Message, "AI is off") || !strings.Contains(b.Error.Message, "Tech Admin") {
		t.Errorf("message = %q", b.Error.Message)
	}
}
