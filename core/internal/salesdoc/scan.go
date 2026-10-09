// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// LineColumns is the shared projection of a sales document line table, for
// the invoice and credit memo reads: the table aliased `l`, charge_codes
// joined as `cc`. Quantities and prices leave SQL as scale 4 integers and
// amounts as cents (recipe step 6: scaled in SQL, never through float64).
// special is the three expressions for is_special_order, vendor_id and the
// special order cost, which only an order line owns; a table without them
// passes `false, NULL::uuid, NULL::bigint`.
func LineColumns(special string) string {
	return `l.id, l.position, l.line_type, l.parent_line_id, l.product_id, l.charge_code_id, cc.code, l.sku, l.description,
	ROUND(l.quantity * 10000)::bigint, l.uom, l.price_uom, ROUND(l.uom_qty * 10000)::bigint, ROUND(l.price_uom_qty * 10000)::bigint,
	ROUND(l.unit_price * 10000)::bigint, ROUND(l.priced_unit_price * 10000)::bigint, l.price_source, l.override_reason,
	ROUND(l.discount_percent * 10000)::bigint, ROUND(l.discount_amount * 100)::bigint, l.discount_reason, l.price_adjusted_by,
	ROUND(l.line_total * 100)::bigint, l.taxable, l.revenue_account_code, ` + special + `, l.created_at`
}

// LineScan holds the nullable scale 4 integers a LineColumns row scans into
// before Finish writes them onto the Line.
type LineScan struct {
	quantity, uomQty, priceUOMQty, unitPrice, priced *int64
	discountPct, discountAmt, lineTotal, specialCost *int64
	source                                           string
	created                                          time.Time
}

// Dests are the scan targets for one LineColumns row, in column order.
func (s *LineScan) Dests(l *Line) []any {
	return []any{
		&l.ID, &l.Position, &l.LineType, &l.ParentLineID, &l.ProductID, &l.ChargeCodeID, &l.ChargeCode, &l.SKU, &l.Description,
		&s.quantity, &l.UOM, &l.PriceUOM, &s.uomQty, &s.priceUOMQty,
		&s.unitPrice, &s.priced, &s.source, &l.OverrideReason,
		&s.discountPct, &s.discountAmt, &l.DiscountReason, &l.PriceAdjustedBy,
		&s.lineTotal, &l.Taxable, &l.RevenueAccountCode, &l.IsSpecialOrder, &l.VendorID, &s.specialCost, &s.created,
	}
}

// Finish moves the scanned integers onto the line's typed fields.
func (s *LineScan) Finish(l *Line) {
	l.PriceSource = PriceSource(s.source)
	l.Quantity = PtrQuantity(s.quantity)
	l.UOMQty, l.PriceUOMQty = PtrQuantity(s.uomQty), PtrQuantity(s.priceUOMQty)
	l.UnitPrice, l.PricedUnitPrice = PtrPrice(s.unitPrice), PtrPrice(s.priced)
	l.DiscountPercent = PtrQuantity(s.discountPct)
	l.DiscountAmount = PtrCents(s.discountAmt)
	l.LineTotal = PtrCents(s.lineTotal)
	l.SpecialOrderCost = PtrPrice(s.specialCost)
	l.CreatedAt = httpx.TimestampOf(s.created)
}

// PtrQuantity, PtrPrice and PtrCents lift a nullable scaled integer into its
// wire type, nil staying nil.
func PtrQuantity(v *int64) *httpx.Quantity {
	if v == nil {
		return nil
	}
	q := httpx.Quantity(*v)
	return &q
}

func PtrPrice(v *int64) *httpx.Price {
	if v == nil {
		return nil
	}
	p := httpx.Price(*v)
	return &p
}

func PtrCents(v *int64) *httpx.Cents {
	if v == nil {
		return nil
	}
	c := httpx.Cents(*v)
	return &c
}
