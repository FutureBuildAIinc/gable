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
	// Update writes the option's money and attributes and moves its revision
	// on, but only when the row still carries the revision the caller built
	// on (ADR 0001 section 11).
	Update(ctx context.Context, o *Option, revision int64) (int64, error)
	// OptionsByCategories reads the options of the named categories, keyed
	// by category. It is how the pricing reads the catalog: one query for
	// every category the configuration selects, so a price is computed from
	// the catalog and never from a guess.
	OptionsByCategories(ctx context.Context, categories []string) (map[string][]Option, error)
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const columns = `id, category, name, ROUND(price_adjustment * 100)::bigint,
	base_price_cents, base_cost_cents, cost_adjustment_cents,
	attributes, revision, created_at, updated_at`

func scanOne(row pgx.Row) (*Option, error) {
	var (
		o                Option
		created, updated time.Time
	)
	if err := row.Scan(&o.ID, &o.Category, &o.Name, &o.PriceAdjustmentCents,
		&o.BasePriceCents, &o.BaseCostCents, &o.CostAdjustmentCents,
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
		INSERT INTO millwork_options (id, category, name, price_adjustment,
			base_price_cents, base_cost_cents, cost_adjustment_cents, attributes)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8)`,
		o.ID, o.Category, o.Name, centsArg(o.PriceAdjustmentCents),
		o.BasePriceCents, o.BaseCostCents, o.CostAdjustmentCents, o.Attributes)
	if err != nil {
		return fmt.Errorf("failed to create option: %w", err)
	}
	return nil
}

// Update writes the option's money and attributes and moves its revision on.
// The revision is the precondition: the write lands only while the row still
// carries the revision the caller read (ADR 0001 section 11).
func (r *PostgresRepository) Update(ctx context.Context, o *Option, revision int64) (int64, error) {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE millwork_options
		SET price_adjustment = $3::numeric,
		    base_price_cents = $4,
		    base_cost_cents = $5,
		    cost_adjustment_cents = $6,
		    attributes = $7,
		    revision = revision + 1,
		    updated_at = NOW()
		WHERE id = $1 AND revision = $2`,
		o.ID, revision, centsArg(o.PriceAdjustmentCents),
		o.BasePriceCents, o.BaseCostCents, o.CostAdjustmentCents, o.Attributes)
	if err != nil {
		return 0, fmt.Errorf("failed to update option: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the row is gone or it moved past the revision the caller
		// built on. The service tells the two apart: a row that is gone is
		// a 404, a row that moved on is a 409.
		if _, gerr := r.Get(ctx, o.ID); gerr != nil {
			return 0, gerr
		}
		return 0, ErrStaleRevision
	}
	return revision + 1, nil
}

// OptionsByCategories reads the options of the named categories.
func (r *PostgresRepository) OptionsByCategories(ctx context.Context, categories []string) (map[string][]Option, error) {
	out := make(map[string][]Option, len(categories))
	if len(categories) == 0 {
		return out, nil
	}
	// The catalog's own category is folded the same way the configuration's
	// is, so a dealer whose catalog spells a category its own way still
	// gets the price. The fold is lower(regexp_replace(lower(category),
	// '[^a-z0-9]+', '_', 'g')), the same shape CategoryName writes: the
	// lower casing comes first, so an upper case letter is a letter and
	// not a separator.
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+columns+` FROM millwork_options
		WHERE lower(regexp_replace(lower(category), '[^a-z0-9]+', '_', 'g')) = ANY($1)
		ORDER BY category, name, created_at DESC, id DESC`, categories)
	if err != nil {
		return nil, fmt.Errorf("failed to read the option catalog: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			o                Option
			created, updated time.Time
		)
		if err := rows.Scan(&o.ID, &o.Category, &o.Name, &o.PriceAdjustmentCents,
			&o.BasePriceCents, &o.BaseCostCents, &o.CostAdjustmentCents,
			&o.Attributes, &o.Revision, &created, &updated); err != nil {
			return nil, fmt.Errorf("failed to scan an option: %w", err)
		}
		o.CreatedAt, o.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
		key := CategoryName(o.Category)
		out[key] = append(out[key], o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read the option catalog: %w", err)
	}
	return out, nil
}
