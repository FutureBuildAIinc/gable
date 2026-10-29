// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// Cash rounding without pennies (CP-18).
//
// The Government of Canada elected symmetric rounding to the nearest five
// cents for cash: a total ending in 1, 2, 6 or 7 cents goes down, one ending
// in 3, 4, 8 or 9 cents goes up, and one ending in 0 or 5 cents does not move.
// The CRA publishes the same rule as a table. The rounding touches the cash
// leg only: cheques, cards, an account and store credit settle electronically
// to the exact cent, and the tax is computed before the cash leg moves.
//
// The branch decides whether the rule runs at all (the locations column
// cash_rounding_mode), so a branch that never set it takes the money exactly
// as it was tendered.

// RoundingMode is a branch's cash rounding rule.
type RoundingMode string

const (
	// RoundingOff takes the cash exactly as tendered: the default, and the
	// rule for every branch that has not turned rounding on.
	RoundingOff RoundingMode = "OFF"
	// RoundingNearest5Cents rounds the cash leg to the nearest five cents,
	// symmetrically.
	RoundingNearest5Cents RoundingMode = "NEAREST_5_CENTS"
)

// ParseRoundingMode reads the branch's stored rule. An unknown or empty rule
// is off: the money moves only where a branch has said so.
func ParseRoundingMode(s string) RoundingMode {
	if RoundingMode(s) == RoundingNearest5Cents {
		return RoundingNearest5Cents
	}
	return RoundingOff
}

// RoundCash rounds a cash amount in cents to the nearest five cents,
// symmetrically: endings 1, 2, 6 and 7 go down, endings 3, 4, 8 and 9 go up,
// endings 0 and 5 do not move. It returns the rounded amount and the signed
// difference from the input, in cents, so the rounded amount is always the
// amount plus the difference.
//
// The difference is what the drawer receives over the exact figure: it is
// positive on a round-up, which is income to the branch, and negative on a
// round-down, which is the branch's loss. A negative amount mirrors the
// positive one (the rounding runs on the magnitude) and its difference
// follows the amount.
func RoundCash(cents int64) (rounded int64, diff int64) {
	magnitude := cents
	negative := false
	if magnitude < 0 {
		magnitude = -magnitude
		negative = true
	}
	switch magnitude % 5 {
	case 0:
		rounded = magnitude
	case 1:
		rounded = magnitude - 1
	case 2:
		rounded = magnitude - 2
	case 3:
		rounded = magnitude + 2
	default: // 4
		rounded = magnitude + 1
	}
	if negative {
		rounded = -rounded
	}
	return rounded, rounded - cents
}

// RoundCashFor rounds a cash amount under a branch's rule. The rule off
// takes the amount exactly as it is, and no difference is carried.
func RoundCashFor(mode RoundingMode, cents int64) (rounded int64, diff int64) {
	if mode != RoundingNearest5Cents {
		return cents, 0
	}
	return RoundCash(cents)
}
