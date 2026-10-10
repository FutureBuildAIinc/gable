// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ListFilter is the list's filters beside the platform's cursor and limit.
// Every filter filters; anything else is refused by the strict query guard.
type ListFilter struct {
	Limit     int
	AfterTime *time.Time
	AfterID   uuid.UUID
	Type      *ActivityType
	ContactID *uuid.UUID
}

// Repository is the activity store. Every statement goes through the
// context's executor, so a write inside a caller's transaction joins it, and
// every read and lock carries the branch wall through the activity's
// customer (the same customer_branches membership the customer module's own
// reads carry; a second branch's request for the first branch's activity is
// a 404 on every route).
type Repository interface {
	List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Activity, bool, *int64, error)
	Get(ctx context.Context, id uuid.UUID) (*Activity, error)
	Create(ctx context.Context, a *Activity) error
	Lock(ctx context.Context, id uuid.UUID) error
	Update(ctx context.Context, a *Activity) error
	Delete(ctx context.Context, id uuid.UUID) error
	// CustomerVisible reports whether the customer exists behind the caller's
	// branch wall; a create naming an invisible customer is a 404, the same
	// answer reading that customer gives.
	CustomerVisible(ctx context.Context, customerID uuid.UUID) (bool, error)
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const columns = `a.id, a.customer_id, a.contact_id, a.activity_type, a.description,
	a.logged_by, a.activity_date, a.revision, a.created_at, a.updated_at`

// wall is the branch wall through the activity's customer: a caller held to
// a branch sees only activities of customers with that branch.
func wall(arg int) string {
	return fmt.Sprintf(`($%d::uuid IS NULL OR EXISTS (SELECT 1 FROM customers c
		JOIN customer_branches cb ON cb.customer_id = c.id
		WHERE c.id = a.customer_id AND cb.branch_id = $%d))`, arg, arg)
}

func scanOne(row pgx.Row) (*Activity, error) {
	var (
		a                      Activity
		created, updated, when time.Time
	)
	err := row.Scan(&a.ID, &a.CustomerID, &a.ContactID, &a.ActivityType, &a.Description,
		&a.LoggedBy, &when, &a.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	a.ActivityDate, a.CreatedAt, a.UpdatedAt = httpx.TimestampOf(when), httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &a, nil
}

// filterConds builds the WHERE conditions shared by the list and its count:
// the customer, the branch wall and the route's filters. The cursor
// predicate is added only to the list, never the count, which is of the
// whole filtered set.
func filterConds(ctx context.Context, customerID uuid.UUID, f ListFilter, args *[]any) []string {
	*args = append(*args, customerID, branchctx.IDForQuery(ctx))
	conds := []string{`a.customer_id = $1`, wall(2)}
	if f.Type != nil {
		*args = append(*args, string(*f.Type))
		conds = append(conds, fmt.Sprintf(`a.activity_type = $%d`, len(*args)))
	}
	if f.ContactID != nil {
		*args = append(*args, *f.ContactID)
		conds = append(conds, fmt.Sprintf(`a.contact_id = $%d`, len(*args)))
	}
	return conds
}

func (r *PostgresRepository) List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Activity, bool, *int64, error) {
	args := []any{}
	conds := filterConds(ctx, customerID, f, &args)
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(a.created_at, a.id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+columns+` FROM crm_activities a WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY a.created_at DESC, a.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list activities: %w", err)
	}
	defer rows.Close()
	items := []Activity{}
	for rows.Next() {
		a, err := scanOne(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan activity: %w", err)
		}
		items = append(items, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil, err
	}
	hasMore := false
	if len(items) > f.Limit {
		items = items[:f.Limit]
		hasMore = true
	}
	var total *int64
	if wantTotal {
		args := []any{}
		conds := filterConds(ctx, customerID, f, &args)
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM crm_activities a WHERE `+strings.Join(conds, " AND "), args...).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count activities: %w", err)
		}
		total = &n
	}
	return items, hasMore, total, nil
}

func (r *PostgresRepository) Get(ctx context.Context, id uuid.UUID) (*Activity, error) {
	a, err := scanOne(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+columns+` FROM crm_activities a WHERE a.id = $1 AND `+wall(2),
		id, branchctx.IDForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get activity: %w", err)
	}
	return a, nil
}

// referenceFields maps the foreign keys a write can violate to the request
// field that named the missing record.
var referenceFields = map[string]string{
	"crm_activities_customer_id_fkey": "customer_id",
	"crm_activities_contact_id_fkey":  "contact_id",
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		if field, ok := referenceFields[pgErr.ConstraintName]; ok {
			return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a referenced record does not exist",
				Details: []httpx.FieldError{{Field: field, Message: "no such record"}}}
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (r *PostgresRepository) Create(ctx context.Context, a *Activity) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO crm_activities (id, customer_id, contact_id, activity_type, description, logged_by, activity_date, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())`,
		a.ID, a.CustomerID, a.ContactID, a.ActivityType, a.Description, a.LoggedBy, a.ActivityDate.Time)
	if err != nil {
		return mapWriteError(err, "failed to create activity")
	}
	return nil
}

// Lock takes the row FOR UPDATE; revision checks happen after it, inside the
// caller's transaction. A joined select cannot lock an outer joined row, so
// lock first, read second.
func (r *PostgresRepository) Lock(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT a.id FROM crm_activities a WHERE a.id = $1 AND `+wall(2)+` FOR UPDATE OF a`,
		id, branchctx.IDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock activity: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Update(ctx context.Context, a *Activity) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE crm_activities a
		SET contact_id = $2, activity_type = $3, description = $4, logged_by = $5,
			activity_date = $6, revision = revision + 1, updated_at = NOW()
		WHERE a.id = $1`,
		a.ID, a.ContactID, a.ActivityType, a.Description, a.LoggedBy, a.ActivityDate.Time)
	if err != nil {
		return mapWriteError(err, "failed to update activity")
	}
	return nil
}

func (r *PostgresRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM crm_activities WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete activity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) CustomerVisible(ctx context.Context, customerID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM customers c WHERE c.id = $1 AND ($2::uuid IS NULL OR EXISTS (
			SELECT 1 FROM customer_branches cb WHERE cb.customer_id = c.id AND cb.branch_id = $2)))`,
		customerID, branchctx.IDForQuery(ctx)).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("failed to check the customer: %w", err)
	}
	return ok, nil
}
