// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import (
	"context"
	"errors"
	"strings"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PostgresStore keeps the settings in system_settings. Every statement goes
// through GetExecutor: inside a transaction it is the transaction.
type PostgresStore struct{ db *database.DB }

// NewPostgresStore builds the settings store.
func NewPostgresStore(db *database.DB) *PostgresStore { return &PostgresStore{db: db} }

// Read returns one setting.
func (p *PostgresStore) Read(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := p.db.GetExecutor(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// Write upserts one setting.
func (p *PostgresStore) Write(ctx context.Context, key, value string) error {
	_, err := p.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()`, key, value)
	return err
}

// Delete removes one setting.
func (p *PostgresStore) Delete(ctx context.Context, key string) error {
	_, err := p.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, key)
	return err
}

// PostgresCatalog reads the yard and the products. Our price is the product's
// base price in cents, exactly the figure the counter's search answers as
// unit_price_cents (pos SearchProducts), and our cost its average unit cost.
type PostgresCatalog struct{ db *database.DB }

// NewPostgresCatalog builds the catalogue reader.
func NewPostgresCatalog(db *database.DB) *PostgresCatalog { return &PostgresCatalog{db: db} }

// Yard answers the caller's yard: the branch in context, else the dealer's
// default branch, else the first active branch.
func (c *PostgresCatalog) Yard(ctx context.Context) (Yard, error) {
	var name, city, state string
	err := c.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(l.name, ''), COALESCE(l.city, ''), COALESCE(l.state, '')
		FROM locations l
		WHERE l.type = 'BRANCH' AND l.active
		ORDER BY COALESCE(l.id = $1::uuid, FALSE) DESC,
		         COALESCE(l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id'), FALSE) DESC,
		         l.name, l.id
		LIMIT 1`, middleware.BranchIDForQuery(ctx)).Scan(&name, &city, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return Yard{}, nil
	}
	if err != nil {
		return Yard{}, err
	}
	town := strings.TrimSpace(city)
	if st := strings.TrimSpace(state); st != "" {
		if town != "" {
			town += ", "
		}
		town += st
	}
	return Yard{Name: strings.TrimSpace(name), Town: town}, nil
}

// Products reads the picked products.
func (c *PostgresCatalog) Products(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Product, error) {
	rows, err := c.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, COALESCE(sku, ''), COALESCE(description, ''), COALESCE(category, ''),
		       COALESCE(uom_primary::text, 'EA'),
		       COALESCE(ROUND(base_price * 100)::bigint, 0),
		       COALESCE(ROUND(average_unit_cost * 100)::bigint, 0)
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.SKU, &p.Description, &p.Category, &p.UOM, &p.PriceCents, &p.CostCents); err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// AuditRecorder writes a settings change to the platform audit log.
type AuditRecorder struct{ L *audit.Logger }

// Record logs the action with the changed field names only.
func (a AuditRecorder) Record(ctx context.Context, action string, fields []string) error {
	return a.L.Log(ctx, audit.Entry{Action: action, EntityType: "market_check_settings",
		EntityID: uuid.Nil, Changes: map[string]interface{}{"fields": fields}})
}
