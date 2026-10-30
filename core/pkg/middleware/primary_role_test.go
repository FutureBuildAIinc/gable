// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import "testing"

func TestClaimsPrimaryRoleIgnoresOrder(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		role  string
		want  string
	}{
		{"owner beats all", []string{"driver", "sales", "owner"}, "", "owner"},
		{"admin beats sales", []string{"sales", "admin"}, "", "admin"},
		{"sales before finance either order", []string{"finance", "sales"}, "", "sales"},
		{"sales before finance other order", []string{"sales", "finance"}, "", "sales"},
		{"single role claim", nil, "finance", "finance"},
		{"role claim beats nothing, roles win by fixed order", []string{"cashier"}, "driver", "cashier"},
		{"unmapped legacy role only", []string{"member"}, "", "member"},
		{"no roles", nil, "", ""},
	}
	for _, tc := range cases {
		got := ClaimsPrimaryRole(&UserClaims{Roles: tc.roles, Role: tc.role})
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if ClaimsPrimaryRole(nil) != "" {
		t.Error("nil claims must give an empty role")
	}
}
