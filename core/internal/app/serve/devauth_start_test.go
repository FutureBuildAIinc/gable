// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/devauth"
)

const runChildEnv = "GABLE_TEST_RUN_SERVE_CHILD"

// TestRunChild is the child half of TestStartRefusesDevMode: when the parent
// sets runChildEnv it runs the real Run, which must end at the refusal. It is
// a no-op in a normal test run.
func TestRunChild(t *testing.T) {
	if os.Getenv(runChildEnv) != "1" {
		t.Skip("child process of TestStartRefusesDevMode")
	}
	Run()
}

// TestStartRefusesDevMode pins ADR 0012 section 7 at the start of the server:
// a binary built without the devauth tag exits with AUTH_MODE=dev, and a dev
// build exits with AUTH_MODE=dev beside the Appwrite settings. The database
// URL points at a closed port, so a start that gets past the refusal would end
// at the database instead and the log would not carry the refusal.
func TestStartRefusesDevMode(t *testing.T) {
	cases := []struct {
		name     string
		needsTag bool
		env      []string
		want     string
	}{
		{"dev mode without the tag", false, []string{"AUTH_MODE=dev"}, "built without the devauth tag"},
		{"dev mode beside Appwrite", true, []string{"AUTH_MODE=dev", "APPWRITE_ENDPOINT=http://127.0.0.1:1/v1"}, "both AUTH_MODE=dev and APPWRITE_ENDPOINT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsTag != devauth.Enabled {
				t.Skipf("case needs a build with the devauth tag = %v", tc.needsTag)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunChild$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				runChildEnv+"=1",
				"DATABASE_URL=postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable",
				"AUTH_MODE=", "APPWRITE_ENDPOINT=", "JWKS_URL=",
			)
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("Run returned normally; want exit 1.\n%s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("log lacks %q.\n%s", tc.want, out)
			}
		})
	}
}
