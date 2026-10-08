// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package customeraudit adapts the platform audit logger to the customer
// service's AuditLogger. It is a package of its own because pkg/audit reaches
// pkg/middleware, which imports internal/customer.
package customeraudit

import (
	"context"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// Logger writes customer audit rows through the platform logger.
type Logger struct{ l *audit.Logger }

// New wraps the platform audit logger.
func New(l *audit.Logger) *Logger { return &Logger{l: l} }

// LogChange writes one audit row for the customer; the actor columns come from
// the context, as for every audited write.
func (a *Logger) LogChange(ctx context.Context, action string, customerID uuid.UUID, changes map[string]any) error {
	return a.l.Log(ctx, audit.Entry{Action: action, EntityType: "customer", EntityID: customerID, Changes: changes})
}
