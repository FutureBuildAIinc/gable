// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"math/big"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// TargetType determines if a category pricing rule targets a specific
// account or a tier; lowercase on the wire.
type TargetType string

const (
	TargetTypeAccount TargetType = "ACCOUNT"
	TargetTypeTier    TargetType = "TIER"
)

// MarshalText writes the lowercase wire name.
func (t TargetType) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(t))), nil
}

// ParseTargetType maps a lowercase wire name to its target type.
func ParseTargetType(name string) (TargetType, bool) {
	for _, t := range []TargetType{TargetTypeAccount, TargetTypeTier} {
		if strings.ToLower(string(t)) == name {
			return t, true
		}
	}
	return "", false
}

// CategoryRuleType defines how the rule's value is applied; lowercase on the
// wire.
type CategoryRuleType string

const (
	CategoryRuleMarkup   CategoryRuleType = "MARKUP"   // sell = cost * (1 + value/100)
	CategoryRuleMarkdown CategoryRuleType = "MARKDOWN" // sell = base * (1 - value/100)
	CategoryRuleFixed    CategoryRuleType = "FIXED"    // sell = value (absolute price)
	CategoryRuleMargin   CategoryRuleType = "MARGIN"   // sell = cost / (1 - value/100)
)

// MarshalText writes the lowercase wire name.
func (t CategoryRuleType) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(t))), nil
}

// ParseCategoryRuleType maps a lowercase wire name to its rule type.
func ParseCategoryRuleType(name string) (CategoryRuleType, bool) {
	for _, t := range []CategoryRuleType{CategoryRuleMarkup, CategoryRuleMarkdown, CategoryRuleFixed, CategoryRuleMargin} {
		if strings.ToLower(string(t)) == name {
			return t, true
		}
	}
	return "", false
}

// ProductCategory represents a node in the hierarchical product category tree.
type ProductCategory struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Slug      string     `json:"slug"`
	Path      string     `json:"path"` // ltree path, e.g. "lumber.framing"
	ParentID  *uuid.UUID `json:"parent_id"`
	SortOrder int        `json:"sort_order"`
	IsActive  bool       `json:"is_active"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`

	Children []ProductCategory `json:"children,omitempty"`
}

// CategoryPricingRule represents a row in category_pricing_rules. The rule's
// value is one number with two readings (ADR 0001 section 7): a fixed price
// at scale 4 when the rule type is FIXED, a percentage as a decimal string
// otherwise, so the wire carries the one that applies and null for the
// other.
type CategoryPricingRule struct {
	ID             uuid.UUID         `json:"id"`
	TargetType     TargetType        `json:"target_type"`
	CustomerID     *uuid.UUID        `json:"customer_id"`
	Tier           string            `json:"tier"`
	CategoryID     uuid.UUID         `json:"category_id"`
	RuleType       CategoryRuleType  `json:"rule_type"`
	ValuePrice     *httpx.Price      `json:"value_ten_thousandths"`
	ValuePct       *httpx.Quantity   `json:"value_pct"`
	MarginFloorPct *httpx.Quantity   `json:"margin_floor_pct"`
	StartsAt       *httpx.Timestamp  `json:"starts_at"`
	ExpiresAt      *httpx.Timestamp  `json:"expires_at"`
	IsActive       bool              `json:"is_active"`
	Priority       int               `json:"priority"`
	CreatedBy      string            `json:"created_by"`
	Revision       int64             `json:"revision"`
	CreatedAt      httpx.Timestamp   `json:"created_at"`
	UpdatedAt      httpx.Timestamp   `json:"updated_at"`

	// Joined fields for API responses
	CategoryName string `json:"category_name,omitempty"`
	CategoryPath string `json:"category_path,omitempty"`
	CustomerName string `json:"customer_name,omitempty"`
}

// ValueRat is the rule's value as an exact rational, whatever reading its
// type gives it. A rule with no value answers nil.
func (r *CategoryPricingRule) ValueRat() *big.Rat {
	if r.ValuePrice != nil {
		return ratOfPrice(*r.ValuePrice)
	}
	if r.ValuePct != nil {
		return ratOfQuantity(*r.ValuePct)
	}
	return nil
}

// ResolvedCategoryPrice is the output of the category resolution algorithm.
type ResolvedCategoryPrice struct {
	Rule         *CategoryPricingRule `json:"rule,omitempty"`
	MatchType    string               `json:"match_type"` // "account_exact", "account_ancestor", "tier_exact", "tier_ancestor", "none"
	CategoryPath string               `json:"category_path"`
	CostPrice    httpx.Price          `json:"cost_price_ten_thousandths"` // product's average unit cost for MARKUP/MARGIN rules
}

// MatrixCell represents a single cell in the pricing matrix grid.
type MatrixCell struct {
	CategoryID   uuid.UUID            `json:"category_id"`
	CategoryName string               `json:"category_name"`
	CategoryPath string               `json:"category_path"`
	Tier         string               `json:"tier"`
	Rule         *CategoryPricingRule `json:"rule,omitempty"`
	Inherited    bool                 `json:"inherited"`
	SourcePath   string               `json:"source_path,omitempty"`
}

// MatrixResponse is the admin API response for the full pricing matrix.
type MatrixResponse struct {
	Categories []ProductCategory `json:"categories"`
	Tiers      []string          `json:"tiers"`
	Cells      []MatrixCell      `json:"cells"`
}

// CategoryRuleFilter is used to filter category pricing rules in list queries.
type CategoryRuleFilter struct {
	TargetType *TargetType `json:"target_type,omitempty"`
	Tier       string      `json:"tier,omitempty"`
	CustomerID *uuid.UUID  `json:"customer_id,omitempty"`
	CategoryID *uuid.UUID  `json:"category_id,omitempty"`
	IsActive   *bool       `json:"is_active,omitempty"`
}

// CategoryPricingAudit represents a row in the audit trail table.
type CategoryPricingAudit struct {
	ID          uuid.UUID      `json:"id"`
	RuleID      uuid.UUID      `json:"rule_id"`
	Action      string         `json:"action"`
	OldValues   map[string]any `json:"old_values,omitempty"`
	NewValues   map[string]any `json:"new_values,omitempty"`
	PerformedBy string         `json:"performed_by"`
	PerformedAt httpx.Timestamp `json:"performed_at"`
	CategoryID  *uuid.UUID     `json:"category_id,omitempty"`
	TargetType  string         `json:"target_type,omitempty"`
	Tier        string         `json:"tier,omitempty"`
	CustomerID  *uuid.UUID     `json:"customer_id,omitempty"`
}

// PaginatedRulesResponse is the legacy offset envelope the category rules
// list served before its conversion onto the recipe; it leaves with that
// conversion.
type PaginatedRulesResponse struct {
	Data   []CategoryPricingRule `json:"data"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}
