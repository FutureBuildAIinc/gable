// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository is the admin half of the zone tables: the create, the list, the
// read, the edit and the close. A zone that a document has used is never
// deleted, only closed, because the sale that used it must stay explainable.
type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

// Draft is a parsed create or edit: the zone's own fields, its rate parts and
// its boundaries.
type Draft struct {
	Country          string
	JurisdictionCode string
	JurisdictionType string
	JurisdictionName string
	StateCode        string
	CountyCode       string
	PlaceCode        string
	ProvinceCode     string
	ZoneKind         string
	Sourcing         string
	EffectiveFrom    string
	EffectiveTo      *string
	DataSource       string
	DataVersion      string
	Rates            []RatePart
	Boundaries       []Boundary
}

var jurisdictionTypes = []string{"STA", "CTY", "CIT", "STJ", "PROV"}
var sourcingValues = []string{"ORIGIN", "DESTINATION", "BOTH"}
var partNames = []string{PartState, PartCounty, PartCity, PartDistrict, PartGoodsTax, PartProvincialSales}

// Create writes a zone with its parts and its boundaries in one transaction.
func (r *Repository) Create(ctx context.Context, d *Draft) (*Zone, error) {
	id := uuid.New()
	err := r.db.RunInTx(ctx, func(ctx context.Context) error {
		exec := r.db.GetExecutor(ctx)
		var to any
		if d.EffectiveTo != nil {
			to = *d.EffectiveTo
		}
		if _, err := exec.Exec(ctx, `
			INSERT INTO tax_zones (id, country, jurisdiction_code, jurisdiction_type, jurisdiction_name,
				state_code, county_code, place_code, province_code, zone_kind, sourcing,
				effective_from, effective_to, data_source, data_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::date, $13::date, $14, $15)`,
			id, d.Country, d.JurisdictionCode, d.JurisdictionType, d.JurisdictionName,
			d.StateCode, d.CountyCode, d.PlaceCode, d.ProvinceCode, d.ZoneKind, d.Sourcing,
			d.EffectiveFrom, to, d.DataSource, d.DataVersion); err != nil {
			return fmt.Errorf("failed to insert the zone: %w", err)
		}
		if err := insertRates(ctx, exec, id, d.Rates); err != nil {
			return err
		}
		return insertBoundaries(ctx, exec, id, d.Boundaries)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

func insertRates(ctx context.Context, exec database.Executor, id uuid.UUID, rates []RatePart) error {
	for i := range rates {
		var to any
		if rates[i].EffectiveTo != "" {
			to = rates[i].EffectiveTo
		}
		if _, err := exec.Exec(ctx, `
			INSERT INTO tax_zone_rates (zone_id, part_name, rate, effective_from, effective_to)
			VALUES ($1, $2, $3, $4::date, $5::date)`,
			id, rates[i].Name, rates[i].Rate, rates[i].EffectiveFrom, to); err != nil {
			return fmt.Errorf("failed to insert a zone rate part: %w", err)
		}
	}
	return nil
}

func insertBoundaries(ctx context.Context, exec database.Executor, id uuid.UUID, bounds []Boundary) error {
	for i := range bounds {
		if _, err := exec.Exec(ctx, `
			INSERT INTO tax_zone_boundaries (zone_id, country, postal_code_low, postal_code_high, extension_low, extension_high)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id, bounds[i].Country, bounds[i].PostalLow, bounds[i].PostalHigh,
			bounds[i].ExtensionLow, bounds[i].ExtensionHigh); err != nil {
			return fmt.Errorf("failed to insert a zone boundary: %w", err)
		}
	}
	return nil
}

// List answers the zones, newest first, filtered by country and jurisdiction
// type when the query names them.
func (r *Repository) List(ctx context.Context, country, jurisdictionType string) ([]*Zone, error) {
	sql := `SELECT id FROM tax_zones`
	var where []string
	var args []any
	if country != "" {
		args = append(args, strings.ToUpper(country))
		where = append(where, fmt.Sprintf("country = $%d", len(args)))
	}
	if jurisdictionType != "" {
		args = append(args, strings.ToUpper(jurisdictionType))
		where = append(where, fmt.Sprintf("jurisdiction_type = $%d", len(args)))
	}
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY created_at DESC, id DESC"
	rows, err := r.db.GetExecutor(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list the zones: %w", err)
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to read a zone id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list the zones: %w", err)
	}
	out := make([]*Zone, 0, len(ids))
	for _, id := range ids {
		z, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, nil
}

// Get reads one zone with its parts and its boundaries.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Zone, error) {
	z := &Zone{}
	var to *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, country, jurisdiction_code, jurisdiction_type, jurisdiction_name,
			state_code, county_code, place_code, province_code, zone_kind, sourcing,
			effective_from::text, effective_to::text, data_source, data_version
		FROM tax_zones WHERE id = $1`, id).
		Scan(&z.ID, &z.Country, &z.JurisdictionCode, &z.JurisdictionType, &z.JurisdictionName,
			&z.StateCode, &z.CountyCode, &z.PlaceCode, &z.ProvinceCode, &z.ZoneKind, &z.Sourcing,
			&z.EffectiveFrom, &to, &z.DataSource, &z.DataVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("tax zone not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the zone: %w", err)
	}
	z.EffectiveTo = to

	rates, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT part_name, rate::text, effective_from::text, COALESCE(effective_to::text, '')
		FROM tax_zone_rates WHERE zone_id = $1 ORDER BY effective_from DESC, part_name`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read the zone's rate parts: %w", err)
	}
	defer rates.Close()
	for rates.Next() {
		var p RatePart
		if err := rates.Scan(&p.Name, &p.Rate, &p.EffectiveFrom, &p.EffectiveTo); err != nil {
			return nil, fmt.Errorf("failed to read a zone rate part: %w", err)
		}
		z.Rates = append(z.Rates, p)
	}
	if err := rates.Err(); err != nil {
		return nil, fmt.Errorf("failed to read the zone's rate parts: %w", err)
	}

	bounds, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT country, postal_code_low, postal_code_high, extension_low, extension_high
		FROM tax_zone_boundaries WHERE zone_id = $1 ORDER BY postal_code_low`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read the zone's boundaries: %w", err)
	}
	defer bounds.Close()
	for bounds.Next() {
		var b Boundary
		if err := bounds.Scan(&b.Country, &b.PostalLow, &b.PostalHigh, &b.ExtensionLow, &b.ExtensionHigh); err != nil {
			return nil, fmt.Errorf("failed to read a zone boundary: %w", err)
		}
		z.Boundaries = append(z.Boundaries, b)
	}
	if err := bounds.Err(); err != nil {
		return nil, fmt.Errorf("failed to read the zone's boundaries: %w", err)
	}
	return z, nil
}

// Close ends a zone's effective window on the named date. A zone a document
// has already used is closed, never deleted, and a close that begins today or
// later keeps every earlier sale on the rate it was charged.
func (r *Repository) Close(ctx context.Context, id uuid.UUID, on string) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE tax_zones SET effective_to = $2::date, updated_at = NOW() WHERE id = $1`, id, on)
	if err != nil {
		return fmt.Errorf("failed to close the zone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("tax zone not found")
	}
	return nil
}

// Delete removes a zone no document has used, with its parts and boundaries.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	err := r.db.RunInTx(ctx, func(ctx context.Context) error {
		exec := r.db.GetExecutor(ctx)
		if _, err := exec.Exec(ctx, `DELETE FROM tax_zone_boundaries WHERE zone_id = $1`, id); err != nil {
			return fmt.Errorf("failed to delete the zone's boundaries: %w", err)
		}
		if _, err := exec.Exec(ctx, `DELETE FROM tax_zone_rates WHERE zone_id = $1`, id); err != nil {
			return fmt.Errorf("failed to delete the zone's rate parts: %w", err)
		}
		tag, err := exec.Exec(ctx, `DELETE FROM tax_zones WHERE id = $1`, id)
		if err != nil {
			return fmt.Errorf("failed to delete the zone: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFound("tax zone not found")
		}
		return nil
	})
	return err
}

// UsedBy counts the documents that name the zone: a used zone answers the
// admin screen's delete with the count, so the screen closes it instead.
func (r *Repository) UsedBy(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM orders WHERE tax_zone_id = $1)
		     + (SELECT count(*) FROM invoices WHERE tax_zone_id = $1)`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count the zone's documents: %w", err)
	}
	return n, nil
}
