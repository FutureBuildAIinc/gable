// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"database/sql"
	"strings"
	"testing"
)

// TestSQLProbeRefusesWrites pins the harness promise that a SQL probe step
// only observes: a write sent through it fails and a read still works.
func TestSQLProbeRefusesWrites(t *testing.T) {
	if goldensDBURL == "" {
		t.Skip(skipReason)
	}
	db, err := sql.Open("pgx", goldensDBURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := runSQLProbe(db, `SELECT 1 AS one`)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read through the probe: rows=%v err=%v", rows, err)
	}

	for _, write := range []string{
		`CREATE TEMP TABLE probe_write_check (x int)`,
		`CREATE TABLE probe_write_check (x int)`,
	} {
		_, err := runSQLProbe(db, write)
		if err == nil || !strings.Contains(err.Error(), "read-only transaction") {
			t.Fatalf("write %q through the probe: err=%v, want a read-only transaction refusal", write, err)
		}
	}
}
