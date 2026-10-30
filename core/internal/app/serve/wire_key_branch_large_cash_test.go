// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"
	"testing"
)

// TestKeyBranchPin_TheLargeCashRoutesHoldTheirClass drives the four routes
// the large cash rules (CP-17a) added through the production chain with a
// branch bound machine key: the two dealer wide writes are refused 403 by
// the gate, and the preview and the settings read are admitted under their
// own class (the preview walled to the sale's branch, the read a stated
// limit). The table holds the decision; this holds the table to it.
func TestKeyBranchPin_TheLargeCashRoutesHoldTheirClass(t *testing.T) {
	f := newAllowlistGateFixture(t)
	cases := []struct {
		method  string
		path    string
		want403 bool
	}{
		{method: "POST", path: "/api/v1/pos/large-cash/preview", want403: false},
		{method: "GET", path: "/api/v1/admin/large-cash/settings", want403: false},
		{method: "PUT", path: "/api/v1/admin/large-cash/settings", want403: true},
		{method: "PATCH", path: "/api/v1/products/11111111-1111-1111-1111-111111111111/consumer-durable", want403: true},
	}
	for _, c := range cases {
		got := f.call(t, f.bound, c.method, c.path)
		if (got == http.StatusForbidden) != c.want403 {
			t.Errorf("bound key %s %s: %d, want %s", c.method, c.path, got, allowlistWant(c.want403))
		}
	}
}
