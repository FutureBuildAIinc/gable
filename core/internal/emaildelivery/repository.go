// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository interface {
	Insert(ctx context.Context, e Entry) (*Delivery, error)
	SetOutcome(ctx context.Context, id uuid.UUID, status, senderAnswer, sendError string) error
	List(ctx context.Context, f Filter, afterTime *time.Time, afterID *uuid.UUID, limit int) ([]Delivery, error)
	Count(ctx context.Context, f Filter) (int64, error)
}

type PostgresRepository struct{ db *database.DB }

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

const columns = `id, created_at, branch_id, recipient, subject, document_type, document_id,
	document_number, status, sender_answer, error, sent_by`

func scan(row pgx.Row) (*Delivery, error) {
	var d Delivery
	if err := row.Scan(&d.ID, &d.CreatedAt.Time, &d.BranchID, &d.Recipient, &d.Subject, &d.DocumentType, &d.DocumentID,
		&d.DocumentNumber, &d.Status, &d.SenderAnswer, &d.Error, &d.SentBy); err != nil {
		return nil, err
	}
	return &d, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *PostgresRepository) Insert(ctx context.Context, e Entry) (*Delivery, error) {
	var sentBy *string
	if id := actor.FromContext(ctx).ID; id != "" {
		sentBy = &id
	}
	d, err := scan(r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO email_deliveries (branch_id, recipient, subject, document_type, document_id,
		                              document_number, status, sender_answer, error, sent_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+columns,
		e.BranchID, e.Recipient, e.Subject, e.DocumentType, e.DocumentID,
		nullable(e.DocumentNumber), e.Status, nullable(e.SenderAnswer), nullable(e.Error), sentBy))
	if err != nil {
		return nil, fmt.Errorf("record email delivery: %w", err)
	}
	return d, nil
}

// SetOutcome resolves one queued row with the send's outcome: sent or logged
// with the sender's own answer, or failed with the error. A row no send
// resolves (the process died between the 202 and the send) stays queued, which
// is the truth of it.
func (r *PostgresRepository) SetOutcome(ctx context.Context, id uuid.UUID, status, senderAnswer, sendError string) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE email_deliveries SET status = $2, sender_answer = $3, error = $4 WHERE id = $1`,
		id, status, nullable(senderAnswer), nullable(sendError)); err != nil {
		return fmt.Errorf("set email delivery outcome: %w", err)
	}
	return nil
}

// The branch wall, as the other lists apply it: a context branch holds the
// list to its rows; with none, a bound user's list covers the branches
// granted to them; with neither, every branch. A row of no branch is visible
// only when no wall applies.
const filters = `
	(($1::uuid IS NOT NULL AND branch_id = $1)
	 OR ($1::uuid IS NULL AND $2::text IS NOT NULL AND branch_id IN
	     (SELECT branch_id FROM user_locations WHERE user_sub = $2))
	 OR ($1::uuid IS NULL AND $2::text IS NULL))
	AND ($3 = '' OR document_type = $3)
	AND ($4 = '' OR status = $4)
	AND ($5::uuid IS NULL OR document_id = $5)`

func wallArgs(ctx context.Context, f Filter) []any {
	return []any{middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), f.DocumentType, f.Status, f.DocumentID}
}

func (r *PostgresRepository) List(ctx context.Context, f Filter, afterTime *time.Time, afterID *uuid.UUID, limit int) ([]Delivery, error) {
	args := append(wallArgs(ctx, f), afterTime, afterID, limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+columns+` FROM email_deliveries
		 WHERE `+filters+`
		   AND ($6::timestamptz IS NULL OR (created_at, id) < ($6, $7::uuid))
		 ORDER BY created_at DESC, id DESC
		 LIMIT $8`, args...)
	if err != nil {
		return nil, fmt.Errorf("list email deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) Count(ctx context.Context, f Filter) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM email_deliveries WHERE`+filters, wallArgs(ctx, f)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count email deliveries: %w", err)
	}
	return n, nil
}
