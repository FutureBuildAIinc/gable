// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"testing"
)

func comp(code string, whole, coarse, fine, den int) Component {
	return Component{Code: code, Amount: Amount{WholeOZ: whole, Coarse: coarse, Fine: fine, Denominator: den}}
}

// The exact case: the five gallon book is exactly five times the one gallon
// book, because a count of 1/32 times five is still a whole count of 1/32
// and a count of 1/384 times five is still a whole count of 1/384.
func TestRescaleExactRatioReportsNoRounding(t *testing.T) {
	// Autumn Brown, one gallon: R3 1 OZ, S1 5 OZ + 12/32, W1 1 OZ + 24/32,
	// Y3 1 OZ + 8/32, in a 1/384 fine grid (12/32 = 144/384, whole numbers).
	in := []Component{
		comp("R3", 1, 0, 0, 384),
		comp("S1", 5, 12, 0, 384),
		comp("W1", 1, 24, 0, 384),
		comp("Y3", 1, 8, 0, 384),
	}
	out, rounded, err := Rescale(in, 128, 640) // one gallon to five, in ounces
	if err != nil {
		t.Fatalf("rescale failed: %v", err)
	}
	if rounded {
		t.Fatal("an exact five fold ratio rounds nothing")
	}
	want := []struct {
		code                string
		whole, coarse, fine int
	}{
		{"R3", 5, 0, 0},
		{"S1", 26, 28, 0},
		{"W1", 8, 24, 0},
		{"Y3", 6, 8, 0},
	}
	if len(out) != len(want) {
		t.Fatalf("got %d components, want %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i].Code != w.code || out[i].Amount.WholeOZ != w.whole ||
			out[i].Amount.Coarse != w.coarse || out[i].Amount.Fine != w.fine {
			t.Errorf("component %d: got %s %d+%d/32+%d/384, want %s %d+%d/32+%d/384",
				i, out[i].Code, out[i].Amount.WholeOZ, out[i].Amount.Coarse, out[i].Amount.Fine,
				w.code, w.whole, w.coarse, w.fine)
		}
		if out[i].Rounded {
			t.Errorf("component %d reports rounding on an exact ratio", i)
		}
	}
}

// The pinned inexact case: 1/32 alone scaled by a quarter is 1/128, which is
// not a whole count of 1/384 (it is 3/384 exactly), so use a 1/192 grid
// instead: 1/32 = 6/192, a quarter is 1.5/192, which rounds.
func TestRescaleQuarterOfAThirtySecondRoundsHalfUp(t *testing.T) {
	// A component of 1/32 OZ in a 1/192 grid, scaled to a quarter: 6/192 * 1/4
	// = 1.5/192, exactly half. The pinned direction is up: 2/192.
	in := []Component{comp("B1", 0, 1, 0, 192)}
	out, rounded, err := Rescale(in, 128, 32)
	if err != nil {
		t.Fatalf("rescale failed: %v", err)
	}
	if !rounded {
		t.Fatal("1.5 fine units is not whole: the rounding flag must be set")
	}
	if len(out) != 1 {
		t.Fatalf("got %d components, want 1", len(out))
	}
	if got := out[0].Amount.FineUnits(); got != 2 {
		t.Fatalf("half rounds up: got %d fine units, want 2", got)
	}
}

// A component whose scaled amount rounds to zero is dropped, never kept as a
// zero row.
func TestRescaleDropsAComponentThatRoundsToZero(t *testing.T) {
	// 1/128 OZ (Fine 1 of 128) scaled to a quarter: 0.25/128 rounds to 0.
	in := []Component{
		comp("W1", 2, 0, 0, 128),
		comp("B1", 0, 0, 1, 128),
	}
	out, _, err := Rescale(in, 128, 32)
	if err != nil {
		t.Fatalf("rescale failed: %v", err)
	}
	if len(out) != 1 || out[0].Code != "W1" {
		t.Fatalf("the zero rounding component must be dropped; got %+v", out)
	}
}

// An empty formula rescales to an empty result with no error.
func TestRescaleEmptyFormula(t *testing.T) {
	out, rounded, err := Rescale(nil, 128, 32)
	if err != nil {
		t.Fatalf("rescale failed: %v", err)
	}
	if len(out) != 0 || rounded {
		t.Fatalf("an empty formula rescales to nothing: got %+v rounded=%v", out, rounded)
	}
}

// A bad volume is refused.
func TestRescaleRefusesABadVolume(t *testing.T) {
	if _, _, err := Rescale([]Component{comp("W1", 1, 0, 0, 32)}, 0, 32); err == nil {
		t.Fatal("a zero source volume must be refused")
	}
	if _, _, err := Rescale([]Component{comp("W1", 1, 0, 0, 32)}, 128, -1); err == nil {
		t.Fatal("a negative target volume must be refused")
	}
}

// The per row denominator is used, not a system default, so two systems in
// one catalogue do not collide: the same coarse count decomposes differently
// in a 1/64 and a 1/128 grid.
func TestAmountUsesItsOwnDenominator(t *testing.T) {
	a64 := Amount{WholeOZ: 0, Coarse: 1, Fine: 0, Denominator: 64}
	if got := a64.FineUnits(); got != 2 {
		t.Fatalf("1/32 is 2/64: got %d", got)
	}
	a128 := Amount{WholeOZ: 0, Coarse: 1, Fine: 0, Denominator: 128}
	if got := a128.FineUnits(); got != 4 {
		t.Fatalf("1/32 is 4/128: got %d", got)
	}
	// and the build back from fine units keeps the guide layout
	back := AmountOf(4, 128)
	if back.Coarse != 1 || back.Fine != 0 || back.WholeOZ != 0 {
		t.Fatalf("4/128 is 1/32: got %+v", back)
	}
}

// The printer lays the amount out as the manufacturer's own guide does, so a
// formula Gable prints reads against the book side by side.
func TestAmountStringPrintsTheGuideLayout(t *testing.T) {
	cases := []struct {
		a    Amount
		want string
	}{
		{Amount{WholeOZ: 1, Coarse: 12, Fine: 0, Denominator: 32}, "1 OZ + 12/32"},
		{Amount{WholeOZ: 0, Coarse: 0, Fine: 7, Denominator: 384}, "7/384"},
		{Amount{WholeOZ: 2, Coarse: 0, Fine: 0, Denominator: 48}, "2 OZ"},
		{Amount{WholeOZ: 0, Coarse: 0, Fine: 0, Denominator: 32}, "0"},
	}
	for _, c := range cases {
		if got := c.a.String(); got != c.want {
			t.Errorf("printed %q, want %q", got, c.want)
		}
	}
}

// The validation: a denominator outside the trade's list is refused, a
// negative count is refused, and a 1/32 count cannot exist in a grid it does
// not divide (a 1/48 shot system carries its whole amount in the fine count).
func TestAmountValidate(t *testing.T) {
	if err := (Amount{WholeOZ: 1, Denominator: 100}).Validate(); err == nil {
		t.Fatal("a denominator of 100 must be refused")
	}
	if err := (Amount{WholeOZ: -1, Denominator: 32}).Validate(); err == nil {
		t.Fatal("a negative whole count must be refused")
	}
	if err := (Amount{Coarse: 1, Denominator: 48}).Validate(); err == nil {
		t.Fatal("a 1/32 count in a 1/48 grid must be refused")
	}
	if err := (Amount{Coarse: 1, Denominator: 384}).Validate(); err != nil {
		t.Fatalf("a 1/32 count in a 1/384 grid is fine: %v", err)
	}
	if err := (Amount{Fine: 3, Denominator: 48}).Validate(); err != nil {
		t.Fatalf("a fine count in a 1/48 grid is fine: %v", err)
	}
}
