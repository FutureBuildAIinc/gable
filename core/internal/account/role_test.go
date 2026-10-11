// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

import (
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/pkg/middleware"
)

// RULE (ADR 0005 9.2 and 9.4, review of PR 55): the finance check reads the
// caller's whole role set. A signed in user with no finance role, or with no
// role at all, is refused; only a caller with no claims (a machine key, an in
// process call, dev mode) is passed on to its own scope check. The route guard
// honours claims.Roles as well as claims.Role, so the in service check does too.
func TestRoleOfReadsTheWholeRoleSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims *middleware.UserClaims
		want   string // "" passes the service check; anything else is judged by FinanceRole
		ok     bool   // whether account.FinanceRole accepts what RoleOf answered
	}{
		{name: "no claims is a machine key or an in process call", claims: nil, want: "", ok: true},
		{name: "role claim", claims: &middleware.UserClaims{Role: "finance"}, want: "finance", ok: true},
		{name: "roles array only, finance among them", claims: &middleware.UserClaims{Roles: []string{"sales", "finance"}}, want: "finance", ok: true},
		{name: "roles array only, none finance", claims: &middleware.UserClaims{Roles: []string{"sales"}}, want: "sales", ok: false},
		{name: "claims with no role at all", claims: &middleware.UserClaims{}, want: "none", ok: false},
		{name: "role claim not finance but roles array finance", claims: &middleware.UserClaims{Role: "member", Roles: []string{"admin"}}, want: "admin", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := account.RoleOf(tc.claims)
			if got != tc.want {
				t.Fatalf("RoleOf = %q, want %q", got, tc.want)
			}
			passes := got == "" || account.FinanceRole(got)
			if passes != tc.ok {
				t.Errorf("the finance check passes = %v, want %v", passes, tc.ok)
			}
		})
	}
}
