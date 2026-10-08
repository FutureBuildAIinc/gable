// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// ErrPayloadBranchRefused is the verdict CheckPayloadBranch returns when a
// branch named in a request body is outside what the caller may target. A
// module maps it to 403 forbidden naming the payload field.
var ErrPayloadBranchRefused = errors.New("payload branch is outside the caller's branches")

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// branch a request body names, so a body can never widen the branch context
// the middleware settled.
type BranchGuard struct {
	m *BranchMiddleware
}

// NewBranchGuard builds a BranchGuard that reads grants from user_locations.
func NewBranchGuard(db *database.DB) *BranchGuard {
	return &BranchGuard{m: &BranchMiddleware{db: db}}
}

// CheckPayloadBranch returns ErrPayloadBranchRefused unless the payload
// branch is one the caller may target:
//
//   - the request's branch context names a branch: the payload must equal it;
//   - no context branch and an administrator (or the single-branch kill
//     switch, which makes every caller one): any branch, as the X-Branch-Id
//     header allows;
//   - no context branch and a user: a branch in the user's user_locations;
//   - no context branch and no user (an unbound machine key, AUTH_MODE=dev):
//     any branch, as the header allows. A branch bound key always carries a
//     context branch, so it is held to the first rule.
//
// A request the branch middleware never saw (a system caller) is not
// restricted. The lookup runs on ctx's executor, so inside a transaction it
// joins it rather than taking a second connection.
func (g *BranchGuard) CheckPayloadBranch(ctx context.Context, payload uuid.UUID) error {
	bc := BranchFromContext(ctx)
	if bc == nil {
		return nil
	}
	if bc.BranchID != nil {
		if *bc.BranchID != payload {
			return ErrPayloadBranchRefused
		}
		return nil
	}
	if bc.IsAdmin || bc.UserSub == "" {
		return nil
	}
	ok, err := g.m.userHasBranch(ctx, bc.UserSub, payload)
	if err != nil {
		return fmt.Errorf("branch grant lookup: %w", err)
	}
	if !ok {
		return ErrPayloadBranchRefused
	}
	return nil
}
