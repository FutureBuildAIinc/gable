// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
	"unicode/utf8"

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

	// Explicit attribution on the entry wins for the legacy user_id column
	// (some callers pass the actor explicitly, e.g. pricing exposure events);
	// actor_id always records what pkg/actor resolved for the request, so the
	// kind and the id on a row can never disagree. A machine key is never the
	// implicit source of user_id: that column meant "a user" until actor_kind
	// existed, and legacy reports grouping by it would list key ids among
	// users. The key id lives in actor_id only. The decision rests on the key
	// id being in the context, not on the actor's kind: an agent marker over
	// a keyed request rewrites the kind to agent while the principal is still
	// the key, and must not smuggle the key id into user_id either.
	userID := entry.UserID
	_, viaMachineKey := actor.KeyIDFromContext(ctx)
	if userID == "" && !viaMachineKey {
		userID = act.ID
	}
	// No attribution at all is stored as NULL, the value the 088 backfill
	// writes for the same rows, rather than an empty string.
	var userIDVal any
	if userID != "" {
		userIDVal = userID
	}
	var actorID any
	if act.ID != "" {
		actorID = act.ID
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

	// Cancellation discipline, per the review's P2: inside a transaction the
	// row must live and die with that transaction's context; the transaction
	// is the mutation's, and a cancelled request cancels the mutation too.
	// With no transaction in ctx the mutation has already committed, so the
	// audit row must survive a client that disconnected right after — the
	// values ride along, but the cancellation does not, and a short timeout
	// bounds the write on its own.
	execCtx := ctx
	if !database.InTx(ctx) {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}

	_, err := l.db.GetExecutor(ctx).Exec(execCtx,
		`INSERT INTO audit_log (action, entity_type, entity_id, user_id, changes, request_id,
		                        actor_kind, actor_id, acting_as, tool)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		entry.Action, entry.EntityType, entry.EntityID, userIDVal, changesJSON, requestID,
		act.Kind, actorID, actingAs, tool,
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

// The refused path is caller controlled: stored verbatim, one refused
// request writes an attacker sized audit_log row, and even a fully scopeless
// key converts cheap requests into disk exhaustion. The row therefore stores
// bounded copies, cut on a rune boundary and marked as truncated; the full
// path is served only to the server log line, which the auth layer writes
// for every refusal.
const (
	maxRefusalPathBytes  = 512
	maxRefusalScopeBytes = 128
)

// cutRunes cuts s to at most limit bytes without splitting a UTF-8 rune,
// reporting whether it cut anything.
func cutRunes(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// AuditKeyRefusal records a refused machine-key request (a valid key refused
// for lacking a scope, for a user-only route, or for a path machine keys do
// not address). It implements the middleware package's KeyRefusalAuditor
// seam. The row's actor is the key: the ctx the auth core passes carries the
// key id, so actor_id is the key's id and user_id stays NULL (a key is never
// a user, even when agent headers rewrite actor_kind to agent). The stored
// path and scope are bounded (see maxRefusalPathBytes); a refusal answers
// before this write, so a bounded row carries everything the trail needs. A
// failure to write is logged and swallowed: the refusal verdict has already
// been served, and a full audit table must not turn a 403 into a 500.
func (l *Logger) AuditKeyRefusal(ctx context.Context, keyID, action, scope, method, path string) {
	id, err := uuid.Parse(keyID)
	if err != nil {
		// The id comes from the api_keys row the validator read; a
		// non-uuid here is a wiring fault worth a loud log line.
		slog.Error("audit: machine key refusal with non-uuid key id", "key_id", keyID, "action", action)
		id = uuid.Nil
	}
	storedPath, pathTruncated := cutRunes(path, maxRefusalPathBytes)
	storedScope, scopeTruncated := cutRunes(scope, maxRefusalScopeBytes)
	changes := map[string]interface{}{"method": method, "path": storedPath}
	if pathTruncated {
		changes["path_truncated"] = true
	}
	if scopeTruncated {
		changes["scope_truncated"] = true
	}
	if storedScope != "" {
		changes["scope"] = storedScope
	}
	if err := l.Log(ctx, Entry{
		Action:     action,
		EntityType: "api_key",
		EntityID:   id,
		Changes:    changes,
	}); err != nil {
		slog.Error("audit: failed to write machine key refusal", "action", action, "key_id", keyID, "error", err)
	}
}

// AuditKeyBranchRefusal records a branch bound key's refused X-Branch-Id
// (ADR 0007 section 5.5): the row's entity is the key, its changes name the
// key's own branch and the bounded method and path. It implements the
// middleware package's BranchRefusalAuditor seam. A failure to write is
// logged and swallowed: the refusal verdict has already been served.
func (l *Logger) AuditKeyBranchRefusal(ctx context.Context, keyID string, branch uuid.UUID, method, path string) {
	id, err := uuid.Parse(keyID)
	if err != nil {
		slog.Error("audit: branch refusal with non-uuid key id", "key_id", keyID)
		id = uuid.Nil
	}
	storedPath, truncated := cutRunes(path, maxRefusalPathBytes)
	changes := map[string]interface{}{
		"branch_id": branch.String(),
		"method":    method,
		"path":      storedPath,
	}
	if truncated {
		changes["path_truncated"] = true
	}
	if err := l.Log(ctx, Entry{
		Action:     "key.branch_refused",
		EntityType: "api_key",
		EntityID:   id,
		Changes:    changes,
	}); err != nil {
		slog.Error("audit: failed to write branch refusal", "key_id", keyID, "error", err)
	}
}

// Drain is retained for graceful-shutdown callers: writes are synchronous
// now, so there is never anything in flight to wait for.
func (l *Logger) Drain() {}
