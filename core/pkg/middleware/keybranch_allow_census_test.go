// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// TestKeyBranchAllowlistCoversEveryReachableCensusRoute is the deny by
// default round's drift gate (ADR 0007 section 5.5): every route the census
// registers that a machine key can reach carries exactly one class in the
// allowlist, and every allowlist entry names a reachable census route. A route
// added without a class decision fails closed at runtime (the gate refuses
// it) and fails here, naming it; a stale entry fails here too, so the table
// cannot silently rot. A CONFINED entry must name the test that proves it and
// a STATED LIMIT entry must carry its one line reason, so no entry can ship
// with no justification.
func TestKeyBranchAllowlistCoversEveryReachableCensusRoute(t *testing.T) {
	reachable := map[string]bool{}
	for _, line := range readCensus(t) {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("census line is not tab separated method/pattern/...: %q", line)
		}
		method, pattern := fields[0], fields[1]
		if !middleware.KeyBranchRouteReachable(method, pattern) {
			continue
		}
		key := method + " " + pattern
		reachable[key] = true
		class, ok := middleware.KeyBranchRouteClassFor(method, pattern)
		if !ok {
			t.Errorf("%s is a machine key reachable census route the allowlist names nowhere; give it a class decision (it is refused at runtime until then)", key)
			continue
		}
		note := middleware.KeyBranchAllowlistNoteFor(method, pattern)
		switch class {
		case middleware.KeyBranchClassConfined:
			if !strings.Contains(note, "Test") {
				t.Errorf("%s is CONFINED but its note names no test; each confined entry names the test that proves it (note %q)", key, note)
			}
		case middleware.KeyBranchClassStatedLimit:
			if strings.TrimSpace(note) == "" {
				t.Errorf("%s is a stated limit with no reason; each stated limit carries its one line reason", key)
			}
		}
	}
	if len(reachable) == 0 {
		t.Fatal("the census carries no machine key reachable routes; the test is not exercising anything")
	}

	// Reverse direction: the table names only routes that exist and are
	// reachable, so a moved or renamed route fails here, naming the stale
	// entry.
	for _, key := range middleware.KeyBranchAllowlistRoutes() {
		if !reachable[key] {
			t.Errorf("allowlist entry %q matches no machine key reachable census route; dead or renamed entry", key)
		}
	}

	// The classes are exhaustive on the table itself, whatever the census
	// says, and every entry carries a note.
	for _, key := range middleware.KeyBranchAllowlistRoutes() {
		method, pattern, _ := strings.Cut(key, " ")
		class, _ := middleware.KeyBranchRouteClassFor(method, pattern)
		if class.String() == "unknown" {
			t.Errorf("allowlist entry %q carries an unknown class", key)
		}
	}
}

// TestKeyBranchRouteAllowedIsDenyByDefault pins the runtime rule the gate
// applies: the three admitted classes pass, REFUSED does not, and a route the
// table does not carry at all (added by a module, or a pattern the router
// never resolved) does not either.
func TestKeyBranchRouteAllowedIsDenyByDefault(t *testing.T) {
	for _, key := range middleware.KeyBranchAllowlistRoutes() {
		method, pattern, _ := strings.Cut(key, " ")
		class, _ := middleware.KeyBranchRouteClassFor(method, pattern)
		want := class != middleware.KeyBranchClassRefused
		if got := middleware.KeyBranchRouteAllowed(method, pattern); got != want {
			t.Errorf("KeyBranchRouteAllowed(%s) = %v, want %v (class %s)", key, got, want, class)
		}
	}
	if middleware.KeyBranchRouteAllowed("GET", "/api/v1/quotes/zz-unclassified") {
		t.Error("a route the table does not carry is allowed; deny by default requires the refusal")
	}
	if middleware.KeyBranchRouteAllowed("GET", "") {
		t.Error("an empty pattern (the router matched nothing) is allowed; deny by default requires the refusal")
	}
}
