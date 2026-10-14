// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
)

// The purchase_order freight call-site degrades OPPOSITELY to parsing: when AI is
// unconfigured it must return a HARD error telling the user to enter the freight
// total manually (never a silent success). extractFreight isolates that contract
// so it can be tested without the DB-backed PO lookup.

const freightManualMsg = "AI is off"

func TestExtractFreight_NilClientHardError(t *testing.T) {
	s := &Service{} // aiClient nil
	_, _, err := s.extractFreight(context.Background(), []byte("x"), "application/pdf")
	if err == nil || !strings.Contains(err.Error(), freightManualMsg) {
		t.Fatalf("nil client: want hard %q error, got %v", freightManualMsg, err)
	}
}

func TestExtractFreight_UnconfiguredClientHardError(t *testing.T) {
	s := &Service{aiClient: ai.NewClient("")} // configured object, no key
	_, _, err := s.extractFreight(context.Background(), []byte("x"), "application/pdf")
	if err == nil || !strings.Contains(err.Error(), freightManualMsg) {
		t.Fatalf("unconfigured client: want hard %q error, got %v", freightManualMsg, err)
	}
}

func TestExtractFreight_ConfiguredSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"total_amount":245.50,"carrier_name":"ABC Freight","invoice_number":"FR-1"}`,
			}}},
		})
	}))
	defer srv.Close()

	s := &Service{aiClient: ai.NewClient("k").WithBaseURL(srv.URL)}
	res, raw, err := s.extractFreight(context.Background(), []byte("%PDF-1.4"), "application/pdf")
	if err != nil {
		t.Fatalf("configured client should succeed, got %v", err)
	}
	if res.TotalAmount != 245.50 || res.CarrierName != "ABC Freight" || res.InvoiceNumber != "FR-1" {
		t.Errorf("parsed result = %+v", res)
	}
	if raw == "" {
		t.Error("expected raw response to be returned for the audit trail")
	}
}

// With Anthropic chosen and only its key saved, the freight invoice is read
// through Anthropic as a document block, and OpenRouter is never asked.
func TestExtractFreight_AnthropicChosen(t *testing.T) {
	var blockType string
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		blockType = body.Messages[0].Content[0].Type
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "```json\n{\"total_amount\":99.5,\"carrier_name\":\"Yard Freight\",\"invoice_number\":\"FR-7\"}\n```"}},
		})
	}))
	defer an.Close()
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("OpenRouter must not be called with Anthropic chosen")
	}))
	defer or.Close()

	client := ai.NewClient("").WithBaseURL(or.URL).WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	s := &Service{aiClient: client}
	res, _, err := s.extractFreight(context.Background(), []byte("%PDF-1.4"), "application/pdf")
	if err != nil {
		t.Fatalf("extractFreight: %v", err)
	}
	if res.TotalAmount != 99.5 || res.InvoiceNumber != "FR-7" || blockType != "document" {
		t.Errorf("result %+v, block %q", res, blockType)
	}
}

// The detector types a text upload "text/plain; charset=utf-8"; freight
// extraction under Anthropic must read it as text, not refuse the type.
func TestExtractFreight_AnthropicChosenReadsDetectedText(t *testing.T) {
	var blockType, sent string
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		blockType, sent = body.Messages[0].Content[0].Type, body.Messages[0].Content[0].Text
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": `{"total_amount":12.5,"carrier_name":"Yard Freight","invoice_number":"FR-8"}`}},
		})
	}))
	defer an.Close()
	client := ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(an.URL)
	s := &Service{aiClient: client}
	res, _, err := s.extractFreight(context.Background(), []byte("Freight total 12.50"), "text/plain; charset=utf-8")
	if err != nil {
		t.Fatalf("extractFreight: %v", err)
	}
	if res.InvoiceNumber != "FR-8" || blockType != "text" || !strings.Contains(sent, "Freight total 12.50") {
		t.Errorf("result %+v, block %q, text %q", res, blockType, sent)
	}
}
