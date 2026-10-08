// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

func q(s string) httpx.Quantity {
	v, err := httpx.ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return v
}

// TestCanonical covers every case of R2's canonical form: both sides exact
// on either side of 1, one side exact, the integer terms, the smallest scale
// 4 multiple, and out of bound.
func TestCanonical(t *testing.T) {
	cases := []struct {
		name         string
		a, b         httpx.Quantity
		wantA, wantB httpx.Quantity
	}{
		// Rule 1: both r and 1/r exact; the 1 goes on the side that leaves
		// the other at least 1.
		{"8 LF to 1 PCS (r above 1)", q("8"), q("1"), q("8"), q("1")},
		{"1 LF of a 2x4x8 is 0.125 PCS (r below 1)", q("1"), q("8"), q("1"), q("8")},
		{"1 SQ is 100 SF", q("1"), q("100"), q("1"), q("100")},
		{"100 SF is 1 SQ the other way (r above 1)", q("100"), q("1"), q("100"), q("1")},
		{"equal units", q("1"), q("1"), q("1"), q("1")},
		{"a 3 to 2 ratio reads 1.5", q("12"), q("8"), q("1.5"), q("1")},
		// Rule 2: only r exact.
		{"187.5 PCS per MBF", q("187.5"), q("1"), q("187.5"), q("1")},
		{"0.125 PCS per LF of an 8 footer stays oriented", q("1"), q("8"), q("1"), q("8")},
		// Rule 3: only 1/r exact.
		{"1 BF of 2x4 is 1.5 LF", q("1"), q("1.5"), q("1"), q("1.5")},
		{"MBF of a fixed 2x4x8", q("1"), q("187.5"), q("1"), q("187.5")},
		{"3/16 PCS per BF of a 2x4x8", q("16"), q("3"), q("1"), q("0.1875")},
		// Rule 3: neither exact, the integer terms.
		{"28 BF = 3 PCS of a 2x4x14", q("28"), q("3"), q("28"), q("3")},
		{"GAL to CF in lowest terms", q("1728"), q("231"), q("576"), q("77")},
		{"750 and 7 of a 2x4x14 per MBF", q("750"), q("7"), q("750"), q("7")},
		// A client may send any equivalent pair; equal ratios are equal bytes.
		{"56 BF = 6 PCS reduces the same", q("56"), q("6"), q("28"), q("3")},
		{"1500 and 14 of a doubled 2x4x14 ratio", q("1500"), q("14"), q("750"), q("7")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical(tc.a, tc.b)
			if err != nil {
				t.Fatalf("Canonical(%s, %s): %v", tc.a.WireString(), tc.b.WireString(), err)
			}
			if got.A != tc.wantA || got.B != tc.wantB {
				t.Errorf("Canonical(%s, %s) = (%s, %s); want (%s, %s)",
					tc.a.WireString(), tc.b.WireString(),
					got.A.WireString(), got.B.WireString(), tc.wantA.WireString(), tc.wantB.WireString())
			}
			// Idempotent: the canonical form canonicalizes to itself.
			again, err := Canonical(got.A, got.B)
			if err != nil || again != got {
				t.Errorf("Canonical is not idempotent: %v, %v", again, err)
			}
		})
	}
}

func TestCanonicalOutOfBound(t *testing.T) {
	// A single pair of scale 4 sides always finds a form inside the bound
	// (its integer terms are at most the sides themselves, and the record's
	// sub multiples shrink both), so the refusal is the domain of the
	// composed ratios a line or a set resolves: two pairs multiplied
	// together. Two coprime, near bound pairs compose past every form.
	big1 := Pair{A: q("99999999.9989"), B: q("1")}
	big2 := Pair{A: q("1"), B: q("99999999.9961")}
	if _, err := ResolveLinePair(big1, big2); !errors.Is(err, ErrOutOfBound) {
		t.Fatalf("a composed ratio past the bound is refused with ErrOutOfBound, got %v", err)
	}
	// Rule 3's sub multiple ladder: integer terms past the bound that fit
	// once divided by 10. The ratio 500000000 to 1 cannot hold its integer
	// form (500000000 is past 99999999.9999), and the 0.1 multiple
	// (50000000, 0.1) is the largest that fits.
	got, err := Canonical(q("5000000"), q("0.01"))
	if err != nil {
		t.Fatalf("Canonical of the 0.1 multiple: %v", err)
	}
	if got.A != q("50000000") || got.B != q("0.1") {
		t.Errorf("the 0.1 multiple is (%s, %s); want (50000000, 0.1)", got.A.WireString(), got.B.WireString())
	}
}

func TestResolveLinePair(t *testing.T) {
	cases := []struct {
		name         string
		u, p         Pair
		wantA, wantB httpx.Quantity
	}{
		{"2x4x8 by the piece per MBF (ADR 0006 3.3)", Pair{q("1"), q("1")}, Pair{q("1"), q("187.5")}, q("187.5"), q("1")},
		{"2x4x14 by the piece per MBF", Pair{q("1"), q("1")}, Pair{q("7"), q("750")}, q("750"), q("7")},
		{"fasteners by EA per M", Pair{q("1"), q("1")}, Pair{q("1"), q("1000")}, q("1000"), q("1")},
		{"equal units", Pair{q("1"), q("1")}, Pair{q("1"), q("1")}, q("1"), q("1")},
		{"a random length 2x4 in LF per MBF", Pair{q("1"), q("1")}, Pair{q("1"), q("1500")}, q("1500"), q("1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLinePair(tc.u, tc.p)
			if err != nil {
				t.Fatalf("ResolveLinePair: %v", err)
			}
			if got.A != tc.wantA || got.B != tc.wantB {
				t.Errorf("ResolveLinePair = (%s, %s); want (%s, %s)",
					got.A.WireString(), got.B.WireString(), tc.wantA.WireString(), tc.wantB.WireString())
			}
		})
	}
}

// TestStandardSizePairs checks the seed's standard sizes are canonical as
// stored (ADR 0006 2.2: each size is stored in canonical form).
func TestStandardSizePairs(t *testing.T) {
	for _, tc := range []struct {
		code                   string
		stdUnit, stdRef        httpx.Quantity
		wantA, wantB           httpx.Quantity
	}{
		{"EA", q("1"), q("1"), q("1"), q("1")},
		{"PCS", q("1"), q("1"), q("1"), q("1")},
		{"PAIR", q("1"), q("2"), q("1"), q("2")},
		{"DOZ", q("1"), q("12"), q("1"), q("12")},
		{"C", q("1"), q("100"), q("1"), q("100")},
		{"M", q("1"), q("1000"), q("1"), q("1000")},
		{"SQ", q("1"), q("100"), q("1"), q("100")},
		{"CY", q("1"), q("27"), q("1"), q("27")},
		{"GAL", q("576"), q("77"), q("576"), q("77")},
		{"CWT", q("1"), q("100"), q("1"), q("100")},
		{"TON", q("1"), q("2000"), q("1"), q("2000")},
		{"MBF", q("1"), q("1000"), q("1"), q("1000")},
	} {
		got, err := StandardSizePair(tc.stdUnit, tc.stdRef)
		if err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if got.A != tc.wantA || got.B != tc.wantB {
			t.Errorf("%s standard size = (%s, %s); want (%s, %s)", tc.code,
				got.A.WireString(), got.B.WireString(), tc.wantA.WireString(), tc.wantB.WireString())
		}
	}
}
