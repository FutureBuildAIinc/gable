// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&own); err != nil {
		t.Fatalf("read the default branch: %v", err)
	}
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
func TestCreateTransactionUsesTheRegistersOwnBranch(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	now := time.Now()
	mk := func() *pos.POSTransaction {
		return &pos.POSTransaction{RegisterID: register, CashierID: uuid.New(), Status: pos.TransactionStatusOpen, ClientCreatedAt: &now}
	}

	refused := mk()
	err := repo.CreateTransaction(pinnedTo(other), refused)
	if err == nil || !strings.Contains(err.Error(), "different branch") {
		t.Fatalf("a caller pinned to another branch: err = %v, want the different branch refusal", err)
	}
	var rows int
	if e := testutil.RequireDB(t).Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pos_transactions WHERE register_id = $1`, register).Scan(&rows); e != nil || rows != 0 {
		t.Fatalf("the refused create left %d rows (err %v), want 0", rows, e)
	}

	ok := mk()
	if err := repo.CreateTransaction(pinnedTo(own), ok); err != nil {
		t.Fatalf("a caller on the register's own branch: %v", err)
	}
	if ok.BranchID != own {
		t.Errorf("the transaction landed at %s, want the register's branch %s", ok.BranchID, own)
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

func TestCreateTillSessionStampsTheRegistersOwnBranch(t *testing.T) {
	repo, register, own, _ := registerBranchFixture(t)
	s := &pos.TillSession{RegisterID: register, CashierID: uuid.New(), Status: pos.TillSessionOpen, OpeningFloat: 5000}
	if err := repo.CreateTillSession(context.Background(), s); err != nil {
		t.Fatalf("CreateTillSession: %v", err)
	}
	if s.BranchID == nil || *s.BranchID != own {
		t.Errorf("the till session branch = %v, want the register's branch %s", s.BranchID, own)
	}
}
