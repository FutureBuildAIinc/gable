// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// TransitionRequest is the body of POST /invoices/{id}/transitions and
// POST /credit-memos/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
	// Reason is the void's reason (required for a void).
	Reason *string `json:"reason"`
}

// CreditRequest is the JSON body of POST /credit-memos and PUT
// /credit-memos/{id}. Identifiers and numbers decode loosely and are parsed
// by Parse, so every problem is one field error with its full path, collected
// into the request's one 400 (ADR 0001 section 4).
type CreditRequest struct {
	BranchID   *string             `json:"branch_id"`
	CustomerID *string             `json:"customer_id"`
	InvoiceID  *string             `json:"invoice_id"`
	JobID      *string             `json:"job_id"`
	ShipToID   *string             `json:"ship_to_id"`
	ReasonCode *string             `json:"reason_code"`
	Reason     *string             `json:"reason"`
	Revision   json.RawMessage     `json:"revision"`
	Lines      []CreditLineRequest `json:"lines"`
}

// CreditLineRequest is one credit memo line. A line that names an invoice
// line (invoice_line_id) takes its price, pair and discount from it and sends
// only the quantity credited, whether it goes back to stock and, optionally,
// a description. A free line (a price adjustment, a fee given back, a note)
// names a product, a charge code or only a description, with its own price.
// Quantities are negative (ADR 0001 section 7a).
type CreditLineRequest struct {
	ID               *string         `json:"id"`
	InvoiceLineID    *string         `json:"invoice_line_id"`
	LineType         *string         `json:"line_type"`
	ProductID        *string         `json:"product_id"`
	ChargeCode       *string         `json:"charge_code"`
	Description      *string         `json:"description"`
	Quantity         json.RawMessage `json:"quantity"`
	UOM              *string         `json:"uom"`
	UnitPriceTenThou json.RawMessage `json:"unit_price_ten_thousandths"`
	Taxable          *bool           `json:"taxable"`
	Restock          *bool           `json:"restock"`
}

// CreditInput is a validated request ready for the service to build.
type CreditInput struct {
	BranchID   *uuid.UUID
	CustomerID *uuid.UUID
	InvoiceID  *uuid.UUID
	JobID      *uuid.UUID
	ShipToID   *uuid.UUID
	ReasonCode ReasonCode
	Reason     string
	Revision   *int64
	Lines      []CreditLineInput
}

// CreditLineInput is one validated credit line.
type CreditLineInput struct {
	ID            *uuid.UUID
	InvoiceLineID *uuid.UUID
	LineType      salesdoc.LineType
	ProductID     *uuid.UUID
	ChargeCode    string
	Description   string
	Quantity      httpx.Quantity // negative
	UOM           string
	UnitPrice     *httpx.Price
	Taxable       *bool
	Restock       bool
}

var (
	chargeCodePattern = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)
	unitPattern       = regexp.MustCompile(`^[A-Z]{1,6}$`)
)

// Parse validates a create request in one pass.
func (req *CreditRequest) Parse() (*CreditInput, error) { return req.parse(false) }

// ParseUpdate is Parse for PUT /credit-memos/{id}: a PUT replaces the header
// fields and the lines and applies nothing else, so a field it does not apply
// is a 400 naming it, never a silent drop.
func (req *CreditRequest) ParseUpdate() (*CreditInput, error) { return req.parse(true) }

func isAbsentRaw(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

func (req *CreditRequest) parse(update bool) (*CreditInput, error) {
	v := &httpx.Validator{}
	d := &CreditInput{}

	if update {
		v.Check(req.BranchID == nil, "branch_id", "cannot be changed by an edit: it is fixed when the credit memo is created")
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			d.Revision = &n
		}
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new credit memo has no revision to precondition on")
	}
	if id, ok := v.UUID("branch_id", req.BranchID, false); ok && !update {
		d.BranchID = &id
	}
	if id, ok := v.UUID("customer_id", req.CustomerID, false); ok {
		d.CustomerID = &id
	}
	if id, ok := v.UUID("invoice_id", req.InvoiceID, false); ok {
		d.InvoiceID = &id
	}
	if !update && d.CustomerID == nil && d.InvoiceID == nil && req.CustomerID == nil && req.InvoiceID == nil {
		v.Check(false, "customer_id", "is required when the credit memo names no invoice")
	}
	if id, ok := v.UUID("job_id", req.JobID, false); ok {
		d.JobID = &id
	}
	if id, ok := v.UUID("ship_to_id", req.ShipToID, false); ok {
		d.ShipToID = &id
	}
	if req.ReasonCode == nil {
		v.Check(false, "reason_code", "is required")
	} else if rc, ok := ParseReasonCode(*req.ReasonCode); ok {
		d.ReasonCode = rc
	} else {
		v.Check(false, "reason_code", "must be one of: "+strings.Join(reasonCodeNames, ", "))
	}
	if req.Reason == nil || strings.TrimSpace(*req.Reason) == "" {
		v.Check(false, "reason", "is required")
	} else {
		d.Reason = strings.TrimSpace(*req.Reason)
		v.Check(len(d.Reason) <= 500, "reason", "must be at most 500 characters")
	}

	credited := 0
	seen := map[uuid.UUID]bool{}
	for i := range req.Lines {
		path := fmt.Sprintf("lines[%d]", i)
		line, ok := req.Lines[i].parse(v, path)
		if !ok {
			continue
		}
		if line.InvoiceLineID != nil {
			if seen[*line.InvoiceLineID] {
				v.Check(false, path+".invoice_line_id", "is named twice: send one line for each invoice line")
			}
			seen[*line.InvoiceLineID] = true
		}
		if line.LineType != salesdoc.LineText {
			credited++
		}
		d.Lines = append(d.Lines, line)
	}
	if credited == 0 && len(d.Lines) == len(req.Lines) {
		v.Check(false, "lines", "needs at least one credited line")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func (l *CreditLineRequest) parse(v *httpx.Validator, path string) (CreditLineInput, bool) {
	var d CreditLineInput
	ok := true
	fail := func(field, msg string) {
		v.Check(false, path+"."+field, msg)
		ok = false
	}
	if id, idOK := v.UUID(path+".id", l.ID, false); idOK {
		d.ID = &id
	} else if l.ID != nil {
		ok = false
	}
	if l.Description != nil {
		d.Description = strings.TrimSpace(*l.Description)
	}

	// A line that names an invoice line: everything but the quantity, the
	// restock flag and a description comes from the invoice line.
	if l.InvoiceLineID != nil {
		id, idOK := v.UUID(path+".invoice_line_id", l.InvoiceLineID, true)
		if !idOK {
			ok = false
		}
		d.InvoiceLineID = &id
		for _, f := range []struct {
			name    string
			present bool
		}{
			{"line_type", l.LineType != nil}, {"product_id", l.ProductID != nil}, {"charge_code", l.ChargeCode != nil},
			{"uom", l.UOM != nil && strings.TrimSpace(*l.UOM) != ""},
			{"unit_price_ten_thousandths", !isAbsentRaw(l.UnitPriceTenThou)}, {"taxable", l.Taxable != nil},
		} {
			if f.present {
				fail(f.name, "comes from the invoice line: send only the quantity, restock and a description")
			}
		}
		qty, qtyOK := v.Quantity(path+".quantity", l.Quantity, true)
		if qtyOK {
			if qty >= 0 {
				fail("quantity", "must be negative on a credit memo line: it is the quantity returned")
			}
			d.Quantity = qty
		} else {
			ok = false
		}
		d.Restock = l.Restock != nil && *l.Restock
		return d, ok
	}

	// A free line.
	if l.LineType != nil {
		switch *l.LineType {
		case "product", "charge", "text":
			d.LineType = salesdoc.LineType(strings.ToUpper(*l.LineType))
		case "kit", "component":
			fail("line_type", "a kit is credited through the invoice line that billed it; send product, charge or text")
		default:
			fail("line_type", "must be one of: product, charge, text")
		}
	} else {
		switch {
		case l.ChargeCode != nil:
			d.LineType = salesdoc.LineCharge
		case l.ProductID != nil || !isAbsentRaw(l.Quantity) || !isAbsentRaw(l.UnitPriceTenThou):
			d.LineType = salesdoc.LineProduct
		default:
			d.LineType = salesdoc.LineText
		}
	}
	if d.LineType == "" {
		return d, false
	}
	if d.LineType == salesdoc.LineText {
		if d.Description == "" {
			fail("description", "is required on a text line")
		}
		for _, f := range []struct {
			name    string
			present bool
		}{
			{"product_id", l.ProductID != nil}, {"charge_code", l.ChargeCode != nil}, {"quantity", !isAbsentRaw(l.Quantity)},
			{"uom", l.UOM != nil && strings.TrimSpace(*l.UOM) != ""},
			{"unit_price_ten_thousandths", !isAbsentRaw(l.UnitPriceTenThou)}, {"taxable", l.Taxable != nil}, {"restock", l.Restock != nil},
		} {
			if f.present {
				fail(f.name, "is a priced field: a text line carries only a description")
			}
		}
		return d, ok
	}

	if d.LineType == salesdoc.LineCharge {
		if l.ProductID != nil {
			fail("product_id", "belongs to a product line; a charge line names a charge_code")
		}
		if l.ChargeCode == nil || strings.TrimSpace(*l.ChargeCode) == "" {
			fail("charge_code", "is required on a charge line")
		} else {
			d.ChargeCode = strings.TrimSpace(*l.ChargeCode)
			if !chargeCodePattern.MatchString(d.ChargeCode) {
				fail("charge_code", "must be a charge code of one to sixteen capital letters, digits or underscores")
			}
		}
		if l.Restock != nil && *l.Restock {
			fail("restock", "a fee cannot go back to stock")
		}
	} else {
		if l.ChargeCode != nil {
			fail("charge_code", "belongs to a charge line")
		}
		if id, idOK := v.UUID(path+".product_id", l.ProductID, false); idOK {
			d.ProductID = &id
		} else if l.ProductID != nil {
			ok = false
		}
		if d.ProductID == nil {
			if d.Description == "" {
				fail("description", "is required when the line names no product")
			}
			if l.Restock != nil && *l.Restock {
				fail("restock", "needs a product: there is no stock to return a non stock line to")
			}
		}
		if l.Taxable != nil {
			if d.ProductID != nil {
				fail("taxable", "comes from the product; a non stock line may send it")
			} else {
				d.Taxable = l.Taxable
			}
		}
		d.Restock = l.Restock != nil && *l.Restock
	}
	qty, qtyOK := v.Quantity(path+".quantity", l.Quantity, true)
	if qtyOK {
		if qty >= 0 {
			fail("quantity", "must be negative on a credit memo line: it is the quantity credited")
		}
		d.Quantity = qty
	} else {
		ok = false
	}
	if l.UOM != nil && strings.TrimSpace(*l.UOM) != "" {
		d.UOM = strings.TrimSpace(*l.UOM)
		if !unitPattern.MatchString(d.UOM) {
			fail("uom", "must be a unit code of one to six capital letters, for example PCS, EA or MBF")
		}
	} else if d.LineType == salesdoc.LineProduct && d.ProductID == nil {
		fail("uom", "is required when the line names no product")
	}
	if price, pOK := v.Int(path+".unit_price_ten_thousandths", l.UnitPriceTenThou, false); pOK {
		if price < 0 {
			fail("unit_price_ten_thousandths", "a unit price is never negative")
		}
		p := httpx.Price(price)
		d.UnitPrice = &p
	} else if !isAbsentRaw(l.UnitPriceTenThou) {
		ok = false
	}
	if d.UnitPrice == nil && d.LineType == salesdoc.LineProduct {
		fail("unit_price_ten_thousandths", "is required on a free product line: the credit memo prices what it gives back")
	}
	return d, ok
}
