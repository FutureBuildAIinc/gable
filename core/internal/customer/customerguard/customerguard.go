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
