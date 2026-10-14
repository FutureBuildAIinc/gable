// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
)

// The roster is the demo's own contract (WALKS.md scene 0, the dev sign in's
// cards): nine people, each granted its home yard, subjects unique.
func TestDemoPersonasAreWellFormed(t *testing.T) {
	if len(demoPersonas) != 9 {
		t.Fatalf("%d personas, want 9", len(demoPersonas))
	}
	seen := map[string]bool{}
	for _, p := range demoPersonas {
		if seen[p.Subject] {
			t.Errorf("subject %s appears twice", p.Subject)
		}
		seen[p.Subject] = true
		home := false
		for _, y := range p.Yards {
			home = home || y == p.Home
		}
		if !home {
			t.Errorf("%s is not granted its home yard %s", p.Subject, p.Home)
		}
	}
}

// RULE: the personas land as staff rows with their grants, the home yard
// flagged, and a second run changes nothing.
func TestUpsertPersonasIsIdempotent(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	sqlDB := stdlib.OpenDBFromPool(pool.Pool)
	defer sqlDB.Close()
	yards := map[string]uuid.UUID{}
	for _, code := range allYards {
		id := uuid.New()
		if _, err := pool.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'persona test')`,
			id, "PT-"+code+"-"+id.String()[:6]); err != nil {
			t.Fatal(err)
		}
		yards[code] = id
		t.Cleanup(func() { _, _ = pool.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, id) })
	}
	// A database a demo seed already ran on holds these grants on the real
	// yards; the test starts from none and leaves none.
	clear := func() {
		for _, p := range demoPersonas {
			_, _ = pool.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, p.Subject)
		}
	}
	clear()
	t.Cleanup(clear)
	count := func() (staff, grants, homes int) {
		if err := pool.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM staff WHERE staff_no LIKE 'demo-%'`).Scan(&staff); err != nil {
			t.Fatal(err)
		}
		if err := pool.Pool.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE is_home) FROM user_locations WHERE user_sub LIKE 'demo-%'
			AND branch_id = ANY($1)`, []uuid.UUID{yards["KEL-MAIN"], yards["WK"], yards["LK"]}).Scan(&grants, &homes); err != nil {
			t.Fatal(err)
		}
		return staff, grants, homes
	}
	for run := 1; run <= 2; run++ {
		if err := upsertPersonas(sqlDB, yards); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		staff, grants, homes := count()
		// One grant per yard each persona holds, one of them the home yard.
		want := 0
		for _, p := range demoPersonas {
			want += len(p.Yards)
		}
		if staff < len(demoPersonas) || grants != want || homes != len(demoPersonas) {
			t.Errorf("run %d: %d staff, %d grants, %d homes, want >=%d, %d, %d", run, staff, grants, homes, len(demoPersonas), want, len(demoPersonas))
		}
	}
}
