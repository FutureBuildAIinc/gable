// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package branchctx holds the context primitives for branch scoping.
// It is intentionally tiny so it can be imported by lower-level packages
// (e.g. customer) without creating an import cycle with pkg/middleware,
// which itself depends on internal/customer for partner authentication.
package branchctx

import (
	"context"

	"github.com/google/uuid"
)

type contextKey struct{}

// Key is the value used to store *Context in request contexts.
var Key = contextKey{}

// Context describes the branch scoping that applies to a request.
//
// BranchID is nil whenever the request is NOT pinned to a single branch, and
// a nil BranchID means "all branches" to every consumer of IDForQuery. Do not
// assume a handler always sees a non-nil BranchID for non-admin callers — it
// does not. See pkg/middleware.BranchMiddleware.Handler for the authority;
// as of this writing BranchID is nil when any of the following holds:
//
//   - the `multi_branch_enabled` kill switch is off (migration 059 seeds it
//     'false', so this is the out-of-the-box state): the middleware forces
//     IsAdmin=true and leaves BranchID nil for every request, admin or not;
//   - the caller is admin/owner and omitted X-Branch-Id, electing
//     "all branches";
//   - claims are nil (AUTH_MODE=dev), which the middleware treats
//     permissively;
//   - the caller is non-admin, omitted X-Branch-Id, and
//     `default_branch_required` is false. Migration 059 seeds it 'true' (the
//     header is then mandatory and a missing one is a 400), but an operator
//     can flip it, and a non-admin request will then reach the handler
//     unscoped.
//
// The practical consequence: a new branch-scoped query must not treat
// IDForQuery()==nil as "impossible" or as a bug. It is a supported state that
// widens the query to every branch, and any endpoint for which that is not
// acceptable has to enforce its own scoping rather than relying on this type.
type Context struct {
	BranchID *uuid.UUID
	UserSub  string
	IsAdmin  bool
}

// FromContext returns the branch Context attached to ctx, or nil if the
// branch middleware did not run on this path.
func FromContext(ctx context.Context) *Context {
	bc, _ := ctx.Value(Key).(*Context)
	return bc
}

// IDForQuery returns the *uuid.UUID suitable for the
// "WHERE ($1::uuid IS NULL OR branch_id = $1)" idiom used by branch-scoped
// repositories. It returns nil both when no branch context is present (the
// middleware did not run on this path) and when the context is present but
// unscoped — see Context for the full list of ways that happens. In the
// query idiom above, nil matches every branch.
func IDForQuery(ctx context.Context) *uuid.UUID {
	bc := FromContext(ctx)
	if bc == nil {
		return nil
	}
	return bc.BranchID
}

// With injects a branch Context onto ctx. Used by test helpers and by
// receivers (e.g. A2A) that bypass the HTTP branch middleware.
func With(ctx context.Context, bc *Context) context.Context {
	return context.WithValue(ctx, Key, bc)
}
