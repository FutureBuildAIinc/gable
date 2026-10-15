// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The day a printed document dates a moment on (G1 notes N14, N15): the
// document's branch's own day, the default branch's when the document names
// none, UTC when neither has a zone.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBusinessDateIsTheBranchsThenTheDefaultBranchsDay(t *testing.T) {
	w := newARWorld(t)
	ctx := context.Background()
	branch := uuid.New()
	w.exec(`INSERT INTO locations (id, type, code, timezone) VALUES ($1, 'BRANCH', $2, 'Pacific/Kiritimati')`, branch, "DZ-"+branch.String()[:8])
	t.Cleanup(func() { w.exec(`DELETE FROM locations WHERE id = $1`, branch) })
	var prior string
	if err := w.db.Pool.QueryRow(ctx, `SELECT timezone FROM locations WHERE id = `+arBranch).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE locations SET timezone = 'America/Vancouver' WHERE id = `+arBranch)
	t.Cleanup(func() { w.exec(`UPDATE locations SET timezone = $1 WHERE id = `+arBranch, prior) })

	day := func(b uuid.UUID, at time.Time) string {
		t.Helper()
		d, err := w.svc.BusinessDate(ctx, b, at)
		if err != nil {
			t.Fatal(err)
		}
		return d.Format("2006-01-02")
	}
	// Vancouver in July is UTC-7: its midnight is 07:00:00Z
	before := time.Date(2026, 7, 1, 6, 59, 59, 0, time.UTC)
	after := time.Date(2026, 7, 1, 7, 0, 0, 0, time.UTC)
	if got := day(uuid.Nil, before); got != "2026-06-30" {
		t.Errorf("default branch, a second before its midnight: %s, want 2026-06-30", got)
	}
	if got := day(uuid.Nil, after); got != "2026-07-01" {
		t.Errorf("default branch, at its midnight: %s, want 2026-07-01", got)
	}
	// Kiritimati is UTC+14: 06:59:59Z is already the next day there
	if got := day(branch, before); got != "2026-07-01" {
		t.Errorf("the document's own branch: %s, want 2026-07-01", got)
	}
}
