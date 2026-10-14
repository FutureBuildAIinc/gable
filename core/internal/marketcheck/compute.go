// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import "math"

// Money is integer cents throughout. The one rounding rule: a price taken from
// a dollar figure is rounded to the nearest cent (roundCents); the margin floor
// is rounded UP to the next cent (so the margin is never under the floor); a
// band target is rounded DOWN to the cent (floorDiv). Percents are held as
// hundredths of a percent ("basis points") so none of this touches a float.

// Reasons for a suggested price.
const (
	ReasonLowerToBand = "lower_to_band"
	ReasonRaiseToBand = "raise_to_band"
	ReasonHeldAtFloor = "held_at_floor"
	ReasonKeep        = "keep"
	ReasonNoCost      = "no_cost"
)

// Positions of our price against the competitor's.
const (
	PositionAbove  = "above"
	PositionBelow  = "below"
	PositionInLine = "in_line"
)

// Suggestion is the server's own reading of one matched row.
type Suggestion struct {
	DiffPct        float64
	Position       string
	SuggestedCents int64 // 0 when SuggestedKnown is false
	SuggestedKnown bool
	Reason         string
}

// roundCents converts a dollar figure to the nearest cent.
func roundCents(dollars float64) int64 { return int64(math.Round(dollars * 100)) }

// Suggest computes the difference, the position and the suggested price. ours,
// theirs and cost are cents; bandBP and floorBP are hundredths of a percent.
// theirs must be above zero.
func Suggest(ours, theirs, cost, bandBP, floorBP int64) Suggestion {
	s := Suggestion{}
	diff := float64(ours-theirs) / float64(theirs) * 100
	s.DiffPct = math.Round(diff*10) / 10
	if s.DiffPct == 0 {
		s.DiffPct = 0 // no negative zero
	}
	switch {
	case (ours-theirs)*10000 > bandBP*theirs:
		s.Position = PositionAbove
	case (theirs-ours)*10000 > bandBP*theirs:
		s.Position = PositionBelow
	default:
		s.Position = PositionInLine
	}
	if cost <= 0 {
		s.Reason = ReasonNoCost
		return s
	}
	// floor = ceil(cost / (1 - floor%)) = ceil(cost * 10000 / (10000 - floorBP))
	floor := ceilDiv(cost*10000, 10000-floorBP)
	s.SuggestedKnown = true
	switch s.Position {
	case PositionAbove:
		target := theirs * (10000 + bandBP) / 10000
		s.SuggestedCents, s.Reason = target, ReasonLowerToBand
		if target < floor {
			s.SuggestedCents, s.Reason = floor, ReasonHeldAtFloor
		}
	case PositionBelow:
		target := theirs * (10000 - bandBP) / 10000
		s.SuggestedCents, s.Reason = target, ReasonRaiseToBand
		if target < floor {
			s.SuggestedCents = floor
		}
	default:
		s.SuggestedCents, s.Reason = ours, ReasonKeep
		if ours < floor {
			s.SuggestedCents, s.Reason = floor, ReasonHeldAtFloor
		}
	}
	return s
}

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}
