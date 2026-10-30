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

func withClaims(roles ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, &middleware.UserClaims{Roles: roles}))
}

// The exposure decisions ask for membership over the whole role set. A session
// holding sales and finance is scoped like sales in either order; owner or
// admin wins over sales.
func TestHoldsRoleIsOrderIndependent(t *testing.T) {
	for _, roles := range [][]string{{"sales", "finance"}, {"finance", "sales"}} {
		r := withClaims(roles...)
		if !holdsRole(r, "sales") || holdsRole(r, "owner", "admin") {
			t.Fatalf("roles %v: sales=%v ownerOrAdmin=%v", roles, holdsRole(r, "sales"), holdsRole(r, "owner", "admin"))
		}
		if userRole(r) != "sales" {
			t.Fatalf("roles %v: audit role %q, want sales", roles, userRole(r))
		}
	}
	r := withClaims("sales", "admin")
	if !holdsRole(r, "owner", "admin") {
		t.Fatal("admin among roles was not seen")
	}
	if holdsRole(withClaims("finance"), "owner", "admin") {
		t.Fatal("finance passed an owner/admin check")
	}
}
