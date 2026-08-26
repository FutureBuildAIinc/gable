// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesteam

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The roster's two rules live in SQL and therefore need a real database:
//
//  1. List filters on `is_active = true`, which is what keeps a departed rep
//     out of the assignment dropdown while their historical orders keep
//     pointing at them.
//  2. Get maps pgx.ErrNoRows onto "salesperson not found", which is what the
//     handler turns into a 404 rather than a 500.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB), which is the
// default state of a fresh clone and of CI's plain `go test ./...`.

// seedRep inserts one salesperson and removes it again at the end of the test,
// so the fixture can run against a seeded demo database without disturbing it.
func seedRep(t *testing.T, repo *PostgresRepository, name string, active bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()

	_, err := repo.db.Pool.Exec(ctx,
		`INSERT INTO sales_team (id, name, email, phone, role, is_active)
		 VALUES ($1, $2, $3, '', 'Sales Rep', $4)`,
		id, name, name+"@dealer.example", active)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Pool.Exec(context.Background(), `DELETE FROM sales_team WHERE id = $1`, id)
	})
	return id
}

// CORRECTNESS: an inactive rep is invisible to the roster listing. A departed
// salesperson who still appears in the assignment dropdown gets new customers
// routed to nobody.
func TestPostgresRepository_ListHidesInactiveReps(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))

	activeName := "PGTest Active " + uuid.NewString()[:8]
	goneName := "PGTest Departed " + uuid.NewString()[:8]
	activeID := seedRep(t, repo, activeName, true)
	goneID := seedRep(t, repo, goneName, false)

	people, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var sawActive, sawGone bool
	for _, p := range people {
		switch p.ID {
		case activeID:
			sawActive = true
			if !p.IsActive {
				t.Errorf("the active rep came back with is_active=false")
			}
		case goneID:
			sawGone = true
		}
	}
	if !sawActive {
		t.Errorf("the active rep %s is missing from the roster", activeID)
	}
	if sawGone {
		t.Errorf("the inactive rep %s is in the roster; the is_active filter is gone", goneID)
	}
}

// CORRECTNESS: List is ordered by name, which is the order the assignment
// dropdown renders in.
func TestPostgresRepository_ListIsOrderedByName(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))

	suffix := uuid.NewString()[:8]
	zID := seedRep(t, repo, "ZZPGTest Zoe "+suffix, true)
	aID := seedRep(t, repo, "AAPGTest Abe "+suffix, true)

	people, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	posA, posZ := -1, -1
	for i, p := range people {
		switch p.ID {
		case aID:
			posA = i
		case zID:
			posZ = i
		}
	}
	if posA < 0 || posZ < 0 {
		t.Fatalf("seeded reps missing from the roster (A=%d Z=%d)", posA, posZ)
	}
	if posA > posZ {
		t.Errorf("roster is not name-ordered: %q came after %q", "AAPGTest", "ZZPGTest")
	}
}

// CORRECTNESS: an inactive rep is still readable by id. The roster filter hides
// them from the picker; it must not break the order-history page that resolves
// a historical salesperson_id.
func TestPostgresRepository_GetReturnsInactiveReps(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))

	goneID := seedRep(t, repo, "PGTest Departed "+uuid.NewString()[:8], false)

	p, err := repo.Get(context.Background(), goneID)
	if err != nil {
		t.Fatalf("Get on an inactive rep: %v", err)
	}
	if p.ID != goneID {
		t.Errorf("Get returned %s, want %s", p.ID, goneID)
	}
	if p.IsActive {
		t.Errorf("is_active = true, want false")
	}
}

// CORRECTNESS: an unknown id is reported as "salesperson not found" rather than
// as a raw pgx.ErrNoRows. handler.go:55 turns that into the 404 the roster API
// promises; a different error text there would still 404, but the mapping is
// what makes the 404 mean "no such rep" rather than "the query blew up".
func TestPostgresRepository_GetMapsNoRowsToNotFound(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))

	_, err := repo.Get(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("Get on a random id succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "salesperson not found") {
		t.Errorf("error = %q, want it to say the salesperson was not found", err)
	}
	if strings.Contains(err.Error(), "no rows in result set") {
		t.Errorf("the raw pgx error leaked through the mapping: %q", err)
	}
}
