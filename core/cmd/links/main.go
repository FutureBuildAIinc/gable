// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command links writes the link resolver's declarative table to
// core/api/links.json: the entity, number pattern and per frontend path
// pattern of every record an agent may ask
// GET /api/v1/links/<module>/{id} about. The generated file is the one the
// desk, the door and the shell drift tests read (ADR 0007 section 8), so no
// frontend carries its own route table.
//
// Run it from the Go module root (or anywhere under it):
//
//	go run ./cmd/links            write the table to stdout
//	go run ./cmd/links -write     write it to api/links.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/gablelbm/gable/internal/links"
	"github.com/gablelbm/gable/internal/routecensus"
)

func main() {
	rootFlag := flag.String("root", "", "module root (default: the nearest ancestor directory holding go.mod)")
	writeFlag := flag.Bool("write", false, "write api/links.json instead of stdout")
	fileFlag := flag.String("file", "api/links.json", "output file, relative to the module root")
	flag.Parse()

	root := *rootFlag
	if root == "" {
		var err error
		root, err = routecensus.FindModuleRoot(".")
		if err != nil {
			log.Fatal(err)
		}
	}

	table := links.Table()
	out := struct {
		Rows []links.Row `json:"entities"`
	}{Rows: table}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		log.Fatal(err)
	}

	if *writeFlag {
		path := filepath.Join(root, *fileFlag)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s: %d entities\n", path, len(table))
		return
	}
	_, _ = os.Stdout.Write(buf.Bytes())
}
