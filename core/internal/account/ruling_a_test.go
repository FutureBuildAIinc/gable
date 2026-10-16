// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// Ruling A on the account routes (ADR 0007 section 5.8): the summary is the
// customer record's own figures and reads through the customer module's
// reach (a user and an unbound key reach every customer, a bound key keeps its
// pin), while the subledger and the statement keep the finance documents'
// wall and answer 404 for a customer outside the caller's yard.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

func TestAccountSummaryReadsThroughRulingAWhileLedgerKeepsItsWall(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	branchA, branchB, cust := uuid.New(), uuid.New(), uuid.New()
	sub := "ruling-a-" + uuid.NewString()[:8]
	for _, b := range []uuid.UUID{branchA, branchB} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, b, "ra-"+b.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, credit_limit, balance_due)
		VALUES ($1, 'Ruling A Co', $2, $3, 500.00, 120.00)`, cust, "RA-"+cust.String()[:8], branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, cust, branchA); err != nil {
		t.Fatalf("seed customer branch: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, branchB); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = db.Pool.Exec(c, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, cust)
		_, _ = db.Pool.Exec(c, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	h := account.NewHandler(account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default()))
	get := func(fn http.HandlerFunc, path string, rctx context.Context) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(rctx)
		r.SetPathValue("id", cust.String())
		w := httptest.NewRecorder()
		fn(w, r)
		return w.Code
	}
	callers := []struct {
		name                  string
		ctx                   context.Context
		summary, ledger, stmt int
	}{
		{"user on yard B by header", branchctx.With(ctx, &branchctx.Context{BranchID: &branchB, UserSub: sub}), 200, 404, 404},
		{"user granted B only, no header", branchctx.With(ctx, &branchctx.Context{UserSub: sub}), 200, 404, 404},
		{"key bound to B", branchctx.WithKeyBranch(branchctx.With(ctx, &branchctx.Context{BranchID: &branchB}), branchB), 404, 404, 404},
		{"unbound key", branchctx.With(ctx, &branchctx.Context{}), 200, 200, 200},
	}
	for _, c := range callers {
		if got := get(h.HandleSummary, "/api/v1/accounts/"+cust.String(), c.ctx); got != c.summary {
			t.Errorf("%s: summary answered %d, want %d", c.name, got, c.summary)
		}
		if got := get(h.HandleTransactions, "/api/v1/accounts/"+cust.String()+"/transactions", c.ctx); got != c.ledger {
			t.Errorf("%s: subledger answered %d, want %d", c.name, got, c.ledger)
		}
		if got := get(h.HandleStatement, "/api/v1/ar/customers/"+cust.String()+"/statement", c.ctx); got != c.stmt {
			t.Errorf("%s: statement answered %d, want %d", c.name, got, c.stmt)
		}
	}
}
