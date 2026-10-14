// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

// maxOriginalFile bounds the base64 decoded original upload.
const maxOriginalFile = 5 << 20

// unitCodeForm is what a unit of measure looks like on the wire: a code of
// one to six capital letters (the catalogue's own rule, ADR 0006 section
// 2.1). Whether the code is a unit of the catalogue, and for a product line
// a unit of the product's set, is the service's answer, which reads them.
var unitCodeForm = regexp.MustCompile(`^[A-Z]{1,6}$`)

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
	Tally                   *TallyRequest   `json:"tally"`
}

// TallyRequest is the tally a line carries (ADR 0006 section 4.3): the
// request carries only rows; thickness_in, width_in, linear_feet and
// board_feet are read only, and one in a request is a 400 naming the field
// (the decoder refuses it as a field the write does not apply).
type TallyRequest struct {
	Rows []TallyRowRequest `json:"rows"`
}

// TallyRowRequest is one length of a tally request.
type TallyRowRequest struct {
	Pieces   json.RawMessage `json:"pieces"`
	LengthFT json.RawMessage `json:"length_ft"`
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
	HasQuantity  bool // false when the quantity comes from the line's tally
	UOM          product.UOM
	PriceUOM     string
	UOMQty       httpx.Quantity
	PriceUOMQty  httpx.Quantity
	UnitPrice    httpx.Price
	Tally        *DraftTally
}

// DraftTally is the tally of a validated line: the rows the client sent,
// parsed (ADR 0006 section 4.3); the derived fields are the service's.
type DraftTally struct {
	Rows []DraftTallyRow
}

// DraftTallyRow is one parsed row of a tally request.
type DraftTallyRow struct {
	Pieces   int
	LengthFT httpx.Quantity
}

// priceUOMCode is what a price unit looks like. It is not limited to the sale
// unit enum (a price per M or per CWT is real), but it is a code, never free
// text.
var priceUOMCode = regexp.MustCompile(`^[A-Z]{1,6}$`)

// one is the quantity 1 at the wire scale.
const one httpx.Quantity = 1 * 10000

// Parse validates the request in one pass and returns the Draft, or the one
// 400 carrying every offending field. Unit of measure rule: a line that names
// a product and no uom takes the product's unit (the service fills it); only a
// line with neither is a 400 naming lines[i].uom, never a database cast fault.
func (req *Request) Parse() (*Draft, error) { return req.parse(false) }

// ParseUpdate is Parse for PUT /quotes/{id}. A PUT replaces the header fields
// and the lines and applies nothing else, so a field it does not apply (the
// quote's branch, source, margin, original upload or parse map, all fixed at
// create) is a 400 naming it rather than a silent drop.
func (req *Request) ParseUpdate() (*Draft, error) { return req.parse(true) }

func (req *Request) parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{DeliveryType: DeliveryPickup, Source: "manual"}

	if update {
		for _, f := range []struct {
			name    string
			present bool
		}{
			{"branch_id", req.BranchID != nil},
			{"source", req.Source != nil},
			{"margin_total_cents", !isAbsentRaw(req.MarginTotalCents)},
			{"original_file", req.OriginalFile != nil},
			{"original_filename", req.OriginalFilename != nil},
			{"original_content_type", req.OriginalContentType != nil},
			{"parse_map", !isAbsentRaw(req.ParseMap)},
		} {
			v.Check(!f.present, f.name, "cannot be changed by an edit: it is fixed when the quote is created")
		}
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new quote has no revision to precondition on")
	}

	if id, ok := v.UUID("branch_id", req.BranchID, false); ok && !update {
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
	if req.Source != nil && !update {
		v.Enum("source", *req.Source, "manual", "ai")
		d.Source = *req.Source
	}
	if n, ok := v.Int("freight_cents", req.FreightCents, false); ok {
		v.Check(n >= 0, "freight_cents", "must not be negative")
		d.FreightCents = httpx.Cents(n)
	}
	if n, ok := v.Int("margin_total_cents", req.MarginTotalCents, false); ok && !update {
		d.MarginTotalCents = httpx.Cents(n)
	}
	if update {
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			d.Revision = &n
		}
	}
	if req.OriginalFilename != nil && !update {
		d.OriginalFilename = *req.OriginalFilename
	}
	if req.OriginalContentType != nil && !update {
		d.OriginalContentType = *req.OriginalContentType
	}
	if req.OriginalFile != nil && *req.OriginalFile != "" && !update {
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
	if !isAbsentRaw(req.ParseMap) && !update {
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

	// The quantity may be omitted on a tallied line: the tally's linear feet
	// are the quantity (ADR 0006 section 4.3). Anywhere else it is required.
	hasTally := l.Tally != nil
	qty, qtyOK := v.Quantity(path+".quantity", l.Quantity, !hasTally)
	if qtyOK {
		v.Check(qty != 0, path+".quantity", "must not be zero")
		d.Quantity, d.HasQuantity = qty, true
	}

	// The unit the quantity is in. A line that names a product may leave it
	// out: the service takes the product's sale unit (ADR 0006 section 3.3).
	// A line with neither is the one 400 on lines[i].uom. The code's form is
	// checked here; the catalogue and the product's set answer in the
	// service, which reads them (ADR 0006 section 7.4).
	if l.UOM == nil || strings.TrimSpace(*l.UOM) == "" {
		v.Check(d.ProductID != nil, path+".uom", "is required when the line names no product")
	} else {
		d.UOM = product.UOM(*l.UOM)
		v.Check(unitCodeForm.MatchString(*l.UOM), path+".uom",
			"must be a unit code of one to six capital letters, for example PCS, MBF or M")
	}

	// The unit the price is quoted per. On a line that names no product it is
	// the uom unless the line says otherwise; on a product line an absent
	// price_uom stays empty and the service takes the product's own price
	// unit (ADR 0006 section 3.3, replacing R1-15's default from the uom).
	if d.ProductID == nil {
		d.PriceUOM = string(d.UOM)
	}
	if l.PriceUOM != nil {
		if strings.TrimSpace(*l.PriceUOM) == "" {
			v.Check(false, path+".price_uom", "must not be empty")
		} else {
			v.Check(priceUOMCode.MatchString(*l.PriceUOM), path+".price_uom",
				"must be a unit code of one to six capital letters, for example MBF, M or CWT")
			d.PriceUOM = *l.PriceUOM
		}
	}

	// The conversion pair. A line that names a product omits it: the server
	// resolves it from the product's unit set and stores it (ADR 0006 section
	// 3.3), and a sent pair must equal the resolved one as a ratio, which the
	// service checks. A line without a product carries the client's pair,
	// required when the two units differ unless the service can derive it
	// from two standard sizes.
	uq, uqOK := v.Quantity(path+".uom_qty", l.UOMQty, false)
	pq, pqOK := v.Quantity(path+".price_uom_qty", l.PriceUOMQty, false)
	switch {
	case uqOK && pqOK:
		v.Check(uq > 0, path+".uom_qty", "must be greater than zero")
		v.Check(pq > 0, path+".price_uom_qty", "must be greater than zero")
		// The rule carried into cycle 2 from R1-15's review: when the price
		// unit is the sale unit the pair is 1 and 1, never anything else.
		if d.PriceUOM == string(d.UOM) && d.UOM != "" && (uq != one || pq != one) {
			v.Check(false, path+".uom_qty", "must be 1 when price_uom equals uom: the units agree, so the pair is 1 and 1")
		}
		d.UOMQty, d.PriceUOMQty = uq, pq
	case uqOK != pqOK:
		missing := path + ".uom_qty"
		if uqOK {
			missing = path + ".price_uom_qty"
		}
		v.Check(false, missing, "is required: the conversion is a pair, uom_qty and price_uom_qty together")
	case d.UOM != "" && d.PriceUOM == string(d.UOM):
		d.UOMQty, d.PriceUOMQty = one, one
	}
	// With the uom still to default and no pair sent, UOMQty stays zero:
	// priceDraft settles it once the unit is known.

	// The tally's rows (ADR 0006 section 4.3): pieces are a count, the
	// lengths a positive decimal, at most 100 rows, one row per length.
	if l.Tally != nil {
		v.Check(len(l.Tally.Rows) > 0, path+".tally", "carries at least one row")
		v.Check(len(l.Tally.Rows) <= 100, path+".tally", "carries at most 100 rows")
		d.Tally = &DraftTally{}
		lengths := map[int64]bool{}
		for j, row := range l.Tally.Rows {
			rowPath := fmt.Sprintf("%s.tally.rows[%d]", path, j)
			rowBefore := errorCount(v)
			pieces, ok := v.Int(rowPath+".pieces", row.Pieces, true)
			if ok {
				v.Check(pieces > 0 && pieces <= 1000000, rowPath+".pieces", "is a count of pieces, 1 to 1000000")
			}
			length, ok := v.Quantity(rowPath+".length_ft", row.LengthFT, true)
			if ok {
				v.Check(length > 0, rowPath+".length_ft", "must be greater than zero")
				if lengths[int64(length)] {
					v.Check(false, rowPath+".length_ft", "repeats a length the tally already carries: one row per length")
				}
			}
			if errorCount(v) == rowBefore {
				d.Tally.Rows = append(d.Tally.Rows, DraftTallyRow{Pieces: int(pieces), LengthFT: length})
				lengths[int64(length)] = true
			}
		}
	}

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
