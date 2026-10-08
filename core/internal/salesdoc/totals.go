// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"errors"
	"math/big"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Totals are a document's money summary (ADR 0005 section 3): the subtotal
// over non text lines, the taxable base, the tax and the total. Tax is
// computed by TaxAt from the taxable base and the rate, or taken from a
// provider's answer.
type Totals struct {
	SubtotalCents httpx.Cents
	TaxableCents  httpx.Cents
	TaxCents      httpx.Cents
	TotalCents    httpx.Cents
}

// SumTotals sums a document's extended lines: subtotal over every non text
// line, taxable over the lines whose taxable flag is set. The caller adds
// the tax.
func SumTotals(lines []Line) Totals {
	var t Totals
	for i := range lines {
		l := &lines[i]
		if l.LineType == LineText {
			continue
		}
		if l.LineTotal != nil {
			t.SubtotalCents += *l.LineTotal
			if l.Taxable {
				t.TaxableCents += *l.LineTotal
			}
		}
	}
	return t
}

// taxRateScale is the scale of a tax rate column: NUMERIC(9,6), read as a
// decimal string, so a rate such as 0.08875 is held exactly.
const taxRateScale = 6

// ErrTaxRateNotConfigured is the refusal of ADR 0005 section 3 step 4: no
// rate is configured for the document's branch (or ship-to). Zero is a valid
// configured rate; the absence of a rate is not. The caller answers 409
// conflict with the blocker tax_rate_not_configured.
var ErrTaxRateNotConfigured = errors.New("tax rate not configured")

// ParseTaxRate parses a rate's decimal string ("0.088750") into its scale 6
// integer. An empty string is no rate; anything unparsable is an error, never
// a silent zero.
func ParseTaxRate(s string) (int64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	v, err := parseFixedScale(s, taxRateScale)
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

// TaxAt computes the document's tax from its taxable base and a resolved
// rate: round_half_away(taxable x rate), once per document, exact until that
// one rounding (ADR 0005 section 3).
func TaxAt(taxableCents httpx.Cents, rateScaled int64) httpx.Cents {
	// taxable (cents, scale 2 of the major unit) x rate (scale 6) is scale 8;
	// a cent is scale 2, so the divisor carries 10^6.
	n := big.NewInt(int64(taxableCents) * rateScaled)
	div := big.NewInt(1)
	for i := 0; i < taxRateScale; i++ {
		div.Mul(div, big.NewInt(10))
	}
	neg := n.Sign() < 0
	var mag big.Int
	mag.Abs(n)
	mag.Add(&mag, new(big.Int).Rsh(div, 1))
	mag.Div(&mag, div)
	cents := mag.Int64()
	if neg {
		cents = -cents
	}
	return httpx.Cents(cents)
}

// TaxInputs are the ingredients of a document's rate resolution (ADR 0005
// section 3): the customer's exemption, the delivery ship-to's rate and the
// branch's rate, each read by the document's own repository.
type TaxInputs struct {
	Exempt     bool
	Delivery   bool
	ShipToRate *string
	BranchRate *string
}

// ResolvedTax is the outcome of the rate resolution.
type ResolvedTax struct {
	Rate   *string // the decimal string, null when the provider answered
	Source TaxSource
}

// ResolveTax runs the rate resolution of ADR 0005 section 3, after the
// exemption check and without a provider: an exempt customer pays nothing;
// a delivery takes its ship-to's rate when set; then the branch's rate; a
// document with no configured rate anywhere is refused. Zero is a valid
// configured rate. The provider path sits in front of this in the document's
// own act: when a provider is configured and the customer is not exempt, the
// provider's total tax becomes the document's tax and this resolution never
// runs.
func ResolveTax(in TaxInputs) (ResolvedTax, error) {
	if in.Exempt {
		zero := "0.000000"
		return ResolvedTax{Rate: &zero, Source: TaxSourceExempt}, nil
	}
	if in.Delivery && in.ShipToRate != nil && strings.TrimSpace(*in.ShipToRate) != "" {
		rate := strings.TrimSpace(*in.ShipToRate)
		if _, _, err := ParseTaxRate(rate); err != nil {
			return ResolvedTax{}, err
		}
		return ResolvedTax{Rate: &rate, Source: TaxSourceShipToRate}, nil
	}
	if in.BranchRate != nil && strings.TrimSpace(*in.BranchRate) != "" {
		rate := strings.TrimSpace(*in.BranchRate)
		if _, _, err := ParseTaxRate(rate); err != nil {
			return ResolvedTax{}, err
		}
		return ResolvedTax{Rate: &rate, Source: TaxSourceBranchRate}, nil
	}
	return ResolvedTax{}, ErrTaxRateNotConfigured
}

// parseFixedScale parses a plain decimal string into a scaled int64 exactly,
// like the platform's fixed-scale parser, at a rate's scale. Rates are read
// as decimal strings and never through float64.
func parseFixedScale(s string, scale int) (int64, error) {
	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	intPart, fracPart := body, ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart, fracPart = body[:i], body[i+1:]
	}
	if intPart == "" {
		return 0, errors.New("not a plain decimal")
	}
	var value int64
	for _, d := range intPart {
		if d < '0' || d > '9' {
			return 0, errors.New("not a plain decimal")
		}
		value = value*10 + int64(d-'0')
	}
	// The fraction's digits walk in at their own places, then the remaining
	// scale pads with zeros: 8.875 at scale 4 is 88750, never 80000875.
	used := len(fracPart)
	if used > scale {
		used = scale
	}
	for i, d := range fracPart {
		if d < '0' || d > '9' {
			return 0, errors.New("not a plain decimal")
		}
		if i < used {
			value = value*10 + int64(d-'0')
		} else if d != '0' {
			return 0, errors.New("precision beyond the rate's scale")
		}
	}
	for i := 0; i < scale-used; i++ {
		value *= 10
	}
	if neg {
		value = -value
	}
	return value, nil
}

func ptrOf[T any](v T) *T { return &v }

func ptrQty(q httpx.Quantity) *httpx.Quantity { return &q }
