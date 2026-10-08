// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// userRole is the sole gate on the emergency price-exposure override and on
// the admin scan. It returns a privileged default ("owner") when claims are
// missing, which is only defensible as a deliberate AUTH_MODE=dev
// affordance. These pin that the privilege requires the explicit opt-in, so a
// deployment that loses its auth middleware fails closed instead of handing
// every anonymous caller an owner-level override.

func requestWithClaims(t *testing.T, claims *middleware.UserClaims) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/x/exposure/override", nil)
	if claims == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
}

func TestUserRoleFailsClosedWithoutDevMode(t *testing.T) {
	t.Setenv("AUTH_MODE", "")
	r := requestWithClaims(t, nil)

	if got := userRole(r); got != "" {
		t.Errorf("userRole with nil claims and AUTH_MODE unset = %q, want %q — a privileged "+
			"default here would open the override endpoint to unauthenticated callers", got, "")
	}
	if got := userIDString(r); got != "" {
		t.Errorf("userIDString with nil claims and AUTH_MODE unset = %q, want %q", got, "")
	}
}

func TestUserRoleGrantsOwnerInDevMode(t *testing.T) {
	t.Setenv("AUTH_MODE", "dev")
	r := requestWithClaims(t, nil)

	if got := userRole(r); got != "owner" {
		t.Errorf("userRole with nil claims under AUTH_MODE=dev = %q, want %q — the demo "+
			"deployment relies on the dev caller acting as owner", got, "owner")
	}
	if got := userIDString(r); got != devActor {
		t.Errorf("userIDString with nil claims under AUTH_MODE=dev = %q, want %q", got, devActor)
	}
}

// Real claims must win over both defaults, in either mode.
func TestUserRolePrefersClaims(t *testing.T) {
	for _, mode := range []string{"", "dev"} {
		t.Run("AUTH_MODE="+mode, func(t *testing.T) {
			t.Setenv("AUTH_MODE", mode)
			r := requestWithClaims(t, &middleware.UserClaims{Role: "sales"})
			if got := userRole(r); got != "sales" {
				t.Errorf("userRole = %q, want %q", got, "sales")
			}
		})
	}
}

// The "roles" array is the fallback when the single-valued "role" claim is
// absent; an empty array must not fall through to the dev-mode default.
func TestUserRoleFromRolesArrayAndEmpty(t *testing.T) {
	t.Setenv("AUTH_MODE", "dev")

	if got := userRole(requestWithClaims(t, &middleware.UserClaims{Roles: []string{"warehouse", "sales"}})); got != "warehouse" {
		t.Errorf("userRole from roles array = %q, want %q", got, "warehouse")
	}
	if got := userRole(requestWithClaims(t, &middleware.UserClaims{})); got != "" {
		t.Errorf("userRole with present-but-roleless claims = %q, want %q — a real token "+
			"carrying no role must not inherit the dev-mode owner default", got, "")
	}
}
