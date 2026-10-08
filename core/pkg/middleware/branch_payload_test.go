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

// The payload branch rule (ADR 0007 section 2.3): a branch named in a request
// body must be within the caller's branch grants.
func TestCheckPayloadBranch(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	guard := middleware.NewBranchGuard(db)

	own, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{own, other} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, id, "pb-"+id.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	sub := "user-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, own); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, own, other)
	})

	with := func(bc *branchctx.Context) context.Context { return branchctx.With(ctx, bc) }
	cases := []struct {
		name    string
		ctx     context.Context
		payload uuid.UUID
		refused bool
	}{
		{"no branch middleware (system caller)", ctx, other, false},
		{"context branch equals payload", with(&branchctx.Context{UserSub: sub, BranchID: &own}), own, false},
		{"context branch differs from payload", with(&branchctx.Context{UserSub: sub, BranchID: &own}), other, true},
		{"admin with context branch, foreign payload", with(&branchctx.Context{UserSub: "a", IsAdmin: true, BranchID: &own}), other, true},
		{"user, no context branch, granted payload", with(&branchctx.Context{UserSub: sub}), own, false},
		{"user, no context branch, ungranted payload", with(&branchctx.Context{UserSub: sub}), other, true},
		{"admin, no context branch, any payload", with(&branchctx.Context{UserSub: "a", IsAdmin: true}), other, false},
		{"unbound machine key (no sub), any payload", with(&branchctx.Context{}), other, false},
	}
	for _, c := range cases {
		err := guard.CheckPayloadBranch(c.ctx, c.payload)
		if c.refused != errors.Is(err, middleware.ErrPayloadBranchRefused) {
			t.Errorf("%s: refused=%v, want %v (err %v)", c.name, errors.Is(err, middleware.ErrPayloadBranchRefused), c.refused, err)
		}
		if !c.refused && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
	}
}
