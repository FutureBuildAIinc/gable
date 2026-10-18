// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package customerquote is the contractor's quote to their own customer: a
// dealer's customer prices the dealer's products with a markup, adds labour
// and overhead, and sends the result to a homeowner or owner on a capability
// link that needs no account. The customer approves selections and signs
// off; the contractor confirms; the approved material becomes the dealer's
// order at the dealer's prices. Two walls hold the money apart: the customer
// never sees the dealer's cost or the contractor's markup (price wall 2,
// public.go), and the dealer never sees the contractor's markup, labour,
// overhead or the customer's sell price (price wall 1, dealer.go).
package customerquote

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The rounding rule, written once. Every money figure is integer cents. A
// figure that is the product of a count and a ratio is computed exactly and
// rounded once, half away from zero, to the cent:
//
//   - a line's extended cost is quantity x unit cost, through httpx.Extend
//     (the platform's one line extension);
//   - a line's sell price is its extended cost carried through its markup:
//     cost x (10000 + bps) / 10000 for a markup on cost, cost x 10000 /
//     (10000 - bps) for a target gross margin;
//   - hourly labour is the rate in cents x hours, percent overhead is its
//     base x basis points / 10000, tax is the taxable base x rate / 10000,
//     the deposit is the total x basis points / 10000.
//
// Totals are sums of rounded lines, never one rounding of an unrounded sum.

// Bounds keep every intermediate product inside what the exact arithmetic and
// the BIGINT columns hold.
const (
	// MaxCostCents is the largest extended cost of one line: ten billion
	// currency units in cents.
	MaxCostCents int64 = 1_000_000_000_000
	// MaxMarkupBPS is the largest markup on cost: ten thousand percent.
	MaxMarkupBPS int64 = 1_000_000
	// BPSWhole is 100 percent in basis points.
	BPSWhole int64 = 10_000
	// MaxTaxRateBPS is the largest customer site tax rate: 50 percent.
	MaxTaxRateBPS int64 = 5_000
)

// MarkupKind says what a markup's basis points mean.
type MarkupKind string

const (
	// KindMarkup is a markup on cost: sell = cost x (1 + bps/10000).
	KindMarkup MarkupKind = "markup"
	// KindMargin is a target gross margin: sell = cost / (1 - bps/10000).
	KindMargin MarkupKind = "margin"
)

// Tier names the level a line's markup came from.
type Tier string

const (
	// TierLine is the line's own override.
	TierLine Tier = "line"
	// TierCategory is the quote's rule for the line's product category.
	TierCategory Tier = "category"
	// TierQuote is the quote's default.
	TierQuote Tier = "quote"
)

// ErrMarginTooHigh refuses a target gross margin of 100 percent or more: no
// finite price earns it.
var ErrMarginTooHigh = errors.New("a target gross margin must be under 100 percent")

// Markup is one tier's rule: a kind and basis points.
type Markup struct {
	Kind MarkupKind `json:"kind"`
	BPS  int64      `json:"bps"`
}

// Validate refuses an unknown kind, a negative figure, a markup past the
// bound and a margin at or above 100 percent.
func (m Markup) Validate() error {
	switch m.Kind {
	case KindMarkup:
		if m.BPS < 0 || m.BPS > MaxMarkupBPS {
			return fmt.Errorf("a markup must be between 0 and %d basis points", MaxMarkupBPS)
		}
	case KindMargin:
		if m.BPS < 0 {
			return errors.New("a margin cannot be negative")
		}
		if m.BPS >= BPSWhole {
			return ErrMarginTooHigh
		}
	default:
		return fmt.Errorf("markup kind %q is neither markup nor margin", string(m.Kind))
	}
	return nil
}

// mulDivHalfUp is a x b / d, exact, rounded once half away from zero. d is
// positive.
func mulDivHalfUp(a, b, d int64) (int64, error) {
	if d <= 0 {
		return 0, errors.New("division by a non positive figure")
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	neg := n.Sign() < 0
	n.Abs(n)
	dd := big.NewInt(d)
	n.Add(n, new(big.Int).Rsh(dd, 1))
	n.Div(n, dd)
	if !n.IsInt64() {
		return 0, errors.New("the amount is too large")
	}
	v := n.Int64()
	if neg {
		v = -v
	}
	return v, nil
}

// SellCents carries an extended cost in cents through a markup.
func SellCents(costCents int64, m Markup) (int64, error) {
	if err := m.Validate(); err != nil {
		return 0, err
	}
	if costCents < 0 || costCents > MaxCostCents {
		return 0, fmt.Errorf("a line cost must be between 0 and %d cents", MaxCostCents)
	}
	if m.Kind == KindMarkup {
		return mulDivHalfUp(costCents, BPSWhole+m.BPS, BPSWhole)
	}
	return mulDivHalfUp(costCents, BPSWhole, BPSWhole-m.BPS)
}

// ResolveMarkup picks the most specific tier that is set: the line's own
// override, then the quote's rule for the line's category, then the quote
// default. A zero override is an override.
func ResolveMarkup(line, category *Markup, quoteDefault Markup) (Markup, Tier) {
	switch {
	case line != nil:
		return *line, TierLine
	case category != nil:
		return *category, TierCategory
	default:
		return quoteDefault, TierQuote
	}
}

// ExtendedCostCents is quantity x unit cost (ten thousandths of a currency
// unit) in cents, through the platform's one extension rule.
func ExtendedCostCents(qty httpx.Quantity, unitCostTenThousandths int64) (int64, error) {
	if qty <= 0 {
		return 0, errors.New("a quantity must be positive")
	}
	if unitCostTenThousandths < 0 {
		return 0, errors.New("a unit cost cannot be negative")
	}
	one := httpx.Quantity(10_000)
	c, err := httpx.Extend(qty, one, one, httpx.Price(unitCostTenThousandths))
	if err != nil {
		return 0, err
	}
	if int64(c) > MaxCostCents {
		return 0, fmt.Errorf("a line cost must be between 0 and %d cents", MaxCostCents)
	}
	return int64(c), nil
}

// SourceLineCostCents reconciles a dealer-quote sourced line's extended cost
// to the dealer quote's own line total (scaled by qty / sourceQty when
// sourceLineTotalCents and sourceQty are positive).
func SourceLineCostCents(qty, sourceQty httpx.Quantity, sourceLineTotalCents, unitCostTenThousandths int64) (int64, error) {
	if qty <= 0 {
		return 0, errors.New("a quantity must be positive")
	}
	if sourceLineTotalCents > 0 && sourceQty > 0 {
		c, err := mulDivHalfUp(sourceLineTotalCents, int64(qty), int64(sourceQty))
		if err != nil {
			return 0, err
		}
		if c < 0 || c > MaxCostCents {
			return 0, fmt.Errorf("a line cost must be between 0 and %d cents", MaxCostCents)
		}
		return c, nil
	}
	return ExtendedCostCents(qty, unitCostTenThousandths)
}

// MaterialLine is the pricing view of a dealer product line.
type MaterialLine struct {
	ID         uuid.UUID
	GroupID    *uuid.UUID
	CategoryID *uuid.UUID
	CostCents  int64
	Override   *Markup

	// Filled by PriceLines.
	Applied   Markup
	Tier      Tier
	SellCents int64
}

// PriceLines resolves each line's tier and sets its applied markup and sell
// price. A rule that cannot be applied (a margin at 100 percent) fails the
// whole quote.
func PriceLines(lines []MaterialLine, categoryRules map[uuid.UUID]Markup, quoteDefault Markup) error {
	if err := quoteDefault.Validate(); err != nil {
		return fmt.Errorf("quote default: %w", err)
	}
	for id, r := range categoryRules {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("category rule %s: %w", id, err)
		}
	}
	for i := range lines {
		l := &lines[i]
		if l.Override != nil {
			if err := l.Override.Validate(); err != nil {
				return fmt.Errorf("line override: %w", err)
			}
		}
		var cat *Markup
		if l.CategoryID != nil {
			if r, ok := categoryRules[*l.CategoryID]; ok {
				rr := r
				cat = &rr
			}
		}
		m, tier := ResolveMarkup(l.Override, cat, quoteDefault)
		sell, err := SellCents(l.CostCents, m)
		if err != nil {
			return err
		}
		l.Applied, l.Tier, l.SellCents = m, tier, sell
	}
	return nil
}

// Labour rate types.
const (
	RateFlat   = "flat"
	RateHourly = "hourly"
)

// LabourLine is a labour line: a flat amount, or an hourly rate times hours.
// A contractor only line is the contractor's own cost: it never reaches the
// customer's price or the customer's view, and it counts against the margin.
type LabourLine struct {
	ID             uuid.UUID
	Description    string
	RateType       string
	RateCents      int64
	Hours          httpx.Quantity
	ContractorOnly bool
}

// LabourTotalCents is a labour line's amount in cents.
func LabourTotalCents(l LabourLine) (int64, error) {
	if l.RateCents < 0 || l.RateCents > MaxCostCents {
		return 0, fmt.Errorf("a labour rate must be between 0 and %d cents", MaxCostCents)
	}
	switch l.RateType {
	case RateFlat:
		return l.RateCents, nil
	case RateHourly:
		if l.Hours <= 0 {
			return 0, errors.New("an hourly labour line needs positive hours")
		}
		v, err := mulDivHalfUp(l.RateCents, int64(l.Hours), 10_000)
		if err != nil || v > MaxCostCents {
			return 0, fmt.Errorf("a labour line must be at most %d cents", MaxCostCents)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("labour rate type %q is neither flat nor hourly", l.RateType)
	}
}

// Overhead amount types.
const (
	AmountFlat    = "flat"
	AmountPercent = "percent"
)

// OverheadLine is an overhead line: a flat amount in cents, or a percentage
// in basis points of the customer facing materials plus labour.
type OverheadLine struct {
	ID             uuid.UUID
	Label          string
	AmountType     string
	Value          int64
	ContractorOnly bool
}

// OverheadTotalCents is an overhead line's amount in cents; baseCents is the
// base a percentage applies to.
func OverheadTotalCents(o OverheadLine, baseCents int64) (int64, error) {
	if o.Value < 0 {
		return 0, errors.New("an overhead amount cannot be negative")
	}
	switch o.AmountType {
	case AmountFlat:
		if o.Value > MaxCostCents {
			return 0, fmt.Errorf("an overhead amount must be at most %d cents", MaxCostCents)
		}
		return o.Value, nil
	case AmountPercent:
		if o.Value > MaxMarkupBPS {
			return 0, fmt.Errorf("an overhead percentage must be at most %d basis points", MaxMarkupBPS)
		}
		if baseCents < 0 || baseCents > MaxCostCents {
			return 0, fmt.Errorf("an overhead base must be between 0 and %d cents", MaxCostCents)
		}
		v, err := mulDivHalfUp(baseCents, o.Value, BPSWhole)
		if err != nil || v > MaxCostCents {
			return 0, fmt.Errorf("an overhead amount must be at most %d cents", MaxCostCents)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("overhead amount type %q is neither flat nor percent", o.AmountType)
	}
}

// Tax modes.
const (
	// TaxSiteRate applies the customer site tax rate to the materials.
	TaxSiteRate = "site_rate"
	// TaxIncluded means every price already carries tax: no tax line.
	TaxIncluded = "included"
	// TaxAtCheckout leaves the material tax to be added when the customer pays.
	TaxAtCheckout = "at_checkout"
)

// SelectionState is the customer's decision on one material line.
type SelectionState string

const (
	SelectionPending  SelectionState = "pending"
	SelectionApproved SelectionState = "approved"
	SelectionDeclined SelectionState = "declined"
)

// TotalsInput is everything a total is a function of.
type TotalsInput struct {
	Lines      []MaterialLine
	States     map[uuid.UUID]SelectionState
	Labour     []LabourLine
	Overhead   []OverheadLine
	TaxMode    string
	TaxRateBPS int64
	DepositBPS int64
}

// Totals are the figures of one quote under one set of selections. The
// margin fields are the contractor's own and appear on no customer or dealer
// surface.
type Totals struct {
	MaterialCostCents   int64 `json:"material_cost_cents"`
	MaterialSellCents   int64 `json:"material_sell_cents"`
	LabourCents         int64 `json:"labour_cents"`
	OverheadCents       int64 `json:"overhead_cents"`
	SubtotalCents       int64 `json:"subtotal_cents"`
	TaxCents            int64 `json:"tax_cents"`
	TotalCents          int64 `json:"total_cents"`
	DepositCents        int64 `json:"deposit_cents"`
	ContractorCostCents int64 `json:"contractor_cost_cents"`
	MarginCents         int64 `json:"margin_cents"`
	MarginBPS           int64 `json:"margin_bps"`
}

// IncludedLines returns the material lines a total counts. A standalone line
// counts unless declined. In an option group the approved line counts; with
// none approved yet, the first line not declined counts (the customer sees
// that choice as the default); all declined, none.
func IncludedLines(lines []MaterialLine, states map[uuid.UUID]SelectionState) []MaterialLine {
	state := func(id uuid.UUID) SelectionState {
		if s, ok := states[id]; ok {
			return s
		}
		return SelectionPending
	}
	chosen := map[uuid.UUID]uuid.UUID{}
	for _, l := range lines {
		if l.GroupID == nil {
			continue
		}
		if state(l.ID) == SelectionApproved {
			if _, taken := chosen[*l.GroupID]; !taken {
				chosen[*l.GroupID] = l.ID
			}
		}
	}
	for _, l := range lines {
		if l.GroupID == nil {
			continue
		}
		if _, taken := chosen[*l.GroupID]; !taken && state(l.ID) != SelectionDeclined {
			chosen[*l.GroupID] = l.ID
		}
	}
	var out []MaterialLine
	for _, l := range lines {
		if l.GroupID == nil {
			if state(l.ID) != SelectionDeclined {
				out = append(out, l)
			}
			continue
		}
		if chosen[*l.GroupID] == l.ID {
			out = append(out, l)
		}
	}
	return out
}

func addBounded(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > MaxCostCents || b > MaxCostCents-a {
		return 0, fmt.Errorf("a quote total must be at most %d cents", MaxCostCents)
	}
	return a + b, nil
}

// Compute totals a quote under its selections.
func Compute(in TotalsInput) (Totals, error) {
	var t Totals
	if in.DepositBPS < 0 || in.DepositBPS > BPSWhole {
		return t, errors.New("a deposit must be between 0 and 100 percent")
	}
	switch in.TaxMode {
	case TaxSiteRate:
		if in.TaxRateBPS < 0 || in.TaxRateBPS > MaxTaxRateBPS {
			return t, fmt.Errorf("a tax rate must be between 0 and %d basis points", MaxTaxRateBPS)
		}
	case TaxIncluded, TaxAtCheckout:
	default:
		return t, fmt.Errorf("tax mode %q is not site_rate, included or at_checkout", in.TaxMode)
	}
	var err error
	for _, l := range IncludedLines(in.Lines, in.States) {
		if t.MaterialCostCents, err = addBounded(t.MaterialCostCents, l.CostCents); err != nil {
			return t, err
		}
		if t.MaterialSellCents, err = addBounded(t.MaterialSellCents, l.SellCents); err != nil {
			return t, err
		}
	}
	for _, l := range in.Labour {
		v, err := LabourTotalCents(l)
		if err != nil {
			return t, err
		}
		if l.ContractorOnly {
			if t.ContractorCostCents, err = addBounded(t.ContractorCostCents, v); err != nil {
				return t, err
			}
		} else {
			if t.LabourCents, err = addBounded(t.LabourCents, v); err != nil {
				return t, err
			}
		}
	}
	base, err := addBounded(t.MaterialSellCents, t.LabourCents)
	if err != nil {
		return t, err
	}
	for _, o := range in.Overhead {
		v, err := OverheadTotalCents(o, base)
		if err != nil {
			return t, err
		}
		if o.ContractorOnly {
			if t.ContractorCostCents, err = addBounded(t.ContractorCostCents, v); err != nil {
				return t, err
			}
		} else {
			if t.OverheadCents, err = addBounded(t.OverheadCents, v); err != nil {
				return t, err
			}
		}
	}
	if t.SubtotalCents, err = addBounded(base, t.OverheadCents); err != nil {
		return t, err
	}
	if in.TaxMode == TaxSiteRate {
		tax, err := mulDivHalfUp(t.MaterialSellCents, in.TaxRateBPS, BPSWhole)
		if err != nil {
			return t, err
		}
		t.TaxCents = tax
	}
	if t.TotalCents, err = addBounded(t.SubtotalCents, t.TaxCents); err != nil {
		return t, err
	}
	dep, err := mulDivHalfUp(t.TotalCents, in.DepositBPS, BPSWhole)
	if err != nil {
		return t, err
	}
	t.DepositCents = dep
	t.MarginCents = t.SubtotalCents - t.MaterialCostCents - t.ContractorCostCents
	if t.SubtotalCents > 0 {
		bps, err := mulDivHalfUp(t.MarginCents, BPSWhole, t.SubtotalCents)
		if err != nil {
			return t, err
		}
		t.MarginBPS = bps
	}
	return t, nil
}

// BoundValidUntil never lets a quote promise its customer longer than the
// dealer holds the price. sourceExpiry is the source dealer quote's expiry
// (nil: none, or no source quote); catalogLines says some line was priced
// from the catalog, so the dealer's configured price protection horizon
// applies too. The tighter bound wins.
func BoundValidUntil(requested time.Time, sourceExpiry *time.Time, catalogLines bool, catalogHorizon time.Time) time.Time {
	out := requested
	if sourceExpiry != nil && sourceExpiry.Before(out) {
		out = *sourceExpiry
	}
	if catalogLines && catalogHorizon.Before(out) {
		out = catalogHorizon
	}
	return out
}
