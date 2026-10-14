// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

// With no key the freight invoice upload answers the plain AI off sentence
// in the error envelope (stable code ai_off), not a bare "Bad Request".
func TestWire_FreightUploadSaysAIOffWithNoKey(t *testing.T) {
	h := NewHandler(&Service{aiClient: ai.NewClient("")}, nil)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "freight.pdf")
	_, _ = fw.Write([]byte("%PDF-1.4 not a real invoice"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-orders/x/freight", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", uuid.New().String())
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
