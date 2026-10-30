// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// registerBranchFixture seeds a register whose location_id names no location
// (the shape migration 027 gives the seeded REG-01), so only the NOT NULL
// pos_registers.branch_id says which branch it belongs to, and a second
// branch the caller can be pinned to.
func registerBranchFixture(t *testing.T) (repo *pos.PostgresRepository, register string, own, other uuid.UUID) {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo = pos.NewRepository(db)
	own = testutil.OwnBranch(t, db, "0")
	other = uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "RB-"+other.String()[:8]); err != nil {
		t.Fatalf("insert the other branch: %v", err)
	}
	register = "RB-REG-" + other.String()[:8]
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'register branch test')`,
		register, uuid.New(), own); err != nil {
		t.Fatalf("insert the register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = $1`, register)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE register_id = $1`, register)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, register)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, other)
	})
	return repo, register, own, other
}

func pinnedTo(branch uuid.UUID) context.Context {
	return branchctx.With(context.Background(), &branchctx.Context{BranchID: &branch})
}

// A register with no branched location still belongs to the branch its own
// pos_registers.branch_id names: a caller pinned to another branch is refused
// and nothing is written, and the register's own branch writes there.
func TestCreateSaleUsesTheRegistersOwnBranch(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	mk := func() *pos.Sale {
		return &pos.Sale{RegisterID: register, CashierID: uuid.New(), Currency: "USD"}
	}

	refused := mk()
	err := repo.CreateSale(pinnedTo(other), refused)
	if err == nil || !strings.Contains(err.Error(), "different branch") {
		t.Fatalf("a caller pinned to another branch: err = %v, want the different branch refusal", err)
	}
	var rows int
	if e := testutil.RequireDB(t).Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pos_transactions WHERE register_id = $1`, register).Scan(&rows); e != nil || rows != 0 {
		t.Fatalf("the refused create left %d rows (err %v), want 0", rows, e)
	}

	ok := mk()
	if err := repo.CreateSale(pinnedTo(own), ok); err != nil {
		t.Fatalf("a caller on the register's own branch: %v", err)
	}
	if ok.BranchID != own {
		t.Errorf("the sale landed at %s, want the register's branch %s", ok.BranchID, own)
	}
}

func TestGetRegisterBranchFallsBackToTheRegistersOwnBranch(t *testing.T) {
	repo, register, own, _ := registerBranchFixture(t)
	got, err := repo.GetRegisterBranch(context.Background(), register)
	if err != nil {
		t.Fatalf("GetRegisterBranch: %v", err)
	}
	if got == nil || *got != own {
		t.Errorf("GetRegisterBranch = %v, want %s (the returns path stamps and taxes by it)", got, own)
	}
}

func TestOpenTillStampsTheRegistersOwnBranch(t *testing.T) {
	repo, register, own, _ := registerBranchFixture(t)
	svc := pos.NewService(testutil.RequireDB(t), repo, slog.Default())
	session, err := svc.OpenTill(context.Background(), register, uuid.New(), 5000, "u-test")
	if err != nil {
		t.Fatalf("OpenTill: %v", err)
	}
	if session.BranchID == nil || *session.BranchID != own {
		t.Errorf("the till session branch = %v, want the register's branch %s", session.BranchID, own)
	}
}
