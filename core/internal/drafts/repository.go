// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PostgresRepository is the drafts store. Every statement goes through
// GetExecutor: inside a transaction that is the transaction, never the pool
// beside it.
type PostgresRepository struct {
	db *database.DB
}

// NewRepository builds the store.
func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// nullStr renders an empty string as SQL NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// strOf reads a nullable actor id column as a string.
func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// InsertDraft writes the new row (revision 1, status OPEN, the creating
// actor's quadruple) through the caller's executor.
func (r *PostgresRepository) InsertDraft(ctx context.Context, d *Draft) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO drafts (id, module, branch_id, subject_id, subject_revision, payload,
		                     created_by_kind, created_by_id, created_acting_as, created_tool,
		                     updated_by_kind, updated_by_id, updated_acting_as, updated_tool)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$7,$8,$9,$10)`,
		d.ID, d.Module, d.BranchID, d.SubjectID, d.SubjectRevision, d.Payload,
		d.CreatedBy.Kind, nullStr(d.CreatedBy.ID), nullStr(d.CreatedBy.ActingAs), nullStr(d.CreatedBy.Tool))
	if err != nil {
		return fmt.Errorf("insert draft: %w", err)
	}
	return nil
}

const draftColumns = `id, module, branch_id, subject_id, subject_revision, status, revision, payload,
	created_by_kind, created_by_id, created_acting_as, created_tool,
	updated_by_kind, updated_by_id, updated_acting_as, updated_tool,
	promoted_entity_id, promoted_number, promoted_at,
	promoted_by_kind, promoted_by_id, promoted_acting_as, promoted_tool,
	discarded_at, discarded_by_kind, discarded_by_id, discarded_acting_as, discarded_tool,
	created_at, updated_at`

func scanDraft(scan func(dest ...any) error) (*Draft, error) {
	var d Draft
	var subjectID, promotedEntity *uuid.UUID
	var subjectRevision *int64
	var promotedNumber *string
	var promotedAt, discardedAt *time.Time
	var createdAt, updatedAt time.Time
	var promotedByKind, discardedByKind *string
	var cbID, cbAct, cbTool, ubID, ubAct, ubTool *string
	var pbID, pbAct, pbTool, dbID, dbAct, dbTool *string
	if err := scan(&d.ID, &d.Module, &d.BranchID, &subjectID, &subjectRevision, &d.Status, &d.Revision, &d.Payload,
		&d.CreatedBy.Kind, &cbID, &cbAct, &cbTool,
		&d.UpdatedBy.Kind, &ubID, &ubAct, &ubTool,
		&promotedEntity, &promotedNumber, &promotedAt,
		&promotedByKind, &pbID, &pbAct, &pbTool,
		&discardedAt, &discardedByKind, &dbID, &dbAct, &dbTool,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}
	d.SubjectID, d.SubjectRevision = subjectID, subjectRevision
	d.CreatedBy.ID, d.CreatedBy.ActingAs, d.CreatedBy.Tool = strOf(cbID), strOf(cbAct), strOf(cbTool)
	d.UpdatedBy.ID, d.UpdatedBy.ActingAs, d.UpdatedBy.Tool = strOf(ubID), strOf(ubAct), strOf(ubTool)
	d.PromotedEntity, d.PromotedNumber = promotedEntity, promotedNumber
	if promotedAt != nil {
		ts := timestampOf(*promotedAt)
		d.PromotedAt = &ts
		d.PromotedBy = &Actor{}
		if promotedByKind != nil {
			d.PromotedBy.Kind = *promotedByKind
			d.PromotedBy.ID, d.PromotedBy.ActingAs, d.PromotedBy.Tool = strOf(pbID), strOf(pbAct), strOf(pbTool)
		}
	}
	if discardedAt != nil {
		ts := timestampOf(*discardedAt)
		d.DiscardedAt = &ts
		d.DiscardedBy = &Actor{}
		if discardedByKind != nil {
			d.DiscardedBy.Kind = *discardedByKind
			d.DiscardedBy.ID, d.DiscardedBy.ActingAs, d.DiscardedBy.Tool = strOf(dbID), strOf(dbAct), strOf(dbTool)
		}
	}
	d.CreatedAt, d.UpdatedAt = timestampOf(createdAt), timestampOf(updatedAt)
	return &d, nil
}

// GetDraft reads one draft, optionally held to the kind's module.
func (r *PostgresRepository) GetDraft(ctx context.Context, id uuid.UUID, module string) (*Draft, error) {
	d, err := scanDraft(func(dest ...any) error {
		return r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT `+draftColumns+` FROM drafts WHERE id = $1 AND ($2 = '' OR module = $2)`, id, module).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get draft: %w", err)
	}
	return d, nil
}

// LockDraft reads one draft holding the row FOR UPDATE, inside the caller's
// transaction: the revision is checked after the lock, so the check and the
// write are one database act.
func (r *PostgresRepository) LockDraft(ctx context.Context, id uuid.UUID, module string) (*Draft, error) {
	d, err := scanDraft(func(dest ...any) error {
		return r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT `+draftColumns+` FROM drafts WHERE id = $1 AND ($2 = '' OR module = $2) FOR UPDATE`, id, module).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock draft: %w", err)
	}
	return d, nil
}

// ReplacePayload writes a PUT: the new payload, the moved revision, the
// writer's quadruple and, on an edit draft, the rebased subject revision.
func (r *PostgresRepository) ReplacePayload(ctx context.Context, id uuid.UUID, payload []byte, revision int64, subjectRevision *int64, by Actor) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drafts SET payload = $3, revision = $4, subject_revision = $5,
			updated_by_kind = $6, updated_by_id = $7, updated_acting_as = $8, updated_tool = $9,
			updated_at = NOW()
		 WHERE id = $1 AND revision = $2`,
		id, revision-1, payload, revision, subjectRevision,
		by.Kind, nullStr(by.ID), nullStr(by.ActingAs), nullStr(by.Tool))
	if err != nil {
		return fmt.Errorf("replace draft payload: %w", err)
	}
	return nil
}

// DiscardDraft moves open to discarded, holding the writer's quadruple.
func (r *PostgresRepository) DiscardDraft(ctx context.Context, id uuid.UUID, revision int64, at time.Time, by Actor) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drafts SET status = 'DISCARDED', revision = $3, discarded_at = $4,
			discarded_by_kind = $5, discarded_by_id = $6, discarded_acting_as = $7, discarded_tool = $8,
			updated_by_kind = $5, updated_by_id = $6, updated_acting_as = $7, updated_tool = $8,
			updated_at = NOW()
		 WHERE id = $1 AND revision = $2`,
		id, revision-1, revision, at,
		by.Kind, nullStr(by.ID), nullStr(by.ActingAs), nullStr(by.Tool))
	if err != nil {
		return fmt.Errorf("discard draft: %w", err)
	}
	return nil
}

// ReopenDraft moves discarded back to open, clearing the discard block.
func (r *PostgresRepository) ReopenDraft(ctx context.Context, id uuid.UUID, revision int64, by Actor) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drafts SET status = 'OPEN', revision = $3,
			discarded_at = NULL, discarded_by_kind = NULL, discarded_by_id = NULL,
			discarded_acting_as = NULL, discarded_tool = NULL,
			updated_by_kind = $4, updated_by_id = $5, updated_acting_as = $6, updated_tool = $7,
			updated_at = NOW()
		 WHERE id = $1 AND revision = $2`,
		id, revision-1, revision,
		by.Kind, nullStr(by.ID), nullStr(by.ActingAs), nullStr(by.Tool))
	if err != nil {
		return fmt.Errorf("reopen draft: %w", err)
	}
	return nil
}

// MarkPromoted writes the promotion: status, the moved revision and the
// promoted block with the committer's quadruple.
func (r *PostgresRepository) MarkPromoted(ctx context.Context, id uuid.UUID, revision int64, entity uuid.UUID, number *string, at time.Time, by Actor) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drafts SET status = 'PROMOTED', revision = $3,
			promoted_entity_id = $4, promoted_number = $5, promoted_at = $6,
			promoted_by_kind = $7, promoted_by_id = $8, promoted_acting_as = $9, promoted_tool = $10,
			updated_by_kind = $7, updated_by_id = $8, updated_acting_as = $9, updated_tool = $10,
			updated_at = NOW()
		 WHERE id = $1 AND revision = $2`,
		id, revision-1, revision, entity, number, at,
		by.Kind, nullStr(by.ID), nullStr(by.ActingAs), nullStr(by.Tool))
	if err != nil {
		return fmt.Errorf("mark draft promoted: %w", err)
	}
	return nil
}

// ListFilter is the drafts list's query: the keyset page, the module, the
// status, subject and creator filters, and the branch wall's two arms (a
// context branch, or a grants sub with no context branch).
type ListFilter struct {
	Module        string
	Statuses      []Status
	SubjectID     *uuid.UUID
	CreatedByKind string
	AfterTime     *time.Time
	AfterID       uuid.UUID
	Limit         int
	// BranchID, GrantsSub: the branch wall. BranchID scopes to one branch;
	// with nil, GrantsSub scopes to the granted branches when set, and both
	// nil reads every branch (the administrator, the unbound key, the
	// single-branch switch, dev mode).
	BranchID  *uuid.UUID
	GrantsSub *string
}

// listPredicate builds the shared WHERE of the list and its count.
func (f ListFilter) predicate() (string, []any) {
	pred := "module = $1"
	args := []any{f.Module}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		pred += ` AND status = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	if f.SubjectID != nil {
		args = append(args, *f.SubjectID)
		pred += ` AND subject_id = $` + strconv.Itoa(len(args))
	}
	if f.CreatedByKind != "" {
		args = append(args, f.CreatedByKind)
		pred += ` AND created_by_kind = $` + strconv.Itoa(len(args))
	}
	// The three arm branch wall: a context branch sees its own drafts; with
	// no context branch a bound non-admin user sees the granted branches,
	// none granted listing none; both nil reads every branch.
	switch {
	case f.BranchID != nil:
		args = append(args, *f.BranchID)
		pred += ` AND branch_id = $` + strconv.Itoa(len(args))
	case f.GrantsSub != nil:
		args = append(args, *f.GrantsSub)
		pred += ` AND branch_id IN (SELECT branch_id FROM user_locations WHERE user_sub = $` + strconv.Itoa(len(args)) + `)`
	}
	return pred, args
}

// ListDrafts reads one keyset page, newest first on (created_at, id): the
// list orders on created_at, never updated_at, because a keyset on a column
// that moves under concurrent writes would repeat or skip rows.
func (r *PostgresRepository) ListDrafts(ctx context.Context, f ListFilter) ([]Draft, error) {
	pred, args := f.predicate()
	q := `SELECT ` + draftColumns + ` FROM drafts WHERE ` + pred
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		q += ` AND (created_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, f.Limit)
	q += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		d, err := scanDraft(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan draft: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// CountDrafts counts the rows matching the filter, the count the list
// serves under include=total.
func (r *PostgresRepository) CountDrafts(ctx context.Context, f ListFilter) (int64, error) {
	pred, args := f.predicate()
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM drafts WHERE `+pred, args...).Scan(&n)
	return n, err
}

// InsertEvent writes one draft_events row as the transaction's last draft
// statement. The BEFORE INSERT trigger takes the feed's own advisory lock
// and draws the commit ordered position (migration 103), the 089 mechanism
// with its own key.
func (r *PostgresRepository) InsertEvent(ctx context.Context, e *DraftEvent) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO draft_events (draft_id, module, branch_id, subject_id, op, revision, status,
		                           actor_kind, actor_id, acting_as, tool,
		                           promoted_entity_id, promoted_number)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.DraftID, e.Module, e.BranchID, e.SubjectID, e.Op, e.Revision, e.Status,
		e.Actor.Kind, nullStr(e.Actor.ID), nullStr(e.Actor.ActingAs), nullStr(e.Actor.Tool),
		e.PromotedEntity, e.PromotedNumber)
	if err != nil {
		return fmt.Errorf("insert draft event: %w", err)
	}
	return nil
}

// EventFilter is the feed reader's query: the module, the optional draft and
// subject filters, the branch wall's two arms and the batch bound.
type EventFilter struct {
	Module    string
	DraftID   *uuid.UUID
	SubjectID *uuid.UUID
	BranchID  *uuid.UUID
	GrantsSub *string
}

// FeedPage is one read of the feed: the rows past the position and the head
// position the same statement saw, one snapshot, so the connection's own
// position moves past rows its filters exclude without a second read.
type FeedPage struct {
	Rows []DraftEvent
	Head int64
}

// ReadEvents reads up to limit rows past after for the filter, in position
// order, with the head position in the same statement.
func (r *PostgresRepository) ReadEvents(ctx context.Context, f EventFilter, after int64, limit int) (FeedPage, error) {
	pred := "e.module = $1 AND e.position > $2"
	args := []any{f.Module, after}
	if f.DraftID != nil {
		args = append(args, *f.DraftID)
		pred += ` AND e.draft_id = $` + strconv.Itoa(len(args))
	}
	if f.SubjectID != nil {
		args = append(args, *f.SubjectID)
		pred += ` AND e.subject_id = $` + strconv.Itoa(len(args))
	}
	switch {
	case f.BranchID != nil:
		args = append(args, *f.BranchID)
		pred += ` AND e.branch_id = $` + strconv.Itoa(len(args))
	case f.GrantsSub != nil:
		args = append(args, *f.GrantsSub)
		pred += ` AND e.branch_id IN (SELECT branch_id FROM user_locations WHERE user_sub = $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, limit)
	// head LEFT JOIN rows: the head row always comes back, so an empty
	// page still carries the head position the connection's own cursor
	// moves to (the one snapshot rule, section 3.2); a plain cross join
	// would return nothing at all when no row matches the filter.
	q := `WITH rows AS (
	        SELECT e.position, e.draft_id, e.module, e.branch_id, e.subject_id, e.op, e.revision, e.status,
	               e.actor_kind, e.actor_id, e.acting_as, e.tool,
	               e.promoted_entity_id, e.promoted_number, e.at
	        FROM draft_events e
	        WHERE ` + pred + `
	        ORDER BY e.position
	        LIMIT $` + strconv.Itoa(len(args)) + `
	      ), head AS (SELECT COALESCE(max(position), $2) AS p FROM draft_events)
	      SELECT rows.position, rows.draft_id, rows.module, rows.branch_id, rows.subject_id, rows.op,
	             rows.revision, rows.status, rows.actor_kind, rows.actor_id, rows.acting_as, rows.tool,
	             rows.promoted_entity_id, rows.promoted_number, rows.at, head.p
	      FROM head LEFT JOIN rows ON TRUE
	      ORDER BY rows.position`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return FeedPage{}, fmt.Errorf("read draft events: %w", err)
	}
	defer rows.Close()
	page := FeedPage{Rows: []DraftEvent{}}
	for rows.Next() {
		// Every row column is nullable: the head-only row (no matching
		// event) carries NULLs beside the head position.
		var position *int64
		var module, op, status, kind *string
		var draftID, branchID uuid.UUID
		var subjectID, promotedEntity *uuid.UUID
		var revision *int64
		var aID, aAct, aTool, promotedNumber *string
		var at *time.Time
		if err := rows.Scan(&position, &draftID, &module, &branchID, &subjectID, &op, &revision, &status,
			&kind, &aID, &aAct, &aTool, &promotedEntity, &promotedNumber, &at, &page.Head); err != nil {
			return FeedPage{}, fmt.Errorf("scan draft event: %w", err)
		}
		if position == nil {
			continue
		}
		e := DraftEvent{
			Position: *position, DraftID: draftID, Module: *module, BranchID: branchID,
			SubjectID: subjectID, Op: *op, Revision: *revision, Status: Status(*status),
			PromotedEntity: promotedEntity, PromotedNumber: promotedNumber,
		}
		if kind != nil {
			e.Actor = Actor{Kind: *kind, ID: strOf(aID), ActingAs: strOf(aAct), Tool: strOf(aTool)}
		}
		if at != nil {
			e.At = timestampOf(*at)
		}
		page.Rows = append(page.Rows, e)
	}
	if err := rows.Err(); err != nil {
		return FeedPage{}, err
	}
	return page, nil
}

// PurgedThrough is the highest position the retention purge has deleted
// through: a client resuming from a cursor at or below it must re-read.
func (r *PostgresRepository) PurgedThrough(ctx context.Context) (int64, error) {
	var through int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT through_position FROM draft_events_purged WHERE id`).Scan(&through)
	return through, err
}

// PurgeOnce deletes one batch of change rows older than the cutoff, records
// the highest deleted position in draft_events_purged, and answers both. It
// is one statement, the ADR 0003 section 6 shape without subscriber
// cursors: this feed has no in process drain.
func (r *PostgresRepository) PurgeOnce(ctx context.Context, cutoff time.Time, batch int) (deleted, through int64, err error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = tx.QueryRow(ctx,
		`WITH victims AS (
	         DELETE FROM draft_events
	         WHERE position IN (
	           SELECT position FROM draft_events WHERE at < $1 ORDER BY position LIMIT $2
	         )
	         RETURNING position
	       )
	     SELECT count(*)::bigint, COALESCE(max(position), 0) FROM victims`, cutoff, batch).Scan(&deleted, &through); err != nil {
		return 0, 0, err
	}
	if deleted > 0 {
		if _, err = tx.Exec(ctx,
			`UPDATE draft_events_purged SET through_position = GREATEST(through_position, $1) WHERE id`, through); err != nil {
			return 0, 0, err
		}
	}
	err = tx.Commit(ctx)
	return deleted, through, err
}
