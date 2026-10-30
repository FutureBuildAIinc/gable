// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// denominators is the closed list of dispense denominators a formula may
// carry: the physical increments of the trade's tinting equipment (a shot is
// 1/48 or 1/96 of a fluid ounce on a manual machine; the automatic machines
// dispense 1/128, 1/192, 1/384 and 1/1024), with the guide columns 1/32 and
// 1/64 beside them.
var denominators = map[int]bool{32: true, 48: true, 64: true, 128: true, 192: true, 384: true, 1024: true}

// ValidDenominator reports whether d is one of the closed list.
func ValidDenominator(d int) bool { return denominators[d] }

// Amount is one colorant amount exactly as the manufacturer guides print it:
// whole fluid ounces, a count of the coarse 1/32 step, and a count of the
// fine 1/denominator step. It is never a decimal quantity (the quantity type
// is scale 4 and a 48th of an ounce does not fit it); the denominator rides
// beside the counts so two systems' formulas never collide.
type Amount struct {
	WholeOZ     int `json:"whole_oz"`
	Coarse      int `json:"increments_coarse"`
	Fine        int `json:"increments_fine"`
	Denominator int `json:"unit_denominator"`
}

// Validate refuses an amount whose denominator is not the trade's, whose
// counts are negative, or whose coarse count cannot exist in its own fine
// grid (a 1/32 step is never a whole number of 1/48 or 1/192 units, so a
// shot system's amount carries its whole amount in the fine count alone).
func (a Amount) Validate() error {
	if !ValidDenominator(a.Denominator) {
		return fmt.Errorf("the denominator %d is not one of the trade's dispense increments", a.Denominator)
	}
	if a.WholeOZ < 0 || a.Coarse < 0 || a.Fine < 0 {
		return fmt.Errorf("a colorant amount is never negative")
	}
	if a.Denominator%32 != 0 && a.Coarse != 0 {
		return fmt.Errorf("a 1/32 count cannot be carried in a 1/%d grid: put the whole amount in the fine count", a.Denominator)
	}
	return nil
}

// FineUnits is the amount as a whole count of its own denominator: the exact
// integer the guides' columns sum to.
func (a Amount) FineUnits() int64 {
	if a.Denominator == 0 {
		return 0
	}
	return int64(a.WholeOZ)*int64(a.Denominator) + int64(a.Coarse)*(int64(a.Denominator)/32) + int64(a.Fine)
}

// AmountOf builds the amount back from a whole count of the denominator.
func AmountOf(fine int64, denominator int) Amount {
	if denominator == 0 {
		return Amount{Denominator: denominator}
	}
	whole := fine / int64(denominator)
	rem := fine % int64(denominator)
	coarse := int64(0)
	if denominator%32 == 0 {
		coarse = rem / int64(denominator/32)
		rem %= int64(denominator / 32)
	}
	return Amount{WholeOZ: int(whole), Coarse: int(coarse), Fine: int(rem), Denominator: denominator}
}

// String prints the amount in the manufacturer's own layout: whole ounces,
// then the 1/32 count, then the fine count, each skipped when zero. What it
// prints can be read against a guide side by side.
func (a Amount) String() string {
	if a.Denominator == 0 {
		return "0"
	}
	parts := []string{}
	if a.WholeOZ > 0 {
		parts = append(parts, fmt.Sprintf("%d OZ", a.WholeOZ))
	}
	if a.Coarse > 0 {
		parts = append(parts, fmt.Sprintf("%d/32", a.Coarse))
	}
	if a.Fine > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", a.Fine, a.Denominator))
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " + ")
}

// Rescaled is one rescaled component: the amount and whether the volume
// ratio forced a rounding on it.
type Rescaled struct {
	Code    string `json:"colorant_code"`
	Name    string `json:"colorant_name"`
	Amount  Amount `json:"amount"`
	Rounded bool   `json:"rounded"`
}

// Rescale multiplies each component by the ratio of the target container
// volume to the source volume, in exact rational arithmetic (no float), and
// rounds each component independently to a whole count of its own dispense
// unit, half up: a rounded count of one more follows a remainder of exactly
// half, a direction this package pins so it cannot drift. A component whose
// scaled amount rounds to zero is dropped. AnyRounded reports whether at
// least one component was rounded, so the counter can see the value is a
// derived approximation and override it.
func Rescale(components []Component, sourceVolumeOZ, targetVolumeOZ int) ([]Rescaled, bool, error) {
	if sourceVolumeOZ <= 0 || targetVolumeOZ <= 0 {
		return nil, false, fmt.Errorf("a container volume is a positive whole number of ounces")
	}
	ratio := new(big.Rat).SetFrac(big.NewInt(int64(targetVolumeOZ)), big.NewInt(int64(sourceVolumeOZ)))
	out := make([]Rescaled, 0, len(components))
	anyRounded := false
	for _, c := range components {
		if err := c.Amount.Validate(); err != nil {
			return nil, false, fmt.Errorf("colorant %s: %w", c.Code, err)
		}
		scaled := new(big.Rat).Mul(new(big.Rat).SetInt64(c.Amount.FineUnits()), ratio)
		// scaled is fine units as a rational; round half up to a whole count
		i := new(big.Int)
		frac := new(big.Rat)
		i.Quo(scaled.Num(), scaled.Denom())
		frac.Sub(scaled, new(big.Rat).SetInt(i))
		if frac.Cmp(big.NewRat(1, 2)) >= 0 {
			i.Add(i, big.NewInt(1))
		}
		rounded := frac.Sign() != 0
		fine := i.Int64()
		if fine == 0 {
			continue
		}
		if rounded {
			anyRounded = true
		}
		out = append(out, Rescaled{
			Code:    c.Code,
			Name:    c.Name,
			Amount:  AmountOf(fine, c.Amount.Denominator),
			Rounded: rounded,
		})
	}
	return out, anyRounded, nil
}

// Component is one colorant of a formula: its code, its name and its amount.
type Component struct {
	ColorantID uuid.UUID `json:"colorant_id"`
	Code       string    `json:"colorant_code"`
	Name       string    `json:"colorant_name"`
	Amount     Amount    `json:"amount"`
}
