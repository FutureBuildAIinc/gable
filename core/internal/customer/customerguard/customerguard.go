// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package customerguard adapts the platform's payload branch guard to the
// customer service's BranchGuard. It is a package of its own because
// pkg/middleware imports internal/customer.
package customerguard

import (
	"context"
	"errors"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Guard holds a branch a customer body names to the caller's branch context.
type Guard struct{ g *middleware.BranchGuard }

// New wraps the platform guard.
func New(g *middleware.BranchGuard) *Guard { return &Guard{g: g} }

// CheckPayloadBranch is the platform rule; a refusal is customer.ErrBranchRefused.
func (a *Guard) CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error {
	err := a.g.CheckPayloadBranch(ctx, branch)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return customer.ErrBranchRefused
	}
	return err
}

// GrantsForCreate is the platform's create branch resolution (one grant:
// that one; several: the choice error; none: the refusal), mapped onto the
// customer package's own sentinels.
func (a *Guard) GrantsForCreate(ctx context.Context) (*uuid.UUID, error) {
	branch, err := a.g.GrantsForCreate(ctx)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return nil, customer.ErrBranchRefused
	}
	if errors.Is(err, middleware.ErrCreateBranchChoice) {
		return nil, customer.ErrCreateBranchChoice
	}
	return branch, err
}

// Shared adapts the key branch wall to the customer service's SharedGuard:
// the shared customer rule of principal rulings 43, 44 and 45.
type Shared struct{ w *middleware.KeyBranchWall }

// NewShared wraps the key branch wall.
func NewShared(w *middleware.KeyBranchWall) *Shared { return &Shared{w: w} }

// Shared answers how the caller stands to the customer.
func (a *Shared) Shared(ctx context.Context, customerID uuid.UUID) (customer.SharedVerdict, error) {
	v, err := a.w.SharedCustomer(ctx, customerID)
	if err != nil {
		return customer.SharedVerdict{}, err
	}
	return customer.SharedVerdict{Refuse: v.Refuse, Branch: v.Branch, RefuseCaller: v.RefuseCaller,
		RefuseCallerKey: v.RefuseCallerKey, Audit: v.Audit}, nil
}

// RecordRefusal writes the refusal's key.branch_refused row.
func (a *Shared) RecordRefusal(ctx context.Context) { a.w.RecordSharedRefusal(ctx) }

// CheckChild answers the shared customer rule for a child record of a customer
// that another module owns: the bound key's 403 on a shared customer, nil
// otherwise. A caller that is not a branch bound key is never refused.
func (a *Shared) CheckChild(ctx context.Context, customerID uuid.UUID, field string) error {
	v, err := a.w.SharedCustomer(ctx, customerID)
	if err != nil {
		return err
	}
	if !v.Refuse {
		return nil
	}
	return customer.RefusedOnChild(v.Branch, field)
}

// RecordCallerRefusal writes the refusal row of a user or an unbound key.
func (a *Shared) RecordCallerRefusal(ctx context.Context, customerID, branch uuid.UUID, fields []string) {
	a.w.RecordSharedCallerRefusal(ctx, customerID, branch, fields)
}

// RecordChange writes the dealer wide key's audit row.
func (a *Shared) RecordChange(ctx context.Context, customerID uuid.UUID, changes map[string]any) error {
	return a.w.RecordSharedChange(ctx, customerID, changes)
}
