// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound reports that a rule or preset read named a row that is not
// there; the handler answers 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "configurator row not found" }

// Repository reads the rule matrix and the presets. The configurator has no
// writes: rules and presets are seeded master data, so the module owns no
// mutation, no audit row and no event.
type Repository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *Repository {
	return &Repository{db: db}
}

const ruleColumns = `id, attribute_type, attribute_value, depends_on_type, depends_on_value,
	is_allowed, error_message, created_at, updated_at`

func scanRule(row pgx.Row) (*Rule, error) {
	var (
		r                Rule
		created, updated time.Time
	)
	if err := row.Scan(&r.ID, &r.AttributeType, &r.AttributeValue, &r.DependsOnType,
		&r.DependsOnValue, &r.IsAllowed, &r.ErrorMessage, &created, &updated); err != nil {
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &r, nil
}

// AllRules returns every rule, deterministically ordered.
func (r *Repository) AllRules(ctx context.Context) ([]Rule, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+ruleColumns+` FROM configurator_rules
		ORDER BY depends_on_type, depends_on_value, attribute_type, attribute_value`)
	if err != nil {
		return nil, fmt.Errorf("failed to query rules: %w", err)
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan rule: %w", err)
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

// RulesByDependency returns the rules that depend on one attribute pair.
func (r *Repository) RulesByDependency(ctx context.Context, dependsOnType, dependsOnValue string) ([]Rule, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+ruleColumns+` FROM configurator_rules
		WHERE depends_on_type = $1 AND depends_on_value = $2
		ORDER BY attribute_type, attribute_value`, dependsOnType, dependsOnValue)
	if err != nil {
		return nil, fmt.Errorf("failed to query rules by dependency: %w", err)
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan rule: %w", err)
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

// AllowedValues returns the rules constraining one attribute against one
// parent pair, ordered by value.
func (r *Repository) AllowedValues(ctx context.Context, attributeType, dependsOnType, dependsOnValue string) ([]Rule, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+ruleColumns+` FROM configurator_rules
		WHERE attribute_type = $1 AND depends_on_type = $2 AND depends_on_value = $3
		ORDER BY attribute_value`, attributeType, dependsOnType, dependsOnValue)
	if err != nil {
		return nil, fmt.Errorf("failed to query allowed values: %w", err)
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan rule: %w", err)
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

const presetColumns = `id, name, description, product_type, config, is_active, created_at, updated_at`

func scanPreset(row pgx.Row) (*Preset, error) {
	var (
		p                Preset
		created, updated time.Time
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.ProductType,
		&p.Config, &p.IsActive, &created, &updated); err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &p, nil
}

// Presets returns the active presets, optionally filtered by product type
// (the filter filters; an empty product type is the whole master).
func (r *Repository) Presets(ctx context.Context, productType string) ([]Preset, error) {
	query := `SELECT ` + presetColumns + ` FROM configurator_presets WHERE is_active = true`
	args := []any{}
	if productType != "" {
		args = append(args, productType)
		query += fmt.Sprintf(` AND product_type = $%d`, len(args))
	}
	query += ` ORDER BY name`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query presets: %w", err)
	}
	defer rows.Close()
	out := []Preset{}
	for rows.Next() {
		p, err := scanPreset(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan preset: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// sortedSelectionKeys gives map iteration a deterministic order, so the same
// selections always validate and build the same answer.
func sortedSelectionKeys(selections map[string]string) []string {
	keys := make([]string, 0, len(selections))
	for k := range selections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
