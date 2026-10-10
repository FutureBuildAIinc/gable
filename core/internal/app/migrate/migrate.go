// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package migrate applies the SQL migrations under migrations/ in order,
// one transaction per file, recording each in schema_migrations. It is the
// body of the old cmd/migrate entry point, moved here so it can be
// imported; cmd/migrate and the one core binary's `migrate` subcommand
// both call Run.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Options are the flags migrate takes.
type Options struct {
	// Report names a read only pre flight report to run instead of the
	// migrations; "units" (ADR 0006 section 8) is the one there is.
	Report string
}

// ParseArgs parses migrate's flags: `-report units`, or nothing. Any other
// flag, a report other than units, or a stray argument is refused.
func ParseArgs(args []string) (Options, error) {
	var opts Options
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Report, "report", "", "run the named read only pre flight report (units) instead of migrating")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	// A -report given an empty value (an unset shell variable, say) is
	// refused rather than read as no report, which would migrate.
	reportSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "report" {
			reportSet = true
		}
	})
	if reportSet && opts.Report == "" {
		return Options{}, fmt.Errorf("migrate -report: a report name is required (the one report is units)")
	}
	if fs.NArg() > 0 {
		return Options{}, fmt.Errorf("migrate takes no arguments, got %q", fs.Args())
	}
	if opts.Report != "" && opts.Report != "units" {
		return Options{}, fmt.Errorf("migrate -report: unknown report %q (the one report is units)", opts.Report)
	}
	return opts, nil
}

// RunArgs parses args with ParseArgs and runs: the units pre flight report
// when -report units is given, otherwise every pending migration. A refused
// flag exits 2, like the one binary's other usage errors.
func RunArgs(args []string) {
	opts, err := ParseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print("usage: migrate [-report units]\n")
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\nusage: migrate [-report units]\n", err)
		os.Exit(2)
	}
	if opts.Report == "units" {
		RunUnitsReport()
		return
	}
	Run()
}

// Run applies every pending migration and returns. Behaviour is unchanged
// from the old entry point, including the log.Fatalf exits on failure.
// RunArgs is the entry that also takes `-report units`.
func Run() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Use standard database/sql with pgx driver for simplicity in migrations
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping DB: %v", err)
	}

	// 1. Ensure migration tracking table exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		);
	`)
	if err != nil {
		log.Fatalf("Failed to create schema_migrations table: %v", err)
	}

	// 2. Read migration files
	all, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		log.Fatalf("Failed to read migration files: %v", err)
	}

	// Skip *_down.sql rollback siblings. Without this, a rollback file sitting
	// next to its forward migration sorts immediately after it and gets applied
	// as a migration — dropping the very columns the forward file just added,
	// silently, on the next deploy. Rollbacks live in migrations/down/ (outside
	// this glob) and are applied by hand; the skip is belt-and-braces so a file
	// placed in the wrong directory cannot destroy a schema.
	files := make([]string, 0, len(all))
	for _, f := range all {
		if strings.HasSuffix(filepath.Base(f), "_down.sql") {
			log.Printf("Skipping rollback file %s (apply manually)", filepath.Base(f))
			continue
		}
		files = append(files, f)
	}
	sort.Strings(files)

	// 3. Apply migrations
	for _, file := range files {
		base := filepath.Base(file)
		// simple version extraction: everything before the first underscore or just the filename
		// Assuming format "001_name.sql"

		var exists bool
		err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", base).Scan(&exists)
		if err != nil {
			log.Fatalf("Failed to check migration status for %s: %v", base, err)
		}

		if exists {
			fmt.Printf("Skipping %s (already applied)\n", base)
			continue
		}

		fmt.Printf("Applying %s...\n", base)
		content, err := os.ReadFile(file)
		if err != nil {
			log.Fatalf("Failed to read file %s: %v", file, err)
		}

		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			log.Fatalf("Failed to begin transaction: %v", err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			log.Fatalf("Failed to execute migration %s: %v", base, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES ($1)", base); err != nil {
			tx.Rollback()
			log.Fatalf("Failed to record migration %s: %v", base, err)
		}

		if err := tx.Commit(); err != nil {
			log.Fatalf("Failed to commit transaction for %s: %v", base, err)
		}
		fmt.Printf("Applied %s successfully.\n", base)
	}
}
