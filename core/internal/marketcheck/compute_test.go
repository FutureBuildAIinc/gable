// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck_test

import (
	"testing"

	"github.com/gablelbm/gable/internal/marketcheck"
)

// The suggestion table: band 5 percent, floor 15 percent unless a case says
// otherwise. Cost 500 cents gives a floor of ceil(500/0.85) = 589.
func TestSuggest(t *testing.T) {
	bandBP, floorBP := int64(500), int64(1500)
	cases := []struct {
		name                    string
		ours, theirs, cost      int64
		wantPos                 string
		wantPrice               int64
		wantReason              string
		wantDiffPct             float64
		wantSuggestionAvailable bool
	}{
		{"above lowers to the band", 1000, 800, 500, "above", 840, "lower_to_band", 25.0, true},
		{"above target over the floor", 1000, 600, 500, "above", 630, "lower_to_band", 66.7, true},
		{"above target under the floor", 1000, 550, 500, "above", 589, "held_at_floor", 81.8, true},
		{"below raises to the band", 700, 1000, 500, "below", 950, "raise_to_band", -30.0, true},
		{"below raise held at the floor", 520, 560, 500, "below", 589, "held_at_floor", -7.1, true},
		{"in line keeps", 1000, 1000, 500, "in_line", 1000, "keep", 0.0, true},
		{"in line edge of band is in line", 1050, 1000, 500, "in_line", 1050, "keep", 5.0, true},
		{"in line under the floor takes the floor", 550, 550, 500, "in_line", 589, "held_at_floor", 0.0, true},
		{"no cost, no suggestion", 1000, 800, 0, "above", 0, "no_cost", 25.0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := marketcheck.Suggest(c.ours, c.theirs, c.cost, bandBP, floorBP)
			if got.Position != c.wantPos {
				t.Fatalf("position %q want %q", got.Position, c.wantPos)
			}
			if got.DiffPct != c.wantDiffPct {
				t.Fatalf("diff %v want %v", got.DiffPct, c.wantDiffPct)
			}
			if got.Reason != c.wantReason {
				t.Fatalf("reason %q want %q", got.Reason, c.wantReason)
			}
			if got.SuggestedCents != c.wantPrice {
				t.Fatalf("suggested %d want %d", got.SuggestedCents, c.wantPrice)
			}
		})
	}
}

func TestSuggest_FloorRoundsUp(t *testing.T) {
	// cost 333 at a 10 percent floor: 333/0.9 = 370 exactly; 334 gives 371.11, up to 372.
	if got := marketcheck.Suggest(100, 100, 334, 500, 1000); got.SuggestedCents != 372 || got.Reason != "held_at_floor" {
		t.Fatalf("got %+v", got)
	}
}
