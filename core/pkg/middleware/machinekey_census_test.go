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
		module, ok := middleware.PolicyModuleForPath(fields[0], pattern)
		if !ok {
			t.Errorf("route %s is under /api/v1 but no module segment resolves", pattern)
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

// draftRouteClassExpectations maps every registered draft and link route
// shape to the class ADR 0007 section 5.1 gives it. The census test below
// asserts each registered route resolves to its shape's class (not only its
// module), so a route cannot drift into a wider class by moving one segment.
var draftRouteClassExpectations = map[string]middleware.ScopeClass{
	"GET /api/v1/drafts/quotes":                   middleware.ScopeDraftRead,
	"POST /api/v1/drafts/quotes":                  middleware.ScopeDraftWrite,
	"GET /api/v1/drafts/quotes/feed":              middleware.ScopeDraftRead,
	"GET /api/v1/drafts/quotes/{id}":              middleware.ScopeDraftRead,
	"PUT /api/v1/drafts/quotes/{id}":              middleware.ScopeDraftWrite,
	"POST /api/v1/drafts/quotes/{id}/transitions": middleware.ScopeDraftWrite,
	"POST /api/v1/drafts/quotes/{id}/promote":     middleware.ScopePromotion,
	"GET /api/v1/links/quotes/{id}":               middleware.ScopeLink,
	"GET /api/v1/links/drafts/quotes/{id}":        middleware.ScopeDraftLink,
}

// TestDraftRoutesResolveThroughScopeTarget is the census extension ADR 0007
// section 5.2 names, both directions:
//
//   - every route under /api/v1/drafts/ and /api/v1/links/ must resolve
//     through ScopeTarget to a vocabulary module, at the class its shape in
//     the table above gives it, and its module must be a registered kind
//     (confirm gated);
//   - every registered kind must have its seven routes in the census;
//   - every module the grammar allows propose and commit on must be a
//     registered kind with those routes.
func TestDraftRoutesResolveThroughScopeTarget(t *testing.T) {
	carried := map[string]bool{}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("census line is not tab separated method/pattern/...: %q", line)
		}
		method, pattern := fields[0], fields[1]
		underDrafts := strings.HasPrefix(pattern, "/api/v1/drafts/")
		underLinks := strings.HasPrefix(pattern, "/api/v1/links/")
		if !underDrafts && !underLinks {
			continue
		}
		key := method + " " + pattern
		carried[key] = true
		module, class, ok := middleware.ScopeTarget(method, pattern)
		if !ok {
			t.Errorf("%s resolves to no scope target; a route under drafts or links must be one of the policy table's shapes", key)
			continue
		}
		if middleware.ModuleScopePolicyFor(module) != middleware.ModuleScopeAllowed {
			t.Errorf("%s resolves to module %q, which the vocabulary does not allow", key, module)
		}
		if !middleware.IsConfirmGated(module) {
			t.Errorf("%s resolves to module %q, which has no registered draft kind; a draft route may only exist for a confirm gated module", key, module)
		}
		want, listed := draftRouteClassExpectations[key]
		if !listed {
			t.Errorf("%s is registered under drafts or links but the class expectation table names it nowhere; give the shape a class decision", key)
			continue
		}
		if class != want {
			t.Errorf("%s resolves to class %s, want %s", key, class, want)
		}
	}

	// Every registered kind has its seven routes, and the link routes for
	// its entity, whatever kinds register later.
	for _, module := range middleware.ConfirmGatedModules() {
		seven := []string{
			"GET /api/v1/drafts/" + module,
			"POST /api/v1/drafts/" + module,
			"GET /api/v1/drafts/" + module + "/feed",
			"GET /api/v1/drafts/" + module + "/{id}",
			"PUT /api/v1/drafts/" + module + "/{id}",
			"POST /api/v1/drafts/" + module + "/{id}/transitions",
			"POST /api/v1/drafts/" + module + "/{id}/promote",
		}
		for _, key := range seven {
			if !carried[key] {
				t.Errorf("confirm gated module %q is missing its route %q; every kind registers the seven draft routes", module, key)
			}
		}
	}
	if len(carried) == 0 {
		t.Fatal("the census carries no routes under /api/v1/drafts/ or /api/v1/links/; the kinds are not registered")
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

// scopeUserOnly marks a table row for a route a machine key may never reach:
// the user only prefixes refuse a key before any scope is consulted.
const scopeUserOnly = "user only"

// adminUsersRouteExpectations is the finer admin scopes' independent
// expectation (ADR 0009), written by hand against the route census rather
// than derived from the middleware it judges: every census route under
// /api/v1/admin and /api/v1/users with the scope a machine key must hold for
// its method, or the user only marker. The test below fails when a census
// route is missing here (a route added without a scope decision), when a row
// matches no census route (a dead or renamed row), when a route resolves to a
// scope other than its row's, and when the middleware declares a finer scope
// no row expects (a stray area, whose name would enter the mint grammar).
var adminUsersRouteExpectations = map[string]string{
	"POST /api/v1/admin/exposure-scan":                    "admin:write",
	"GET /api/v1/admin/keys":                              scopeUserOnly,
	"POST /api/v1/admin/keys":                             scopeUserOnly,
	"DELETE /api/v1/admin/keys/{id}":                      scopeUserOnly,
	"GET /api/v1/admin/modules":                           "admin:modules",
	"PUT /api/v1/admin/modules/{id}":                      "admin:modules",
	"DELETE /api/v1/admin/settings/ai":                    "admin:settings",
	"GET /api/v1/admin/settings/ai":                       "admin:settings",
	"PUT /api/v1/admin/settings/ai":                       "admin:settings",
	"DELETE /api/v1/admin/settings/routing":               "admin:settings",
	"GET /api/v1/admin/settings/routing":                  "admin:settings",
	"PUT /api/v1/admin/settings/routing":                  "admin:settings",
	"GET /api/v1/admin/staff":                             "admin:staff",
	"POST /api/v1/admin/staff":                            "admin:staff",
	"GET /api/v1/admin/staff/{id}":                        "admin:staff",
	"PUT /api/v1/admin/staff/{id}":                        "admin:staff",
	"POST /api/v1/admin/staff/{id}/modules":               "admin:staff",
	"DELETE /api/v1/admin/staff/{id}/modules/{module_id}": "admin:staff",
	"GET /api/v1/users":                                   "users:read",
	"GET /api/v1/users/{sub}/branches":                    "users:read",
	"POST /api/v1/users/{sub}/branches":                   "users:grants",
	"DELETE /api/v1/users/{sub}/branches/{branch_id}":     "users:grants",
	"PUT /api/v1/users/{sub}/home-branch":                 "users:grants",
}

// underUserOnlyPrefix mirrors the middleware's segment boundary rule for a
// user-only prefix: the prefix itself or a path under it, never a longer
// module name that merely shares the stem.
func underUserOnlyPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// TestFinerAdminScopesCoverCensusRoutes is the finer scopes' drift gate
// (ADR 0009), judged against the hand written table above: every census route
// under /api/v1/admin and /api/v1/users must be in the table and resolve to
// its row's scope, every table row must match a census route, a user only row
// must sit under a user-only prefix AND keep resolving to the coarse module
// scope (a stray finer area declared over it would put its name into the
// grammar the mint validates), an area scope must hold for every method, and
// every finer scope the middleware declares must be some row's expectation.
func TestFinerAdminScopesCoverCensusRoutes(t *testing.T) {
	carried := map[string]bool{}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("census line is not tab separated method/pattern/...: %q", line)
		}
		method, pattern := fields[0], fields[1]
		underAdmin := pattern == "/api/v1/admin" || strings.HasPrefix(pattern, "/api/v1/admin/")
		underUsers := pattern == "/api/v1/users" || strings.HasPrefix(pattern, "/api/v1/users/")
		if !underAdmin && !underUsers {
			continue
		}
		key := method + " " + pattern
		carried[key] = true
		want, ok := adminUsersRouteExpectations[key]
		if !ok {
			t.Errorf("%s is under /api/v1/admin or /api/v1/users but the expectation table names it nowhere; give the route a scope decision", key)
			continue
		}
		if want == scopeUserOnly {
			matched := false
			for _, prefix := range middleware.MachineKeyUserOnlyPrefixes() {
				if underUserOnlyPrefix(pattern, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("%s is marked user only but sits under no user-only prefix; a key holding every scope would reach it", key)
			}
			// The user-only check runs before the scope check, so the route
			// stays refused whatever the resolver says; the resolver must
			// still answer the coarse module name; a finer area declared over
			// a user-only surface would leak its name into ValidScopeGrammar.
			if got, ok := middleware.RequiredScopeForPath(method, pattern); !ok || got != middleware.RequiredScope("admin", method) {
				t.Errorf("%s resolves to scope %q (ok=%v), want the coarse %q a user only route keeps; a finer area is declared over it", key, got, ok, middleware.RequiredScope("admin", method))
			}
			continue
		}
		got, ok := middleware.RequiredScopeForPath(method, pattern)
		if !ok || got != want {
			t.Errorf("%s resolves to scope %q (ok=%v), want %q", key, got, ok, want)
		}
		// One scope per area for every method (ADR 0009 section 3): the
		// table's admin rows name areas, so the other verbs must agree.
		if strings.HasPrefix(want, "admin:") && want != "admin:read" && want != "admin:write" {
			for _, other := range []string{"GET", "PUT", "POST", "DELETE"} {
				if got, ok := middleware.RequiredScopeForPath(other, pattern); !ok || got != want {
					t.Errorf("%s %s resolves to scope %q (ok=%v), want the area scope %s for every method", other, pattern, got, ok, want)
				}
			}
		}
	}
	if len(carried) == 0 {
		t.Fatal("the census carries no routes under /api/v1/admin or /api/v1/users; the test is not exercising anything")
	}

	// Reverse direction: the table names only routes that exist.
	for key, want := range adminUsersRouteExpectations {
		if !carried[key] {
			t.Errorf("expectation %q (%s) matches no route in the census; dead or renamed row", key, want)
		}
	}

	// Every finer scope the middleware declares is some row's expectation: a
	// stray area declared over no table route (or only over user-only routes)
	// fails here before its name reaches the mint's grammar.
	expected := map[string]bool{}
	for _, want := range adminUsersRouteExpectations {
		if want != scopeUserOnly {
			expected[want] = true
		}
	}
	for _, scope := range middleware.FinerAdminScopes() {
		if !expected[scope] {
			t.Errorf("the middleware declares finer admin scope %q, but no route in the expectation table requires it; a stray area puts its name into ValidScopeGrammar", scope)
		}
	}
}

// TestValidScopeGrammarHoldsTheCensus pins the grammar a minted scope may
// carry (ADR 0009; the mint route's validation landed with C5-2a, ADR 0007
// section 5.3): every scope a census route can require is in the grammar,
// the finer names replaced the coarse ones they narrow, and the propose and
// commit verbs exist only for confirm gated modules, so no route requires a
// scope the grammar cannot grant and no ungated module can be granted a
// confirm verb.
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
	if !grammar["quotes:propose"] || !grammar["quotes:commit"] {
		t.Errorf("grammar lacks the quotes confirm verbs: %v", middleware.ValidScopeGrammar())
	}
	if grammar["orders:propose"] || grammar["orders:commit"] || grammar["customers:propose"] {
		t.Error("the grammar grants a confirm verb on a module with no registered draft kind; propose and commit are gated-only (ADR 0007 5.1)")
	}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "/api/v1/") {
			continue
		}
		method, pattern := fields[0], fields[1]
		module, _ := middleware.PolicyModuleForPath(method, pattern)
		if middleware.ModuleScopePolicyFor(module) != middleware.ModuleScopeAllowed {
			continue // excluded segments keep their own seam and no key scope
		}
		// Every route resolves through the class table; every scope its
		// class admits must be grantable.
		scopeModule, class, ok := middleware.ScopeTarget(method, pattern)
		if !ok {
			t.Fatalf("route %s is under /api/v1 but resolves to no scope target", pattern)
		}
		for _, scope := range middleware.AdmittedScopes(scopeModule, class) {
			if !grammar[scope] {
				t.Errorf("%s %s requires %q, which the grammar cannot grant", method, pattern, scope)
			}
		}
	}
}
