// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

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

// ErrNotFound is returned when a staff member does not exist.
var ErrNotFound = errors.New("staff not found")

// ListFilter is the staff list's query: the keyset page, the opt in total,
// and the active filter.
type ListFilter struct {
	Limit   int
	Active  *bool
	AfterAt *time.Time
	AfterID uuid.UUID
}

// Repository is the Postgres implementation of the store: it reads and
// writes the `staff` / `module_grants` tables, the `modules.<id>.enabled`
// rows of `system_settings` and their `admin_revisions` anchors (migrations
// 080 and 095). Every statement goes through GetExecutor: inside a
// transaction that is the transaction, never the pool beside it.
type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository {
	return &Repository{db: db}
}

// listPredicate is the shared WHERE of the list and its count, so a filter
// can never filter in one and not the other.
func (f ListFilter) listPredicate() (string, []any) {
	conds := []string{}
	var args []any
	if f.Active != nil {
		args = append(args, *f.Active)
		conds = append(conds, "s.active = $"+strconv.Itoa(len(args)))
	}
	if f.AfterAt != nil {
		args = append(args, *f.AfterAt, f.AfterID)
		from := strconv.Itoa(len(args) - 1)
		conds = append(conds, "(s.created_at, s.id) < ($"+from+", $"+strconv.Itoa(len(args))+")")
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]Staff, error) {
	pred, args := f.listPredicate()
	q := `SELECT s.id, s.email, s.full_name, s.staff_no, s.role, s.active, s.revision, s.created_at, s.updated_at,
	       COALESCE(ARRAY_REMOVE(ARRAY_AGG(g.module_id ORDER BY g.module_id), NULL), '{}') AS modules
		FROM staff s
		LEFT JOIN module_grants g ON g.staff_id = s.id` +
		pred + ` GROUP BY s.id ORDER BY s.created_at DESC, s.id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, f.Limit)

	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Staff{}
	for rows.Next() {
		s, err := scanStaff(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// Count counts the rows the filter matches, for the opt in total.
func (r *Repository) Count(ctx context.Context, f ListFilter) (int64, error) {
	pred, args := f.listPredicate()
	q := `SELECT count(*) FROM staff s` + pred
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func scanStaff(scan func(dest ...any) error) (*Staff, error) {
	var s Staff
	var created, updated time.Time
	if err := scan(&s.ID, &s.Email, &s.FullName, &s.StaffNo, &s.Role, &s.Active, &s.Revision,
		&created, &updated, &s.Modules); err != nil {
		return nil, err
	}
	s.CreatedAt = httpx.TimestampOf(created)
	s.UpdatedAt = httpx.TimestampOf(updated)
	if s.Modules == nil {
		s.Modules = []string{}
	}
	return &s, nil
}

// Get returns a single staff member by id with granted modules attached.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Staff, error) {
	const q = `
		SELECT s.id, s.email, s.full_name, s.staff_no, s.role, s.active, s.revision, s.created_at, s.updated_at,
		       COALESCE(ARRAY_REMOVE(ARRAY_AGG(g.module_id ORDER BY g.module_id), NULL), '{}') AS modules
		FROM staff s
		LEFT JOIN module_grants g ON g.staff_id = s.id
		WHERE s.id = $1
		GROUP BY s.id`
	s, err := scanStaff(func(dest ...any) error {
		return r.db.GetExecutor(ctx).QueryRow(ctx, q, id).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// LockStaff takes SELECT ... FOR UPDATE on the row; the revision check and
// the write run after it, inside the same transaction.
func (r *Repository) LockStaff(ctx context.Context, id uuid.UUID) error {
	var one int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT 1 FROM staff WHERE id = $1 FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// mapWriteError turns a unique violation into the wire's 409 naming the
// request field, so a bad reference is never a 500.
func mapWriteError(err error) error {
	var pgErr interface{ SQLState() string }
	if !errors.As(err, &pgErr) || pgErr.SQLState() != "23505" {
		return err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "staff_email_key"):
		return httpx.Duplicate("a staff member with this email already exists",
			httpx.Blocker("email_taken", "email is already used by another staff member"))
	case strings.Contains(msg, "staff_staff_no_key"):
		return httpx.Duplicate("a staff member with this staff number already exists",
			httpx.Blocker("staff_no_taken", "staff_no is already used by another staff member"))
	}
	return err
}

// Create inserts a new staff member at revision 1.
func (r *Repository) Create(ctx context.Context, in *ParsedCreate) (*Staff, error) {
	const q = `
		INSERT INTO staff (email, full_name, staff_no, role, active)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, in.Email, in.FullName, in.StaffNo, in.Role, in.Active).Scan(&id)
	if err != nil {
		return nil, mapWriteError(err)
	}
	return r.Get(ctx, id)
}

// Update applies the non-nil fields of in and moves the revision; the caller
// has locked the row and checked the revision inside the same transaction.
func (r *Repository) Update(ctx context.Context, id uuid.UUID, in *ParsedUpdate) (*Staff, error) {
	sets := []string{}
	args := []any{}
	add := func(sql string, val any) {
		args = append(args, val)
		sets = append(sets, sql+" = $"+strconv.Itoa(len(args)))
	}
	if in.Email != nil {
		add("email", *in.Email)
	}
	if in.FullName != nil {
		add("full_name", *in.FullName)
	}
	if in.StaffNo != nil {
		if *in.StaffNo == "" {
			sets = append(sets, "staff_no = NULL")
		} else {
			add("staff_no", *in.StaffNo)
		}
	}
	if in.Role != nil {
		add("role", *in.Role)
	}
	if in.Active != nil {
		add("active", *in.Active)
	}
	if len(sets) == 0 {
		return r.Get(ctx, id)
	}
	sets = append(sets, "revision = revision + 1", "updated_at = NOW()")
	args = append(args, id)
	q := `UPDATE staff SET ` + strings.Join(sets, ", ") + ` WHERE id = $` + strconv.Itoa(len(args))
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, q, args...)
	if err != nil {
		return nil, mapWriteError(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.Get(ctx, id)
}

// GrantModule grants a module to a staff member, reporting whether the grant
// was added (false when it already existed: an idempotent no-op).
func (r *Repository) GrantModule(ctx context.Context, staffID uuid.UUID, moduleID, grantedBy string) (bool, error) {
	const q = `
		INSERT INTO module_grants (staff_id, module_id, granted_by)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (staff_id, module_id) DO NOTHING`
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, q, staffID, moduleID, grantedBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeModule removes a module grant, reporting whether a grant was removed.
func (r *Repository) RevokeModule(ctx context.Context, staffID uuid.UUID, moduleID string) (bool, error) {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`DELETE FROM module_grants WHERE staff_id = $1 AND module_id = $2`, staffID, moduleID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// BumpStaffRevision moves a staff row's revision without touching its
// columns (the grant routes move it, because the modules list is part of the
// staff document).
func (r *Repository) BumpStaffRevision(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnabledModules returns the set of module_ids whose modules.<id>.enabled flag
// is exactly 'true' in system_settings. The literal string compare matches the
// one internal/integrations uses when it computes entitlement; anything other
// than 'true' (missing row, 'false', '1', 'TRUE') means disabled on both sides.
func (r *Repository) EnabledModules(ctx context.Context) (map[string]bool, error) {
	const q = `SELECT key FROM system_settings WHERE key LIKE 'modules.%.enabled' AND value = 'true'`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	enabled := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(strings.TrimPrefix(key, "modules."), ".enabled")
		if id != "" {
			enabled[id] = true
		}
	}
	return enabled, rows.Err()
}

// SetModuleEnabled upserts the modules.<id>.enabled flag, reporting whether
// the value changed.
func (r *Repository) SetModuleEnabled(ctx context.Context, moduleID string, enabled bool) (bool, error) {
	value := "false"
	if enabled {
		value = "true"
	}
	// The conflict update carries a WHERE, so rewriting the same value
	// updates nothing and returns no row: that is the no-op answer.
	const q = `
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
			WHERE system_settings.value <> EXCLUDED.value
		RETURNING TRUE`
	var changed bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, moduleSettingKey(moduleID), value).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return changed, err
}

// ReadModuleRevision reads a module flag's revision anchor; a missing anchor
// is revision 1.
func (r *Repository) ReadModuleRevision(ctx context.Context, moduleID string) (int64, error) {
	var rev int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT revision FROM admin_revisions WHERE resource = $1`, moduleResource(moduleID)).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return rev, nil
}

// LockModuleRevision takes the module flag's revision anchor FOR UPDATE,
// creating the anchor on its first write.
func (r *Repository) LockModuleRevision(ctx context.Context, moduleID string) (int64, error) {
	ex := r.db.GetExecutor(ctx)
	if _, err := ex.Exec(ctx,
		`INSERT INTO admin_revisions (resource, revision) VALUES ($1, 1) ON CONFLICT (resource) DO NOTHING`,
		moduleResource(moduleID)); err != nil {
		return 0, err
	}
	var rev int64
	err := ex.QueryRow(ctx,
		`SELECT revision FROM admin_revisions WHERE resource = $1 FOR UPDATE`, moduleResource(moduleID)).Scan(&rev)
	return rev, err
}

// BumpModuleRevision moves a module flag's revision anchor.
func (r *Repository) BumpModuleRevision(ctx context.Context, moduleID string, to int64) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE admin_revisions SET revision = $2, updated_at = NOW() WHERE resource = $1`, moduleResource(moduleID), to)
	return err
}
