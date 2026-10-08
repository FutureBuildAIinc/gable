// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package apicontract loads the assembled API contract
// (core/api/openapi.yaml) and the route census (core/api/ROUTES.txt) and
// answers coverage and lookup questions over them.
//
// Two consumers meet here: the coverage test keeps the census, the pending
// list (core/api/contract-pending.txt) and the contract honest against each
// other while modules convert in batches, and the later conformance pass
// resolves a recorded request (method and concrete path) to the operation
// whose schema the response must validate against, through Find.
//
// The package reads only committed files and never touches a database, so
// its tests run everywhere the Go suite runs.
package apicontract

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operation is one method+path entry of the contract, with the fields the
// coverage and conformance passes read.
type Operation struct {
	Method string // upper case HTTP method
	Path   string // the templated path exactly as the document carries it
	ID     string // operationId, "" when the document lacks one
	// Responses maps each declared status code ("200", "404", "default")
	// to its declaration, with a $ref into #/components/responses already
	// resolved. The conformance pass checks a golden's recorded status and
	// content type against it and validates the body under the matching
	// media type's schema.
	Responses map[string]ResponseDecl
}

// ResponseDecl is one declared response of an operation.
type ResponseDecl struct {
	Description string
	// Content maps each declared media type exactly as the document spells
	// it (application/json) to its declaration. Empty when the response
	// declares no body at all (a 204 or an undetailed status).
	Content map[string]MediaTypeDecl
}

// MediaTypeDecl is one declared media type of a response.
type MediaTypeDecl struct {
	// Schema is the body's JSON Schema node as the document carries it,
	// usually a $ref map into #/components/schemas. Nil when the media
	// type declares no schema. $refs inside the node are left for the
	// validator to resolve against the whole document (Spec.Document).
	Schema any
}

// Spec is the loaded contract.
type Spec struct {
	// paths maps each templated path to its methods, lower case.
	paths map[string]map[string]Operation
	// document is the whole assembled YAML document as decoded, kept for
	// consumers that need to resolve $refs the Operation fields already
	// dereferenced (the conformance pass compiles response schemas
	// against it as one JSON Schema resource).
	document any
}

// Document returns the whole assembled document as decoded from YAML
// (map[string]any, []any, string, bool, int, nil). Callers that need the
// JSON Schema validator's view of it must convert it to JSON values first.
func (s *Spec) Document() any { return s.document }

// yamlResponse is one response entry, either inline (description, content)
// or a $ref into #/components/responses.
type yamlResponse struct {
	Ref         string `yaml:"$ref"`
	Description string `yaml:"description"`
	Content     map[string]struct {
		Schema any `yaml:"schema"`
	} `yaml:"content"`
}

// Load reads and validates an assembled OpenAPI document. It fails on a
// document with no paths (an unassembled or empty contract), on an
// operation without an operationId, and on a response $ref that does not
// name a shared response, all of which make coverage or conformance
// answers meaningless.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string                  `yaml:"operationId"`
			Responses   map[string]yamlResponse `yaml:"responses"`
		} `yaml:"paths"`
		Components struct {
			Responses map[string]yamlResponse `yaml:"responses"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s carries no paths; run go run ./api/tools/merge", path)
	}

	resolve := func(r yamlResponse, where string) (ResponseDecl, error) {
		if r.Ref != "" {
			name, ok := strings.CutPrefix(r.Ref, "#/components/responses/")
			if !ok {
				return ResponseDecl{}, fmt.Errorf("%s: %s: response $ref outside components/responses is not supported: %q", path, where, r.Ref)
			}
			shared, ok := doc.Components.Responses[name]
			if !ok {
				return ResponseDecl{}, fmt.Errorf("%s: %s: response $ref names no shared response: %q", path, where, r.Ref)
			}
			if shared.Ref != "" {
				return ResponseDecl{}, fmt.Errorf("%s: %s: a shared response may not itself be a $ref: %q", path, where, r.Ref)
			}
			r = shared
		}
		decl := ResponseDecl{Description: r.Description, Content: make(map[string]MediaTypeDecl, len(r.Content))}
		for media, mt := range r.Content {
			decl.Content[media] = MediaTypeDecl{Schema: mt.Schema}
		}
		return decl, nil
	}

	s := &Spec{paths: make(map[string]map[string]Operation, len(doc.Paths))}
	for path, methods := range doc.Paths {
		for method, op := range methods {
			m := strings.ToUpper(method)
			if !isHTTPMethod(m) {
				continue
			}
			if op.OperationID == "" {
				return nil, fmt.Errorf("%s: %s %s has no operationId", path, m, path)
			}
			responses := make(map[string]ResponseDecl, len(op.Responses))
			for status, r := range op.Responses {
				decl, err := resolve(r, fmt.Sprintf("%s %s responses %s", m, path, status))
				if err != nil {
					return nil, err
				}
				responses[status] = decl
			}
			if s.paths[path] == nil {
				s.paths[path] = map[string]Operation{}
			}
			s.paths[path][m] = Operation{Method: m, Path: path, ID: op.OperationID, Responses: responses}
		}
	}
	var document any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.document = document
	return s, nil
}

// Has reports whether the contract carries the method on the templated
// path, byte for byte as the census spells it.
func (s *Spec) Has(method, pattern string) bool {
	methods, ok := s.paths[pattern]
	if !ok {
		return false
	}
	_, ok = methods[strings.ToUpper(method)]
	return ok
}

// Covers reports whether the contract describes a census route: the exact
// method and pattern, or, for a wildcard (*) census method (a mount), any
// operation on the pattern.
func (s *Spec) Covers(method, pattern string) bool {
	if s.Has(method, pattern) {
		return true
	}
	return method == "*" && specHasAnyMethod(s, pattern)
}

// specHasAnyMethod reports whether the contract carries any operation on the
// path, regardless of method. Used to satisfy a wildcard (*) census entry
// (a mount) with any concrete method the fragment describes.
func specHasAnyMethod(spec *Spec, pattern string) bool {
	_, ok := spec.paths[pattern]
	return ok
}

// Operations lists every operation, sorted by path then method.
func (s *Spec) Operations() []Operation {
	var ops []Operation
	for _, methods := range s.paths {
		for _, op := range methods {
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

// Find resolves a concrete request path to its operation: each templated
// segment ({id}, {customerId}) matches exactly one concrete segment, and a
// query string recorded with the request takes no part in matching. When
// several templates match, the most specific one wins the way net/http's
// ServeMux ranks patterns: a literal segment beats a templated one at the
// first position where they differ, and a longer run of leading literal
// segments beats a shorter one, with the pattern text as the deterministic
// backstop so the answer never depends on map order. It returns the matched
// operation and the extracted path parameters, or nil when no template
// matches. The conformance pass resolves every golden's recorded request
// through this.
func (s *Spec) Find(method, concretePath string) (*Operation, map[string]string) {
	m := strings.ToUpper(method)
	if i := strings.IndexByte(concretePath, '?'); i >= 0 {
		concretePath = concretePath[:i]
	}
	concrete := strings.Split(strings.Trim(concretePath, "/"), "/")
	var matches []Operation
	var matchParams []map[string]string
	for path, methods := range s.paths {
		op, ok := methods[m]
		if !ok {
			continue
		}
		params, ok := matchTemplate(path, concrete)
		if !ok {
			continue
		}
		matches = append(matches, op)
		matchParams = append(matchParams, params)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	best := 0
	for i := 1; i < len(matches); i++ {
		if moreSpecific(matches[i].Path, matches[best].Path) {
			best = i
		}
	}
	found := matches[best]
	return &found, matchParams[best]
}

// moreSpecific reports whether pattern a outranks pattern b under ServeMux
// precedence. Both patterns match the same concrete path, so they have the
// same number of segments; the first segment where one is literal and the
// other templated decides it, and an otherwise total tie falls to the
// pattern text so the order is deterministic.
func moreSpecific(a, b string) bool {
	as := strings.Split(strings.Trim(a, "/"), "/")
	bs := strings.Split(strings.Trim(b, "/"), "/")
	for i := range as {
		at := isTemplatedSegment(as[i])
		bt := isTemplatedSegment(bs[i])
		if at != bt {
			return bt && !at
		}
	}
	return a < b
}

// isTemplatedSegment reports whether the segment is a {parameter}.
func isTemplatedSegment(seg string) bool {
	_, ok := templateSegmentName(seg)
	return ok
}

// matchTemplate matches a templated path against already split concrete
// segments and extracts the parameter values.
func matchTemplate(pattern string, concrete []string) (map[string]string, bool) {
	template := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(template) != len(concrete) {
		return nil, false
	}
	params := map[string]string{}
	for i, seg := range template {
		if name, ok := templateSegmentName(seg); ok {
			if concrete[i] == "" {
				return nil, false
			}
			params[name] = concrete[i]
			continue
		}
		if seg != concrete[i] {
			return nil, false
		}
	}
	return params, true
}

// templateSegmentName parses "{name}" and reports whether the segment is a
// templated parameter.
func templateSegmentName(seg string) (string, bool) {
	rest, ok := strings.CutPrefix(seg, "{")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, "}")
	if !ok || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func isHTTPMethod(m string) bool {
	switch m {
	case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE":
		return true
	}
	return false
}
