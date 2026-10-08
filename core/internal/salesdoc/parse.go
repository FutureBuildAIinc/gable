// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// LineRequest is one line of a sales document's create or edit body. Every
// field that carries a number or an identifier decodes loosely and is parsed
// by ParseLines, so a bad value is a field error with its full path
// (lines[2].quantity) collected with every other into the request's one 400
// (ADR 0001 section 4). A kit or component line is never a request line: the
// server writes both when a line names a kit product (Explode).
type LineRequest struct {
	ID               *string         `json:"id"`
	LineType         *string         `json:"line_type"`
	ProductID        *string         `json:"product_id"`
	ChargeCode       *string         `json:"charge_code"`
	SKU              *string         `json:"sku"`
	Description      *string         `json:"description"`
	Quantity         json.RawMessage `json:"quantity"`
	UOM              *string         `json:"uom"`
	PriceUOM         *string         `json:"price_uom"`
	UOMQty           json.RawMessage `json:"uom_qty"`
	PriceUOMQty      json.RawMessage `json:"price_uom_qty"`
	UnitPriceTenThou json.RawMessage `json:"unit_price_ten_thousandths"`
	OverrideReason   *string         `json:"override_reason"`
	DiscountPercent  json.RawMessage `json:"discount_percent"`
	DiscountCents    json.RawMessage `json:"discount_cents"`
	DiscountReason   *string         `json:"discount_reason"`
	Taxable          *bool           `json:"taxable"`
	IsSpecialOrder   *bool           `json:"is_special_order"`
	VendorID         *string         `json:"vendor_id"`
	SpecialOrderCost json.RawMessage `json:"special_order_unit_cost_ten_thousandths"`
}

// ParsedLine is one validated request line: the numbers parsed, the defaults
// the parse owns applied. Defaults that need the product or the charge code
// (the unit, the description, the price of a charge) are filled by the
// document's pricing step.
type ParsedLine struct {
	ID               *uuid.UUID
	LineType         LineType
	ProductID        *uuid.UUID
	ChargeCode       string
	SKU              string
	Description      string
	Quantity         httpx.Quantity
	UOM              string // empty: default from the product
	PriceUOM         string
	UOMQty           httpx.Quantity
	PriceUOMQty      httpx.Quantity
	UnitPrice        *httpx.Price // nil: the engine (a product) or the code (a charge) fills it
	OverrideReason   string
	DiscountPercent  *httpx.Quantity
	DiscountAmount   *httpx.Cents
	DiscountReason   string
	Taxable          *bool // a charge line's override of its code's flag
	IsSpecialOrder   bool
	VendorID         *uuid.UUID
	SpecialOrderCost *httpx.Price
}

// chargeCodePattern is a charge code: one to sixteen uppercase letters,
// digits or underscores (ADR 0005 section 2.5).
var chargeCodePattern = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)

// priceUOMPattern is what a price unit looks like: a code of one to six
// capital letters, never free text (a price per M or per CWT is real).
var priceUOMPattern = regexp.MustCompile(`^[A-Z]{1,6}$`)

// ParseLines validates a request's lines in one pass. v is the request's
// validator: every field problem, of the header or of any line, collects
// into its one 400. The rules are the line shape of ADR 0005 section 2:
// the request vocabulary is product, charge and text lines; the conversion
// pair and the price unit follow ADR 0001 section 7a with the cycle 2
// carried rule that a pair whose units agree is 1 and 1, never anything
// else; a price on a stocked product line is an override that names its
// reason; a discount is one of percent or amount, never both, with its
// reason.
func ParseLines(v *httpx.Validator, lines []LineRequest) []ParsedLine {
	out := make([]ParsedLine, 0, len(lines))
	for i := range lines {
		if line, ok := lines[i].parse(v, fmt.Sprintf("lines[%d]", i)); ok {
			out = append(out, line)
		}
	}
	return out
}

func isAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

func (l *LineRequest) parse(v *httpx.Validator, path string) (ParsedLine, bool) {
	var d ParsedLine
	before := len(detailsOf(v))

	// The line type: sent or derived. A kit or a component is the server's
	// to write (Explode), never a client's.
	if l.LineType != nil {
		switch *l.LineType {
		case "product", "charge", "text":
			d.LineType = LineType(strings.ToUpper(*l.LineType))
		case "kit", "component":
			v.Check(false, path+".line_type", "is written by the server when a line names a kit product; send a product line")
		default:
			v.Check(false, path+".line_type", "must be one of: product, charge, text")
		}
	} else {
		switch {
		case l.ChargeCode != nil:
			d.LineType = LineCharge
		case l.ProductID != nil || !isAbsent(l.Quantity) || !isAbsent(l.UnitPriceTenThou):
			d.LineType = LineProduct
		default:
			d.LineType = LineText
		}
	}
	if d.LineType == "" {
		return d, false
	}

	if d.LineType == LineText {
		return l.parseText(v, path, d, before)
	}

	if id, ok := v.UUID(path+".id", l.ID, false); ok {
		d.ID = &id
	}
	if d.LineType == LineCharge {
		if l.ProductID != nil {
			v.Check(false, path+".product_id", "belongs to a product line; a charge line names a charge_code")
		}
		if l.ChargeCode == nil || strings.TrimSpace(*l.ChargeCode) == "" {
			v.Check(false, path+".charge_code", "is required on a charge line")
		} else {
			code := strings.TrimSpace(*l.ChargeCode)
			v.Check(chargeCodePattern.MatchString(code), path+".charge_code",
				"must be a charge code of one to sixteen capital letters, digits or underscores")
			d.ChargeCode = code
		}
		d.Taxable = l.Taxable
	} else {
		if l.ChargeCode != nil {
			v.Check(false, path+".charge_code", "belongs to a charge line")
		}
		if l.Taxable != nil {
			// A product's taxability comes from the product (products.taxable);
			// a non stock line takes the request's flag.
			if l.ProductID != nil {
				v.Check(false, path+".taxable", "comes from the product; a non stock line may send it")
			} else {
				d.Taxable = l.Taxable
			}
		}
		if id, ok := v.UUID(path+".product_id", l.ProductID, false); ok {
			d.ProductID = &id
		}
	}
	if l.SKU != nil {
		d.SKU = strings.TrimSpace(*l.SKU)
	}
	if l.Description != nil {
		d.Description = strings.TrimSpace(*l.Description)
	}
	// A line that names a product defaults its description and sku from it;
	// every other line carries its own.
	if d.ProductID == nil {
		v.Required(path+".description", d.Description)
	}

	qty, qtyOK := v.Quantity(path+".quantity", l.Quantity, true)
	if qtyOK {
		httpx.CheckLineSign(v, path, qty, 0, false)
		v.Check(qty > 0, path+".quantity", "must be greater than zero on a sales document")
		d.Quantity = qty
	}

	// The unit the quantity is in: a product line may leave it to the
	// product's own unit; a charge line defaults EA below.
	if l.UOM == nil || strings.TrimSpace(*l.UOM) == "" {
		if d.LineType == LineCharge {
			d.UOM = "EA"
		} else {
			v.Check(d.ProductID != nil, path+".uom", "is required when the line names no product")
		}
	} else {
		d.UOM = strings.TrimSpace(*l.UOM)
		v.Check(priceUOMPattern.MatchString(d.UOM), path+".uom", "must be a unit code of one to six capital letters, for example PCS, EA or MBF")
	}

	// The unit the price is per, and the conversion pair (ADR 0001 section
	// 7a): both sides together or neither; price_uom defaults to uom; a
	// different price_uom requires the pair; and, the rule carried into
	// cycle 2, when price_uom equals uom the pair is 1 and 1, so any other
	// pair on equal units is a 400 on uom_qty.
	d.PriceUOM = d.UOM
	if l.PriceUOM != nil {
		if strings.TrimSpace(*l.PriceUOM) == "" {
			v.Check(false, path+".price_uom", "must not be empty")
		} else {
			d.PriceUOM = strings.TrimSpace(*l.PriceUOM)
			v.Check(priceUOMPattern.MatchString(d.PriceUOM), path+".price_uom",
				"must be a unit code of one to six capital letters, for example MBF, M or CWT")
		}
	}
	uq, uqOK := v.Quantity(path+".uom_qty", l.UOMQty, false)
	pq, pqOK := v.Quantity(path+".price_uom_qty", l.PriceUOMQty, false)
	switch {
	case uqOK && pqOK:
		v.Check(uq > 0, path+".uom_qty", "must be greater than zero")
		v.Check(pq > 0, path+".price_uom_qty", "must be greater than zero")
		if d.PriceUOM == d.UOM && (uq != One || pq != One) {
			v.Check(false, path+".uom_qty", "must be 1 when price_uom equals uom: the units agree, so the pair is 1 and 1")
		}
		d.UOMQty, d.PriceUOMQty = uq, pq
	case uqOK != pqOK:
		missing := path + ".uom_qty"
		if uqOK {
			missing = path + ".price_uom_qty"
		}
		v.Check(false, missing, "is required: the conversion is a pair, uom_qty and price_uom_qty together")
	case d.UOM != "" && d.PriceUOM != d.UOM:
		v.Check(false, path+".uom_qty", "is required when price_uom differs from uom: send uom_qty and price_uom_qty")
	}

	// The price. Absent means the document prices the line (a stocked
	// product through the engine, a charge from its code's default); present
	// it is an override on a stocked product line (ADR 0005 section 2.3),
	// which names its reason, and simply the price on a non stock or charge
	// line.
	if price, ok := v.Int(path+".unit_price_ten_thousandths", l.UnitPriceTenThou, false); ok {
		v.Check(price >= 0, path+".unit_price_ten_thousandths", "a unit price is never negative")
		p := httpx.Price(price)
		d.UnitPrice = &p
	}
	if l.OverrideReason != nil {
		reason := strings.TrimSpace(*l.OverrideReason)
		if reason != "" {
			v.Check(len(reason) <= 500, path+".override_reason", "must be at most 500 characters")
			d.OverrideReason = reason
		}
	}
	switch {
	case d.UnitPrice != nil && d.OverrideReason == "" && d.LineType == LineProduct && d.ProductID != nil:
		v.Check(false, path+".override_reason", "is required when a stocked product line sends unit_price_ten_thousandths: a price beside the engine's is an override")
	case d.UnitPrice == nil && d.OverrideReason != "":
		v.Check(false, path+".override_reason", "is an override's reason: send it with unit_price_ten_thousandths")
	case d.UnitPrice == nil && d.LineType == LineProduct && d.ProductID == nil:
		v.Check(false, path+".unit_price_ten_thousandths", "is required on a non stock line: there is no product to price it from")
	}

	// The discount: one of percent or amount, never both, each with its
	// reason (ADR 0005 section 2.3).
	dp, dpOK := v.Quantity(path+".discount_percent", l.DiscountPercent, false)
	dc, dcOK := v.Int(path+".discount_cents", l.DiscountCents, false)
	if dpOK {
		v.Check(dp > 0 && dp <= 100*One, path+".discount_percent", "must be greater than 0 and at most 100")
	}
	if dcOK {
		v.Check(dc > 0, path+".discount_cents", "must be greater than zero")
	}
	switch {
	case dpOK && dcOK:
		v.Check(false, path+".discount_percent", "and discount_cents are one discount: send one, never both")
	case dpOK:
		d.DiscountPercent = &dp
	case dcOK:
		c := httpx.Cents(dc)
		d.DiscountAmount = &c
	}
	if (dpOK || dcOK) && strings.TrimSpace(deref(l.DiscountReason)) == "" {
		v.Check(false, path+".discount_reason", "is required with a discount")
	}
	if l.DiscountReason != nil {
		d.DiscountReason = strings.TrimSpace(*l.DiscountReason)
	}

	if l.IsSpecialOrder != nil {
		d.IsSpecialOrder = *l.IsSpecialOrder
	}
	if d.IsSpecialOrder && d.LineType != LineProduct {
		v.Check(false, path+".is_special_order", "belongs to a product line")
	}
	if id, ok := v.UUID(path+".vendor_id", l.VendorID, false); ok {
		d.VendorID = &id
	}
	if !isAbsent(l.SpecialOrderCost) {
		if n, ok := v.Int(path+".special_order_unit_cost_ten_thousandths", l.SpecialOrderCost, true); ok {
			v.Check(n >= 0, path+".special_order_unit_cost_ten_thousandths", "a unit cost is never negative")
			p := httpx.Price(n)
			d.SpecialOrderCost = &p
		}
	}

	return d, len(detailsOf(v)) == before
}

// parseText validates a text line: a note on the document, carrying only its
// description, its position and its identity (ADR 0005 section 2.1). Every
// priced field it sends is refused, so a text line with a price cannot be
// stored.
func (l *LineRequest) parseText(v *httpx.Validator, path string, d ParsedLine, before int) (ParsedLine, bool) {
	if id, ok := v.UUID(path+".id", l.ID, false); ok {
		d.ID = &id
	}
	if l.Description == nil || strings.TrimSpace(*l.Description) == "" {
		v.Check(false, path+".description", "is required on a text line")
	} else {
		d.Description = strings.TrimSpace(*l.Description)
	}
	for _, f := range []struct {
		name    string
		present bool
	}{
		{"product_id", l.ProductID != nil},
		{"charge_code", l.ChargeCode != nil},
		{"quantity", !isAbsent(l.Quantity)},
		{"uom", l.UOM != nil && strings.TrimSpace(*l.UOM) != ""},
		{"price_uom", l.PriceUOM != nil && strings.TrimSpace(*l.PriceUOM) != ""},
		{"uom_qty", !isAbsent(l.UOMQty)},
		{"price_uom_qty", !isAbsent(l.PriceUOMQty)},
		{"unit_price_ten_thousandths", !isAbsent(l.UnitPriceTenThou)},
		{"override_reason", l.OverrideReason != nil && strings.TrimSpace(*l.OverrideReason) != ""},
		{"discount_percent", !isAbsent(l.DiscountPercent)},
		{"discount_cents", !isAbsent(l.DiscountCents)},
		{"discount_reason", l.DiscountReason != nil && strings.TrimSpace(*l.DiscountReason) != ""},
		{"taxable", l.Taxable != nil},
		{"is_special_order", l.IsSpecialOrder != nil},
		{"vendor_id", l.VendorID != nil},
		{"special_order_unit_cost_ten_thousandths", !isAbsent(l.SpecialOrderCost)},
	} {
		v.Check(!f.present, path+"."+f.name, "is a priced field: a text line carries only a description")
	}
	return d, len(detailsOf(v)) == before
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// detailsOf reads how many field errors the validator holds.
func detailsOf(v *httpx.Validator) []httpx.FieldError {
	err := v.Err()
	if err == nil {
		return nil
	}
	if he, ok := err.(*httpx.Error); ok {
		return he.Details
	}
	return nil
}
