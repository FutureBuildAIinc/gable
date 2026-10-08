// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"testing"

	"github.com/gablelbm/gable/internal/routecensus"
	"github.com/santhosh-tekuri/jsonschema/v6"
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

// TestPlaceholderExcusesOnlyStringTypedFields pins the placeholder excuse: a
// normaliser placeholder is a string, so it stands in only for a field whose
// declared type admits a string. An integer field holding a placeholder is a
// fragment bug and must fail.
func TestPlaceholderExcusesOnlyStringTypedFields(t *testing.T) {
	cases := []struct {
		name    string
		schema  map[string]any
		wantErr bool
		value   string
	}{
		{"string field with enum", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "enum": []any{"a"}}}}, false, "<id-3>"},
		{"string or null field", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": []any{"string", "null"}, "enum": []any{"a", nil}}}}, false, "<id-3>"},
		{"integer field", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}, true, "<id-3>"},
		{"number field", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "number"}}}, true, "<id-3>"},
		{"number field holding a numeric class placeholder", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "number"}}}, false, "<days>"},
		{"boolean field", map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "boolean"}}}, true, "<id-3>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiler := jsonschema.NewCompiler()
			errs := validateBody(compiler, map[string]bool{}, tc.schema, map[string]any{"id": tc.value}, "op", "200", "application/json")
			if tc.wantErr && len(errs) == 0 {
				t.Fatal("a placeholder in a field whose declared type excludes strings must fail")
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Fatalf("a placeholder in a string typed field must be excused, got %v", errs)
			}
		})
	}
}
