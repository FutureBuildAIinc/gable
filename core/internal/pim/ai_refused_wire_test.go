// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

// When the provider refuses the saved key (a 401) each PIM generate route that
// answers in the request says so in words with the stable code ai_refused: a
// 502 in the wire envelope, never a bare 500 the page cannot explain. (The
// image route generates after it answers; its refusal is the media row's.)
func TestWire_PIMGenerateRoutesAnswerAProviderRefusalInWords(t *testing.T) {
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	id := uuid.New().String()
	for _, route := range []string{"descriptions", "seo", "collateral"} {
		t.Run(route, func(t *testing.T) {
			mux := aiOffMux(t, client)
			body := `{}`
			if route == "collateral" {
				body = `{"type":"spec_sheet"}`
			}
			rec := postJSON(mux, "/api/v1/products/"+id+"/pim/generate/"+route, body)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body)
			}
			var b offBody
			if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			if b.Error.Code != "ai_refused" {
				t.Errorf("code = %q, want ai_refused", b.Error.Code)
			}
			if !strings.Contains(b.Error.Message, "Anthropic refused the saved key") ||
				!strings.Contains(b.Error.Message, "Tech Admin, AI Settings") {
				t.Errorf("message = %q", b.Error.Message)
			}
		})
	}
}

// The image route answers 202 and draws afterwards, so a refusal of the saved
// key cannot be its answer. The row settles "refused", a state of its own that
// the page reads in words, never "failed" with a retry that can never work.
func TestWire_PIMImageSettlesRefusedWhenTheProviderRefusesTheKey(t *testing.T) {
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer an.Close()
	repo := newChanRepo()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(repo, anthropicClient(an.URL))).RegisterRoutes(mux)

	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	u := repo.wait(t)
	if u.status != "refused" || u.url != "" {
		t.Errorf("finalize = %+v, want refused with no url", u)
	}
}
