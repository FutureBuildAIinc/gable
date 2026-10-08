// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"time"

	"github.com/google/uuid"
)

// UOM represents the strict Unit of Measure types matching the database ENUM
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

// Product represents a catalog item
type Product struct {
	ID          uuid.UUID  `json:"id"`
	SKU         string     `json:"sku"`
	Description string     `json:"description"`
	UOMPrimary  UOM        `json:"uom_primary"`
	BasePrice   float64    `json:"base_price"`
	Vendor      *string    `json:"vendor"`    // Denormalized display name (kept for back-compat)
	VendorID    *uuid.UUID `json:"vendor_id"` // Canonical FK -> vendors.id
	UPC         *string    `json:"upc"`
	WeightLbs   float64    `json:"weight_lbs"`

	// Canonical parametric geometry (inches) — the PIM is the source of truth
	// for per-product dimensions, and downstream load planners (AI_LM) render
	// each product as a scaled digital twin from these. They are POINTERS
	// because nil ("no geometry entered yet") must stay distinguishable from a
	// real 0.0 dimension; a consumer falls back to its own defaults only for
	// nil. Stackable is likewise nil when unknown rather than assumed true.
	// GeometrySource records the provenance of the L/W/H triple and is a
	// forward-compat seam for future mesh geometry.
	// Added by migration 080_ailm_integration_contract.
	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource *string  `json:"geometry_source"`

	ReorderPoint    float64   `json:"reorder_point"`
	ReorderQty      float64   `json:"reorder_qty"`
	TotalQuantity   float64   `json:"total_quantity" db:"-"` // Aggregated from inventory
	TotalAllocated  float64   `json:"total_allocated" db:"-"`
	AverageUnitCost float64   `json:"average_unit_cost"`
	TargetMargin    float64   `json:"target_margin"`
	CommissionRate  float64   `json:"commission_rate"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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

// ReorderAlert represents a product that's below its reorder point
type ReorderAlert struct {
	ProductID    uuid.UUID  `json:"product_id"`
	SKU          string     `json:"sku"`
	Description  string     `json:"description"`
	Vendor       *string    `json:"vendor"`
	VendorID     *uuid.UUID `json:"vendor_id"`
	ReorderPoint float64    `json:"reorder_point"`
	ReorderQty   float64    `json:"reorder_qty"`
	CurrentStock float64    `json:"current_stock"`
	Deficit      float64    `json:"deficit"`
}
