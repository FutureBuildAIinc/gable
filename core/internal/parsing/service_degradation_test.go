// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
)

// The parsing call-site must ALWAYS degrade when AI is unavailable rather than
// return an error (CLAUDE.md "degrade gracefully"). The fallback is not an
// extraction of the uploaded file — it is a fixed demo list — so the second
// half of the invariant is that the call reports the result as synthetic.
// These guard both halves per the degradation matrix.

// assertFallback verifies the result came from the canned demo list (not the AI
// path and not the raw input), keyed on a marker unique to that list so the
// assertion is falsifiable rather than a bare len>0. It also requires the call
// to have flagged the result synthetic: the list is unrelated to the caller's
// file, so a silent fallback that does not say so is the bug this guards.
func assertFallback(t *testing.T, items []extractedLine, synthetic bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("AI unavailable must not error (silent fallback), got %v", err)
	}
	if !synthetic {
		t.Errorf("fallback result must be reported as synthetic so callers cannot present it as the user's takeoff")
	}
	for _, it := range items {
		if strings.Contains(it.rawText, "SPF Stud") {
			return
		}
	}
	t.Fatalf("expected the canned demo list (marker %q), got %d items: %+v", "SPF Stud", len(items), items)
}

func TestExtractItemsWithAI_NilClientFallsBack(t *testing.T) {
	s := NewService(&mockProductRepo{}, nil)
	items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte("anything"), "image/png")
	assertFallback(t, items, synthetic, err)
}

func TestExtractItemsWithAI_UnconfiguredClientFallsBack(t *testing.T) {
	s := NewService(&mockProductRepo{}, ai.NewClient("")) // no key
	items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte("anything"), "image/png")
	assertFallback(t, items, synthetic, err)
}

// TestExtractItemsWithAI_MidCallFailureFallsBack is the riskiest path: AI is
// configured but the call fails mid-flight. The service must swallow the error
// and fall back to the rule-based list, not propagate it.
func TestExtractItemsWithAI_MidCallFailureFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := ai.NewClient("test-key").WithBaseURL(srv.URL) // configured, but the call 500s
	s := NewService(&mockProductRepo{}, client)
	items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte("anything"), "image/png")
	assertFallback(t, items, synthetic, err)
}
