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
