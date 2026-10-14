// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/techadmin"
)

// anthropicAnswering is a stand-in provider that answers every call with the
// status and counts the calls.
func anthropicAnswering(t *testing.T, status int, hits *int32) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)
	return ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey(fakeAnthropicKey).WithAnthropicBaseURL(srv.URL)
}

// RULE (N7): saving a key tests it with the provider and the document reads
// from that test: "passed" after an answer, "refused" with the plain words
// after a refusal. The result is stored, so a later read says the same.
func TestWire_SavingAKeyTestsItAndStoresTheResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		want    string
		message string
	}{
		{"passed", http.StatusOK, "passed", ""},
		{"refused", http.StatusUnauthorized, "refused", "Anthropic refused the saved key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			client := anthropicAnswering(t, tc.status, &hits)
			f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
				return s.WithVault(testVault(t)).WithAITester(client)
			})
			r := f.do("PUT", "/api/v1/admin/settings/ai",
				map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", `"1"`)
			if r.status != http.StatusOK {
				t.Fatalf("save = %d: %s", r.status, r.raw)
			}
			if atomic.LoadInt32(&hits) != 1 {
				t.Errorf("the provider was called %d times by the save, want 1", hits)
			}
			if r.body["test_status"] != tc.want {
				t.Errorf("save answer test_status = %v, want %q: %s", r.body["test_status"], tc.want, r.raw)
			}
			if msg, _ := r.body["test_message"].(string); !strings.Contains(msg, tc.message) {
				t.Errorf("test_message = %q, want it to hold %q", msg, tc.message)
			}
			get := f.do("GET", "/api/v1/admin/settings/ai", nil)
			if get.body["test_status"] != tc.want {
				t.Errorf("GET test_status = %v, want %q", get.body["test_status"], tc.want)
			}
			if strings.Contains(string(r.raw)+string(get.raw), fakeAnthropicKey) {
				t.Error("an answer holds the key")
			}
		})
	}
}

// RULE (N7): a key that was never tested says so; a Test connection press is
// stored like a save's test.
func TestWire_AnUntestedKeyReadsUntestedAndAPressStoresTheResult(t *testing.T) {
	var hits int32
	client := anthropicAnswering(t, http.StatusUnauthorized, &hits)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithAITester(client).WithSettingsDefaults("or-"+"test-not-real", "", "")
	})
	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if get.body["configured"] != true || get.body["test_status"] != "untested" {
		t.Fatalf("configured, never tested: %s", get.raw)
	}
	press := f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if press.status != http.StatusOK {
		t.Fatalf("test = %d: %s", press.status, press.raw)
	}
	after := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if after.body["test_status"] == "untested" || after.body["test_status"] == nil {
		t.Errorf("a pressed test left the document %v", after.body["test_status"])
	}
}
