// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"fmt"
	"math/big"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// MaxTallyRows is the most rows one tally carries (ADR 0006 section 4.2).
const MaxTallyRows = 100

// TallyRow is one length of a tally: pieces at a length in feet. Pieces is a
// count, always positive; a credit's sign rides the line's quantity, never
// the row.
type TallyRow struct {
	Pieces   int64
	LengthFT httpx.Quantity
}

// LinearFeet sums a tally's rows: the exact sum of pieces x length (R3;
// integers times scale 4 decimals are exact at scale 4), which the ADR makes
// the tallied line's quantity in LF.
func LinearFeet(rows []TallyRow) (httpx.Quantity, error) {
	total := int64(0)
	for i, r := range rows {
		if r.Pieces <= 0 {
			return 0, fmt.Errorf("tally row %d: pieces are positive", i)
		}
		if r.LengthFT <= 0 {
			return 0, fmt.Errorf("tally row %d: the length is positive", i)
		}
		p := new(big.Int).Mul(big.NewInt(r.Pieces), big.NewInt(int64(r.LengthFT)))
		if !p.IsInt64() {
			return 0, ErrOutOfBound
		}
		total += p.Int64()
	}
	out := httpx.Quantity(total)
	if out > httpx.QuantityMax || out < -httpx.QuantityMax {
		return 0, ErrOutOfBound
	}
	return out, nil
}

// BoardFeet computes a tally's board feet exactly (R3): linear feet x
// thickness x width / 12, a big rational. It is never stored and never
// multiplied (R4.3): the money goes through the line's pair, which the unit
// set derived from the same cross section, so the extension is exact board
// foot pricing. 10 pieces at 8 and 5 at 14 of a 2x4 are 150 LF and exactly
// 100 BF; 10 at 10 are exactly 200/3.
func BoardFeet(linearFeet httpx.Quantity, thicknessIn, widthIn httpx.Quantity) (*big.Rat, error) {
	if thicknessIn <= 0 || widthIn <= 0 {
		return nil, fmt.Errorf("a board measure cross section is positive")
	}
	r := ratOf(linearFeet)
	r.Mul(r, ratOf(thicknessIn))
	r.Mul(r, ratOf(widthIn))
	r.Quo(r, big.NewRat(12, 1))
	return r, nil
}

// DisplayBoardFeet is the wire's board_feet: the exact board feet rounded
// once to scale 4, half away from zero (R4.3). No arithmetic reads it;
// 200/3 is "66.6667".
func DisplayBoardFeet(exact *big.Rat) httpx.Quantity {
	scaled := new(big.Rat).Mul(exact, new(big.Rat).SetInt64(10_000))
	i := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	frac := new(big.Rat).Sub(scaled, new(big.Rat).SetInt(i)) // in [0, 1); the sign stays on i
	half := big.NewRat(1, 2)
	neg := exact.Sign() < 0
	mag := new(big.Int).Abs(i)
	if frac.Cmp(half) >= 0 {
		mag.Add(mag, big.NewInt(1))
	}
	out := mag.Int64()
	if neg {
		out = -out
	}
	return httpx.Quantity(out)
}
