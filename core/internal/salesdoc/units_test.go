// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

func testCatalogue() map[string]units.CatalogueUnit {
	out := map[string]units.CatalogueUnit{}
	for _, code := range []string{"PC", "PCS", "LF", "BF", "MBF", "SHT", "SF", "MSF", "CWT", "TON", "LB", "EA"} {
		out[code] = units.CatalogueUnit{Code: code, Dimension: units.DimCount, IsActive: true}
	}
	return out
}

// TestResolveLineUnitsAndExtend_LumberSheetGoodsRebar covers BRIEF-G2-M2-UNITS-TALLIES
// section 4.4:
//   - dimensional lumber sold in PC and LF, priced in MBF, stocked in PC or LF
//     with a random length tally (10x8' + 12x10' + 8x12' + 6x16' = 36 PC =
//     392 LF = 261.3333 BF);
//   - ADR 0006 section 8.2 test #4: 299 PC of 2x4x8 at $485.00/MBF extends to
//     $773.41 (77341 cents) with a single half away from zero round, never
//     $773.58 (77358);
//   - sheet goods sold in SHT, priced in MSF (31.25 SHT = 1 MSF);
//   - rebar sold in PC, priced in CWT and TON.
func TestResolveLineUnitsAndExtend_LumberSheetGoodsRebar(t *testing.T) {
	cat := testCatalogue()

	t.Run("299 PC of 2x4x8 at $485.00/MBF extends to 77341 cents", func(t *testing.T) {
		prodID := uuid.New()
		qty := mustQ(t, "299")
		pc := "PC"
		mbf := "MBF"
		price := mustP(t, "485.00")
		line := Line{
			ID:        uuid.New(),
			LineType:  LineProduct,
			ProductID: &prodID,
			Quantity:  &qty,
			UOM:       &pc,
			PriceUOM:  &mbf,
			UnitPrice: &price,
		}
		set := &ProductUnitSet{
			StockUOM: "PC",
			SaleUOM:  "PC",
			PriceUOM: "MBF",
			Rows: []UnitSetRow{
				{UOM: "PC", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "1"), Sell: true, Purchase: true, Price: true},
				{UOM: "LF", UnitQty: mustQ(t, "8"), StockQty: mustQ(t, "1"), Sell: true, Purchase: false, Price: false},
				{UOM: "BF", UnitQty: mustQ(t, "16"), StockQty: mustQ(t, "3"), Sell: false, Purchase: false, Price: true},
				{UOM: "MBF", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "187.5"), Sell: false, Purchase: true, Price: true},
			},
		}
		v := &httpx.Validator{}
		ok := ResolveLineUnits(v, "lines[0]", &line, LineUnits{
			UOM: "PC", PriceUOM: "MBF", HasQuantity: true,
		}, set, cat)
		if !ok || v.Err() != nil {
			t.Fatalf("ResolveLineUnits failed: ok=%v err=%v", ok, v.Err())
		}
		if line.UOMQty.WireString() != "187.5" || line.PriceUOMQty.WireString() != "1" {
			t.Fatalf("pair = %s/%s, want 187.5/1", line.UOMQty.WireString(), line.PriceUOMQty.WireString())
		}
		if line.StockUOM == nil || *line.StockUOM != "PC" || StockQty(&line) != mustQ(t, "299") {
			t.Fatalf("stock = %v %s, want 299 PC", line.StockUOM, StockQty(&line).WireString())
		}
		if err := ExtendLine(&line); err != nil {
			t.Fatal(err)
		}
		if *line.LineTotal != 77341 {
			t.Fatalf("299 PC of 2x4x8 at $485.00/MBF = %d cents, want 77341", *line.LineTotal)
		}
	})

	t.Run("random length 2x4 tally 10x8 + 12x10 + 8x12 + 6x16 = 392 LF = 261.3333 BF priced per MBF", func(t *testing.T) {
		prodID := uuid.New()
		lf := "LF"
		mbf := "MBF"
		price := mustP(t, "525.00")
		thick := mustQ(t, "2")
		width := mustQ(t, "4")
		line := Line{
			ID:        uuid.New(),
			LineType:  LineProduct,
			ProductID: &prodID,
			UOM:       &lf,
			PriceUOM:  &mbf,
			UnitPrice: &price,
		}
		set := &ProductUnitSet{
			StockUOM:     "LF",
			SaleUOM:      "LF",
			PriceUOM:     "MBF",
			RandomLength: true,
			BoardThick:   &thick,
			BoardWidth:   &width,
			Rows: []UnitSetRow{
				{UOM: "LF", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "1"), Sell: true, Purchase: true, Price: true},
				{UOM: "BF", UnitQty: mustQ(t, "2"), StockQty: mustQ(t, "3"), Sell: false, Purchase: false, Price: true},
				{UOM: "MBF", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "1500"), Sell: false, Purchase: true, Price: true},
			},
		}
		tallyRows := []TallyRow{
			{Pieces: 10, LengthFT: mustQ(t, "8")},
			{Pieces: 12, LengthFT: mustQ(t, "10")},
			{Pieces: 8, LengthFT: mustQ(t, "12")},
			{Pieces: 6, LengthFT: mustQ(t, "16")},
		}
		v := &httpx.Validator{}
		ok := ResolveLineUnits(v, "lines[0]", &line, LineUnits{
			UOM: "LF", PriceUOM: "MBF", TallyRows: tallyRows,
		}, set, cat)
		if !ok || v.Err() != nil {
			t.Fatalf("ResolveLineUnits failed: ok=%v err=%v", ok, v.Err())
		}
		if line.Quantity == nil || *line.Quantity != mustQ(t, "392") {
			t.Fatalf("quantity = %v, want 392 LF", line.Quantity)
		}
		if line.Tally == nil || line.Tally.BoardFeet == nil || *line.Tally.BoardFeet != mustQ(t, "261.3333") {
			t.Fatalf("tally board feet = %+v, want 261.3333", line.Tally)
		}
		if line.UOMQty.WireString() != "1500" || line.PriceUOMQty.WireString() != "1" {
			t.Fatalf("pair = %s/%s, want 1500/1", line.UOMQty.WireString(), line.PriceUOMQty.WireString())
		}
		if StockQty(&line) != mustQ(t, "392") || *line.StockUOM != "LF" {
			t.Fatalf("stock = %s %s, want 392 LF", StockQty(&line).WireString(), *line.StockUOM)
		}
		if err := ExtendLine(&line); err != nil {
			t.Fatal(err)
		}
		// 392 LF * (1 MBF / 1500 LF) * $525.00/MBF = $137.20 = 13720 cents.
		if *line.LineTotal != 13720 {
			t.Fatalf("line total = %d cents, want 13720", *line.LineTotal)
		}
	})

	t.Run("sheet goods sold in SHT and priced in MSF", func(t *testing.T) {
		prodID := uuid.New()
		qty := mustQ(t, "50")
		sht := "SHT"
		msf := "MSF"
		price := mustP(t, "640.00")
		line := Line{
			ID:        uuid.New(),
			LineType:  LineProduct,
			ProductID: &prodID,
			Quantity:  &qty,
			UOM:       &sht,
			PriceUOM:  &msf,
			UnitPrice: &price,
		}
		// 4x8 sheet = 32 SF -> 31.25 SHT = 1 MSF (1000 SF).
		set := &ProductUnitSet{
			StockUOM: "SHT",
			SaleUOM:  "SHT",
			PriceUOM: "MSF",
			Rows: []UnitSetRow{
				{UOM: "SHT", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "1"), Sell: true, Purchase: true, Price: true},
				{UOM: "SF", UnitQty: mustQ(t, "32"), StockQty: mustQ(t, "1"), Sell: true, Purchase: false, Price: true},
				{UOM: "MSF", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "31.25"), Sell: false, Purchase: true, Price: true},
			},
		}
		v := &httpx.Validator{}
		ok := ResolveLineUnits(v, "lines[0]", &line, LineUnits{
			UOM: "SHT", PriceUOM: "MSF", HasQuantity: true,
		}, set, cat)
		if !ok || v.Err() != nil {
			t.Fatalf("ResolveLineUnits failed: ok=%v err=%v", ok, v.Err())
		}
		if line.UOMQty.WireString() != "31.25" || line.PriceUOMQty.WireString() != "1" {
			t.Fatalf("pair = %s/%s, want 31.25/1", line.UOMQty.WireString(), line.PriceUOMQty.WireString())
		}
		if err := ExtendLine(&line); err != nil {
			t.Fatal(err)
		}
		// 50 SHT = 1600 SF = 1.6 MSF * $640/MSF = $1,024.00 = 102400 cents.
		if *line.LineTotal != 102400 {
			t.Fatalf("50 SHT at $640.00/MSF = %d cents, want 102400", *line.LineTotal)
		}
	})

	t.Run("rebar sold in PC and priced in CWT and TON", func(t *testing.T) {
		// #4 rebar 20' bar weighs 13.36 lb (0.668 lb/ft * 20 ft):
		// 100 LB (1 CWT) = 100 / 13.36 PC -> 1336 LB = 100 PC, so 100 PC = 13.36 CWT (pair 250 PC = 33.4 CWT, or 125 PC = 16.7 CWT).
		// Simpler exact #5 rebar 20' bar at 20 lb/bar: 5 PC = 1 CWT, 100 PC = 1 TON.
		prodID := uuid.New()
		set := &ProductUnitSet{
			StockUOM: "PC",
			SaleUOM:  "PC",
			PriceUOM: "CWT",
			Rows: []UnitSetRow{
				{UOM: "PC", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "1"), Sell: true, Purchase: true, Price: true},
				{UOM: "CWT", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "5"), Sell: false, Purchase: true, Price: true},
				{UOM: "TON", UnitQty: mustQ(t, "1"), StockQty: mustQ(t, "100"), Sell: false, Purchase: true, Price: true},
			},
		}

		// Sold in PC, priced in CWT ($55.00/CWT): 12 PC = 2.4 CWT = $132.00 = 13200 cents.
		qtyCWT := mustQ(t, "12")
		pc := "PC"
		cwt := "CWT"
		priceCWT := mustP(t, "55.00")
		lineCWT := Line{
			ID: uuid.New(), LineType: LineProduct, ProductID: &prodID,
			Quantity: &qtyCWT, UOM: &pc, PriceUOM: &cwt, UnitPrice: &priceCWT,
		}
		v := &httpx.Validator{}
		if !ResolveLineUnits(v, "lines[0]", &lineCWT, LineUnits{UOM: "PC", PriceUOM: "CWT", HasQuantity: true}, set, cat) {
			t.Fatalf("ResolveLineUnits CWT failed: %v", v.Err())
		}
		if err := ExtendLine(&lineCWT); err != nil {
			t.Fatal(err)
		}
		if *lineCWT.LineTotal != 13200 {
			t.Fatalf("12 PC at $55/CWT = %d cents, want 13200", *lineCWT.LineTotal)
		}

		// Sold in PC, priced in TON ($1050.00/TON): 25 PC = 0.25 TON = $262.50 = 26250 cents.
		qtyTON := mustQ(t, "25")
		ton := "TON"
		priceTON := mustP(t, "1050.00")
		lineTON := Line{
			ID: uuid.New(), LineType: LineProduct, ProductID: &prodID,
			Quantity: &qtyTON, UOM: &pc, PriceUOM: &ton, UnitPrice: &priceTON,
		}
		if !ResolveLineUnits(v, "lines[1]", &lineTON, LineUnits{UOM: "PC", PriceUOM: "TON", HasQuantity: true}, set, cat) {
			t.Fatalf("ResolveLineUnits TON failed: %v", v.Err())
		}
		if err := ExtendLine(&lineTON); err != nil {
			t.Fatal(err)
		}
		if *lineTON.LineTotal != 26250 {
			t.Fatalf("25 PC at $1050/TON = %d cents, want 26250", *lineTON.LineTotal)
		}
	})
}
