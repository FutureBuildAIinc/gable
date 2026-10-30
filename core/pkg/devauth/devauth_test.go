// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package devauth

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		mode     string
		endpoint string
		wantErr  string
	}{
		{"release build, no dev mode", false, "", "", ""},
		{"release build, production mode", false, "production", "https://aw.example", ""},
		{"release build refuses dev mode", false, "dev", "", "built without the devauth tag"},
		{"release build refuses dev mode in any case", false, "DEV", "", "built without the devauth tag"},
		{"dev build, no dev mode", true, "", "", ""},
		{"dev build, dev mode, no Appwrite", true, "dev", "", ""},
		{"dev build refuses dev mode with Appwrite", true, "dev", "http://127.0.0.1:8580/v1", "both AUTH_MODE=dev and APPWRITE_ENDPOINT"},
		{"dev build, Appwrite without dev mode", true, "", "http://127.0.0.1:8580/v1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.enabled, tc.mode, tc.endpoint)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestEnabledMatchesBuild pins the tag wiring: the CI jobs that need dev auth
// build with -tags devauth, and the image to publish builds without it.
func TestEnabledMatchesBuild(t *testing.T) {
	if Enabled != tagged {
		t.Fatalf("Enabled=%v but the devauth tag is %v", Enabled, tagged)
	}
}
