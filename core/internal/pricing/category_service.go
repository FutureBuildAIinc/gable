// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Precondition is the revision a client states for a write: the If-Match
// header and the body's revision field, either or both (httpx.CheckRevision
// resolves them against the locked row).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

// CategoryPricingService implements the 5-step category-aware pricing resolution.
type CategoryPricingService struct {
	catRepo CategoryRepository
	tx      TxRunner // optional; nil runs each write unwrapped (tests)
}

// NewCategoryPricingService creates a new CategoryPricingService.
func NewCategoryPricingService(catRepo CategoryRepository) *CategoryPricingService {
	return &CategoryPricingService{catRepo: catRepo}
}

// ResolveEffectivePrice runs the 5-step resolution algorithm:
//  1. Account + exact category
//  2. Account + ancestor category
//  3. Tier + exact category
//  4. Tier + ancestor category
//  5. No match (fallthrough to base price)
func (s *CategoryPricingService) ResolveEffectivePrice(
	ctx context.Context,
	customerID uuid.UUID,
	customerTier string,
	productID uuid.UUID,
) (*ResolvedCategoryPrice, error) {
	// Step 0: Get the product's category, ltree path, and cost price
	categoryID, categoryPath, costPrice, err := s.catRepo.GetProductCategoryPath(ctx, productID)
	if err != nil {
		// Product has no category — cannot resolve, fall through
		return &ResolvedCategoryPrice{MatchType: "none"}, nil
	}

	makeResult := func(rule *CategoryPricingRule, matchType string) *ResolvedCategoryPrice {
		return &ResolvedCategoryPrice{
			Rule:         rule,
			MatchType:    matchType,
			CategoryPath: categoryPath,
			CostPrice:    costPrice,
		}
	}

	// Step 1: Account + exact category
	rule, err := s.catRepo.ResolveAccountExact(ctx, customerID, categoryID)
	if err != nil {
		return nil, fmt.Errorf("resolve account exact: %w", err)
	}
	if rule != nil {
		return makeResult(rule, "account_exact"), nil
	}

	// Step 2: Account + ancestor category (climb ltree path)
	rule, err = s.catRepo.ResolveAccountAncestor(ctx, customerID, categoryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve account ancestor: %w", err)
	}
	if rule != nil {
		return makeResult(rule, "account_ancestor"), nil
	}

	// Step 3: Tier + exact category
	if customerTier != "" && customerTier != "RETAIL" {
		rule, err = s.catRepo.ResolveTierExact(ctx, customerTier, categoryID)
		if err != nil {
			return nil, fmt.Errorf("resolve tier exact: %w", err)
		}
		if rule != nil {
			return makeResult(rule, "tier_exact"), nil
		}

		// Step 4: Tier + ancestor category
		rule, err = s.catRepo.ResolveTierAncestor(ctx, customerTier, categoryPath)
		if err != nil {
			return nil, fmt.Errorf("resolve tier ancestor: %w", err)
		}
		if rule != nil {
			return makeResult(rule, "tier_ancestor"), nil
		}
	}

	// Step 5: No match — fall through to base price
	return &ResolvedCategoryPrice{
		MatchType:    "none",
		CategoryPath: categoryPath,
		CostPrice:    costPrice,
	}, nil
}

// ApplyRule calculates the effective price based on rule type, in exact
// rational arithmetic (ADR 0006 R3); the one scale 4 rounding happens where
// the engine returns the price (R4.2), never inside a step.
//   - MARKDOWN: basePrice * (1 - value/100)
//   - MARKUP:   costPrice * (1 + value/100), or on the base price when
//     there is no cost to read (a cost of zero)
//   - FIXED:    value (absolute price, exact)
//   - MARGIN:   costPrice / (1 - value/100); a margin of 100 or more, or a
//     zero cost, answers the base price unchanged
func (s *CategoryPricingService) ApplyRule(rule *CategoryPricingRule, basePrice *big.Rat, costPrice *big.Rat) *big.Rat {
	if rule == nil {
		return basePrice
	}
	value := rule.ValueRat()
	if value == nil {
		return basePrice
	}

	switch rule.RuleType {
	case CategoryRuleMarkdown:
		return applyMarkdown(basePrice, value)
	case CategoryRuleMarkup:
		if costPrice.Sign() > 0 {
			return applyMarkupPercent(costPrice, value)
		}
		return applyMarkupPercent(basePrice, value)
	case CategoryRuleFixed:
		return value
	case CategoryRuleMargin:
		if costPrice.Sign() > 0 && value.Cmp(big.NewRat(100, 1)) < 0 {
			return new(big.Rat).Quo(costPrice, new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(value, big.NewRat(100, 1))))
		}
		return basePrice
	default:
		return basePrice
	}
}

// --- Category Management ---

// ListCategories returns all active categories as a flat list.
func (s *CategoryPricingService) ListCategories(ctx context.Context) ([]ProductCategory, error) {
	return s.catRepo.ListCategories(ctx)
}

// ListCategoriesTree returns categories as a nested tree structure.
func (s *CategoryPricingService) ListCategoriesTree(ctx context.Context) ([]ProductCategory, error) {
	flat, err := s.catRepo.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	return buildCategoryTree(flat), nil
}

// CreateCategory creates a new product category.
func (s *CategoryPricingService) CreateCategory(ctx context.Context, c *ProductCategory) error {
	return s.catRepo.CreateCategory(ctx, c)
}

// UpdateCategory updates an existing product category.
func (s *CategoryPricingService) UpdateCategory(ctx context.Context, c *ProductCategory) error {
	return s.catRepo.UpdateCategory(ctx, c)
}

// --- Rule Management ---

// WithTxRunner substitutes the transaction boundary the rule writes run in.
// Optional: unit tests run without one and each write then runs unwrapped.
func (s *CategoryPricingService) WithTxRunner(tx TxRunner) *CategoryPricingService {
	s.tx = tx
	return s
}

// inTx runs fn in one transaction when a runner is wired, so a rule write
// and its audit entry share one fate; every statement fn runs goes through
// the transaction the context carries.
func (s *CategoryPricingService) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// CreateCategoryRule creates a new category pricing rule and its audit entry
// in one transaction: a failed audit write fails the create.
func (s *CategoryPricingService) CreateCategoryRule(ctx context.Context, r *CategoryPricingRule) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.catRepo.CreateCategoryRule(ctx, r); err != nil {
			return err
		}
		return s.logAudit(ctx, r.ID, "CREATE", nil, r)
	})
}

// UpdateCategoryRule updates an existing category pricing rule at the given
// revision and writes its audit entry, in one transaction that holds the
// row. A missing row or a stale revision is the contract's 404 or 409; the
// revision check runs after the lock, so the audit entry's old values are
// the row the update replaced.
func (s *CategoryPricingService) UpdateCategoryRule(ctx context.Context, r *CategoryPricingRule, pre Precondition) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		old, err := s.catRepo.LockCategoryRule(ctx, r.ID)
		if err != nil {
			return err
		}
		if old == nil {
			return httpx.NotFound("no such category rule")
		}
		if err := httpx.CheckRevision(old.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		r.TargetType, r.CustomerID, r.Tier, r.CategoryID = old.TargetType, old.CustomerID, old.Tier, old.CategoryID
		if err := s.catRepo.UpdateCategoryRule(ctx, r, old.Revision); err != nil {
			return resolveRuleWrite(err)
		}
		return s.logAudit(ctx, r.ID, "UPDATE", old, r)
	})
}

// resolveRuleWrite maps a rule write's refusals to the boundary errors.
func resolveRuleWrite(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("no such category rule")
	case errors.Is(err, ErrStaleRevision):
		return httpx.StaleRevision("the rule was changed after this revision was read; reload and retry")
	default:
		return err
	}
}

// ListCategoryRulesPage is the category rules list's keyset page:
// `created_at DESC, id DESC` under the filters, limit+1 rows read so the
// handler knows whether another page exists.
func (s *CategoryPricingService) ListCategoryRulesPage(ctx context.Context, filter CategoryRuleFilter, after *time.Time, afterID *uuid.UUID, limit int, wantTotal bool) ([]CategoryPricingRule, bool, int64, error) {
	rules, err := s.catRepo.ListCategoryRulesPage(ctx, filter, after, afterID, limit+1)
	if err != nil {
		return nil, false, 0, err
	}
	more := len(rules) > limit
	if more {
		rules = rules[:limit]
	}
	var total int64
	if wantTotal {
		total, err = s.catRepo.CountCategoryRules(ctx, filter)
		if err != nil {
			return nil, false, 0, err
		}
	}
	return rules, more, total, nil
}

// DeleteCategoryRule deletes a category pricing rule and writes its audit
// entry, in one transaction that holds the row. The revision the caller
// built on is checked after the lock.
func (s *CategoryPricingService) DeleteCategoryRule(ctx context.Context, id uuid.UUID, pre Precondition) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		old, err := s.catRepo.LockCategoryRule(ctx, id)
		if err != nil {
			return err
		}
		if old == nil {
			return httpx.NotFound("no such category rule")
		}
		if err := httpx.CheckRevision(old.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if err := s.catRepo.DeleteCategoryRule(ctx, id); err != nil {
			return err
		}
		return s.logAudit(ctx, id, "DELETE", old, nil)
	})
}

// GetCategoryRule retrieves a single category pricing rule by ID.
func (s *CategoryPricingService) GetCategoryRule(ctx context.Context, id uuid.UUID) (*CategoryPricingRule, error) {
	return s.catRepo.GetCategoryRule(ctx, id)
}

// ListCategoryRules lists category pricing rules with optional filters.
func (s *CategoryPricingService) ListCategoryRules(ctx context.Context, filter CategoryRuleFilter) ([]CategoryPricingRule, error) {
	return s.catRepo.ListCategoryRules(ctx, filter)
}

// ListAuditEntries returns audit log entries for a given rule.
func (s *CategoryPricingService) ListAuditEntries(ctx context.Context, ruleID uuid.UUID) ([]CategoryPricingAudit, error) {
	return s.catRepo.ListAuditEntries(ctx, ruleID)
}

// BulkUpsertRules creates or updates many rules, and writes one audit entry
// per rule, in one transaction: a rule that cannot be written, or an audit
// entry that cannot, refuses the whole batch.
func (s *CategoryPricingService) BulkUpsertRules(ctx context.Context, rules []CategoryPricingRule) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		existing := make(map[uuid.UUID]*CategoryPricingRule)
		for _, r := range rules {
			if r.ID == uuid.Nil {
				continue
			}
			old, err := s.catRepo.LockCategoryRule(ctx, r.ID)
			if err != nil {
				return err
			}
			if old != nil {
				existing[r.ID] = old
			}
		}
		if err := s.catRepo.BulkUpsertRules(ctx, rules); err != nil {
			return err
		}
		for i := range rules {
			old := existing[rules[i].ID]
			action := "CREATE"
			if old != nil {
				action = "UPDATE"
			}
			if err := s.logAudit(ctx, rules[i].ID, action, old, &rules[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// BulkDeleteRules deletes many rules and writes one audit entry per deleted
// rule, in one transaction.
func (s *CategoryPricingService) BulkDeleteRules(ctx context.Context, ids []uuid.UUID) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		var oldRules []*CategoryPricingRule
		for _, id := range ids {
			old, err := s.catRepo.LockCategoryRule(ctx, id)
			if err != nil {
				return err
			}
			if old != nil {
				oldRules = append(oldRules, old)
			}
		}
		if err := s.catRepo.BulkDeleteRules(ctx, ids); err != nil {
			return err
		}
		for _, old := range oldRules {
			if err := s.logAudit(ctx, old.ID, "DELETE", old, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- Audit Helpers ---

func (s *CategoryPricingService) logAudit(ctx context.Context, ruleID uuid.UUID, action string, old, new_ *CategoryPricingRule) error {
	entry := &CategoryPricingAudit{
		RuleID:      ruleID,
		Action:      action,
		PerformedBy: getPerformedBy(ctx),
	}
	if old != nil {
		entry.OldValues = ruleToMap(old)
		entry.CategoryID = &old.CategoryID
		entry.TargetType = string(old.TargetType)
		entry.Tier = old.Tier
		entry.CustomerID = old.CustomerID
	}
	if new_ != nil {
		entry.NewValues = ruleToMap(new_)
		entry.CategoryID = &new_.CategoryID
		entry.TargetType = string(new_.TargetType)
		entry.Tier = new_.Tier
		entry.CustomerID = new_.CustomerID
	}
	return s.catRepo.CreateAuditEntry(ctx, entry)
}

func getPerformedBy(ctx context.Context) string {
	if claims := middleware.ClaimsFromContext(ctx); claims != nil {
		if claims.Email != "" {
			return claims.Email
		}
		return claims.Subject
	}
	return "system"
}

func ruleToMap(r *CategoryPricingRule) map[string]any {
	m := map[string]any{
		"rule_type": strings.ToLower(string(r.RuleType)),
		"is_active": r.IsActive,
		"priority":  r.Priority,
	}
	if r.ValuePrice != nil {
		m["value_ten_thousandths"] = int64(*r.ValuePrice)
	}
	if r.ValuePct != nil {
		m["value_pct"] = r.ValuePct.WireString()
	}
	if r.MarginFloorPct != nil {
		m["margin_floor_pct"] = r.MarginFloorPct.WireString()
	}
	if r.Tier != "" {
		m["tier"] = r.Tier
	}
	if r.CustomerID != nil {
		m["customer_id"] = r.CustomerID.String()
	}
	return m
}

// --- Matrix ---

// GetMatrix builds the full pricing matrix for the admin UI.
func (s *CategoryPricingService) GetMatrix(ctx context.Context) (*MatrixResponse, error) {
	categories, err := s.catRepo.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}

	rules, err := s.catRepo.GetMatrixRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("get matrix rules: %w", err)
	}

	tiers := []string{"RETAIL", "SILVER", "GOLD", "PLATINUM"}

	// Build a lookup: tier+categoryID → rule
	ruleMap := make(map[string]*CategoryPricingRule)
	for i := range rules {
		key := rules[i].Tier + ":" + rules[i].CategoryID.String()
		ruleMap[key] = &rules[i]
	}

	// Build cells for each category×tier combination
	var cells []MatrixCell
	for _, cat := range categories {
		for _, tier := range tiers {
			key := tier + ":" + cat.ID.String()
			cell := MatrixCell{
				CategoryID:   cat.ID,
				CategoryName: cat.Name,
				CategoryPath: cat.Path,
				Tier:         tier,
			}

			if rule, ok := ruleMap[key]; ok {
				cell.Rule = rule
				cell.Inherited = false
			} else {
				// Check ancestors for inherited rule
				inheritedRule := findInheritedRule(cat.Path, tier, categories, ruleMap)
				if inheritedRule != nil {
					cell.Rule = inheritedRule
					cell.Inherited = true
					cell.SourcePath = inheritedRule.CategoryPath
				}
			}

			cells = append(cells, cell)
		}
	}

	return &MatrixResponse{
		Categories: buildCategoryTree(categories),
		Tiers:      tiers,
		Cells:      cells,
	}, nil
}

// --- Helpers ---

// buildCategoryTree converts a flat sorted list into a nested tree.
func buildCategoryTree(flat []ProductCategory) []ProductCategory {
	// Make copies so we don't mutate the input
	nodes := make([]ProductCategory, len(flat))
	copy(nodes, flat)
	for i := range nodes {
		nodes[i].Children = nil
	}

	idMap := make(map[uuid.UUID]*ProductCategory)
	for i := range nodes {
		idMap[nodes[i].ID] = &nodes[i]
	}

	var roots []ProductCategory
	for i := range nodes {
		if nodes[i].ParentID != nil {
			if parent, ok := idMap[*nodes[i].ParentID]; ok {
				parent.Children = append(parent.Children, nodes[i])
				continue
			}
		}
		roots = append(roots, nodes[i])
	}

	// Re-attach children from the map (they may have been appended after being added to roots)
	for i := range roots {
		if mapped, ok := idMap[roots[i].ID]; ok {
			roots[i].Children = mapped.Children
		}
	}

	return roots
}

// findInheritedRule walks up the category path to find an ancestor rule.
func findInheritedRule(path string, tier string, categories []ProductCategory, ruleMap map[string]*CategoryPricingRule) *CategoryPricingRule {
	// Walk up the path segments: "lumber.framing" → check "lumber"
	parts := splitPath(path)
	for i := len(parts) - 1; i >= 0; i-- {
		ancestorPath := joinPath(parts[:i])
		if ancestorPath == "" || ancestorPath == path {
			continue
		}
		// Find the category with this path
		for _, cat := range categories {
			if cat.Path == ancestorPath {
				key := tier + ":" + cat.ID.String()
				if rule, ok := ruleMap[key]; ok {
					return rule
				}
			}
		}
	}
	return nil
}

func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	result := []string{}
	current := ""
	for _, ch := range path {
		if ch == '.' {
			if current != "" {
				result = append(result, current)
			}
			current = ""
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func joinPath(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "."
		}
		result += p
	}
	return result
}
