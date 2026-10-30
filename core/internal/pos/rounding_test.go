// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The cash rounding table (CP-18): the Government of Canada elected symmetric
// rounding to the nearest five cents and the CRA publishes it as a table. The
// cases below are that table, written over every ending and both signs of the
// input, with the difference the drawer receives over the exact figure.

import (
	"testing"

	"github.com/gablelbm/gable/internal/pos"
)

func TestCP18_RoundCashTable(t *testing.T) {
	cases := []struct {
		name    string
		cents   int64
		rounded int64
		diff    int64
	}{
		{"one cent down", 101, 100, -1},
		{"two cents down", 102, 100, -2},
		{"three cents up", 103, 105, 2},
		{"four cents up", 104, 105, 1},
		{"five cents unchanged", 105, 105, 0},
		{"six cents down", 106, 105, -1},
		{"seven cents down", 107, 105, -2},
		{"eight cents up", 108, 110, 2},
		{"nine cents up", 109, 110, 1},
		{"ten cents unchanged", 110, 110, 0},
		{"a dollar unchanged", 100, 100, 0},
		{"one cent down over a dollar", 201, 200, -1},
		{"nine cents up over two dollars", 209, 210, 1},
		{"a negative amount mirrors the positive", -101, -100, 1},
		{"a negative amount rounds up", -104, -105, -1},
		{"a negative amount rounds down", -106, -105, 1},
		{"a negative amount unchanged", -105, -105, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rounded, diff := pos.RoundCash(c.cents)
			if rounded != c.rounded || diff != c.diff {
				t.Errorf("RoundCash(%d) = (%d, %d), want (%d, %d)", c.cents, rounded, diff, c.rounded, c.diff)
			}
			if rounded != c.cents+diff {
				t.Errorf("RoundCash(%d) = %d, which is not the amount %d plus the difference %d", c.cents, rounded, c.cents, diff)
			}
		})
	}
}

// The branch's rule is the gate: off takes the money exactly, and only the
// nearest five cents rule moves it.
func TestCP18_RoundCashForFollowsTheBranchRule(t *testing.T) {
	if rounded, diff := pos.RoundCashFor(pos.RoundingOff, 104); rounded != 104 || diff != 0 {
		t.Errorf("RoundCashFor(OFF, 104) = (%d, %d), want (104, 0)", rounded, diff)
	}
	if rounded, diff := pos.RoundCashFor(pos.RoundingNearest5Cents, 104); rounded != 105 || diff != 1 {
		t.Errorf("RoundCashFor(NEAREST_5_CENTS, 104) = (%d, %d), want (105, 1)", rounded, diff)
	}
	if pos.ParseRoundingMode("") != pos.RoundingOff {
		t.Errorf("an unset rule must read as off")
	}
	if pos.ParseRoundingMode("NEAREST_5_CENTS") != pos.RoundingNearest5Cents {
		t.Errorf("the nearest five cents rule must read back")
	}
}
