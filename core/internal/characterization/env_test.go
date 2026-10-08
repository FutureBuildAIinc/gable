// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"os"
	"strings"
	"testing"
)

// TestSubprocessEnvBlocksOutsideVariables proves the subprocess environment is
// built from the allow list plus the harness's explicit values only: a
// variable held by the caller's shell (a developer's real integration or
// payments key) must never reach the server, because it would change the
// configured behaviour under characterisation - and with it the goldens -
// silently and per machine.
func TestSubprocessEnvBlocksOutsideVariables(t *testing.T) {
	t.Setenv("INTEGRATION_API_KEY", "outside-value-that-must-not-leak")
	t.Setenv("RUN_PAYMENTS_API_KEY", "outside-value-that-must-not-leak")
	t.Setenv("CATEGORY_PRICING_ENABLED", "true")

	env := subprocessEnv(map[string]string{"DATABASE_URL": "postgres://harness"})

	allowed := map[string]bool{}
	for _, k := range envAllowList {
		allowed[k] = true
	}

	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case allowed[k]:
			if os.Getenv(k) == "" {
				t.Errorf("variable %s passed through but is unset in the caller", k)
			}
		case k == "DATABASE_URL":
			if v != "postgres://harness" {
				t.Errorf("DATABASE_URL = %q, want the harness's explicit value", v)
			}
		default:
			t.Errorf("subprocess environment carries %s from the caller's environment", k)
		}
	}

	for _, k := range []string{"INTEGRATION_API_KEY", "RUN_PAYMENTS_API_KEY", "CATEGORY_PRICING_ENABLED"} {
		for _, kv := range env {
			if name, _, _ := strings.Cut(kv, "="); name == k {
				t.Errorf("%s leaked into the subprocess environment", k)
			}
		}
	}
}
