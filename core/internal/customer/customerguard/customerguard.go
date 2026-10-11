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
	return customer.SharedVerdict{Refuse: v.Refuse, Branch: v.Branch, RefuseCaller: v.RefuseCaller, Audit: v.Audit}, nil
}

// RecordRefusal writes the refusal's key.branch_refused row.
func (a *Shared) RecordRefusal(ctx context.Context) { a.w.RecordSharedRefusal(ctx) }

// RecordChange writes the dealer wide key's audit row.
func (a *Shared) RecordChange(ctx context.Context, customerID uuid.UUID, changes map[string]any) error {
	return a.w.RecordSharedChange(ctx, customerID, changes)
}
