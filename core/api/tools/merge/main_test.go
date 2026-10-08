// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/routecensus"
	"gopkg.in/yaml.v3"
)

// TestResponsesSortedAscending pins the document's readability rule: every
// operation's responses are listed by ascending status code (numeric, with
// any non numeric key such as "default" last), whatever order its fragment
// wrote them in. The payment fragment once carried 402 after 403; the
// assembler now normalizes, so a fragment can never ship a shuffled list.
func TestResponsesSortedAscending(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	doc, err := assemble(filepath.Join(root, "api"))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	paths := child(doc, "paths")
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i], paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			method, op := item.Content[j], item.Content[j+1]
			if !httpMethods[method.Value] {
				continue
			}
			responses := child(op, "responses")
			if responses == nil || responses.Kind != yaml.MappingNode {
				t.Fatalf("%s %s: responses is missing or not a mapping", method.Value, path.Value)
			}
			var keys []string
			for k := 0; k+1 < len(responses.Content); k += 2 {
				keys = append(keys, responses.Content[k].Value)
			}
			if !sort.SliceIsSorted(keys, func(a, b int) bool { return statusKeyLess(keys[a], keys[b]) }) {
				t.Fatalf("%s %s: response codes are not ascending: %v",
					strings.ToUpper(method.Value), path.Value, keys)
			}
		}
	}
}

// TestTemplatedPathCollisions pins the collision rule: two spellings of one
// templated shape collide when they share a method, and ServeMux's habit of
// letting GET also match HEAD makes a GET beside a HEAD collide too.
func TestTemplatedPathCollisions(t *testing.T) {
	cases := []struct {
		name  string
		paths string
		want  int
	}{
		{"same method, different parameter name", `
/x/{id}:
  get: {}
/x/{other}:
  get: {}
`, 1},
		{"different method, same shape", `
/x/{id}:
  get: {}
/x/{other}:
  delete: {}
`, 0},
		{"GET beside HEAD on one shape", `
/x/{id}:
  get: {}
/x/{other}:
  head: {}
`, 1},
		{"HEAD beside GET on one shape", `
/x/{id}:
  head: {}
/x/{other}:
  get: {}
`, 1},
		{"POST beside HEAD on one shape", `
/x/{id}:
  post: {}
/x/{other}:
  head: {}
`, 0},
		{"one path with several methods", `
/x/{id}:
  get: {}
  delete: {}
`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.paths), &doc); err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := templateCollisions(doc.Content[0])
			if len(got) != tc.want {
				t.Fatalf("want %d collisions, got %d: %v", tc.want, len(got), got)
			}
		})
	}
}
