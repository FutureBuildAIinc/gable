// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"math/big"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// TestTallyArithmetic covers section 4.3's worked cases: the linear feet sum,
// the exact board feet (including 200/3) and the display rounding of R4.3.
func TestTallyArithmetic(t *testing.T) {
	// 10 at 8 and 5 at 14: 150 linear feet, exactly 100 board feet of a 2x4.
	rows := []TallyRow{{Pieces: 10, LengthFT: q("8")}, {Pieces: 5, LengthFT: q("14")}}
	lf, err := LinearFeet(rows)
	if err != nil || lf != q("150") {
		t.Fatalf("linear feet = %s, %v; want 150", lf.WireString(), err)
	}
	bf, err := BoardFeet(lf, q("2"), q("4"))
	if err != nil {
		t.Fatalf("board feet: %v", err)
	}
	if bf.Cmp(big.NewRat(100, 1)) != 0 {
		t.Errorf("board feet = %s; want exactly 100", bf.RatString())
	}
	if d := DisplayBoardFeet(bf); d != q("100") {
		t.Errorf("display board feet = %s; want 100", d.WireString())
	}

	// 10 at 10: 100 linear feet, exactly 200/3 board feet, displayed 66.6667.
	rows = []TallyRow{{Pieces: 10, LengthFT: q("10")}}
	lf, _ = LinearFeet(rows)
	bf, _ = BoardFeet(lf, q("2"), q("4"))
	if bf.Cmp(big.NewRat(200, 3)) != 0 {
		t.Errorf("board feet = %s; want exactly 200/3", bf.RatString())
	}
	if d := DisplayBoardFeet(bf); d != q("66.6667") {
		t.Errorf("display board feet = %s; want 66.6667 (R4.3's one rounding)", d.WireString())
	}

	// A 2x6 tally of 10 at 12 and 6 at 16: 216 linear feet, 216 board feet
	// (a 2x6 is 1 BF per LF: 2 x 6 / 12).
	rows = []TallyRow{{Pieces: 10, LengthFT: q("12")}, {Pieces: 6, LengthFT: q("16")}}
	lf, _ = LinearFeet(rows)
	if lf != q("216") {
		t.Fatalf("linear feet = %s; want 216", lf.WireString())
	}
	bf, _ = BoardFeet(lf, q("2"), q("6"))
	if bf.Cmp(big.NewRat(216, 1)) != 0 {
		t.Errorf("board feet = %s; want exactly 216", bf.RatString())
	}

	// Fractional lengths are exact at scale 4: 2 pieces at 12.5 are 25 LF.
	rows = []TallyRow{{Pieces: 2, LengthFT: q("12.5")}}
	if lf, err = LinearFeet(rows); err != nil || lf != q("25") {
		t.Errorf("linear feet = %s, %v; want 25", lf.WireString(), err)
	}

	// A row without a cross section has no board feet: the extension goes
	// through the line's pair alone (a random length moulding).
	if _, err := BoardFeet(q("150"), q("0"), q("0")); err == nil {
		t.Errorf("a cross section is positive")
	}

	// The extension of the tallied line is Extend(linear_feet, pair, price):
	// 150 LF at 500.00 per MBF with pair (1500, 1) is 5000 cents, and the
	// 200/3 tally's 100 LF is 3333 cents (R4.1 the only rounding).
	if cents, err := httpx.Extend(q("150"), q("1500"), q("1"), 5000000); err != nil || cents != 5000 {
		t.Errorf("150 LF at 500 per MBF extends to %d cents, %v; want 5000", cents, err)
	}
	if cents, err := httpx.Extend(q("100"), q("1500"), q("1"), 5000000); err != nil || cents != 3333 {
		t.Errorf("the 200/3 tally's 100 LF extends to %d cents, %v; want 3333", cents, err)
	}
}
