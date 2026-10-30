// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"errors"
	"testing"
)

func TestCheckFloor(t *testing.T) {
	cases := []struct {
		version string
		wantErr bool
	}{
		{"2.1.0", false},
		{"2.0.0", false},
		{"2.1.0-rc1", false},
		{"3.0.0", false},
		{"2.1", false},
		{"1.9.0", true},
		{"1.7.2", true},
		{"0.9.9", true},
		{"garbage", true},
		{"", true},
		{"2", true},
		{"2.x", true},
	}
	for _, tc := range cases {
		err := CheckFloor(tc.version)
		if tc.wantErr && !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("CheckFloor(%q) = %v, want ErrUnsupportedVersion", tc.version, err)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("CheckFloor(%q) = %v, want nil", tc.version, err)
		}
	}
}
