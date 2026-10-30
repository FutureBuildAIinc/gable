// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/gablelbm/gable/pkg/middleware"
)

// Every role a guard in this package names must be one a staff session can
// hold, or the guard can never admit one (ADR 0012 section 5).
func TestEveryGuardRoleIsInTheStaffVocabulary(t *testing.T) {
	vocab := map[string]bool{}
	for _, r := range middleware.StaffRoles() {
		vocab[r] = true
	}
	files, _ := filepath.Glob("*.go")
	guard := regexp.MustCompile(`RequireRole\(([^)]*)\)`)
	quoted := regexp.MustCompile(`"([a-z_]+)"`)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range guard.FindAllStringSubmatch(string(b), -1) {
			for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
				found++
				if !vocab[q[1]] {
					t.Errorf("%s: guard names %q, which no staff session can hold", f, q[1])
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("the scan found no guard roles; the pattern is stale")
	}
}

// The uploads route admits exactly the mapped staff roles, and "user" is gone.
func TestUploadsGuardAdmitsTheStaffRolesOnly(t *testing.T) {
	want := []string{"admin", "cashier", "driver", "finance", "owner", "purchasing", "sales", "warehouse"}
	got := append([]string(nil), uploadsRoles...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uploads roles = %v, want %v", got, want)
	}
}

// The role mapping the Appwrite side yields is the vocabulary the guards use.
func TestAppwriteMappingCoversTheVocabulary(t *testing.T) {
	var prefixed []string
	for _, r := range middleware.StaffRoles() {
		prefixed = append(prefixed, appwrite.RolePrefix+r)
	}
	got := appwrite.MapRoles(prefixed)
	if !reflect.DeepEqual(got, middleware.StaffRoles()) {
		t.Fatalf("MapRoles = %v, want %v", got, middleware.StaffRoles())
	}
}
