// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrStaleRevision is a write's answer when the row moved past the revision
// the caller built on (ADR 0001 section 11).
var ErrStaleRevision = errors.New("stale revision")

// ErrNotFound is returned when a lookup by id finds no row.
var ErrNotFound = errors.New("location not found")

// locationColumns lists the columns selected by every SELECT so the scanner
// stays in sync with model.Location. Nullable string columns are COALESCEd to
// ” because the model uses non-pointer string fields (non-branch rows leave
// branch-only metadata NULL in the DB).
const locationColumns = `
    id, parent_id, path, type, code, description,
    name, address, city, state, zip, phone,
    tax_jurisdiction_code, default_tax_rate,
    timezone, active, branch_id,
    revision, created_at, updated_at
`

// ListScope is a location list's branch scope: nil is every branch, an
// empty slice is no branches (a bound caller with no grants lists nothing).
type ListScope struct {
	Branches       []uuid.UUID
	IncludeInactive bool
}

type Repository interface {
	CreateLocation(ctx context.Context, loc *Location) error
	GetLocation(ctx context.Context, id uuid.UUID) (*Location, error)
	UpdateLocation(ctx context.Context, loc *Location, revision int64) error
	DeleteLocation(ctx context.Context, id uuid.UUID, revision int64) error // soft delete: active=false
	ListLocationsPage(ctx context.Context, scope ListScope, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, error)
	ListBranchesPage(ctx context.Context, includeInactive bool, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, error)
	CountBranches(ctx context.Context, includeInactive bool) (int64, error)
	CountLocations(ctx context.Context, scope ListScope) (int64, error)
	GetBranchTree(ctx context.Context, branchID uuid.UUID) ([]Location, error)
	IsBranch(ctx context.Context, id uuid.UUID) (bool, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func scanLocation(row pgx.Row, loc *Location) error {
	var created, updated time.Time
	if err := row.Scan(
		&loc.ID,
		&loc.ParentID,
		&loc.Path,
		&loc.Type,
		&loc.Code,
		&loc.Description,
		&loc.Name,
		&loc.Address,
		&loc.City,
		&loc.State,
		&loc.Zip,
		&loc.Phone,
		&loc.TaxJurisdictionCode,
		&loc.DefaultTaxRate,
		&loc.Timezone,
		&loc.Active,
		&loc.BranchID,
		&loc.Revision,
		&created,
		&updated,
	); err != nil {
		return err
	}
	loc.CreatedAt = httpx.TimestampOf(created)
	loc.UpdatedAt = httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) CreateLocation(ctx context.Context, loc *Location) error {
	query := `
		INSERT INTO locations (
			parent_id, path, type, code, description,
			name, address, city, state, zip, phone,
			tax_jurisdiction_code, default_tax_rate, timezone, active
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15,TRUE))
		RETURNING ` + locationColumns

	row := r.db.GetExecutor(ctx).QueryRow(ctx, query,
		loc.ParentID, loc.Path, loc.Type, loc.Code, loc.Description,
		loc.Name, loc.Address, loc.City, loc.State, loc.Zip, loc.Phone,
		loc.TaxJurisdictionCode, loc.DefaultTaxRate, tzOrDefault(loc.Timezone), loc.Active,
	)
	if err := scanLocation(row, loc); err != nil {
		return fmt.Errorf("create location: %w", err)
	}
	return nil
}

// tzOrDefault is the row's timezone or the dealer default when none was sent.
func tzOrDefault(tz *string) any {
	if tz == nil || *tz == "" {
		return "America/New_York"
	}
	return *tz
}

func (r *PostgresRepository) GetLocation(ctx context.Context, id uuid.UUID) (*Location, error) {
	query := `SELECT ` + locationColumns + ` FROM locations WHERE id = $1`
	var loc Location
	if err := scanLocation(r.db.GetExecutor(ctx).QueryRow(ctx, query, id), &loc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get location: %w", err)
	}
	return &loc, nil
}

// UpdateLocation carries the revision precondition: the check and the write
// are one database act, and zero rows means stale or gone (ADR 0001 section
// 11).
func (r *PostgresRepository) UpdateLocation(ctx context.Context, loc *Location, revision int64) error {
	query := `
		UPDATE locations SET
			path = $3,
			code = $4,
			description = $5,
			name = $6,
			address = $7,
			city = $8,
			state = $9,
			zip = $10,
			phone = $11,
			tax_jurisdiction_code = $12,
			default_tax_rate = $13,
			timezone = COALESCE(NULLIF($14,''), timezone),
			active = $15,
			revision = revision + 1,
			updated_at = NOW()
		WHERE id = $1 AND revision = $2
		RETURNING ` + locationColumns

	row := r.db.GetExecutor(ctx).QueryRow(ctx, query,
		loc.ID, revision, loc.Path, loc.Code, loc.Description,
		loc.Name, loc.Address, loc.City, loc.State, loc.Zip, loc.Phone,
		loc.TaxJurisdictionCode, loc.DefaultTaxRate, derefString(loc.Timezone), loc.Active,
	)
	if err := scanLocation(row, loc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.staleOrMissing(ctx, loc.ID)
		}
		return fmt.Errorf("update location: %w", err)
	}
	return nil
}

func derefString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// staleOrMissing tells a missing row from a stale revision for the write the
// caller just attempted.
func (r *PostgresRepository) staleOrMissing(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM locations WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
		return ErrStaleRevision
	}
	return ErrNotFound
}

// DeleteLocation soft-deletes by setting active=false. Branches can be
// archived this way; bins typically shouldn't be soft-deleted (use a hard
// delete via DELETE FROM if needed).
func (r *PostgresRepository) DeleteLocation(ctx context.Context, id uuid.UUID, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE locations SET active = FALSE, revision = revision + 1, updated_at = NOW()
		 WHERE id = $1 AND revision = $2`, id, revision)
	if err != nil {
		return fmt.Errorf("delete location: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return r.staleOrMissing(ctx, id)
	}
	return nil
}

// ListLocationsPage is the locations list's keyset page: `created_at DESC,
// id DESC` inside the branch scope the caller's wall resolved (nil branches
// is every branch, an empty slice is no branches). after is nil for the
// first page.
func (r *PostgresRepository) ListLocationsPage(ctx context.Context, scope ListScope, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, error) {
	query := `SELECT ` + locationColumns + ` FROM locations`
	args := []any{}
	conds := []string{}
	if scope.Branches != nil {
		conds = append(conds, fmt.Sprintf(`branch_id = ANY($%d)`, len(args)+1))
		args = append(args, scope.Branches)
	}
	if after != nil {
		conds = append(conds, fmt.Sprintf(`(created_at, id) < ($%d, $%d)`, len(args)+1, len(args)+2))
		args = append(args, *after, *afterID)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + joinAnd(conds)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	query += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	return scanLocations(rows)
}

// joinAnd joins SQL conditions with AND.
func joinAnd(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}

// CountLocations is the include=total count under the same scope.
func (r *PostgresRepository) CountLocations(ctx context.Context, scope ListScope) (int64, error) {
	query := `SELECT COUNT(*) FROM locations`
	args := []any{}
	if scope.Branches != nil {
		query += ` WHERE branch_id = ANY($1)`
		args = append(args, scope.Branches)
	}
	var total int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count locations: %w", err)
	}
	return total, nil
}

// ListBranchesPage is the branches list's keyset page.
func (r *PostgresRepository) ListBranchesPage(ctx context.Context, includeInactive bool, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, error) {
	query := `SELECT ` + locationColumns + ` FROM locations WHERE type = 'BRANCH'`
	args := []any{}
	if !includeInactive {
		query += ` AND active = TRUE`
	}
	if after != nil {
		query += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, *after, *afterID)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	query += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	return scanLocations(rows)
}

// CountBranches is the include=total count.
func (r *PostgresRepository) CountBranches(ctx context.Context, includeInactive bool) (int64, error) {
	query := `SELECT COUNT(*) FROM locations WHERE type = 'BRANCH'`
	if !includeInactive {
		query += ` AND active = TRUE`
	}
	var total int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("count branches: %w", err)
	}
	return total, nil
}

// GetBranchTree returns the branch row plus all of its descendants ordered
// by path. The caller is responsible for stitching them into a tree if
// needed; the materialized `path` column is sufficient for most UI use cases.
func (r *PostgresRepository) GetBranchTree(ctx context.Context, branchID uuid.UUID) ([]Location, error) {
	query := `SELECT ` + locationColumns + ` FROM locations
	          WHERE branch_id = $1 OR id = $1
	          ORDER BY path ASC`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, branchID)
	if err != nil {
		return nil, fmt.Errorf("get branch tree: %w", err)
	}
	return scanLocations(rows)
}

func (r *PostgresRepository) IsBranch(ctx context.Context, id uuid.UUID) (bool, error) {
	var t string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT type FROM locations WHERE id = $1`, id).Scan(&t)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("is branch: %w", err)
	}
	return t == string(LocTypeBranch), nil
}

func scanLocations(rows pgx.Rows) ([]Location, error) {
	defer rows.Close()
	var locs []Location
	for rows.Next() {
		var loc Location
		if err := scanLocation(rows, &loc); err != nil {
			return nil, fmt.Errorf("scan location: %w", err)
		}
		locs = append(locs, loc)
	}
	return locs, rows.Err()
}
