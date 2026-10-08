// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package salesdoc is the one line shape, extension, totals and tax rule of
// every sales document (ADR 0005 section 2.7): orders, invoices, credit memos
// and the counter all parse, price, extend and total their lines through it,
// and no module computes a line or a total another way.
package salesdoc

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// LineType is a document line's kind (ADR 0005 section 2.1). Storage is
// UPPERCASE with a database CHECK; the wire is lowercase (ADR 0001 section 6).
type LineType string

const (
	// LineProduct is goods: a stocked item when it names a product, a non
	// stock item (a special order the dealer does not carry) when it does not.
	LineProduct LineType = "PRODUCT"
	// LineKit is a kit sold and priced as a whole; its components move stock.
	LineKit LineType = "KIT"
	// LineComponent is one component of a kit, child of a LineKit line.
	LineComponent LineType = "COMPONENT"
	// LineCharge is a fee (freight, fuel, restocking) from the charge code
	// master.
	LineCharge LineType = "CHARGE"
	// LineText is a note on the document; it carries no amount.
	LineText LineType = "TEXT"
)

var lineTypeNames = []string{"product", "kit", "component", "charge", "text"}

// Status returns the wire spelling.
func (t LineType) Status() string { return strings.ToLower(string(t)) }

// MarshalText writes the lowercase wire name.
func (t LineType) MarshalText() ([]byte, error) { return []byte(t.Status()), nil }

// ParseLineType maps a lowercase wire name to its line type. The request
// vocabulary is narrower: kit and component lines are written by the server
// (Explode), never sent by a client.
func ParseLineType(name string) (LineType, bool) {
	for _, n := range lineTypeNames {
		if name == n {
			return LineType(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// PriceSource is where a line's unit price came from (ADR 0005 section 2.3).
type PriceSource string

const (
	PriceSourceList     PriceSource = "PRICE_LIST"
	PriceSourceQuote    PriceSource = "QUOTE"
	PriceSourceOverride PriceSource = "OVERRIDE"
	PriceSourceManual   PriceSource = "MANUAL"
	PriceSourceNone     PriceSource = "NONE"
)

var priceSourceNames = []string{"price_list", "quote", "override", "manual", "none"}

// MarshalText writes the lowercase wire name.
func (p PriceSource) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(p))), nil }

// ParsePriceSource maps a lowercase wire name to its price source.
func ParsePriceSource(name string) (PriceSource, bool) {
	for _, n := range priceSourceNames {
		if name == n {
			return PriceSource(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// TaxSource records how a document's tax was found (ADR 0005 section 3).
type TaxSource string

const (
	TaxSourceExempt     TaxSource = "EXEMPT"
	TaxSourceProvider   TaxSource = "PROVIDER"
	TaxSourceShipToRate TaxSource = "SHIP_TO_RATE"
	TaxSourceBranchRate TaxSource = "BRANCH_RATE"
	TaxSourceLegacy     TaxSource = "LEGACY"
)

var taxSourceNames = []string{"exempt", "provider", "ship_to_rate", "branch_rate", "legacy"}

// MarshalText writes the lowercase wire name.
func (s TaxSource) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(s))), nil }

// ParseTaxSource maps a lowercase wire name to its tax source.
func ParseTaxSource(name string) (TaxSource, bool) {
	for _, n := range taxSourceNames {
		if name == n {
			return TaxSource(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// One is the quantity 1 and each pair side 1 at the wire scale: what the
// conversion pair is when the units agree.
const One httpx.Quantity = 10000

// Line is one line of a sales document in the shared shape of ADR 0005
// section 2.2: the columns every sales document line carries, read and
// written as the wire fields named there. Fields that are null only on a
// text line are pointers; every priced line type carries them non nil, so a
// client reads one shape per line and never guesses which fields a kind
// drops (ADR 0001 section 7a).
type Line struct {
	ID           uuid.UUID  `json:"id"`
	Position     int        `json:"position"`
	LineType     LineType   `json:"line_type"`
	ParentLineID *uuid.UUID `json:"parent_line_id"`
	ProductID    *uuid.UUID `json:"product_id"`
	ChargeCodeID *uuid.UUID `json:"charge_code_id"`
	ChargeCode   *string    `json:"charge_code"` // the code text, read
	SKU          *string    `json:"sku"`
	Description  string     `json:"description"`

	Quantity    *httpx.Quantity `json:"quantity"`
	UOM         *string         `json:"uom"`
	PriceUOM    *string         `json:"price_uom"`
	UOMQty      *httpx.Quantity `json:"uom_qty"`
	PriceUOMQty *httpx.Quantity `json:"price_uom_qty"`

	UnitPrice       *httpx.Price `json:"unit_price_ten_thousandths"`
	PricedUnitPrice *httpx.Price `json:"priced_unit_price_ten_thousandths"`
	PriceSource     PriceSource  `json:"price_source"`
	OverrideReason  *string      `json:"override_reason"`

	DiscountPercent *httpx.Quantity `json:"discount_percent"`
	DiscountAmount  *httpx.Cents    `json:"discount_cents"`
	DiscountReason  *string         `json:"discount_reason"`
	PriceAdjustedBy *string         `json:"price_adjusted_by"`

	LineTotal *httpx.Cents `json:"line_total_cents"`
	Taxable   bool         `json:"taxable"`

	// RevenueAccountCode is the charge code's account, snapshotted at create;
	// posting reads it (product, kit and non stock lines post to 4010).
	RevenueAccountCode *string `json:"revenue_account_code"`

	IsSpecialOrder   bool         `json:"is_special_order"`
	VendorID         *uuid.UUID   `json:"vendor_id"`
	SpecialOrderCost *httpx.Price `json:"special_order_unit_cost_ten_thousandths"`

	CreatedAt httpx.Timestamp `json:"created_at"`
}

// IsStocked reports whether the line moves stock: a product line that names a
// product, or a kit component (ADR 0005 sections 2.1 and 5.4).
func (l *Line) IsStocked() bool {
	if l.LineType == LineComponent {
		return true
	}
	return l.LineType == LineProduct && l.ProductID != nil
}

// ProductRef is the slice of a product a document line defaults and prices
// from.
type ProductRef struct {
	ID             uuid.UUID
	SKU            string
	Description    string
	UOMPrimary     string
	BasePrice      httpx.Price
	AverageCost    httpx.Price
	CommissionRate float64
	IsKit          bool
	Taxable        bool
}

// KitComponent is one row of a kit's definition (ADR 0005 section 2.6):
// quantity is per one kit, in the component's own stocking unit.
type KitComponent struct {
	KitProductID       uuid.UUID
	ComponentProductID uuid.UUID
	Quantity           httpx.Quantity
	Position           int
}

// ChargeCode is the charge code master row a charge line snapshots from
// (ADR 0005 section 2.5).
type ChargeCode struct {
	ID                 uuid.UUID
	Code               string
	Name               string
	RevenueAccountCode string
	Taxable            bool
	DefaultUnitPrice   *httpx.Price
	IsActive           bool
	Revision           int64
}
