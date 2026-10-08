// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/routecensus"
)

// TestContractCoversCensus is the coverage gate between the three files
// that describe the surface: the route census (what the sources register),
// the contract (what the fragments describe) and the pending list (what is
// acknowledged as not yet described). It needs no database. It fails when:
//
//   - a census route has no operation and is not listed as pending (a
//     route the contract silently forgot),
//   - a pending entry already has an operation (the list may only shrink:
//     strike the line, never leave it),
//   - a pending entry names no census route (a typo would hide behind it),
//   - an operation backs no census route (a fragment describing a route
//     the sources do not register, or a pattern spelled differently).
func TestContractCoversCensus(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	census, err := ParseCensus(root + "/api/ROUTES.txt")
	if err != nil {
		t.Fatalf("parse census: %v", err)
	}
	spec, err := Load(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	pending, err := ParsePending(root + "/api/contract-pending.txt")
	if err != nil {
		t.Fatalf("parse pending list: %v", err)
	}

	pendingSet := map[string]bool{}
	for _, key := range pending {
		pendingSet[key] = true
	}

	var problems []string
	censusKeys := map[string]bool{}
	for _, r := range census {
		censusKeys[r.Key()] = true
		if spec.Has(r.Method, r.Pattern) {
			continue
		}
		if !pendingSet[r.Key()] {
			problems = append(problems, "no operation and not pending: "+r.Key()+
				" ("+r.Module+", "+r.Handler+"); write a fragment or list it in api/contract-pending.txt")
		}
	}
	for _, key := range pending {
		if !censusKeys[key] {
			problems = append(problems, "pending entry names no census route: "+key)
			continue
		}
		method, pattern, _ := strings.Cut(key, " ")
		if spec.Has(method, pattern) {
			problems = append(problems, "pending entry already has an operation; strike the line from api/contract-pending.txt: "+key)
		}
	}
	for _, op := range spec.Operations() {
		if !censusKeys[op.Method+" "+op.Path] {
			problems = append(problems, "operation backs no census route: "+op.Method+" "+op.Path+
				" ("+op.ID+"); the pattern must match ROUTES.txt byte for byte")
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("contract coverage failed with %d problem(s):\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
}

// TestFindResolvesConcretePaths pins the lookup the conformance pass will
// resolve every golden's recorded request through: templated segments
// match single concrete segments, the method must match, and a path that
// fits no template resolves to nothing rather than to a wrong operation.
func TestFindResolvesConcretePaths(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	spec, err := Load(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}

	op, params := spec.Find("GET", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a")
	if op == nil || op.ID != "quoteGet" {
		t.Fatalf("GET /api/v1/quotes/{id} must resolve to quoteGet, got %+v", op)
	}
	if params["id"] != "8f14e45f-ceea-467f-a830-a" {
		t.Fatalf("the id parameter must be extracted, got %v", params)
	}

	op, _ = spec.Find("GET", "/api/v1/customers/8f14e45f-ceea-467f-a830-a/contacts")
	if op == nil || op.ID != "customerListContacts" {
		t.Fatalf("GET /api/v1/customers/{customerId}/contacts must resolve to customerListContacts, got %+v", op)
	}

	if op, _ := spec.Find("POST", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a"); op != nil {
		t.Fatalf("POST on a GET only path must not resolve, got %+v", op)
	}
	if op, _ := spec.Find("GET", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a/nothing"); op != nil {
		t.Fatalf("an unregistered path must not resolve, got %+v", op)
	}
	if op, _ := spec.Find("GET", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a/extra/segment"); op != nil {
		t.Fatalf("a deeper path must not resolve, got %+v", op)
	}
}

// TestFindPrefersTheMostSpecificTemplate pins the precedence the conformance
// pass relies on when a literal route and a templated route both match one
// concrete path, and the fact that a recorded query string never takes part
// in matching: net/http's ServeMux serves /api/v1/quotes/analytics from its
// own route, not from /api/v1/quotes/{id}, and a golden recorded as
// /api/v1/invoices?limit=1 is the list route.
func TestFindPrefersTheMostSpecificTemplate(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	spec, err := Load(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}

	op, params := spec.Find("GET", "/api/v1/quotes/analytics")
	if op == nil || op.ID != "quoteAnalytics" {
		t.Fatalf("GET /api/v1/quotes/analytics must resolve to quoteAnalytics, not the {id} template, got %+v", op)
	}
	if len(params) != 0 {
		t.Fatalf("a literal route extracts no path parameters, got %v", params)
	}

	op, _ = spec.Find("GET", "/api/v1/invoices?limit=1")
	if op == nil || op.ID != "invoiceList" {
		t.Fatalf("GET /api/v1/invoices?limit=1 must resolve to invoiceList, got %+v", op)
	}

	op, params = spec.Find("GET", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a?include=lines")
	if op == nil || op.ID != "quoteGet" {
		t.Fatalf("GET /api/v1/quotes/{id}?include=lines must resolve to quoteGet, got %+v", op)
	}
	if params["id"] != "8f14e45f-ceea-467f-a830-a" {
		t.Fatalf("the id parameter must survive the stripped query string, got %v", params)
	}
}

// TestFindBreaksTiesDeterministically pins the backstop: the assembled
// document cannot carry two templates of the same shape under different
// parameter names (the merge tool refuses), but Find still answers in a
// fixed order, never the map's, so a hand-built or future document cannot
// make the same request resolve differently between calls.
func TestFindBreaksTiesDeterministically(t *testing.T) {
	s := &Spec{paths: map[string]map[string]Operation{
		"/a/{x}": {"GET": {Method: "GET", Path: "/a/{x}", ID: "lexicographicallyFirst"}},
		"/a/{y}": {"GET": {Method: "GET", Path: "/a/{y}", ID: "second"}},
	}}
	for i := 0; i < 100; i++ {
		op, _ := s.Find("GET", "/a/concrete")
		if op == nil || op.ID != "lexicographicallyFirst" {
			t.Fatalf("call %d: a shape tie must resolve to the lexicographically first pattern, got %+v", i, op)
		}
	}
}
