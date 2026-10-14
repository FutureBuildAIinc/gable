// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// unitCode is the catalogue's code rule (ADR 0006 section 2.1): the format
// is checked here, and the catalogue itself answers in the service.
var unitCode = regexp.MustCompile(`^[A-Z]{1,6}$`)

// createRequest is the POST /products body as it arrives: strings, pointers
// and raw numbers, parsed into the domain row by Parse with every problem
// collected into one 400 (the recipe's input rule).
type createRequest struct {
	SKU         string `json:"sku"`
	Description string `json:"description"`
	StockUOM    string `json:"stock_uom"`

	BasePrice *json.RawMessage `json:"base_price_ten_thousandths"`

	Vendor   *string `json:"vendor"`
	VendorID *string `json:"vendor_id"`
	UPC      *string `json:"upc"`

	WeightLbs *float64 `json:"weight_lbs"`

	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource *string  `json:"geometry_source"`

	ReorderPoint *json.RawMessage `json:"reorder_point"`
	ReorderQty   *json.RawMessage `json:"reorder_qty"`

	// The nominal board measure columns and the random length flag (ADR
	// 0006 section 3.1): decimal strings, both halves of a cross section
	// or neither.
	BoardThicknessIn *json.RawMessage `json:"board_thickness_in"`
	BoardWidthIn     *json.RawMessage `json:"board_width_in"`
	BoardLengthFT    *json.RawMessage `json:"board_length_ft"`
	RandomLength     *bool            `json:"random_length"`
}

// ParseCreate validates the create body and fills the domain row to hand the
// service.
func ParseCreate(r *http.Request) (*Product, error) {
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, err
	}
	v := &httpx.Validator{}
	p := &Product{SKU: req.SKU, Description: req.Description}

	v.Required("sku", req.SKU)
	v.Required("description", req.Description)
	if req.StockUOM == "" {
		v.Check(false, "stock_uom", "is required")
	} else if !unitCode.MatchString(req.StockUOM) {
		v.Check(false, "stock_uom", "must be a unit code of one to six capital letters, for example PCS, LF or MBF")
	}
	p.UOMPrimary = UOM(req.StockUOM)

	// The nominal board measure columns: decimal strings, a cross section
	// both halves or neither, a random length product carries no fixed
	// length (ADR 0006 section 3.1; the stocking unit rule for a random
	// length product is the service's, which reads the catalogue).
	p.BoardThicknessIn = optQuantity(v, "board_thickness_in", req.BoardThicknessIn)
	p.BoardWidthIn = optQuantity(v, "board_width_in", req.BoardWidthIn)
	p.BoardLengthFT = optQuantity(v, "board_length_ft", req.BoardLengthFT)
	if req.RandomLength != nil {
		p.RandomLength = *req.RandomLength
	}
	switch {
	case (p.BoardThicknessIn == nil) != (p.BoardWidthIn == nil):
		v.Check(false, "board_thickness_in", "a cross section is both thickness and width, or neither")
		v.Check(false, "board_width_in", "a cross section is both thickness and width, or neither")
	case p.RandomLength && p.BoardLengthFT != nil:
		v.Check(false, "board_length_ft", "a random length product carries no fixed length: its lengths live on its tallies")
	}

	if req.BasePrice != nil {
		if n, ok := v.Int("base_price_ten_thousandths", *req.BasePrice, true); ok {
			p.BasePriceScaled = httpx.Price(n)
			v.Check(p.BasePriceScaled >= 0, "base_price_ten_thousandths", "a unit price is never negative")
			v.Check(int64(p.BasePriceScaled) <= int64(httpx.QuantityMax), "base_price_ten_thousandths", "is larger than a price can be (99999999.9999)")
			p.BasePrice = float64(p.BasePriceScaled) / 10_000
		}
	}
	if req.ReorderPoint != nil {
		if q, ok := v.Quantity("reorder_point", *req.ReorderPoint, true); ok {
			p.ReorderPointQ = q
			v.Check(p.ReorderPointQ >= 0, "reorder_point", "must be zero or positive")
		}
	}
	if req.ReorderQty != nil {
		if q, ok := v.Quantity("reorder_qty", *req.ReorderQty, true); ok {
			p.ReorderQtyQ = q
			v.Check(p.ReorderQtyQ >= 0, "reorder_qty", "must be zero or positive")
		}
	}
	if req.VendorID != nil && *req.VendorID != "" {
		if id, ok := v.UUID("vendor_id", req.VendorID, true); ok {
			p.VendorID = &id
		}
	}
	p.Vendor, p.UPC = req.Vendor, req.UPC
	p.WeightLbs = 0
	if req.WeightLbs != nil {
		p.WeightLbs = *req.WeightLbs
		v.Check(p.WeightLbs >= 0, "weight_lbs", "must be zero or positive")
	}
	p.LengthIn, p.WidthIn, p.HeightIn = req.LengthIn, req.WidthIn, req.HeightIn
	p.Stackable, p.GeometrySource = req.Stackable, req.GeometrySource

	if err := v.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

// optQuantity parses an optional decimal-string quantity column, zero or
// negative refused (a board measure is positive).
func optQuantity(v *httpx.Validator, field string, raw *json.RawMessage) *httpx.Quantity {
	if raw == nil || string(*raw) == "null" {
		return nil
	}
	q, ok := v.Quantity(field, *raw, true)
	if !ok {
		return nil
	}
	v.Check(q > 0, field, "must be greater than zero")
	return &q
}

// marginsRequest is the PATCH /products/{id}/margins body.
type marginsRequest struct {
	TargetMargin   *float64 `json:"target_margin"`
	CommissionRate *float64 `json:"commission_rate"`
	Revision       *int64   `json:"revision"`
}

func parseMargins(r *http.Request) (targetMargin, commissionRate float64, revision *int64, err error) {
	var req marginsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return 0, 0, nil, err
	}
	v := &httpx.Validator{}
	targetMargin, commissionRate = 0, 0
	if req.TargetMargin != nil {
		targetMargin = *req.TargetMargin
		v.Check(targetMargin >= 0, "target_margin", "must be zero or positive")
	}
	if req.CommissionRate != nil {
		commissionRate = *req.CommissionRate
		v.Check(commissionRate >= 0, "commission_rate", "must be zero or positive")
	}
	return targetMargin, commissionRate, req.Revision, v.Err()
}

// leadTimeRequest is the PATCH /products/{id}/lead-time body.
type leadTimeRequest struct {
	LeadTimeDays *int   `json:"lead_time_days"`
	Revision     *int64 `json:"revision"`
}

func parseLeadTime(r *http.Request) (days *int, revision *int64, err error) {
	var req leadTimeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, nil, err
	}
	return req.LeadTimeDays, req.Revision, nil
}

// dimensionsRequest is the PATCH /products/{id}/dimensions body. Every field
// is a pointer so a null reaches the column as SQL NULL (the geometry
// contract).
type dimensionsRequest struct {
	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource *string  `json:"geometry_source"`
	Revision       *int64   `json:"revision"`
}

func parseDimensions(r *http.Request) (Geometry, *int64, error) {
	var req dimensionsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return Geometry{}, nil, err
	}
	v := &httpx.Validator{}
	checkDim := func(name string, f *float64) {
		if f != nil {
			v.Check(*f >= 0, name, "must be zero or positive")
		}
	}
	checkDim("length_in", req.LengthIn)
	checkDim("width_in", req.WidthIn)
	checkDim("height_in", req.HeightIn)
	return Geometry{
		LengthIn:       req.LengthIn,
		WidthIn:        req.WidthIn,
		HeightIn:       req.HeightIn,
		Stackable:      req.Stackable,
		GeometrySource: req.GeometrySource,
	}, req.Revision, v.Err()
}
