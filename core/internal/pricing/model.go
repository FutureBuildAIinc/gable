// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// PricingSource is where a price came from, in its storage vocabulary (the
// UPPERCASE values the engine's callers compare). On the wire it is
// `price_basis` in lowercase (ADR 0001 section 6, ADR 0006 section 7.3).
type PricingSource string

const (
	SourceContract        PricingSource = "CONTRACT"
	SourceTier            PricingSource = "TIER"
	SourceRetail          PricingSource = "RETAIL"
	SourceQuantityBreak   PricingSource = "QUANTITY_BREAK"
	SourceJobOverride     PricingSource = "JOB_OVERRIDE"
	SourcePromotional     PricingSource = "PROMOTIONAL"
	SourceCategoryTier    PricingSource = "CATEGORY_TIER"
	SourceCategoryAccount PricingSource = "CATEGORY_ACCOUNT"
)

// MarshalText writes the lowercase wire name.
func (s PricingSource) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(s))), nil
}

// ParsePricingSource maps a lowercase wire name back to its source. Any
// other spelling, the legacy uppercase included, is not a price basis.
func ParsePricingSource(name string) (PricingSource, bool) {
	for _, s := range []PricingSource{
		SourceContract, SourceTier, SourceRetail, SourceQuantityBreak,
		SourceJobOverride, SourcePromotional, SourceCategoryTier, SourceCategoryAccount,
	} {
		if strings.ToLower(string(s)) == name {
			return s, true
		}
	}
	return "", false
}

// RuleType is a pricing rule's kind in its storage vocabulary; lowercase on
// the wire.
type RuleType string

const (
	RuleTypeQuantityBreak RuleType = "QUANTITY_BREAK"
	RuleTypeJobOverride   RuleType = "JOB_OVERRIDE"
	RuleTypePromotional   RuleType = "PROMOTIONAL"
)

// MarshalText writes the lowercase wire name.
func (t RuleType) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(t))), nil
}

// ParseRuleType maps a lowercase wire name to its rule type. Any other
// spelling is not a rule type.
func ParseRuleType(name string) (RuleType, bool) {
	for _, t := range []RuleType{RuleTypeQuantityBreak, RuleTypeJobOverride, RuleTypePromotional} {
		if strings.ToLower(string(t)) == name {
			return t, true
		}
	}
	return "", false
}

// CalculatedPrice is the compatibility answer the float64 entry points
// return (ADR 0006 section 9.1): the same struct and field names cycle 2
// built against, with the exact scale 4 value in its float64 fields, never a
// cent rounding.
type CalculatedPrice struct {
	ProductID     uuid.UUID     `json:"product_id"`
	OriginalPrice float64       `json:"original_price"` // Base Retail
	FinalPrice    float64       `json:"final_price"`
	DiscountPct   float64       `json:"discount_pct"`
	Source        PricingSource `json:"source"`
	Details       string        `json:"details"` // e.g. "Gold Member Discount"
}

// ScaledPrice is the engine's own answer (ADR 0006 section 7.3): the exact
// scale 4 price with its source. Until C3-2A-pricing every price is in the
// product's stocking unit.
type ScaledPrice struct {
	Price   httpx.Price   `json:"unit_price_ten_thousandths"`
	Source  PricingSource `json:"price_basis"`
	Details string        `json:"details"`
}

// Float64 renders the scaled price the way the compatibility entry points
// answer it: the scale 4 value as a float64.
func (s ScaledPrice) Float64() float64 {
	return float64(s.Price) / 10_000
}

// CustomerContract is one customer's negotiated price for one product. The
// price is a fixed price at scale 4, returned as stored and never rounded
// (ADR 0006 R4.2).
type CustomerContract struct {
	ID            uuid.UUID       `json:"id"`
	CustomerID    uuid.UUID       `json:"customer_id"`
	ProductID     uuid.UUID       `json:"product_id"`
	ContractPrice httpx.Price     `json:"contract_price_ten_thousandths"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
	UpdatedAt     httpx.Timestamp `json:"updated_at"`
}

// PricingRule is one dealer pricing rule. Prices and percentages are exact
// scale 4 values; quantities are scale 4 decimal strings on the wire. A
// derived price (a discount or a markup) is computed exactly and rounded
// once when the engine returns it; the stored values are never rounded.
type PricingRule struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	RuleType   RuleType   `json:"rule_type"`
	ProductID  *uuid.UUID `json:"product_id"`
	CustomerID *uuid.UUID `json:"customer_id"`
	JobID      *uuid.UUID `json:"job_id"`
	// Category scopes the rule to a product category. Empty means unscoped:
	// the rule applies to every product. The column is nullable and the
	// repository COALESCEs NULL to "", so NULL and "" are the same thing here.
	// See categoryScopePredicate in repository.go for how a non-empty value is
	// matched (flat products.category string, or the ltree node/ancestor).
	Category       string           `json:"category"`
	FixedPrice     *httpx.Price     `json:"fixed_price_ten_thousandths"`
	DiscountPct    *httpx.Quantity  `json:"discount_pct"`
	MarkupPct      *httpx.Quantity  `json:"markup_pct"`
	MinQuantity    httpx.Quantity   `json:"min_quantity"`
	MaxQuantity    *httpx.Quantity  `json:"max_quantity"`
	MarginFloorPct *httpx.Quantity  `json:"margin_floor_pct"`
	StartsAt       *httpx.Timestamp `json:"starts_at"`
	ExpiresAt      *httpx.Timestamp `json:"expires_at"`
	IsActive       bool             `json:"is_active"`
	Priority       int              `json:"priority"`
	Revision       int64            `json:"revision"`
	CreatedAt      httpx.Timestamp  `json:"created_at"`
	UpdatedAt      httpx.Timestamp  `json:"updated_at"`
}

// timeOf is the time.Time behind a timestamp field, for the repository
// writes that predate the wire type.
func timeOf(t *httpx.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	tt := t.Time
	return &tt
}
