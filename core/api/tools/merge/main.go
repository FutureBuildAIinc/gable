// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command merge assembles core/api/openapi.yaml from the per module
// fragments under core/api/fragments/.
//
// Every fragment is a YAML file whose top-level keys are exactly paths and
// components. The underscore prefixed files (the shared fragment) hold only
// components. This tool deep merges them into one OpenAPI 3.1 document:
// paths merge per HTTP method (two fragments may each own methods of one
// path, but one method of one path may be owned by only one fragment), and
// component names must be unique across every fragment, so a name is owned
// by the fragment that declares it.
//
// The assembly is deterministic: fragments are read in sorted filename
// order and YAML nodes are emitted in the order their fragment wrote them,
// so the same fragment set always produces byte identical output. No
// timestamp, environment value, or absolute path enters the document.
//
// It fails on: unparseable YAML, wrong top-level keys, duplicate
// operations, duplicate component names, duplicate operationIds, a
// non-method key under a path, an operation without exactly one tag, an
// undeclared path parameter, templated paths that collide under different
// parameter names, and unresolved local $refs.
//
// Modes: default writes core/api/openapi.yaml. -check (or the CHECK
// environment variable, for places a flag is awkward) regenerates the
// document in memory and exits non zero when the committed file differs,
// printing the first differing lines, so an edited fragment without a
// regeneration cannot pass as fresh. CI runs the check; a developer who
// sees it fails runs, from the Go module root:
//
//	go run ./api/tools/merge
//
// The later conformance pass (every characterisation golden validated
// against its operation) loads the assembled document through
// internal/apicontract, which reads exactly what this tool writes.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/routecensus"
	"gopkg.in/yaml.v3"
)

// httpMethods are the OpenAPI operation keys. Anything else under a path
// item (parameters, summary, description, servers) is refused: today every
// such fact lives on the operation, and a path level key would silently
// apply to methods another fragment owns.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true, "patch": true,
	"head": true, "options": true, "trace": true,
}

// componentKinds are the component buckets the merge fills, in the order
// the assembled document carries them.
var componentKinds = []string{"securitySchemes", "parameters", "responses", "schemas"}

// pathParamRe matches one templated path segment such as {id} or
// {customerId}. A multi segment wildcard ({rest...}) never appears in a
// fragment: the census lists it as the route's whole pattern and such
// routes stay in contract-pending until their shape is decided.
var pathParamRe = regexp.MustCompile(`\{([^}/]+)\}`)

const fileHeader = `# SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Gable API contract, assembled by core/api/tools/merge from the fragments
# in core/api/fragments/. Edit a fragment, never this file; regenerate with:
#
#	go run ./api/tools/merge
#
# The merge -check drift gate and the internal/apicontract coverage test
# both fail when this file and the fragments disagree.
`

func main() {
	check := flag.Bool("check", false, "compare the assembled document against the committed openapi.yaml and exit non zero on any difference, writing nothing")
	flag.Parse()

	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		fatal("find module root: %v", err)
	}
	apiDir := filepath.Join(root, "api")

	doc, err := assemble(apiDir)
	if err != nil {
		fatal("%v", err)
	}

	var out bytes.Buffer
	out.WriteString(fileHeader)
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		fatal("encode document: %v", err)
	}
	if err := enc.Close(); err != nil {
		fatal("close encoder: %v", err)
	}
	out.WriteString("\n")

	target := filepath.Join(apiDir, "openapi.yaml")
	if *check || os.Getenv("CHECK") == "1" {
		committed, err := os.ReadFile(target)
		if err != nil {
			fatal("read %s: %v\nRegenerate it with: go run ./api/tools/merge", target, err)
		}
		if !bytes.Equal(committed, out.Bytes()) {
			first := firstDiffLines(committed, out.Bytes())
			fatal("%s is stale: the fragments assemble to a different document.\nFirst difference around:\n%s\nRegenerate and commit it:\n\n\tgo run ./api/tools/merge", target, first)
		}
		fmt.Printf("openapi.yaml is fresh: %d paths, %d operations, %d schemas\n",
			countPaths(doc), countOperations(doc), countSchemas(doc))
		return
	}

	if err := os.WriteFile(target, out.Bytes(), 0o644); err != nil {
		fatal("write %s: %v", target, err)
	}
	fmt.Printf("openapi.yaml assembled: %d paths, %d operations, %d schemas\n",
		countPaths(doc), countOperations(doc), countSchemas(doc))
}

// assemble reads every fragment and returns the document node tree, after
// every check has passed.
func assemble(apiDir string) (*yaml.Node, error) {
	fragDir := filepath.Join(apiDir, "fragments")
	entries, err := os.ReadDir(fragDir)
	if err != nil {
		return nil, fmt.Errorf("read fragments dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no fragments under %s", fragDir)
	}
	sort.Strings(files)

	var problems []string
	problem := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	paths := yaml.Node{Kind: yaml.MappingNode}
	components := map[string]*yaml.Node{}
	for _, kind := range componentKinds {
		components[kind] = &yaml.Node{Kind: yaml.MappingNode}
	}
	operationIDs := map[string]string{}
	owner := map[string]string{} // component kind+name -> fragment, for collision messages

	mergeComponents := func(frag *yaml.Node, source string) {
		comp := child(frag, "components")
		if comp == nil {
			return
		}
		if comp.Kind != yaml.MappingNode {
			problem("%s: components is not a mapping", source)
			return
		}
		for _, kind := range componentKinds {
			bucket := child(comp, kind)
			if bucket == nil {
				continue
			}
			if bucket.Kind != yaml.MappingNode {
				problem("%s: components.%s is not a mapping", source, kind)
				continue
			}
			for i := 0; i+1 < len(bucket.Content); i += 2 {
				name := bucket.Content[i].Value
				key := kind + "/" + name
				if _, taken := owner[key]; taken {
					problem("%s: %s name collision on %s, already owned by %s", source, kind, name, owner[key])
					continue
				}
				owner[key] = source
				appendPair(components[kind], bucket.Content[i], bucket.Content[i+1])
			}
		}
		for i := 0; i+1 < len(comp.Content); i += 2 {
			kind := comp.Content[i].Value
			known := false
			for _, k := range componentKinds {
				if k == kind {
					known = true
				}
			}
			if !known {
				problem("%s: unsupported components bucket %s", source, kind)
			}
		}
	}

	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(fragDir, file))
		if err != nil {
			problem("%s: read: %v", file, err)
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			problem("%s: YAML parse failure: %v", file, err)
			continue
		}
		frag := doc.Content[0]
		if frag == nil || frag.Kind != yaml.MappingNode {
			problem("%s: fragment must be a YAML mapping", file)
			continue
		}
		var keys []string
		for i := 0; i+1 < len(frag.Content); i += 2 {
			keys = append(keys, frag.Content[i].Value)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "components,paths" {
			problem("%s: top-level keys must be exactly paths and components, found [%s]", file, strings.Join(keys, ","))
			continue
		}

		mergeComponents(frag, file)

		p := child(frag, "paths")
		if p == nil || p.Kind != yaml.MappingNode {
			problem("%s: paths is missing or not a mapping", file)
			continue
		}
		for i := 0; i+1 < len(p.Content); i += 2 {
			path, item := p.Content[i], p.Content[i+1]
			if item.Kind != yaml.MappingNode {
				problem("%s: %s is not a mapping", file, path.Value)
				continue
			}
			target := childNode(&paths, path.Value)
			if target == nil {
				appendPair(&paths, copyNode(path), &yaml.Node{Kind: yaml.MappingNode})
				target = childNode(&paths, path.Value)
			}
			for j := 0; j+1 < len(item.Content); j += 2 {
				method, op := item.Content[j], item.Content[j+1]
				if !httpMethods[method.Value] {
					problem("%s: %s carries non-method key %s; declare everything on the operation", file, path.Value, method.Value)
					continue
				}
				if childNode(target, method.Value) != nil {
					problem("%s: duplicate operation %s %s (already owned by another fragment)", file, strings.ToUpper(method.Value), path.Value)
					continue
				}
				appendPair(target, method, op)
				where := fmt.Sprintf("%s %s (%s)", strings.ToUpper(method.Value), path.Value, file)
				id := childString(op, "operationId")
				if id == "" {
					problem("%s: missing operationId", where)
				} else if prev, taken := operationIDs[id]; taken {
					problem("%s: operationId %s is also used by %s", where, id, prev)
				} else {
					operationIDs[id] = where
				}
				tags := child(op, "tags")
				if tags == nil || tags.Kind != yaml.SequenceNode || len(tags.Content) != 1 {
					problem("%s: must carry exactly one tag", where)
				}
			}
		}
	}

	doc := mapping(
		pair("openapi", scalar("3.1.0")),
		pair("info", mapping(
			pair("title", scalar("Gable API")),
			pair("version", scalar("1.0.0")),
			pair("description", scalar(`The HTTP surface of the Gable ERP core, transcribed route by route from the handlers as they behave today. Assembled from per module fragments; see docs/refactor/CONTRACT.md. The wire conventions of ADR 0001 land on these routes module by module, and each conversion edits the fragments in the same change.`)),
		)),
		pair("security", seq(mapping(pair("BearerJWT", seq())))),
		pair("tags", tagList(&paths)),
		pair("paths", &paths),
		pair("components", mapping(
			pair("securitySchemes", components["securitySchemes"]),
			pair("parameters", components["parameters"]),
			pair("responses", components["responses"]),
			pair("schemas", components["schemas"]),
		)),
	)

	// A tag with no operations is documentation of nothing; a tag used
	// twice under one path item means two modules share a path key, which
	// the method ownership check already guards. Nothing further here.

	// Templated-path collisions: the same shape under different parameter
	// names would be two routes the router cannot distinguish.
	seenTemplates := map[string]string{}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path := paths.Content[i].Value
		shape := templateShape(path)
		if prev, ok := seenTemplates[shape]; ok && prev != path {
			problem("templated path collision: %s vs %s", prev, path)
		} else {
			seenTemplates[shape] = path
		}
	}

	// Every templated path parameter is declared, inline or through a
	// $ref to a shared parameter; every local $ref resolves.
	refTargets := map[string]bool{}
	for _, kind := range componentKinds {
		bucket := components[kind]
		for i := 0; i+1 < len(bucket.Content); i += 2 {
			refTargets[fmt.Sprintf("#/components/%s/%s", kind, bucket.Content[i].Value)] = true
		}
	}
	declaredParams := map[string]string{} // shared parameter name -> in
	for i := 0; i+1 < len(components["parameters"].Content); i += 2 {
		name := components["parameters"].Content[i].Value
		p := components["parameters"].Content[i+1]
		if in := childString(p, "in"); in != "" {
			declaredParams[name] = in
		} else {
			problem("shared parameter %s carries no in", name)
		}
	}

	pathParamRe := regexp.MustCompile(`\{([^}/]+)\}`)
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i], paths.Content[i+1]
		templated := pathParamRe.FindAllStringSubmatch(path.Value, -1)
		for j := 0; j+1 < len(item.Content); j += 2 {
			method, op := item.Content[j], item.Content[j+1]
			if !httpMethods[method.Value] {
				continue
			}
			where := fmt.Sprintf("%s %s", strings.ToUpper(method.Value), path.Value)
			declared := map[string]bool{}
			params := child(op, "parameters")
			if params != nil && params.Kind == yaml.SequenceNode {
				for _, p := range params.Content {
					if p.Kind != yaml.MappingNode {
						continue
					}
					if name := childString(p, "name"); name != "" && childString(p, "in") == "path" {
						declared[name] = true
					}
					if ref := childString(p, "$ref"); ref != "" {
						name := strings.TrimPrefix(ref, "#/components/parameters/")
						if declaredParams[name] == "path" {
							declared[name] = true
						}
					}
				}
			}
			for _, m := range templated {
				if !declared[m[1]] {
					problem("%s: path parameter {%s} is not declared", where, m[1])
				}
			}
			walkRefs(op, refTargets, func(ref, trail string) {
				problem("%s: unresolved $ref %s at %s", where, ref, trail)
			})
		}
	}
	for _, kind := range componentKinds {
		walkRefs(components[kind], refTargets, func(ref string, trail string) {
			problem("unresolved $ref %s at components.%s.%s", ref, kind, trail)
		})
	}

	sortResponses(&paths)

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("merge failed with %d problem(s):\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return doc, nil
}

// sortResponses lists every operation's responses by ascending status code
// (numeric, with any non numeric key such as "default" last), whatever order
// the fragment wrote them in. The order is presentation only, but one
// direction across the whole document keeps it readable and the generated
// TypeScript stable.
func sortResponses(paths *yaml.Node) {
	for i := 0; i+1 < len(paths.Content); i += 2 {
		item := paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			if !httpMethods[item.Content[j].Value] {
				continue
			}
			responses := child(item.Content[j+1], "responses")
			if responses == nil || responses.Kind != yaml.MappingNode {
				continue
			}
			type entry struct{ key, value *yaml.Node }
			var entries []entry
			for k := 0; k+1 < len(responses.Content); k += 2 {
				entries = append(entries, entry{responses.Content[k], responses.Content[k+1]})
			}
			sort.SliceStable(entries, func(a, b int) bool {
				return statusKeyLess(entries[a].key.Value, entries[b].key.Value)
			})
			responses.Content = responses.Content[:0]
			for _, e := range entries {
				responses.Content = append(responses.Content, e.key, e.value)
			}
		}
	}
}

// statusKeyLess reports whether status code a sorts before b: numerically
// when both are numeric, with any non numeric key (a "default") after every
// number.
func statusKeyLess(a, b string) bool {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return na < nb
	case errA == nil:
		return true
	case errB == nil:
		return false
	default:
		return a < b
	}
}

// tagList derives the document's tag declarations from the operations
// themselves, sorted, so a tag can never be declared but unused nor used
// but undeclared.
func tagList(paths *yaml.Node) *yaml.Node {
	seen := map[string]bool{}
	var names []string
	for i := 0; i+1 < len(paths.Content); i += 2 {
		item := paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			op := item.Content[j+1]
			tags := child(op, "tags")
			if tags == nil {
				continue
			}
			for _, t := range tags.Content {
				if !seen[t.Value] {
					seen[t.Value] = true
					names = append(names, t.Value)
				}
			}
		}
	}
	sort.Strings(names)
	list := yaml.Node{Kind: yaml.SequenceNode}
	for _, n := range names {
		list.Content = append(list.Content, mapping(pair("name", scalar(n))))
	}
	return &list
}

func templateShape(path string) string {
	return pathParamRe.ReplaceAllString(path, "{}")
}

// walkRefs visits every $ref under node and reports the ones that point
// outside the assembled document's components.
func walkRefs(node *yaml.Node, targets map[string]bool, report func(ref, trail string)) {
	var walk func(n *yaml.Node, trail string)
	walk = func(n *yaml.Node, trail string) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, fmt.Sprintf("%s[%d]", trail, i))
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if k.Value == "$ref" && v.Kind == yaml.ScalarNode && strings.HasPrefix(v.Value, "#/") {
					if !targets[v.Value] {
						report(v.Value, trail)
					}
					continue
				}
				walk(v, trail+"."+k.Value)
			}
		}
	}
	walk(node, "")
}

// --- yaml.Node helpers ------------------------------------------------------

// child returns the value node of the first key named name.
func child(n *yaml.Node, name string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == name {
			return n.Content[i+1]
		}
	}
	return nil
}

func childString(n *yaml.Node, name string) string {
	c := child(n, name)
	if c == nil || c.Kind != yaml.ScalarNode {
		return ""
	}
	return c.Value
}

// childNode is child for a node that may be under construction.
func childNode(n *yaml.Node, name string) *yaml.Node {
	return child(n, name)
}

func appendPair(m *yaml.Node, key, value *yaml.Node) {
	m.Content = append(m.Content, key, value)
}

// copyNode shallow copies a node so a node reused across the assembled
// document keeps its own comment state.
func copyNode(n *yaml.Node) *yaml.Node {
	c := *n
	return &c
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"}
}

func pair(k string, v *yaml.Node) *yaml.Node {
	return mapping(scalarPair(k, v))
}

func scalarPair(k string, v *yaml.Node) *yaml.Node {
	kk := scalar(k)
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{kk, v}}
}

func mapping(children ...*yaml.Node) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, c := range children {
		m.Content = append(m.Content, c.Content...)
	}
	return m
}

func seq(children ...*yaml.Node) *yaml.Node {
	s := &yaml.Node{Kind: yaml.SequenceNode}
	s.Content = append(s.Content, children...)
	return s
}

func countPaths(doc *yaml.Node) int {
	return len(child(doc, "paths").Content) / 2
}

func countOperations(doc *yaml.Node) int {
	n := 0
	paths := child(doc, "paths")
	for i := 0; i+1 < len(paths.Content); i += 2 {
		for j := 0; j+1 < len(paths.Content[i+1].Content); j += 2 {
			if httpMethods[paths.Content[i+1].Content[j].Value] {
				n++
			}
		}
	}
	return n
}

func countSchemas(doc *yaml.Node) int {
	return len(child(child(doc, "components"), "schemas").Content) / 2
}

// firstDiffLines finds the first line pair that differs, for a readable
// check failure.
func firstDiffLines(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var la, lb string
		if i < len(al) {
			la = al[i]
		}
		if i < len(bl) {
			lb = bl[i]
		}
		if la != lb {
			return fmt.Sprintf("committed (line %d): %s\nassembled (line %d): %s", i+1, la, i+1, lb)
		}
	}
	return "(no line difference)"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "merge: "+format+"\n", args...)
	os.Exit(1)
}
