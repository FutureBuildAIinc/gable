// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No handler may decide on the single role claim alone, because a staff
// session sets roles and never role (ADR 0012 section 2). This scan finds every
// read of the role claim in the core's sources outside the auth layer and the
// portal's own claims, and pins each remaining one to a reviewed file.
func TestNoHandlerReadsTheRoleClaimAlone(t *testing.T) {
	// Files that read claims.Role and are judged safe: they read it together
	// with Roles, or it is a different claims type.
	reviewed := map[string]string{
		"internal/account/service.go":              "RoleOf reads Role and Roles together",
		"internal/techadmin/handler.go":            "owner check reads Role and Roles",
		"internal/pos/handler.go":                  "empty check reads Role and Roles",
		"internal/payment/service.go":              "c is a Caller built from RoleOf",
		"internal/order/handler.go":                "callerRole reads the set; audit role from ClaimsPrimaryRole",
		"internal/portal/handler.go":               "the portal's own claims type",
		"internal/portal/handlers_core.go":         "the portal's own claims type",
		"internal/portal/handlers_phase2.go":       "the portal's own claims type",
		"internal/customerquote/handler.go":        "the portal's own claims type",
		"internal/customer/repository_children.go": "a contact's role, not a claim",
		"pkg/middleware/auth.go":                   "the auth layer",
		"pkg/middleware/branch.go":                 "the claims helpers",
		"pkg/middleware/keybranch_shared.go":       "reads Role and Roles",
		"pkg/middleware/portal_auth.go":            "the portal's own chain",
	}
	read := regexp.MustCompile(`\b(?:claims|c|liveClaims|cl)\.Role\b`)
	root := filepath.Join("..", "..")
	var bad []string
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(path)
			if !read.Match(b) {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, ok := reviewed[rel]; !ok {
				bad = append(bad, rel)
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("these files read the role claim and are not reviewed (read Roles as well, or use middleware.ClaimsHaveAnyRole): %v", bad)
	}
}
