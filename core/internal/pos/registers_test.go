// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// twoYards seeds two branches, a register at each, a user granted only the
// first, and one customer reachable at each branch only.
type twoYards struct {
	repo             *pos.PostgresRepository
	a, b             uuid.UUID
	regA, regB       string
	sub              string
	custA, custB     uuid.UUID
	nameA, nameB     string
	codeA, codeB     string
	needleA, needleB string
}

func seedTwoYards(t *testing.T) *twoYards {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	y := &twoYards{repo: pos.NewRepository(db), a: uuid.New(), b: uuid.New(), sub: "user-" + uuid.NewString(),
		custA: uuid.New(), custB: uuid.New()}
	tag := uuid.NewString()[:8]
	y.codeA, y.codeB = "RA-"+tag, "RB-"+tag
	y.needleA, y.needleB = "Alderwood"+tag, "Birchwood"+tag
	y.regA, y.regB = "RGA-"+tag, "RGB-"+tag
	exec := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'Yard A')`, y.a, y.codeA)
	exec(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'Yard B')`, y.b, y.codeB)
	exec(`INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'Second till')`, y.regA, uuid.New(), y.a)
	exec(`INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'Front till')`, y.regB, uuid.New(), y.b)
	exec(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, y.sub, y.a)
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`, y.custA, y.needleA+" Co", "CA-"+tag, y.a)
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`, y.custB, y.needleB+" Co", "CB-"+tag, y.b)
	exec(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, y.custA, y.a)
	exec(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, y.custB, y.b)
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM customer_branches WHERE customer_id IN ($1, $2)`,
		} {
			_, _ = db.Pool.Exec(ctx, q, y.custA, y.custB)
		}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id IN ($1, $2)`, y.custA, y.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, y.sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id IN ($1, $2)`, y.regA, y.regB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, y.a, y.b)
	})
	return y
}

func (y *twoYards) header(branch uuid.UUID) context.Context {
	return branchctx.With(context.Background(), &branchctx.Context{BranchID: &branch, UserSub: y.sub})
}

func (y *twoYards) bound(branch uuid.UUID) context.Context {
	return branchctx.WithKeyBranch(y.header(branch), branch)
}

func registerIDs(regs []pos.CounterRegister) map[string]pos.CounterRegister {
	out := map[string]pos.CounterRegister{}
	for _, r := range regs {
		out[r.ID] = r
	}
	return out
}

// RULE (PR 119's walls): the registers list is the caller's reach: a context
// branch, else the user's grants, else every branch for an administrator, and
// a branch bound key's pin.
func TestRegistersListHoldsTheBranchWalls(t *testing.T) {
	y := seedTwoYards(t)
	only := func(name string, ctx context.Context, want, notWant string) {
		t.Helper()
		regs, err := y.repo.ListRegisters(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := registerIDs(regs)
		if _, ok := got[want]; !ok {
			t.Errorf("%s: %s missing from %v", name, want, regs)
		}
		if notWant != "" {
			if _, ok := got[notWant]; ok {
				t.Errorf("%s: %s must not be listed", name, notWant)
			}
		}
	}
	only("header A", y.header(y.a), y.regA, y.regB)
	only("header B", y.header(y.b), y.regB, y.regA)
	only("grants of A, no header", branchctx.With(context.Background(), &branchctx.Context{UserSub: y.sub}), y.regA, y.regB)
	only("bound key pinned B", y.bound(y.b), y.regB, y.regA)

	admin := branchctx.With(context.Background(), &branchctx.Context{UserSub: "admin", IsAdmin: true})
	regs, err := y.repo.ListRegisters(admin)
	if err != nil {
		t.Fatal(err)
	}
	got := registerIDs(regs)
	if _, a := got[y.regA]; !a {
		t.Errorf("admin with no header: register A missing")
	}
	if _, b := got[y.regB]; !b {
		t.Errorf("admin with no header: register B missing")
	}

	r := got[y.regA]
	if r.Name != "Second till" || r.BranchID != y.a || r.BranchCode != y.codeA || r.BranchName != "Yard A" {
		t.Errorf("register A = %+v, want its name, branch id, branch code and branch name", r)
	}
}

// RULE (ruling A at the counter): the lookup answers every customer to a
// user, whatever the user's yard or grants; a branch bound key keeps its pin.
func TestCounterLookupFollowsRulingA(t *testing.T) {
	y := seedTwoYards(t)
	find := func(ctx context.Context, q string) int {
		t.Helper()
		got, err := y.repo.FindCounterCustomers(ctx, q, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := find(y.header(y.a), y.needleB); n != 1 {
		t.Errorf("a user at yard A finding yard B's customer: %d, want 1", n)
	}
	grantsOnly := branchctx.With(context.Background(), &branchctx.Context{UserSub: y.sub})
	if n := find(grantsOnly, y.needleB); n != 1 {
		t.Errorf("a user granted only A finding B's customer: %d, want 1", n)
	}
	if n := find(y.bound(y.a), y.needleB); n != 0 {
		t.Errorf("a key pinned to A finding B's customer: %d, want 0", n)
	}
	if n := find(y.bound(y.a), y.needleA); n != 1 {
		t.Errorf("a key pinned to A finding A's customer: %d, want 1", n)
	}
	// the sale's own naming reads the same reach
	if c, ok, err := y.repo.CounterCustomerByID(y.header(y.a), y.custB); err != nil || !ok || c.ID != y.custB {
		t.Errorf("a user at A naming B's customer: %v %v %v, want found", c, ok, err)
	}
	if _, ok, _ := y.repo.CounterCustomerByID(y.bound(y.a), y.custB); ok {
		t.Errorf("a key pinned to A naming B's customer: found, want missing")
	}
}
