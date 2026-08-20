// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package money

import "testing"

// CORRECTNESS: dollars convert to the exact cent, in both directions of the
// number line. The negative cases are the ones the `int64(x*100.0 + 0.5)`
// idiom this package replaces gets wrong.
func TestDollarsToCents(t *testing.T) {
	tests := []struct {
		dollars float64
		want    int64
	}{
		{0, 0},
		{0.01, 1},
		{8.20, 820},   // 8.20*100 is 819.9999999999999 in float64
		{10.99, 1099}, // 1098.9999999999998
		{1234.56, 123456},
		{1000, 100000},
		{0.005, 1}, // half away from zero
		{-0.005, -1},
		{-0.01, -1},
		{-100.00, -10000},
		{-1234.56, -123456},
		{-8.20, -820},
	}
	for _, tc := range tests {
		if got := DollarsToCents(tc.dollars); got != tc.want {
			t.Errorf("DollarsToCents(%v) = %d, want %d", tc.dollars, got, tc.want)
		}
	}
}

// CORRECTNESS: a value already expressed in cents is rounded, not rescaled.
func TestRoundToCents(t *testing.T) {
	tests := []struct {
		cents float64
		want  int64
	}{
		{0, 0},
		{82.5, 83}, // $10.00 at 8.25% — half up
		{49.5, 50}, // $6.00 at 8.25%
		{101.805, 102},
		{828.9999999999999, 829}, // 100 cents x 8.29
		{-82.5, -83},             // a credit memo rounds by magnitude too
		{-0.5, -1},
	}
	for _, tc := range tests {
		if got := RoundToCents(tc.cents); got != tc.want {
			t.Errorf("RoundToCents(%v) = %d, want %d", tc.cents, got, tc.want)
		}
	}
}

func TestCentsToDollars(t *testing.T) {
	tests := []struct {
		cents int64
		want  float64
	}{
		{0, 0}, {1, 0.01}, {123456, 1234.56}, {-10000, -100},
	}
	for _, tc := range tests {
		if got := CentsToDollars(tc.cents); got != tc.want {
			t.Errorf("CentsToDollars(%d) = %v, want %v", tc.cents, got, tc.want)
		}
	}
}

// CORRECTNESS: the two conversions round-trip for every whole-cent amount.
func TestRoundTrip(t *testing.T) {
	for cents := int64(-100000); cents <= 100000; cents += 7 {
		if got := DollarsToCents(CentsToDollars(cents)); got != cents {
			t.Fatalf("round trip of %d cents produced %d", cents, got)
		}
	}
}
