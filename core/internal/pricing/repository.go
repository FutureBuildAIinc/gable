// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository interface {
	GetContract(ctx context.Context, customerID, productID uuid.UUID) (*CustomerContract, error)
	CreateContract(ctx context.Context, c *CustomerContract) error
	GetMatchingRules(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID, jobID *uuid.UUID, quantity httpx.Quantity) ([]PricingRule, error)
	ListBreakQuantities(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID) ([]httpx.Quantity, error)
	CreateRule(ctx context.Context, r *PricingRule) error
	ListRules(ctx context.Context) ([]PricingRule, error)
	ListRulesPage(ctx context.Context, after *RuleCursor, limit int) ([]PricingRule, error)
	CountRules(ctx context.Context) (int64, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// scanPrice reads a NUMERIC(12,4) column as its exact scale 4 value; the
// scan never goes through float64 (ADR 0001 section 7).
func scanPrice(dest *httpx.Price, s string) error {
	p, err := httpx.ParsePrice(s)
	if err != nil {
		return err
	}
	*dest = p
	return nil
}

func (r *PostgresRepository) GetContract(ctx context.Context, customerID, productID uuid.UUID) (*CustomerContract, error) {
	query := `
		SELECT id, customer_id, product_id, contract_price::text, created_at, updated_at
		FROM customer_contracts
		WHERE customer_id = $1 AND product_id = $2`

	var c CustomerContract
	var price string
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, customerID, productID).Scan(
		&c.ID, &c.CustomerID, &c.ProductID, &price, &created, &updated,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // No contract found
		}
		return nil, fmt.Errorf("failed to get contract: %w", err)
	}
	if err := scanPrice(&c.ContractPrice, price); err != nil {
		return nil, fmt.Errorf("failed to read contract price: %w", err)
	}
	c.CreatedAt = httpx.TimestampOf(created)
	c.UpdatedAt = httpx.TimestampOf(updated)
	return &c, nil
}

func (r *PostgresRepository) CreateContract(ctx context.Context, c *CustomerContract) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	now := time.Now()
	c.CreatedAt = httpx.TimestampOf(now)
	c.UpdatedAt = httpx.TimestampOf(now)

	query := `
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, created_at, updated_at)
		VALUES ($1, $2, $3, $4::numeric, $5, $6)
		ON CONFLICT (customer_id, product_id) DO UPDATE
		SET contract_price = EXCLUDED.contract_price, updated_at = EXCLUDED.updated_at`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		c.ID, c.CustomerID, c.ProductID, c.ContractPrice.DecimalString(), c.CreatedAt.Time, c.UpdatedAt.Time,
	)
	if err != nil {
		return fmt.Errorf("failed to create contract: %w", err)
	}
	return nil
}

// categoryScopePredicate is the SQL that makes pricing_rules.category an actual
// scope instead of a decorative label. It takes the product id as $1 and is
// shared verbatim by GetMatchingRules and ListBreakQuantities so the two can
// never drift — ListBreakQuantities' contract is that its candidates are
// exactly the rules the waterfall could reach.
//
// A rule with no category is unscoped and matches everything, as before. A rule
// WITH a category matches on either of two readings, and it needs both:
//
//   - the flat products.category display string, matched case- and
//     whitespace-insensitively. This is the taxonomy the catalog actually
//     shows, and three of the demo catalog's categories ('Cornice', 'Millwork',
//     'Sheet Goods') still have no node in the tree — migration 085 explains
//     why a migration must not invent them. Dropping this arm would silently
//     switch off the seeded "Sheet Goods Qty Break 50+" rule.
//
//   - the tree: the name or slug of the product's own product_categories node
//     OR OF ANY ANCESTOR of it, via `pc.path <@ anc.path`. This is what ltree
//     is for and it is the reading that scales: a rule the dealer writes
//     against 'Lumber' has to reach a product filed under 'lumber.framing'
//     without the dealer restating the rule once per leaf.
//
// The two arms are OR'd rather than one replacing the other because
// pricing_rules.category is a STRING and products now carry BOTH a string and a
// tree link. Matching only the tree would break rules written against
// un-noded categories; matching only the string would make a parent-category
// rule useless, which is the whole reason the tree exists. Either arm hitting
// is enough, and a rule whose category matches nothing at all reaches nothing —
// scope fails closed, so a typo costs a discount rather than giving one away
// catalog-wide.
const categoryScopePredicate = `(
			pricing_rules.category IS NULL
			OR TRIM(pricing_rules.category) = ''
			OR EXISTS (
				SELECT 1
				FROM products p
				LEFT JOIN product_categories pc ON pc.id = p.category_id
				WHERE p.id = $1
				  AND (
					LOWER(TRIM(p.category)) = LOWER(TRIM(pricing_rules.category))
					OR EXISTS (
						SELECT 1
						FROM product_categories anc
						WHERE pc.path <@ anc.path
						  AND (
							LOWER(anc.name) = LOWER(TRIM(pricing_rules.category))
							OR LOWER(anc.slug) = LOWER(TRIM(pricing_rules.category))
						  )
					)
				  )
			)
		)`

// ruleColumns reads every column the PricingRule wire type carries, with the
// scaled columns read as text so the scan is exact.
const ruleColumns = `id, name, rule_type, product_id, customer_id, job_id, COALESCE(category, ''),
			fixed_price::text, discount_pct::text, markup_pct::text,
			min_quantity::text, max_quantity::text, margin_floor_pct::text,
			starts_at, expires_at, is_active, priority, revision, created_at, updated_at`

func scanRule(scanner interface{ Scan(dest ...any) error }) (PricingRule, error) {
	var rule PricingRule
	var fixed, discount, markup, minQty, maxQty, floor *string
	var starts, expires *time.Time
	var created, updated time.Time
	if err := scanner.Scan(
		&rule.ID, &rule.Name, &rule.RuleType, &rule.ProductID, &rule.CustomerID, &rule.JobID, &rule.Category,
		&fixed, &discount, &markup, &minQty, &maxQty, &floor,
		&starts, &expires, &rule.IsActive, &rule.Priority, &rule.Revision, &created, &updated,
	); err != nil {
		return PricingRule{}, err
	}
	var err error
	if fixed != nil {
		rule.FixedPrice = new(httpx.Price)
		err = scanPrice(rule.FixedPrice, *fixed)
	}
	if err == nil && discount != nil {
		rule.DiscountPct = new(httpx.Quantity)
		*rule.DiscountPct, err = httpx.ParseQuantity(*discount)
	}
	if err == nil && markup != nil {
		rule.MarkupPct = new(httpx.Quantity)
		*rule.MarkupPct, err = httpx.ParseQuantity(*markup)
	}
	if err == nil {
		var q httpx.Quantity
		if minQty != nil {
			q, err = httpx.ParseQuantity(*minQty)
		}
		rule.MinQuantity = q
	}
	if err == nil && maxQty != nil {
		rule.MaxQuantity = new(httpx.Quantity)
		*rule.MaxQuantity, err = httpx.ParseQuantity(*maxQty)
	}
	if err == nil && floor != nil {
		rule.MarginFloorPct = new(httpx.Quantity)
		*rule.MarginFloorPct, err = httpx.ParseQuantity(*floor)
	}
	if err != nil {
		return PricingRule{}, fmt.Errorf("failed to read pricing rule columns: %w", err)
	}
	rule.StartsAt = httpx.PtrTimestamp(starts)
	rule.ExpiresAt = httpx.PtrTimestamp(expires)
	rule.CreatedAt = httpx.TimestampOf(created)
	rule.UpdatedAt = httpx.TimestampOf(updated)
	return rule, nil
}

func (r *PostgresRepository) GetMatchingRules(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID, jobID *uuid.UUID, quantity httpx.Quantity) ([]PricingRule, error) {
	// COALESCE on category: the column is nullable but PricingRule.Category is
	// a plain string, and pgx refuses to scan NULL into one. Without this, a
	// single rule row with a NULL category makes the whole query fail — and
	// CalculatePriceWithQty swallows that error and falls through to tier
	// pricing, so ONE such row silently switches off every pricing rule in the
	// system for every product. NULL and '' both mean "not category-scoped",
	// which is what the model already treats "" as.
	query := `
		SELECT ` + ruleColumns + `
		FROM pricing_rules
		WHERE is_active = true
			AND (product_id IS NULL OR product_id = $1)
			AND (customer_id IS NULL OR customer_id = $2)
			AND (job_id IS NULL OR job_id = $3)
			AND min_quantity <= $4::numeric
			AND (max_quantity IS NULL OR max_quantity >= $4::numeric)
			AND (starts_at IS NULL OR starts_at <= NOW())
			AND (expires_at IS NULL OR expires_at > NOW())
			AND ` + categoryScopePredicate + `
		ORDER BY priority DESC, rule_type ASC
	`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, productID, customerID, jobID, quantity.DecimalString())
	if err != nil {
		return nil, fmt.Errorf("failed to get matching rules: %w", err)
	}
	defer rows.Close()

	var rules []PricingRule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pricing rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// ListBreakQuantities returns the distinct quantity thresholds at which a
// QUANTITY_BREAK rule could apply to this (product, customer) pair.
//
// The scope predicate is deliberately identical to GetMatchingRules' —
// same product/customer NULL-or-equal matching, same category scope (it shares
// categoryScopePredicate literally), same active/date window — so the
// candidates this returns are exactly the rules the waterfall could reach.
// The two differences are intentional:
//
//   - No quantity band. That is the point: we are asking "where does the band
//     start", not "which band contains q".
//   - job_id IS NULL only. A catalog break ladder is not job-scoped, and
//     letting a caller pass a job id here would let it probe job pricing.
//
// DISTINCT collapses the duplicate rule rows the seeder's `ON CONFLICT DO
// NOTHING` leaves behind on a table with no unique constraint (see the note in
// cmd/seed/main.go's pricing-rules block); a contractor should see one rung per
// threshold, not fifty.
func (r *PostgresRepository) ListBreakQuantities(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID) ([]httpx.Quantity, error) {
	query := `
		SELECT DISTINCT min_quantity::text
		FROM pricing_rules
		WHERE is_active = true
			AND rule_type = 'QUANTITY_BREAK'
			AND min_quantity > 1
			AND (product_id IS NULL OR product_id = $1)
			AND (customer_id IS NULL OR customer_id = $2)
			AND job_id IS NULL
			AND (starts_at IS NULL OR starts_at <= NOW())
			AND (expires_at IS NULL OR expires_at > NOW())
			AND ` + categoryScopePredicate + `
		ORDER BY 1 ASC
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, productID, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list break quantities: %w", err)
	}
	defer rows.Close()

	quantities := make([]httpx.Quantity, 0)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("failed to scan break quantity: %w", err)
		}
		q, err := httpx.ParseQuantity(s)
		if err != nil {
			return nil, fmt.Errorf("failed to read break quantity: %w", err)
		}
		quantities = append(quantities, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("break quantity rows error: %w", err)
	}
	return quantities, nil
}

func (r *PostgresRepository) CreateRule(ctx context.Context, rule *PricingRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	now := time.Now()
	rule.CreatedAt = httpx.TimestampOf(now)
	rule.UpdatedAt = httpx.TimestampOf(now)

	query := `
		INSERT INTO pricing_rules (id, name, rule_type, product_id, customer_id, job_id, category,
			fixed_price, discount_pct, markup_pct, min_quantity, max_quantity,
			margin_floor_pct, starts_at, expires_at, is_active, priority, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric,
			$11::numeric, $12::numeric, $13::numeric, $14, $15, $16, $17, $18, $19, $20)
	`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		rule.ID, rule.Name, rule.RuleType, rule.ProductID, rule.CustomerID, rule.JobID, nilIfEmptyCategory(rule.Category),
		priceString(rule.FixedPrice), quantityString(rule.DiscountPct), quantityString(rule.MarkupPct),
		rule.MinQuantity.DecimalString(), quantityString(rule.MaxQuantity), quantityString(rule.MarginFloorPct),
		timeOf(rule.StartsAt), timeOf(rule.ExpiresAt), rule.IsActive, rule.Priority, rule.Revision, rule.CreatedAt.Time, rule.UpdatedAt.Time,
	)
	if err != nil {
		return fmt.Errorf("failed to create pricing rule: %w", err)
	}
	return nil
}

// nilIfEmptyCategory writes the column's NULL for an unscoped rule, so the
// scope key's NULL semantics keep one row per scope.
func nilIfEmptyCategory(c string) any {
	if c == "" {
		return nil
	}
	return c
}

func priceString(p *httpx.Price) any {
	if p == nil {
		return nil
	}
	return p.DecimalString()
}

func quantityString(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return q.DecimalString()
}

func (r *PostgresRepository) ListRules(ctx context.Context) ([]PricingRule, error) {
	// COALESCE on category for the same reason GetMatchingRules does it: the
	// column is nullable, PricingRule.Category is not, and a NULL row would
	// fail the scan and blank the whole rules screen.
	query := `
		SELECT ` + ruleColumns + `
		FROM pricing_rules
		ORDER BY priority DESC, created_at DESC
	`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list pricing rules: %w", err)
	}
	defer rows.Close()

	var rules []PricingRule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pricing rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// RuleCursor is the keyset position of a rules list page: created_at, id.
type RuleCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListRulesPage is the rules list's keyset query: `created_at DESC, id DESC`,
// the ordering the C3-1 migration built its index on. after is nil for the
// first page; the caller asks for limit+1 rows and reads whether another page
// exists.
func (r *PostgresRepository) ListRulesPage(ctx context.Context, after *RuleCursor, limit int) ([]PricingRule, error) {
	query := `SELECT ` + ruleColumns + ` FROM pricing_rules`
	args := []any{}
	if after != nil {
		query += ` WHERE (created_at, id) < ($1, $2)`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	query += fmt.Sprintf(` LIMIT $%d`, len(args))

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list pricing rules: %w", err)
	}
	defer rows.Close()

	var rules []PricingRule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pricing rule: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pricing rule rows error: %w", err)
	}
	return rules, nil
}

func (r *PostgresRepository) CountRules(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM pricing_rules`).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count pricing rules: %w", err)
	}
	return total, nil
}
