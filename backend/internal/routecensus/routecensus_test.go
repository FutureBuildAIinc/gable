// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package routecensus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRoutesFileMatchesSources regenerates the route census from the Go
// sources and compares it against the committed api/ROUTES.txt. It needs no
// database: the census is a source walk. When the two disagree the failure
// names every added and removed route and the command that regenerates the
// file, so a route cannot silently appear or disappear.
func TestRoutesFileMatchesSources(t *testing.T) {
	root, err := FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}

	result, err := Collect(root)
	if err != nil {
		t.Fatalf("collect routes: %v", err)
	}
	if len(result.Routes) == 0 {
		t.Fatal("census found no routes; the source walk is broken")
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}

	got := Render(result.Routes)
	path := filepath.Join(root, "api", "ROUTES.txt")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRegenerate it from the Go module root with:\n\n\tgo run ./cmd/census -write\n", path, err)
	}

	if got == string(want) {
		return
	}

	added, removed := diffRoutes(string(want), got)
	var b strings.Builder
	fmt.Fprintf(&b, "%s is stale: %d route(s) added, %d route(s) removed\n", "api/ROUTES.txt", len(added), len(removed))
	for _, r := range added {
		fmt.Fprintf(&b, "\tadded:   %s\n", r)
	}
	for _, r := range removed {
		fmt.Fprintf(&b, "\tremoved: %s\n", r)
	}
	b.WriteString("\nRegenerate the file from the Go module root and commit it:\n\n\tgo run ./cmd/census -write\n")
	t.Fatal(b.String())
}

// diffRoutes compares two rendered censuses by route key and returns one
// human line per added and removed route.
func diffRoutes(oldRender, newRender string) (added, removed []string) {
	type entry struct {
		method, pattern, module string
	}
	parse := func(render string) map[string]entry {
		routes := map[string]entry{}
		for _, line := range strings.Split(render, "\n") {
			if strings.HasPrefix(line, "#") || line == "" || strings.HasPrefix(line, "method\t") {
				continue
			}
			fields := strings.Split(line, "\t")
			if len(fields) != 4 {
				continue
			}
			routes[fields[0]+" "+fields[1]] = entry{method: fields[0], pattern: fields[1], module: fields[2]}
		}
		return routes
	}
	oldRoutes := parse(oldRender)
	newRoutes := parse(newRender)
	for k, e := range newRoutes {
		if _, ok := oldRoutes[k]; !ok {
			added = append(added, fmt.Sprintf("%s %s (%s)", e.method, e.pattern, e.module))
		}
	}
	for k, e := range oldRoutes {
		if _, ok := newRoutes[k]; !ok {
			removed = append(removed, fmt.Sprintf("%s %s (%s)", e.method, e.pattern, e.module))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
