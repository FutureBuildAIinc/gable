// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command pending regenerates core/api/contract-pending.txt: every census
// route that has no contract operation yet, one per line. Run it after
// go run ./api/tools/merge whenever fragments land or routes are added;
// commit the shrunk list with the fragment that covers its routes.
//
// The default mode compares the committed file against a fresh render and
// exits non zero on any difference (the drift twin of the coverage test);
// -write writes it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/apicontract"
	"github.com/gablelbm/gable/internal/routecensus"
)

const fileHeader = `# SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Routes still without a contract operation, one per line, tab separated:
# method, pattern, exactly as core/api/ROUTES.txt lists them. The coverage
# test (internal/apicontract) fails while a route here has no operation,
# fails when a route here already gained one (so entries only ever leave
# this file), and fails for an operation no route backs. A module fragment
# lands in the same change that removes its routes from this list.
#
# Generated file. Do not edit by hand; regenerate from the Go module root:
#
#	go run ./api/tools/merge && go run ./cmd/pending -write
`

func main() {
	write := flag.Bool("write", false, "write the regenerated list instead of only checking it")
	flag.Parse()

	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		fatal("find module root: %v", err)
	}
	census, err := apicontract.ParseCensus(filepath.Join(root, "api", "ROUTES.txt"))
	if err != nil {
		fatal("%v", err)
	}
	spec, err := apicontract.Load(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		fatal("%v", err)
	}

	var pending []string
	for _, r := range census {
		if !spec.Covers(r.Method, r.Pattern) {
			pending = append(pending, r.Method+"\t"+r.Pattern)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })

	var out strings.Builder
	out.WriteString(fileHeader)
	for _, line := range pending {
		out.WriteString(line)
		out.WriteString("\n")
	}

	target := filepath.Join(root, "api", "contract-pending.txt")
	if !*write {
		committed, err := os.ReadFile(target)
		if err != nil {
			fatal("read %s: %v\nRegenerate it with: go run ./cmd/pending -write", target, err)
		}
		if string(committed) != out.String() {
			fatal("%s is stale: the census and the contract assemble to a different pending list.\nRegenerate and commit it:\n\n\tgo run ./api/tools/merge && go run ./cmd/pending -write", target)
		}
		fmt.Printf("contract-pending.txt is fresh: %d routes pending, %d covered\n",
			len(pending), len(census)-len(pending))
		return
	}
	if err := os.WriteFile(target, []byte(out.String()), 0o644); err != nil {
		fatal("write %s: %v", target, err)
	}
	fmt.Printf("contract-pending.txt written: %d routes pending, %d covered\n",
		len(pending), len(census)-len(pending))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pending: "+format+"\n", args...)
	os.Exit(1)
}
