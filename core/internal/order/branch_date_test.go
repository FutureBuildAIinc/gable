// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestBranchLocalDate pins the arithmetic behind the invoice date at the
// instants the characterisation goldens' clock window opens and closes. The
// business date is the branch's local date, so for a branch behind UTC it
// trails the UTC date from UTC midnight until the branch's own midnight; the
// goldens avoid that window by holding their branches on Etc/UTC (see
// pinBranchZonesToUTC), and this test is the reproduction that the window is
// real and that a UTC branch has none.
func TestBranchLocalDate(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := order.NewRepository(db)

	branchIn := func(zone string) uuid.UUID {
		id := uuid.New()
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO locations (id, type, code, timezone) VALUES ($1, 'BRANCH', $2, $3)`,
			id, "TZ-"+id.String()[:8], zone); err != nil {
			t.Fatalf("insert branch in %s: %v", zone, err)
		}
		t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, id) })
		return id
	}
	utc := branchIn("Etc/UTC")
	newYork := branchIn("America/New_York")
	farWest := branchIn("Etc/GMT+8") // UTC-8 all year, no daylight rule to date the case

	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}

	// 2031-01-15 is in standard time (New York UTC-5).
	cases := []struct {
		name   string
		branch uuid.UUID
		at     string
		want   string
	}{
		{"utc after midnight", utc, "2031-01-15T00:30:00Z", "2031-01-15"},
		{"utc before midnight", utc, "2031-01-15T23:59:50Z", "2031-01-15"},
		{"new york 00:30Z is still yesterday", newYork, "2031-01-15T00:30:00Z", "2031-01-14"},
		{"new york 03:59Z is still yesterday", newYork, "2031-01-15T03:59:00Z", "2031-01-14"},
		{"new york 05:00Z is today", newYork, "2031-01-15T05:00:00Z", "2031-01-15"},
		{"new york 23:59:50Z is today", newYork, "2031-01-15T23:59:50Z", "2031-01-15"},
		{"eight behind 07:59Z is still yesterday", farWest, "2031-01-15T07:59:00Z", "2031-01-14"},
		{"eight behind 08:00Z is today", farWest, "2031-01-15T08:00:00Z", "2031-01-15"},
	}
	for _, c := range cases {
		got, err := repo.BranchLocalDate(ctx, c.branch, at(c.at))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !got.Equal(day(c.want)) {
			t.Errorf("%s: BranchLocalDate(%s) = %s, want %s", c.name, c.at, got.Format("2006-01-02"), c.want)
		}
	}
}
