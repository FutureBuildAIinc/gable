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

// A pasted list, a text file or a spreadsheet (the handler hands the service
// text/plain for a spreadsheet) is read by the rule-based reader when no AI
// reader is set, so the user's own lines come back, flagged not synthetic.
func TestExtractItemsWithAI_TextWithoutAIReadsTheCallersList(t *testing.T) {
	for _, tc := range []struct {
		name        string
		client      *ai.Client
		contentType string
	}{
		{"nil client plain text", nil, "text/plain"},
		{"unconfigured client csv", ai.NewClient(""), "text/csv"},
		{"charset suffix", nil, "text/plain; charset=utf-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewService(&mockProductRepo{}, tc.client)
			body := "12 pcs - 4x4x8 Cedar Post\n3 bags - Polymeric Sand\n"
			items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte(body), tc.contentType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if synthetic {
				t.Fatalf("the caller's own text must not be reported as synthetic")
			}
			if len(items) != 2 || items[0].quantity != 12 || !strings.Contains(items[0].rawText, "Cedar Post") {
				t.Fatalf("expected the caller's two lines, got %+v", items)
			}
		})
	}
}

// Images and PDFs need the AI reader, so without one they keep the sample.
func TestExtractItemsWithAI_NonTextWithoutAIStaysSynthetic(t *testing.T) {
	for _, ct := range []string{"image/png", "application/pdf"} {
		s := NewService(&mockProductRepo{}, nil)
		items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte("5 pcs - 2x4x8"), ct)
		assertFallback(t, items, synthetic, err)
	}
}

// Only plain text, csv and the spreadsheet types are a list; markup the
// detector calls text (an SVG, an HTML page) is not the user's list, so it keeps
// the labelled sample rather than coming back as noise lines.
func TestExtractItemsWithAI_MarkupIsNotAList(t *testing.T) {
	for _, ct := range []string{"text/xml; charset=utf-8", "text/html; charset=utf-8", "image/svg+xml"} {
		s := NewService(&mockProductRepo{}, nil)
		items, synthetic, err := s.ExtractItemsWithAI(context.Background(), []byte("<svg><text>5 pcs</text></svg>"), ct)
		assertFallback(t, items, synthetic, err)
	}
}

// A header row (qty,item) is not a line of the list.
func TestExtractItems_SkipsAHeaderRow(t *testing.T) {
	s := NewService(&mockProductRepo{}, nil)
	for _, body := range []string{
		"qty,item\n12,Cedar Post\n3,Polymeric Sand\n",
		"Quantity\tDescription\n12\tCedar Post\n3\tPolymeric Sand\n",
	} {
		got := s.ExtractItems(body)
		if len(got) != 2 || strings.Contains(strings.ToLower(got[0].rawText), "qty") || strings.Contains(strings.ToLower(got[0].rawText), "quantity") {
			t.Fatalf("expected the two data rows only, got %+v", got)
		}
	}
	// A first line that is a product, not a header, stays.
	if got := s.ExtractItems("Polymeric Sand, bag\n3 pcs - Cedar Post\n"); len(got) != 2 {
		t.Fatalf("a product line must not be taken for a header, got %+v", got)
	}
}
