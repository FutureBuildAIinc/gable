// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tax

import "testing"

// RULE: the base URL override replaces the environment's URL; without one the
// environment decides, as before.
func TestAvalaraConfigBaseURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  AvalaraConfig
		want string
	}{
		{"sandbox default", AvalaraConfig{Environment: "sandbox"}, "https://sandbox-rest.avatax.com"},
		{"production", AvalaraConfig{Environment: "production"}, "https://rest.avatax.com"},
		{"override wins", AvalaraConfig{Environment: "production", BaseURLOverride: "http://127.0.0.1:1"}, "http://127.0.0.1:1"},
	}
	for _, c := range cases {
		if got := c.cfg.BaseURL(); got != c.want {
			t.Errorf("%s: BaseURL() = %q, want %q", c.name, got, c.want)
		}
	}
}
