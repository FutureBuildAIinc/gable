// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// seedCatalogue is the seed of ADR 0006 section 2.2, as the catalogue the
// set arithmetic reads it.
func seedCatalogue() map[string]CatalogueUnit {
	mk := func(code string, dim Dimension, stdUnit, stdRef string) CatalogueUnit {
		cu := CatalogueUnit{Code: code, Dimension: dim, IsActive: true}
		if stdUnit != "" {
			cu.StdUnitQty, cu.StdRefQty, cu.HasStdSize = q(stdUnit), q(stdRef), true
		}
		return cu
	}
	out := map[string]CatalogueUnit{}
	for _, cu := range []CatalogueUnit{
		mk("EA", DimCount, "1", "1"), mk("PCS", DimCount, "1", "1"),
		mk("PAIR", DimCount, "1", "2"), mk("DOZ", DimCount, "1", "12"),
		mk("C", DimCount, "1", "100"), mk("M", DimCount, "1", "1000"),
		mk("SET", DimCount, "", ""), mk("BOX", DimCount, "", ""),
		mk("CTN", DimCount, "", ""), mk("BAG", DimCount, "", ""),
		mk("BUNDLE", DimCount, "", ""), mk("RL", DimCount, "", ""),
		mk("LF", DimLength, "1", "1"), mk("SF", DimArea, "1", "1"),
		mk("SQ", DimArea, "1", "100"), mk("CF", DimVolume, "1", "1"),
		mk("CY", DimVolume, "1", "27"), mk("GAL", DimVolume, "576", "77"),
		mk("LBS", DimWeight, "1", "1"), mk("CWT", DimWeight, "1", "100"),
		mk("TON", DimWeight, "1", "2000"), mk("BF", DimBoardMeasure, "1", "1"),
		mk("MBF", DimBoardMeasure, "1", "1000"),
	} {
		out[cu.Code] = cu
	}
	return out
}

func rowIn(uom string, sell, purchase, price bool) SetInput {
	return SetInput{UOM: uom, Sell: sell, Purchase: purchase, Price: price}
}

func rowPair(uom string, unit, stock string, sell, purchase, price bool) SetInput {
	return SetInput{UOM: uom, UnitQty: q(unit), StockQty: q(stock), HasPair: true,
		Sell: sell, Purchase: purchase, Price: price}
}

// findRow answers the completed row of a unit.
func findRow(rows []SetRow, uom string) (SetRow, bool) {
	for _, r := range rows {
		if r.UOM == uom {
			return r, true
		}
	}
	return SetRow{}, false
}

// TestResolveSetDerivations covers each derivation rule of section 3.2: the
// 2x4x8 and the 2x4 random length sets of the record read back exactly.
func TestResolveSetDerivations(t *testing.T) {
	cat := seedCatalogue()

	// The 2x4x8 of the record's PUT body: stocked in PCS, its LF row derives
	// through the piece (8 LF = 1 PCS), its BF row through the cross
	// section (1, 0.1875) and its MBF row through the standard 1000 to 1
	// (1, 187.5). No pair is sent at all.
	facts := SetFacts{StockUOM: "PCS", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"),
		HasBoardLength: true, BoardLengthFT: q("8")}
	inputs := []SetInput{
		rowIn("PCS", true, false, true),
		rowIn("LF", true, false, false),
		rowIn("BF", false, false, true),
		rowIn("MBF", false, true, true),
	}
	rows, warnings, err := ResolveSet(inputs, facts, cat)
	if err != nil {
		t.Fatalf("the 2x4x8 set resolves: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("the 2x4x8 set has no warnings, got %v", warnings)
	}
	want := map[string][2]httpx.Quantity{
		"PCS": {q("1"), q("1")}, "LF": {q("8"), q("1")},
		"BF": {q("1"), q("0.1875")}, "MBF": {q("1"), q("187.5")},
	}
	for uom, w := range want {
		r, ok := findRow(rows, uom)
		if !ok {
			t.Fatalf("%s is missing from the resolved set", uom)
		}
		if r.UnitQty != w[0] || r.StockQty != w[1] {
			t.Errorf("%s row is (%s, %s); want (%s, %s)", uom,
				r.UnitQty.WireString(), r.StockQty.WireString(), w[0].WireString(), w[1].WireString())
		}
	}

	// The 2x4 random length set: stocked in LF, BF is (1, 1.5) through the
	// cross section and MBF (1, 1500) through the standard size.
	facts = SetFacts{StockUOM: "LF", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"), RandomLength: true}
	inputs = []SetInput{
		rowIn("LF", true, false, true),
		rowIn("BF", false, false, true),
		rowIn("MBF", false, true, true),
	}
	rows, _, err = ResolveSet(inputs, facts, cat)
	if err != nil {
		t.Fatalf("the random length set resolves: %v", err)
	}
	want = map[string][2]httpx.Quantity{
		"LF": {q("1"), q("1")}, "BF": {q("1"), q("1.5")}, "MBF": {q("1"), q("1500")},
	}
	for uom, w := range want {
		r, _ := findRow(rows, uom)
		if r.UnitQty != w[0] || r.StockQty != w[1] {
			t.Errorf("random %s row is (%s, %s); want (%s, %s)", uom,
				r.UnitQty.WireString(), r.StockQty.WireString(), w[0].WireString(), w[1].WireString())
		}
	}

	// A sheet in PCS and SF: the SF row is a free pair (1 sheet = 32 SF),
	// and SQ derives through the standard size (1, 3200).
	facts = SetFacts{StockUOM: "PCS"}
	inputs = []SetInput{
		rowIn("PCS", true, true, true),
		rowPair("SF", "1", "32", true, false, false),
		rowIn("SQ", false, false, true),
	}
	rows, _, err = ResolveSet(inputs, facts, cat)
	if err != nil {
		t.Fatalf("the sheet set resolves: %v", err)
	}
	if r, _ := findRow(rows, "SQ"); r.UnitQty != q("1") || r.StockQty != q("3200") {
		t.Errorf("SQ row is (%s, %s); want (1, 3200)", r.UnitQty.WireString(), r.StockQty.WireString())
	}

	// A free pair where no rule applies: a box of 100.
	facts = SetFacts{StockUOM: "EA"}
	inputs = []SetInput{rowIn("EA", true, true, true), rowPair("BOX", "1", "100", true, true, false)}
	rows, warnings, err = ResolveSet(inputs, facts, cat)
	if err != nil {
		t.Fatalf("a free box pair resolves: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("an exact box needs no warning, got %v", warnings)
	}
}

// TestResolveSetRule3ThroughThePieceRow proves rule 3 anchors on a COUNT
// piece row, never on the stocking row (ADR 0006 section 3.2: 1 PCS =
// board_length_ft LF): a 2x4x8 stocked in BF, in LF or in MBF derives its
// other rows through the piece row the set carries, and a sent LF pair
// equal to the derived one is accepted, not refused.
func TestResolveSetRule3ThroughThePieceRow(t *testing.T) {
	cat := seedCatalogue()

	// The same 2x4x8 (2 inches by 4 inches by 8 feet) stocked in each of
	// the four units: the piece row (PCS) is sent, the other rows derive.
	// One piece is 16/3 BF, so against BF stock the PCS row is (0.1875, 1)
	// and the LF row (1.5, 1); against MBF stock the piece row is
	// (187.5, 1) and LF (1500, 1); against LF stock the piece row is (1, 8)
	// and BF (1, 1.5) through the cross section.
	for _, tc := range []struct {
		stock string
		want  map[string][2]httpx.Quantity
	}{
		{"PCS", map[string][2]httpx.Quantity{
			"PCS": {q("1"), q("1")}, "LF": {q("8"), q("1")},
			"BF": {q("1"), q("0.1875")}, "MBF": {q("1"), q("187.5")}}},
		{"BF", map[string][2]httpx.Quantity{
			"BF": {q("1"), q("1")}, "PCS": {q("0.1875"), q("1")},
			"LF": {q("1.5"), q("1")}, "MBF": {q("1"), q("1000")}}},
		{"LF", map[string][2]httpx.Quantity{
			"LF": {q("1"), q("1")}, "PCS": {q("1"), q("8")},
			"BF": {q("1"), q("1.5")}, "MBF": {q("1"), q("1500")}}},
		{"MBF", map[string][2]httpx.Quantity{
			"MBF": {q("1"), q("1")}, "PCS": {q("187.5"), q("1")},
			"LF": {q("1500"), q("1")}, "BF": {q("1000"), q("1")}}},
	} {
		inputs := []SetInput{rowIn(tc.stock, true, false, true)}
		for _, uom := range []string{"PCS", "LF", "BF", "MBF"} {
			if uom == tc.stock {
				continue
			}
			if uom == "PCS" {
				inputs = append(inputs, rowPair("PCS", tc.want["PCS"][0].WireString(), tc.want["PCS"][1].WireString(), true, false, false))
				continue
			}
			inputs = append(inputs, rowIn(uom, uom == "LF", uom == "MBF", uom != "LF"))
		}
		facts := SetFacts{StockUOM: tc.stock, HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"),
			HasBoardLength: true, BoardLengthFT: q("8")}
		rows, _, err := ResolveSet(inputs, facts, cat)
		if err != nil {
			t.Fatalf("the 2x4x8 stocked in %s resolves: %v", tc.stock, err)
		}
		for uom, w := range tc.want {
			r, ok := findRow(rows, uom)
			if !ok {
				t.Fatalf("%s is missing from the %s stocked set", uom, tc.stock)
			}
			if r.UnitQty != w[0] || r.StockQty != w[1] {
				t.Errorf("stocked in %s: %s row is (%s, %s); want (%s, %s)", tc.stock, uom,
					r.UnitQty.WireString(), r.StockQty.WireString(), w[0].WireString(), w[1].WireString())
			}
		}
	}

	// The review's exact case: a 2x4 stocked in BF whose LF pair is sent as
	// the right (1.5, 1) is accepted and stored, never refused for
	// disagreeing with a stocking row derivation of (8, 1).
	facts := SetFacts{StockUOM: "BF", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"),
		HasBoardLength: true, BoardLengthFT: q("8")}
	rows, _, err := ResolveSet([]SetInput{
		rowIn("BF", false, false, true),
		rowPair("PCS", "0.1875", "1", true, false, false),
		rowPair("LF", "1.5", "1", true, false, false),
	}, facts, cat)
	if err != nil {
		t.Fatalf("the right LF pair on a BF stocked 2x4 is accepted: %v", err)
	}
	if r, _ := findRow(rows, "LF"); r.UnitQty != q("1.5") || r.StockQty != q("1") {
		t.Errorf("the sent LF pair is stored as (1.5, 1), got (%s, %s)", r.UnitQty.WireString(), r.StockQty.WireString())
	}
}

// TestResolveSetAgreementRefusals covers the agreement rule: a sent pair
// that disagrees with a derivation that applies to it is a 400 naming
// units[k].unit_qty with the derived pair in the message.
func TestResolveSetAgreementRefusals(t *testing.T) {
	cat := seedCatalogue()
	facts := SetFacts{StockUOM: "LF", HasCrossSection: true, ThicknessIn: q("2"), WidthIn: q("4"), RandomLength: true}

	// BF (1, 1.5) beside MBF (1, 1400): the standard 1000 to 1 gives
	// (1, 1500), so the MBF pair is refused. BF is left to derive from the
	// cross section, so the disagreement names the MBF row the client sent.
	inputs := []SetInput{
		rowIn("LF", true, false, true),
		rowIn("BF", false, false, true),
		rowPair("MBF", "1", "1400", false, true, true),
	}
	_, _, err := ResolveSet(inputs, facts, cat)
	de, ok := err.(*DeriveError)
	if !ok {
		t.Fatalf("the disagreeing MBF pair is refused, got %v", err)
	}
	if de.Field != "units[2].unit_qty" {
		t.Errorf("the refusal names %s; want units[2].unit_qty", de.Field)
	}
	if !strings.Contains(de.Message, "(1, 1500)") {
		t.Errorf("the refusal carries the derived pair: %q", de.Message)
	}

	// An MBF pair off the cross section on a random length 2x4.
	inputs = []SetInput{
		rowIn("LF", true, false, true),
		rowIn("BF", false, false, true),
		rowPair("MBF", "1", "1600", false, true, true),
	}
	_, _, err = ResolveSet(inputs, facts, cat)
	if de, ok := err.(*DeriveError); !ok || de.Field != "units[2].unit_qty" {
		t.Errorf("an MBF pair off the cross section is refused naming units[2].unit_qty, got %v", err)
	}

	// A row no rule reaches and no pair sent is refused naming the pair.
	inputs = []SetInput{
		rowIn("PCS", true, false, true),
		rowIn("BUNDLE", true, false, false),
	}
	_, _, err = ResolveSet(inputs, SetFacts{StockUOM: "PCS"}, cat)
	if de, ok := err.(*DeriveError); !ok || de.Field != "units[1].unit_qty" {
		t.Errorf("an underivable row is refused naming units[1].unit_qty, got %v", err)
	}

	// The stocking row is (1, 1); a sent pair on it is refused.
	inputs = []SetInput{rowPair("PCS", "1", "2", true, true, true)}
	_, _, err = ResolveSet(inputs, SetFacts{StockUOM: "PCS"}, cat)
	if de, ok := err.(*DeriveError); !ok || !strings.Contains(de.Message, "1 and 1") {
		t.Errorf("a stocking row pair is refused, got %v", err)
	}

	// The set must hold the stocking unit's row.
	inputs = []SetInput{rowIn("EA", true, true, true)}
	_, _, err = ResolveSet(inputs, SetFacts{StockUOM: "PCS"}, cat)
	if de, ok := err.(*DeriveError); !ok || !strings.Contains(de.Message, "PCS") {
		t.Errorf("a set without its stocking row is refused, got %v", err)
	}
}

// TestResolveSetWarning covers the paver: a sell row whose one unit does not
// convert exactly into the stocking unit is stored, with a warning naming a
// finer stocking unit that would make every sell row exact.
func TestResolveSetWarning(t *testing.T) {
	cat := seedCatalogue()
	// The 6x9 paver: stocked in PCS and sold by SF, 1 SF = 8/3 PCS, so the
	// pair is (3, 8) and stocking in SF (1 PCS = 0.375 SF) makes both rows
	// exact.
	facts := SetFacts{StockUOM: "PCS"}
	inputs := []SetInput{
		rowIn("PCS", true, false, true),
		rowPair("SF", "3", "8", true, false, false),
	}
	rows, warnings, err := ResolveSet(inputs, facts, cat)
	if err != nil {
		t.Fatalf("the paver set resolves: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the paver set carries one warning, got %v", warnings)
	}
	w := warnings[0]
	if w.Field != "units[1]" {
		t.Errorf("the warning names %s; want units[1]", w.Field)
	}
	for _, want := range []string{"stocking in SF", "1 PCS = 0.375 SF"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("the warning says %q; it should carry %q", w.Message, want)
		}
	}
	// The warning does not refuse: the row is stored, canonically.
	if r, _ := findRow(rows, "SF"); r.UnitQty != q("0.375") || r.StockQty != q("1") {
		t.Errorf("the SF row is stored canonically as (0.375, 1), got (%s, %s)", r.UnitQty.WireString(), r.StockQty.WireString())
	}
}
