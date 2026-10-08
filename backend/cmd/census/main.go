// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command census lists every HTTP route the Go sources of this module
// register, as the route census (internal/routecensus) sees them.
//
// Run it from the Go module root (or anywhere under it):
//
//	go run ./cmd/census            write the census to stdout
//	go run ./cmd/census -write     write it to api/ROUTES.txt
//
// The committed api/ROUTES.txt is pinned by the census test in
// internal/routecensus: a route added or removed without regenerating the
// file fails `go test ./...`.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/gablelbm/gable/internal/routecensus"
)

func main() {
	rootFlag := flag.String("root", "", "module root (default: the nearest ancestor directory holding go.mod)")
	writeFlag := flag.Bool("write", false, "write api/ROUTES.txt instead of stdout")
	fileFlag := flag.String("file", "api/ROUTES.txt", "output file, relative to the module root")
	flag.Parse()

	root := *rootFlag
	if root == "" {
		var err error
		root, err = routecensus.FindModuleRoot(".")
		if err != nil {
			log.Fatal(err)
		}
	}

	result, err := routecensus.Collect(root)
	if err != nil {
		log.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		log.Fatal(err)
	}
	out := routecensus.Render(result.Routes)

	if *writeFlag {
		path := filepath.Join(root, *fileFlag)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s: %d routes\n", path, len(result.Routes))
		return
	}
	os.Stdout.WriteString(out)
}
