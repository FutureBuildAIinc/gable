// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// creditBuild is a credit memo's lines and totals as built from a draft (or
// rebuilt from a stored draft at post).
type creditBuild struct {
	lines    []CreditLine
	subtotal int64 // cents, negative
	tax      int64 // cents, negative
	taxRate  *string
}

func (b *creditBuild) total() int64 { return b.subtotal + b.tax }

// share is round_half_away(whole x part / of), exact in big arithmetic: a
// part of one invoice line's amount, so the credit memos of a line sum to the
// line's own amount to the cent whatever order and size the quantities come
// back in (the telescoping rule of ADR 0005 2.4, applied to credits).
func share(whole, part, of int64) int64 {
	if of <= 0 || part <= 0 || whole == 0 {
		return 0
	}
	if part >= of {
		return whole
	}
	n := new(big.Int).Mul(big.NewInt(whole), big.NewInt(part))
	neg := n.Sign() < 0
	n.Abs(n)
	d := big.NewInt(of)
	n.Add(n, new(big.Int).Rsh(d, 1))
	n.Div(n, d)
	v := n.Int64()
	if neg {
		v = -v
	}
	return v
}

func exceedsBilled(path string, billed, credited httpx.Quantity) *httpx.Error {
	left := billed - credited
	if left < 0 {
		left = 0
	}
	msg := fmt.Sprintf("%s credits more than was billed: %s left to credit on the invoice line", path, left.DecimalString())
	return &httpx.Error{Status: 409, Code: httpx.CodeConflict, Message: msg,
		Details: []httpx.FieldError{httpx.Blocker("exceeds_billed", msg)}}
}

// buildCredit builds a credit memo's lines and totals (ADR 0005 6.3 and
// section 3). source is the invoice it names (nil for none). amounts is what
// the POSTED memos of the invoice have credited (the extension, discount and
// cost pieces telescope against it); guard adds the drafts, for the
// exceeds_billed check at draft time. A free line is priced by its own
// request; an invoice line takes its price, pair and discount from the
// invoice line, so a partial credit is the same proportion of what was
// charged and the last credit takes the remainder.
func (s *Service) buildCredit(ctx context.Context, st Store, d *CreditInput, source *Invoice, amounts, guard CreditedAgainst,
	currentIDs map[uuid.UUID]bool, branchID uuid.UUID, shipToID *uuid.UUID, exempt bool) (*creditBuild, error) {

	srcLines := map[uuid.UUID]*InvoiceLine{}
	if source != nil {
		for i := range source.Lines {
			srcLines[source.Lines[i].ID] = &source.Lines[i]
		}
	}
	var productIDs []uuid.UUID
	for _, l := range d.Lines {
		if l.ProductID != nil {
			productIDs = append(productIDs, *l.ProductID)
		}
	}
	refs, err := st.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}

	out := &creditBuild{}
	for i := range d.Lines {
		l := &d.Lines[i]
		path := fmt.Sprintf("lines[%d]", i)
		var cl CreditLine
		if l.InvoiceLineID != nil {
			if source == nil {
				return nil, validationFailed("one or more fields failed validation",
					httpx.FieldError{Field: path + ".invoice_line_id", Message: "the credit memo names no invoice: credit a free line instead"})
			}
			src, found := srcLines[*l.InvoiceLineID]
			if !found {
				return nil, validationFailed("one or more fields failed validation",
					httpx.FieldError{Field: path + ".invoice_line_id", Message: "no such line on this invoice"})
			}
			if src.LineType != salesdoc.LineProduct && src.LineType != salesdoc.LineCharge {
				msg := kitCreditRefusal
				if src.LineType == salesdoc.LineText {
					msg = "a text line is not credited by line: send a free line with its own description and price"
				}
				return nil, validationFailed("one or more fields failed validation",
					httpx.FieldError{Field: path + ".invoice_line_id", Message: msg})
			}
			cl, err = creditFromInvoiceLine(path, l, src, amounts.ByLine[src.ID], guard.ByLine[src.ID])
			if err != nil {
				return nil, err
			}
		} else {
			cl, err = s.freeCreditLine(ctx, st, path, l, refs)
			if err != nil {
				return nil, err
			}
		}
		if l.ID != nil && currentIDs[*l.ID] {
			cl.ID = *l.ID
		} else {
			cl.ID = uuid.New()
		}
		cl.Position = i
		cl.CreatedAt = httpx.TimestampOf(s.now().UTC())
		out.lines = append(out.lines, cl)
	}

	// Totals and tax (section 3).
	var taxable int64
	for i := range out.lines {
		l := &out.lines[i]
		if l.LineTotal == nil {
			continue
		}
		out.subtotal += int64(*l.LineTotal)
		if l.Taxable {
			taxable += int64(*l.LineTotal)
		}
	}
	rate, tax, err := s.creditTax(ctx, st, source, amounts, taxable, branchID, shipToID, exempt)
	if err != nil {
		return nil, err
	}
	out.taxRate, out.tax = rate, tax
	return out, nil
}

// creditFromInvoiceLine builds the credit line for r units returned of an
// invoice line.
func creditFromInvoiceLine(path string, d *CreditLineInput, src *InvoiceLine, amounts, guard Credited) (CreditLine, error) {
	whole := int64(0)
	if src.Quantity != nil {
		whole = int64(*src.Quantity)
	}
	r := -int64(d.Quantity)
	if guard.Quantity+r > whole {
		return CreditLine{}, exceedsBilled(path, httpx.Quantity(whole), httpx.Quantity(guard.Quantity))
	}
	before := amounts.Quantity
	var total, discount int64
	if src.LineTotal != nil {
		total = share(int64(*src.LineTotal), before+r, whole) - amounts.TotalCents
	}
	if src.DiscountAmount != nil {
		discount = share(int64(*src.DiscountAmount), before+r, whole) - amounts.DiscountCents
	}
	if total < 0 {
		total = 0
	}
	if discount < 0 {
		discount = 0
	}
	desc := src.Description
	if d.Description != "" {
		desc = d.Description
	}
	q := httpx.Quantity(-r)
	lt := httpx.Cents(-total)
	cl := CreditLine{InvoiceLineID: &src.ID}
	cl.Line = salesdoc.Line{
		LineType: src.LineType, ProductID: src.ProductID, ChargeCodeID: src.ChargeCodeID, ChargeCode: src.ChargeCode,
		SKU: src.SKU, Description: desc, Quantity: &q, UOM: src.UOM, PriceUOM: src.PriceUOM, UOMQty: src.UOMQty,
		PriceUOMQty: src.PriceUOMQty, UnitPrice: src.UnitPrice, PricedUnitPrice: src.PricedUnitPrice,
		PriceSource: src.PriceSource, OverrideReason: src.OverrideReason, DiscountPercent: src.DiscountPercent,
		DiscountReason: src.DiscountReason, PriceAdjustedBy: src.PriceAdjustedBy,
		LineTotal: &lt, Taxable: src.Taxable, RevenueAccountCode: src.RevenueAccountCode,
	}
	if src.DiscountAmount != nil {
		dc := httpx.Cents(discount)
		cl.DiscountAmount = &dc
	}
	if d.Restock {
		if src.LineType != salesdoc.LineProduct || src.ProductID == nil {
			return CreditLine{}, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: path + ".restock", Message: "only a stocked product line goes back to stock"})
		}
		cl.Restock = true
		cl.UnitCost = src.UnitCost
		// The cost that comes back is the invoice line's own cost in the
		// same proportion, never more than that line relieved and not yet
		// reversed: COGS reverses at the original cost, not today's.
		cost := share(int64(src.CostCents), amounts.RestockQuantity+r, whole) - amounts.CostCents
		if left := int64(src.CostCents) - amounts.CostCents; cost > left {
			cost = left
		}
		if cost < 0 {
			cost = 0
		}
		cl.CostCents = httpx.Cents(-cost)
	}
	return cl, nil
}

// freeCreditLine builds a credit line that names no invoice line: a price
// adjustment, a fee given back, goods returned against no invoice, a note.
func (s *Service) freeCreditLine(ctx context.Context, st Store, path string, d *CreditLineInput, refs map[uuid.UUID]salesdoc.ProductRef) (CreditLine, error) {
	var cl CreditLine
	cl.LineType = d.LineType
	cl.Description = d.Description
	switch d.LineType {
	case salesdoc.LineText:
		cl.PriceSource = salesdoc.PriceSourceNone
		return cl, nil

	case salesdoc.LineCharge:
		code, found, err := st.ChargeCodeByCode(ctx, d.ChargeCode)
		if err != nil {
			return cl, err
		}
		if !found || !code.IsActive {
			return cl, validationFailed("a referenced record does not exist",
				httpx.FieldError{Field: path + ".charge_code", Message: "no such active charge code"})
		}
		price := httpx.Price(0)
		switch {
		case d.UnitPrice != nil:
			price = *d.UnitPrice
		case code.DefaultUnitPrice != nil:
			price = *code.DefaultUnitPrice
		default:
			return cl, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: path + ".unit_price_ten_thousandths", Message: "is required: the charge code has no default price"})
		}
		cl.ChargeCodeID, cl.ChargeCode = &code.ID, &code.Code
		cl.RevenueAccountCode = &code.RevenueAccountCode
		cl.Taxable = code.Taxable
		if d.Taxable != nil {
			cl.Taxable = *d.Taxable
		}
		if cl.Description == "" {
			cl.Description = code.Name
		}
		uom := "EA"
		if d.UOM != "" {
			uom = d.UOM
		}
		return finishFree(path, &cl, d, uom, price)

	default: // a product line
		uom := d.UOM
		cl.Taxable = true
		if d.Taxable != nil {
			cl.Taxable = *d.Taxable
		}
		if d.ProductID != nil {
			ref, found := refs[*d.ProductID]
			if !found {
				return cl, validationFailed("a referenced record does not exist",
					httpx.FieldError{Field: path + ".product_id", Message: "no such product"})
			}
			if ref.IsKit {
				return cl, validationFailed("one or more fields failed validation",
					httpx.FieldError{Field: path + ".product_id", Message: kitCreditRefusal})
			}
			cl.ProductID = d.ProductID
			sku := ref.SKU
			cl.SKU = &sku
			cl.Taxable = ref.Taxable
			if uom == "" {
				uom = ref.UOMPrimary
			}
			if cl.Description == "" {
				cl.Description = ref.Description
			}
			if d.Restock {
				cost := ref.AverageCost
				cl.Restock = true
				if cost > 0 {
					cl.UnitCost = &cost
					cl.CostCents = httpx.Cents(-int64(salesdoc.CostOf(-d.Quantity, cost)))
				}
			}
		}
		return finishFree(path, &cl, d, uom, *d.UnitPrice)
	}
}

// finishFree prices a free line: the unit, the pair 1 and 1, the extension
// rounded once (negative with the quantity), the source MANUAL.
func finishFree(path string, cl *CreditLine, d *CreditLineInput, uom string, price httpx.Price) (CreditLine, error) {
	q := d.Quantity
	one := salesdoc.One
	total, err := httpx.Extend(q, one, one, price)
	if err != nil {
		return *cl, validationFailed("one or more fields failed validation",
			httpx.FieldError{Field: path + ".quantity", Message: "the extension is out of range"})
	}
	cl.Quantity, cl.UOM, cl.PriceUOM = &q, &uom, &uom
	cl.UOMQty, cl.PriceUOMQty = &one, &one
	cl.UnitPrice = &price
	cl.PriceSource = salesdoc.PriceSourceManual
	cl.LineTotal = &total
	return *cl, nil
}

// kitCreditRefusal is the one message for every way a credit memo line can
// name a kit: ADR 0005 6.3 does not say how a kit return explodes into
// component quantities, costs and restock, so v1 refuses it everywhere.
const kitCreditRefusal = "a kit cannot be returned in v1; credit its price with a free line that names no product"

// creditTax is the credit memo's tax (ADR 0005 section 3): at the invoice's
// rate on the memo's taxable base, never more than the invoice's tax less the
// tax earlier memos credited, the memo that returns the last of the invoice's
// taxable amount taking exactly that remainder; a provider's invoice (no
// rate) credits in proportion to its tax. With no invoice the rate resolves
// from the ship-to or the branch (the exemption first), and a document with
// no configured rate is refused. The result is negative like the memo.
func (s *Service) creditTax(ctx context.Context, st Store, source *Invoice, amounts CreditedAgainst, taxableBase int64,
	branchID uuid.UUID, shipToID *uuid.UUID, exempt bool) (*string, int64, error) {

	base := -taxableBase // positive
	if source == nil {
		if exempt {
			zero := "0.000000"
			return &zero, 0, nil
		}
		shipRate, branchRate, err := st.TaxInputs(ctx, branchID, shipToID)
		if err != nil {
			return nil, 0, err
		}
		resolved, err := salesdoc.ResolveTax(salesdoc.TaxInputs{Delivery: shipToID != nil, ShipToRate: shipRate, BranchRate: branchRate})
		if err != nil {
			if err == salesdoc.ErrTaxRateNotConfigured {
				return nil, 0, conflictBlocker("tax_rate_not_configured",
					"set the branch's default tax rate (or the ship-to's rate) before crediting")
			}
			return nil, 0, err
		}
		scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
		if err != nil {
			return nil, 0, err
		}
		return resolved.Rate, int64(salesdoc.TaxAt(httpx.Cents(taxableBase), scaled)), nil
	}

	// The invoice's own taxable base and tax.
	var invTaxable int64
	for i := range source.Lines {
		if source.Lines[i].Taxable && source.Lines[i].LineTotal != nil {
			invTaxable += int64(*source.Lines[i].LineTotal)
		}
	}
	invTax := int64(source.TaxCents)
	var rateStr *string
	if source.TaxRatePercent != nil {
		if scaled, err := httpx.ParseQuantity(*source.TaxRatePercent); err == nil {
			// percent at scale 4 and the rate at scale 6 are the same digits
			r := fmt.Sprintf("%d.%06d", int64(scaled)/1000000, abs(int64(scaled))%1000000)
			rateStr = &r
		}
	}
	if base == 0 || invTaxable <= 0 || invTax <= 0 {
		return rateStr, 0, nil
	}
	left := invTax - amounts.TaxCents
	if left < 0 {
		left = 0
	}
	var tax int64
	switch {
	case amounts.TaxableCents+base >= invTaxable:
		tax = left // the memo that returns the last of the taxable amount takes the remainder
	case rateStr != nil:
		scaled, _, err := salesdoc.ParseTaxRate(*rateStr)
		if err != nil {
			return nil, 0, err
		}
		tax = int64(salesdoc.TaxAt(httpx.Cents(base), scaled))
	default:
		tax = share(invTax, base, invTaxable)
	}
	if tax > left {
		tax = left
	}
	return rateStr, -tax, nil
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// draftFromStored rebuilds a stored draft's request lines so the post can run
// the same build against what has been POSTED by then.
func draftFromStored(cm *CreditMemo) *CreditInput {
	d := &CreditInput{InvoiceID: cm.InvoiceID, ReasonCode: cm.ReasonCode, Reason: cm.Reason}
	for i := range cm.Lines {
		l := &cm.Lines[i]
		id := l.ID
		ld := CreditLineInput{ID: &id}
		if l.Quantity != nil {
			ld.Quantity = *l.Quantity
		}
		ld.Description = l.Description
		ld.Restock = l.Restock
		if l.InvoiceLineID != nil {
			ld.InvoiceLineID = l.InvoiceLineID
			d.Lines = append(d.Lines, ld)
			continue
		}
		ld.LineType = l.LineType
		ld.ProductID = l.ProductID
		if l.ChargeCode != nil {
			ld.ChargeCode = *l.ChargeCode
		}
		if l.UOM != nil {
			ld.UOM = *l.UOM
		}
		ld.UnitPrice = l.UnitPrice
		taxable := l.Taxable
		if l.ProductID == nil {
			ld.Taxable = &taxable
		}
		d.Lines = append(d.Lines, ld)
	}
	return d
}

// restockSum is the cost a memo's restocked lines bring back, as a positive
// figure in cents.
func restockSum(lines []CreditLine) int64 {
	var c int64
	for i := range lines {
		if lines[i].Restock {
			c += -int64(lines[i].CostCents)
		}
	}
	return c
}

func memoDay(t time.Time) string { return strings.TrimSpace(t.Format("2006-01-02")) }
