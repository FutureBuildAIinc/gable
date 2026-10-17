// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
)

// UnitSetRow is one row of a product's unit set (ADR 0006 section 3.1):
// UnitQty of the row's unit is StockQty of the product's stocking unit,
// canonical, with the use flags a line's units are checked against.
type UnitSetRow struct {
	UOM      string
	UnitQty  httpx.Quantity
	StockQty httpx.Quantity
	Sell     bool
	Purchase bool
	Price    bool
}

// Pair is the row as a conversion pair, its left side the row's unit.
func (r UnitSetRow) Pair() units.Pair { return units.Pair{A: r.UnitQty, B: r.StockQty} }

// ProductUnitSet is the product's unit set beside the columns a line's units
// resolve from (ADR 0006 sections 3.1 and 4): the stocking unit, whether the
// product is sold by tally, and the nominal cross section a tallied line
// snapshots.
type ProductUnitSet struct {
	StockUOM     string
	SaleUOM      string // the default a line's uom takes
	PriceUOM     string // the default a manually priced line's price_uom takes
	RandomLength bool
	BoardThick   *httpx.Quantity
	BoardWidth   *httpx.Quantity
	Rows         []UnitSetRow
}

// Row is the set's row for a unit.
func (s ProductUnitSet) Row(uom string) (UnitSetRow, bool) {
	for _, r := range s.Rows {
		if r.UOM == uom {
			return r, true
		}
	}
	return UnitSetRow{}, false
}

// LineUnits carries what one line request named beside the resolved units:
// the input the service reads before and after ResolveLineUnits runs.
type LineUnits struct {
	UOM         string
	PriceUOM    string
	UOMQty      httpx.Quantity // 0, 0 when the request sent no pair
	PriceUOMQty httpx.Quantity
	TallyRows   []TallyRow // nil when the request sent no tally
	// HasQuantity is false when the quantity comes from the line's tally.
	HasQuantity bool
}

// ResolveLineUnits settles one product line's units on the shared line shape
// (ADR 0006 sections 3.3, 3.4 and 4): the catalogue check, the pair resolved
// from the product's set and stored on the line, the tally of a random
// length line, and the stocking quantity, exact or refused with the nearest
// quantities that are exact. It answers false when the line is unusable;
// every problem is already collected in v. A line without a product keeps
// the older rule: the pair is the client's, or the units' standard sizes
// derive it.
func ResolveLineUnits(v *httpx.Validator, path string, line *Line, in LineUnits, set *ProductUnitSet, catalogue map[string]units.CatalogueUnit) bool {
	before := ErrorCount(v)
	uom := in.UOM

	// The catalogue: a line's units are active catalogue units (2.1).
	for _, unit := range []struct {
		field string
		code  string
		sent  bool
	}{
		{"uom", uom, uom != ""},
		{"price_uom", in.PriceUOM, in.PriceUOM != "" && in.PriceUOM != uom},
	} {
		if !unit.sent {
			continue // defaulted from the product: the set check below covers it
		}
		u, known := catalogue[unit.code]
		if !known {
			v.Check(false, path+"."+unit.field, unit.code+" is not a unit of the catalogue")
		} else if !u.IsActive {
			v.Check(false, path+"."+unit.field, unit.code+" is inactive: an inactive unit cannot enter a new line")
		}
	}

	if set != nil {
		// The units must be rows of the product's set, sell on the sale unit
		// and price on the price unit (3.3).
		uRow, hasU := set.Row(uom)
		if !hasU || !uRow.Sell {
			v.Check(false, path+".uom", uom+" is not a sale unit of the product")
		}
		pRow, hasP := set.Row(line.PriceUOMString())
		if !hasP || !pRow.Price {
			v.Check(false, path+".price_uom", line.PriceUOMString()+" is not a price unit of the product")
		}
		if ErrorCount(v) > before {
			return false
		}
		// The pair resolves from the two rows and is stored on the line; a
		// sent pair must equal it as a ratio (3.3), and the resolved pair is
		// what the line stores, canonical whatever equivalent pair the
		// client sent (R2: one conversion, one byte form).
		resolved, err := units.ResolveLinePair(uRow.Pair(), pRow.Pair())
		if err != nil {
			v.Check(false, path+".uom_qty", "the conversion between "+uom+" and "+line.PriceUOMString()+" does not fit the pair's bound")
			return false
		}
		if in.UOMQty != 0 || in.PriceUOMQty != 0 {
			sent := units.Pair{A: in.UOMQty, B: in.PriceUOMQty}
			if !sent.SameRatio(resolved) {
				v.Check(false, path+".uom_qty",
					"does not match the product's unit set; omit the pair or send "+
						resolved.A.WireString()+" and "+resolved.B.WireString())
			}
		}
		line.UOMQty, line.PriceUOMQty = ptrQty(resolved.A), ptrQty(resolved.B)
	} else {
		// A line without a product keeps the cycle 2 rule: the pair is the
		// client's, except that two units with standard sizes in one
		// dimension derive it the same way (rule 2 of 3.2) and a sent pair
		// must agree.
		if in.UOMQty == 0 && in.PriceUOMQty == 0 {
			if line.PriceUOMString() == uom {
				line.UOMQty, line.PriceUOMQty = ptrQty(One), ptrQty(One)
			} else if derived, ok := units.StandardPair(uom, line.PriceUOMString(), catalogue); ok {
				line.UOMQty, line.PriceUOMQty = ptrQty(derived.A), ptrQty(derived.B)
			} else {
				v.Check(false, path+".uom_qty", "is required when price_uom differs from uom: send uom_qty and price_uom_qty")
			}
		} else if line.PriceUOMString() == uom {
			v.Check(in.UOMQty == One && in.PriceUOMQty == One, path+".uom_qty",
				"must be 1 when price_uom equals uom: the units agree, so the pair is 1 and 1")
			line.UOMQty, line.PriceUOMQty = ptrQty(One), ptrQty(One)
		} else if derived, ok := units.StandardPair(uom, line.PriceUOMString(), catalogue); ok {
			if !(units.Pair{A: in.UOMQty, B: in.PriceUOMQty}).SameRatio(derived) {
				v.Check(false, path+".uom_qty",
					"does not match the units' standard sizes; the derived pair is "+
						derived.A.WireString()+" and "+derived.B.WireString())
			}
			// The derived pair is canonical (R2): an agreeing sent pair
			// stores it, not the form the client chose.
			line.UOMQty, line.PriceUOMQty = ptrQty(derived.A), ptrQty(derived.B)
		} else {
			line.UOMQty, line.PriceUOMQty = ptrQty(in.UOMQty), ptrQty(in.PriceUOMQty)
		}
	}
	if ErrorCount(v) > before {
		return false
	}

	// The tally (section 4): allowed on a random length product line only,
	// its sale unit LF, its linear feet the line's quantity.
	if in.TallyRows != nil {
		if set == nil || !set.RandomLength {
			v.Check(false, path+".tally", "a tally is allowed on a random length product line only")
			return false
		}
		if uom != "LF" {
			v.Check(false, path+".uom", "a tallied line's unit is LF: the tally carries the lengths")
			return false
		}
		rows := make([]units.TallyRow, 0, len(in.TallyRows))
		for _, r := range in.TallyRows {
			rows = append(rows, units.TallyRow{Pieces: int64(r.Pieces), LengthFT: r.LengthFT})
		}
		linearFeet, err := units.LinearFeet(rows)
		if err != nil {
			v.Check(false, path+".tally",
				"the tally's linear feet are beyond the quantity bound of 99999999.9999 linear feet")
			return false
		}
		if in.HasQuantity && line.Quantity != nil && *line.Quantity != linearFeet {
			v.Check(false, path+".quantity", "must equal the tally's linear feet, "+linearFeet.WireString())
			return false
		}
		line.Quantity = ptrQty(linearFeet)
		tally := &Tally{Rows: make([]TallyRow, 0, len(rows))}
		for _, r := range rows {
			tally.Rows = append(tally.Rows, TallyRow{Pieces: int(r.Pieces), LengthFT: r.LengthFT})
		}
		tally.LinearFeet = linearFeet
		if set.BoardThick != nil && set.BoardWidth != nil {
			tally.ThicknessIn, tally.WidthIn = set.BoardThick, set.BoardWidth
			if bf, err := units.BoardFeet(linearFeet, *set.BoardThick, *set.BoardWidth); err == nil {
				display := units.DisplayBoardFeet(bf)
				tally.BoardFeet = &display
			}
		}
		line.Tally = tally
	}

	// The stocking unit and quantity (3.4): exact at scale 4 or the line is
	// refused with the nearest quantities that are exact (R5).
	if set != nil {
		stockUOM := set.StockUOM
		var stockQty httpx.Quantity
		var err error
		if uom == stockUOM {
			stockQty = *line.Quantity
		} else if uRow, ok := set.Row(uom); ok {
			stockQty, err = units.ConvertStock(*line.Quantity, uom, stockUOM, uRow.Pair())
		} else {
			return false // the set check above already refused the unit
		}
		if err != nil {
			var inexact *units.InexactError
			switch {
			case errors.As(err, &inexact):
				v.Check(false, path+".quantity", inexact.Error())
			case errors.Is(err, units.ErrOutOfBound):
				v.Check(false, path+".quantity",
					"is past the quantity bound of 99999999.9999 when converted into the stocking unit "+stockUOM)
			default:
				v.Check(false, path+".quantity", "does not convert exactly into the stocking unit "+stockUOM)
			}
			return false
		}
		line.StockUOM, line.StockQuantity = ptrOf(stockUOM), ptrQty(stockQty)
	}
	return true
}

// PriceUOMString is the line's price unit, read for the resolver.
func (l *Line) PriceUOMString() string {
	if l.PriceUOM == nil {
		return ""
	}
	return *l.PriceUOM
}

// ErrorCount is how many field errors the validator holds so far; a resolver
// compares it against its own starting count to know whether it refused.
func ErrorCount(v *httpx.Validator) int {
	err := v.Err()
	if err == nil {
		return 0
	}
	if he, ok := err.(*httpx.Error); ok {
		return len(he.Details)
	}
	return 0
}

// EngineAnswer is the pricing engine's answer for one line (ADR 0006
// sections 3.3 and 5.3): the unit price in its own unit, the line's
// conversion pair for the line's unit and the price's unit, and the basis
// the answer's price_basis carries (null on a line the engine did not
// price). The order and counter modules read it through their PriceEngine
// seams; the wiring names the engine's own Resolve.
type EngineAnswer struct {
	Price       httpx.Price
	PriceUOM    string
	UOMQty      httpx.Quantity
	PriceUOMQty httpx.Quantity
	Basis       string
}
