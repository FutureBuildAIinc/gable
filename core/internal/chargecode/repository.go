// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package chargecode

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository is the charge code store. Every statement goes through the
// context's executor, so a write inside a caller's transaction joins it.
type Repository interface {
	List(ctx context.Context, includeInactive bool) ([]Code, error)
	Get(ctx context.Context, id uuid.UUID) (*Code, error)
	Create(ctx context.Context, c *Code) error
	Update(ctx context.Context, c *Code) error
	Lock(ctx context.Context, id uuid.UUID) error
}

type PostgresRepository struct{ db *database.DB }

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

const columns = `
	id, code, name, revenue_account_code, taxable, default_unit_price, is_active, revision, created_at, updated_at`

func scanOne(row pgx.Row) (*Code, error) {
	var c Code
	var defaultPrice *string
	var created, updated time.Time
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.RevenueAccountCode, &c.Taxable, &defaultPrice,
		&c.IsActive, &c.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	if defaultPrice != nil {
		p, err := httpx.ParsePrice(*defaultPrice)
		if err != nil {
			return nil, fmt.Errorf("default price: %w", err)
		}
		c.DefaultUnitPrice = &p
	}
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &c, nil
}

func (r *PostgresRepository) List(ctx context.Context, includeInactive bool) ([]Code, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+columns+` FROM charge_codes WHERE ($1 OR is_active) ORDER BY code`, includeInactive)
	if err != nil {
		return nil, fmt.Errorf("failed to list charge codes: %w", err)
	}
	defer rows.Close()
	out := []Code{}
	for rows.Next() {
		c, err := scanOne(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan charge code: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) Get(ctx context.Context, id uuid.UUID) (*Code, error) {
	c, err := scanOne(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+columns+` FROM charge_codes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get charge code: %w", err)
	}
	return c, nil
}

// referenceFields maps the foreign keys a write can violate to the request
// field that named the missing record.
var referenceFields = map[string]string{
	"charge_codes_revenue_account_code_fkey": "revenue_account_code",
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23503":
			if field, ok := referenceFields[pgErr.ConstraintName]; ok {
				return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a referenced record does not exist",
					Details: []httpx.FieldError{{Field: field, Message: "no such account"}}}
			}
		case pgErr.Code == "23505" && pgErr.ConstraintName == "charge_codes_code_key":
			return &httpx.Error{Status: 409, Code: httpx.CodeDuplicate,
				Message: "a charge code with this code already exists",
				Details: []httpx.FieldError{{Field: "code", Message: "already used"}}}
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func defaultPriceArg(c *Code) any {
	if c.DefaultUnitPrice == nil {
		return nil
	}
	return c.DefaultUnitPrice.DecimalString()
}

func (r *PostgresRepository) Create(ctx context.Context, c *Code) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO charge_codes (id, code, name, revenue_account_code, taxable, default_unit_price, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, c.Code, c.Name, c.RevenueAccountCode, c.Taxable, defaultPriceArg(c), c.IsActive)
	if err != nil {
		return mapWriteError(err, "failed to insert charge code")
	}
	return nil
}

func (r *PostgresRepository) Lock(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM charge_codes WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock charge code: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Update(ctx context.Context, c *Code) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE charge_codes
		SET name = $2, revenue_account_code = $3, taxable = $4, default_unit_price = $5,
			is_active = $6, revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		c.ID, c.Name, c.RevenueAccountCode, c.Taxable, defaultPriceArg(c), c.IsActive)
	if err != nil {
		return mapWriteError(err, "failed to update charge code")
	}
	return nil
}
