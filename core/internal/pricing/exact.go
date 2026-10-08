// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// The engine's arithmetic rules, stated once (ADR 0006 section 1): every
// conversion, price comparison and derived price is computed in exact
// rational arithmetic (R3), and a derived price is rounded once, to scale 4,
// half away from zero, at the moment the engine returns it (R4.2). A fixed
// price is returned as stored and is never rounded. Nothing here goes
// through float64 and nothing goes through text beyond the one decimal
// string a float64 caller's value is recovered from.

// wireScaleFactor is 10^4: prices, quantities and percentages are all held
// as scale 4 integers on the wire and in the database.
var wireScaleFactor = big.NewInt(10_000)

// ratOfPrice is the exact rational a scale 4 price holds.
func ratOfPrice(p httpx.Price) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(int64(p)), wireScaleFactor)
}

// ratOfQuantity is the exact rational a scale 4 quantity or percentage
// holds. A percentage carried as a Quantity is its own value at scale 4:
// Quantity(105000) is 10.5 percent.
func ratOfQuantity(q httpx.Quantity) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(int64(q)), wireScaleFactor)
}

// ratOfFloat recovers the exact decimal a float64 caller passed in. The
// compatibility entry points keep float64 parameters (ADR 0006 section 9.1),
// and every value they are handed in this product came out of a NUMERIC
// column: the shortest decimal that round trips through the float is that
// column's own value, so parsing it back is exact. A NaN or infinity is an
// error, never a price.
func ratOfFloat(f float64) (*big.Rat, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("a price or quantity that is not a number cannot be priced: %v", f)
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'f', -1, 64))
	if !ok {
		return nil, fmt.Errorf("a price or quantity that is not a decimal cannot be priced: %v", f)
	}
	return r, nil
}

// priceOfRat rounds a rational price once, to scale 4, half away from zero
// (R4.2, the one rounding of a derived price). A value already at scale 4
// passes through unchanged.
func priceOfRat(r *big.Rat) httpx.Price {
	return httpx.Price(roundHalfAway(new(big.Int).Mul(r.Num(), wireScaleFactor), r.Denom()).Int64())
}

// quantityOfRat rounds a rational to a scale 4 quantity the same way; the
// engine uses it for the request quantity the rule bands compare against.
func quantityOfRat(r *big.Rat) (httpx.Quantity, error) {
	v := roundHalfAway(new(big.Int).Mul(r.Num(), wireScaleFactor), r.Denom())
	if !v.IsInt64() || v.Int64() > int64(httpx.QuantityMax) || v.Int64() < -int64(httpx.QuantityMax) {
		return 0, fmt.Errorf("a quantity beyond what a NUMERIC(12,4) column holds cannot be priced")
	}
	return httpx.Quantity(v.Int64()), nil
}

// roundHalfAway divides n by d rounding half away from zero, the rounding
// mode the one extension and the one derived price rounding share (ADR 0001
// section 7a, ADR 0006 R4).
func roundHalfAway(n, d *big.Int) *big.Int {
	if d.Sign() == 0 {
		return new(big.Int)
	}
	neg := n.Sign() < 0
	mag := new(big.Int).Abs(n)
	div := new(big.Int).Abs(d)
	mag.Add(mag, new(big.Int).Rsh(div, 1))
	mag.Div(mag, div)
	if neg {
		return mag.Neg(mag)
	}
	return mag
}

// applyMarkupPercent returns base x (1 + pct/100) exactly.
func applyMarkupPercent(base *big.Rat, pct *big.Rat) *big.Rat {
	f := new(big.Rat).Quo(pct, big.NewRat(100, 1))
	f.Add(big.NewRat(1, 1), f)
	return new(big.Rat).Mul(base, f)
}

// applyMarkdown returns base x (1 - pct/100) exactly.
func applyMarkdown(base *big.Rat, pct *big.Rat) *big.Rat {
	f := new(big.Rat).Quo(pct, big.NewRat(100, 1))
	f.Sub(big.NewRat(1, 1), f)
	return new(big.Rat).Mul(base, f)
}
