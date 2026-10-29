// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

// The branch a test owns (the shared database fixture sweep).
//
// The database holds one default branch, and every package's fixture shares
// it. A test that writes that row's tax rate or its cash rounding rule moves
// it under every other package's test that runs at the same time, and a test
// that reads it takes whatever value it finds: the CI Backend job runs
// `go test -race ./...`, so the package binaries overlap on one database.
//
// A branch of the test's own carries an id no other fixture knows, so no other
// fixture can move its rate or its rule. The caller's own rows (the customer,
// the yard, the stock) point at it, and the test that needs a different rate
// writes it on that branch alone.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// OwnBranch gives the calling test a branch of its own and returns its id.
// The rate is the branch's default_tax_rate as written: "0.088750" for the
// standard rate, "0" for a tax free branch, "" for a branch with no rate at
// all. The branch is removed when the test ends.
func OwnBranch(t *testing.T, db *database.DB, rate string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, timezone, default_tax_rate)
		VALUES ($1, 'BRANCH', $2, 'Test own branch', 'America/New_York', NULLIF($3, '')::numeric)`,
		id, "TB-"+id.String()[:8], rate); err != nil {
		t.Fatalf("own branch: %v", err)
	}
	t.Cleanup(func() { removeOwnBranch(t, db, id) })
	return id
}

// removeOwnBranch takes the branch away: the ledger rows that name it, the
// links that point at it, and the row itself. Every row it reaches names
// this branch, so another test's rows are never touched.
func removeOwnBranch(t *testing.T, db *database.DB, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("own branch cleanup: %s: %v", sql, err)
		}
	}
	exec(`UPDATE bank_transactions SET matched_journal_entry_id = NULL WHERE matched_journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, id)
	exec(`DELETE FROM gl_journal_lines WHERE branch_id = $1 OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, id)
	exec(`UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE reverses_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, id)
	exec(`DELETE FROM gl_journal_entries WHERE branch_id = $1`, id)
	exec(`DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND changes->>'branch_id' = $1`, id.String())
	exec(`DELETE FROM customer_branches WHERE branch_id = $1`, id)
	exec(`DELETE FROM locations WHERE id = $1`, id)
}

// OwnBranchRate writes the rate on the branch the test owns. It is the write
// the order fixture used to make on the shared default branch.
func OwnBranchRate(t *testing.T, db *database.DB, branchID uuid.UUID, rate string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`UPDATE locations SET default_tax_rate = NULLIF($2, '')::numeric WHERE id = $1`, branchID, rate); err != nil {
		t.Fatalf("own branch rate: %v", err)
	}
}
