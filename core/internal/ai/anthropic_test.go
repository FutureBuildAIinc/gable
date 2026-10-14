// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testAnthropicKey is an obvious placeholder, never a provider key shape.
const testAnthropicKey = "test-" + "key-not-real"

func newAnthropicTestClient(url string) *Client {
	return NewClient("").
		WithProvider(ProviderAnthropic).
		WithAnthropicKey(testAnthropicKey).
		WithAnthropicBaseURL(url)
}

func TestValidateAnthropicModel(t *testing.T) {
	ok := []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-6-0", "claude-sonnet-5-5-20260101", "claude-sonnet-5-5-20261001", "claude-opus-6-20270101", "claude-fable-5-5", "claude-fable-5-1", ""}
	for _, m := range ok {
		if err := ValidateAnthropicModel(m); err != nil {
			t.Errorf("ValidateAnthropicModel(%q) = %v, want nil", m, err)
		}
	}
	bad := []string{"claude-haiku-5-5", "claude-sonnet-4-5", "claude-opus-4-1", "gpt-4o", "claude-sonnet-5-4", "claude-3-5-sonnet",
		"claude-sonnet-5-20260101", "claude-sonnet-5", "claude-opus-5-5-x", "claude-sonnet-5-4-20261001"}
	for _, m := range bad {
		err := ValidateAnthropicModel(m)
		if err == nil {
			t.Errorf("ValidateAnthropicModel(%q) = nil, want a refusal", m)
			continue
		}
		if !strings.Contains(err.Error(), m) {
			t.Errorf("refusal for %q does not name the model: %v", m, err)
		}
	}
}

func TestAnthropicGenerateRequestShape(t *testing.T) {
	var gotPath, gotKey, gotVersion, gotCT string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "claude-sonnet-5-5",
			"content": []any{map[string]any{"type": "text", "text": "```json\n{\"a\":1}\n```"}},
		})
	}))
	defer srv.Close()

	text, model, err := newAnthropicTestClient(srv.URL).Generate(context.Background(), "be brief", "hello", 50)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != `{"a":1}` || model != "claude-sonnet-5-5" {
		t.Errorf("got (%q, %q)", text, model)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %s", gotPath)
	}
	if gotKey != testAnthropicKey {
		t.Errorf("x-api-key header not set from the key")
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q", gotVersion)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if body["model"] != "claude-sonnet-5-5" {
		t.Errorf("model = %v, want the default claude-sonnet-5-5", body["model"])
	}
	if body["system"] != "be brief" {
		t.Errorf("system = %v", body["system"])
	}
	if n, _ := body["max_tokens"].(float64); n != 50 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
}

func TestAnthropicExtractMaterialListBlocks(t *testing.T) {
	var body struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "50 pcs - 2x4x8 SPF Stud"}},
		})
	}))
	defer srv.Close()
	c := newAnthropicTestClient(srv.URL)

	for _, tc := range []struct{ ct, blockType, media string }{
		{"image/png", "image", "image/png"},
		{"application/pdf", "document", "application/pdf"},
	} {
		out, err := c.ExtractMaterialList(context.Background(), []byte("bytes"), tc.ct)
		if err != nil {
			t.Fatalf("%s: %v", tc.ct, err)
		}
		if out != "50 pcs - 2x4x8 SPF Stud" {
			t.Errorf("%s: out = %q", tc.ct, out)
		}
		blocks := body.Messages[0].Content
		if blocks[0]["type"] != tc.blockType {
			t.Errorf("%s: first block type = %v, want %s", tc.ct, blocks[0]["type"], tc.blockType)
		}
		src, _ := blocks[0]["source"].(map[string]any)
		if src["type"] != "base64" || src["media_type"] != tc.media || src["data"] != "Ynl0ZXM=" {
			t.Errorf("%s: source = %v", tc.ct, src)
		}
		if blocks[1]["type"] != "text" {
			t.Errorf("%s: second block = %v", tc.ct, blocks[1])
		}
	}
}

func TestAnthropicErrorAnswerNamesProviderNotKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A provider that echoes the key back must not leak it through us.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "authentication_error", "message": "invalid x-api-key " + testAnthropicKey},
		})
	}))
	defer srv.Close()

	_, _, err := newAnthropicTestClient(srv.URL).Generate(context.Background(), "", "hi", 10)
	if err == nil {
		t.Fatal("want an error on a 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("error does not carry the provider's words: %v", err)
	}
	if strings.Contains(err.Error(), testAnthropicKey) {
		t.Errorf("error leaks the key: %v", err)
	}
}

func TestAnthropicTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := newAnthropicTestClient(srv.URL).Generate(ctx, "", "hi", 10)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if strings.Contains(err.Error(), testAnthropicKey) {
		t.Errorf("error leaks the key: %v", err)
	}
}

func TestAnthropicRefusesModelBelowSonnet55(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	c := newAnthropicTestClient(srv.URL).WithAnthropicModel("claude-haiku-5-5")
	_, _, err := c.Generate(context.Background(), "", "hi", 10)
	if err == nil || !strings.Contains(err.Error(), "claude-haiku-5-5") {
		t.Fatalf("want a refusal naming the model, got %v", err)
	}
	if called {
		t.Error("a refused model must not reach the provider")
	}
}

func TestAnthropicIsConfiguredNeedsItsOwnKey(t *testing.T) {
	// The OpenRouter key does not turn the Anthropic provider on.
	c := NewClient("openrouter-test-key").WithProvider(ProviderAnthropic)
	if c.IsConfigured(context.Background()) {
		t.Error("anthropic provider reads as configured with no anthropic key")
	}
	if !c.WithAnthropicKey(testAnthropicKey).IsConfigured(context.Background()) {
		t.Error("anthropic provider with a key reads as not configured")
	}
}

func TestAnthropicTestConnection(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
	}))
	defer srv.Close()
	if err := newAnthropicTestClient(srv.URL).TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if n, _ := body["max_tokens"].(float64); n != 1 {
		t.Errorf("test connection max_tokens = %v, want the cheapest call (1)", body["max_tokens"])
	}
}

func TestOpenSealedValue(t *testing.T) {
	s := fakeSealer{}
	got, err := openValue(s, "enc:v1:abc")
	if err != nil || got != "opened:abc" {
		t.Errorf("openValue sealed = (%q, %v)", got, err)
	}
	got, err = openValue(s, "plain")
	if err != nil || got != "plain" {
		t.Errorf("openValue legacy plaintext = (%q, %v)", got, err)
	}
}

type fakeSealer struct{}

func (fakeSealer) Present() bool { return true }
func (fakeSealer) Seal(s string) (string, error) {
	return "enc:v1:" + s, nil
}
func (fakeSealer) Open(s string) (string, error) {
	return "opened:" + strings.TrimPrefix(s, "enc:v1:"), nil
}
