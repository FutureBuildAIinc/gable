// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

// maxOriginalFile bounds the base64 decoded original upload.
const maxOriginalFile = 5 << 20

// uomCodes are the units of measure the quote_lines.uom enum holds (the
// standard codes keep their form: ADR 0001 section 6). A code outside it is
// a 400 at the boundary, not a database cast fault.
var uomCodes = []product.UOM{
	product.UOM_PCS, product.UOM_EA, product.UOM_LF, product.UOM_SF, product.UOM_BF, product.UOM_MBF,
	product.UOM_SQ, product.UOM_BOX, product.UOM_CTN, product.UOM_RL, product.UOM_GAL, product.UOM_LBS,
	product.UOM_BAG, product.UOM_BUNDLE, product.UOM_PAIR, product.UOM_SET,
}

func uomNames() []string {
	out := make([]string, len(uomCodes))
	for i, u := range uomCodes {
		out[i] = string(u)
	}
	return out
}

// Request is the JSON body of POST /quotes and PUT /quotes/{id}. Every field
// that carries a number, an identifier or a timestamp decodes loosely (a
// string, a pointer or raw JSON) and is parsed by Parse, so a bad value is a
// field error with its full path, lines[2].quantity, collected with every
// other in one 400 (ADR 0001 section 4), not a decoder's pathless failure.
type Request struct {
	BranchID            *string         `json:"branch_id"`
	CustomerID          *string         `json:"customer_id"`
	JobID               *string         `json:"job_id"`
	ExpiresAt           json.RawMessage `json:"expires_at"`
	DeliveryType        *string         `json:"delivery_type"`
	FreightCents        json.RawMessage `json:"freight_cents"`
	VehicleID           *string         `json:"vehicle_id"`
	Source              *string         `json:"source"`
	MarginTotalCents    json.RawMessage `json:"margin_total_cents"`
	OriginalFile        *string         `json:"original_file"` // base64
	OriginalFilename    *string         `json:"original_filename"`
	OriginalContentType *string         `json:"original_content_type"`
	ParseMap            json.RawMessage `json:"parse_map"`
	Revision            json.RawMessage `json:"revision"` // PUT: the precondition, beside If-Match
	Lines               []LineRequest   `json:"lines"`
}

// LineRequest is one line of a Request.
type LineRequest struct {
	ID                      *string         `json:"id"`
	ProductID               *string         `json:"product_id"`
	SKU                     *string         `json:"sku"`
	Description             *string         `json:"description"`
	CustomerNote            *string         `json:"customer_note"`
	Quantity                json.RawMessage `json:"quantity"`
	UOM                     *string         `json:"uom"`
	PriceUOM                *string         `json:"price_uom"`
	UOMQty                  json.RawMessage `json:"uom_qty"`
	PriceUOMQty             json.RawMessage `json:"price_uom_qty"`
	UnitPriceTenThousandths json.RawMessage `json:"unit_price_ten_thousandths"`
}

// Draft is a validated, normalized quote ready to price and store: the
// request with its numbers parsed and its defaults applied. Defaults that
// need the product (a line's sku and description) are filled by the service
// inside the transaction.
type Draft struct {
	BranchID            *uuid.UUID
	CustomerID          uuid.UUID
	JobID               *uuid.UUID
	ExpiresAt           *httpx.Timestamp
	DeliveryType        DeliveryType
	FreightCents        httpx.Cents
	VehicleID           *uuid.UUID
	Source              string
	MarginTotalCents    httpx.Cents
	OriginalFile        []byte
	OriginalFilename    string
	OriginalContentType string
	ParseMap            json.RawMessage
	Revision            *int64
	Lines               []DraftLine
}

// DraftLine is one validated line.
type DraftLine struct {
	ID           uuid.UUID // uuid.Nil for a new line
	ProductID    *uuid.UUID
	SKU          string
	Description  string
	CustomerNote string
	Quantity     httpx.Quantity
	UOM          product.UOM
	PriceUOM     string
	UOMQty       httpx.Quantity
	PriceUOMQty  httpx.Quantity
	UnitPrice    httpx.Price
}

// one is the quantity 1 at the wire scale.
const one httpx.Quantity = 1 * 10000

// Parse validates the request in one pass and returns the Draft, or the one
// 400 carrying every offending field. Unit of measure rule: a line that names
// a product and no uom takes the product's unit (the service fills it); only a
// line with neither is a 400 naming lines[i].uom, never a database cast fault.
func (req *Request) Parse() (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{DeliveryType: DeliveryPickup, Source: "manual"}

	if id, ok := v.UUID("branch_id", req.BranchID, false); ok {
		d.BranchID = &id
	}
	if id, ok := v.UUID("customer_id", req.CustomerID, true); ok {
		d.CustomerID = id
	}
	if id, ok := v.UUID("job_id", req.JobID, false); ok {
		d.JobID = &id
	}
	if id, ok := v.UUID("vehicle_id", req.VehicleID, false); ok {
		d.VehicleID = &id
	}
	if ts, ok := v.Timestamp("expires_at", req.ExpiresAt, false); ok {
		d.ExpiresAt = ts
	}

	if req.DeliveryType != nil {
		switch *req.DeliveryType {
		case "pickup":
			d.DeliveryType = DeliveryPickup
		case "delivery":
			d.DeliveryType = DeliveryDelivery
		default:
			v.Check(false, "delivery_type", "must be one of: pickup, delivery")
		}
	}
	if req.Source != nil {
		v.Enum("source", *req.Source, "manual", "ai")
		d.Source = *req.Source
	}
	if n, ok := v.Int("freight_cents", req.FreightCents, false); ok {
		v.Check(n >= 0, "freight_cents", "must not be negative")
		d.FreightCents = httpx.Cents(n)
	}
	if n, ok := v.Int("margin_total_cents", req.MarginTotalCents, false); ok {
		d.MarginTotalCents = httpx.Cents(n)
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	}
	if req.OriginalFilename != nil {
		d.OriginalFilename = *req.OriginalFilename
	}
	if req.OriginalContentType != nil {
		d.OriginalContentType = *req.OriginalContentType
	}
	if req.OriginalFile != nil && *req.OriginalFile != "" {
		data, err := base64.StdEncoding.DecodeString(*req.OriginalFile)
		switch {
		case err != nil:
			v.Check(false, "original_file", "must be standard base64")
		case len(data) > maxOriginalFile:
			v.Check(false, "original_file", "exceeds the 5 MB limit")
		default:
			d.OriginalFile = data
		}
	}
	if !isAbsentRaw(req.ParseMap) {
		if !json.Valid(req.ParseMap) {
			v.Check(false, "parse_map", "must be valid JSON")
		} else {
			d.ParseMap = req.ParseMap
		}
	}

	for i := range req.Lines {
		line, ok := req.Lines[i].parse(v, fmt.Sprintf("lines[%d]", i))
		if ok {
			d.Lines = append(d.Lines, line)
		}
	}

	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func isAbsentRaw(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

func (l *LineRequest) parse(v *httpx.Validator, path string) (DraftLine, bool) {
	var d DraftLine
	before := errorCount(v)

	if id, ok := v.UUID(path+".id", l.ID, false); ok {
		d.ID = id
	}
	if id, ok := v.UUID(path+".product_id", l.ProductID, false); ok {
		d.ProductID = &id
	}
	if l.SKU != nil {
		d.SKU = strings.TrimSpace(*l.SKU)
	}
	if l.Description != nil {
		d.Description = strings.TrimSpace(*l.Description)
	}
	if d.ProductID == nil {
		// A special order line has no product to default from.
		v.Required(path+".sku", d.SKU)
		v.Required(path+".description", d.Description)
	}
	if l.CustomerNote != nil {
		d.CustomerNote = *l.CustomerNote
	}

	qty, qtyOK := v.Quantity(path+".quantity", l.Quantity, true)
	if qtyOK {
		v.Check(qty != 0, path+".quantity", "must not be zero")
		d.Quantity = qty
	}

	// The unit the quantity is in. A line that names a product may leave it
	// out: the service takes the product's own unit (priceDraft). A line with
	// neither is the one 400 on lines[i].uom.
	if l.UOM == nil || strings.TrimSpace(*l.UOM) == "" {
		v.Check(d.ProductID != nil, path+".uom", "is required when the line names no product")
	} else {
		d.UOM = product.UOM(*l.UOM)
		known := false
		for _, u := range uomCodes {
			if u == d.UOM {
				known = true
			}
		}
		v.Check(known, path+".uom", "must be one of: "+strings.Join(uomNames(), ", "))
	}

	// The unit the price is quoted per: the uom unless the line says otherwise.
	// While the uom is still to default from the product, an absent price_uom
	// stays empty and priceDraft completes both.
	d.PriceUOM = string(d.UOM)
	if l.PriceUOM != nil {
		if strings.TrimSpace(*l.PriceUOM) == "" {
			v.Check(false, path+".price_uom", "must not be empty")
		} else {
			d.PriceUOM = *l.PriceUOM
		}
	}

	uq, uqOK := v.Quantity(path+".uom_qty", l.UOMQty, false)
	pq, pqOK := v.Quantity(path+".price_uom_qty", l.PriceUOMQty, false)
	switch {
	case uqOK && pqOK:
		v.Check(uq > 0, path+".uom_qty", "must be greater than zero")
		v.Check(pq > 0, path+".price_uom_qty", "must be greater than zero")
		d.UOMQty, d.PriceUOMQty = uq, pq
	case uqOK != pqOK:
		missing := path + ".uom_qty"
		if uqOK {
			missing = path + ".price_uom_qty"
		}
		v.Check(false, missing, "is required: the conversion is a pair, uom_qty and price_uom_qty together")
	case d.UOM != "" && d.PriceUOM != string(d.UOM):
		v.Check(false, path+".uom_qty", "is required when price_uom differs from uom: send uom_qty and price_uom_qty")
	case d.UOM != "":
		d.UOMQty, d.PriceUOMQty = one, one
	}
	// With the uom still to default and no pair sent, UOMQty stays zero:
	// priceDraft settles it once the unit is known.

	if price, ok := v.Int(path+".unit_price_ten_thousandths", l.UnitPriceTenThousandths, true); ok {
		httpx.CheckLineSign(v, path, d.Quantity, httpx.Price(price), false)
		d.UnitPrice = httpx.Price(price)
	}

	return d, errorCount(v) == before
}

// errorCount reads how many field errors the validator holds.
func errorCount(v *httpx.Validator) int {
	err := v.Err()
	if err == nil {
		return 0
	}
	return len(err.(*httpx.Error).Details)
}
