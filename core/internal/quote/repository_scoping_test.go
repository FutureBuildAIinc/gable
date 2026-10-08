// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// ListQuotesByCustomer carries the same three arm branch predicate as the
// list (ADR 0007 section 2.3). Its only production callers run with no
// branch middleware (the partner surface), so both scope parameters are nil
// there and its answers are unchanged; these tests drive the method directly
// with injected contexts, so each arm is pinned: a context branch reads its
// own quotes only, a grants sub reads its granted branches' only, none
// granted reads none, and an administrator without a header reads every
// branch's.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

func TestListQuotesByCustomer_BranchScoping(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	branchA, branchB := uuid.New(), uuid.New()
	customerID := uuid.New()

	for _, r := range []struct {
		id     uuid.UUID
		parent any
	}{{branchA, nil}, {branchB, nil}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, 'BRANCH', $2, $3, $4)`,
			r.id, "rq-"+r.id.String()[:8], "rq branch "+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'rq cust', $2, $3)`, customerID, "RQ-"+customerID.String()[:8], branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	quoteA, quoteB := uuid.New(), uuid.New()
	for _, q := range []struct {
		id     uuid.UUID
		branch uuid.UUID
	}{
		{quoteA, branchA}, {quoteB, branchB},
	} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO quotes (id, number, customer_id, state, branch_id, total_amount)
			 VALUES ($1, $2, $3, 'DRAFT', $4, 10)`, q.id, "RQQ-"+q.id.String()[:8], customerID, q.branch); err != nil {
			t.Fatalf("seed quote: %v", err)
		}
	}
	const grantsSub = "u-rq"
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`,
		grantsSub, branchA); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, grantsSub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	repo := quote.NewRepository(db)
	withBranch := func(branch *uuid.UUID, sub string, admin bool) context.Context {
		return middleware.WithBranchContext(ctx, &middleware.BranchContext{BranchID: branch, UserSub: sub, IsAdmin: admin})
	}
	ids := func(quotes []quote.QuoteSummary) map[uuid.UUID]bool {
		out := map[uuid.UUID]bool{}
		for _, q := range quotes {
			out[q.ID] = true
		}
		return out
	}
	for _, c := range []struct {
		name         string
		ctx          context.Context
		wantA, wantB bool
	}{
		{"context branch A", withBranch(&branchA, "", false), true, false},
		{"grants A, no context branch", withBranch(nil, grantsSub, false), true, false},
		{"no grants, no context branch", withBranch(nil, "u-rq-none", false), false, false},
		{"admin, no context branch", withBranch(nil, "", true), true, true},
		{"no branch context at all", ctx, true, true},
	} {
		got, err := repo.ListQuotesByCustomer(c.ctx, customerID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		seen := ids(got)
		if seen[quoteA] != c.wantA {
			t.Errorf("%s: branch A's quote present = %v, want %v", c.name, seen[quoteA], c.wantA)
		}
		if seen[quoteB] != c.wantB {
			t.Errorf("%s: branch B's quote present = %v, want %v", c.name, seen[quoteB], c.wantB)
		}
	}
}
