// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

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
	Category  string
}

// Repository is the option store. Every statement goes through the context's
// executor, so a write inside a caller's transaction joins it. The catalog is
// global: a millwork option is not branch scoped, so no wall applies.
type Repository interface {
	List(ctx context.Context, f ListFilter, wantTotal bool) ([]Option, bool, *int64, error)
	Get(ctx context.Context, id uuid.UUID) (*Option, error)
	Create(ctx context.Context, o *Option) error
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const columns = `id, category, name, ROUND(price_adjustment * 100)::bigint,
	attributes, revision, created_at, updated_at`

func scanOne(row pgx.Row) (*Option, error) {
	var (
		o                Option
		created, updated time.Time
	)
	if err := row.Scan(&o.ID, &o.Category, &o.Name, &o.PriceAdjustmentCents,
		&o.Attributes, &o.Revision, &created, &updated); err != nil {
		return nil, err
	}
	o.CreatedAt, o.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &o, nil
}

func (r *PostgresRepository) List(ctx context.Context, f ListFilter, wantTotal bool) ([]Option, bool, *int64, error) {
	conds := []string{"category = $1"}
	args := []any{f.Category}
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(created_at, id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+columns+` FROM millwork_options WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list options: %w", err)
	}
	defer rows.Close()
	items := []Option{}
	for rows.Next() {
		o, err := scanOne(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan option: %w", err)
		}
		items = append(items, *o)
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
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM millwork_options WHERE category = $1`, f.Category).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count options: %w", err)
		}
		total = &n
	}
	return items, hasMore, total, nil
}

func (r *PostgresRepository) Get(ctx context.Context, id uuid.UUID) (*Option, error) {
	o, err := scanOne(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+columns+` FROM millwork_options WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get option: %w", err)
	}
	return o, nil
}

// centsArg writes the cents back as the column's decimal, through SQL
// parameters, never float64 (ADR 0001 section 7).
func centsArg(cents int64) string {
	sign := ""
	n := cents
	if n < 0 {
		sign, n = "-", -n
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}

func (r *PostgresRepository) Create(ctx context.Context, o *Option) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO millwork_options (id, category, name, price_adjustment, attributes)
		VALUES ($1, $2, $3, $4::numeric, $5)`,
		o.ID, o.Category, o.Name, centsArg(o.PriceAdjustmentCents), o.Attributes)
	if err != nil {
		return fmt.Errorf("failed to create option: %w", err)
	}
	return nil
}
