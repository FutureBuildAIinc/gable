// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Request bodies are decoded into DTOs whose fields are strings, pointers
// and raw JSON, never the final types, then parsed by Parse with an
// httpx.Validator, so a bad value is a field error with its full path
// (lines[1].quantity) collected with every other in one 400 (ADR 0001
// section 4).

// CreateRequest is the body of POST /api/v1/purchase-orders.
type CreateRequest struct {
	BranchID *string             `json:"branch_id"`
	VendorID *string             `json:"vendor_id"`
	Currency *string             `json:"currency"`
	Source   *string             `json:"source"`
	Revision json.RawMessage     `json:"revision"` // refused on a create
	Lines    []CreateLineRequest `json:"lines"`
}

// CreateLineRequest is one line of a CreateRequest.
type CreateLineRequest struct {
	ProductID              *string         `json:"product_id"`
	Description            *string         `json:"description"`
	Quantity               json.RawMessage `json:"quantity"`
	UOM                    *string         `json:"uom"`
	PriceUOM               *string         `json:"price_uom"`
	UOMQty                 json.RawMessage `json:"uom_qty"`
	PriceUOMQty            json.RawMessage `json:"price_uom_qty"`
	UnitCostTenThousandths json.RawMessage `json:"unit_cost_ten_thousandths"`
}

// CreateDraft is a validated create request, ready for the service.
type CreateDraft struct {
	BranchID *uuid.UUID
	VendorID uuid.UUID
	Currency string
	Source   Source
	Lines    []CreateLineDraft
}

// CreateLineDraft is one validated line. The unit hold (ADR 0008 section 1,
// the stocking unit while the unit catalogue has not landed) is the
// service's: it fills the product's unit and refuses another.
type CreateLineDraft struct {
	ProductID              *uuid.UUID
	Description            string
	Quantity               httpx.Quantity
	UOM                    string
	PriceUOM               string
	UOMQty                 httpx.Quantity
	PriceUOMQty            httpx.Quantity
	UnitCostTenThousandths httpx.Price
	LinkedSOLineID         *uuid.UUID
}

// ReceiveRequest is the body of POST /api/v1/purchase-orders/{id}/receive.
type ReceiveRequest struct {
	Revision json.RawMessage      `json:"revision"`
	Lines    []ReceiveLineRequest `json:"lines"`
}

// ReceiveLineRequest is one line of a ReceiveRequest. The quantity stays
// named qty_received on this route; C4-2 C's receipts route replaces it.
type ReceiveLineRequest struct {
	LineID      *string         `json:"line_id"`
	QtyReceived json.RawMessage `json:"qty_received"`
	LocationID  *string         `json:"location_id"`
}

// ReceiveDraft is a validated receive request.
type ReceiveDraft struct {
	Revision *int64
	Lines    []ReceiveLineDraft
}

// ReceiveLineDraft is one validated receive line.
type ReceiveLineDraft struct {
	LineID      uuid.UUID
	QtyReceived httpx.Quantity
	LocationID  uuid.UUID
}

// SubmitRequest is the body of POST /api/v1/purchase-orders/{id}/submit.
type SubmitRequest struct {
	Revision json.RawMessage `json:"revision"`
}

// uomCode is what a unit of measure looks like on this wire: a code, never
// free text (the pattern quotes' input uses).
var uomCode = regexp.MustCompile(`^[A-Z]{1,6}$`)

// currencyCode is an ISO 4217 code.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// one is the quantity 1 at the wire scale.
const one httpx.Quantity = 1 * 10000

// ParseCreate validates a create request into a CreateDraft. The unit hold
// needs the product's stocking unit, so the per line unit check happens in
// the service, inside the transaction; everything that needs no database is
// collected here.
func ParseCreate(req *CreateRequest) (*CreateDraft, error) {
	v := &httpx.Validator{}
	out := &CreateDraft{}
	if req.Revision != nil && !isAbsent(req.Revision) {
		v.Check(false, "revision", "a create has no revision to precondition on")
	}
	if req.VendorID == nil || *req.VendorID == "" {
		v.Check(false, "vendor_id", "is required")
	} else if id, ok := v.UUID("vendor_id", req.VendorID, true); ok {
		out.VendorID = id
	}
	if req.BranchID != nil && *req.BranchID != "" {
		if id, ok := v.UUID("branch_id", req.BranchID, true); ok {
			out.BranchID = &id
		}
	}
	out.Currency = "USD"
	if req.Currency != nil && *req.Currency != "" {
		if !currencyCode.MatchString(*req.Currency) {
			v.Check(false, "currency", "must be an ISO 4217 code")
		} else {
			out.Currency = *req.Currency
		}
	}
	switch {
	case req.Source == nil || *req.Source == "":
		out.Source = SourceManual
	case *req.Source == "manual":
		out.Source = SourceManual
	case *req.Source == "reorder":
		out.Source = SourceReorder
	case *req.Source == "a2a":
		out.Source = SourceA2A
	default:
		v.Check(false, "source", "must be one of manual, reorder, special_order, a2a")
		out.Source = SourceManual
	}
	if len(req.Lines) == 0 {
		v.Check(false, "lines", "at least one line is required")
	}
	for i := range req.Lines {
		l := &req.Lines[i]
		path := fmt.Sprintf("lines[%d]", i)
		d := CreateLineDraft{}
		if l.ProductID != nil && *l.ProductID != "" {
			if id, ok := v.UUID(path+".product_id", l.ProductID, true); ok {
				d.ProductID = &id
			}
		}
		if l.Description != nil {
			d.Description = *l.Description
		}
		if d.Description == "" && d.ProductID == nil {
			v.Check(false, path+".description", "is required on a line with no product")
		}
		if len(l.Quantity) == 0 || isAbsent(l.Quantity) {
			v.Check(false, path+".quantity", "is required")
		} else if q, ok := v.Quantity(path+".quantity", l.Quantity, true); ok {
			if q <= 0 {
				v.Check(false, path+".quantity", "must be positive")
			} else {
				d.Quantity = q
			}
		}
		if l.UOM != nil && *l.UOM != "" {
			if !uomCode.MatchString(*l.UOM) {
				v.Check(false, path+".uom", "must be a unit code of 1 to 6 uppercase letters")
			} else {
				d.UOM = *l.UOM
			}
		}
		if l.PriceUOM != nil && *l.PriceUOM != "" {
			if !uomCode.MatchString(*l.PriceUOM) {
				v.Check(false, path+".price_uom", "must be a unit code of 1 to 6 uppercase letters")
			} else {
				d.PriceUOM = *l.PriceUOM
			}
		}
		// The pair is both sides or neither; a price_uom different from the
		// uom requires it (the quotes parse's rule).
		uomQtyGiven := len(l.UOMQty) != 0 && !isAbsent(l.UOMQty)
		priceQtyGiven := len(l.PriceUOMQty) != 0 && !isAbsent(l.PriceUOMQty)
		if uomQtyGiven != priceQtyGiven {
			v.Check(false, path+".uom_qty", "the conversion is a pair: both uom_qty and price_uom_qty, or neither")
		}
		if uomQtyGiven {
			if q, ok := v.Quantity(path+".uom_qty", l.UOMQty, true); ok {
				if q <= 0 {
					v.Check(false, path+".uom_qty", "must be positive")
				} else {
					d.UOMQty = q
				}
			}
			if q, ok := v.Quantity(path+".price_uom_qty", l.PriceUOMQty, true); ok {
				if q <= 0 {
					v.Check(false, path+".price_uom_qty", "must be positive")
				} else {
					d.PriceUOMQty = q
				}
			}
		}
		if len(l.UnitCostTenThousandths) == 0 || isAbsent(l.UnitCostTenThousandths) {
			v.Check(false, path+".unit_cost_ten_thousandths", "is required")
		} else if n, ok := v.Int(path+".unit_cost_ten_thousandths", l.UnitCostTenThousandths, true); ok {
			if n < 0 {
				v.Check(false, path+".unit_cost_ten_thousandths", "must not be negative")
			} else {
				d.UnitCostTenThousandths = httpx.Price(n)
			}
		}
		out.Lines = append(out.Lines, d)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseReceive validates a receive request into a ReceiveDraft.
func ParseReceive(req *ReceiveRequest) (*ReceiveDraft, error) {
	v := &httpx.Validator{}
	out := &ReceiveDraft{}
	if len(req.Revision) != 0 && !isAbsent(req.Revision) {
		if n, ok := v.Int("revision", req.Revision, true); ok {
			out.Revision = &n
		}
	}
	if len(req.Lines) == 0 {
		v.Check(false, "lines", "at least one line is required")
	}
	for i := range req.Lines {
		l := &req.Lines[i]
		path := fmt.Sprintf("lines[%d]", i)
		d := ReceiveLineDraft{}
		if l.LineID == nil || *l.LineID == "" {
			v.Check(false, path+".line_id", "is required")
		} else if id, ok := v.UUID(path+".line_id", l.LineID, true); ok {
			d.LineID = id
		}
		if len(l.QtyReceived) == 0 || isAbsent(l.QtyReceived) {
			v.Check(false, path+".qty_received", "is required")
		} else if q, ok := v.Quantity(path+".qty_received", l.QtyReceived, true); ok {
			if q <= 0 {
				v.Check(false, path+".qty_received", "must be positive")
			} else {
				d.QtyReceived = q
			}
		}
		if l.LocationID == nil || *l.LocationID == "" {
			v.Check(false, path+".location_id", "is required")
		} else if id, ok := v.UUID(path+".location_id", l.LocationID, true); ok {
			d.LocationID = id
		}
		out.Lines = append(out.Lines, d)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseRevision reads a body's revision field into the precondition value.
func ParseRevision(raw json.RawMessage) (*int64, error) {
	if len(raw) == 0 || isAbsent(raw) {
		return nil, nil
	}
	v := &httpx.Validator{}
	if n, ok := v.Int("revision", raw, true); ok {
		if err := v.Err(); err != nil {
			return nil, err
		}
		return &n, nil
	}
	return nil, v.Err()
}

// isAbsent reports whether a raw JSON field was absent or null.
func isAbsent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	s := string(raw)
	return s == "null"
}

// errBranch wraps the validator's absent helper for the service's use.
var errUnknownLine = errors.New("purchase order line not found")
