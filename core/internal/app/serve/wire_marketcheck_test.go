// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestMarketCheck_RealWalls drives the three Market check routes through the
// composed chain (the machine key core, the allowlist gate, the role guards
// registerMarketCheck mounts): roles, a branch bound key, and a key without
// the settings scope.
func TestMarketCheck_RealWalls(t *testing.T) {
	f := newKeyPinFixture(t)
	const settings = "/api/v1/admin/market-check/settings"
	const run = "/api/v1/pricing/market-check"
	runBody := `{"product_ids":["` + uuid.NewString() + `"]}`

	// Users: only admin and owner.
	for _, role := range []string{"cashier", "sales", "finance", "warehouse"} {
		for _, rt := range []struct{ method, path, body string }{
			{"GET", settings, ""}, {"PUT", settings, `{"flow_name":"x"}`}, {"POST", run, runBody},
		} {
			if status, buf := f.userAs(t, role, rt.method, rt.path, rt.body, nil); status != http.StatusForbidden {
				t.Errorf("%s %s %s: %d %s, want 403", role, rt.method, rt.path, status, buf)
			}
		}
	}
	for _, role := range []string{"admin", "owner"} {
		if status, buf := f.userAs(t, role, "GET", settings, "", nil); status != http.StatusOK {
			t.Errorf("%s GET settings: %d %s", role, status, buf)
		}
		// Not configured: the run is reached and answers in words, not a wall.
		if status, buf := f.userAs(t, role, "POST", run, runBody, nil); status != http.StatusConflict {
			t.Errorf("%s POST run: %d %s, want 409 not_configured", role, status, buf)
		}
	}

	// A branch bound key holding admin:settings is refused the write and the run.
	if status, buf := f.key(t, f.bound, "PUT", settings, `{"flow_name":"x"}`, nil); status != http.StatusForbidden {
		t.Errorf("bound key PUT settings: %d %s, want 403", status, buf)
	}
	if status, buf := f.key(t, f.bound, "POST", run, runBody, nil); status != http.StatusForbidden {
		t.Errorf("bound key POST run: %d %s, want 403", status, buf)
	}
	// An unbound key with admin:settings reads the settings (a stated limit).
	if status, buf := f.key(t, f.unbound, "GET", settings, "", nil); status != http.StatusOK {
		t.Errorf("unbound key GET settings: %d %s", status, buf)
	}

	// An unbound key without admin:settings is refused both settings routes.
	narrow := f.mintWithScopes(t, "market check narrow", []string{"pricing:read", "pricing:write"}, nil)
	for _, rt := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"flow_name":"x"}`}} {
		if status, buf := f.key(t, narrow, rt.method, settings, rt.body, nil); status != http.StatusForbidden {
			t.Errorf("narrow key %s settings: %d %s, want 403", rt.method, status, buf)
		}
	}
}
