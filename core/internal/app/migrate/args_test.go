// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package migrate

import "testing"

// TestParseArgs pins the one flag migrate takes: -report names a read only
// pre flight report (units is the one there is); no flag means apply the
// migrations; anything else is refused.
func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		report  string
		refused bool
	}{
		{nil, "", false},
		{[]string{"-report", "units"}, "units", false},
		{[]string{"-report=units"}, "units", false},
		{[]string{"-report", "orders"}, "", true},
		{[]string{"-report"}, "", true},
		{[]string{"--port", "9999"}, "", true},
		{[]string{"units"}, "", true},
	} {
		opts, err := ParseArgs(tc.args)
		if tc.refused {
			if err == nil {
				t.Errorf("ParseArgs(%v) = %+v, want a refusal", tc.args, opts)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseArgs(%v): %v", tc.args, err)
			continue
		}
		if opts.Report != tc.report {
			t.Errorf("ParseArgs(%v).Report = %q, want %q", tc.args, opts.Report, tc.report)
		}
	}
}
