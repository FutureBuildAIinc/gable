// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
)

// A pasted or text list never needs the AI reader: when the provider refuses
// the saved key the rule-based reader still returns the caller's own lines,
// not a refusal and not the canned sample.
func TestExtractItemsWithAI_TextListIsReadByRulesWhenTheKeyIsRefused(t *testing.T) {
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	svc := NewService(&mockProductRepo{}, client)

	items, synthetic, err := svc.ExtractItemsWithAI(context.Background(), []byte("50 pcs - 2x4x8 stud"), "text/plain")
	if err != nil || synthetic {
		t.Fatalf("err = %v, synthetic = %v, want the caller's own line", err, synthetic)
	}
	if len(items) != 1 || items[0].quantity != 50 {
		t.Fatalf("items = %+v, want the one pasted line", items)
	}
}
