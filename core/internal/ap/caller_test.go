// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// A session's claims carry roles and no role: the caller's role is judged
// over the set (finance wins when held), never read from the role field.
func TestCallerReadsTheRoleSet(t *testing.T) {
	for _, roles := range [][]string{{"finance", "sales"}, {"sales", "finance"}} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		c := &middleware.UserClaims{Roles: roles}
		c.Subject = "sub-1"
		r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, c))
		got := caller(r)
		if got.Role != "finance" || got.Actor != "sub-1" {
			t.Fatalf("roles %v: caller = %+v, want role finance", roles, got)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, &middleware.UserClaims{}))
	if got := caller(r); got.Role == "" {
		t.Fatal("claims with no role became the role-less in process caller")
	}
}
