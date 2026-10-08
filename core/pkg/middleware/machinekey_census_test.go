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
