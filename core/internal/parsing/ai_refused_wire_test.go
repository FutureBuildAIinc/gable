// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
)

// When the provider refuses the saved key (a 401) the intake says so in words
// the desk shows, in the wire envelope with the stable code ai_refused. It is
// never the "no reader" sample list and never a bare 500.
func TestWire_UploadProviderRefusalIsAnswerInWords(t *testing.T) {
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	h := NewHandler(NewService(&mockProductRepo{}, client))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "list.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\n not really a picture"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/parsing/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body)
	}
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != "ai_refused" {
		t.Errorf("code = %q, want ai_refused", b.Error.Code)
	}
	want := "Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings."
	if b.Error.Message != want {
		t.Errorf("message = %q, want %q", b.Error.Message, want)
	}
	if strings.Contains(rec.Body.String(), "key-not-real") {
		t.Error("the answer holds the key")
	}
}
