// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The write side walls the service holds cannot answer for (SEC-NOHEADER-1):
// the service's holdSaleBranch refuses an act on another branch's sale
// before the write, so a wire probe cannot tell whether the update wall
// still matches underneath. This drives the repository itself: a caller
// granted only A completes B's sale, and the wall leaves the row alone.
func TestUpdateTransactionWallHoldsTheGrants(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	branchA, branchB, yardB := uuid.New(), uuid.New(), uuid.New()
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{branchA, "BRANCH", nil}, {branchB, "BRANCH", nil}, {yardB, "YARD", branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, $2, $3, $4)`,
			r.id, r.typ, "uw-"+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	sub := "user-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, branchA); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	reg := "UW-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'uw')`, reg, yardB, branchB); err != nil {
		t.Fatalf("seed register: %v", err)
	}
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id) VALUES ($1, $2, $3, 'OPEN', $4)`,
		sale, reg, uuid.New(), branchB); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, sale)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2, $3)`, branchA, branchB, yardB)
	})

	repo := pos.NewRepository(db)
	now := time.Now()
	completed := sale
	done := pos.POSTransaction{ID: completed, Status: pos.TransactionStatusCompleted, CompletedAt: &now}
	if err := repo.UpdateTransaction(branchctx.With(ctx, &branchctx.Context{UserSub: sub}), &done); err != nil {
		t.Fatalf("walled update: %v", err)
	}
	var status string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM pos_transactions WHERE id = $1`, sale).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "OPEN" {
		t.Errorf("the walled update completed B's sale (status %s), want it left OPEN", status)
	}

	// The control: a caller the wall does not hold completes it.
	if err := repo.UpdateTransaction(branchctx.With(ctx, &branchctx.Context{UserSub: "admin", IsAdmin: true}), &done); err != nil {
		t.Fatalf("admin update: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM pos_transactions WHERE id = $1`, sale).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" {
		t.Errorf("the admin update left the sale %s, want COMPLETED", status)
	}
}
