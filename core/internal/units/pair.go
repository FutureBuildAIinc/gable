// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package units holds the arithmetic of ADR 0006: the conversion pair and
// its canonical form (section 1 R1 and R2), the resolution of a line's pair
// from a product's unit set (section 3.3), the exact stock conversion (R5)
// and the tally and board foot arithmetic (section 4.3). Everything here is
// exact rational arithmetic over the scale 4 integers the wire and the
// columns carry (R3): no float64 and no parsing through text. The package
// has no database; the unit set service and the quote module apply it.
package units

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// ErrOutOfBound is R2 step 4: a ratio that fits the NUMERIC(12,4) bound in
// no canonical form is refused where it is written, never at a line.
var ErrOutOfBound = errors.New("the conversion does not fit the pair's bound")

// Pair is a conversion between two units as a pair of positive scale 4
// quantities (R1): A of one unit is the same goods as B of the other. The
// zero Pair is not a value; every constructor refuses it.
type Pair struct {
	A httpx.Quantity // the left unit's side
	B httpx.Quantity // the right unit's side
}

// Ratio returns the pair as the exact rational a / b at the wire's scale:
// the numerator and denominator are the scale 4 integers of the two sides.
func (p Pair) Ratio() (*big.Rat, error) {
	if p.A <= 0 || p.B <= 0 {
		return nil, fmt.Errorf("a pair's sides are positive: %s and %s", p.A.WireString(), p.B.WireString())
	}
	return new(big.Rat).SetFrac64(int64(p.A), int64(p.B)), nil
}

// SameRatio reports whether two pairs name the same conversion.
func (p Pair) SameRatio(q Pair) bool {
	rp, err := p.Ratio()
	if err != nil {
		return false
	}
	rq, err := q.Ratio()
	if err != nil {
		return false
	}
	return rp.Cmp(rq) == 0
}

// Swap is the pair read in the other direction: the conversion of B to A.
func (p Pair) Swap() Pair { return Pair{A: p.B, B: p.A} }

// scaleInts is the exact rational of a/b over the scale 4 integers, reduced.
func scaleInts(a, b int64) (*big.Int, *big.Int) {
	num, den := new(big.Int).SetInt64(a), new(big.Int).SetInt64(b)
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), new(big.Int).Abs(den))
	if g.Sign() == 0 {
		return num, den
	}
	return num.Quo(num, g), den.Quo(den, g)
}

// exactAtScale4 reports whether the rational num/den (both positive, in the
// scale 4 integer representation) is a decimal with at most four fraction
// digits, and returns that scale 4 integer when it is.
func exactAtScale4(num, den *big.Int) (int64, bool) {
	scaled := new(big.Int).Mul(num, big.NewInt(10_000))
	q, r := new(big.Int).QuoRem(scaled, den, new(big.Int))
	if r.Sign() != 0 {
		return 0, false
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

// quantityOfScale turns a scale 4 integer into its Quantity, refusing one
// past the NUMERIC(12,4) bound.
func quantityOfScale(v int64) (httpx.Quantity, error) {
	q := httpx.Quantity(v)
	if v > int64(httpx.QuantityMax) || v < -int64(httpx.QuantityMax) {
		return 0, fmt.Errorf("%d is beyond the quantity bound", v)
	}
	return q, nil
}

// Canonical puts a pair into the one canonical form of R2, so that equal
// ratios are equal bytes. a and b are the pair as sent or as stored (any
// equivalent pair; both must be positive). The form, in order:
//
//  1. if both r = a/b and 1/r are exact decimals at scale 4, the 1 goes on
//     the side that leaves the other side at least 1: (r, 1) when r >= 1,
//     else (1, 1/r);
//  2. else if r is exact, the pair is (r, 1); else if 1/r is exact, the
//     pair is (1, 1/r);
//  3. else r in lowest integer terms (p, q) when both fit the bound; when
//     they do not, (p x k, q x k) for the largest k of 0.1, 0.01, 0.001 and
//     0.0001 for which both fit, the multiple closest to the integer form;
//  4. a ratio that fits the bound in no form above is refused
//     (ErrOutOfBound), never at a line.
//
// Worked (ADR 0006 R2): 187.5 PCS to 1 MBF is (187.5, 1); 8 LF to 1 PCS is
// (8, 1); 1 BF of 2x4 is 1.5 LF, so (1, 1.5); 28 BF = 3 PCS of 2x4x14 is
// (28, 3); GAL to CF is (576, 77).
func Canonical(a, b httpx.Quantity) (Pair, error) {
	if a <= 0 || b <= 0 {
		return Pair{}, fmt.Errorf("a pair's sides are positive: %s and %s", a.WireString(), b.WireString())
	}
	num, den := scaleInts(int64(a), int64(b))
	r := new(big.Rat).SetFrac(num, den)
	cmp := r.Cmp(big.NewRat(1, 1))

	// Rules 1 and 2: the side that can be a plain decimal at scale 4. A
	// side past the bound is not a form of the pair, and the record's step
	// 3 sub multiples may still hold it, so each rule is taken only when
	// its pair fits.
	rScaled, rExact := exactAtScale4(num, den)
	invScaled, invExact := exactAtScale4(den, num)
	if rExact && invExact {
		// Rule 1: the 1 on the side that leaves the other at least 1.
		if cmp >= 0 {
			if p, err := pairFromScale(rScaled, 10_000); err == nil {
				return p, nil
			}
		} else if p, err := pairFromScale(10_000, invScaled); err == nil {
			return p, nil
		}
	} else if rExact {
		// Rule 2: r is exact and 1/r is not, so (r, 1).
		if p, err := pairFromScale(rScaled, 10_000); err == nil {
			return p, nil
		}
	} else if invExact {
		// Rule 2: only 1/r is exact, so (1, 1/r).
		if p, err := pairFromScale(10_000, invScaled); err == nil {
			return p, nil
		}
	}

	// Rule 3: neither side is exact, so the lowest integer terms. num and
	// den are the decimal ratio's coprime integer terms (scaleInts reduced
	// them); the pair is (num, den) as decimals, and when a side does not
	// fit the bound, (num x k, den x k) for the largest k of 0.1 to 0.0001,
	// the multiple closest to the integer form. Every such side is exact at
	// scale 4.
	return integerTermsPair(num, den)
}

// integerTermsPair is rule 3 of R2 over the reduced, positive, non exact
// rational num/den: the integer terms as decimals, then their largest
// decimal sub multiple that fits the bound.
func integerTermsPair(num, den *big.Int) (Pair, error) {
	for _, k := range []int64{10_000, 1_000, 100, 10, 1} {
		ps := new(big.Int).Mul(num, big.NewInt(k))
		qs := new(big.Int).Mul(den, big.NewInt(k))
		if !ps.IsInt64() || !qs.IsInt64() {
			continue
		}
		if pair, err := pairFromScale(ps.Int64(), qs.Int64()); err == nil {
			return pair, nil
		}
	}
	return Pair{}, ErrOutOfBound
}

// pairFromScale builds a Pair from two positive scale 4 integers, refusing a
// side past the bound.
func pairFromScale(a, b int64) (Pair, error) {
	if a <= 0 || b <= 0 {
		return Pair{}, fmt.Errorf("a pair's sides are positive")
	}
	qa, err := quantityOfScale(a)
	if err != nil {
		return Pair{}, ErrOutOfBound
	}
	qb, err := quantityOfScale(b)
	if err != nil {
		return Pair{}, ErrOutOfBound
	}
	return Pair{A: qa, B: qb}, nil
}

// ResolveLinePair resolves a line's pair from the two unit set rows of ADR
// 0006 section 3.3. The line is written in unit U and priced in unit P; u is
// U's row of the product's set, p is P's: u = (u_unit, u_stock) meaning
// u_unit of U is u_stock of the stocking unit, and likewise p. One U is
// u_stock / u_unit stocking units and one P is p_stock / p_unit of them, so
// x U = y P exactly when x / y = (u_unit x p_stock) / (p_unit x u_stock);
// the line's pair is that ratio in canonical form.
//
// Worked: a 2x4x8 sold by PCS and priced per MBF, u = (1, 1) and
// p = (1, 187.5), gives (187.5, 1); a 2x4x14 by the piece per MBF gives
// (750, 7); fasteners by EA per M give (1000, 1).
func ResolveLinePair(u, p Pair) (Pair, error) {
	if u.A <= 0 || u.B <= 0 || p.A <= 0 || p.B <= 0 {
		return Pair{}, fmt.Errorf("a pair's sides are positive")
	}
	// (u_unit x p_stock) : (p_unit x u_stock), as one rational ratio.
	num := new(big.Int).Mul(big.NewInt(int64(u.A)), big.NewInt(int64(p.B)))
	den := new(big.Int).Mul(big.NewInt(int64(p.A)), big.NewInt(int64(u.B)))
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), new(big.Int).Abs(den))
	num.Quo(num, g)
	den.Quo(den, g)
	// Canonical of the ratio num/den: bring both sides back to scale 4
	// integers through the shared power of ten, then canonicalize. The
	// sides of a ratio of scale 4 integers are integers times a common
	// 10^-d, so multiplying by the largest 10^d that keeps both in the int64
	// range reaches the integer form when it fits, exactly rule 3's ladder.
	return canonicalRatio(num, den)
}

// canonicalRatio is Canonical over an already reduced positive rational.
func canonicalRatio(num, den *big.Int) (Pair, error) {
	rScaled, rExact := exactAtScale4(num, den)
	invScaled, invExact := exactAtScale4(den, num)
	cmp := new(big.Rat).SetFrac(num, den).Cmp(big.NewRat(1, 1))
	if rExact && invExact {
		if cmp >= 0 {
			if p, err := pairFromScale(rScaled, 10_000); err == nil {
				return p, nil
			}
		} else if p, err := pairFromScale(10_000, invScaled); err == nil {
			return p, nil
		}
	} else if rExact {
		if p, err := pairFromScale(rScaled, 10_000); err == nil {
			return p, nil
		}
	} else if invExact {
		if p, err := pairFromScale(10_000, invScaled); err == nil {
			return p, nil
		}
	}
	return integerTermsPair(num, den)
}

// One is the pair of two equal units, 1 and 1.
func One() Pair { return Pair{A: 10_000, B: 10_000} }

// StandardSizePair is the canonical pair of a unit's standard size (ADR 0006
// section 2.2): stdUnitQty of the unit is stdRefQty of its dimension's
// reference unit. "1 = 100 SF" is the pair (1, 100); "576 GAL = 77 CF" is
// (576, 77).
func StandardSizePair(stdUnitQty, stdRefQty httpx.Quantity) (Pair, error) {
	return Canonical(stdUnitQty, stdRefQty)
}
