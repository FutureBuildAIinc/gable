// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// The Anthropic provider's settings on the wire: the key is entered by an
// admin, sealed at rest and never read back (FROM-PRINCIPAL 94).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
)

// The key is an obvious placeholder, never a provider key shape.
const fakeAnthropicKey = "test-" + "key-not-real"

func testVault(t *testing.T) *payment.Vault {
	t.Helper()
	v, err := payment.NewVault(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type fakeTester struct{ err error }

func (f fakeTester) TestConnection(context.Context) error { return f.err }

func (f *fixture) settingRow(key string) (string, bool) {
	f.t.Helper()
	var v string
	err := f.db.Pool.QueryRow(context.Background(), `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// RULE: with no vault key a key is refused, never stored in plain text.
func TestWire_AnthropicKeyRefusedWithoutVault(t *testing.T) {
	f := newFixture(t)
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey}, "If-Match", `"1"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("save without a vault = %d: %s", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "vault") {
		t.Errorf("the refusal does not say why: %s", r.raw)
	}
	if strings.Contains(string(r.raw), fakeAnthropicKey) {
		t.Errorf("the refusal echoes the key")
	}
	if _, ok := f.settingRow("anthropic_api_key"); ok {
		t.Error("a key was stored with no vault")
	}
}

// RULE: the key is sealed at rest, never returned, never in the audit row or
// the event; the document carries a masked hint only.
func TestWire_AnthropicKeySealedAndNeverReturned(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t)).WithAuditLog(audit.NewLogger(testutil.RequireDB(t)))
	})
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey, "model": "claude-opus-5-5"},
		"If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if strings.Contains(string(r.raw), fakeAnthropicKey) {
		t.Errorf("the save answer holds the key: %s", r.raw)
	}
	if r.body["provider"] != "anthropic" || r.body["model"] != "claude-opus-5-5" {
		t.Errorf("document = %v", r.body)
	}
	if r.body["anthropic_configured"] != true || r.body["configured"] != true {
		t.Errorf("document = %v", r.body)
	}
	if hint, _ := r.body["anthropic_key_hint"].(string); hint != "****real" {
		t.Errorf("hint = %q, want the last four only", hint)
	}
	stored, ok := f.settingRow("anthropic_api_key")
	if !ok || !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, fakeAnthropicKey) {
		t.Errorf("stored value is not sealed: %q", stored)
	}
	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if strings.Contains(string(get.raw), fakeAnthropicKey) {
		t.Errorf("GET holds the key: %s", get.raw)
	}
	// Neither the audit trail nor the events feed holds it.
	for _, q := range []string{
		`SELECT COALESCE(string_agg(changes::text, ' '), '') FROM audit_log`,
		`SELECT COALESCE(string_agg(data::text, ' '), '') FROM events_outbox WHERE entity_type = 'admin_settings'`,
	} {
		var all string
		if err := f.db.Pool.QueryRow(context.Background(), q).Scan(&all); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if strings.Contains(all, fakeAnthropicKey) {
			t.Errorf("key found by %s", q)
		}
	}
}

// RULE: a model below Sonnet 5.5 is a 400 naming it, and nothing is written.
func TestWire_AnthropicModelRefused(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service { return s.WithVault(testVault(t)) })
	r := f.do("PUT", "/api/v1/admin/settings/ai",
		map[string]any{"provider": "anthropic", "anthropic_api_key": fakeAnthropicKey, "model": "claude-haiku-5-5"},
		"If-Match", `"1"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("haiku = %d: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) != 1 || details[0]["field"] != "model" || !strings.Contains(details[0]["message"].(string), "claude-haiku-5-5") {
		t.Errorf("details = %v", details)
	}
	if _, ok := f.settingRow("anthropic_api_key"); ok {
		t.Error("a refused save wrote the key")
	}
}

// RULE: the OpenRouter key is sealed too when a vault key exists, and a
// legacy plaintext row is sealed by the read time upgrade without loss.
func TestWire_OpenRouterKeySealedAndLegacyUpgraded(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service { return s.WithVault(testVault(t)) })
	r := f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "or-" + "test-not-real"}, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if v, _ := f.settingRow("openrouter_api_key"); !strings.HasPrefix(v, "enc:v1:") {
		t.Errorf("openrouter key not sealed: %q", v)
	}
	// A legacy plaintext row.
	_, err := f.db.Pool.Exec(context.Background(),
		`UPDATE system_settings SET value = 'legacy-not-real' WHERE key = 'openrouter_api_key'`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.SealLegacySettings(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("SealLegacySettings = (%d, %v), want (1, nil)", n, err)
	}
	v, _ := f.settingRow("openrouter_api_key")
	if !strings.HasPrefix(v, "enc:v1:") || strings.Contains(v, "legacy-not-real") {
		t.Errorf("legacy row not sealed: %q", v)
	}
	opened, err := testVault(t).Open(v)
	if err != nil || opened != "legacy-not-real" {
		t.Errorf("sealed legacy value does not open back: (%q, %v)", opened, err)
	}
	if n, _ := f.svc.SealLegacySettings(context.Background()); n != 0 {
		t.Errorf("second upgrade sealed %d rows, want 0", n)
	}
}

// RULE: test connection answers ok, or the provider's error in words, and
// never the key.
func TestWire_AITestConnection(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithAITester(fakeTester{err: nil})
	})
	r := f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.status != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("ok test = %d: %s", r.status, r.raw)
	}
	// The same fixture (it holds the outbox lock), the tester swapped.
	f.svc.WithAITester(fakeTester{err: errors.New("Anthropic API error (401): invalid x-api-key")})
	r = f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.status != http.StatusOK || r.body["ok"] != false {
		t.Fatalf("failed test = %d: %s", r.status, r.raw)
	}
	if msg, _ := r.body["message"].(string); !strings.Contains(msg, "401") {
		t.Errorf("message = %q", msg)
	}
}
