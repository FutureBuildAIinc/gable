// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is the repository's answer for a row that does not exist.
var ErrNotFound = errors.New("tint row not found")

// ErrDuplicate is a unique key another row already holds.
var ErrDuplicate = errors.New("a tint row with this key already exists")

// The write drafts the create routes carry.
type SystemDraft struct {
	Code, Name, Manufacturer string
	UnitDenominator          int
	UnitLabel                string
}

type BaseDraft struct {
	TintSystemID         uuid.UUID
	Code, Name           string
	ManufacturerBaseCode string
	ProductID            *uuid.UUID
	FillVolumeOZ         int
	IsDeepest            bool
}

type ColorantDraft struct {
	TintSystemID     uuid.UUID
	Code, Name       string
	ManufacturerCode string
	ManufacturerUPC  *string
}

type ColorDraft struct {
	TintSystemID     uuid.UUID
	ManufacturerCode *string
	Name             string
	IsCustom         bool
	CustomerID       *uuid.UUID
	Note             string
}

type FormulaDraft struct {
	ColorID, BaseID   uuid.UUID
	ProductID         *uuid.UUID
	ContainerSizeCode string
	ContainerVolumeOZ int
	Source            string
	Components        []StoredComponent
}

// StoredComponent is a formula component with the colorant row it names.
type StoredComponent struct {
	ColorantID uuid.UUID
	Code, Name string
	Amount     Amount
}

type SavedColorDraft struct {
	CustomerID, ColorID uuid.UUID
	Name, JobNote, Note string
	ProjectID           *uuid.UUID
	CreatedBy           string
}

// Repository is the catalogue store. Every method reads and writes through
// the context's executor, so inside a transaction it never reaches for a
// second pool connection.
type Repository interface {
	ListSystems(ctx context.Context) ([]System, error)
	CreateSystem(ctx context.Context, d *SystemDraft) (*System, error)
	ListBases(ctx context.Context, systemID, productID *uuid.UUID) ([]Base, error)
	CreateBase(ctx context.Context, d *BaseDraft) (*Base, error)
	ListColorants(ctx context.Context, systemID *uuid.UUID) ([]Colorant, error)
	CreateColorant(ctx context.Context, d *ColorantDraft) (*Colorant, error)
	ListColors(ctx context.Context, systemID *uuid.UUID, q string) ([]Color, error)
	CreateColor(ctx context.Context, d *ColorDraft) (*Color, error)
	ListFormulas(ctx context.Context, colorID, baseID *uuid.UUID, sizeCode string) ([]Formula, error)
	GetFormula(ctx context.Context, id uuid.UUID) (*Formula, error)
	CreateFormula(ctx context.Context, d *FormulaDraft) (*Formula, error)
	// SystemOfFormula reads the system a formula belongs to, for the
	// component denominator rule and the rescale.
	SystemOfFormula(ctx context.Context, formulaID uuid.UUID) (*System, error)
	// SystemDenominatorOfColor reads the denominator of the system a colour
	// belongs to: every component of a formula of that colour carries it.
	SystemDenominatorOfColor(ctx context.Context, colorID uuid.UUID) (int, error)
	ListSavedColors(ctx context.Context, customerID *uuid.UUID, jobNote string) ([]SavedColor, error)
	CreateSavedColor(ctx context.Context, d *SavedColorDraft) (*SavedColor, error)
	DeleteSavedColor(ctx context.Context, id uuid.UUID) error
	// TintChargeCents reads the dealer's flat tint charge setting (0 when
	// none is set: the dominant retail model folds the tint into the shelf
	// price).
	TintChargeCents(ctx context.Context) (int64, error)
}

type PostgresRepository struct{ db *database.DB }

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

func mapInsert(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505":
			return ErrDuplicate
		case pgErr.Code == "23503":
			return ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *PostgresRepository) ListSystems(ctx context.Context) ([]System, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, code, name, manufacturer, unit_denominator, unit_label, created_at
		FROM paint_tint_systems ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("failed to list tint systems: %w", err)
	}
	defer rows.Close()
	out := []System{}
	for rows.Next() {
		var s System
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.Manufacturer, &s.UnitDenominator, &s.UnitLabel, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateSystem(ctx context.Context, d *SystemDraft) (*System, error) {
	s := &System{Code: d.Code, Name: d.Name, Manufacturer: d.Manufacturer,
		UnitDenominator: d.UnitDenominator, UnitLabel: d.UnitLabel}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_tint_systems (code, name, manufacturer, unit_denominator, unit_label)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		d.Code, d.Name, d.Manufacturer, d.UnitDenominator, d.UnitLabel).Scan(&s.ID, &s.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to create the tint system")
	}
	return s, nil
}

func (r *PostgresRepository) ListBases(ctx context.Context, systemID, productID *uuid.UUID) ([]Base, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, tint_system_id, code, name, manufacturer_base_code, product_id, fill_volume_oz, is_deepest, created_at
		FROM paint_bases
		WHERE ($1::uuid IS NULL OR tint_system_id = $1)
		  AND ($2::uuid IS NULL OR product_id = $2)
		ORDER BY tint_system_id, code`, systemID, productID)
	if err != nil {
		return nil, fmt.Errorf("failed to list bases: %w", err)
	}
	defer rows.Close()
	out := []Base{}
	for rows.Next() {
		var b Base
		if err := rows.Scan(&b.ID, &b.TintSystemID, &b.Code, &b.Name, &b.ManufacturerBaseCode, &b.ProductID,
			&b.FillVolumeOZ, &b.IsDeepest, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateBase(ctx context.Context, d *BaseDraft) (*Base, error) {
	b := &Base{TintSystemID: d.TintSystemID, Code: d.Code, Name: d.Name,
		ManufacturerBaseCode: d.ManufacturerBaseCode, ProductID: d.ProductID,
		FillVolumeOZ: d.FillVolumeOZ, IsDeepest: d.IsDeepest}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_bases (tint_system_id, code, name, manufacturer_base_code, product_id, fill_volume_oz, is_deepest)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		d.TintSystemID, d.Code, d.Name, d.ManufacturerBaseCode, d.ProductID, d.FillVolumeOZ, d.IsDeepest).
		Scan(&b.ID, &b.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to create the base")
	}
	return b, nil
}

func (r *PostgresRepository) ListColorants(ctx context.Context, systemID *uuid.UUID) ([]Colorant, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, tint_system_id, code, name, manufacturer_code, manufacturer_upc, created_at
		FROM paint_colorants
		WHERE ($1::uuid IS NULL OR tint_system_id = $1)
		ORDER BY tint_system_id, code`, systemID)
	if err != nil {
		return nil, fmt.Errorf("failed to list colorants: %w", err)
	}
	defer rows.Close()
	out := []Colorant{}
	for rows.Next() {
		var c Colorant
		if err := rows.Scan(&c.ID, &c.TintSystemID, &c.Code, &c.Name, &c.ManufacturerCode, &c.ManufacturerUPC, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateColorant(ctx context.Context, d *ColorantDraft) (*Colorant, error) {
	c := &Colorant{TintSystemID: d.TintSystemID, Code: d.Code, Name: d.Name,
		ManufacturerCode: d.ManufacturerCode, ManufacturerUPC: d.ManufacturerUPC}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_colorants (tint_system_id, code, name, manufacturer_code, manufacturer_upc)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		d.TintSystemID, d.Code, d.Name, d.ManufacturerCode, d.ManufacturerUPC).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to create the colorant")
	}
	return c, nil
}

func (r *PostgresRepository) ListColors(ctx context.Context, systemID *uuid.UUID, q string) ([]Color, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, tint_system_id, manufacturer_code, name, is_custom, customer_id, note, created_at
		FROM paint_colors
		WHERE ($1::uuid IS NULL OR tint_system_id = $1)
		  AND ($2 = '' OR name ILIKE '%' || $2 || '%' OR manufacturer_code ILIKE '%' || $2 || '%')
		ORDER BY name LIMIT 200`, systemID, q)
	if err != nil {
		return nil, fmt.Errorf("failed to list colours: %w", err)
	}
	defer rows.Close()
	out := []Color{}
	for rows.Next() {
		var c Color
		if err := rows.Scan(&c.ID, &c.TintSystemID, &c.ManufacturerCode, &c.Name, &c.IsCustom, &c.CustomerID, &c.Note, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateColor(ctx context.Context, d *ColorDraft) (*Color, error) {
	c := &Color{TintSystemID: d.TintSystemID, ManufacturerCode: d.ManufacturerCode,
		Name: d.Name, IsCustom: d.IsCustom, CustomerID: d.CustomerID, Note: d.Note}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_colors (tint_system_id, manufacturer_code, name, is_custom, customer_id, note)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		d.TintSystemID, d.ManufacturerCode, d.Name, d.IsCustom, d.CustomerID, d.Note).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to create the colour")
	}
	return c, nil
}

// componentsOf reads the components of the formulas, keyed by formula id, in
// guide order.
func (r *PostgresRepository) componentsOf(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Component, error) {
	out := map[uuid.UUID][]Component{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT fc.formula_id, c.id, c.code, c.name, fc.whole_oz, fc.increments_coarse, fc.increments_fine, fc.unit_denominator
		FROM paint_formula_components fc
		JOIN paint_colorants c ON c.id = fc.colorant_id
		WHERE fc.formula_id = ANY($1)
		ORDER BY fc.formula_id, fc.position`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to read formula components: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fid uuid.UUID
		var comp Component
		if err := rows.Scan(&fid, &comp.ColorantID, &comp.Code, &comp.Name, &comp.Amount.WholeOZ,
			&comp.Amount.Coarse, &comp.Amount.Fine, &comp.Amount.Denominator); err != nil {
			return nil, err
		}
		out[fid] = append(out[fid], comp)
	}
	return out, rows.Err()
}

const formulaCols = `id, color_id, base_id, product_id, container_size_code, container_volume_oz,
	total_colorant_oz::text, source, created_at`

func (r *PostgresRepository) ListFormulas(ctx context.Context, colorID, baseID *uuid.UUID, sizeCode string) ([]Formula, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+formulaCols+` FROM paint_formulas
		WHERE ($1::uuid IS NULL OR color_id = $1)
		  AND ($2::uuid IS NULL OR base_id = $2)
		  AND ($3 = '' OR container_size_code = $3)
		ORDER BY container_volume_oz LIMIT 200`, colorID, baseID, sizeCode)
	if err != nil {
		return nil, fmt.Errorf("failed to list formulas: %w", err)
	}
	defer rows.Close()
	out := []Formula{}
	for rows.Next() {
		f := Formula{}
		if err := rows.Scan(&f.ID, &f.ColorID, &f.BaseID, &f.ProductID, &f.ContainerSizeCode,
			&f.ContainerVolumeOZ, &f.TotalColorantOZ, &f.Source, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(out))
	for i := range out {
		ids = append(ids, out[i].ID)
	}
	comps, err := r.componentsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Components = comps[out[i].ID]
		if out[i].Components == nil {
			out[i].Components = []Component{}
		}
	}
	return out, nil
}

func (r *PostgresRepository) GetFormula(ctx context.Context, id uuid.UUID) (*Formula, error) {
	out := Formula{}
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+formulaCols+` FROM paint_formulas WHERE id = $1`, id).
		Scan(&out.ID, &out.ColorID, &out.BaseID, &out.ProductID, &out.ContainerSizeCode,
			&out.ContainerVolumeOZ, &out.TotalColorantOZ, &out.Source, &out.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	comps, err := r.componentsOf(ctx, []uuid.UUID{out.ID})
	if err != nil {
		return nil, err
	}
	out.Components = comps[out.ID]
	if out.Components == nil {
		out.Components = []Component{}
	}
	return &out, nil
}

func (r *PostgresRepository) SystemOfFormula(ctx context.Context, formulaID uuid.UUID) (*System, error) {
	s := &System{}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT s.id, s.code, s.name, s.manufacturer, s.unit_denominator, s.unit_label, s.created_at
		FROM paint_tint_systems s
		JOIN paint_colors c ON c.tint_system_id = s.id
		JOIN paint_formulas f ON f.color_id = c.id
		WHERE f.id = $1`, formulaID).
		Scan(&s.ID, &s.Code, &s.Name, &s.Manufacturer, &s.UnitDenominator, &s.UnitLabel, &s.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return s, nil
}

func (r *PostgresRepository) SystemDenominatorOfColor(ctx context.Context, colorID uuid.UUID) (int, error) {
	var den int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT s.unit_denominator FROM paint_tint_systems s
		JOIN paint_colors c ON c.tint_system_id = s.id
		WHERE c.id = $1`, colorID).Scan(&den)
	if err != nil {
		return 0, notFound(err)
	}
	return den, nil
}

// totalColorantOZ sums the components as an exact rational and prints it at
// scale 4, so the stored total can never disagree with the components.
func totalColorantOZ(components []StoredComponent) string {
	total := new(big.Rat)
	for _, c := range components {
		total.Add(total, ratOfAmount(c.Amount))
	}
	return total.FloatString(4)
}

// ratOfAmount is the amount as an exact rational of fluid ounces.
func ratOfAmount(a Amount) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(a.FineUnits()), big.NewInt(int64(a.Denominator)))
}

func (r *PostgresRepository) CreateFormula(ctx context.Context, d *FormulaDraft) (*Formula, error) {
	comps := make([]Component, 0, len(d.Components))
	for _, c := range d.Components {
		comps = append(comps, Component{ColorantID: c.ColorantID, Code: c.Code, Name: c.Name, Amount: c.Amount})
	}
	f := &Formula{ColorID: d.ColorID, BaseID: d.BaseID, ProductID: d.ProductID,
		ContainerSizeCode: d.ContainerSizeCode, ContainerVolumeOZ: d.ContainerVolumeOZ,
		Source: d.Source, Components: comps, TotalColorantOZ: totalColorantOZ(d.Components)}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_formulas (color_id, base_id, product_id, container_size_code, container_volume_oz, total_colorant_oz, source)
		VALUES ($1, $2, $3, $4, $5, $6::numeric, $7) RETURNING id, created_at`,
		d.ColorID, d.BaseID, d.ProductID, d.ContainerSizeCode, d.ContainerVolumeOZ, f.TotalColorantOZ, d.Source).
		Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to create the formula")
	}
	for i, comp := range d.Components {
		_, err := r.db.GetExecutor(ctx).Exec(ctx, `
			INSERT INTO paint_formula_components (formula_id, colorant_id, position, whole_oz, increments_coarse, increments_fine, unit_denominator)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			f.ID, comp.ColorantID, i, comp.Amount.WholeOZ, comp.Amount.Coarse, comp.Amount.Fine, comp.Amount.Denominator)
		if err != nil {
			return nil, mapInsert(err, "failed to create a formula component")
		}
	}
	return f, nil
}

func (r *PostgresRepository) ListSavedColors(ctx context.Context, customerID *uuid.UUID, jobNote string) ([]SavedColor, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, customer_id, color_id, name, job_note, note, project_id, created_by, created_at
		FROM paint_saved_colors
		WHERE ($1::uuid IS NULL OR customer_id = $1)
		  AND ($2 = '' OR job_note ILIKE '%' || $2 || '%')
		ORDER BY created_at DESC LIMIT 200`, customerID, jobNote)
	if err != nil {
		return nil, fmt.Errorf("failed to list saved colours: %w", err)
	}
	defer rows.Close()
	out := []SavedColor{}
	for rows.Next() {
		var s SavedColor
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.ColorID, &s.Name, &s.JobNote, &s.Note,
			&s.ProjectID, &s.CreatedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateSavedColor(ctx context.Context, d *SavedColorDraft) (*SavedColor, error) {
	s := &SavedColor{CustomerID: d.CustomerID, ColorID: d.ColorID, Name: d.Name,
		JobNote: d.JobNote, Note: d.Note, ProjectID: d.ProjectID, CreatedBy: d.CreatedBy}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO paint_saved_colors (customer_id, color_id, name, job_note, note, project_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		d.CustomerID, d.ColorID, d.Name, d.JobNote, d.Note, d.ProjectID, d.CreatedBy).Scan(&s.ID, &s.CreatedAt)
	if err != nil {
		return nil, mapInsert(err, "failed to save the colour")
	}
	return s, nil
}

func (r *PostgresRepository) DeleteSavedColor(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM paint_saved_colors WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete the saved colour: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) TintChargeCents(ctx context.Context) (int64, error) {
	var value *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = 'pos.tint_charge_cents'`).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read the tint charge setting: %w", err)
	}
	if value == nil || strings.TrimSpace(*value) == "" {
		return 0, nil
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(*value), 10, 64)
	if perr != nil || n < 0 {
		return 0, fmt.Errorf("the pos.tint_charge_cents setting is not a whole number of cents from 0 up")
	}
	return n, nil
}

// ChargeView is the dealer's tint charge as the counter reads it.
type ChargeView struct {
	TintChargeCents int64 `json:"tint_charge_cents"`
}
