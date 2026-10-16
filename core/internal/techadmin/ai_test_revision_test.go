// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/techadmin"
)

// The stored test result names the settings revision it ran on: a result that
// belongs to an older revision of the settings reads as untested. (Two tests
// that finish out of order are the next leg's: the result of the older
// revision never replaces the later one.)
func TestWire_AStoredTestResultReadsUntestedOnceTheSettingsMoved(t *testing.T) {
	var hits int32
	client := anthropicAnswering(t, http.StatusOK, &hits)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t)).WithAITester(client)
	})
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", `"1"`)
	if r.status != http.StatusOK || r.body["test_status"] != "passed" {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE admin_revisions SET revision = revision + 1 WHERE resource = 'admin.settings.ai'`); err != nil {
		t.Fatal(err)
	}
	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if get.body["test_status"] != "untested" {
		t.Errorf("after the settings moved test_status = %v, want untested: %s", get.body["test_status"], get.raw)
	}
}

// A save whose provider cannot be reached keeps the key, answers 200, and says
// in words that the key could not be tested: never the raw transport error.
func TestWire_ASaveThatCannotReachTheProviderAnswersInWords(t *testing.T) {
	client := unreachableClient(t)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t)).WithAITester(client)
	})
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if r.body["anthropic_configured"] != true {
		t.Errorf("the key was not saved: %s", r.raw)
	}
	if r.body["test_status"] != "failed" {
		t.Errorf("test_status = %v, want failed", r.body["test_status"])
	}
	want := "The key is saved. Anthropic could not be reached to test it; press Test connection later."
	if msg, _ := r.body["test_message"].(string); msg != want {
		t.Errorf("test_message = %q, want %q", msg, want)
	}
	if strings.Contains(string(r.raw), "dial tcp") {
		t.Errorf("the answer holds the raw transport error: %s", r.raw)
	}
}

// unreachableClient is an Anthropic client whose provider address refuses
// every connection.
func unreachableClient(t *testing.T) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return ai.NewClient("").WithProvider(ai.ProviderAnthropic).
		WithAnthropicKey(fakeAnthropicKey).WithAnthropicBaseURL(url)
}
