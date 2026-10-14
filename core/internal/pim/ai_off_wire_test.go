// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

func aiOffMux(t *testing.T, client *ai.Client) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(newPIMTestService(&stubPIMRepo{}, client)).RegisterRoutes(mux)
	return mux
}

type offBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func postJSON(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// With no key every PIM generate route says plainly that AI is off: the
// stable code and the sentence, never a 500 or a bare status.
func TestWire_PIMGenerateRoutesSayAIOffWithNoKey(t *testing.T) {
	id := uuid.New().String()
	for _, route := range []string{"descriptions", "seo", "collateral", "image"} {
		t.Run(route, func(t *testing.T) {
			mux := aiOffMux(t, ai.NewClient(""))
			rec := postJSON(mux, "/api/v1/products/"+id+"/pim/generate/"+route, `{}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
			}
			var b offBody
			if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			if b.Error.Code != ai.CodeAIOff {
				t.Errorf("code = %q, want %q", b.Error.Code, ai.CodeAIOff)
			}
			if !strings.Contains(b.Error.Message, "AI is off") || !strings.Contains(b.Error.Message, "Tech Admin") {
				t.Errorf("message = %q", b.Error.Message)
			}
		})
	}
}

// With Anthropic chosen, image generation answers the plain refusal naming
// the reason and no OpenRouter server receives a thing.
func TestWire_PIMImageUnderAnthropicNeverCallsOpenRouter(t *testing.T) {
	var hits int32
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer or.Close()
	client := ai.NewClient("or-test-key-not-real").WithBaseURL(or.URL).
		WithProvider(ai.ProviderAnthropic).WithAnthropicKey("test-" + "key-not-real")
	mux := aiOffMux(t, client)
	rec := postJSON(mux, "/api/v1/products/"+uuid.New().String()+"/pim/generate/image", `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
	}
	var b offBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b.Error.Code != ai.CodeAIOff || !strings.Contains(b.Error.Message, "OpenRouter") {
		t.Errorf("answer = %+v", b.Error)
	}
	if _, _, err := client.GenerateImage(t.Context(), "x", ""); err == nil {
		t.Error("GenerateImage under anthropic must refuse")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("OpenRouter received %d requests, want 0", n)
	}
}
