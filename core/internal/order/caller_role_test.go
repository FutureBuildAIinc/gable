// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

func reqWith(claims *middleware.UserClaims) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if claims == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
}

// A staff session carries roles only; the hold release must judge the set,
// whatever order it arrives in, and refuse claims that carry no role.
func TestCallerRoleJudgesTheWholeRoleSet(t *testing.T) {
	releasable := func(role string) bool { return role == "" || releaseRoles[role] }
	cases := []struct {
		name   string
		claims *middleware.UserClaims
		allow  bool
	}{
		{"no claims (machine key, in process)", nil, true},
		{"finance alone", &middleware.UserClaims{Roles: []string{"finance"}}, true},
		{"sales then finance", &middleware.UserClaims{Roles: []string{"sales", "finance"}}, true},
		{"finance then sales", &middleware.UserClaims{Roles: []string{"finance", "sales"}}, true},
		{"admin among others", &middleware.UserClaims{Roles: []string{"driver", "admin"}}, true},
		{"sales only", &middleware.UserClaims{Roles: []string{"sales"}}, false},
		{"warehouse and cashier", &middleware.UserClaims{Roles: []string{"warehouse", "cashier"}}, false},
		{"claims with no role at all", &middleware.UserClaims{}, false},
	}
	for _, tc := range cases {
		if got := releasable(callerRole(reqWith(tc.claims))); got != tc.allow {
			t.Errorf("%s: release allowed = %v, want %v", tc.name, got, tc.allow)
		}
	}
}

func TestAuditRoleDoesNotDependOnOrder(t *testing.T) {
	a := auditRole(reqWith(&middleware.UserClaims{Roles: []string{"finance", "sales"}}))
	b := auditRole(reqWith(&middleware.UserClaims{Roles: []string{"sales", "finance"}}))
	if a != b || a != "sales" {
		t.Fatalf("audit roles %q and %q, want sales for both", a, b)
	}
}
