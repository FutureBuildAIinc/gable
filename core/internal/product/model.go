// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"strconv"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// UOM represents the unit of measure codes the product's stocking unit holds.
// The closed enum the database keeps lasts until C3-2A-units replaces it with
// the unit catalogue (ADR 0006 section 2); the codes are the standard
// vocabulary ADR 0001 section 6 preserves verbatim.
type UOM string

const (
	UOM_PCS    UOM = "PCS"
	UOM_EA     UOM = "EA"
	UOM_LF     UOM = "LF"
	UOM_SF     UOM = "SF"
	UOM_BF     UOM = "BF"
	UOM_MBF    UOM = "MBF"
	UOM_SQ     UOM = "SQ"
	UOM_BOX    UOM = "BOX"
	UOM_CTN    UOM = "CTN"
	UOM_RL     UOM = "RL"
	UOM_GAL    UOM = "GAL"
	UOM_LBS    UOM = "LBS"
	UOM_BAG    UOM = "BAG"
	UOM_BUNDLE UOM = "BUNDLE"
	UOM_PAIR   UOM = "PAIR"
	UOM_SET    UOM = "SET"
)

// uomCodes is the closed vocabulary of the uom_primary column.
var uomCodes = map[UOM]bool{
	UOM_PCS: true, UOM_EA: true, UOM_LF: true, UOM_SF: true, UOM_BF: true,
	UOM_MBF: true, UOM_SQ: true, UOM_BOX: true, UOM_CTN: true, UOM_RL: true,
	UOM_GAL: true, UOM_LBS: true, UOM_BAG: true, UOM_BUNDLE: true,
	UOM_PAIR: true, UOM_SET: true,
}

// ValidUOM reports whether the code is one the column holds.
func ValidUOM(u UOM) bool { return uomCodes[u] }

// Product is the domain row. Its float64 and timestamp fields stay because
// unconverted readers (the counter, the quote service, the portal cart) read
// them; the wire answers the View beside it, whose scaled and string fields
// are the copy of the same values the contract serves. Nothing JSON encodes
// a Product any more, and every json tag here is "-" so one never leaks back
// onto the wire by accident.
type Product struct {
	ID          uuid.UUID  `json:"-"`
	SKU         string     `json:"-"`
	Description string     `json:"-"`
	UOMPrimary  UOM        `json:"-"`
	BasePrice   float64    `json:"-"`
	Vendor      *string    `json:"-"`
	VendorID    *uuid.UUID `json:"-"`
	UPC         *string    `json:"-"`
	WeightLbs   float64    `json:"-"`

	// Canonical parametric geometry (inches) — the PIM is the source of truth
	// for per-product dimensions, and downstream load planners (AI_LM) render
	// each product as a scaled digital twin from these. They are POINTERS
	// because nil ("no geometry entered yet") must stay distinguishable from a
	// real 0.0 dimension; a consumer falls back to its own defaults only for
	// nil. Stackable is likewise nil when unknown rather than assumed true.
	// GeometrySource records the provenance of the L/W/H triple and is a
	// forward-compat seam for future mesh geometry.
	// Added by migration 080_ailm_integration_contract.
	LengthIn       *float64 `json:"-"`
	WidthIn        *float64 `json:"-"`
	HeightIn       *float64 `json:"-"`
	Stackable      *bool    `json:"-"`
	GeometrySource *string  `json:"-"`

	ReorderPoint    float64 `json:"-"`
	ReorderQty      float64 `json:"-"`
	TotalQuantity   float64 `json:"-"`
	TotalAllocated  float64 `json:"-"`
	AverageUnitCost float64 `json:"-"`
	TargetMargin    float64 `json:"-"`
	CommissionRate  float64 `json:"-"`

	// The exact forms of the fields above, as the wire serves them (ADR 0006
	// 7.1): the scaled base price and cost, the stocking quantities as scale 4
	// integers, the reorder targets, and the revision. OnHand, Allocated and
	// Available carry the stocking unit's totals (quantity, allocated and
	// quantity - allocated); TotalQuantity and TotalAllocated above keep the
	// legacy float copies for the unconverted readers.
	BasePriceScaled       httpx.Price    `json:"-"`
	AverageUnitCostScaled httpx.Price    `json:"-"`
	OnHand                httpx.Quantity `json:"-"`
	Allocated             httpx.Quantity `json:"-"`
	Available             httpx.Quantity `json:"-"`
	ReorderPointQ         httpx.Quantity `json:"-"`
	ReorderQtyQ           httpx.Quantity `json:"-"`
	Revision              int64          `json:"-"`

	// LeadTimeDays is the dealer-published lead time (migration 084): nil
	// means unpublished and is a different fact than zero.
	LeadTimeDays *int `json:"-"`

	// The unit set facts of ADR 0006 sections 3.1 and 3.2, added by item
	// C3-2A-units: the three default units, the nominal board measure
	// columns a random length tally reads, and the product's unit set rows
	// (filled on the reads that serve them; a set always holds at least the
	// stocking row, so Units is never empty on a stored row).
	SaleUOM          string           `json:"-"`
	PriceUOM         string           `json:"-"`
	PurchaseUOM      string           `json:"-"`
	BoardThicknessIn *httpx.Quantity  `json:"-"`
	BoardWidthIn     *httpx.Quantity  `json:"-"`
	BoardLengthFT    *httpx.Quantity  `json:"-"`
	RandomLength     bool             `json:"-"`
	Units            []UnitSetRowView `json:"-"`

	CreatedAt httpx.Timestamp `json:"-"`
	UpdatedAt httpx.Timestamp `json:"-"`
}

// View is the product on the wire (ADR 0006 sections 6 and 7.1): stock_uom
// replaces uom_primary, base_price_ten_thousandths replaces base_price,
// quantities are decimal strings, and the stock totals answer on_hand,
// allocated and available in the stocking unit. A list item is the same
// shape: every field the summary carries the full read carries too.
type View struct {
	ID          uuid.UUID `json:"id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	StockUOM    UOM       `json:"stock_uom"`

	BasePrice       httpx.Price `json:"base_price_ten_thousandths"`
	AverageUnitCost httpx.Price `json:"average_unit_cost_ten_thousandths"`
	TargetMargin    float64     `json:"target_margin"`
	CommissionRate  float64     `json:"commission_rate"`

	Vendor   *string    `json:"vendor"`
	VendorID *uuid.UUID `json:"vendor_id"`
	UPC      *string    `json:"upc"`

	WeightLbs float64 `json:"weight_lbs"`

	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource *string  `json:"geometry_source"`

	LeadTimeDays *int `json:"lead_time_days"`

	ReorderPoint httpx.Quantity `json:"reorder_point"`
	ReorderQty   httpx.Quantity `json:"reorder_qty"`

	OnHand    httpx.Quantity `json:"on_hand"`
	Allocated httpx.Quantity `json:"allocated"`
	Available httpx.Quantity `json:"available"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`

	// The unit set facts of ADR 0006 section 6 (C3-2A-units): the three
	// default units, the nominal board measure columns (decimal strings,
	// null when the product carries none) and the set itself.
	SaleUOM          string           `json:"sale_uom"`
	PriceUOM         string           `json:"price_uom"`
	PurchaseUOM      string           `json:"purchase_uom"`
	BoardThicknessIn *httpx.Quantity  `json:"board_thickness_in"`
	BoardWidthIn     *httpx.Quantity  `json:"board_width_in"`
	BoardLengthFT    *httpx.Quantity  `json:"board_length_ft"`
	RandomLength     bool             `json:"random_length"`
	Units            []UnitSetRowView `json:"units"`
}

// ViewOf builds the wire view of a domain row. The scaled fields are already
// exact (the repository parses them from the column text); a row that
// reached the service without a repository pass keeps its float copies,
// which the decimal round trip renders exactly for every value a NUMERIC
// column can hold.
func ViewOf(p *Product) View {
	if p.Units == nil {
		p.Units = []UnitSetRowView{}
	}
	base, cost := p.BasePriceScaled, p.AverageUnitCostScaled
	if base == 0 {
		base = priceFromFloat(p.BasePrice)
	}
	if cost == 0 {
		cost = priceFromFloat(p.AverageUnitCost)
	}
	onHand, allocated := p.OnHand, p.Allocated
	if onHand == 0 && p.TotalQuantity != 0 {
		onHand = quantityFromFloat(p.TotalQuantity)
	}
	if allocated == 0 && p.TotalAllocated != 0 {
		allocated = quantityFromFloat(p.TotalAllocated)
	}
	rp, rq := p.ReorderPointQ, p.ReorderQtyQ
	if rp == 0 && p.ReorderPoint != 0 {
		rp = quantityFromFloat(p.ReorderPoint)
	}
	if rq == 0 && p.ReorderQty != 0 {
		rq = quantityFromFloat(p.ReorderQty)
	}
	return View{
		ID: p.ID, SKU: p.SKU, Description: p.Description, StockUOM: p.UOMPrimary,
		BasePrice: base, AverageUnitCost: cost,
		TargetMargin: p.TargetMargin, CommissionRate: p.CommissionRate,
		Vendor: p.Vendor, VendorID: p.VendorID, UPC: p.UPC, WeightLbs: p.WeightLbs,
		LengthIn: p.LengthIn, WidthIn: p.WidthIn, HeightIn: p.HeightIn,
		Stackable: p.Stackable, GeometrySource: p.GeometrySource,
		LeadTimeDays: p.LeadTimeDays,
		ReorderPoint: rp, ReorderQty: rq,
		OnHand: onHand, Allocated: allocated, Available: onHand - allocated,
		Revision: p.Revision, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		SaleUOM: p.SaleUOM, PriceUOM: p.PriceUOM, PurchaseUOM: p.PurchaseUOM,
		BoardThicknessIn: p.BoardThicknessIn, BoardWidthIn: p.BoardWidthIn,
		BoardLengthFT: p.BoardLengthFT, RandomLength: p.RandomLength,
		Units: p.Units,
	}
}

// QtyFloat renders one of the product wire's scale 4 quantities as the float
// an unconverted in-process caller (the purchase order line) still reads.
func QtyFloat(q httpx.Quantity) float64 { return float64(q) / 10_000 }

func priceFromFloat(f float64) httpx.Price {
	p, err := httpx.ParsePrice(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		return 0
	}
	return p
}

func quantityFromFloat(f float64) httpx.Quantity {
	q, err := httpx.ParseQuantity(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		return 0
	}
	return q
}

// Geometry is the mutable slice of a Product that the geometry editor owns:
// the parametric L/W/H triple, the stackable flag and the provenance label.
//
// EVERY FIELD IS A POINTER, and that is the entire point of the type. nil means
// "not recorded" and must survive all the way to a SQL NULL; it is materially
// different from a recorded 0.0 (a zero-volume box) or a recorded false (a SKU
// an operator has explicitly marked un-stackable). Downstream, AI_LM's
// resolveGeometry() falls back to its own override table only for null, so
// collapsing nil to a zero value here would hand the load planner a phantom
// box it has no way to detect. Keep these pointers.
type Geometry struct {
	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource *string  `json:"geometry_source"`
}

// HasDimensions reports whether any of the three linear dimensions was
// recorded. A SKU with no dimensions at all has no geometry to attribute a
// provenance to — see Service.UpdateDimensions.
func (g Geometry) HasDimensions() bool {
	return g.LengthIn != nil || g.WidthIn != nil || g.HeightIn != nil
}

// GeometrySourceParametric is the provenance recorded for an operator-entered
// L/W/H triple. It matches the value the seed data and the integration layer's
// resolveGeometrySource() use, and is the forward-compat seam for a future
// 'mesh' source (migration 080).
const GeometrySourceParametric = "parametric"

// ReorderAlert represents a product that's below its reorder point.
type ReorderAlert struct {
	ProductID    uuid.UUID      `json:"product_id"`
	SKU          string         `json:"sku"`
	Description  string         `json:"description"`
	Vendor       *string        `json:"vendor"`
	VendorID     *uuid.UUID     `json:"vendor_id"`
	ReorderPoint httpx.Quantity `json:"reorder_point"`
	ReorderQty   httpx.Quantity `json:"reorder_qty"`
	CurrentStock httpx.Quantity `json:"current_stock"`
	Deficit      httpx.Quantity `json:"deficit"`
}
