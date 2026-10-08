// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"testing"

	"github.com/gablelbm/gable/internal/routecensus"
)

// TestOperationsCarryDeclaredResponses pins what the conformance pass reads
// after Find: the operation carries every declared status, a response
// declared through $ref into #/components/responses arrives dereferenced
// (its content inline), and each media type's schema node is the document's
// own node, $refs inside it left for the validator to resolve against
// Spec.Document.
func TestOperationsCarryDeclaredResponses(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	spec, err := Load(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}

	op, _ := spec.Find("GET", "/api/v1/quotes/8f14e45f-ceea-467f-a830-a")
	if op == nil {
		t.Fatal("GET /api/v1/quotes/{id} must resolve")
	}
	if len(op.Responses) == 0 {
		t.Fatalf("the operation must carry its declared responses, got none")
	}
	ok, has200 := op.Responses["200"]
	if !has200 {
		t.Fatalf("the 200 response must be declared, got %v", op.Responses)
	}
	json, hasJSON := ok.Content["application/json"]
	if !hasJSON {
		t.Fatalf("the 200 response must declare application/json, got %v", ok.Content)
	}
	ref, isRef := json.Schema.(map[string]any)
	if !isRef || ref["$ref"] != "#/components/schemas/Quote" {
		t.Fatalf("the schema node must stay the document's own $ref node, got %#v", json.Schema)
	}

	// A shared error response arrives dereferenced: the $ref line in the
	// fragment must not hide the media type or the schema behind it.
	notFound, has404 := op.Responses["404"]
	if !has404 {
		t.Fatalf("the 404 response must be declared, got %v", op.Responses)
	}
	if notFound.Content == nil {
		t.Fatal("a response declared through #/components/responses/NotFound must arrive with its content resolved")
	}
	if mt, has := notFound.Content["application/json"]; !has || mt.Schema == nil {
		t.Fatalf("the resolved 404 must carry the application/json error schema, got %#v", notFound.Content)
	}

	if spec.Document() == nil {
		t.Fatal("the spec must keep the whole document for $ref resolution")
	}
}
