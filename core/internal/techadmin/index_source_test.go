// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// The market index source settings document (QE-07-b1): where a deployment's
// market index values come from. Manual is the default and the fallback; the
// FB Connect feed is configured here and pulled by a later slice. The feed
// key is a credential stored the way the AI settings store theirs: sealed at
// rest with the payment vault, never returned, never logged, never in an
// audit row's changes.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
)

// lockedBuffer collects log output from the handler goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeFeedKey is an obvious placeholder shaped like a feed credential: its
// body is a repeated stub, never a real key.
var fakeFeedKey = "fk_test_" + strings.Repeat("teststub", 4)

// resetIndexSource returns the settings row to its seeded defaults, so each
// test starts from manual and leaves nothing behind.
func (f *fixture) resetIndexSource() {
	_, _ = f.db.Pool.Exec(context.Background(),
		`UPDATE market_index_source SET source = 'manual', feed_url = '', feed_key = NULL,
		 staleness_days = 7, last_pull_at = NULL, last_pull_ok = NULL, last_pull_error = NULL,
		 revision = 1, updated_at = NOW()`)
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM audit_log WHERE entity_type = 'admin_settings'`)
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM events_outbox WHERE entity_type = 'admin_settings'`)
}

// RULE: a fresh deployment reads manual with the seven day staleness
// threshold, no feed configured, and revision 1 on the ETag.
func TestQE07_IndexSourceDefaultsManual(t *testing.T) {
	f := newFixture(t)
	f.resetIndexSource()
	get := f.do("GET", "/api/v1/admin/settings/market-index-source", nil)
	if get.status != http.StatusOK {
		t.Fatalf("GET = %d: %s", get.status, get.raw)
	}
	if get.body["source"] != "manual" {
		t.Errorf("source = %v, want manual", get.body["source"])
	}
	if num(t, get.body, "staleness_days") != 7 {
		t.Errorf("staleness_days = %v, want 7", get.body["staleness_days"])
	}
	if get.body["feed_url"] != "" {
		t.Errorf("feed_url = %v, want empty", get.body["feed_url"])
	}
	if get.body["feed_key_set"] != false {
		t.Errorf("feed_key_set = %v, want false", get.body["feed_key_set"])
	}
	if get.body["last_pull_at"] != nil {
		t.Errorf("last_pull_at = %v, want null until the pull exists", get.body["last_pull_at"])
	}
	if etag := get.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want the revision", etag)
	}
	if num(t, get.body, "revision") != 1 {
		t.Errorf("revision = %v, want 1", get.body["revision"])
	}
}

// RULE: the feed key is write only. It is sealed at rest, never returned
// (only its masked hint), never logged, and never in an audit row's changes
// (the row records only that it changed). A write without a precondition is
// 428 and a stale one 409.
func TestQE07_IndexSourceKeyNeverReturned(t *testing.T) {
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t))
	})
	// The shared fixture wires no audit sink; the key's audit row is what
	// this test reads, so the service gets the real logger.
	f.svc.WithAuditLog(audit.NewLogger(f.db))
	f.resetIndexSource()

	// A write with no precondition is refused, nothing written.
	bare := f.do("PUT", "/api/v1/admin/settings/market-index-source",
		map[string]any{"source": "fb_connect_feed"})
	if bare.status != http.StatusPreconditionRequired {
		t.Fatalf("PUT without a precondition = %d, want 428", bare.status)
	}

	// Nothing the request touches may log the key either: the save runs with
	// the default logger captured.
	var logBuf lockedBuffer
	quiet := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prevLogger := slog.Default()
	slog.SetDefault(quiet)
	save := f.do("PUT", "/api/v1/admin/settings/market-index-source", map[string]any{
		"source":         "fb_connect_feed",
		"feed_url":       "https://connect.example.test/feed",
		"feed_key":       fakeFeedKey,
		"staleness_days": 3,
	}, "If-Match", `"1"`)
	if save.status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", save.status, save.raw)
	}
	slog.SetDefault(prevLogger)
	if strings.Contains(logBuf.String(), fakeFeedKey) {
		t.Errorf("the save logged the key: %s", logBuf.String())
	}
	if save.body["source"] != "fb_connect_feed" || save.body["feed_url"] != "https://connect.example.test/feed" {
		t.Errorf("saved document = %v", save.body)
	}
	if hint, _ := save.body["feed_key_hint"].(string); hint != "****stub" {
		t.Errorf("feed_key_hint = %q, want four asterisks and the last four", hint)
	}
	if strings.Contains(string(save.raw), fakeFeedKey) {
		t.Errorf("the save's answer holds the key: %s", save.raw)
	}
	if etag := save.header.Get("ETag"); etag != `"2"` {
		t.Errorf("ETag after save = %q, want the moved revision", etag)
	}

	// The stored row is sealed, not the key.
	var stored string
	err := f.db.Pool.QueryRow(context.Background(),
		`SELECT feed_key FROM market_index_source`).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, fakeFeedKey) {
		t.Errorf("stored feed key is not sealed: %.20s...", stored)
	}

	// The read never carries it.
	get := f.do("GET", "/api/v1/admin/settings/market-index-source", nil)
	if get.status != http.StatusOK || strings.Contains(string(get.raw), fakeFeedKey) {
		t.Errorf("GET = %d and holds the key: %s", get.status, get.raw)
	}

	// Neither the audit trail nor the events feed holds it, and the audit row
	// records only that the key changed.
	for _, q := range []string{
		`SELECT COALESCE(string_agg(changes::text, ' '), '') FROM audit_log WHERE entity_type = 'admin_settings'`,
		`SELECT COALESCE(string_agg(data::text, ' '), '') FROM events_outbox WHERE entity_type = 'admin_settings'`,
	} {
		var all string
		if err := f.db.Pool.QueryRow(context.Background(), q).Scan(&all); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if strings.Contains(all, fakeFeedKey) {
			t.Errorf("the key found by %s", q)
		}
	}
	var changed bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE((changes->>'feed_key_changed')::boolean, false) FROM audit_log
		 WHERE entity_type = 'admin_settings' AND action = 'settings.saved'`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("the audit row does not record that the feed key changed")
	}

	// A stale revision is a 409, and a second save that leaves the key keeps it.
	stale := f.do("PUT", "/api/v1/admin/settings/market-index-source",
		map[string]any{"staleness_days": 5}, "If-Match", `"1"`)
	if stale.status != http.StatusConflict {
		t.Fatalf("PUT on a stale revision = %d, want 409", stale.status)
	}
	keep := f.do("PUT", "/api/v1/admin/settings/market-index-source",
		map[string]any{"staleness_days": 5}, "If-Match", `"2"`)
	if keep.status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", keep.status, keep.raw)
	}
	if keep.body["feed_key_set"] != true || keep.body["feed_key_hint"] == nil {
		t.Errorf("a save that leaves the key lost it: %v", keep.body["feed_key_hint"])
	}

	// An empty feed_key clears it.
	clear := f.do("PUT", "/api/v1/admin/settings/market-index-source",
		map[string]any{"feed_key": ""}, "If-Match", `"3"`)
	if clear.status != http.StatusOK {
		t.Fatalf("PUT clear = %d: %s", clear.status, clear.raw)
	}
	if clear.body["feed_key_set"] != false || clear.body["feed_key_hint"] != nil {
		t.Errorf("an empty key did not clear it: %v", clear.body)
	}
}

// RULE: only an administrator or owner reaches the market index source
// settings; any other role is a 403 before anything is read or written. The
// PUT's own 400 on an empty body proves the allowed roles reach the handler.
func TestQE07_IndexSourceAdminOnly(t *testing.T) {
	svc := techadmin.NewService(nil)
	mux := http.NewServeMux()
	techadmin.NewHandler(svc).RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	call := func(role, method, path string, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if role != "" {
			ctx := context.WithValue(req.Context(), middleware.UserContextKey, &middleware.UserClaims{Role: role})
			req = req.WithContext(ctx)
		}
		mux.ServeHTTP(rec, req)
		return rec
	}
	const path = "/api/v1/admin/settings/market-index-source"
	for _, role := range []string{"sales", "finance", "warehouse", "cashier", "driver"} {
		if rec := call(role, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET as %s = %d, want 403", role, rec.Code)
		}
		if rec := call(role, http.MethodPut, path, "{}"); rec.Code != http.StatusForbidden {
			t.Errorf("PUT as %s = %d, want 403", role, rec.Code)
		}
	}
	for _, role := range []string{"admin", "owner"} {
		if rec := call(role, http.MethodPut, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT as %s = %d, want the handler's own 400", role, rec.Code)
		}
	}
}
