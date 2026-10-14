// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// The repository's own walls (SEC-NOHEADER-1): the service's act hold refuses
// an act on another branch's sale by name before any read, so a wire probe
// cannot tell whether the sale read and lock still carry the wall
// underneath. This drives the repository itself: a caller granted only A
// neither reads nor locks B's sale, and an administrator does.
func TestSaleReadAndLockWallHoldTheGrants(t *testing.T) {
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
		`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id, currency) VALUES ($1, $2, $3, 'OPEN', $4, 'USD')`,
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
	held := branchctx.With(ctx, &branchctx.Context{UserSub: sub})
	notFound := func(err error) bool {
		var he *httpx.Error
		return errors.As(err, &he) && he.Status == http.StatusNotFound
	}
	if _, err := repo.GetSale(held, sale); !notFound(err) {
		t.Errorf("a caller granted only A read B's sale: %v, want the missing sale's 404", err)
	}
	if err := repo.LockSale(held, sale); !notFound(err) {
		t.Errorf("a caller granted only A locked B's sale: %v, want the missing sale's 404", err)
	}

	// The control: a caller the wall does not hold reads and locks it.
	admin := branchctx.With(ctx, &branchctx.Context{UserSub: "admin", IsAdmin: true})
	if got, err := repo.GetSale(admin, sale); err != nil || got.ID != sale {
		t.Errorf("the admin read of B's sale: %v %v, want the sale", got, err)
	}
	if err := repo.LockSale(admin, sale); err != nil {
		t.Errorf("the admin lock of B's sale: %v", err)
	}
}
