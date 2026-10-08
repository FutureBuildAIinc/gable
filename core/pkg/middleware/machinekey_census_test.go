// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// readCensus loads the committed route census. The file is the drift gate the
// R1-2 item installed (internal/routecensus keeps it honest against the Go
// sources), so the module vocabulary test can lean on it: any /api/v1 route
// the sources register appears here.
func readCensus(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "ROUTES.txt"))
	if err != nil {
		t.Fatalf("read api/ROUTES.txt: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// TestModuleVocabularyCoversEveryV1CensusRoute is the mapping's drift gate: a
// route added under /api/v1 with a segment the scope vocabulary does not
// declare fails here, so no /api/v1 route is ever left without a module (and
// therefore without a scope a machine key can be granted). The reverse
// direction keeps the vocabulary honest too: a module nothing routes under is
// a typo or a dead entry, and both fail.
func TestModuleVocabularyCoversEveryV1CensusRoute(t *testing.T) {
	censusModules := map[string]bool{}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("census line is not tab separated method/pattern/...: %q", line)
		}
		pattern := fields[1]
		if !strings.HasPrefix(pattern, "/api/v1/") {
			continue
		}
		module, ok := middleware.ModuleForPath(pattern)
		if !ok {
			t.Errorf("route %s is under /api/v1 but ModuleForPath finds no module segment", pattern)
			continue
		}
		censusModules[module] = true
		switch middleware.ModuleScopePolicyFor(module) {
		case middleware.ModuleScopeAllowed:
		case middleware.ModuleScopeExcluded:
			// Deliberate: segments with their own auth seam, refused for keys.
		default:
			t.Errorf("route %s resolves to module %q, which the machine-key vocabulary neither allows nor excludes; declare it (or exclude it with a reason) so the route has a scope policy", pattern, module)
		}
	}
	if len(censusModules) == 0 {
		t.Fatal("census carries no /api/v1 routes; the test is not exercising anything")
	}

	// Reverse direction: the vocabulary names only modules that exist.
	for _, module := range middleware.MachineKeyModules() {
		if !censusModules[module] {
			t.Errorf("machine-key vocabulary declares module %q, but no /api/v1 route in the census sits under it; dead or misspelled entry", module)
		}
	}

	// Every user-only prefix (key management: routes a machine key may never
	// reach, whatever scope it holds) must match at least one census route,
	// so the rule cannot silently rot onto a path nothing serves.
	for _, prefix := range middleware.MachineKeyUserOnlyPrefixes() {
		matched := false
		for _, line := range readCensus(t) {
			fields := strings.Split(line, "\t")
			if len(fields) >= 2 && strings.HasPrefix(fields[1], prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("user-only prefix %q matches no route in the census; key management moved or the prefix is wrong", prefix)
		}
	}
}

// TestRequiredScopeSplit pins the read/write split: GET and HEAD are reads,
// every other method is a write. The scope name is the module verbatim plus
// the suffix, so a caller can derive the scope it needs from the URL alone.
func TestRequiredScopeSplit(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{"GET", "quotes:read"},
		{"HEAD", "quotes:read"},
		{"POST", "quotes:write"},
		{"PUT", "quotes:write"},
		{"PATCH", "quotes:write"},
		{"DELETE", "quotes:write"},
		{"OPTIONS", "quotes:write"},
	}
	for _, tc := range cases {
		if got := middleware.RequiredScope("quotes", tc.method); got != tc.want {
			t.Errorf("RequiredScope(quotes, %s) = %q, want %q", tc.method, got, tc.want)
		}
	}
}

// TestFinerAdminScopesCoverCensusRoutes is the finer scopes' drift gate
// (ADR 0009): every admin area the middleware declares must sit under real
// census routes, every census route under a declared area resolves to that
// area's scope for every method, and every route under /api/v1/admin outside
// the declared areas keeps the coarse module scope (the exposure scan, and
// the key routes a key may not reach anyway).
func TestFinerAdminScopesCoverCensusRoutes(t *testing.T) {
	areas := map[string]bool{}
	for _, scope := range middleware.FinerAdminScopes() {
		areas[strings.TrimPrefix(scope, "admin:")] = true
	}
	if len(areas) == 0 {
		t.Fatal("no admin areas declared; the test is not exercising anything")
	}

	served := map[string]bool{}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "/api/v1/admin/") {
			continue
		}
		served[fields[1]] = true
		rest := strings.TrimPrefix(fields[1], "/api/v1/admin/")
		segment, _, _ := strings.Cut(rest, "/")
		if areas[segment] {
			for _, method := range []string{"GET", "PUT", "POST", "DELETE"} {
				scope, ok := middleware.RequiredScopeForPath(method, fields[1])
				if !ok || scope != "admin:"+segment {
					t.Errorf("%s %s resolves to scope %q (ok=%v), want the area scope admin:%s for every method", method, fields[1], scope, ok, segment)
				}
			}
		} else {
			scope, _ := middleware.RequiredScopeForPath("POST", fields[1])
			if scope != "admin:write" {
				t.Errorf("POST %s outside the declared areas resolves to %q, want the coarse admin:write", fields[1], scope)
			}
		}
	}
	for area := range areas {
		found := false
		for path := range served {
			if strings.HasPrefix(path, "/api/v1/admin/"+area+"/") || path == "/api/v1/admin/"+area {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("admin area %q matches no route in the census; dead or misspelled entry", area)
		}
	}
}

// TestValidScopeGrammarHoldsTheCensus pins the grammar a minted scope may
// carry (ADR 0009; the mint route's validation is C5-2a's): every scope a
// census route can require is in the grammar, and the finer names replaced
// the coarse ones they narrow, so no route requires a scope the grammar
// cannot grant.
func TestValidScopeGrammarHoldsTheCensus(t *testing.T) {
	grammar := map[string]bool{}
	for _, scope := range middleware.ValidScopeGrammar() {
		grammar[scope] = true
	}
	if grammar["users:write"] {
		t.Error("users:write is in the grammar; users:grants replaced it (ADR 0009)")
	}
	if !grammar["users:grants"] || !grammar["admin:settings"] || !grammar["admin:staff"] || !grammar["admin:modules"] {
		t.Errorf("grammar lacks a finer name: %v", middleware.ValidScopeGrammar())
	}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "/api/v1/") {
			continue
		}
		module, _ := middleware.ModuleForPath(fields[1])
		if middleware.ModuleScopePolicyFor(module) != middleware.ModuleScopeAllowed {
			continue // excluded segments keep their own seam and no key scope
		}
		for _, method := range []string{"GET", "POST"} {
			scope, ok := middleware.RequiredScopeForPath(method, fields[1])
			if !ok {
				t.Fatalf("route %s is under /api/v1 but resolves to no scope", fields[1])
			}
			if !grammar[scope] {
				t.Errorf("%s %s requires %q, which the grammar cannot grant", method, fields[1], scope)
			}
		}
	}
}
