// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is a write's answer for a row that is not there.
var ErrNotFound = fmt.Errorf("not found")

// ErrStaleRevision is a write's answer when the row moved past the revision
// the caller built on (ADR 0001 section 11).
var ErrStaleRevision = fmt.Errorf("stale revision")

// CategoryRepository defines database operations for category pricing.
type CategoryRepository interface {
	// Categories
	ListCategories(ctx context.Context) ([]ProductCategory, error)
	GetCategory(ctx context.Context, id uuid.UUID) (*ProductCategory, error)
	CreateCategory(ctx context.Context, c *ProductCategory) error
	UpdateCategory(ctx context.Context, c *ProductCategory) error

	// Category Pricing Rules
	CreateCategoryRule(ctx context.Context, r *CategoryPricingRule) error
	UpdateCategoryRule(ctx context.Context, r *CategoryPricingRule, revision int64) error
	DeleteCategoryRule(ctx context.Context, id uuid.UUID) error
	GetCategoryRule(ctx context.Context, id uuid.UUID) (*CategoryPricingRule, error)
	ListCategoryRules(ctx context.Context, filter CategoryRuleFilter) ([]CategoryPricingRule, error)

	// Resolution: 5-step algorithm queries
	ResolveAccountExact(ctx context.Context, customerID uuid.UUID, categoryID uuid.UUID) (*CategoryPricingRule, error)
	ResolveAccountAncestor(ctx context.Context, customerID uuid.UUID, categoryPath string) (*CategoryPricingRule, error)
	ResolveTierExact(ctx context.Context, tier string, categoryID uuid.UUID) (*CategoryPricingRule, error)
	ResolveTierAncestor(ctx context.Context, tier string, categoryPath string) (*CategoryPricingRule, error)

	// Matrix view (batch for admin UI)
	GetMatrixRules(ctx context.Context) ([]CategoryPricingRule, error)

	// Product category lookup (returns categoryID, categoryPath, the scale 4 cost)
	GetProductCategoryPath(ctx context.Context, productID uuid.UUID) (uuid.UUID, string, httpx.Price, error)

	// Audit trail
	CreateAuditEntry(ctx context.Context, entry *CategoryPricingAudit) error
	ListAuditEntries(ctx context.Context, ruleID uuid.UUID) ([]CategoryPricingAudit, error)

	// Bulk operations
	BulkUpsertRules(ctx context.Context, rules []CategoryPricingRule) error
	BulkDeleteRules(ctx context.Context, ids []uuid.UUID) error

	// Pagination
	ListCategoryRulesPage(ctx context.Context, filter CategoryRuleFilter, after *time.Time, afterID *uuid.UUID, limit int) ([]CategoryPricingRule, error)
	CountCategoryRules(ctx context.Context, filter CategoryRuleFilter) (int64, error)
}

// PostgresCategoryRepository implements CategoryRepository using pgx.
type PostgresCategoryRepository struct {
	db *database.DB
}

// NewCategoryRepository creates a new PostgresCategoryRepository.
func NewCategoryRepository(db *database.DB) *PostgresCategoryRepository {
	return &PostgresCategoryRepository{db: db}
}

// --- Categories ---

func (r *PostgresCategoryRepository) ListCategories(ctx context.Context) ([]ProductCategory, error) {
	query := `
		SELECT id, name, slug, path::text, parent_id, sort_order, is_active, created_at, updated_at
		FROM product_categories
		WHERE is_active = true
		ORDER BY path ASC, sort_order ASC`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list categories: %w", err)
	}
	defer rows.Close()

	var cats []ProductCategory
	for rows.Next() {
		var c ProductCategory
		var created, updated time.Time
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Path, &c.ParentID, &c.SortOrder, &c.IsActive, &created, &updated); err != nil {
			return nil, fmt.Errorf("failed to scan category: %w", err)
		}
		c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
		cats = append(cats, c)
	}
	return cats, nil
}

func (r *PostgresCategoryRepository) GetCategory(ctx context.Context, id uuid.UUID) (*ProductCategory, error) {
	query := `
		SELECT id, name, slug, path::text, parent_id, sort_order, is_active, created_at, updated_at
		FROM product_categories
		WHERE id = $1`

	var c ProductCategory
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, id).Scan(
		&c.ID, &c.Name, &c.Slug, &c.Path, &c.ParentID, &c.SortOrder, &c.IsActive, &created, &updated,
	)
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get category: %w", err)
	}
	return &c, nil
}

func (r *PostgresCategoryRepository) CreateCategory(ctx context.Context, c *ProductCategory) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	now := time.Now()
	c.CreatedAt = httpx.TimestampOf(now)
	c.UpdatedAt = httpx.TimestampOf(now)

	query := `
		INSERT INTO product_categories (id, name, slug, path, parent_id, sort_order, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4::ltree, $5, $6, $7, $8, $9)`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		c.ID, c.Name, c.Slug, c.Path, c.ParentID, c.SortOrder, c.IsActive, c.CreatedAt.Time, c.UpdatedAt.Time,
	)
	if err != nil {
		return fmt.Errorf("failed to create category: %w", err)
	}
	return nil
}

func (r *PostgresCategoryRepository) UpdateCategory(ctx context.Context, c *ProductCategory) error {
	c.UpdatedAt = httpx.TimestampOf(time.Now())

	query := `
		UPDATE product_categories
		SET name = $2, slug = $3, sort_order = $4, is_active = $5, updated_at = $6
		WHERE id = $1`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		c.ID, c.Name, c.Slug, c.SortOrder, c.IsActive, c.UpdatedAt.Time,
	)
	if err != nil {
		return fmt.Errorf("failed to update category: %w", err)
	}
	return nil
}

// --- Category Pricing Rules ---

func (r *PostgresCategoryRepository) CreateCategoryRule(ctx context.Context, rule *CategoryPricingRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	now := time.Now()
	rule.CreatedAt = httpx.TimestampOf(now)
	rule.UpdatedAt = httpx.TimestampOf(now)
	if rule.Revision == 0 {
		rule.Revision = 1
	}

	query := `
		INSERT INTO category_pricing_rules
			(id, target_type, customer_id, tier, category_id, rule_type, rule_value,
			 margin_floor_pct, starts_at, expires_at, is_active, priority, created_by, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10, $11, $12, $13, $14, $15, $16)`

	args, err := categoryRuleWriteArgs(rule)
	if err != nil {
		return err
	}
	_, err = r.db.GetExecutor(ctx).Exec(ctx, query,
		append(args[:13], rule.Revision, rule.CreatedAt.Time, rule.UpdatedAt.Time)...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("an active rule already exists for this target and category")
		}
		return fmt.Errorf("failed to create category rule: %w", err)
	}
	return nil
}

func (r *PostgresCategoryRepository) UpdateCategoryRule(ctx context.Context, rule *CategoryPricingRule, revision int64) error {
	rule.UpdatedAt = httpx.TimestampOf(time.Now())

	query := `
		UPDATE category_pricing_rules
		SET rule_type = $2, rule_value = $3::numeric, margin_floor_pct = $4::numeric,
		    starts_at = $5, expires_at = $6, is_active = $7, priority = $8,
		    revision = revision + 1, updated_at = $9
		WHERE id = $1 AND revision = $10`

	args, err := categoryRuleWriteArgs(rule)
	if err != nil {
		return err
	}
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		rule.ID, args[5], args[6], args[7], args[8], args[9], args[10], args[11], rule.UpdatedAt.Time, revision,
	)
	if err != nil {
		return fmt.Errorf("failed to update category rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return staleOrMissingRule(ctx, r, rule.ID)
	}
	rule.Revision = revision + 1
	return nil
}

// staleOrMissingRule tells a missing rule from a stale revision for the write
// the caller just attempted.
func staleOrMissingRule(ctx context.Context, r *PostgresCategoryRepository, id uuid.UUID) error {
	var exists bool
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM category_pricing_rules WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
		return ErrStaleRevision
	}
	return ErrNotFound
}

func (r *PostgresCategoryRepository) DeleteCategoryRule(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM category_pricing_rules WHERE id = $1`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete category rule: %w", err)
	}
	return nil
}

func (r *PostgresCategoryRepository) GetCategoryRule(ctx context.Context, id uuid.UUID) (*CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.id = $1`

	raw, err := scanRawRule(r.db.GetExecutor(ctx).QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("failed to get category rule: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	var rule CategoryPricingRule
	if err := rule.fromRaw(raw); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *PostgresCategoryRepository) ListCategoryRules(ctx context.Context, filter CategoryRuleFilter) ([]CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE 1=1`

	var args []any
	argIdx := 1

	if filter.TargetType != nil {
		query += fmt.Sprintf(" AND cpr.target_type = $%d", argIdx)
		args = append(args, *filter.TargetType)
		argIdx++
	}
	if filter.Tier != "" {
		query += fmt.Sprintf(" AND cpr.tier = $%d", argIdx)
		args = append(args, filter.Tier)
		argIdx++
	}
	if filter.CustomerID != nil {
		query += fmt.Sprintf(" AND cpr.customer_id = $%d", argIdx)
		args = append(args, *filter.CustomerID)
		argIdx++
	}
	if filter.CategoryID != nil {
		query += fmt.Sprintf(" AND cpr.category_id = $%d", argIdx)
		args = append(args, *filter.CategoryID)
		argIdx++
	}
	if filter.IsActive != nil {
		query += fmt.Sprintf(" AND cpr.is_active = $%d", argIdx)
		args = append(args, *filter.IsActive)
		argIdx++
	}

	query += " ORDER BY cpr.target_type ASC, cpr.tier ASC, pc.path ASC, cpr.priority DESC"

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list category rules: %w", err)
	}
	defer rows.Close()

	return scanCategoryRules(rows)
}

// --- Resolution Queries (5-step algorithm) ---

func (r *PostgresCategoryRepository) ResolveAccountExact(ctx context.Context, customerID uuid.UUID, categoryID uuid.UUID) (*CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.target_type = 'ACCOUNT'
		  AND cpr.customer_id = $1
		  AND cpr.category_id = $2
		  AND cpr.is_active = true
		  AND (cpr.starts_at IS NULL OR cpr.starts_at <= NOW())
		  AND (cpr.expires_at IS NULL OR cpr.expires_at > NOW())
		ORDER BY cpr.priority DESC
		LIMIT 1`

	return r.scanSingleRule(ctx, query, customerID, categoryID)
}

func (r *PostgresCategoryRepository) ResolveAccountAncestor(ctx context.Context, customerID uuid.UUID, categoryPath string) (*CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.target_type = 'ACCOUNT'
		  AND cpr.customer_id = $1
		  AND cpr.is_active = true
		  AND pc.path <@ $2::ltree
		  AND (cpr.starts_at IS NULL OR cpr.starts_at <= NOW())
		  AND (cpr.expires_at IS NULL OR cpr.expires_at > NOW())
		ORDER BY nlevel(pc.path) DESC, cpr.priority DESC
		LIMIT 1`

	return r.scanSingleRule(ctx, query, customerID, categoryPath)
}

func (r *PostgresCategoryRepository) ResolveTierExact(ctx context.Context, tier string, categoryID uuid.UUID) (*CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.target_type = 'TIER'
		  AND cpr.tier = $1
		  AND cpr.category_id = $2
		  AND cpr.is_active = true
		  AND (cpr.starts_at IS NULL OR cpr.starts_at <= NOW())
		  AND (cpr.expires_at IS NULL OR cpr.expires_at > NOW())
		ORDER BY cpr.priority DESC
		LIMIT 1`

	return r.scanSingleRule(ctx, query, tier, categoryID)
}

func (r *PostgresCategoryRepository) ResolveTierAncestor(ctx context.Context, tier string, categoryPath string) (*CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.target_type = 'TIER'
		  AND cpr.tier = $1
		  AND cpr.is_active = true
		  AND pc.path <@ $2::ltree
		  AND (cpr.starts_at IS NULL OR cpr.starts_at <= NOW())
		  AND (cpr.expires_at IS NULL OR cpr.expires_at > NOW())
		ORDER BY nlevel(pc.path) DESC, cpr.priority DESC
		LIMIT 1`

	return r.scanSingleRule(ctx, query, tier, categoryPath)
}

// --- Matrix (admin UI) ---

func (r *PostgresCategoryRepository) GetMatrixRules(ctx context.Context) ([]CategoryPricingRule, error) {
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id
		WHERE cpr.is_active = true
		  AND cpr.target_type = 'TIER'
		  AND (cpr.starts_at IS NULL OR cpr.starts_at <= NOW())
		  AND (cpr.expires_at IS NULL OR cpr.expires_at > NOW())
		ORDER BY pc.path ASC, cpr.tier ASC`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get matrix rules: %w", err)
	}
	defer rows.Close()

	return scanCategoryRules(rows)
}

// --- Product Category Lookup ---

func (r *PostgresCategoryRepository) GetProductCategoryPath(ctx context.Context, productID uuid.UUID) (uuid.UUID, string, httpx.Price, error) {
	query := `
		SELECT pc.id, pc.path::text, COALESCE(p.average_unit_cost, 0)::text
		FROM products p
		JOIN product_categories pc ON pc.id = p.category_id
		WHERE p.id = $1`

	var categoryID uuid.UUID
	var path, costText string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, productID).Scan(&categoryID, &path, &costText)
	if err != nil {
		if err == pgx.ErrNoRows {
			return uuid.Nil, "", 0, fmt.Errorf("product %s has no category", productID)
		}
		return uuid.Nil, "", 0, fmt.Errorf("failed to get product category: %w", err)
	}
	var cost httpx.Price
	if err := scanPrice(&cost, costText); err != nil {
		return uuid.Nil, "", 0, fmt.Errorf("failed to read average unit cost: %w", err)
	}
	return categoryID, path, cost, nil
}

// --- Helpers ---

func (r *PostgresCategoryRepository) scanSingleRule(ctx context.Context, query string, args ...any) (*CategoryPricingRule, error) {
	var rule CategoryPricingRule
	raw, err := scanRawRule(r.db.GetExecutor(ctx).QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to resolve category rule: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	if err := rule.fromRaw(raw); err != nil {
		return nil, err
	}
	return &rule, nil
}

func scanCategoryRules(rows pgx.Rows) ([]CategoryPricingRule, error) {
	var rules []CategoryPricingRule
	for rows.Next() {
		raw, err := scanRawRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan category rule: %w", err)
		}
		var rule CategoryPricingRule
		if err := rule.fromRaw(raw); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// rawCategoryRule holds one scanned row before its scaled columns are
// parsed, so the row scan and the exact parsing stay separate steps.
type rawCategoryRule struct {
	id, categoryID                                  uuid.UUID
	targetType                                      TargetType
	customerID                                      *uuid.UUID
	tier                                            *string
	ruleType                                        CategoryRuleType
	value, floor                                    *string
	startsAt, expiresAt                             *time.Time
	createdAt, updatedAt                            time.Time
	isActive                                        bool
	priority                                        int
	createdBy                                       string
	revision                                        int64
	categoryName, categoryPath                      string
}

// scanRawRule scans the shared column list into a raw row; nil means no row.
func scanRawRule(scanner interface{ Scan(dest ...any) error }) (*rawCategoryRule, error) {
	var r rawCategoryRule
	err := scanner.Scan(
		&r.id, &r.targetType, &r.customerID, &r.tier, &r.categoryID,
		&r.ruleType, &r.value, &r.floor,
		&r.startsAt, &r.expiresAt, &r.isActive, &r.priority,
		&r.createdBy, &r.revision, &r.createdAt, &r.updatedAt,
		&r.categoryName, &r.categoryPath,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// fromRaw parses the raw row's exact fields: the one rule_value column into
// the reading its type gives it (a scale 4 price for FIXED, a percentage
// otherwise), the margin floor, the timestamps and the tier.
func (rule *CategoryPricingRule) fromRaw(r *rawCategoryRule) error {
	rule.ID, rule.TargetType, rule.CustomerID = r.id, r.targetType, r.customerID
	rule.CategoryID, rule.RuleType = r.categoryID, r.ruleType
	rule.IsActive, rule.Priority, rule.CreatedBy, rule.Revision = r.isActive, r.priority, r.createdBy, r.revision
	rule.CategoryName, rule.CategoryPath = r.categoryName, r.categoryPath
	if r.tier != nil {
		rule.Tier = *r.tier
	}
	if r.value != nil {
		v, err := httpx.ParseQuantity(*r.value)
		if err != nil {
			return fmt.Errorf("failed to read rule value: %w", err)
		}
		if r.ruleType == CategoryRuleFixed {
			p := httpx.Price(v)
			rule.ValuePrice = &p
		} else {
			rule.ValuePct = &v
		}
	}
	if r.floor != nil {
		f, err := httpx.ParseQuantity(*r.floor)
		if err != nil {
			return fmt.Errorf("failed to read margin floor: %w", err)
		}
		rule.MarginFloorPct = &f
	}
	rule.StartsAt = httpx.PtrTimestamp(r.startsAt)
	rule.ExpiresAt = httpx.PtrTimestamp(r.expiresAt)
	rule.CreatedAt = httpx.TimestampOf(r.createdAt)
	rule.UpdatedAt = httpx.TimestampOf(r.updatedAt)
	return nil
}

// categoryRuleValueArg is the rule's value as the decimal string the
// rule_value column takes, whichever reading the rule's type gives it.
func categoryRuleValueArg(rule *CategoryPricingRule) (any, error) {
	if rule.ValuePrice != nil && rule.ValuePct != nil {
		return nil, fmt.Errorf("a rule carries either value_ten_thousandths or value_pct, never both")
	}
	if rule.ValuePrice != nil {
		return rule.ValuePrice.DecimalString(), nil
	}
	if rule.ValuePct != nil {
		return rule.ValuePct.DecimalString(), nil
	}
	return nil, fmt.Errorf("a rule carries no value")
}

// categoryRuleWriteArgs collects the write arguments the rule inserts and
// updates share, so the two can never drift.
func categoryRuleWriteArgs(rule *CategoryPricingRule) ([]any, error) {
	value, err := categoryRuleValueArg(rule)
	if err != nil {
		return nil, err
	}
	return []any{
		rule.ID, rule.TargetType, rule.CustomerID, nilIfEmpty(rule.Tier), rule.CategoryID,
		rule.RuleType, value, quantityString(rule.MarginFloorPct),
		timeOf(rule.StartsAt), timeOf(rule.ExpiresAt), rule.IsActive, rule.Priority,
		rule.CreatedBy, rule.CreatedAt.Time, rule.UpdatedAt.Time,
	}, nil
}

func nilIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// --- Audit Trail ---

func (r *PostgresCategoryRepository) CreateAuditEntry(ctx context.Context, entry *CategoryPricingAudit) error {
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.PerformedAt.IsZero() {
		entry.PerformedAt = httpx.TimestampOf(time.Now())
	}

	oldJSON, _ := json.Marshal(entry.OldValues)
	newJSON, _ := json.Marshal(entry.NewValues)

	query := `
		INSERT INTO category_pricing_audit
			(id, rule_id, action, old_values, new_values, performed_by, performed_at,
			 category_id, target_type, tier, customer_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		entry.ID, entry.RuleID, entry.Action,
		oldJSON, newJSON,
		entry.PerformedBy, entry.PerformedAt.Time,
		entry.CategoryID, nilIfEmpty(entry.TargetType), nilIfEmpty(entry.Tier), entry.CustomerID,
	)
	if err != nil {
		return fmt.Errorf("failed to create audit entry: %w", err)
	}
	return nil
}

func (r *PostgresCategoryRepository) ListAuditEntries(ctx context.Context, ruleID uuid.UUID) ([]CategoryPricingAudit, error) {
	query := `
		SELECT id, rule_id, action, old_values, new_values, performed_by, performed_at,
		       category_id, target_type, tier, customer_id
		FROM category_pricing_audit
		WHERE rule_id = $1
		ORDER BY performed_at DESC
		LIMIT 50`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to list audit entries: %w", err)
	}
	defer rows.Close()

	var entries []CategoryPricingAudit
	for rows.Next() {
		var e CategoryPricingAudit
		var oldJSON, newJSON []byte
		var targetType, tier *string
		var performedAt time.Time
		if err := rows.Scan(
			&e.ID, &e.RuleID, &e.Action, &oldJSON, &newJSON,
			&e.PerformedBy, &performedAt,
			&e.CategoryID, &targetType, &tier, &e.CustomerID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan audit entry: %w", err)
		}
		e.PerformedAt = httpx.TimestampOf(performedAt)
		if oldJSON != nil {
			_ = json.Unmarshal(oldJSON, &e.OldValues)
		}
		if newJSON != nil {
			_ = json.Unmarshal(newJSON, &e.NewValues)
		}
		if targetType != nil {
			e.TargetType = *targetType
		}
		if tier != nil {
			e.Tier = *tier
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// --- Bulk Operations ---

func (r *PostgresCategoryRepository) BulkUpsertRules(ctx context.Context, rules []CategoryPricingRule) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for i := range rules {
		rule := &rules[i]
		if rule.ID == uuid.Nil {
			rule.ID = uuid.New()
		}
		now := time.Now()
		rule.UpdatedAt = httpx.TimestampOf(now)

		query := `
			INSERT INTO category_pricing_rules
				(id, target_type, customer_id, tier, category_id, rule_type, rule_value,
				 margin_floor_pct, starts_at, expires_at, is_active, priority, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (id) DO UPDATE SET
				rule_type = EXCLUDED.rule_type,
				rule_value = EXCLUDED.rule_value,
				margin_floor_pct = EXCLUDED.margin_floor_pct,
				starts_at = EXCLUDED.starts_at,
				expires_at = EXCLUDED.expires_at,
				is_active = EXCLUDED.is_active,
				priority = EXCLUDED.priority,
				revision = category_pricing_rules.revision + 1,
				updated_at = EXCLUDED.updated_at`

		if rule.CreatedAt.IsZero() {
			rule.CreatedAt = httpx.TimestampOf(now)
		}

		args, err := categoryRuleWriteArgs(rule)
		if err != nil {
			return fmt.Errorf("bulk upsert rule %s: %w", rule.ID, err)
		}
		_, err = tx.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("bulk upsert rule %s: %w", rule.ID, err)
		}
	}

	return tx.Commit(ctx)
}

func (r *PostgresCategoryRepository) BulkDeleteRules(ctx context.Context, ids []uuid.UUID) error {
	query := `DELETE FROM category_pricing_rules WHERE id = ANY($1)`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, ids)
	if err != nil {
		return fmt.Errorf("bulk delete rules: %w", err)
	}
	return nil
}

// --- Keyset Page ---

// categoryRuleWhere builds the filters' shared predicate and arguments.
func categoryRuleWhere(filter CategoryRuleFilter) (string, []any) {
	where := " WHERE 1=1"
	var args []any
	if filter.TargetType != nil {
		where += fmt.Sprintf(" AND cpr.target_type = $%d", len(args)+1)
		args = append(args, *filter.TargetType)
	}
	if filter.Tier != "" {
		where += fmt.Sprintf(" AND cpr.tier = $%d", len(args)+1)
		args = append(args, filter.Tier)
	}
	if filter.CustomerID != nil {
		where += fmt.Sprintf(" AND cpr.customer_id = $%d", len(args)+1)
		args = append(args, *filter.CustomerID)
	}
	if filter.CategoryID != nil {
		where += fmt.Sprintf(" AND cpr.category_id = $%d", len(args)+1)
		args = append(args, *filter.CategoryID)
	}
	if filter.IsActive != nil {
		where += fmt.Sprintf(" AND cpr.is_active = $%d", len(args)+1)
		args = append(args, *filter.IsActive)
	}
	return where, args
}

// ListCategoryRulesPage is the category rules list's keyset page:
// `created_at DESC, id DESC`, the ordering migration 093 indexed.
func (r *PostgresCategoryRepository) ListCategoryRulesPage(ctx context.Context, filter CategoryRuleFilter, after *time.Time, afterID *uuid.UUID, limit int) ([]CategoryPricingRule, error) {
	where, args := categoryRuleWhere(filter)
	if after != nil {
		where += fmt.Sprintf(" AND (cpr.created_at, cpr.id) < ($%d, $%d)", len(args)+1, len(args)+2)
		args = append(args, *after, *afterID)
	}
	query := `
		SELECT cpr.id, cpr.target_type, cpr.customer_id, cpr.tier, cpr.category_id,
		       cpr.rule_type, cpr.rule_value::text, cpr.margin_floor_pct::text,
		       cpr.starts_at, cpr.expires_at, cpr.is_active, cpr.priority,
		       cpr.created_by, cpr.revision, cpr.created_at, cpr.updated_at,
		       pc.name, pc.path::text
		FROM category_pricing_rules cpr
		JOIN product_categories pc ON pc.id = cpr.category_id` +
		where +
		" ORDER BY cpr.created_at DESC, cpr.id DESC" +
		fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list category rules: %w", err)
	}
	return scanCategoryRules(rows)
}

// CountCategoryRules is the include=total count under the same filters.
func (r *PostgresCategoryRepository) CountCategoryRules(ctx context.Context, filter CategoryRuleFilter) (int64, error) {
	where, args := categoryRuleWhere(filter)
	query := `SELECT COUNT(*) FROM category_pricing_rules cpr` + where
	var total int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count category rules: %w", err)
	}
	return total, nil
}
