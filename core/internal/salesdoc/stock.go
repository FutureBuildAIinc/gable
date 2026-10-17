// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"fmt"
	"math/big"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
)

// StockUnitChangedError builds the 409 conflict of ADR 0006 section 3.4 when a
// line's stock_uom no longer matches the product's current stocking unit and
// cannot be converted through the product's current unit set.
func StockUnitChangedError(lineIndex int, lineStockUOM, currentUOMPrimary string) *httpx.Error {
	field := fmt.Sprintf("lines[%d]", lineIndex)
	msg := fmt.Sprintf("%s carries stock_uom %s, which cannot convert into the product's current stocking unit %s", field, lineStockUOM, currentUOMPrimary)
	return &httpx.Error{
		Status:  http.StatusConflict,
		Code:    httpx.CodeConflict,
		Message: msg,
		Details: []httpx.FieldError{
			{Field: field, Code: "stock_unit_changed", Message: msg},
		},
	}
}

// ResolveStockMove compares a line's stored stock_uom against the product's
// current stocking unit (ADR 0006 section 3.4). When they agree, it returns the
// current stocking unit and StockQty(l). When they differ (the product's
// stocking unit was changed after the line was written), it converts StockQty(l)
// from the old stocking unit through the product's current unit set when the
// old unit is still a row of the set (and the current stocking unit's row is
// 1 and 1) and the result is exact at scale 4; otherwise it returns the 409
// conflict with blocker stock_unit_changed naming lines[lineIndex].
func ResolveStockMove(lineIndex int, l *Line, currentUOMPrimary string, set *ProductUnitSet) (string, httpx.Quantity, error) {
	qty := StockQty(l)
	if l.StockUOM == nil || *l.StockUOM == "" || currentUOMPrimary == "" || *l.StockUOM == currentUOMPrimary {
		uom := currentUOMPrimary
		if uom == "" && l.StockUOM != nil {
			uom = *l.StockUOM
		}
		return uom, qty, nil
	}
	oldUOM := *l.StockUOM
	if set != nil && set.StockUOM == currentUOMPrimary {
		curRow, hasCur := set.Row(currentUOMPrimary)
		oldRow, hasOld := set.Row(oldUOM)
		if hasCur && curRow.UnitQty == One && curRow.StockQty == One && hasOld {
			converted, err := units.ConvertStock(qty, oldUOM, currentUOMPrimary, oldRow.Pair())
			if err == nil {
				return currentUOMPrimary, converted, nil
			}
		}
	}
	return "", 0, StockUnitChangedError(lineIndex, oldUOM, currentUOMPrimary)
}

// StockQty is the line's quantity in the unit the product stocks in (ADR 0006
// section 3.4): the stored stock quantity, or the line's own quantity when it
// carries none (a line written before the stocking columns, whose unit is the
// stocking unit anyway). Every inventory-affecting quantity on a stocked line
// reads this, never the sale quantity.
func StockQty(l *Line) httpx.Quantity {
	if l.StockQuantity != nil && *l.StockQuantity != 0 {
		return *l.StockQuantity
	}
	return derefQty(l.Quantity)
}

// SaleFromStock converts a stocking quantity back into the line's sale unit
// (ADR 0006 section 3.4): sale = stock x quantity / stock_quantity, exact at
// scale 4 or false, the case where a partial bill falls back to the stocking
// unit instead.
func SaleFromStock(l *Line, stock httpx.Quantity) (httpx.Quantity, bool) {
	if l.StockQuantity == nil || *l.StockQuantity == 0 || l.Quantity == nil {
		return stock, true
	}
	s, q := new(big.Int).SetInt64(int64(*l.StockQuantity)), big.NewInt(int64(*l.Quantity))
	num := new(big.Int).Mul(big.NewInt(int64(stock)), q)
	q2, r := new(big.Int).QuoRem(num, s, new(big.Int))
	if r.Sign() != 0 || !q2.IsInt64() {
		return 0, false
	}
	out := httpx.Quantity(q2.Int64())
	if out > httpx.QuantityMax || out < -httpx.QuantityMax {
		return 0, false
	}
	return out, true
}

// StockPricePair is the conversion pair between the line's stocking unit and
// its price unit, derived from the line's own stored pair and quantities (ADR
// 0006 section 3.4's fallback bill: no unit set read). S stock = Q sale and
// A sale = B price, so x stock = y price exactly when y / x is the ratio
// (quantity x price_uom_qty) / (stock_quantity x uom_qty), canonical. A line
// whose sale unit is the stocking unit already carries this pair.
func StockPricePair(l *Line) (units.Pair, error) {
	if l.StockQuantity == nil || *l.StockQuantity == 0 || l.Quantity == nil || *l.Quantity == 0 {
		return units.Pair{}, fmt.Errorf("line %s carries no stocking quantity to derive its stock pair from", l.ID)
	}
	num := new(big.Int).Mul(big.NewInt(int64(*l.Quantity)), big.NewInt(int64(derefQty(l.PriceUOMQty))))
	den := new(big.Int).Mul(big.NewInt(int64(*l.StockQuantity)), big.NewInt(int64(derefQty(l.UOMQty))))
	return units.CanonicalRatio(num, den)
}

// StockLineView is the line read in its stocking unit: the quantity, unit and
// pair a cumulative extension in stocking quantities reads (ADR 0006 section
// 3.4's partial bills, which compute every bill of a line in one unit so the
// telescoping sums stay exact). The price and the discounts ride unchanged:
// no price is ever converted (R4).
func StockLineView(l *Line) (Line, error) {
	pair, err := StockPricePair(l)
	if err != nil {
		return Line{}, err
	}
	out := *l
	uom := ""
	if l.StockUOM != nil {
		uom = *l.StockUOM
	} else if l.UOM != nil {
		uom = *l.UOM
	}
	out.Quantity = ptrQty(StockQty(l))
	out.UOM = &uom
	out.UOMQty = ptrQty(pair.A)
	out.PriceUOMQty = ptrQty(pair.B)
	return out, nil
}
