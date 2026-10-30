// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"os"
	"strings"
	"testing"
)

// The staff sign in is gated on the dealer's sign in allowlist, as the portal
// login is: serve.go must hand the staff handler the email_password check.
func TestStaffSignInIsGatedOnTheEmailPasswordAllowlist(t *testing.T) {
	b, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	guard := strings.Index(src, "staffHandler.WithEmailPasswordGuard(")
	register := strings.Index(src, "staffHandler.RegisterRoutes(mux)")
	if guard < 0 || register < 0 || guard > register {
		t.Fatal("serve.go does not gate the staff sign in on the allowlist before registering its routes")
	}
	if !strings.Contains(src[guard:register], "signinmethods.MethodEmailPassword") {
		t.Fatal("the staff sign in guard does not ask for the email_password method")
	}
}
