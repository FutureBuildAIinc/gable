// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package routecensus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRoutesFileMatchesSources regenerates the route census from the Go
// sources and compares it against the committed api/ROUTES.txt. It needs no
// database: the census is a source walk. When the two disagree the failure
// names every added and removed route and the command that regenerates the
// file, so a route cannot silently appear or disappear.
func TestRoutesFileMatchesSources(t *testing.T) {
	root, err := FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}

	result, err := Collect(root)
	if err != nil {
		t.Fatalf("collect routes: %v", err)
	}
	if len(result.Routes) == 0 {
		t.Fatal("census found no routes; the source walk is broken")
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}

	got := Render(result.Routes)
	path := filepath.Join(root, "api", "ROUTES.txt")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRegenerate it from the Go module root with:\n\n\tgo run ./cmd/census -write\n", path, err)
	}

	if got == string(want) {
		return
	}

	added, removed := diffRoutes(string(want), got)
	var b strings.Builder
	fmt.Fprintf(&b, "%s is stale: %d route(s) added, %d route(s) removed\n", "api/ROUTES.txt", len(added), len(removed))
	for _, r := range added {
		fmt.Fprintf(&b, "\tadded:   %s\n", r)
	}
	for _, r := range removed {
		fmt.Fprintf(&b, "\tremoved: %s\n", r)
	}
	b.WriteString("\nRegenerate the file from the Go module root and commit it:\n\n\tgo run ./cmd/census -write\n")
	t.Fatal(b.String())
}

// collectFixture runs the census over one fixture module under testdata.
// The real census walk skips testdata directories, so the fixtures never
// pollute api/ROUTES.txt.
func collectFixture(t *testing.T, name string) Result {
	t.Helper()
	result, err := Collect(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("collect fixture %s: %v", name, err)
	}
	return result
}

// TestGatedRouterOutsidePkgAppsIsCounted pins the fix for the review's F1:
// only the pkg/apps forwarders may skip their calls, and a call inside them
// whose pattern resolves is a real route. A type named gatedRouter anywhere
// else must not hide its registrations.
func TestGatedRouterOutsidePkgAppsIsCounted(t *testing.T) {
	result := collectFixture(t, "gatedrouter")
	if err := result.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(result.Routes) != 1 {
		t.Fatalf("want 1 route, got %d: %+v", len(result.Routes), result.Routes)
	}
	r := result.Routes[0]
	if r.Method != "GET" || r.Pattern != "/yy/hidden" || r.Module != "internal/yy" {
		t.Fatalf("wrong route: %+v", r)
	}
}

// TestGatedForwarderLiteralPatternIsCounted pins the fix for the review's
// N1: a call inside a pkg/apps gatedRouter forwarder whose pattern is a
// literal resolves and is counted as a real route. The forwarder skip
// covers only the forwarded, caller-supplied pattern.
func TestGatedForwarderLiteralPatternIsCounted(t *testing.T) {
	result := collectFixture(t, "gatedforwarder")
	if err := result.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, r := range result.Routes {
		if r.Method == "GET" && r.Pattern == "/apps/hidden" && r.Module == "pkg/apps" {
			return
		}
	}
	t.Fatalf("want GET /apps/hidden counted in pkg/apps, got %+v", result.Routes)
}

// TestHandleFuncMethodValueIsUnresolved pins the fix for the review's F2: a
// HandleFunc method value bound to a variable is reported as unresolved
// instead of silently hiding the route.
func TestHandleFuncMethodValueIsUnresolved(t *testing.T) {
	result := collectFixture(t, "methodvalue")
	if len(result.Routes) != 0 {
		t.Fatalf("want no routes, got %+v", result.Routes)
	}
	for _, u := range result.Unresolved {
		if u.Callee == "HandleFunc" && u.File == "internal/zz/zz.go" {
			return
		}
	}
	t.Fatalf("want an unresolved HandleFunc binding in internal/zz/zz.go, got %+v", result.Unresolved)
}

// TestSubMuxMountsFailUnlessAllowListed pins the fix for the review's F3:
// an http.NewServeMux outside cmd/server and a StripPrefix mount fail the
// census, unless the mount is on the allow list (the /uploads/ file
// server), which is still listed as its outer route.
func TestSubMuxMountsFailUnlessAllowListed(t *testing.T) {
	result := collectFixture(t, "submux")
	if err := result.Validate(); err == nil {
		t.Fatalf("want restricted registrations, got none (routes %+v, restricted %+v)", result.Routes, result.Restricted)
	}
	var newServeMux, stripPrefix int
	for _, u := range result.Restricted {
		if u.File != "internal/zz/zz.go" {
			t.Errorf("restricted entry outside internal/zz/zz.go: %v", u)
			continue
		}
		switch u.Callee {
		case "NewServeMux":
			newServeMux++
		case "Handle":
			stripPrefix++
		}
	}
	if newServeMux != 1 || stripPrefix != 1 {
		t.Fatalf("want 1 NewServeMux and 1 Handle restriction, got %d and %d: %+v", newServeMux, stripPrefix, result.Restricted)
	}
	var uploads bool
	for _, r := range result.Routes {
		if r.Module == "cmd/server" && r.Pattern == "/uploads/" {
			uploads = true
		}
	}
	if !uploads {
		t.Fatalf("the allow listed /uploads/ mount must still be listed, got %+v", result.Routes)
	}
}

// TestShadowedPatternNameIsUnresolved pins the fix for the review's F4: a
// pattern name bound as a parameter or short variable in the enclosing
// function is unresolved, not silently resolved to the package constant it
// shadows.
func TestShadowedPatternNameIsUnresolved(t *testing.T) {
	result := collectFixture(t, "shadow")
	if len(result.Routes) != 0 {
		t.Fatalf("a shadowed name must not resolve to the package constant, got %+v", result.Routes)
	}
	if len(result.Unresolved) != 2 {
		t.Fatalf("want 2 unresolved registrations (parameter and variable shadow), got %+v", result.Unresolved)
	}
}

// TestExactDoubleRegistrationFails pins the fix for the review's F5: two
// identical registrations outside mutually exclusive branches are a
// duplicate, while the if/else pair still collapses to one route.
func TestExactDoubleRegistrationFails(t *testing.T) {
	result := collectFixture(t, "double")
	err := result.Validate()
	if err == nil {
		t.Fatalf("want a duplicate registration, got none (routes %+v)", result.Routes)
	}
	if !strings.Contains(err.Error(), "GET /zz/dup") {
		t.Fatalf("the duplicate message must name GET /zz/dup, got: %v", err)
	}
	cond := 0
	for _, r := range result.Routes {
		if r.Pattern == "/zz/cond" {
			cond++
		}
	}
	if cond != 1 {
		t.Fatalf("the if/else pair must collapse to one route, got %d: %+v", cond, result.Routes)
	}
}

// TestDiffRoutesReportsHandlerOnlyChange pins the fix for the review's F6:
// the staleness diff works on the full row, so a handler-only change is
// reported as the old row removed and the new row added.
func TestDiffRoutesReportsHandlerOnlyChange(t *testing.T) {
	oldRender := "GET\t/a\tinternal/aa\toldHandler\n"
	newRender := "GET\t/a\tinternal/aa\tnewHandler\n"
	added, removed := diffRoutes(oldRender, newRender)
	if len(added) != 1 || !strings.Contains(added[0], "newHandler") {
		t.Fatalf("want the new row added, got %q", added)
	}
	if len(removed) != 1 || !strings.Contains(removed[0], "oldHandler") {
		t.Fatalf("want the old row removed, got %q", removed)
	}
}

// TestBuildExcludedFilesAreNotWalked pins the fix for the review's F7:
// directories the go tool ignores (leading underscore) and files its build
// constraints exclude (//go:build ignore) are not walked, so only the
// route of the buildable file remains.
func TestBuildExcludedFilesAreNotWalked(t *testing.T) {
	result := collectFixture(t, "buildexcluded")
	if err := result.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(result.Routes) != 1 || result.Routes[0].Pattern != "/zz/kept" || result.Routes[0].Module != "." {
		t.Fatalf("want only GET /zz/kept in the module root package, got %+v", result.Routes)
	}
}

// diffRoutes compares two rendered censuses by full row and returns the
// rows present in newRender only and in oldRender only, so a change in any
// column, handler included, is reported.
func diffRoutes(oldRender, newRender string) (added, removed []string) {
	rows := func(render string) map[string]bool {
		present := map[string]bool{}
		for _, line := range strings.Split(render, "\n") {
			if strings.HasPrefix(line, "#") || line == "" || strings.HasPrefix(line, "method\t") {
				continue
			}
			if len(strings.Split(line, "\t")) != 4 {
				continue
			}
			present[line] = true
		}
		return present
	}
	oldRows := rows(oldRender)
	newRows := rows(newRender)
	for line := range newRows {
		if !oldRows[line] {
			added = append(added, line)
		}
	}
	for line := range oldRows {
		if !newRows[line] {
			removed = append(removed, line)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
