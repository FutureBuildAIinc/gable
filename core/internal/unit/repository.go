// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is the repository's answer for a unit that does not exist.
var ErrNotFound = errors.New("unit not found")

// ErrDuplicate is a code another unit already holds.
var ErrDuplicate = errors.New("a unit with this code already exists")

// StaleRevisionError carries the revision the row holds.
type StaleRevisionError struct{ Have int64 }

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("the unit moved past the revision sent (it holds %d)", e.Have)
}

// Row is the stored shape of a unit. The standard size is read and written
// in SQL as the scale 4 integer (ROUND(col * 10000)::bigint), never through
// float64 and never through text.
type Row struct {
	Code       string
	Name       string
	Dimension  Dimension
	StdUnitQty *httpx.Quantity
	StdRefQty  *httpx.Quantity
	IsSystem   bool
	IsActive   bool
	Revision   int64
}

// Repository is the catalogue store. Every method reads and writes through
// the context's executor, so inside a transaction it never reaches for a
// second pool connection.
type Repository interface {
	GetUnit(ctx context.Context, code string) (*Row, error)
	// LockUnit takes the row lock for the rest of the transaction, so a
	// revision check and the write after it are one act.
	LockUnit(ctx context.Context, code string) error
	ListUnits(ctx context.Context, dimension *Dimension, isActive *bool, after *string, limit int) ([]Row, error)
	CountUnits(ctx context.Context, dimension *Dimension, isActive *bool) (int64, error)
	CreateUnit(ctx context.Context, r *Row) error
	UpdateUnit(ctx context.Context, r *Row, revision int64) error
	// UnitReferenced reports whether any product unit set row or quote
	// line names the unit: the immutability rule of section 2.1.
	UnitReferenced(ctx context.Context, code string) (bool, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

const columns = `code, name, dimension, ROUND(std_unit_qty * 10000)::bigint, ROUND(std_ref_qty * 10000)::bigint, is_system, is_active, revision`

func scanUnit(row pgx.Row) (*Row, error) {
	var r Row
	err := row.Scan(&r.Code, &r.Name, &r.Dimension, &r.StdUnitQty, &r.StdRefQty,
		&r.IsSystem, &r.IsActive, &r.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read unit: %w", err)
	}
	return &r, nil
}

func (r *PostgresRepository) GetUnit(ctx context.Context, code string) (*Row, error) {
	row := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+columns+` FROM units WHERE code = $1`, code)
	return scanUnit(row)
}

func (r *PostgresRepository) LockUnit(ctx context.Context, code string) error {
	var locked string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT code FROM units WHERE code = $1 FOR UPDATE`, code).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock unit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListUnits(ctx context.Context, dimension *Dimension, isActive *bool, after *string, limit int) ([]Row, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+columns+` FROM units
		WHERE ($1::text IS NULL OR dimension = $1)
		  AND ($2::boolean IS NULL OR is_active = $2)
		  AND ($3::text IS NULL OR code > $3)
		ORDER BY code ASC
		LIMIT $4`, dimension, isActive, after, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list units: %w", err)
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Code, &r.Name, &r.Dimension, &r.StdUnitQty, &r.StdRefQty,
			&r.IsSystem, &r.IsActive, &r.Revision); err != nil {
			return nil, fmt.Errorf("failed to scan unit: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountUnits(ctx context.Context, dimension *Dimension, isActive *bool) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT count(*) FROM units
		WHERE ($1::text IS NULL OR dimension = $1)
		  AND ($2::boolean IS NULL OR is_active = $2)`, dimension, isActive).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count units: %w", err)
	}
	return n, nil
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "units_pkey":
			return ErrDuplicate
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func stdArgs(r *Row) (any, any) {
	var u, s any
	if r.StdUnitQty != nil {
		u, s = int64(*r.StdUnitQty), int64(*r.StdRefQty)
	}
	return u, s
}

func (r *PostgresRepository) CreateUnit(ctx context.Context, r2 *Row) error {
	u, s := stdArgs(r2)
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO units (code, name, dimension, std_unit_qty, std_ref_qty, is_active)
		VALUES ($1, $2, $3, $4::numeric / 10000, $5::numeric / 10000, $6)`,
		r2.Code, r2.Name, string(r2.Dimension), u, s, r2.IsActive)
	if err != nil {
		return mapWriteError(err, "failed to create unit")
	}
	return nil
}

func (r *PostgresRepository) UpdateUnit(ctx context.Context, row *Row, revision int64) error {
	u, s := stdArgs(row)
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE units
		SET name = $1, dimension = $2, std_unit_qty = $3::numeric / 10000,
		    std_ref_qty = $4::numeric / 10000, is_active = $5,
		    revision = revision + 1, updated_at = NOW()
		WHERE code = $6 AND revision = $7`,
		row.Name, string(row.Dimension), u, s, row.IsActive, row.Code, revision)
	if err != nil {
		return mapWriteError(err, "failed to update unit")
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetUnit(ctx, row.Code); errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return &StaleRevisionError{}
	}
	return nil
}

func (r *PostgresRepository) UnitReferenced(ctx context.Context, code string) (bool, error) {
	var referenced bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM product_units WHERE uom = $1
			UNION ALL
			SELECT 1 FROM quote_lines WHERE uom = $1 OR price_uom = $1
		)`, code).Scan(&referenced)
	if err != nil {
		return false, fmt.Errorf("failed to check unit references: %w", err)
	}
	return referenced, nil
}

// ViewOf maps a stored row onto the wire shape, the standard size kept in
// the canonical form it is stored in.
func ViewOf(r *Row) *Unit {
	u := &Unit{
		Code: r.Code, Name: r.Name, Dimension: r.Dimension,
		IsSystem: r.IsSystem, IsActive: r.IsActive, Revision: r.Revision,
	}
	if r.StdUnitQty != nil {
		sq := httpx.Quantity(*r.StdUnitQty)
		sr := httpx.Quantity(*r.StdRefQty)
		u.StdUnitQty, u.StdRefQty = &sq, &sr
	}
	return u
}
