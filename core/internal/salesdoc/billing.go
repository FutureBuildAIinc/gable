// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"math/big"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// CumulativeTotal is the extension of the first qty units of a line (ADR 0005
// sections 2.4 and 5.6): the line's price, pair and discount applied to qty
// as if qty were the line's quantity, with an amount discount's NET (the
// extension less the discount) prorated by qty over the line's ordered
// quantity, rounded half away from zero once.
//
// A partial invoice bills the difference of two cumulative totals, the one
// after and the one before, so the invoices of a line sum to the order line's
// own total to the cent, whatever order and size the quantities ship in: the
// telescoping sum has no rounding residue. Because every cumulative figure is
// one rounding of a quantity-proportional amount, the cumulative total never
// runs backwards, so no billed piece can go negative (a piece of -1 cent on
// dust lines, which PostEntry refuses as a negative revenue leg). At qty
// equal to the line's quantity it equals the line's LineTotal.
func CumulativeTotal(l *Line, qty httpx.Quantity) (httpx.Cents, error) {
	if qty <= 0 {
		return 0, nil
	}
	uq, pq := derefQty(l.UOMQty), derefQty(l.PriceUOMQty)
	price := derefPrice(l.UnitPrice)
	switch {
	case l.DiscountPercent != nil:
		return httpx.ExtendDiscounted(qty, uq, pq, price, *l.DiscountPercent)
	case l.DiscountAmount != nil:
		ordered := derefQty(l.Quantity)
		if ordered <= 0 {
			return httpx.Extend(qty, uq, pq, price)
		}
		whole, err := httpx.Extend(ordered, uq, pq, price)
		if err != nil {
			return 0, err
		}
		net := whole - *l.DiscountAmount
		if qty >= ordered {
			return net, nil
		}
		// the net, prorated: round_half_away(net x qty / ordered)
		n := new(big.Int).Mul(big.NewInt(int64(net)), big.NewInt(int64(qty)))
		d := big.NewInt(int64(ordered))
		n.Add(n, new(big.Int).Rsh(d, 1))
		n.Div(n, d)
		return httpx.Cents(n.Int64()), nil
	default:
		return httpx.Extend(qty, uq, pq, price)
	}
}

// BilledTotal is the extension of the units from before to after on a line:
// CumulativeTotal(after) less CumulativeTotal(before).
func BilledTotal(l *Line, before, after httpx.Quantity) (httpx.Cents, error) {
	hi, err := CumulativeTotal(l, after)
	if err != nil {
		return 0, err
	}
	lo, err := CumulativeTotal(l, before)
	if err != nil {
		return 0, err
	}
	return hi - lo, nil
}

// BilledDiscount is the amount discount a billed piece of a line gave, in
// cents: the gross extension of the piece (Extend over the cumulative
// quantities, the telescoping difference like BilledTotal) less the piece's
// net. Defining the share as gross less net, rather than prorating the
// discount on its own, makes every invoice line satisfy line total + discount
// = its gross piece to the cent, and the pieces of a line sum to the order
// line's whole discount exactly (the carried C2-2b item).
func BilledDiscount(l *Line, before, after httpx.Quantity) (httpx.Cents, error) {
	if l.DiscountAmount == nil {
		return 0, nil
	}
	uq, pq := derefQty(l.UOMQty), derefQty(l.PriceUOMQty)
	price := derefPrice(l.UnitPrice)
	gross := func(q httpx.Quantity) (httpx.Cents, error) {
		if q <= 0 {
			return 0, nil
		}
		return httpx.Extend(q, uq, pq, price)
	}
	hi, err := gross(after)
	if err != nil {
		return 0, err
	}
	lo, err := gross(before)
	if err != nil {
		return 0, err
	}
	net, err := BilledTotal(l, before, after)
	if err != nil {
		return 0, err
	}
	return (hi - lo) - net, nil
}

// RevenueAccountProduct is the account product, kit and non stock lines post
// their totals to (ADR 0005 section 8.3).
const RevenueAccountProduct = "4010"

// RevenueGroup is one revenue account's share of a document.
type RevenueGroup struct {
	AccountCode string
	Cents       httpx.Cents
}

// RevenueGroups sums line totals per revenue account (ADR 0005 section 8.3):
// product, kit and non stock lines to 4010; charge lines to their line's
// revenue account code. Discounts are already inside the totals, so revenue
// posts net. Component and text lines post nothing. The result is ordered by
// account code.
func RevenueGroups(lines []Line) []RevenueGroup {
	sums := map[string]httpx.Cents{}
	for i := range lines {
		l := &lines[i]
		if l.LineTotal == nil || *l.LineTotal == 0 {
			continue
		}
		switch l.LineType {
		case LineProduct, LineKit:
			sums[RevenueAccountProduct] += *l.LineTotal
		case LineCharge:
			code := RevenueAccountProduct
			if l.RevenueAccountCode != nil && *l.RevenueAccountCode != "" {
				code = *l.RevenueAccountCode
			}
			sums[code] += *l.LineTotal
		}
	}
	out := make([]RevenueGroup, 0, len(sums))
	for code, c := range sums {
		out = append(out, RevenueGroup{AccountCode: code, Cents: c})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].AccountCode < out[b].AccountCode })
	return out
}

// CostOf is a line's cost in cents (ADR 0005 section 8.4): round_half_away(
// quantity x unit cost), both at scale 4, so the divisor carries 10^6. A
// zero or negative unit cost, or quantity, is no cost.
func CostOf(qty httpx.Quantity, unitCost httpx.Price) httpx.Cents {
	if qty <= 0 || unitCost <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(int64(qty)), big.NewInt(int64(unitCost)))
	div := big.NewInt(1_000_000)
	n.Add(n, new(big.Int).Rsh(div, 1))
	n.Div(n, div)
	return httpx.Cents(n.Int64())
}
