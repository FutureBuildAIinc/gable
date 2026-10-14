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
	"os"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// When the provider refuses the saved key (a 401) the freight invoice upload
// answers 502 with the stable code ai_refused and the sentence naming what to
// do, the same answer as the intake and PIM, never a bare "Bad Request". The
// file is not kept for a read that failed.
func TestWire_FreightUploadAnswersAProviderRefusalInWords(t *testing.T) {
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
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "refused-"+vendor.String()[:6])
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'MANUAL', `+branch+`)`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	// The upload directory is relative to the working directory: keep it out
	// of the package folder.
	wd, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)

	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil)
	svc.WithAIClient(client)
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

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body)
	}
	var b struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != ai.CodeAIRefused {
		t.Errorf("code = %q, want %q", b.Error.Code, ai.CodeAIRefused)
	}
	if !strings.Contains(b.Error.Message, "Anthropic refused the saved key") ||
		!strings.Contains(b.Error.Message, "enter the freight total by hand") {
		t.Errorf("message = %q", b.Error.Message)
	}
	if strings.Contains(rec.Body.String(), "key-not-real") {
		t.Error("the answer holds the key")
	}
	if entries, _ := os.ReadDir(dir + "/uploads/freight"); len(entries) != 0 {
		t.Errorf("%d file(s) kept for a refused read, want none", len(entries))
	}
}
