// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// CheckEveryBranch is the every branch admission (ADR 0006 section 5.6): a
// write that reaches every branch (a null branch_id price list slice,
// contract or vendor cost) is admitted exactly when the request has no
// context branch and the caller is one CheckPayloadBranch would admit any
// branch for: an administrator, an unbound caller or a system caller. A
// caller working in a branch context, administrator included, is held to
// that branch and refused here; a bound user with no context branch has no
// every branch reach either. No module re-tests these arms inline.
func TestCheckEveryBranch(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	guard := middleware.NewBranchGuard(db)

	branch := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, branch, "eb-"+branch.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branch)
	})

	with := func(bc *branchctx.Context) context.Context { return branchctx.With(ctx, bc) }
	bound := branch.String()
	cases := []struct {
		name    string
		ctx     context.Context
		refused bool
	}{
		{"no branch middleware, not marked", ctx, true},
		{"no branch middleware, system caller", branchctx.WithSystem(ctx), false},
		{"administrator, no context branch", with(&branchctx.Context{UserSub: "a", IsAdmin: true}), false},
		{"unbound caller (no user sub), no context branch", with(&branchctx.Context{}), false},
		{"administrator working in a branch context", with(&branchctx.Context{UserSub: "a", IsAdmin: true, BranchID: &branch}), true},
		{"bound user in a branch context", with(&branchctx.Context{UserSub: "u-" + bound, BranchID: &branch}), true},
		{"bound user, no context branch", with(&branchctx.Context{UserSub: "u-" + bound}), true},
	}
	for _, c := range cases {
		err := guard.CheckEveryBranch(c.ctx)
		if c.refused != errors.Is(err, middleware.ErrPayloadBranchRefused) {
			t.Errorf("%s: refused=%v, want %v (err %v)", c.name, errors.Is(err, middleware.ErrPayloadBranchRefused), c.refused, err)
		}
		if !c.refused && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
	}
}
