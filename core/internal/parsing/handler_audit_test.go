// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
)

// failingProducts is a product repository whose catalogue read fails, so the
// upload fails after the extractor has run.
type failingProducts struct{ product.Repository }

func (failingProducts) ListProducts(context.Context) ([]product.Product, error) {
	return nil, errors.New("catalogue unavailable")
}

// TestUploadAuditRowSurvivesALaterFailure holds the PD-6 ruling's edge: the
// upload is audited before the AI extractor runs, so a failure after the AI
// call (here the product match) still leaves the row that records the spend.
func TestUploadAuditRowSurvivesALaterFailure(t *testing.T) {
	db := testutil.RequireDB(t)
	rows := func() (n int) {
		if err := db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM audit_log WHERE action = 'ai.parsing.upload' AND changes->>'filename' = 'pin-audit-order.csv'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action = 'ai.parsing.upload' AND changes->>'filename' = 'pin-audit-order.csv'`)
	})

	mux := http.NewServeMux()
	NewHandler(NewService(failingProducts{}, nil)).WithAuditLog(audit.NewLogger(db)).RegisterRoutes(mux)

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("file", "pin-audit-order.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("sku,qty\nPIN-1,2\n"))
	_ = w.Close()
	req := httptest.NewRequest("POST", "/api/v1/parsing/upload", body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	before := rows()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("upload with a failing catalogue answered %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if after := rows(); after != before+1 {
		t.Errorf("upload audit rows: %d then %d, want one new row although the upload failed after the extractor", before, after)
	}
}
