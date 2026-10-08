// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is the repository's sentinel for an RFC the caller named that
// does not exist.
var ErrNotFound = errors.New("rfc not found")

// ListFilter is the RFC list's query: the keyset page, the status filter and
// the opt in total.
type ListFilter struct {
	Limit    int
	Statuses []RFCStatus
	AfterAt  *time.Time
	AfterID  uuid.UUID
}

// Repository is the store the service reads and writes through. Every
// statement goes through GetExecutor: inside a transaction it is the
// transaction, never the pool beside it.
type Repository interface {
	CreateRFC(ctx context.Context, rfc *RFC) error
	LockRFC(ctx context.Context, id uuid.UUID) error
	GetRFC(ctx context.Context, id uuid.UUID) (*RFC, error)
	ListRFCs(ctx context.Context, f ListFilter) ([]RFCSummary, error)
	CountRFCs(ctx context.Context, f ListFilter) (int64, error)
	UpdateRFC(ctx context.Context, rfc *RFC) error
	SetStatus(ctx context.Context, id uuid.UUID, status RFCStatus) error
	NextNumber(ctx context.Context) (string, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// listPredicate is the shared WHERE of the list and its count.
func (f ListFilter) listPredicate() (string, []any) {
	conds := []string{}
	var args []any
	if len(f.Statuses) > 0 {
		placeholders := make([]string, 0, len(f.Statuses))
		for _, st := range f.Statuses {
			args = append(args, string(st))
			placeholders = append(placeholders, "$"+strconv.Itoa(len(args)))
		}
		conds = append(conds, "status IN ("+strings.Join(placeholders, ", ")+")")
	}
	if f.AfterAt != nil {
		args = append(args, *f.AfterAt, f.AfterID)
		conds = append(conds, "(created_at, id) < ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const summaryCols = `id, number, title, status, author_id, revision, created_at, updated_at`

func scanSummary(scan func(dest ...any) error) (*RFCSummary, error) {
	var s RFCSummary
	var created, updated time.Time
	if err := scan(&s.ID, &s.Number, &s.Title, &s.Status, &s.AuthorID, &s.Revision, &created, &updated); err != nil {
		return nil, err
	}
	s.CreatedAt = httpx.TimestampOf(created)
	s.UpdatedAt = httpx.TimestampOf(updated)
	return &s, nil
}

func (r *PostgresRepository) ListRFCs(ctx context.Context, f ListFilter) ([]RFCSummary, error) {
	pred, args := f.listPredicate()
	q := `SELECT ` + summaryCols + ` FROM rfcs` + pred +
		` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RFCSummary{}
	for rows.Next() {
		s, err := scanSummary(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountRFCs(ctx context.Context, f ListFilter) (int64, error) {
	pred, args := f.listPredicate()
	q := `SELECT count(*) FROM rfcs` + pred
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func (r *PostgresRepository) GetRFC(ctx context.Context, id uuid.UUID) (*RFC, error) {
	const q = `SELECT id, number, title, status, author_id, revision, created_at, updated_at,
		COALESCE(problem_statement, ''), COALESCE(proposed_solution, ''), content
		FROM rfcs WHERE id = $1`
	var out RFC
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, id).Scan(
		&out.ID, &out.Number, &out.Title, &out.Status, &out.AuthorID, &out.Revision, &created, &updated,
		&out.ProblemStatement, &out.ProposedSolution, &out.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out.CreatedAt = httpx.TimestampOf(created)
	out.UpdatedAt = httpx.TimestampOf(updated)
	return &out, nil
}

func (r *PostgresRepository) LockRFC(ctx context.Context, id uuid.UUID) error {
	var one int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT 1 FROM rfcs WHERE id = $1 FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *PostgresRepository) CreateRFC(ctx context.Context, rfc *RFC) error {
	const q = `INSERT INTO rfcs (title, status, problem_statement, proposed_solution, content, author_id, number, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, NOW(), NOW())
		RETURNING id, revision, created_at, updated_at`
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q,
		rfc.Title, string(rfc.Status), rfc.ProblemStatement, rfc.ProposedSolution, rfc.Content,
		rfc.AuthorID, rfc.Number).Scan(&rfc.ID, &rfc.Revision, &created, &updated)
	if err != nil {
		return err
	}
	rfc.CreatedAt = httpx.TimestampOf(created)
	rfc.UpdatedAt = httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) UpdateRFC(ctx context.Context, rfc *RFC) error {
	sets := []string{}
	args := []any{}
	add := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if rfc.Title != "" {
		add("title", rfc.Title)
	}
	if rfc.ProblemStatement != "" {
		add("problem_statement", rfc.ProblemStatement)
	}
	if rfc.ProposedSolution != "" {
		add("proposed_solution", rfc.ProposedSolution)
	}
	if rfc.Content != nil {
		add("content", *rfc.Content)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "revision = revision + 1", "updated_at = NOW()")
	args = append(args, rfc.ID)
	q := `UPDATE rfcs SET ` + strings.Join(sets, ", ") + ` WHERE id = $` + strconv.Itoa(len(args))
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) SetStatus(ctx context.Context, id uuid.UUID, status RFCStatus) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE rfcs SET status = $2, revision = revision + 1, updated_at = NOW() WHERE id = $1`, id, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// NextNumber mints the next RFC document number through the caller's
// transaction (ADR 0001 section 8).
func (r *PostgresRepository) NextNumber(ctx context.Context) (string, error) {
	return httpx.NextDocumentNumber(ctx, r.db.GetExecutor(ctx), "rfc_number_seq", "RFC", httpx.DefaultDocNumberWidth)
}
