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
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
)

// With no key the intake keeps its labelled sample, and the reason says AI is
// off and that an administrator turns it on in Tech Admin, AI: it names no
// environment variable.
func TestWire_UploadSampleReasonSaysAIOff(t *testing.T) {
	h := NewHandler(NewService(&mockProductRepo{}, ai.NewClient("")))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "list.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\n not really a picture"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/parsing/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	var resp struct {
		Synthetic       bool   `json:"synthetic"`
		SyntheticReason string `json:"synthetic_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Synthetic {
		t.Fatal("the sample must be labelled synthetic")
	}
	r := resp.SyntheticReason
	if strings.Contains(r, "OPENROUTER_API_KEY") {
		t.Errorf("reason names an environment variable: %q", r)
	}
	if !strings.Contains(r, "AI is off") || !strings.Contains(r, "Tech Admin") {
		t.Errorf("reason = %q", r)
	}
}

// Under Anthropic a picture type Claude cannot read is refused in words
// (415), never answered with the canned demo list.
func TestWire_UploadUnreadableTypeIsRefusedInWords(t *testing.T) {
	var hits int32
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	h := NewHandler(NewService(&mockProductRepo{}, client))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "list.bmp")
	_, _ = fw.Write(append([]byte("BM"), make([]byte, 64)...))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/parsing/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Claude reads JPEG, PNG, GIF or WebP pictures and PDF files") {
		t.Errorf("body lacks the words: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "synthetic") {
		t.Errorf("a canned list was answered: %s", rec.Body)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Error("the provider was called for a type it cannot read")
	}
}
