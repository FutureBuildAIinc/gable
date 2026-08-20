// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
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
