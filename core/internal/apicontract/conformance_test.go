// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/routecensus"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// TestGoldenResponsesConform is the contract conformance gate (R1-7b): every
// response every characterisation golden records validates against the
// operation its request resolves to in the assembled contract. It reads only
// committed files (the goldens, the contract, the census, the pending list
// and the known deviation list) and needs no database.
//
// For each recorded request it resolves the operation through Spec.Find,
// then checks, in order:
//
//  1. the recorded status is declared on the operation;
//  2. the recorded content type (parameters stripped, case folded) is a
//     declared media type of that status's response;
//  3. the recorded body validates against that media type's schema with a
//     JSON Schema 2020-12 validator (github.com/santhosh-tekuri/jsonschema,
//     pinned in go.mod; format assertions stay off, matching 2020-12, which
//     also keeps the normaliser's placeholders out of format checks).
//
// Requests whose route is still in core/api/contract-pending.txt are skipped:
// no operation exists to conform to yet. A request that resolves to no
// operation, no pending entry AND no census pattern, or to a census pattern
// under a different method only, is the mux's own answer (the unmounted 404,
// the wrong method 405), not a handler's, and is skipped the same way; those
// shapes are pinned by the golden harness itself and the census, not by any
// operation's declaration.
//
// Golden bodies are recorded through the harness's wrapper: non JSON content
// types arrive as {"text": "..."} (and printed PDFs as {"binary": {...}}).
// The wrapper is unwrapped for non JSON content types; an empty {"text": ""}
// is the empty body of a 204. A binary body cannot be schema validated (the
// golden keeps only a text hash), so it is checked to its media type alone.
//
// The normaliser replaced values that vary between runs with class
// placeholders (<id-3>, <ts+30d>, <day-2>, <days>, <customer>, <ms>, ...).
// A placeholder stands in a field whose real value is of that field's type,
// so a validation error located exactly at a placeholder leaf is excused
// (every keyword: the placeholder carries no information beyond presence),
// except a type error whose wanted types exclude string: a placeholder is a
// string, so an integer or boolean field holding one is a fragment bug.
// Errors anywhere else are real: a wrong type, a missing required field or
// an undeclared enum value outside a placeholder is a fragment bug.
//
// Known deviations live in core/api/conformance-known.txt, one line per
// golden step with its reason. The list only shrinks: the test fails when a
// step that is not listed fails, and fails when a listed step now passes
// (strike the line) or names no golden step this pass checks.
func TestGoldenResponsesConform(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	spec, err := Load(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	pending, err := ParsePending(root + "/api/contract-pending.txt")
	if err != nil {
		t.Fatalf("parse pending list: %v", err)
	}
	census, err := ParseCensus(root + "/api/ROUTES.txt")
	if err != nil {
		t.Fatalf("parse census: %v", err)
	}
	goldensDir := root + "/internal/characterization/testdata/goldens"
	goldenFiles, err := filepath.Glob(goldensDir + "/*.json")
	if err != nil || len(goldenFiles) == 0 {
		t.Fatalf("no golden files under %s: %v", goldensDir, err)
	}
	known, err := parseKnownDeviations(root + "/api/conformance-known.txt")
	if err != nil {
		t.Fatalf("parse known deviations: %v", err)
	}

	// One JSON Schema compiler carries the whole assembled document as the
	// resource every $ref resolves against; each response schema is compiled
	// as its own resource rooted at that one.
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(contractResource, toJSONValues(spec.Document())); err != nil {
		t.Fatalf("add the contract as a schema resource: %v", err)
	}

	// Track which (opID, media) pairs have been compiled to avoid re-registering
	// the same resource URL when multiple golden steps share an operation.
	compiled := map[string]bool{}

	var problems []string
	checked := map[string]bool{} // every golden step the pass ran checks on
	var validated, skippedPending, skippedMux int
	for _, file := range goldenFiles {
		group := strings.TrimSuffix(filepath.Base(file), ".json")
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read golden %s: %v", file, err)
		}
		var steps []goldenStep
		if err := json.Unmarshal(raw, &steps); err != nil {
			t.Fatalf("parse golden %s: %v", file, err)
		}
		for _, step := range steps {
			where := fmt.Sprintf("%s %s: %s %s", group, step.Name, step.Request.Method, step.Request.Path)
			// Pending routes have no operation to conform to yet; skip them first.
			if matchesPending(pending, step.Request.Method, step.Request.Path) {
				skippedPending++
				continue
			}
			op, _ := spec.Find(step.Request.Method, step.Request.Path)
			if op == nil {
				if matchesMuxAnswer(census, step.Request.Method, step.Request.Path) {
					skippedMux++
				} else {
					problems = append(problems, where+
						": resolves to no operation, no pending entry and no census route; the request line must name a real route")
				}
				continue
			}
			checked[group+"\t"+step.Name] = true
			stepProblems := conformStep(compiler, compiled, op, step)
			if len(stepProblems) == 0 {
				if reason, listed := known[group+"\t"+step.Name]; listed {
					problems = append(problems, where+": conforms now; strike its line from api/conformance-known.txt ("+reason+")")
				} else {
					validated++
				}
				continue
			}
			if _, listed := known[group+"\t"+step.Name]; !listed {
				problems = append(problems, where+": "+strings.Join(stepProblems, "; "))
			}
		}
	}

	// The known list only shrinks: a line that names nothing this pass
	// checked (a typo, a renamed step, or a step now skipped) is dead and
	// must go.
	for key, reason := range known {
		if !checked[key] {
			problems = append(problems, "api/conformance-known.txt names a step this pass did not check; strike the line: "+key+" ("+reason+")")
		}
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("contract conformance failed with %d problem(s):\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
	t.Logf("conformance: %d golden steps validated, %d skipped on pending routes, %d skipped as mux answers, %d known deviations",
		validated, skippedPending, skippedMux, len(known))
}

// goldenStep is one recorded step of a golden file: the request the harness
// sent and the response it compared. Bodies stay raw; their decoding depends
// on the content type the response recorded.
type goldenStep struct {
	Name    string `json:"name"`
	Request struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"request"`
	Response struct {
		Status      int             `json:"status"`
		ContentType string          `json:"content_type"`
		Body        json.RawMessage `json:"body"`
	} `json:"response"`
}

// conformStep runs the three checks of one golden step against its resolved
// operation and returns the problems found (empty when the step conforms).
func conformStep(compiler *jsonschema.Compiler, compiled map[string]bool, op *Operation, step goldenStep) []string {
	where := fmt.Sprintf("%s %s", op.Method, op.Path)
	var problems []string

	status := fmt.Sprint(step.Response.Status)
	decl, ok := op.Responses[status]
	if !ok {
		var declared []string
		for code := range op.Responses {
			declared = append(declared, code)
		}
		sort.Strings(declared)
		return []string{fmt.Sprintf("status %s is not declared on %s (declared: %s)", status, op.ID, strings.Join(declared, ", "))}
	}

	recorded := parseContentType(step.Response.ContentType)
	body, bodyKind := decodeGoldenBody(step.Response.Body, recorded)

	if len(decl.Content) == 0 {
		if bodyKind != bodyEmpty {
			problems = append(problems, fmt.Sprintf("response declares no content for %s but the golden records a body", where))
		}
		return problems
	}

	var declared []string
	for media := range decl.Content {
		declared = append(declared, media)
	}
	sort.Strings(declared)
	if recorded == "" {
		problems = append(problems, fmt.Sprintf("response declares %s but the golden records an empty body", strings.Join(declared, ", ")))
		return problems
	}
	mt, ok := decl.Content[matchMediaType(declared, recorded)]
	if !ok {
		problems = append(problems, fmt.Sprintf("content type %q is not declared on %s %s (declared: %s)",
			step.Response.ContentType, op.ID, status, strings.Join(declared, ", ")))
		return problems
	}
	if bodyKind == bodyBinary || mt.Schema == nil {
		// A binary body is pinned by the golden's own text hash, not by a
		// schema; a media type without a schema has nothing to check.
		return problems
	}
	if problems = append(problems, validateBody(compiler, compiled, mt.Schema, body, op.ID, status, recorded)...); len(problems) > 0 {
		return problems
	}
	return problems
}

// validateBody compiles the declared schema and validates the golden's body
// against it, excusing validation errors located exactly at a normaliser
// placeholder. Compile errors fail the step: the assembled document must
// carry a schema a 2020-12 validator can build.
func validateBody(compiler *jsonschema.Compiler, compiled map[string]bool, schemaNode, instance any, opID, status, media string) []string {
	resource := fmt.Sprintf("response://%s/%s/%s", opID, status, media)
	if !compiled[resource] {
		detached := absolutizeRefs(toJSONValues(schemaNode))
		if err := compiler.AddResource(resource, detached); err != nil {
			return []string{fmt.Sprintf("the declared schema for %s %s cannot be prepared: %v", opID, media, err)}
		}
		compiled[resource] = true
	}
	sch, err := compiler.Compile(resource)
	if err != nil {
		return []string{fmt.Sprintf("the declared schema for %s %s does not compile as JSON Schema 2020-12: %v", opID, media, err)}
	}
	err = sch.Validate(instance)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{fmt.Sprintf("body does not validate: %v", err)}
	}
	tokens := map[string]string{}
	collectPlaceholders(instance, nil, tokens)
	var real []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		// Skip structural wrappers: kind.Schema (root, nil KeywordPath) and
		// kind.Reference ($ref wrapper).  Every other ErrorKind — Type,
		// Enum, Required, MinItems, etc. — carries a real keyword failure;
		// record it unless it lands on a placeholder leaf.
		if _, isRef := e.ErrorKind.(*kind.Reference); isRef {
			for _, cause := range e.Causes {
				walk(cause)
			}
			return
		}
		if e.ErrorKind.KeywordPath() == nil {
			for _, cause := range e.Causes {
				walk(cause)
			}
			return
		}
		// A string class placeholder excuses an error only where the
		// declared type admits a string: a type error whose wanted types
		// exclude "string" (an integer field holding <id-3>) is real. The
		// numeric class placeholders (<ms>, <poolstat>, <days>) replace a
		// number, so they excuse a type error only where a number is wanted.
		ph, onPlaceholder := tokens[strings.Join(e.InstanceLocation, "\x00")]
		excused := onPlaceholder
		if te, isType := e.ErrorKind.(*kind.Type); isType && onPlaceholder {
			excused = false
			for _, w := range te.Want {
				if numericPlaceholders[ph] && (w == "integer" || w == "number") || !numericPlaceholders[ph] && w == "string" {
					excused = true
				}
			}
		}
		if !excused {
			real = append(real, fmt.Sprintf("at %s: %v", joinJSONPointer(e.InstanceLocation), e.ErrorKind))
		}
	}
	walk(verr)
	if len(real) == 0 {
		return nil
	}
	sort.Strings(real)
	return []string{"body does not validate against the declared schema: " + strings.Join(real, "; ")}
}

// bodyKind classifies the recorded body of a golden step.
type bodyKind int

const (
	bodyEmpty  bodyKind = iota // no body at all, or the empty {"text": ""} of a 204
	bodyJSON                   // a JSON value, decoded
	bodyText                   // a non JSON body kept as text
	bodyBinary                 // a printed document, recorded as a hash
)

// decodeGoldenBody classifies and decodes the recorded body. The harness
// wraps non JSON bodies: {"text": "..."} for text, {"binary": {...}} for
// printed documents. The wrapper is unwrapped only when the recorded
// content type is not JSON, so a JSON response that happens to be an object
// with one "text" key stays what the server sent.
func decodeGoldenBody(raw json.RawMessage, media string) (any, bodyKind) {
	if len(raw) == 0 {
		return nil, bodyEmpty
	}
	if isJSONMediaType(media) {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, bodyEmpty
		}
		return v, bodyJSON
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err == nil {
			if text, ok := singleKey(obj, "text"); ok {
				var s string
				if json.Unmarshal(text, &s) == nil {
					if s == "" {
						return nil, bodyEmpty
					}
					// A text body that carries JSON (today's headerless
					// create responses) is parsed so the declared schema
					// can check it.
					if v, err := jsonschema.UnmarshalJSON(strings.NewReader(s)); err == nil {
						return v, bodyJSON
					}
					return s, bodyText
				}
			}
			if _, ok := singleKey(obj, "binary"); ok {
				return nil, bodyBinary
			}
		}
	}
	// Any other shape under a non JSON content type has no defined meaning
	// in the transcript; treat it as text the schema never sees.
	return nil, bodyText
}

func singleKey(obj map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	if len(obj) != 1 {
		return nil, false
	}
	v, ok := obj[key]
	return v, ok
}

// parseContentType returns the recorded content type's media type: the part
// before ";", lower cased and trimmed.
func parseContentType(recorded string) string {
	media, _, _ := strings.Cut(recorded, ";")
	return strings.ToLower(strings.TrimSpace(media))
}

func isJSONMediaType(media string) bool {
	return parseContentType(media) == "application/json"
}

// matchMediaType finds the declared media type the recorded one names,
// folding case and parameters on both sides.
func matchMediaType(declared []string, recorded string) string {
	for _, media := range declared {
		if parseContentType(media) == recorded {
			return media
		}
	}
	return ""
}

// placeholderRe matches the characterisation normaliser's class
// placeholders: <id-3>, <ts+30d>, <day-2>, <days>, <customer>, <api-key>,
// <ms>, <uptime>, <poolstat>, <idem-key>, <jwt>, <orders>. The shape is a
// lowercase base with optional hyphenated number, then an optional signed
// day offset with or without the trailing d (docs/refactor/GOLDENS.md).
var placeholderRe = regexp.MustCompile(`^<[a-z][a-z0-9-]*>([+-][0-9]+d?)?$`)

// numericPlaceholders are the normaliser's placeholders for volatile numeric
// fields (volatileNumberFields in the characterisation harness): they stand
// where a number was recorded, every other placeholder where a string was.
var numericPlaceholders = map[string]bool{"<ms>": true, "<poolstat>": true, "<days>": true}

// collectPlaceholders records, as NUL joined paths mapped to the placeholder
// text, every leaf of the instance whose value is exactly a normaliser
// placeholder.
func collectPlaceholders(v any, path []string, into map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			collectPlaceholders(x, append(path, k), into)
		}
	case []any:
		for i, x := range t {
			collectPlaceholders(x, append(path, fmt.Sprint(i)), into)
		}
	case string:
		if placeholderRe.MatchString(t) {
			into[strings.Join(path, "\x00")] = t
		}
	}
}

// joinJSONPointer renders a token path as a JSON Pointer for messages.
func joinJSONPointer(tokens []string) string {
	var sb strings.Builder
	for _, tok := range tokens {
		sb.WriteByte('/')
		sb.WriteString(strings.ReplaceAll(strings.ReplaceAll(tok, "~", "~0"), "/", "~1"))
	}
	return sb.String()
}

// contractResource is the URL the assembled document is registered under
// with the JSON Schema compiler; every response schema's $refs resolve
// against it. The file:// scheme is required: when a response schema's
// $ref is absolutized to file://openapi.yaml#/... and registered under
// response://..., the library resolves it to file://openapi.yaml#/... and
// finds the component schema already registered.
const contractResource = "file://openapi.yaml"

// toJSONValues converts a YAML decoded tree into the JSON values the JSON
// Schema compiler requires (json.Number numbers, map[string]any objects).
func toJSONValues(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out, _ := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	return out
}

// absolutizeRefs rewrites every local $ref ("#/...") of a detached schema
// node into the assembled document's resource, so a response schema
// compiled on its own still resolves its components. It mutates the tree it
// is given, which the caller built for this purpose alone.
func absolutizeRefs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			t["$ref"] = contractResource + ref
		}
		for k, x := range t {
			t[k] = absolutizeRefs(x)
		}
	case []any:
		for i, x := range t {
			t[i] = absolutizeRefs(x)
		}
	}
	return v
}

// parseKnownDeviations reads core/api/conformance-known.txt: comment lines
// and blank lines are skipped, every other line is group, step name and
// reason, tab separated. Duplicate lines are refused.
func parseKnownDeviations(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	known := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("%s:%d: expected group, step and reason tab separated, found %q", path, i+1, line)
		}
		key := parts[0] + "\t" + parts[1]
		if _, dup := known[key]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate line for %s", path, i+1, key)
		}
		known[key] = parts[2]
	}
	return known, nil
}

// matchesPending reports whether the recorded request's route is still in
// the pending list: the same method (or the any method "*") under a pattern
// the concrete path fits. A pattern ending in "/" is ServeMux subtree
// syntax: every path under the prefix matches.
func matchesPending(pending []string, method, concretePath string) bool {
	m := strings.ToUpper(method)
	if i := strings.IndexByte(concretePath, '?'); i >= 0 {
		concretePath = concretePath[:i]
	}
	for _, key := range pending {
		pm, pattern, _ := strings.Cut(key, " ")
		if pm != "*" && pm != m {
			continue
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(concretePath, pattern) {
			return true
		}
		concrete := strings.Split(strings.Trim(concretePath, "/"), "/")
		if _, ok := matchTemplate(pattern, concrete); ok {
			return true
		}
	}
	return false
}

// matchesMuxAnswer reports whether the recorded request is one the net/http
// mux itself answers rather than a registered handler: a path no census
// pattern matches at all (the unmounted 404), or a pattern that matches
// only under a different method (the 405). The census registers handlers;
// the contract describes handlers; a mux answer has no operation to conform
// to and its shape is pinned by the golden harness itself.
func matchesMuxAnswer(census []CensusRoute, method, concretePath string) bool {
	m := strings.ToUpper(method)
	if i := strings.IndexByte(concretePath, '?'); i >= 0 {
		concretePath = concretePath[:i]
	}
	concrete := strings.Split(strings.Trim(concretePath, "/"), "/")
	pathRegistered := false
	for _, r := range census {
		if strings.HasSuffix(r.Pattern, "/") && strings.HasPrefix(concretePath, r.Pattern) {
			pathRegistered = true
			if r.Method == "*" || r.Method == m {
				return false
			}
			continue
		}
		if _, ok := matchTemplate(r.Pattern, concrete); !ok {
			continue
		}
		pathRegistered = true
		if r.Method == "*" || r.Method == m {
			return false
		}
	}
	return pathRegistered
}
