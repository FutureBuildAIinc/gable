// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package outbox implements the transactional outbox (ADR 0003, item
// R1-12): a mutation writes its domain events into the events_outbox table
// inside the mutation's own transaction, and a drain runner republishes the
// committed rows to the in-process event bus so delivery no longer depends
// on what one process kept in memory.
//
// The ordering rule is the package's core property: positions are drawn from
// the sequence under a transaction scoped advisory lock, so position order
// is commit order among event writers, and every reader (the drain, the
// events API) that pages with `position > cursor` can never skip an event
// that commits after it paged. ADR 0003 records why a reader side snapshot
// horizon alone cannot give that guarantee. Nothing in this package deletes
// rows; the outbox is a replay window, not a ledger.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// AdvisoryLockKey is the advisory lock every event writer takes (transaction
// scoped) before its row draws a position from the sequence: 0x6F757462,
// the ASCII bytes 'outb'. Holding it to commit is what makes position order
// equal commit order; see the package doc and ADR 0003 section 2.
const AdvisoryLockKey = 0x6F757462

// DefaultOrg is the org slug stamped on events when no deployment level org
// identity is configured. Gable is one database per dealer, so the org is a
// property of the deployment; the column is NOT NULL so the wire envelope
// always carries one.
const DefaultOrg = "default"

// Event is one domain event to be committed with a mutation. Type is the
// dot-delimited event type and doubles as the in-process bus subject; Data
// is the small summary payload; EntityType and EntityID name the entity the
// event is about (the envelope's entity {kind, id}).
type Event struct {
	ID         uuid.UUID
	Type       string
	Org        string
	BranchID   *uuid.UUID
	EntityType string
	EntityID   uuid.UUID
	Data       json.RawMessage
	At         time.Time
}

// Row is a committed event as read back, with its position for cursor
// pagination.
type Row struct {
	Position int64
	Event    Event
}

// Writer records events into events_outbox through the database seam.
type Writer struct {
	db  *database.DB
	org string
}

// NewWriter builds a Writer stamping the deployment's org slug on events
// that do not carry their own. An empty org falls back to DefaultOrg.
func NewWriter(db *database.DB, org string) *Writer {
	if org == "" {
		org = DefaultOrg
	}
	return &Writer{db: db, org: org}
}

// Write records one event. Inside a transaction (ctx carrying one, resolved
// by database.GetExecutor) the row joins that transaction and commits or
// rolls back with the mutation it describes; outside one the write wraps
// itself in its own short transaction, so the advisory lock is always
// transaction scoped and never outlives the write on a pooled connection.
// Callers inside a transaction propagate the error: a mutation whose event
// cannot be recorded does not silently pretend it happened.
func (w *Writer) Write(ctx context.Context, ev Event) error {
	if ev.Type == "" {
		return errors.New("outbox: event type is required")
	}
	if !ValidType(ev.Type) {
		return fmt.Errorf("outbox: event type %q is not a dot-delimited lowercase type", ev.Type)
	}
	if ev.EntityType == "" {
		return errors.New("outbox: entity type is required")
	}
	if ev.EntityID == uuid.Nil {
		return errors.New("outbox: entity id is required")
	}
	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}
	if ev.Org == "" {
		ev.Org = w.org
	}
	// The branch scope rides along from the request when the caller did not
	// pin one; a NULL branch_id keeps its branchctx meaning, not pinned to a
	// branch.
	if ev.BranchID == nil {
		ev.BranchID = branchctx.IDForQuery(ctx)
	}
	if len(ev.Data) == 0 {
		ev.Data = json.RawMessage(`{}`)
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}

	if database.InTx(ctx) {
		return w.insert(ctx, ev)
	}
	return w.db.RunInTx(ctx, func(txCtx context.Context) error {
		return w.insert(txCtx, ev)
	})
}

// insert takes the advisory lock and inserts the row through the caller's
// executor. The lock must be taken before the position is drawn (that is,
// before the INSERT runs its DEFAULT nextval), and it must be transaction
// scoped: both hold here whether the caller supplied the transaction or
// Write opened one for the write.
func (w *Writer) insert(ctx context.Context, ev Event) error {
	ex := w.db.GetExecutor(ctx)
	if _, err := ex.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, AdvisoryLockKey); err != nil {
		return fmt.Errorf("outbox: acquire event writer lock: %w", err)
	}
	_, err := ex.Exec(ctx,
		`INSERT INTO events_outbox (event_id, type, org, branch_id, entity_type, entity_id, data, at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ev.ID, ev.Type, ev.Org, ev.BranchID, ev.EntityType, ev.EntityID, ev.Data, ev.At,
	)
	if err != nil {
		return fmt.Errorf("outbox: insert event: %w", err)
	}
	return nil
}

// ValidType accepts the dot-delimited lowercase event types the product
// uses (quote.exposure.flagged, order.confirmed): one or more tokens of
// ASCII lowercase letters, digits, underscore or hyphen, each one to 64
// bytes, the whole name bounded. The type doubles as a bus subject, so a
// value outside this shape could not be routed anyway. The events read API
// validates its types filter with the same rule, so the feed accepts
// exactly the vocabulary the writer records.
func ValidType(t string) bool {
	if t == "" || len(t) > 200 {
		return false
	}
	start := 0
	for i := 0; i <= len(t); i++ {
		if i == len(t) || t[i] == '.' {
			token := t[start:i]
			if len(token) == 0 || len(token) > 64 {
				return false
			}
			start = i + 1
		}
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == '.' || c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') {
			continue
		}
		return false
	}
	return true
}

// ListEvents reads up to limit rows with position greater than after, in
// position order, through the caller's executor. A nil or empty types list
// reads every type; otherwise only rows whose type is listed. It is a
// single statement through the pool outside a transaction; the drain, which
// reads inside its cursor transaction, uses the same function with the
// transaction's executor.
func ListEvents(ctx context.Context, ex database.Executor, after int64, types []string, limit int) ([]Row, error) {
	query := `SELECT position, event_id, type, org, branch_id, entity_type, entity_id, data, at
	          FROM events_outbox WHERE position > $1`
	args := []any{after}
	if len(types) > 0 {
		query += ` AND type = ANY($2) ORDER BY position LIMIT $3`
		args = append(args, types, limit)
	} else {
		query += ` ORDER BY position LIMIT $2`
		args = append(args, limit)
	}

	rows, err := ex.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("outbox: list events: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Position, &r.Event.ID, &r.Event.Type, &r.Event.Org, &r.Event.BranchID,
			&r.Event.EntityType, &r.Event.EntityID, &r.Event.Data, &r.Event.At); err != nil {
			return nil, fmt.Errorf("outbox: scan event: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: list events: %w", err)
	}
	return out, nil
}

// CountEvents counts the rows matching the given types (every type when
// none are given), the count the events feed serves under ?include=total.
func CountEvents(ctx context.Context, ex database.Executor, types []string) (int64, error) {
	query := `SELECT count(*) FROM events_outbox`
	args := []any{}
	if len(types) > 0 {
		query += ` WHERE type = ANY($1)`
		args = append(args, types)
	}
	var n int64
	if err := ex.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("outbox: count events: %w", err)
	}
	return n, nil
}
