// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package customeractor names the acting principal for the customer
// package's rows that keep one: the alert's author and the one who ended it.
// It is a package of its own because pkg/actor reaches pkg/middleware, which
// imports internal/customer.
package customeractor

import (
	"context"

	"github.com/gablelbm/gable/pkg/actor"
)

// Resolver names the acting principal of a request.
type Resolver struct{}

// New returns the resolver.
func New() *Resolver { return &Resolver{} }

// ActorID names the principal the request acts as: the user's subject, or
// the machine key's id. A request with no principal at all is "unknown", the
// same word the customer package writes when nothing named the caller.
func (r *Resolver) ActorID(ctx context.Context) string {
	id := actor.FromContext(ctx).ID
	if id == "" {
		return "unknown"
	}
	return id
}
