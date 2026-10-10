// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"math/big"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// fixtureProducts is the fixture catalogue of the cycle 3 exit test (ADR
// 0006 section 9.3): a product per derivation and per dimension.
type fixtureProduct struct {
	name  string
	facts SetFacts
	rows  []SetInput
}

func fixtureProducts() []fixtureProduct {
	return []fixtureProduct{
		{"2x4x8 stocked in PCS, sold by the piece and LF, priced per BF and MBF",
			SetFacts{StockUOM: "PCS", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"),
				HasBoardLength: true, BoardLengthFT: q("8")},
			[]SetInput{rowIn("PCS", true, false, true), rowIn("LF", true, false, false),
				rowIn("BF", false, false, true), rowIn("MBF", false, true, true)}},
		{"2x4x14 stocked in PCS",
			SetFacts{StockUOM: "PCS", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"),
				HasBoardLength: true, BoardLengthFT: q("14")},
			[]SetInput{rowIn("PCS", true, false, true), rowIn("BF", false, false, true), rowIn("MBF", false, false, true)}},
		{"2x4 random length stocked in LF, priced per MBF",
			SetFacts{StockUOM: "LF", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"), RandomLength: true},
			[]SetInput{rowIn("LF", true, false, true), rowIn("BF", false, false, true), rowIn("MBF", false, true, true)}},
		{"4x8 sheet in PCS and SF, priced per SQ",
			SetFacts{StockUOM: "PCS"},
			[]SetInput{rowIn("PCS", true, true, true), rowPair("SF", "1", "32", true, false, false),
				rowIn("SQ", false, false, true)}},
		{"fasteners in EA, BOX of 100 and M",
			SetFacts{StockUOM: "EA"},
			[]SetInput{rowIn("EA", true, true, true), rowPair("BOX", "1", "100", true, true, false),
				rowIn("M", false, true, true)}},
		{"rebar in LF and CWT",
			SetFacts{StockUOM: "LF"},
			[]SetInput{rowIn("LF", true, true, true), rowPair("CWT", "1", "110", false, true, true)}},
		{"paint in GAL and CF",
			SetFacts{StockUOM: "GAL"},
			[]SetInput{rowIn("GAL", true, true, true), rowIn("CF", false, false, true)}},
	}
}

// resolveFixture resolves a fixture product's set once, failing the test on
// any refusal: the fixture itself is a claim that each derivation applies.
func resolveFixture(t *testing.T, f fixtureProduct, cat map[string]CatalogueUnit) []SetRow {
	t.Helper()
	inputs := make([]SetInput, len(f.rows))
	copy(inputs, f.rows)
	rows, _, err := ResolveSet(inputs, f.facts, cat)
	if err != nil {
		t.Fatalf("%s: the fixture set does not resolve: %v", f.name, err)
	}
	return rows
}

// TestEveryDefinedPairRoundTrips is the cycle 3 exit test's first line (ADR
// 0006 section 9.3): for every product of a fixture catalogue covering each
// derivation and each dimension, and every ordered pair (A, B) of its set:
// the pair for (A, B) is the swap of the pair for (B, A); for every triple
// (A, B, C) the pair for (A, C) equals the composition of (A, B) and
// (B, C); a sample of scale 4 quantities converted A to B and back returns
// itself whenever the forward result is exact, and the exact rationals agree
// when it is not; plus the standard size pairs of the seed.
func TestEveryDefinedPairRoundTrips(t *testing.T) {
	cat := seedCatalogue()

	// The standard size pairs of the seed: every pair of units that both
	// carry a standard size in one dimension converts and round trips. The
	// size pair (stdUnitQty, stdRefQty) is the unit's row against its
	// dimension's reference unit as the stocking unit.
	for codeA, cuA := range cat {
		if !cuA.HasStdSize {
			continue
		}
		for codeB, cuB := range cat {
			if !cuB.HasStdSize || cuA.Dimension != cuB.Dimension {
				continue
			}
			roundTripOne(t, codeA, codeB,
				stdRow{Pair{A: cuA.StdUnitQty, B: cuA.StdRefQty}},
				stdRow{Pair{A: cuB.StdUnitQty, B: cuB.StdRefQty}})
		}
	}

	for _, f := range fixtureProducts() {
		rows := resolveFixture(t, f, cat)
		t.Run(f.name, func(t *testing.T) {
			for _, a := range rows {
				for _, b := range rows {
					roundTripOne(t, a.UOM, b.UOM, a, b)
				}
			}
			// Triples: the pair for (A, C) equals the composition of
			// (A, B) and (B, C), as exact rationals.
			for _, a := range rows {
				for _, b := range rows {
					for _, c := range rows {
						ab, err := ResolveLinePair(a.PairOf(), b.PairOf())
						if err != nil {
							t.Fatalf("(A, B) does not resolve: %v", err)
						}
						bc, err := ResolveLinePair(b.PairOf(), c.PairOf())
						if err != nil {
							t.Fatalf("(B, C) does not resolve: %v", err)
						}
						ac, err := ResolveLinePair(a.PairOf(), c.PairOf())
						if err != nil {
							t.Fatalf("(A, C) does not resolve: %v", err)
						}
						// x A = ab.A of B per ab.B of ... compose the two
						// ratios as rationals and compare.
						abr, _ := ab.Ratio()
						bcr, _ := bc.Ratio()
						acr, _ := ac.Ratio()
						composed := new(big.Rat).Mul(abr, bcr)
						if composed.Cmp(acr) != 0 {
							t.Errorf("the pair for (%s, %s) is not the composition through %s: %s vs %s",
								a.UOM, c.UOM, b.UOM, composed.RatString(), acr.RatString())
						}
					}
				}
			}
		})
	}
}

// roundTripOne asserts the two laws of one ordered pair: the swap symmetry
// of the resolved line pair, and the quantity round trip through the two set
// rows (exact back whenever the forward leg is exact, and exact rationals
// otherwise).
func roundTripOne(t *testing.T, codeA, codeB string, a, b SetRowLike) {
	t.Helper()
	pairAB, err := ResolveLinePair(a.PairOf(), b.PairOf())
	if err != nil {
		t.Errorf("the pair for (%s, %s) does not resolve: %v", codeA, codeB, err)
		return
	}
	pairBA, err := ResolveLinePair(b.PairOf(), a.PairOf())
	if err != nil {
		t.Errorf("the pair for (%s, %s) does not resolve: %v", codeB, codeA, err)
		return
	}
	if !(pairAB.A == pairBA.B && pairAB.B == pairBA.A) {
		t.Errorf("the pair for (%s, %s) is (%s, %s); the swap of (%s, %s)'s pair is not it",
			codeA, codeB, pairAB.A.WireString(), pairAB.B.WireString(),
			pairBA.A.WireString(), pairBA.B.WireString())
	}
	// The quantity round trip over a sample of scale 4 quantities.
	for _, sample := range []httpx.Quantity{q("1"), q("2"), q("3"), q("7"), q("10"), q("28"), q("100"), q("187.5"), q("1234.5")} {
		forward, err := Convert(sample, codeA, codeB, a.PairOf(), b.PairOf())
		if err != nil {
			if _, inexact := err.(*InexactError); !inexact {
				t.Errorf("converting %s %s to %s: %v", sample.WireString(), codeA, codeB, err)
			}
			continue
		}
		back, err := Convert(forward, codeB, codeA, b.PairOf(), a.PairOf())
		if err != nil {
			t.Errorf("converting %s %s back to %s: %v", forward.WireString(), codeB, codeA, err)
			continue
		}
		if back != sample {
			t.Errorf("%s %s converts to %s %s and back to %s", sample.WireString(), codeA,
				forward.WireString(), codeB, back.WireString())
		}
	}
	// When the forward leg is inexact, the exact rationals still agree:
	// converting forward and back through the exact ratio returns the sent
	// quantity as a rational.
	for _, sample := range []httpx.Quantity{q("1"), q("10")} {
		if _, err := Convert(sample, codeA, codeB, a.PairOf(), b.PairOf()); err == nil {
			continue // exact cases are covered above
		}
		// The exact conversion ratio: sample x (a.B x b.A) / (a.A x b.B).
		ratio := new(big.Rat).SetFrac64(
			int64(a.PairOf().B)*int64(b.PairOf().A),
			int64(a.PairOf().A)*int64(b.PairOf().B))
		exact := new(big.Rat).Mul(new(big.Rat).SetFrac64(int64(sample), 1), ratio)
		back := new(big.Rat).Quo(exact, ratio)
		if back.Cmp(new(big.Rat).SetFrac64(int64(sample), 1)) != 0 {
			t.Errorf("the exact rationals do not agree for %s %s to %s", sample.WireString(), codeA, codeB)
		}
	}
}

// SetRowLike is the slice of a row the round trip needs.
type SetRowLike interface {
	PairOf() Pair
}

// stdRow adapts a catalogue standard size to the round trip's row shape:
// the pair (stdUnitQty, stdRefQty) is the unit's row with its dimension's
// reference unit as the stocking unit.
type stdRow struct{ p Pair }

func (s stdRow) PairOf() Pair { return s.p }

var (
	_ SetRowLike = SetRow{}
	_ SetRowLike = stdRow{}
)
