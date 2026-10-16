// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
// The guard fails closed: a call with no branch Context is refused unless the
// caller marked itself a system caller with branchctx.WithSystem. A route
// mounted without the branch middleware therefore refuses a payload branch
// rather than passing it. The lookup runs on ctx's executor, so inside a transaction it
// joins it rather than taking a second connection.
func (g *BranchGuard) CheckPayloadBranch(ctx context.Context, payload uuid.UUID) error {
	bc := BranchFromContext(ctx)
	if bc == nil {
		if branchctx.IsSystem(ctx) {
			return nil
		}
		return ErrPayloadBranchRefused
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

// CheckEveryBranch is the every branch admission (ADR 0006 section 5.6): a
// write that reaches every branch of the dealer (a null branch_id price list
// slice, contract or vendor cost) calls this instead of checking one payload
// branch. It admits exactly when the request has no context branch and the
// caller is one CheckPayloadBranch would admit any branch for: an
// administrator, an unbound caller (a machine key with no user, AUTH_MODE
// dev) or a system caller marked with branchctx.WithSystem. An
// administrator working in a branch context is held to that branch, as
// CheckPayloadBranch's first rule holds them, so they are refused here; a
// bound user with no context branch has no every branch reach either. The
// refusal is ErrPayloadBranchRefused, the verdict a module maps exactly as
// it maps the payload rule's.
func (g *BranchGuard) CheckEveryBranch(ctx context.Context) error {
	bc := BranchFromContext(ctx)
	if bc == nil {
		if branchctx.IsSystem(ctx) {
			return nil
		}
		return ErrPayloadBranchRefused
	}
	if bc.BranchID != nil {
		return ErrPayloadBranchRefused
	}
	if bc.IsAdmin || bc.UserSub == "" {
		return nil
	}
	return ErrPayloadBranchRefused
}

// CheckGrantsBranch holds a branch the caller acts on, or a loaded record's
// own branch, to the caller's grants when the request's branch context
// carries no branch (SEC-NOHEADER-1, the act side of the same section 2.3
// rule): it returns nil unless the context is a user context with no branch,
// and then ErrPayloadBranchRefused unless the branch is among the user's
// user_locations grants. A nil branch refuses, because no grant decides it,
// so a user with no grants at all is refused everywhere; an administrator, an
// owner, a caller with no user sub and a caller with no branch Context at
// all (a route without the middleware, a context free test or job caller,
// which every HTTP route's middleware makes impossible) keep the reach the
// X-Branch-Id header rule already gives them, the same third arm Wall's
// predicate carries. A context branch is not this helper's case: the caller's
// own equality check governs it, so the answer for a caller that sends the
// header is unchanged.
func (g *BranchGuard) CheckGrantsBranch(ctx context.Context, branch *uuid.UUID) error {
	bc := BranchFromContext(ctx)
	if bc == nil {
		return nil
	}
	if bc.BranchID != nil || bc.IsAdmin || bc.UserSub == "" {
		return nil
	}
	if branch == nil {
		return ErrPayloadBranchRefused
	}
	ok, err := g.m.userHasBranch(ctx, bc.UserSub, *branch)
	if err != nil {
		return fmt.Errorf("branch grant lookup: %w", err)
	}
	if !ok {
		return ErrPayloadBranchRefused
	}
	return nil
}

// ErrCreateBranchChoice is the verdict GrantsForCreate returns when a user
// context with no branch holds more than one granted branch: no grant alone
// decides the branch a create would write at, so the request must name one
// (the module maps this to its 400 naming branch_id).
var ErrCreateBranchChoice = errors.New("the caller holds more than one branch grant; the request must name branch_id")

// GrantsForCreate settles the branch a create writes at for a caller whose
// context carries no branch (SEC-NOHEADER-1): a user's create takes its
// branch from the user's user_locations grants, never the deployment
// default, so the record is one its own grants let the creator read back.
// Exactly one granted branch answers it; several answer
// ErrCreateBranchChoice (the request must name one); none answer
// ErrPayloadBranchRefused (fail closed, the same refusal CheckGrantsBranch
// serves). A caller the grants never hold (a context branch, an
// administrator, an owner, the single branch switch, an unbound key, a
// system caller) answers (nil, nil) and keeps its own default resolution.
func (g *BranchGuard) GrantsForCreate(ctx context.Context) (*uuid.UUID, error) {
	bc := BranchFromContext(ctx)
	if bc == nil || bc.BranchID != nil || bc.IsAdmin || bc.UserSub == "" {
		return nil, nil
	}
	rows, err := g.m.db.GetExecutor(ctx).Query(ctx,
		`SELECT branch_id FROM user_locations WHERE user_sub = $1`, bc.UserSub)
	if err != nil {
		return nil, fmt.Errorf("branch grant lookup: %w", err)
	}
	defer rows.Close()
	var branches []uuid.UUID
	for rows.Next() {
		var b uuid.UUID
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("branch grant lookup: %w", err)
		}
		branches = append(branches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("branch grant lookup: %w", err)
	}
	switch len(branches) {
	case 0:
		return nil, ErrPayloadBranchRefused
	case 1:
		return &branches[0], nil
	default:
		return nil, ErrCreateBranchChoice
	}
}

// CheckPayloadLocation applies the same rule to a location id a body names
// where the location stands for a branch (stock moves, receiving): the
// location's own branch (its branch_id, or itself when it is a branch) must be
// one the caller may target. A location that does not exist or belongs to no
// branch is not a crossing of the wall and passes; the module's own lookup
// answers for an unknown id.
func (g *BranchGuard) CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error {
	if BranchFromContext(ctx) == nil {
		if branchctx.IsSystem(ctx) {
			return nil
		}
		return ErrPayloadBranchRefused
	}
	var branch *uuid.UUID
	err := g.m.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT CASE WHEN type = 'BRANCH' THEN COALESCE(branch_id, id) ELSE branch_id END
		   FROM locations WHERE id = $1`, locationID).Scan(&branch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("location branch lookup: %w", err)
	}
	if branch == nil {
		return nil
	}
	return g.CheckPayloadBranch(ctx, *branch)
}
