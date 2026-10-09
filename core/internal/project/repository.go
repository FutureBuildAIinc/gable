// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListFilter is the list's filters beside the platform's cursor and limit.
type ListFilter struct {
	Limit     int
	AfterTime *time.Time
	AfterID   uuid.UUID
	Status    *ProjectStatus
}

// Repository is the project store. The portal is the only surface: every
// statement is scoped to one customer, the caller the portal auth chain
// identifies, so a project of another customer's is not there at all. Every
// statement goes through the context's executor, so a write inside a
// caller's transaction joins it.
type Repository interface {
	List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Project, bool, *int64, error)
	Get(ctx context.Context, id, customerID uuid.UUID) (*Project, error)
	Create(ctx context.Context, p *Project) error
	Lock(ctx context.Context, id, customerID uuid.UUID) error
	Update(ctx context.Context, p *Project, status *string) error
	Entities(ctx context.Context, projectID, customerID uuid.UUID) ([]ProjectItem, []ProjectItem, []ProjectItem, error)
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const columns = `id, customer_id, name, status, revision, created_at, updated_at`

func scanOne(row pgx.Row) (*Project, error) {
	var (
		p                Project
		status           string
		created, updated time.Time
	)
	if err := row.Scan(&p.ID, &p.CustomerID, &p.Name, &status, &p.Revision, &created, &updated); err != nil {
		return nil, err
	}
	p.Status, p.CreatedAt, p.UpdatedAt = fromStorage(status), httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &p, nil
}

func (r *PostgresRepository) List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Project, bool, *int64, error) {
	conds := []string{"customer_id = $1"}
	args := []any{customerID}
	if f.Status != nil {
		args = append(args, storageOf[*f.Status])
		conds = append(conds, fmt.Sprintf(`status = $%d`, len(args)))
	}
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(created_at, id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+columns+` FROM projects WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer rows.Close()
	items := []Project{}
	for rows.Next() {
		p, err := scanOne(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan project: %w", err)
		}
		items = append(items, *p)
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
		// The count shares the filters, not the cursor predicate.
		countArgs := []any{customerID}
		cc := []string{"customer_id = $1"}
		if f.Status != nil {
			countArgs = append(countArgs, storageOf[*f.Status])
			cc = append(cc, fmt.Sprintf(`status = $%d`, len(countArgs)))
		}
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM projects WHERE `+strings.Join(cc, " AND "), countArgs...).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count projects: %w", err)
		}
		total = &n
	}
	return items, hasMore, total, nil
}

func (r *PostgresRepository) Get(ctx context.Context, id, customerID uuid.UUID) (*Project, error) {
	p, err := scanOne(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+columns+` FROM projects WHERE id = $1 AND customer_id = $2`, id, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return p, nil
}

func (r *PostgresRepository) Create(ctx context.Context, p *Project) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO projects (id, customer_id, name, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())`,
		p.ID, p.CustomerID, p.Name, storageOf[p.Status])
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	return nil
}

// Lock takes the row FOR UPDATE; revision checks happen after it, inside the
// caller's transaction.
func (r *PostgresRepository) Lock(ctx context.Context, id, customerID uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM projects WHERE id = $1 AND customer_id = $2 FOR UPDATE`, id, customerID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock project: %w", err)
	}
	return nil
}

// Update writes the row with the status the body named, or none: a NULL
// status keeps the stored spelling (SQL COALESCE), so a name-only update
// cannot blank or rename a legacy value the storage map does not know (the
// column is a free VARCHAR; 'On Hold ' and friends stay byte identical).
func (r *PostgresRepository) Update(ctx context.Context, p *Project, status *string) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE projects
		SET name = $2, status = COALESCE($3, status), revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		p.ID, p.Name, status)
	if err != nil {
		return fmt.Errorf("failed to update project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// itemColumns is the shared summary read of a grouped document: the total in
// integer cents, read and scaled in SQL (ADR 0001 section 7), never through
// float64.
func scanItem(row pgx.Row, kind string) (*ProjectItem, error) {
	var (
		it      ProjectItem
		status  string
		total   *int64
		created time.Time
		ref     string
	)
	if err := row.Scan(&it.ID, &status, &total, &created, &ref); err != nil {
		return nil, err
	}
	it.Type = kind
	it.Status = strings.ToLower(status)
	it.TotalCents = total
	it.CreatedAt = httpx.TimestampOf(created)
	it.Reference = ref
	return &it, nil
}

// Entities fetches the orders, deliveries and invoices grouped under the
// project, each a summary with its total in cents.
func (r *PostgresRepository) Entities(ctx context.Context, projectID, customerID uuid.UUID) ([]ProjectItem, []ProjectItem, []ProjectItem, error) {
	orders := []ProjectItem{}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, status, ROUND(total_amount * 100)::bigint, created_at,
			'Order ' || left(id::text, 8)
		FROM orders
		WHERE project_id = $1 AND customer_id = $2
		ORDER BY created_at DESC, id DESC`, projectID, customerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch orders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows, "order")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to scan order: %w", err)
		}
		orders = append(orders, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	deliveries := []ProjectItem{}
	rows, err = r.db.GetExecutor(ctx).Query(ctx, `
		SELECT d.id, d.status, NULL::bigint, d.created_at,
			'Delivery for order ' || left(o.id::text, 8)
		FROM deliveries d
		JOIN orders o ON d.order_id = o.id
		WHERE o.project_id = $1 AND o.customer_id = $2
		ORDER BY d.created_at DESC, d.id DESC`, projectID, customerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows, "delivery")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to scan delivery: %w", err)
		}
		deliveries = append(deliveries, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	invoices := []ProjectItem{}
	rows, err = r.db.GetExecutor(ctx).Query(ctx, `
		SELECT i.id, i.status, ROUND(i.total_amount * 100)::bigint, i.created_at,
			'Invoice for order ' || left(o.id::text, 8)
		FROM invoices i
		JOIN orders o ON i.order_id = o.id
		WHERE o.project_id = $1 AND i.customer_id = $2
		ORDER BY i.created_at DESC, i.id DESC`, projectID, customerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch invoices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows, "invoice")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to scan invoice: %w", err)
		}
		invoices = append(invoices, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	return orders, deliveries, invoices, nil
}
