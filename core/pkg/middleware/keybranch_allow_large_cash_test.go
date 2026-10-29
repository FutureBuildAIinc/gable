// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// The large cash rules (CP-17a) added four routes a machine key can reach.
// Each holds one class in the key branch allow table, and each class is a
// decision about what a machine key may do: the preview reads the sale the
// key's own branch holds (walled), the dealer's settings read as a stated
// limit, and the two writes are dealer wide configuration a machine key is
// refused. This pins the four decisions by name, so a reclassifying of any
// of them fails here instead of silently changing what a machine key may do.
func TestKeyBranchAllowlist_TheLargeCashRoutesHoldTheirClass(t *testing.T) {
	cases := []struct {
		method  string
		pattern string
		want    middleware.KeyBranchRouteClass
		why     string
	}{
		{
			method:  "POST",
			pattern: "/api/v1/pos/large-cash/preview",
			want:    middleware.KeyBranchClassWalled,
			why:     "the preview reads the sale the key's own branch holds, exactly as its completion",
		},
		{
			method:  "GET",
			pattern: "/api/v1/admin/large-cash/settings",
			want:    middleware.KeyBranchClassStatedLimit,
			why:     "the dealer's settings are dealer wide configuration, read under a stated limit",
		},
		{
			method:  "PUT",
			pattern: "/api/v1/admin/large-cash/settings",
			want:    middleware.KeyBranchClassRefused,
			why:     "a dealer wide configuration write is refused a machine key",
		},
		{
			method:  "PATCH",
			pattern: "/api/v1/products/{id}/consumer-durable",
			want:    middleware.KeyBranchClassRefused,
			why:     "the product catalogue is dealer wide, so a machine key may not write it",
		},
	}
	for _, c := range cases {
		class, ok := middleware.KeyBranchRouteClassFor(c.method, c.pattern)
		if !ok {
			t.Errorf("%s %s is a machine key reachable route the allowlist names nowhere: give it a class decision",
				c.method, c.pattern)
			continue
		}
		if class != c.want {
			t.Errorf("%s %s holds the class %s, want %s (%s)", c.method, c.pattern, class, c.want, c.why)
		}
		if allowed := middleware.KeyBranchRouteAllowed(c.method, c.pattern); allowed != (c.want != middleware.KeyBranchClassRefused) {
			t.Errorf("%s %s is allowed %v, want %v for the class %s", c.method, c.pattern, allowed,
				c.want != middleware.KeyBranchClassRefused, c.want)
		}
	}
}
