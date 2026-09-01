// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// reached reports whether the guarded handler ran, alongside the status.
func runGuard(t *testing.T, roles []string, claims *UserClaims) (int, bool) {
	t.Helper()
	reached := false
	h := RequireRole(roles...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/staff", nil)
	if claims != nil {
		r = r.WithContext(context.WithValue(r.Context(), UserContextKey, claims))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, reached
}

// withDevBypass sets the process-wide bypass for one test and puts it back.
func withDevBypass(t *testing.T, on bool) {
	t.Helper()
	prev := devAuthBypass.Load()
	SetDevAuthBypass(on)
	t.Cleanup(func() { SetDevAuthBypass(prev) })
}

// RequireRole used to pass an unauthenticated request straight through,
// reading "no claims" as "dev mode". A route registered under one of the
// prefixes the JWT middleware skips — /api/portal/v1/, /api/integration/,
// /api/v1/a2a/ — would therefore have been completely unauthenticated while
// looking guarded. Nothing was exposed at the time; the point is that the next
// route added there would have been, silently.
func TestRequireRole_FailsClosedOnMissingClaims(t *testing.T) {
	withDevBypass(t, false)

	status, reached := runGuard(t, []string{"admin", "owner"}, nil)
	if reached {
		t.Error("an unauthenticated request reached the guarded handler")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
}

// The dev bypass has to be DECLARED, not inferred. A process that never calls
// SetDevAuthBypass gets the closed behaviour — which is what makes the default
// safe for any binary or test that forgets.
func TestRequireRole_DevBypassIsOptIn(t *testing.T) {
	withDevBypass(t, true)

	status, reached := runGuard(t, []string{"admin"}, nil)
	if !reached {
		t.Error("AUTH_MODE=dev: an unauthenticated request must pass through, or the whole dev demo 401s")
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
}

// The bypass is about MISSING claims only. A request that IS authenticated
// must still be role-checked even in dev, or dev mode would quietly become a
// full authorization bypass for authenticated callers too.
func TestRequireRole_DevBypassDoesNotSkipRoleCheckForAuthenticatedCallers(t *testing.T) {
	withDevBypass(t, true)

	status, reached := runGuard(t, []string{"admin", "owner"}, &UserClaims{Role: "sales"})
	if reached {
		t.Error("a caller with the wrong role reached the handler under the dev bypass")
	}
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
}

// Positive controls: both claim shapes the guard understands still admit.
func TestRequireRole_AdmitsAllowedRoles(t *testing.T) {
	withDevBypass(t, false)

	for name, claims := range map[string]*UserClaims{
		"brain single role": {Role: "admin"},
		"oidc roles array":  {Roles: []string{"warehouse", "owner"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, reached := runGuard(t, []string{"admin", "owner"}, claims)
			if !reached || status != http.StatusOK {
				t.Errorf("status = %d, reached = %v; want 200/true", status, reached)
			}
		})
	}
}

// And an authenticated caller with no matching role is still 403, not 401 —
// the two failures are different and an operator reading logs needs them apart.
func TestRequireRole_RefusesUnmatchedRoleWith403(t *testing.T) {
	withDevBypass(t, false)

	status, reached := runGuard(t, []string{"admin"}, &UserClaims{Role: "driver", Roles: []string{"driver"}})
	if reached {
		t.Error("a caller with the wrong role reached the handler")
	}
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
}
