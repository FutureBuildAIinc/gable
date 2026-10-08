// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"fmt"
	"math/big"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// InexactError is R5's refusal: a quantity in a sale unit that does not
// convert exactly into the stocking unit at scale 4. Stock is never rounded,
// so the line is refused, and the refusal names the two neighbouring
// quantities that do convert exactly: the multiples of the smallest exact
// step below and above the one sent.
type InexactError struct {
	From, To  string
	Sent      httpx.Quantity
	Below     httpx.Quantity
	Above     httpx.Quantity
	ExactStep httpx.Quantity
}

// Error renders the refusal of ADR 0006 section 3.4: "does not convert
// exactly into the stocking unit PCS; the nearest quantities that do are
// 9.9988 and 10.0016".
func (e *InexactError) Error() string {
	return fmt.Sprintf("does not convert exactly into the stocking unit %s; the nearest quantities that do are %s and %s",
		e.To, e.Below.WireString(), e.Above.WireString())
}

// Convert converts qty from one unit into another through the two rows of
// the product's set (R5): the from row f (f.A of the sale unit is f.B of the
// stocking unit) and the to row t. The result is exact or the conversion is
// refused with the nearest quantities that are exact.
//
// The arithmetic: qty x f.B / f.A lands in the stocking unit; dividing by
// t.B / t.A (multiplying by t.A / t.B) lands in the to unit, so the whole
// conversion is qty x (f.B x t.A) / (f.A x t.B), exact in big rationals (R3)
// until the one question: is the result a scale 4 decimal?
func Convert(qty httpx.Quantity, from, to string, f, t Pair) (httpx.Quantity, error) {
	if f.A <= 0 || f.B <= 0 || t.A <= 0 || t.B <= 0 {
		return 0, fmt.Errorf("a pair's sides are positive")
	}
	num := new(big.Int).Mul(big.NewInt(int64(qty)), big.NewInt(int64(f.B)))
	num.Mul(num, big.NewInt(int64(t.A)))
	den := new(big.Int).Mul(big.NewInt(int64(f.A)), big.NewInt(int64(t.B)))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() == 0 && q.IsInt64() {
		out := httpx.Quantity(q.Int64())
		if out <= httpx.QuantityMax && out >= -httpx.QuantityMax {
			return out, nil
		}
		return 0, ErrOutOfBound
	}
	// Inexact: name the neighbouring multiples of the smallest exact step.
	// With the ratio N/D in lowest terms, a quantity converts exactly iff D
	// divides its scale 4 integer (gcd(N, D) is 1), so the step is D itself.
	// 0.0028 BF on a 2x4x14, where 1 BF is 3/28 PCS, is the worked case of
	// ADR 0006 section 3.4.
	step := smallestExactStep(f, t)
	below := floorToStep(int64(qty), int64(step))
	return 0, &InexactError{
		From: from, To: to, Sent: qty,
		Below: httpx.Quantity(below), Above: httpx.Quantity(below + int64(step)),
		ExactStep: step,
	}
}

// floorToStep is the largest multiple of step not greater than v, for any
// sign of v.
func floorToStep(v, step int64) int64 {
	if step <= 0 {
		return v
	}
	q := v / step
	if v%step != 0 && v < 0 {
		q--
	}
	return q * step
}

// smallestExactStep is the least positive scale 4 quantity of the from unit
// whose conversion through f and t is exact: the denominator of the reduced
// conversion ratio.
func smallestExactStep(f, t Pair) httpx.Quantity {
	num := new(big.Int).Mul(big.NewInt(int64(f.B)), big.NewInt(int64(t.A)))
	den := new(big.Int).Mul(big.NewInt(int64(f.A)), big.NewInt(int64(t.B)))
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), new(big.Int).Abs(den))
	den.Quo(den, g)
	if !den.IsInt64() || den.Int64() <= 0 || den.Int64() > int64(httpx.QuantityMax) {
		return 10_000 // unreachable for pairs inside the bound; a safe step
	}
	return httpx.Quantity(den.Int64())
}

// ConvertStock converts a sale quantity into the product's stocking unit
// through the sale unit's row (ADR 0006 section 3.4): stock = qty x u.B/u.A,
// exact at scale 4 or refused with the nearest quantities.
func ConvertStock(qty httpx.Quantity, saleUOM, stockUOM string, u Pair) (httpx.Quantity, error) {
	if saleUOM == stockUOM {
		return qty, nil
	}
	return Convert(qty, saleUOM, stockUOM, u, One())
}
