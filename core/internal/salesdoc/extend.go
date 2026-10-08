// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"fmt"
	"math/big"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ExtendLine computes one line's extension, the one rule of ADR 0005
// section 2.4: no discount through httpx.Extend; a percent discount through
// httpx.ExtendDiscounted (the exact product scaled before the one rounding);
// an amount discount as the extension less the discount, both integers, no
// second rounding. A discount larger than the extension is the caller's 400
// naming lines[i].discount_cents. A text line carries no extension.
func ExtendLine(l *Line) error {
	if l.LineType == LineText {
		l.LineTotal = nil
		return nil
	}
	qty, uq, pq := derefQty(l.Quantity), derefQty(l.UOMQty), derefQty(l.PriceUOMQty)
	price := derefPrice(l.UnitPrice)
	ext, err := httpx.Extend(qty, uq, pq, price)
	if err != nil {
		return fmt.Errorf("line %s: extension: %w", l.ID, err)
	}
	switch {
	case l.DiscountPercent != nil:
		disc, err := httpx.ExtendDiscounted(qty, uq, pq, price, *l.DiscountPercent)
		if err != nil {
			return fmt.Errorf("line %s: discounted extension: %w", l.ID, err)
		}
		l.LineTotal = &disc
	case l.DiscountAmount != nil:
		if *l.DiscountAmount > ext {
			return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a line's discount is larger than its extension",
				Details: []httpx.FieldError{{Field: "lines.discount_cents", Message: "must not exceed the line's extension"}}}
		}
		less := ext - *l.DiscountAmount
		l.LineTotal = &less
	default:
		l.LineTotal = &ext
	}
	return nil
}

func derefQty(q *httpx.Quantity) httpx.Quantity {
	if q == nil {
		return 0
	}
	return *q
}

func derefPrice(p *httpx.Price) httpx.Price {
	if p == nil {
		return 0
	}
	return *p
}

// Explode replaces every product line that names a kit product with the kit
// line followed by one component line per component of the kit's definition
// (ADR 0005 section 2.6). The explosion is done once, at the line's create
// or edit, so a later change of the kit definition does not touch existing
// documents: component quantities and units are captured here. Positions are
// rewritten so a kit's components follow it.
func Explode(lines []Line, kits map[string][]KitComponent, refs map[string]ProductRef) ([]Line, error) {
	out := make([]Line, 0, len(lines))
	for i := range lines {
		line := lines[i]
		if line.LineType != LineProduct || line.ProductID == nil {
			out = append(out, line)
			continue
		}
		ref, ok := refs[line.ProductID.String()]
		if !ok || !ref.IsKit {
			out = append(out, line)
			continue
		}
		line.LineType = LineKit
		comps, ok := kits[line.ProductID.String()]
		if !ok || len(comps) == 0 {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
				Message: "a kit line names a kit with no components",
				Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].product_id", i),
					Message: "the kit has no components defined"}}}
		}
		out = append(out, line)
		for _, comp := range comps {
			cref, ok := refs[comp.ComponentProductID.String()]
			if !ok {
				return nil, fmt.Errorf("kit %s: component %s not found", ref.SKU, comp.ComponentProductID)
			}
			if cref.IsKit {
				return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
					Message: "a kit cannot contain a kit",
					Details: []httpx.FieldError{{Field: fmt.Sprintf("lines[%d].product_id", i),
						Message: "kit " + cref.SKU + " is itself a kit: a kit holds one level"}}}
			}
			// The component's quantity is the kit's quantity times the kit's
			// per kit quantity, at the shared scale 4, rounded once, half
			// away from zero.
			n := new(big.Int).Mul(big.NewInt(int64(derefQty(line.Quantity))), big.NewInt(int64(comp.Quantity)))
			div := big.NewInt(int64(One))
			var mag big.Int
			mag.Abs(n)
			mag.Add(&mag, new(big.Int).Rsh(div, 1))
			mag.Div(&mag, div)
			cqty := httpx.Quantity(mag.Int64())
			cuom := cref.UOMPrimary
			cprice := httpx.Price(0)
			zero := httpx.Cents(0)
			sku := cref.SKU
			desc := cref.Description
			compLine := Line{
				ID:              uuid.New(),
				LineType:        LineComponent,
				ParentLineID:    ptrOf(line.ID),
				ProductID:       &comp.ComponentProductID,
				SKU:             &sku,
				Description:     desc,
				Quantity:        &cqty,
				UOM:             &cuom,
				PriceUOM:        &cuom,
				UOMQty:          ptrQty(One),
				PriceUOMQty:     ptrQty(One),
				UnitPrice:       &cprice,
				PricedUnitPrice: &cprice,
				PriceSource:     PriceSourceNone,
				LineTotal:       &zero,
				Taxable:         false,
				CreatedAt:       line.CreatedAt,
			}
			out = append(out, compLine)
		}
	}
	for i := range out {
		out[i].Position = i
	}
	return out, nil
}
