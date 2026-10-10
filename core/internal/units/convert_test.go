// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"testing"
)

// TestConvertStockExact covers R5: a quantity in a sale unit converts into
// the stocking unit exactly at scale 4, or the line is refused with the
// neighbouring exact quantities.
func TestConvertStockExact(t *testing.T) {
	// 10 BF of a 2x4x14 (1 BF is 3/28 PCS) does not convert; the nearest
	// quantities that do are 9.9988 and 10.0016, the neighbouring multiples
	// of the smallest exact step 0.0028 BF (ADR 0006 3.4's own example).
	bf := Pair{A: q("28"), B: q("3")} // 28 BF = 3 PCS
	_, err := ConvertStock(q("10"), "BF", "PCS", bf)
	inexact, ok := err.(*InexactError)
	if !ok {
		t.Fatalf("10 BF of a 2x4x14 is refused, got %v", err)
	}
	if inexact.ExactStep != q("0.0028") {
		t.Errorf("the smallest exact step is %s; want 0.0028", inexact.ExactStep.WireString())
	}
	if inexact.Below != q("9.9988") || inexact.Above != q("10.0016") {
		t.Errorf("the nearest exact quantities are %s and %s; want 9.9988 and 10.0016",
			inexact.Below.WireString(), inexact.Above.WireString())
	}
	if got := inexact.Error(); got != "does not convert exactly into the stocking unit PCS; the nearest quantities that do are 9.9988 and 10.0016" {
		t.Errorf("the refusal message is %q", got)
	}

	// An exact multiple of the step converts: 28 BF is 3 PCS exactly.
	got, err := ConvertStock(q("28"), "BF", "PCS", bf)
	if err != nil || got != q("3") {
		t.Errorf("28 BF = 3 PCS, got %s, %v", got.WireString(), err)
	}
	// A paver sold by SF against PCS stock at 3 SF = 8 PCS: 3 SF converts,
	// 1 SF does not, and its neighbours are the multiples of the smallest
	// exact step 0.0003 SF around it.
	sf := Pair{A: q("3"), B: q("8")}
	if got, err := ConvertStock(q("3"), "SF", "PCS", sf); err != nil || got != q("8") {
		t.Errorf("3 SF = 8 PCS, got %s, %v", got.WireString(), err)
	}
	_, err = ConvertStock(q("1"), "SF", "PCS", sf)
	if inexact, ok := err.(*InexactError); !ok {
		t.Errorf("1 SF is refused, got %v", err)
	} else if inexact.ExactStep != q("0.0003") || inexact.Below != q("0.9999") || inexact.Above != q("1.0002") {
		t.Errorf("1 SF's step and neighbours are %s, %s and %s; want 0.0003, 0.9999 and 1.0002",
			inexact.ExactStep.WireString(), inexact.Below.WireString(), inexact.Above.WireString())
	}
	// The stocking unit itself is exact by construction.
	if got, err := ConvertStock(q("10.5"), "PCS", "PCS", One()); err != nil || got != q("10.5") {
		t.Errorf("the stocking unit converts trivially, got %s, %v", got.WireString(), err)
	}
}

// TestConvertStockRefusalNamesHoldableQuantities proves the inexact refusal
// names only quantities the line can hold: never zero, never past the
// quantity bound. A row whose smallest exact step is huge (the from row
// (99999999.9989, 1), whose step is the prime 99999999.9989) used to name
// zero for a small quantity and a past the bound multiple above a large one.
func TestConvertStockRefusalNamesHoldableQuantities(t *testing.T) {
	prime := Pair{A: q("99999999.9989"), B: q("1")}

	// 12.3456 of the from unit: the step below is zero, which a line cannot
	// hold, so the refusal names the step above alone.
	_, err := ConvertStock(q("12.3456"), "XX", "PCS", prime)
	inexact, ok := err.(*InexactError)
	if !ok {
		t.Fatalf("12.3456 against the prime step row is refused, got %v", err)
	}
	if inexact.Below != 0 || inexact.Above != q("99999999.9989") {
		t.Errorf("the neighbours are %s and %s; want 0 and 99999999.9989",
			inexact.Below.WireString(), inexact.Above.WireString())
	}
	if got := inexact.Error(); got != "does not convert exactly into the stocking unit PCS; the nearest quantity that does is 99999999.9989" {
		t.Errorf("the refusal names the one holdable neighbour, got %q", got)
	}

	// 99999999.9999: the step above is 199999999.9978, past the bound, so
	// the refusal names the step below alone.
	_, err = ConvertStock(q("99999999.9999"), "XX", "PCS", prime)
	if inexact, ok = err.(*InexactError); !ok {
		t.Fatalf("99999999.9999 against the prime step row is refused, got %v", err)
	}
	if got := inexact.Error(); got != "does not convert exactly into the stocking unit PCS; the nearest quantity that does is 99999999.9989" {
		t.Errorf("the refusal names the one holdable neighbour, got %q", got)
	}

	// The ordinary case still names both: the 2x4x14's worked refusal.
	bf := Pair{A: q("28"), B: q("3")}
	_, err = ConvertStock(q("10"), "BF", "PCS", bf)
	if inexact, ok = err.(*InexactError); !ok ||
		inexact.Error() != "does not convert exactly into the stocking unit PCS; the nearest quantities that do are 9.9988 and 10.0016" {
		t.Errorf("the ordinary refusal names both neighbours, got %v", err)
	}
	// A negative quantity inside one step of zero names zero above: -0.001
	// BF of the 2x4x14 names -0.0028 and 0, and zero is not holdable.
	_, err = ConvertStock(q("-0.001"), "BF", "PCS", bf)
	if inexact, ok = err.(*InexactError); !ok {
		t.Fatalf("-0.001 BF of a 2x4x14 is refused, got %v", err)
	}
	if got := inexact.Error(); got != "does not convert exactly into the stocking unit PCS; the nearest quantity that does is -0.0028" {
		t.Errorf("the negative refusal names the holdable neighbour alone, got %q", got)
	}
}

// TestConvertBetweenUnits covers the general A to B conversion the round trip
// property uses: through the stocking unit, exact or refused.
func TestConvertBetweenUnits(t *testing.T) {
	// A 2x4x8: LF (8, 1) and MBF (1, 187.5) against PCS stock.
	lf := Pair{A: q("8"), B: q("1")}
	mbf := Pair{A: q("1"), B: q("187.5")}
	// 16 LF is 2 PCS is 375 MBF-thousandths... rather: 187.5 PCS = 1 MBF, so
	// 16 LF = 2 PCS = 2/187.5 MBF, not exact; 187.5 PCS = 1 MBF exact.
	got, err := Convert(q("187.5"), "PCS", "MBF", One(), mbf)
	if err != nil || got != q("1") {
		t.Errorf("187.5 PCS = 1 MBF, got %s, %v", got.WireString(), err)
	}
	// LF to MBF on the fixed 8 footer: 8 LF = 1 PCS = 1/187.5 MBF, not
	// exact; 1500 LF = 187.5 PCS = 1 MBF exact.
	got, err = Convert(q("1500"), "LF", "MBF", lf, mbf)
	if err != nil || got != q("1") {
		t.Errorf("1500 LF = 1 MBF, got %s, %v", got.WireString(), err)
	}
	if _, err := Convert(q("1"), "LF", "MBF", lf, mbf); err == nil {
		t.Errorf("1 LF does not convert exactly into MBF")
	}
}
