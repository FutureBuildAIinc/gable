// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Entry represents a single audit log record for a financial operation.
type Entry struct {
	Action     string
	EntityType string
	EntityID   uuid.UUID
	UserID     string
	Changes    map[string]interface{}
}

// Logger writes audit entries to the audit_log table.
type Logger struct {
	db *database.DB
}

// NewLogger creates a new audit Logger backed by the given database.
func NewLogger(db *database.DB) *Logger {
	return &Logger{db: db}
}

// Log writes an audit entry synchronously through the caller's executor,
// resolved by the database seam: inside a transaction the row joins that
// transaction and commits or rolls back with the mutation it describes;
// outside one it goes to the pool directly. Either way the write happens
// before Log returns, so an error is returned (and logged) rather than lost
// to a goroutine. Callers inside a transaction propagate the error so a
// failed audit write fails the mutation; callers with no transaction may
// ignore it — their mutation is already committed, and Log has logged it.
//
// The actor columns record who performed the write (user, key or agent),
// resolved from the context by pkg/actor.
func (l *Logger) Log(ctx context.Context, entry Entry) error {
	act := actor.FromContext(ctx)

	// Explicit attribution on the entry wins (some callers pass the actor
	// explicitly, e.g. pricing exposure events); otherwise the resolved
	// principal's id is the attribution.
	userID := entry.UserID
	if userID == "" {
		userID = act.ID
	}

	// Extract request ID from context
	requestID := middleware.GetRequestID(ctx)

	// Marshal changes to JSON
	var changesJSON []byte
	if entry.Changes != nil {
		var err error
		changesJSON, err = json.Marshal(entry.Changes)
		if err != nil {
			slog.Error("audit: failed to marshal changes", "error", err)
			changesJSON = nil
		}
	}

	var actingAs, tool any
	if act.Kind == actor.KindAgent {
		actingAs, tool = act.ActingAs, act.Tool
	}

	_, err := l.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO audit_log (action, entity_type, entity_id, user_id, changes, request_id,
		                        actor_kind, actor_id, acting_as, tool)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		entry.Action, entry.EntityType, entry.EntityID, userID, changesJSON, requestID,
		act.Kind, userID, actingAs, tool,
	)
	if err != nil {
		slog.Error("audit: failed to write audit log",
			"action", entry.Action,
			"entity_type", entry.EntityType,
			"entity_id", entry.EntityID,
			"error", err,
		)
		return err
	}
	return nil
}

// Drain is retained for graceful-shutdown callers: writes are synchronous
// now, so there is never anything in flight to wait for.
func (l *Logger) Drain() {}
