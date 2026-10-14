// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// Review round 1 fixes for the AI settings: a save takes effect at once, the
// page can switch back to OpenRouter, a key the vault cannot open reads as AI
// off, and only an administrator reaches the test connection.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/pkg/middleware"
)

// aiClientOn builds the production shaped AI client (DB backed stores with
// their TTL cache) against the fixture's database, with the Anthropic
// provider pointed at a fake server.
func (f *fixture) aiClientOn(vault ai.Sealer, anthropicURL string) *ai.Client {
	pool := f.db.Pool
	return ai.NewClientWithKeyStore(ai.NewKeyStore(pool, "openrouter_api_key", "").WithSealer(vault)).
		WithBaseURLStore(ai.NewKeyStore(pool, "openrouter_base_url", "")).
		WithProviderStore(ai.NewKeyStore(pool, ai.SettingProvider, ai.ProviderOpenRouter)).
		WithAnthropicKeyStore(ai.NewKeyStore(pool, ai.SettingAnthropicKey, "").WithSealer(vault)).
		WithAnthropicModelStore(ai.NewKeyStore(pool, ai.SettingAnthropicModel, ai.DefaultAnthropicModel)).
		WithAnthropicBaseURL(anthropicURL)
}

func (f *fixture) fakeAnthropic(hits *int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"p"}]}`))
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

func revisionOf(t *testing.T, r resp) string {
	t.Helper()
	n, ok := r.body["revision"]
	if !ok {
		t.Fatalf("no revision in %s", r.raw)
	}
	return fmt.Sprintf(`"%v"`, n)
}

// RULE: a save takes effect at once: the test connection right after saving
// an Anthropic key reaches the Anthropic server, not the cached old provider.
func TestWire_SaveTakesEffectAtOnce(t *testing.T) {
	vault := testVault(t)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service { return s.WithVault(vault) })
	var hits int32
	client := f.aiClientOn(vault, f.fakeAnthropic(&hits).URL)
	f.svc.WithAITester(client)

	// Warm the client's caches with the old settings (AI off).
	r := f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.body["ok"] != false {
		t.Fatalf("before any key: %s", r.raw)
	}
	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	save := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey},
		"If-Match", revisionOf(t, get))
	if save.status != http.StatusOK {
		t.Fatalf("save = %d: %s", save.status, save.raw)
	}
	r = f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.body["ok"] != true {
		t.Fatalf("test right after the save = %s", r.raw)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("the Anthropic server received %d calls, want 1", n)
	}

	// A delete takes effect at once too: the old key is not used again.
	cur := f.do("GET", "/api/v1/admin/settings/ai", nil)
	del := f.do("DELETE", "/api/v1/admin/settings/ai", nil, "If-Match", revisionOf(t, cur))
	if del.status != http.StatusNoContent && del.status != http.StatusOK {
		t.Fatalf("delete = %d: %s", del.status, del.raw)
	}
	r = f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.body["ok"] != false {
		t.Errorf("test after delete = %s", r.raw)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("a deleted key still reached the server (%d calls)", n)
	}
}

// RULE: the page switches back to OpenRouter by provider alone when an
// OpenRouter key is stored, without deleting anything.
func TestWire_SwitchBackToOpenRouter(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service { return s.WithVault(testVault(t)) })
	r := f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "or-test-key-not-real"}, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("or key = %d: %s", r.status, r.raw)
	}
	r = f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", revisionOf(t, r))
	if r.status != http.StatusOK || r.body["provider"] != "anthropic" {
		t.Fatalf("to anthropic = %d: %s", r.status, r.raw)
	}
	r = f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "openrouter", "model": "claude-sonnet-5-5"}, "If-Match", revisionOf(t, r))
	if r.status != http.StatusOK {
		t.Fatalf("switch back = %d: %s", r.status, r.raw)
	}
	if r.body["provider"] != "openrouter" || r.body["configured"] != true {
		t.Errorf("document = %s", r.raw)
	}
	if _, ok := f.settingRow("openrouter_api_key"); !ok {
		t.Error("the OpenRouter key was deleted")
	}
	if _, ok := f.settingRow("anthropic_api_key"); !ok {
		t.Error("the Anthropic key was deleted")
	}
	// An empty body still says what is missing.
	bad := f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{}, "If-Match", revisionOf(t, r))
	if bad.status != http.StatusBadRequest {
		t.Errorf("empty save = %d", bad.status)
	}
}

// RULE: a stored key the vault cannot open (the vault key changed) reads as
// AI off, never a 500, and says it is not configured.
func TestWire_UnreadableKeyReadsAsAIOff(t *testing.T) {
	vaultA := testVault(t)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service { return s.WithVault(vaultA) })
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	vaultB, err := payment.NewVault(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	var hits int32
	client := f.aiClientOn(vaultB, f.fakeAnthropic(&hits).URL)
	f.svc.WithVault(vaultB).WithAITester(client)

	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if get.status != http.StatusOK {
		t.Fatalf("GET = %d: %s", get.status, get.raw)
	}
	if get.body["anthropic_configured"] != false || get.body["configured"] != false {
		t.Errorf("an unreadable key reads as configured: %s", get.raw)
	}
	r = f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.status != http.StatusOK || r.body["ok"] != false {
		t.Errorf("test = %d: %s", r.status, r.raw)
	}
	if msg, _ := r.body["message"].(string); !strings.Contains(msg, "AI is off") {
		t.Errorf("message = %q", msg)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("a sealed value was sent: %d calls", n)
	}
	if client.IsConfigured(context.Background()) {
		t.Error("client reads as configured")
	}
}

// RULE: only an administrator or owner reaches POST .../ai/test; any other
// role is a 403 before the provider is called.
func TestWire_AITestRefusesNonAdministrators(t *testing.T) {
	var calls int32
	svc := techadmin.NewService(nil).WithAITester(countingTester{n: &calls})
	mux := http.NewServeMux()
	techadmin.NewHandler(svc).RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	as := func(role string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &middleware.UserClaims{Role: role})
			mux.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	for _, role := range []string{"sales", "finance", "warehouse", "cashier", "driver"} {
		rec := httptest.NewRecorder()
		as(role).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/ai/test", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", role, rec.Code)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("the provider was called %d times for refused roles", n)
	}
	for _, role := range []string{"admin", "owner"} {
		rec := httptest.NewRecorder()
		as(role).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/ai/test", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", role, rec.Code)
		}
	}
}

type countingTester struct{ n *int32 }

func (c countingTester) TestConnection(context.Context) error { atomic.AddInt32(c.n, 1); return nil }
