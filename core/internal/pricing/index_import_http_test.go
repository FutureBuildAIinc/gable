// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The market index import routes over HTTP (QE-07-b1): the file arrives as
// multipart form data, the preview answers without writing, the apply writes
// the accepted rows, and a file the server cannot read is the caller's
// mistake while a read that fails is the server's.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// importServer serves the import routes the way serve does, over the real
// repositories.
func importServer(t *testing.T, db *database.DB) *httptest.Server {
	t.Helper()
	escalators := pricing.NewEscalatorRepository(db)
	exposure := pricing.NewExposureRepository(db)
	scanner := pricing.NewExposureScanner(exposure, escalators, quote.NewRepository(db), nil, db, nil)
	source := pricing.NewIndexSourceRepository(db)
	handler := pricing.NewIndexImportHandler(
		pricing.NewIndexImportService(escalators, source, db, scanner, nil),
		pricing.NewIndexStalenessService(source),
	)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// importAnswer is one route's answer: the status and the body.
type importAnswer struct {
	Status int
	Body   []byte
}

// postFileOnce posts one CSV as multipart form data, once, and reads the
// answer.
func postFileOnce(t *testing.T, srv *httptest.Server, path, filename, body string) importAnswer {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("build the multipart form: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("write the file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the multipart form: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, &buf)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post the file: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return importAnswer{Status: resp.StatusCode, Body: raw}
}

// RULE: the preview route answers what the file would do and writes
// nothing; the apply route writes the rows the preview accepted.
func TestQE07_ImportRoutesOverHTTP(t *testing.T) {
	db := testutil.RequireDB(t)
	code := "QE07_HTTP_" + uuid.NewString()[:8]
	indexID := seedImportIndex(t, db, code, 100)
	srv := importServer(t, db)

	file := fmt.Sprintf(`index_code,value,as_of_date
%s,120,2026-01-06
`, code)

	preview := postFileOnce(t, srv, "/api/v1/market-indices/import/preview", "indices.csv", file)
	if preview.Status != http.StatusOK {
		t.Fatalf("the preview = %d, want 200", preview.Status)
	}
	var previewBody pricing.IndexImportPreview
	if err := json.Unmarshal(preview.Body, &previewBody); err != nil {
		t.Fatalf("read the preview: %v", err)
	}
	if previewBody.AcceptedCount != 1 {
		t.Errorf("the preview accepted %d rows, want 1", previewBody.AcceptedCount)
	}
	// The preview writes nothing.
	if n := historyCount(t, db, indexID); n != 0 {
		t.Errorf("the preview wrote %d history rows, want 0", n)
	}

	apply := postFileOnce(t, srv, "/api/v1/market-indices/import/apply", "indices.csv", file)
	if apply.Status != http.StatusOK {
		t.Fatalf("the apply = %d, want 200: %s", apply.Status, apply.Body)
	}
	var applied pricing.IndexImportResult
	if err := json.Unmarshal(apply.Body, &applied); err != nil {
		t.Fatalf("read the apply: %v", err)
	}
	if applied.AppliedCount != 1 || applied.ChangedIndices != 1 {
		t.Errorf("the apply = %+v, want one row and one index", applied)
	}
	if n := historyCount(t, db, indexID); n != 1 {
		t.Errorf("history rows = %d, want 1", n)
	}
}

// RULE: a file the server cannot read is the caller's mistake (400), and a
// file that is not there at all is refused the same way.
func TestQE07_ImportRoutesRefuseABadFile(t *testing.T) {
	db := testutil.RequireDB(t)
	srv := importServer(t, db)

	// A file whose header names no column the import needs.
	bad := postFileOnce(t, srv, "/api/v1/market-indices/import/preview", "bad.csv", "name,value\nx,1\n")
	if bad.Status != http.StatusBadRequest {
		t.Errorf("the preview of a bad file = %d, want 400", bad.Status)
	}

	// A request with no file at all.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/market-indices/import/preview", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post without a file: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the preview without a file = %d, want 400", resp.StatusCode)
	}
}

// RULE: the staleness route answers the threshold and each index's
// freshness.
func TestQE07_StalenessRouteOverHTTP(t *testing.T) {
	db := testutil.RequireDB(t)
	srv := importServer(t, db)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/market-indices/staleness")
	if err != nil {
		t.Fatalf("read the staleness: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the staleness route = %d, want 200", resp.StatusCode)
	}
	var doc pricing.IndexStalenessDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("read the staleness: %v", err)
	}
	// The threshold itself belongs to the settings row, which the Tech Admin
	// tests also write, so this route's test holds that a threshold is
	// answered rather than which number it is.
	if doc.ThresholdDays < 1 {
		t.Errorf("threshold_days = %d, want a threshold", doc.ThresholdDays)
	}
	if doc.Indices == nil {
		t.Error("the freshness list is null, want a list")
	}
}
